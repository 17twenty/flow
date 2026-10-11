package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
