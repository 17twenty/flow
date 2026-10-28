package core

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

// header builds a header the way a server does, so that the keys are
// canonicalised exactly as http.Header.Get expects them.
func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestParseRateLimitReadsWhatTheProviderSaid(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t.Run("absent", func(t *testing.T) {
		rate := ParseRateLimit(http.Header{}, now)
		if rate.Known {
			t.Errorf("rate = %+v, want nothing known", rate)
		}
		if rate.Wait(now) != 0 || rate.Exhausted() {
			t.Error("an unreported budget is neither exhausted nor a wait")
		}
	})
	t.Run("rubbish is ignored", func(t *testing.T) {
		h := header("X-RateLimit-Limit", "many", "X-RateLimit-Reset", "soon")
		if rate := ParseRateLimit(h, now); rate.Known || rate.Limit != 0 {
			t.Errorf("rate = %+v, want nothing known", rate)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		h := header("X-RateLimit-Limit", "60", "X-RateLimit-Remaining", "0")
		rate := ParseRateLimit(h, now)
		if !rate.Exhausted() {
			t.Errorf("rate = %+v, want exhausted", rate)
		}
	})
	t.Run("retry-after in seconds", func(t *testing.T) {
		h := header("Retry-After", " 42 ")
		if rate := ParseRateLimit(h, now); rate.RetryAfter != 42*time.Second {
			t.Errorf("RetryAfter = %v, want 42s", rate.RetryAfter)
		}
	})
	t.Run("retry-after as a date", func(t *testing.T) {
		h := header("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
		if rate := ParseRateLimit(h, now); rate.RetryAfter != 90*time.Second {
			t.Errorf("RetryAfter = %v, want 90s", rate.RetryAfter)
		}
	})
	t.Run("retry-after in the past is no wait", func(t *testing.T) {
		h := header("Retry-After", now.Add(-time.Hour).Format(http.TimeFormat))
		rate := ParseRateLimit(h, now)
		if !rate.Known || rate.RetryAfter != 0 {
			t.Errorf("rate = %+v, want a known, zero wait", rate)
		}
	})
	t.Run("retry-after beats the reset window", func(t *testing.T) {
		h := header("Retry-After", "30", "X-RateLimit-Reset", "1772366400")
		if got := ParseRateLimit(h, now).Wait(now); got != 30*time.Second {
			t.Errorf("Wait = %v, want the 30s asked for", got)
		}
	})
}

func TestParseLinksReadsEveryRelation(t *testing.T) {
	t.Run("github's single header", func(t *testing.T) {
		h := http.Header{"Link": []string{
			`<https://api.github.com/repos/a/b/pulls?page=2>; rel="next", <https://api.github.com/repos/a/b/pulls?page=5>; rel="last"`,
		}}
		links := ParseLinks(h)
		if links["next"] != "https://api.github.com/repos/a/b/pulls?page=2" {
			t.Errorf("next = %q", links["next"])
		}
		if links["last"] != "https://api.github.com/repos/a/b/pulls?page=5" {
			t.Errorf("last = %q", links["last"])
		}
	})
	t.Run("a comma inside the URL", func(t *testing.T) {
		h := http.Header{"Link": []string{`<https://api/x?labels=a,b&page=2>; rel="next"`}}
		if got := ParseLinks(h)["next"]; got != "https://api/x?labels=a,b&page=2" {
			t.Errorf("next = %q, want the comma kept inside the URL", got)
		}
	})
	t.Run("several headers and unquoted rel", func(t *testing.T) {
		h := http.Header{"Link": []string{`<https://api/1>; rel=first`, `<https://api/9>; rel="prev"`}}
		links := ParseLinks(h)
		if links["first"] != "https://api/1" || links["prev"] != "https://api/9" {
			t.Errorf("links = %v", links)
		}
	})
	t.Run("nothing to read", func(t *testing.T) {
		for _, raw := range []string{"", "not a link", "<https://api/1>", `rel="next"`, "<unterminated; rel=\"next\""} {
			if links := ParseLinks(http.Header{"Link": []string{raw}}); len(links) != 0 {
				t.Errorf("ParseLinks(%q) = %v, want nothing", raw, links)
			}
		}
	})
}

func TestAPIMessageReadsTheProvidersOwnWords(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", ""},
		{"not json", "<html>", ""},
		{"message", `{"message":"Not Found"}`, "Not Found"},
		{"error", `{"error":"invalid_grant"}`, "invalid_grant"},
		{"errors only", `{"errors":[{"message":"first"},{"message":"second"}]}`, "first; second"},
		{"message and field errors", `{"message":"Validation Failed","errors":[{"resource":"Status","field":"state","code":"invalid"}]}`, "Validation Failed: Status.state invalid"},
		{"message on several lines", "{\"message\":\"one\\ntwo\"}", "one two"},
		{"message is not a string", `{"message":{"text":"x"}}`, ""},
		// Jira says it the other way round: a list of messages under its
		// own name, and "errors" holding a map of field to message rather
		// than a list of objects. A reader that declares one shape makes
		// the other body unreadable altogether, so both are tried.
		{"jira messages", `{"errorMessages":["Issue does not exist"],"errors":{}}`, "Issue does not exist"},
		{"jira field errors", `{"errorMessages":[],"errors":{"summary":"must not be empty","resolution":"cannot be set"}}`, "resolution: cannot be set; summary: must not be empty"},
		{"jira says both", `{"errorMessages":["cannot transition"],"errors":{"status":"not in the workflow"}}`, "cannot transition; status: not in the workflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := APIMessage([]byte(tc.body)); got != tc.want {
				t.Errorf("APIMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSecondaryRateLimitedTellsAThrottleFromARefusal(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		body   string
		want   bool
	}{
		{"a plain refusal", http.Header{}, `{"message":"Resource not accessible by integration"}`, false},
		{"budget spent", header("X-RateLimit-Remaining", "0"), "", true},
		{"budget left", header("X-RateLimit-Remaining", "12"), "", false},
		{"asked to wait", header("Retry-After", "60"), "", true},
		{"said so", http.Header{}, `{"message":"You have exceeded a secondary rate limit"}`, true},
		{"abuse detection", http.Header{}, `{"message":"triggered an abuse detection mechanism"}`, true},
		{"primary limit", http.Header{}, `{"message":"API rate limit exceeded for user ID 1"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SecondaryRateLimited(tc.header, []byte(tc.body)); got != tc.want {
				t.Errorf("SecondaryRateLimited = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusSentinelCoversEveryDocumentedMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{200, nil}, {201, nil}, {204, nil},
		{400, ErrValidation}, {401, ErrPermission}, {403, ErrPermission},
		{404, ErrNotFound}, {405, ErrUnsupported}, {409, ErrConflict},
		{410, ErrNotFound}, {412, ErrConflict}, {422, ErrValidation},
		{429, ErrRateLimited}, {500, ErrUnavailable}, {501, ErrUnsupported},
		{502, ErrUnavailable}, {503, ErrUnavailable},
	}
	for _, tc := range cases {
		if got := StatusSentinel(tc.status, http.Header{}, nil); got != tc.want {
			t.Errorf("StatusSentinel(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// Linear reports each budget under a family of its own, with the reset in
// epoch milliseconds, and the budget that ran out is the one whose reset
// matters (#294).
func TestParseRateLimitReadsLinearsBudgets(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).UnixMilli(), 10) }
	t.Run("requests", func(t *testing.T) {
		h := header("X-RateLimit-Requests-Limit", "1500", "X-RateLimit-Requests-Remaining", "0",
			"X-RateLimit-Requests-Reset", ms(90*time.Second))
		rate := ParseRateLimit(h, now)
		if !rate.Known || rate.Limit != 1500 || rate.Remaining != 0 || rate.Resource != "requests" || !rate.Exhausted() {
			t.Errorf("rate = %+v, want the requests budget, spent", rate)
		}
		if got := rate.Wait(now); got != 90*time.Second {
			t.Errorf("Wait = %v, want 90s: the reset is in milliseconds", got)
		}
	})
	t.Run("the spent budget wins", func(t *testing.T) {
		h := header("X-RateLimit-Requests-Limit", "1500", "X-RateLimit-Requests-Remaining", "1400",
			"X-RateLimit-Requests-Reset", ms(time.Hour),
			"X-RateLimit-Complexity-Limit", "250000", "X-RateLimit-Complexity-Remaining", "0",
			"X-RateLimit-Complexity-Reset", ms(30*time.Second))
		rate := ParseRateLimit(h, now)
		if rate.Resource != "complexity" || rate.Wait(now) != 30*time.Second {
			t.Errorf("rate = %+v, want the complexity budget that ran out", rate)
		}
	})
	t.Run("the first budget while none is spent", func(t *testing.T) {
		h := header("X-RateLimit-Requests-Limit", "1500", "X-RateLimit-Requests-Remaining", "1400",
			"X-RateLimit-Complexity-Limit", "250000", "X-RateLimit-Complexity-Remaining", "1000")
		if rate := ParseRateLimit(h, now); rate.Resource != "requests" || rate.Remaining != 1400 {
			t.Errorf("rate = %+v, want the requests budget", rate)
		}
	})
	t.Run("a spent budget keeps no other budget's reset", func(t *testing.T) {
		h := header("X-RateLimit-Requests-Limit", "1500", "X-RateLimit-Requests-Remaining", "1400",
			"X-RateLimit-Requests-Reset", ms(time.Hour),
			"X-RateLimit-Complexity-Limit", "250000", "X-RateLimit-Complexity-Remaining", "0")
		if rate := ParseRateLimit(h, now); rate.Resource != "complexity" || !rate.Reset.IsZero() {
			t.Errorf("rate = %+v, want the complexity budget and no reset: it named none", rate)
		}
	})
	// Each header of the common family is enough on its own to say which
	// family the provider speaks.
	linear := []string{"X-RateLimit-Requests-Limit", "1500", "X-RateLimit-Requests-Remaining", "0",
		"X-RateLimit-Requests-Reset", ms(time.Minute)}
	for _, c := range []struct {
		name   string
		common []string
		want   func(RateLimit) bool
	}{
		{"limit", []string{"X-RateLimit-Limit", "60"}, func(r RateLimit) bool { return r.Limit == 60 }},
		{"remaining", []string{"X-RateLimit-Remaining", "59"}, func(r RateLimit) bool { return r.Remaining == 59 }},
		{"reset", []string{"X-RateLimit-Reset", "1772370000"}, func(r RateLimit) bool { return r.Reset.Equal(time.Unix(1772370000, 0)) }},
	} {
		t.Run("the common family's "+c.name+" wins", func(t *testing.T) {
			rate := ParseRateLimit(header(append(slices.Clone(linear), c.common...)...), now)
			if !c.want(rate) || rate.Resource != "" {
				t.Errorf("rate = %+v, want the X-RateLimit-* family a provider sends for itself", rate)
			}
		})
	}
}
