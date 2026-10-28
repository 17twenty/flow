package core

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Rate-limit header names. GitHub, GitLab and Gitea all answer with this
// family; a provider that answers with none leaves [RateLimit.Known] false.
const (
	headerRateLimit     = "X-RateLimit-Limit"
	headerRateRemaining = "X-RateLimit-Remaining"
	headerRateUsed      = "X-RateLimit-Used"
	headerRateReset     = "X-RateLimit-Reset"
	headerRateResource  = "X-RateLimit-Resource"
	headerRetryAfter    = "Retry-After"
)

// RateLimit is what the provider said about how much budget is left. It is
// carried on every [Response] so that an adapter can slow down before it is
// told to, and so that a refusal can say when the budget returns.
type RateLimit struct {
	// Limit is the ceiling for the window, or 0 when unreported.
	Limit int
	// Remaining is how many requests are left in the window.
	Remaining int
	// Used is how many have been spent, when the provider reports it.
	Used int
	// Reset is when the window rolls over, zero when unreported.
	Reset time.Time
	// Resource names the budget the numbers belong to, for example "core"
	// or "graphql".
	Resource string
	// RetryAfter is how long the provider asked us to wait, zero when it
	// did not ask.
	RetryAfter time.Duration
	// Known is true when the response carried any of the above.
	Known bool
}

// Exhausted reports whether the provider says there is nothing left. It is the
// signal that separates a 403 meaning "slow down" from a 403 meaning "no".
func (r RateLimit) Exhausted() bool { return r.Known && r.Limit > 0 && r.Remaining <= 0 }

// Wait returns how long to wait before trying again: what the provider asked
// for, or the time left until the window resets, whichever it told us.
func (r RateLimit) Wait(now time.Time) time.Duration {
	if r.RetryAfter > 0 {
		return r.RetryAfter
	}
	if !r.Reset.IsZero() && r.Reset.After(now) {
		return r.Reset.Sub(now)
	}
	return 0
}

// ParseRateLimit reads the rate-limit headers of one response. now anchors a
// Retry-After expressed as an HTTP date.
func ParseRateLimit(h http.Header, now time.Time) RateLimit {
	var r RateLimit
	number := func(name string, into *int) bool {
		raw := h.Get(name)
		if raw == "" {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		*into = n
		r.Known = true
		return true
	}
	limit := number(headerRateLimit, &r.Limit)
	remaining := number(headerRateRemaining, &r.Remaining)
	number(headerRateUsed, &r.Used)
	var reset int
	resets := number(headerRateReset, &reset)
	if reset > 0 {
		r.Reset = time.Unix(int64(reset), 0).UTC()
	}
	if resource := h.Get(headerRateResource); resource != "" {
		r.Resource = resource
		r.Known = true
	}
	if after, ok := parseRetryAfter(h.Get(headerRetryAfter), now); ok {
		r.RetryAfter = after
		r.Known = true
	}
	// A provider that answers with the common family has said what it
	// means by it, and Linear's are read only where it is absent.
	if !limit && !remaining && !resets {
		r = linearBudget(h, r)
	}
	return r
}

// linearBudgets are the budgets Linear reports, each under its own family of
// headers rather than the X-RateLimit-* one, with a reset in epoch
// milliseconds rather than seconds. A RATELIMITED answer can mean any of them
// is spent, so the one that is spent is the one whose reset matters (#294).
var linearBudgets = []string{"Requests", "Complexity", "Endpoint-Requests"}

// linearBudget fills base from Linear's headers: the first budget it reports,
// unless a later one is a budget that ran out.
func linearBudget(h http.Header, base RateLimit) RateLimit {
	r, found := base, false
	for _, budget := range linearBudgets {
		prefix := "X-RateLimit-" + budget + "-"
		limit, okLimit := headerInt(h, prefix+"Limit")
		remaining, okRemaining := headerInt(h, prefix+"Remaining")
		reset, okReset := headerInt(h, prefix+"Reset")
		if !okLimit && !okRemaining && !okReset {
			continue
		}
		next := base
		next.Known = true
		next.Limit, next.Remaining = limit, remaining
		next.Resource = strings.ToLower(budget)
		if reset > 0 {
			next.Reset = time.UnixMilli(int64(reset)).UTC()
		}
		if !found || next.Exhausted() {
			r, found = next, true
		}
	}
	return r
}

// headerInt reads one header as a whole number.
func headerInt(h http.Header, name string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	return n, err == nil
}

// parseRetryAfter reads a Retry-After value, which RFC 9110 allows to be a
// number of seconds or an HTTP date.
func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0, true
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if d := when.Sub(now); d > 0 {
		return d, true
	}
	return 0, true
}

// ParseLinks returns the relations of a Link header, so that a caller can page
// without constructing URLs of its own (spec 10 §2). The keys are the rel
// values — "next", "prev", "first", "last" — and the values absolute URLs.
func ParseLinks(h http.Header) map[string]string {
	links := map[string]string{}
	for _, header := range h.Values("Link") {
		for _, part := range splitLinkHeader(header) {
			url, rel, ok := parseLink(part)
			if ok {
				links[rel] = url
			}
		}
	}
	if len(links) == 0 {
		return nil
	}
	return links
}

// splitLinkHeader splits on the commas that separate link-values, ignoring
// commas inside the angle brackets of a URL.
func splitLinkHeader(header string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range header {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, header[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, header[start:])
}

// parseLink reads one `<url>; rel="next"` link-value.
func parseLink(part string) (url, rel string, ok bool) {
	part = strings.TrimSpace(part)
	if !strings.HasPrefix(part, "<") {
		return "", "", false
	}
	end := strings.Index(part, ">")
	if end < 0 {
		return "", "", false
	}
	url = part[1:end]
	for _, param := range strings.Split(part[end+1:], ";") {
		name, value, found := strings.Cut(strings.TrimSpace(param), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "rel") {
			continue
		}
		rel = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if url == "" || rel == "" {
		return "", "", false
	}
	return url, rel, true
}

// StatusSentinel maps an HTTP status to the sentinel error it means, per spec
// 01 §5. The headers and body are read too, because a 403 is the provider's
// answer both to "you may not" and to "you are going too fast", and only the
// rate-limit headers or the message tell them apart.
func StatusSentinel(status int, h http.Header, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status == http.StatusForbidden && SecondaryRateLimited(h, body):
		return ErrRateLimited
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ErrPermission
	case status == http.StatusNotFound, status == http.StatusGone:
		return ErrNotFound
	case status == http.StatusConflict, status == http.StatusPreconditionFailed:
		return ErrConflict
	case status == http.StatusNotImplemented, status == http.StatusMethodNotAllowed:
		return ErrUnsupported
	case status >= 500:
		return ErrUnavailable
	default:
		return ErrValidation
	}
}

// secondaryPhrases are what GitHub says when it is throttling rather than
// refusing. The wording has changed over the years; both forms are matched.
var secondaryPhrases = []string{"secondary rate limit", "abuse detection", "rate limit exceeded"}

// SecondaryRateLimited reports whether a 403 is really a throttle: the budget
// is spent, the provider asked us to wait, or it said so in the message.
func SecondaryRateLimited(h http.Header, body []byte) bool {
	if _, asked := parseRetryAfter(h.Get(headerRetryAfter), time.Time{}); asked {
		return true
	}
	if remaining := h.Get(headerRateRemaining); remaining != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(remaining)); err == nil && n <= 0 {
			return true
		}
	}
	message := strings.ToLower(APIMessage(body))
	if message == "" {
		return false
	}
	for _, phrase := range secondaryPhrases {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// listedErrors reads whatever a provider put under "errors": a list of
// objects, as GitHub writes it, or a map of field to message, as Jira does.
// Anything else says nothing.
func listedErrors(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var listed []struct {
		Message  string `json:"message"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(raw, &listed); err == nil {
		var detail []string
		for _, e := range listed {
			switch {
			case e.Message != "":
				detail = append(detail, e.Message)
			case e.Field != "" && e.Code != "":
				detail = append(detail, e.Resource+"."+e.Field+" "+e.Code)
			}
		}
		return detail
	}
	var byField map[string]string
	if err := json.Unmarshal(raw, &byField); err != nil {
		return nil
	}
	detail := make([]string, 0, len(byField))
	for _, field := range slices.Sorted(maps.Keys(byField)) {
		detail = append(detail, field+": "+byField[field])
	}
	return detail
}

// APIMessage returns the human-readable message of a JSON error body, which
// GitHub, GitLab and Jira all put under "message" or "error". It returns the
// empty string for a body that is not JSON or says nothing, and never returns
// more than one line.
func APIMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var doc struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
		// Errors is a list of objects to GitHub and a map of field to
		// message to Jira, so it is read twice rather than decoded once:
		// one shape declared would make the other body unreadable
		// altogether, and a refusal nobody can read is the one thing this
		// exists to prevent.
		Errors json.RawMessage `json:"errors"`
		// ErrorMessages is Jira's list of what went wrong.
		ErrorMessages []string `json:"errorMessages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	message, _ := doc.Message.(string)
	if message == "" {
		message = doc.Error
	}
	detail := append([]string(nil), doc.ErrorMessages...)
	detail = append(detail, listedErrors(doc.Errors)...)
	if len(detail) > 0 {
		if message == "" {
			message = strings.Join(detail, "; ")
		} else {
			message += ": " + strings.Join(detail, "; ")
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(message, "\n", " "))
}
