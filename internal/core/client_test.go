package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testClock is a clock the test moves by hand. coretest's FakeClock cannot be
// used here: coretest imports core.
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *testClock) Slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// fixedBackoff makes every wait the same, so a test can assert on them.
func fixedBackoff(d time.Duration) func(int, RateLimit) time.Duration {
	return func(int, RateLimit) time.Duration { return d }
}

// newTestClient returns a client addressing srv with a hand-moved clock.
func newTestClient(t *testing.T, srv *httptest.Server, adjust func(*ClientOptions)) (*Client, *testClock) {
	t.Helper()
	clock := newTestClock()
	opts := ClientOptions{
		BaseURL: srv.URL,
		Token:   "test-token",
		Version: "v1.2.3",
		Clock:   clock,
		Backoff: fixedBackoff(time.Second),
	}
	if adjust != nil {
		adjust(&opts)
	}
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, clock
}

func TestNewClientRefusesABaseThatIsNotAnAbsoluteURL(t *testing.T) {
	for _, base := range []string{"", "api.github.com", "/repos", "::not a url"} {
		if _, err := NewClient(ClientOptions{BaseURL: base}); !errors.Is(err, ErrValidation) {
			t.Errorf("NewClient(%q) error = %v, want ErrValidation", base, err)
		}
	}
}

func TestDoResolvesPathsAgainstTheBaseAndSendsIdentity(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"flow"}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, func(o *ClientOptions) {
		o.Header = http.Header{"Accept": []string{"application/vnd.github+json"}}
	})
	var out struct {
		Name string `json:"name"`
	}
	res, err := client.Do(context.Background(), Request{
		Path:  "repos/17twenty/flow",
		Query: url.Values{"per_page": []string{"100"}},
		Out:   &out,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Name != "flow" {
		t.Errorf("decoded name = %q, want flow", out.Name)
	}
	if res.Status != http.StatusOK || res.Attempts != 1 {
		t.Errorf("Status/Attempts = %d/%d, want 200/1", res.Status, res.Attempts)
	}
	if got.URL.Path != "/repos/17twenty/flow" {
		t.Errorf("path = %q, want /repos/17twenty/flow", got.URL.Path)
	}
	if got.URL.Query().Get("per_page") != "100" {
		t.Errorf("query = %q, want per_page=100", got.URL.RawQuery)
	}
	if want := "flow/v1.2.3 (+https://github.com/17twenty/flow)"; got.Header.Get("User-Agent") != want {
		t.Errorf("User-Agent = %q, want %q", got.Header.Get("User-Agent"), want)
	}
	if got.Header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want the client default", got.Header.Get("Accept"))
	}
}

func TestDoKeepsABasePathAndFollowsAnAbsoluteURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.BaseURL = srv.URL + "/api/v3" })
	if _, err := client.Do(context.Background(), Request{Path: "repos/a/b"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, err := client.Do(context.Background(), Request{Path: srv.URL + "/elsewhere"}); err != nil {
		t.Fatalf("Do absolute: %v", err)
	}
	want := []string{"/api/v3/repos/a/b", "/elsewhere"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestDoSendsAJSONBodyAndReportsLinks(t *testing.T) {
	var body []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = readAll(t, r)
		contentType = r.Header.Get("Content-Type")
		w.Header().Add("Link", `<https://api.example/x?page=2>; rel="next", <https://api.example/x?page=9>; rel="last"`)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, nil)
	res, err := client.Do(context.Background(), Request{
		Method: http.MethodPost,
		Path:   "statuses",
		Body:   map[string]string{"state": "success"},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(body) != `{"state":"success"}` {
		t.Errorf("body = %s", body)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if res.Links["next"] != "https://api.example/x?page=2" || res.Links["last"] != "https://api.example/x?page=9" {
		t.Errorf("links = %v", res.Links)
	}
}

func TestDoRetriesAnIdempotentRequestAndReportsTheAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client, clock := newTestClient(t, srv, nil)
	res, err := client.Do(context.Background(), Request{Path: "repo"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
	if slept := clock.Slept(); len(slept) != 2 {
		t.Errorf("slept %v, want two waits", slept)
	}
}

func TestDoDoesNotRetryAWriteThatFailed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, nil)
	_, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "pulls"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if calls.Load() != 1 {
		t.Errorf("sent %d requests, want exactly one: a POST that may have been performed is not replayed", calls.Load())
	}
}

func TestDoRetriesAWriteThatWasRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	clock := newTestClock()
	client, err := NewClient(ClientOptions{BaseURL: srv.URL, Clock: clock, Backoff: DefaultBackoff})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	res, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "pulls"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", res.Attempts)
	}
	if slept := clock.Slept(); len(slept) != 1 || slept[0] != 7*time.Second {
		t.Errorf("slept %v, want the 7s the provider asked for", slept)
	}
}

func TestDoHonoursRetryNeverAndRetryAlways(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Retries = 2 })

	calls.Store(0)
	if _, err := client.Do(context.Background(), Request{Path: "x", Retry: RetryNever}); err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 1 {
		t.Errorf("RetryNever sent %d requests, want 1", calls.Load())
	}

	calls.Store(0)
	if _, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "x", Retry: RetryAlways}); err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 3 {
		t.Errorf("RetryAlways sent %d requests, want 3", calls.Load())
	}
}

func TestDoReplaysTheBodyOnEveryAttempt(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bodies = append(bodies, string(readAll(t, r)))
		mu.Unlock()
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, nil)
	if _, err := client.Do(context.Background(), Request{
		Method: http.MethodPut,
		Path:   "protection",
		Body:   map[string]any{"contexts": []string{"flow/policy"}},
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0] == "" {
		t.Errorf("bodies = %q, want the same body twice", bodies)
	}
}

func TestDoMapsStatusesToSentinels(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		want   error
	}{
		{"unauthorized", 401, nil, "", ErrPermission},
		{"forbidden", 403, nil, `{"message":"Resource not accessible by integration"}`, ErrPermission},
		{"primary limit", 403, map[string]string{"X-RateLimit-Remaining": "0"}, "", ErrRateLimited},
		{"secondary limit", 403, nil, `{"message":"You have exceeded a secondary rate limit"}`, ErrRateLimited},
		{"too many", 429, nil, "", ErrRateLimited},
		{"missing", 404, nil, "", ErrNotFound},
		{"gone", 410, nil, "", ErrNotFound},
		{"conflict", 409, nil, "", ErrConflict},
		{"precondition", 412, nil, "", ErrConflict},
		{"unprocessable", 422, nil, `{"message":"Validation Failed"}`, ErrValidation},
		{"not implemented", 501, nil, "", ErrUnsupported},
		{"server", 500, nil, "", ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, value := range tc.header {
					w.Header().Set(name, value)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Retries = -1 })
			_, err := client.Do(context.Background(), Request{Path: "x"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			var user *UserError
			if !errors.As(err, &user) || user.Why == "" || user.What == "" {
				t.Fatalf("error %v does not explain itself", err)
			}
			if tc.status != 501 && tc.status != 422 && user.Fix == "" {
				t.Errorf("no fix offered for %d", tc.status)
			}
		})
	}
}

func TestDoExplainsARefusalWithTheProvidersMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Status","field":"state","code":"invalid"}]}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Retries = -1 })
	_, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "statuses/abc"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "Validation Failed: Status.state invalid") {
		t.Errorf("error = %q, want the provider's own message", err)
	}
	if !strings.Contains(err.Error(), "POST ") {
		t.Errorf("error = %q, want the method and URL", err)
	}
}

func TestDoRedactsACredentialInTheQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	secret := "gh" + "p_" + strings.Repeat("A", 36)
	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Retries = -1 })
	_, err := client.Do(context.Background(), Request{
		Path:  "x",
		Query: url.Values{"access_token": []string{secret}},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the credential: %q", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("error = %q, want the value redacted", err)
	}
}

func TestDoReadsRateLimitHeaders(t *testing.T) {
	reset := time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4987")
		w.Header().Set("X-RateLimit-Used", "13")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client, clock := newTestClient(t, srv, nil)
	res, err := client.Do(context.Background(), Request{Path: "x"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	rate := res.Rate
	if !rate.Known || rate.Limit != 5000 || rate.Remaining != 4987 || rate.Used != 13 || rate.Resource != "core" {
		t.Fatalf("rate = %+v", rate)
	}
	if !rate.Reset.Equal(reset) {
		t.Errorf("Reset = %v, want %v", rate.Reset, reset)
	}
	if rate.Exhausted() {
		t.Error("4987 remaining is not exhausted")
	}
	if got := rate.Wait(clock.Now()); got != time.Hour {
		t.Errorf("Wait = %v, want an hour until the window resets", got)
	}
}

func TestDoValidatesWithAnETagAndServesTheCachedBody(t *testing.T) {
	var calls atomic.Int32
	var conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("ETag", `W/"abc"`)
		if r.Header.Get("If-None-Match") == `W/"abc"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`{"name":"flow"}`))
	}))
	defer srv.Close()

	cache := NewMemoryCache()
	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Cache = cache })

	var first, second struct {
		Name string `json:"name"`
	}
	if _, err := client.Do(context.Background(), Request{Path: "repo", Out: &first}); err != nil {
		t.Fatalf("first Do: %v", err)
	}
	res, err := client.Do(context.Background(), Request{Path: "repo", Out: &second})
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if conditional.Load() != 1 {
		t.Fatal("the second request was not conditional")
	}
	if !res.FromCache || res.Status != http.StatusOK {
		t.Errorf("FromCache/Status = %v/%d, want true/200", res.FromCache, res.Status)
	}
	if second.Name != first.Name || second.Name != "flow" {
		t.Errorf("decoded %q from the cache, want %q", second.Name, first.Name)
	}
	if cache.Len() != 1 {
		t.Errorf("cache holds %d entries, want 1", cache.Len())
	}
}

func TestCacheEntriesAreKeyedByTheCredential(t *testing.T) {
	var conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			conditional.Add(1)
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"name":"` + r.Header.Get("Authorization") + `"}`))
	}))
	defer srv.Close()

	cache := NewMemoryCache()
	mine, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Cache = cache; o.Token = "mine" })
	yours, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Cache = cache; o.Token = "yours" })

	if _, err := mine.Do(context.Background(), Request{Path: "repo"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, err := yours.Do(context.Background(), Request{Path: "repo"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if conditional.Load() != 0 {
		t.Error("one credential sent another's ETag")
	}
	if cache.Len() != 2 {
		t.Errorf("cache holds %d entries, want one per credential", cache.Len())
	}
	if mine.Fingerprint() == yours.Fingerprint() {
		t.Error("two credentials fingerprinted the same")
	}
	if strings.Contains(CacheKey(mine.Fingerprint(), "GET", srv.URL), "mine") {
		t.Error("the cache key spells out the credential")
	}
}

func TestNoCacheAndWritesAreNeverCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cache := NewMemoryCache()
	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Cache = cache })
	if _, err := client.Do(context.Background(), Request{Path: "x", NoCache: true}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "x"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if cache.Len() != 0 {
		t.Errorf("cache holds %d entries, want none", cache.Len())
	}
}

func TestDoRefusesANonLoopbackHostInATest(t *testing.T) {
	client, err := NewClient(ClientOptions{BaseURL: "https://api.github.com", Retries: -1})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Do(context.Background(), Request{Path: "repos/17twenty/flow"})
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("error = %v, want the transport guard to refuse", err)
	}
	if !strings.Contains(err.Error(), "api.github.com") {
		t.Errorf("error = %q, want the host named", err)
	}
}

// AllowHost narrows and never widens: a caller that allows everything still
// cannot reach a provider from inside a test binary, so a fake nobody wired
// up fails the test rather than talking to the real thing (spec 14).
func TestAllowHostCanOnlyNarrow(t *testing.T) {
	client, err := NewClient(ClientOptions{
		BaseURL:   "https://api.github.com",
		Retries:   -1,
		AllowHost: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Do(context.Background(), Request{Path: "repos/17twenty/flow"})
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("error = %v, want the test guard to refuse anyway", err)
	}
	var user *UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, LiveEnv) {
		t.Errorf("the refusal does not say which guard refused: %+v", user)
	}
}

// And a caller's own refusal says so in its own terms: this client was built
// to address one host, and a request that left it would carry the credential.
func TestAllowHostNarrowsTheGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var asked []string
	client, _ := newTestClient(t, srv, func(o *ClientOptions) {
		o.AllowHost = func(host string) bool { asked = append(asked, host); return false }
	})
	if _, err := client.Do(context.Background(), Request{Path: "x"}); !errors.Is(err, ErrPermission) {
		t.Fatalf("error = %v, want ErrPermission", err)
	}
	if len(asked) != 1 {
		t.Errorf("AllowHost asked %v, want exactly the one host", asked)
	}
	_, err := client.Do(context.Background(), Request{Path: "x"})
	var user *UserError
	if !errors.As(err, &user) || !strings.Contains(user.Why, "only address the host it was built for") {
		t.Errorf("the refusal reads as the test guard's rather than the caller's: %+v", user)
	}
}

func TestLoopbackNamesThisMachine(t *testing.T) {
	for _, host := range []string{"localhost", "api.localhost", "127.0.0.1", "127.9.9.9", "::1", "[::1]"} {
		if !Loopback(host) {
			t.Errorf("Loopback(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"api.github.com", "10.0.0.1", "", "notlocalhost"} {
		if Loopback(host) {
			t.Errorf("Loopback(%q) = true, want false", host)
		}
	}
}

func TestDoReturnsTheContextErrorWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client, _ := newTestClient(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := client.Do(ctx, Request{Path: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled so the root can still report an interrupt", err)
	}
}

func TestDoTimesOutOneAttemptAndSaysSo(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	client, _ := newTestClient(t, srv, func(o *ClientOptions) {
		o.Timeout = 20 * time.Millisecond
		o.Retries = -1
	})
	_, err := client.Do(context.Background(), Request{Path: "x"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "20ms") {
		t.Errorf("error = %q, want the timeout named", err)
	}
}

func TestDoBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := inFlight.Add(1)
		for {
			high := peak.Load()
			if now <= high || peak.CompareAndSwap(high, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Concurrency = 2 })
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Do(context.Background(), Request{Path: "x"}); err != nil {
				t.Errorf("Do: %v", err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Errorf("peak concurrency = %d, want at most 2", peak.Load())
	}
}

func TestDoDecodesIntoBytesAndToleratesAnEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/empty" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("workflow: yaml"))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, nil)
	var raw []byte
	if _, err := client.Do(context.Background(), Request{Path: "file", Out: &raw}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(raw) != "workflow: yaml" {
		t.Errorf("raw = %q", raw)
	}
	var out map[string]string
	if _, err := client.Do(context.Background(), Request{Path: "empty", Out: &out}); err != nil {
		t.Fatalf("Do empty: %v", err)
	}
}

func TestDoReportsABodyItCannotRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv, nil)
	var out map[string]string
	_, err := client.Do(context.Background(), Request{Path: "x", Out: &out})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestDefaultBackoffGrowsAndObeysTheProvider(t *testing.T) {
	for attempt := range 4 {
		got := DefaultBackoff(attempt, RateLimit{})
		ceiling := DefaultBackoffBase << attempt
		if got <= 0 || got > ceiling {
			t.Errorf("DefaultBackoff(%d) = %v, want within (0, %v]", attempt, got, ceiling)
		}
	}
	if got := DefaultBackoff(0, RateLimit{RetryAfter: 30 * time.Second}); got != 30*time.Second {
		t.Errorf("DefaultBackoff with Retry-After = %v, want 30s", got)
	}
	if got := DefaultBackoff(40, RateLimit{RetryAfter: time.Hour}); got != MaxBackoff {
		t.Errorf("DefaultBackoff = %v, want it capped at %v", got, MaxBackoff)
	}
}

func TestUserAgentNamesFlowAndItsVersion(t *testing.T) {
	if got := UserAgent("v1.0.0"); got != "flow/v1.0.0 (+https://github.com/17twenty/flow)" {
		t.Errorf("UserAgent = %q", got)
	}
	if got := UserAgent(""); !strings.HasPrefix(got, "flow/dev ") {
		t.Errorf("UserAgent(\"\") = %q, want a dev build", got)
	}
}

// readAll drains a request body in a handler.
func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	raw := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for {
		n, err := r.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			return raw
		}
	}
}
