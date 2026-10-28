package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// GraphQLQuery is one query and the variables it is given.
type GraphQLQuery struct {
	// Query is the document sent to the provider.
	Query string `json:"query"`
	// Variables are its variables, omitted when there are none.
	Variables map[string]any `json:"variables,omitempty"`
}

// GraphQLError is one entry of a GraphQL answer's errors array.
type GraphQLError struct {
	// Type is the provider's own name for what went wrong, which only it
	// can map onto a sentinel.
	Type string `json:"type"`
	// Message is what it said about it.
	Message string `json:"message"`
	// Extensions is where a provider that does not name the failure at the
	// top level puts it. GitHub writes `type` beside the message; Linear
	// writes `extensions: {type, code}` (spec 11 §4). Which of the two
	// a provider uses is not something an adapter should have to unwrap the
	// envelope again to find out.
	Extensions struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"extensions"`
}

// Kind is the provider's name for what went wrong, wherever it put it: the
// top-level type first, then the code an extensions object carries, then its
// coarser type. A sentinel is given this one string, because an adapter maps
// the names its own provider uses and no other.
func (e GraphQLError) Kind() string {
	return cmp.Or(e.Type, e.Extensions.Code, e.Extensions.Type)
}

// GraphQLResponse is the envelope every GraphQL answer arrives in: the data
// asked for, and the errors that happened while producing it. Both can be
// present at once, which is why the envelope is unwrapped rather than decoded
// straight into the data.
type GraphQLResponse[T any] struct {
	Data   T              `json:"data"`
	Errors []GraphQLError `json:"errors"`
}

// GraphQL sends one query and returns its data.
//
// A GraphQL provider answers 200 whatever happened and says what went wrong
// beside the data, so the envelope is part of the wire format rather than
// something [Client] can be left to notice. An answer carrying errors is a
// refusal here, whatever came back alongside it.
//
// req carries the endpoint and whatever headers the adapter puts on all its
// requests; the method, the body and the retry policy belong to GraphQL. A
// query is a read however it is sent, so trying it again cannot do anything
// twice — which POST's default policy, which knows only the method, has to
// assume it could.
//
// sentinel maps the provider's error type onto one of flow's; a nil mapper,
// or one that returns nil, makes every error [ErrUnavailable]. It lives here
// rather than in an adapter because every adapter speaking GraphQL needs the
// same unwrapping, and they may not import each other (spec 01 §2).
func GraphQL[T any](ctx context.Context, c *Client, req Request, q GraphQLQuery, sentinel func(errType string) error) (T, error) {
	var zero T
	if c == nil {
		return zero, Errorf(ErrValidation, "send a GraphQL query").
			WithWhy("no HTTP client was given")
	}
	var envelope GraphQLResponse[T]
	req.Method = http.MethodPost
	req.Body = q
	req.Out = &envelope
	req.Retry = RetryAlways
	req.Throttled = func(body []byte) bool { return graphQLThrottled(body, sentinel) }
	if _, err := c.Do(ctx, req); err != nil {
		return zero, err
	}
	if len(envelope.Errors) > 0 {
		return zero, GraphQLRefusal(envelope.Errors, sentinel)
	}
	return envelope.Data, nil
}

// graphQLThrottled reports whether an answer's errors say the provider is
// throttling. A provider that says so in the envelope rather than the status
// (Linear's RATELIMITED, GitHub's RATE_LIMITED) has to be heard inside the
// retry loop, or the one refusal that waiting would fix is the one that ends
// the run (#294). It asks [GraphQLRefusal] rather than reading the errors
// itself, so that what is retried and what is reported cannot disagree.
func graphQLThrottled(body []byte, sentinel func(errType string) error) bool {
	var envelope struct {
		Errors []GraphQLError `json:"errors"`
	}
	// A body that is not an envelope carries no errors, and no errors are
	// not a throttle: the status has already said whatever it means.
	_ = json.Unmarshal(body, &envelope)
	return errors.Is(GraphQLRefusal(envelope.Errors, sentinel), ErrRateLimited)
}

// GraphQLRefusal states an errors array as one refusal, keeping every message:
// a query that failed for two reasons has two things to fix.
func GraphQLRefusal(errs []GraphQLError, sentinel func(errType string) error) error {
	kind := ErrUnavailable
	if len(errs) > 0 && sentinel != nil {
		if mapped := sentinel(errs[0].Kind()); mapped != nil {
			kind = mapped
		}
	}
	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		if message := strings.TrimSpace(e.Message); message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "the provider gave no reason")
	}
	return Errorf(kind, "ask the provider's GraphQL API").
		WithWhy("%s", strings.Join(messages, "; "))
}
