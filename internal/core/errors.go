// Package core holds flow's domain types, adapter interfaces, sentinel errors,
// HTTP client, markers, redaction and clock. It depends only on the standard
// library (plus YAML for capability decoding).
package core

import (
	"errors"
	"fmt"
)

// Sentinel errors. Every error flow returns from an adapter or the engine wraps
// one of these, so that [ExitCode] can map it to a documented exit code.
var (
	// ErrNotFound means the addressed resource does not exist.
	ErrNotFound = errors.New("not found")
	// ErrPermission means the credential is missing a required scope or role.
	ErrPermission = errors.New("permission denied")
	// ErrRateLimited means the provider asked us to slow down.
	ErrRateLimited = errors.New("rate limited")
	// ErrConflict means the remote state changed under us, or already exists.
	ErrConflict = errors.New("conflict")
	// ErrUnsupported means the adapter or build cannot do what was asked.
	ErrUnsupported = errors.New("unsupported")
	// ErrNeedsInput means a human must supply a value or make a decision.
	ErrNeedsInput = errors.New("needs input")
	// ErrValidation means the inputs were malformed or contradictory.
	ErrValidation = errors.New("validation failed")
	// ErrUnavailable means the provider could not be reached or kept failing.
	ErrUnavailable = errors.New("unavailable")
)

// Exit codes, per spec 02 §4.
const (
	// ExitOK means the command did what was asked and nothing is outstanding.
	ExitOK = 0
	// ExitFail means the command failed.
	ExitFail = 1
	// ExitActionRequired means a human must act: follow-ups, inputs, drift.
	ExitActionRequired = 2
	// ExitUsage means the invocation or configuration was invalid.
	ExitUsage = 64
	// ExitUnavailable means a dependency was unreachable or rate limited.
	ExitUnavailable = 69
	// ExitNoPerm means the credential lacks a required permission.
	ExitNoPerm = 77
	// ExitInterrupted means the process received SIGINT or SIGTERM.
	ExitInterrupted = 130
)

// ExitCode maps err, or any error it wraps, to its documented exit code.
// A nil error is [ExitOK]; an unrecognised error is [ExitFail].
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrPermission):
		return ExitNoPerm
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrUnavailable):
		return ExitUnavailable
	case errors.Is(err, ErrValidation):
		return ExitUsage
	case errors.Is(err, ErrNeedsInput):
		return ExitActionRequired
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict), errors.Is(err, ErrUnsupported):
		return ExitFail
	default:
		return ExitFail
	}
}

// UserError is an error that knows how to explain itself: what failed, why in
// one line, and how to fix it (principle P10). The Why and Fix fields are
// optional; Err carries the sentinel that decides the exit code.
type UserError struct {
	// What failed, phrased as the action that did not happen.
	What string
	// Why it failed, in one line, in the user's terms.
	Why string
	// Fix is the concrete next action the user can take.
	Fix string
	// Err is the wrapped sentinel, used by [ExitCode] and errors.Is.
	Err error
}

// Errorf builds a [UserError] wrapping sentinel, with a formatted What.
func Errorf(sentinel error, format string, args ...any) *UserError {
	return &UserError{What: fmt.Sprintf(format, args...), Err: sentinel}
}

// WithWhy returns a copy of e explaining why it failed.
func (e *UserError) WithWhy(format string, args ...any) *UserError {
	c := *e
	c.Why = fmt.Sprintf(format, args...)
	return &c
}

// WithFix returns a copy of e telling the user how to fix it.
func (e *UserError) WithFix(format string, args ...any) *UserError {
	c := *e
	c.Fix = fmt.Sprintf(format, args...)
	return &c
}

// Error implements error.
func (e *UserError) Error() string {
	if e.Why == "" {
		return e.What
	}
	return e.What + ": " + e.Why
}

// Unwrap exposes the wrapped sentinel to errors.Is and [ExitCode].
func (e *UserError) Unwrap() error { return e.Err }
