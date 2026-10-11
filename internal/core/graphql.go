package core

import (
	"context"
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
	if _, err := c.Do(ctx, req); err != nil {
		return zero, err
	}
	if len(envelope.Errors) > 0 {
		return zero, GraphQLRefusal(envelope.Errors, sentinel)
	}
	return envelope.Data, nil
}

// GraphQLRefusal states an errors array as one refusal, keeping every message:
// a query that failed for two reasons has two things to fix.
func GraphQLRefusal(errs []GraphQLError, sentinel func(errType string) error) error {
	kind := ErrUnavailable
	if len(errs) > 0 && sentinel != nil {
		if mapped := sentinel(errs[0].Type); mapped != nil {
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
