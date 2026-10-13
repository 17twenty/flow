package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Client defaults, per spec 01 §5.
const (
	// DefaultTimeout bounds one attempt, not the whole call.
	DefaultTimeout = 30 * time.Second
	// DefaultRetries is how many times an idempotent request is tried again
	// after the first attempt fails.
	DefaultRetries = 3
	// DefaultConcurrency bounds the requests one client has in flight.
	DefaultConcurrency = 4
	// DefaultBackoffBase is the first backoff; it doubles per attempt.
	DefaultBackoffBase = 500 * time.Millisecond
	// MaxBackoff caps one wait, however long the provider asks for. A longer
	// wait is reported as rate limiting instead of endured.
	MaxBackoff = 60 * time.Second
	// MaxResponseBytes caps a response body, so that a provider cannot make
	// flow allocate without bound.
	MaxResponseBytes = 32 << 20
	// LiveEnv lifts the test transport guard for the live suite (spec 14).
	LiveEnv = "FLOW_LIVE"
)

// UserAgent returns the User-Agent flow identifies itself with (spec 01 §5).
func UserAgent(version string) string {
	if version == "" {
		version = "dev"
	}
	return "flow/" + version + " (+https://github.com/17twenty/flow)"
}

// RetryPolicy says whether one request may be tried again.
type RetryPolicy int

const (
	// RetryDefault retries the methods HTTP defines as idempotent, and
	// retries any method that was rate limited, because a request the
	// provider refused to perform cannot have been performed twice.
	RetryDefault RetryPolicy = iota
	// RetryAlways retries whatever the method, for a request the caller
	// knows to be idempotent at the provider.
	RetryAlways
	// RetryNever sends the request exactly once.
	RetryNever
)

// ClientOptions configures a [Client]. Only BaseURL is required.
type ClientOptions struct {
	// BaseURL is the provider's API root; a request path is resolved
	// against it.
	BaseURL string
	// Token is the credential sent as Authorization. It is never logged.
	Token string
	// AuthScheme is the Authorization scheme, "Bearer" by default.
	AuthScheme string
	// Version is flow's version, used in the User-Agent.
	Version string
	// Header holds default headers sent with every request, such as a
	// provider's Accept and API-version headers.
	Header http.Header
	// Timeout bounds one attempt. [DefaultTimeout] when zero.
	Timeout time.Duration
	// Retries is the number of extra attempts. [DefaultRetries] when zero;
	// negative means none.
	Retries int
	// Concurrency bounds requests in flight. [DefaultConcurrency] when zero.
	Concurrency int
	// Backoff returns how long to wait before attempt n (counting from 0),
	// given what the provider said. Defaults to exponential with jitter.
	Backoff func(attempt int, rate RateLimit) time.Duration
	// Clock is where the client reads time and waits. [SystemClock] when nil.
	Clock Clock
	// Cache stores validated responses. Conditional requests are only made
	// when it is set.
	Cache ETagCache
	// Transport is the round tripper to use. [http.DefaultTransport] when
	// nil, which honours SSL_CERT_FILE and HTTPS_PROXY (spec 15 §4).
	Transport http.RoundTripper
	// Logger receives one debug record per attempt. [slog.Default] when nil.
	Logger *slog.Logger
	// AllowHost narrows where this client may speak, and is asked about
	// every request including a redirect. It can only narrow: inside a test
	// binary a non-loopback host is refused whatever it says (spec 14), so
	// a forgotten fake cannot reach a real provider because a caller
	// allowed its host.
	//
	// When nil, every host is allowed outside a test binary and only
	// loopback inside one; the live suite lifts that with FLOW_LIVE=1.
	AllowHost func(host string) bool
}

// Client is the one way flow talks to a provider: a timeout per attempt,
// retries for what may be retried, rate-limit headers read on every response,
// and an ETag cache keyed by the credential's fingerprint (spec 01 §5).
//
// A Client is safe for concurrent use and bounds its own concurrency.
type Client struct {
	base        *url.URL
	token       string
	fingerprint string
	authScheme  string
	userAgent   string
	header      http.Header
	timeout     time.Duration
	retries     int
	backoff     func(int, RateLimit) time.Duration
	clock       Clock
	cache       ETagCache
	log         *slog.Logger
	http        *http.Client
	sem         chan struct{}
}

// Request is one call to a provider. The zero Method is GET.
type Request struct {
	// Method is the HTTP method.
	Method string
	// Path is resolved against the client's BaseURL, or used as given when
	// it is already absolute — which is what makes a Link header followable.
	Path string
	// Query is appended to the resolved URL.
	Query url.Values
	// Header adds to, and overrides, the client's default headers.
	Header http.Header
	// Body is sent as JSON unless it is []byte or string, which are sent
	// verbatim. Nil sends no body.
	Body any
	// Out, when not nil, is the destination for the decoded JSON response.
	Out any
	// Retry overrides the retry policy for this request.
	Retry RetryPolicy
	// NoCache skips the ETag cache for this request.
	NoCache bool
}

// Response is what a provider answered, after retries and cache validation.
type Response struct {
	// Status is the HTTP status of the answer; for a cache hit it is the
	// status of the response that was cached, never 304.
	Status int
	// Header is the header of the answer that was actually received.
	Header http.Header
	// Body is the response body, from the cache when FromCache is true.
	Body []byte
	// Links are the Link header's relations, for pagination.
	Links map[string]string
	// Rate is what the provider said about the remaining budget.
	Rate RateLimit
	// FromCache is true when the provider answered 304 and the body came
	// from the ETag cache.
	FromCache bool
	// Attempts is how many requests were sent, including the one that
	// succeeded.
	Attempts int
}

// NewClient returns a client for one provider.
func NewClient(o ClientOptions) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(o.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, Errorf(ErrValidation, "use %q as an API base URL", o.BaseURL).
			WithWhy("it is not an absolute http or https URL").
			WithFix("give a URL such as https://api.github.com")
	}
	// A base whose path does not end in a slash loses its last segment when
	// a relative path is resolved against it, which is never what was meant.
	if base.Path != "" && !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	c := &Client{
		base:        base,
		token:       o.Token,
		fingerprint: TokenFingerprint(o.Token),
		authScheme:  cmp.Or(o.AuthScheme, "Bearer"),
		userAgent:   UserAgent(o.Version),
		header:      o.Header.Clone(),
		timeout:     o.Timeout,
		retries:     o.Retries,
		backoff:     o.Backoff,
		clock:       o.Clock,
		cache:       o.Cache,
		log:         o.Logger,
	}
	if c.header == nil {
		c.header = http.Header{}
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if o.Retries == 0 {
		c.retries = DefaultRetries
	}
	if c.retries < 0 {
		c.retries = 0
	}
	if c.backoff == nil {
		c.backoff = DefaultBackoff
	}
	if c.clock == nil {
		c.clock = SystemClock{}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	concurrency := o.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	c.sem = make(chan struct{}, concurrency)
	transport := o.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	c.http = &http.Client{Transport: &guardTransport{allow: o.AllowHost, inner: transport}}
	return c, nil
}

// Fingerprint is the credential's cache key prefix. It identifies the token
// without revealing it, and is safe to log.
func (c *Client) Fingerprint() string { return c.fingerprint }

// BaseURL is the API root this client addresses.
func (c *Client) BaseURL() string { return c.base.String() }

// DefaultBackoff waits [DefaultBackoffBase] doubled per attempt, with full
// jitter, unless the provider asked for something longer — in which case it
// waits what was asked, up to [MaxBackoff].
func DefaultBackoff(attempt int, rate RateLimit) time.Duration {
	wait := DefaultBackoffBase << min(max(attempt, 0), 6)
	wait = time.Duration(float64(wait) * (0.5 + 0.5*rand.Float64()))
	if asked := rate.RetryAfter; asked > wait {
		wait = asked
	}
	return min(wait, MaxBackoff)
}

// Do sends one request, retrying what may be retried, and returns the answer.
// The error, when there is one, wraps the sentinel the status means and
// explains itself in what/why/fix form.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	target, err := c.resolve(r)
	if err != nil {
		return nil, err
	}
	body, contentType, err := encodeBody(r.Body)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(cmp.Or(r.Method, http.MethodGet))

	var (
		cacheKey string
		cached   CachedResponse
		haveTag  bool
	)
	if c.cache != nil && method == http.MethodGet && !r.NoCache {
		cacheKey = CacheKey(c.fingerprint, method, target)
		cached, haveTag = c.cache.Load(cacheKey)
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		res, retryable, err := c.attempt(ctx, attempt, method, target, body, contentType, r, cached, haveTag)
		if err == nil {
			res.Attempts = attempt + 1
			if cacheKey != "" {
				c.store(cacheKey, res)
			}
			return res, c.decode(res, r)
		}
		lastErr = err
		if !retryable || attempt >= c.retries || errors.Is(err, context.Canceled) {
			return nil, lastErr
		}
		var rate RateLimit
		if res != nil {
			rate = res.Rate
		}
		if err := c.clock.Sleep(ctx, c.backoff(attempt, rate)); err != nil {
			return nil, err
		}
	}
}

// attempt sends the request once. It reports whether the failure it returns is
// one that trying again could fix.
func (c *Client) attempt(ctx context.Context, attempt int, method, target string, body []byte, contentType string, r Request, cached CachedResponse, haveTag bool) (*Response, bool, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}

	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(attemptCtx, method, target, reader)
	if err != nil {
		return nil, false, Errorf(ErrValidation, "build a %s request to %s", method, Redact(target)).
			WithWhy("%s", err)
	}
	c.setHeaders(req, r, contentType, body)
	if haveTag {
		req.Header.Set("If-None-Match", cached.ETag)
	}

	started := c.clock.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.retryableTransport(method, r.Retry, err), c.transportError(ctx, method, target, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return nil, c.retryableTransport(method, r.Retry, err), c.transportError(ctx, method, target, err)
	}

	out := &Response{
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   raw,
		Links:  ParseLinks(resp.Header),
		Rate:   ParseRateLimit(resp.Header, c.clock.Now()),
	}
	c.log.Debug("http request",
		"method", method, "url", Redact(target), "status", resp.StatusCode,
		"attempt", attempt+1, "duration", c.clock.Now().Sub(started),
		"rate_remaining", out.Rate.Remaining, "credential", c.fingerprint)

	if resp.StatusCode == http.StatusNotModified && haveTag {
		out.Status = cached.Status
		out.Body = cached.Body
		out.FromCache = true
		return out, false, nil
	}
	if sentinel := StatusSentinel(out.Status, out.Header, out.Body); sentinel != nil {
		retryable := c.retryableFailure(method, r.Retry, out, sentinel)
		return out, retryable, c.statusError(method, target, out, sentinel)
	}
	if out.Status == http.StatusNotModified {
		// A 304 without a cached body is unusable; ask again unconditionally.
		return out, false, Errorf(ErrUnavailable, "%s %s", method, Redact(target)).
			WithWhy("the provider answered 304 but flow had nothing cached").
			WithFix("clear the response cache and run the command again")
	}
	return out, false, nil
}

// setHeaders applies the client defaults, then the request's own, then the
// ones only the client can know.
func (c *Client) setHeaders(req *http.Request, r Request, contentType string, body []byte) {
	for name, values := range c.header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	for name, values := range r.Header {
		req.Header.Del(name)
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", c.authScheme+" "+c.token)
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
}

// retryableTransport reports whether a failure that never reached a status may
// be tried again. A host flow is not allowed to address never may: the answer
// will be the same every time, and the guard exists to be noticed.
func (c *Client) retryableTransport(method string, policy RetryPolicy, err error) bool {
	var guard *hostNotAllowedError
	if errors.As(err, &guard) {
		return false
	}
	switch policy {
	case RetryNever:
		return false
	case RetryAlways:
		return true
	default:
		return idempotent(method)
	}
}

// retryableFailure decides whether a failed response is worth another attempt.
// Rate limiting always is, whatever the method: the provider says it did not
// do the work. Anything else only is for an idempotent method.
func (c *Client) retryableFailure(method string, policy RetryPolicy, res *Response, sentinel error) bool {
	if policy == RetryNever {
		return false
	}
	if errors.Is(sentinel, ErrRateLimited) {
		return true
	}
	if !retryableStatus(res.Status) {
		return false
	}
	return policy == RetryAlways || idempotent(method)
}

// idempotent reports whether HTTP defines the method as idempotent, which is
// the only safe default for replaying a request flow did not write itself.
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

// retryableStatus reports whether a status is transient. 501 and 405 are not:
// the provider will keep saying no.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// store writes a successful GET into the ETag cache. A cache that refuses is
// logged and otherwise ignored: it costs a request, not a command.
func (c *Client) store(key string, res *Response) {
	if res.FromCache || res.Status != http.StatusOK {
		return
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		return
	}
	entry := CachedResponse{
		ETag:   etag,
		Status: res.Status,
		Header: res.Header.Clone(),
		Body:   res.Body,
		Stored: c.clock.Now(),
	}
	if err := c.cache.Store(key, entry); err != nil {
		c.log.Debug("response not cached", "error", err.Error(), "credential", c.fingerprint)
	}
}

// decode reads the response body into the caller's destination.
func (c *Client) decode(res *Response, r Request) error {
	if r.Out == nil || len(bytes.TrimSpace(res.Body)) == 0 {
		return nil
	}
	if raw, ok := r.Out.(*[]byte); ok {
		*raw = res.Body
		return nil
	}
	if err := json.Unmarshal(res.Body, r.Out); err != nil {
		return Errorf(ErrUnavailable, "read the provider's answer").
			WithWhy("%s", err).
			WithFix("this is a provider or flow bug; report it with the command you ran")
	}
	return nil
}

// resolve turns a request path into the absolute URL to address.
func (c *Client) resolve(r Request) (string, error) {
	ref, err := url.Parse(r.Path)
	if err != nil {
		return "", Errorf(ErrValidation, "use %q as a request path", r.Path).WithWhy("%s", err)
	}
	target := c.base.ResolveReference(ref)
	if ref.IsAbs() {
		target = ref
	}
	if len(r.Query) > 0 {
		query := target.Query()
		for name, values := range r.Query {
			query.Del(name)
			for _, v := range values {
				query.Add(name, v)
			}
		}
		target.RawQuery = query.Encode()
	}
	return target.String(), nil
}

// encodeBody turns a request body into bytes and the Content-Type to send it
// with. Bytes and strings go verbatim; anything else is JSON.
func encodeBody(body any) ([]byte, string, error) {
	switch v := body.(type) {
	case nil:
		return nil, "", nil
	case []byte:
		return v, "", nil
	case string:
		return []byte(v), "", nil
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, "", Errorf(ErrValidation, "encode the request body").WithWhy("%s", err)
		}
		return raw, "application/json", nil
	}
}

// statusError explains a provider's refusal in what/why/fix form.
func (c *Client) statusError(method, target string, res *Response, sentinel error) error {
	e := Errorf(sentinel, "%s %s", method, Redact(target))
	if message := APIMessage(res.Body); message != "" {
		e = e.WithWhy("the provider answered %d: %s", res.Status, Redact(message))
	} else {
		e = e.WithWhy("the provider answered %d", res.Status)
	}
	switch {
	case errors.Is(sentinel, ErrRateLimited):
		if wait := res.Rate.Wait(c.clock.Now()); wait > 0 {
			return e.WithFix("wait %s and run the command again", wait.Round(time.Second))
		}
		return e.WithFix("wait for the rate limit window to reset and run the command again")
	case errors.Is(sentinel, ErrPermission):
		return e.WithFix("check that the credential has the scopes this provider needs")
	case errors.Is(sentinel, ErrNotFound):
		return e.WithFix("check the name is right and the credential can see it")
	case errors.Is(sentinel, ErrConflict):
		return e.WithFix("re-read the current state and try again")
	case errors.Is(sentinel, ErrUnavailable):
		return e.WithFix("the provider is failing; try again shortly")
	default:
		return e
	}
}

// transportError explains a failure that never reached a status. A cancelled
// context is returned as it is, so that the root can still tell an interrupt
// from an outage.
func (c *Client) transportError(ctx context.Context, method, target string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var guard *hostNotAllowedError
	if errors.As(err, &guard) {
		return guard.err
	}
	e := Errorf(ErrUnavailable, "%s %s", method, Redact(target)).WithWhy("%s", Redact(err.Error()))
	if errors.Is(err, context.DeadlineExceeded) {
		return e.WithWhy("no answer within %s", c.timeout).
			WithFix("check the network and try again")
	}
	return e.WithFix("check the network and try again")
}

// guardTransport refuses a host the client may not address, before any packet
// leaves. It also catches a redirect to a host the first request did not name.
type guardTransport struct {
	allow func(host string) bool
	inner http.RoundTripper
}

// hostNotAllowedError carries the explanation out through http.Client, which
// wraps whatever RoundTrip returns in a *url.Error.
type hostNotAllowedError struct{ err error }

// Error implements error.
func (e *hostNotAllowedError) Error() string { return e.err.Error() }

// Unwrap exposes the carried error.
func (e *hostNotAllowedError) Unwrap() error { return e.err }

// RoundTrip implements http.RoundTripper.
//
// Both allowances have to agree, and they refuse for different reasons: the
// client was built to address one host and this request names another, or
// this is a test binary and the request is leaving the machine. Saying which
// is the difference between a configuration to fix and a fake nobody wired
// up.
func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if g.allow != nil && !g.allow(host) {
		return nil, &hostNotAllowedError{err: Errorf(ErrPermission, "reach %s", host).
			WithWhy("this client may only address the host it was built for, and a request that left it would carry the credential there").
			WithFix("approve %s for this provider if it is yours", host)}
	}
	if !defaultAllowHost(host) {
		return nil, &hostNotAllowedError{err: Errorf(ErrPermission, "reach %s", host).
			WithWhy("flow is not allowed to address that host").
			WithFix("tests must use a loopback server; set %s=1 for the live suite", LiveEnv)}
	}
	return g.inner.RoundTrip(req)
}

// defaultAllowHost allows every host outside a test binary, and only loopback
// inside one, so that a forgotten fake fails the test instead of reaching a
// real provider (spec 14). FLOW_LIVE=1 lifts it for the live suite.
func defaultAllowHost(host string) bool {
	if !testing.Testing() || isLive() {
		return true
	}
	return Loopback(host)
}

// isLive reports whether the live suite is running.
func isLive() bool { return os.Getenv(LiveEnv) == "1" }

// Loopback reports whether a host names this machine. It is the allowance the
// test transport guard grants.
func Loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
