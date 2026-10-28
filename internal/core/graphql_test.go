package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// graphQLServer answers every request with body, recording what was asked.
func graphQLServer(t *testing.T, body string) (*Client, *[]GraphQLQuery) {
	t.Helper()
	var asked []GraphQLQuery
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var q GraphQLQuery
		_ = json.Unmarshal(raw, &q)
		asked = append(asked, q)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	client, _ := newTestClient(t, srv, nil)
	return client, &asked
}

// ghSentinel is a provider's reading of its own error types, as GitHub's is.
func ghSentinel(errType string) error {
	switch errType {
	case "NOT_FOUND":
		return ErrNotFound
	case "FORBIDDEN":
		return ErrPermission
	default:
		return nil
	}
}

type graphQLViewer struct {
	Viewer struct {
		Login string `json:"login"`
	} `json:"viewer"`
}

func TestGraphQLReturnsTheDataInTheEnvelope(t *testing.T) {
	client, asked := graphQLServer(t, `{"data":{"viewer":{"login":"dev"}}}`)
	data, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"},
		GraphQLQuery{Query: "query { viewer { login } }", Variables: map[string]any{"first": 100}}, ghSentinel)
	if err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if data.Viewer.Login != "dev" {
		t.Errorf("login = %q, want dev", data.Viewer.Login)
	}
	if len(*asked) != 1 {
		t.Fatalf("sent %d queries, want one", len(*asked))
	}
	sent := (*asked)[0]
	if sent.Query == "" || sent.Variables["first"] != float64(100) {
		t.Errorf("the provider was sent %+v, want the query and its variables", sent)
	}
}

// A GraphQL provider answers 200 whatever happened. An answer carrying errors
// is a refusal however much data came back with it, because acting on half an
// answer is worse than stopping.
func TestGraphQLRefusesAnAnswerCarryingErrors(t *testing.T) {
	client, _ := graphQLServer(t, `{"data":{"viewer":{"login":"dev"}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository."}]}`)
	_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, ghSentinel)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GraphQL = %v, want the sentinel the provider's error type means", err)
	}
	var user *UserError
	if !errors.As(err, &user) || !strings.Contains(user.Why, "Could not resolve") {
		t.Errorf("the refusal is %q, want it to carry what the provider said", err)
	}
}

func TestGraphQLKeepsEveryReason(t *testing.T) {
	client, _ := graphQLServer(t, `{"errors":[{"type":"FORBIDDEN","message":"first"},{"type":"NOT_FOUND","message":"second"}]}`)
	_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, ghSentinel)
	if !errors.Is(err, ErrPermission) {
		t.Errorf("GraphQL = %v, want the first error's sentinel", err)
	}
	var user *UserError
	if !errors.As(err, &user) {
		t.Fatalf("GraphQL = %v, want a UserError", err)
	}
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(user.Why, want) {
			t.Errorf("the refusal is %q, want it to keep %q: a query that failed twice has two things to fix", user.Why, want)
		}
	}
}

// A type the provider's mapper does not know, and a provider that sends no
// type at all, are both the provider refusing for a reason flow has no name
// for: unavailable, not invalid input, because the query was flow's.
func TestGraphQLHasADefaultForAnErrorTypeNobodyKnows(t *testing.T) {
	for _, body := range []string{
		`{"errors":[{"type":"SOMETHING_NEW","message":"who knows"}]}`,
		`{"errors":[{"message":"who knows"}]}`,
	} {
		client, _ := graphQLServer(t, body)
		_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, ghSentinel)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s = %v, want ErrUnavailable", body, err)
		}
	}
}

func TestGraphQLRefusesWithoutAReasonRatherThanBlankly(t *testing.T) {
	client, _ := graphQLServer(t, `{"errors":[{"type":"NOT_FOUND","message":"   "}]}`)
	_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, ghSentinel)
	var user *UserError
	if !errors.As(err, &user) || user.Why == "" {
		t.Errorf("a provider that said nothing produced %q, want flow to say that itself", err)
	}
}

func TestGraphQLSendsAPostThatMayBeRetried(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Method != http.MethodPost {
			t.Errorf("a query was sent as %s, want POST", r.Method)
		}
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"dev"}}}`)
	}))
	t.Cleanup(srv.Close)
	client, _ := newTestClient(t, srv, func(o *ClientOptions) { o.Backoff = fixedBackoff(0) })

	// A query is a read however it is sent, so a failed attempt must be
	// tried again: POST's default policy, which knows only the method,
	// would stop at the first failure.
	data, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, ghSentinel)
	if err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if attempts != 2 {
		t.Errorf("the provider was asked %d times, want the query to be retried", attempts)
	}
	if data.Viewer.Login != "dev" {
		t.Errorf("login = %q, want dev", data.Viewer.Login)
	}
}

func TestGraphQLWithoutAClientRefuses(t *testing.T) {
	_, err := GraphQL[graphQLViewer](t.Context(), nil, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, nil)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("GraphQL with no client = %v, want ErrValidation", err)
	}
}

func TestGraphQLWithoutAMapperIsUnavailable(t *testing.T) {
	client, _ := graphQLServer(t, `{"errors":[{"type":"NOT_FOUND","message":"gone"}]}`)
	_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("GraphQL without a mapper = %v, want ErrUnavailable", err)
	}
}

// linearSentinel is a provider that names its failures where GitHub does not:
// Linear writes the name under extensions rather than beside the message.
func linearSentinel(errType string) error {
	switch errType {
	case "ENTITY_NOT_FOUND":
		return ErrNotFound
	case "authentication error":
		return ErrPermission
	default:
		return nil
	}
}

// A provider that names the failure under extensions is read, because the
// envelope has two places for it and an adapter should not have to unwrap the
// answer a second time to find out which one its provider uses.
func TestGraphQLReadsTheNameOfAFailureWhereverTheProviderPutIt(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want error
	}{
		{
			name: "beside the message",
			body: `{"errors":[{"type":"NOT_FOUND","message":"gone"}]}`,
			want: ErrNotFound,
		},
		{
			name: "the code under extensions",
			body: `{"errors":[{"message":"gone","extensions":{"type":"invalid input","code":"ENTITY_NOT_FOUND"}}]}`,
			want: ErrNotFound,
		},
		{
			name: "the coarse type under extensions, where there is no code",
			body: `{"errors":[{"message":"no","extensions":{"type":"authentication error"}}]}`,
			want: ErrPermission,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, _ := graphQLServer(t, c.body)
			_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"},
				GraphQLQuery{Query: "query { viewer { login } }"}, func(errType string) error {
					if mapped := ghSentinel(errType); mapped != nil {
						return mapped
					}
					return linearSentinel(errType)
				})
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want it to wrap %v", err, c.want)
			}
		})
	}
}

// The top-level name wins where a provider sends both, because that is the
// one GitHub means and the extensions it also sends are GraphQL's own.
func TestGraphQLErrorPrefersTheNameBesideTheMessage(t *testing.T) {
	var e GraphQLError
	e.Type = "NOT_FOUND"
	e.Extensions.Code = "RATELIMITED"
	if got := e.Kind(); got != "NOT_FOUND" {
		t.Errorf("Kind() = %q, want the provider's own top-level name", got)
	}
}

// throttledServer answers the first throttles requests with a GraphQL throttle
// in the given status, and every request after them with data. header is set
// on the throttles, so a test can say when the budget returns.
func throttledServer(t *testing.T, status, throttles int, header map[string]string) (*httptest.Server, *int) {
	t.Helper()
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", "application/json")
		if asked <= throttles {
			for name, value := range header {
				w.Header().Set(name, value)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"dev"}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// throttleSentinel reads Linear's name for a throttle, as its adapter does.
func throttleSentinel(errType string) error {
	switch errType {
	case "RATELIMITED":
		return ErrRateLimited
	case "INVALID_INPUT":
		return ErrValidation
	default:
		return nil
	}
}

// A provider that says "slow down" in the envelope rather than the status is
// heard inside the retry loop: in a 200, where GraphQL puts every failure, and
// in the 400 Linear has answered it with (#294).
func TestGraphQLWaitsOutAThrottleStatedInTheEnvelope(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, asked := throttledServer(t, status, 1, nil)
			client, clock := newTestClient(t, srv, nil)
			data, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, throttleSentinel)
			if err != nil {
				t.Fatalf("GraphQL after one throttle = %v, want it to wait and ask again", err)
			}
			if data.Viewer.Login != "dev" {
				t.Errorf("login = %q, want the data of the answer that succeeded", data.Viewer.Login)
			}
			if *asked != 2 {
				t.Errorf("the provider was asked %d times, want exactly one retry", *asked)
			}
			if slept := clock.Slept(); len(slept) != 1 {
				t.Errorf("slept %v, want one wait before the retry", slept)
			}
		})
	}
}

// A throttle that does not lift ends in ErrRateLimited after the retries every
// request is allowed, and the waits are the clock's, not the test's.
func TestGraphQLReportsAThrottleThatDoesNotLift(t *testing.T) {
	srv, asked := throttledServer(t, http.StatusBadRequest, 100, nil)
	client, clock := newTestClient(t, srv, nil)
	_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, throttleSentinel)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("GraphQL under a lasting throttle = %v, want ErrRateLimited", err)
	}
	if *asked != 1+DefaultRetries {
		t.Errorf("the provider was asked %d times, want %d", *asked, 1+DefaultRetries)
	}
	if slept := clock.Slept(); len(slept) != DefaultRetries {
		t.Errorf("slept %v, want one wait per retry", slept)
	}
}

// Only a throttle is waited out: an answer refusing the query is refused
// again however long flow waits, and a status that is otherwise retried is
// not made into a throttle by an envelope that says nothing of one.
func TestGraphQLDoesNotRetryARefusalThatIsNotAThrottle(t *testing.T) {
	for _, body := range []string{
		`{"errors":[{"message":"Argument Validation Error","extensions":{"code":"INVALID_INPUT"}}]}`,
		`not json at all`,
	} {
		var asked int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, body)
		}))
		client, _ := newTestClient(t, srv, nil)
		_, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, throttleSentinel)
		srv.Close()
		if err == nil || errors.Is(err, ErrRateLimited) {
			t.Errorf("%s: GraphQL = %v, want a refusal that is not a throttle", body, err)
		}
		if asked != 1 {
			t.Errorf("%s: the provider was asked %d times, want once", body, asked)
		}
	}
}

// Linear says when its budget returns in its own headers, in milliseconds, and
// a throttle is waited out until then rather than for a guessed backoff.
func TestGraphQLWaitsUntilLinearSaysTheBudgetReturns(t *testing.T) {
	now := newTestClock().Now()
	srv, asked := throttledServer(t, http.StatusBadRequest, 1, map[string]string{
		"X-RateLimit-Requests-Limit":     "1500",
		"X-RateLimit-Requests-Remaining": "0",
		"X-RateLimit-Requests-Reset":     strconv.FormatInt(now.Add(20*time.Second).UnixMilli(), 10),
	})
	client, clock := newTestClient(t, srv, func(o *ClientOptions) { o.Backoff = DefaultBackoff })
	if _, err := GraphQL[graphQLViewer](t.Context(), client, Request{Path: "graphql"}, GraphQLQuery{Query: "x"}, throttleSentinel); err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if *asked != 2 {
		t.Errorf("the provider was asked %d times, want 2", *asked)
	}
	if slept := clock.Slept(); len(slept) != 1 || slept[0] != 20*time.Second {
		t.Errorf("slept %v, want the 20s until the budget returns", slept)
	}
}
