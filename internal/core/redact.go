package core

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

// secretKeyRe matches attribute keys whose values are secret by name alone.
var secretKeyRe = regexp.MustCompile(`(?i)(token|secret|password|authorization|api[_-]?key|cookie|session)`)

// valuePattern is a known credential shape and the kind reported when redacted.
type valuePattern struct {
	kind string
	re   *regexp.Regexp
}

// valuePatterns are the credential shapes flow recognises inside free text.
// Longer, more specific prefixes are listed first so they win.
var valuePatterns = []valuePattern{
	{"github_pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"github_token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`)},
	{"gitlab_token", regexp.MustCompile(`glpat-[A-Za-z0-9_-]{16,}`)},
	// Linear spells a personal API key and an OAuth access token
	// differently, and flow accepts both (spec 11 §4), so both are
	// credentials to keep out of a log.
	{"linear_key", regexp.MustCompile(`lin_(?:api|oauth)_[A-Za-z0-9]{16,}`)},
	{"atlassian_token", regexp.MustCompile(`ATATT[A-Za-z0-9_=.-]{16,}`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)},
}

// urlParamRe matches credential-bearing URL query parameters. The parameter
// name may be suffixed onto a longer name, so api_key and private_token match.
var urlParamRe = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_-]*(?:access_token|private_token|token|key)=)([^&\s"']+)`)

// redactedSecret is the replacement used when only the key name, not the value
// shape, told us the value was sensitive.
const redactedSecret = "[REDACTED:secret]"

// Redact removes credentials from s: known token shapes anywhere in the text,
// and the values of credential-bearing URL query parameters. Each removal is
// replaced by "[REDACTED:<kind>]". It is safe to call on any string bound for a
// log, an error message or the terminal.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, p := range valuePatterns {
		s = p.re.ReplaceAllString(s, "[REDACTED:"+p.kind+"]")
	}
	s = urlParamRe.ReplaceAllString(s, "${1}"+redactedSecret)
	return s
}

// RedactKeyValue redacts a single logical key/value pair. A key naming a secret
// discards the value entirely; otherwise the value is scrubbed by [Redact].
func RedactKeyValue(key, value string) string {
	if secretKeyRe.MatchString(key) {
		if value == "" {
			return ""
		}
		return redactedSecret
	}
	return Redact(value)
}

// redactingHandler wraps an [slog.Handler] so that nothing sensitive reaches it.
type redactingHandler struct{ inner slog.Handler }

// NewRedactingHandler returns a handler that scrubs credentials from every
// message and attribute before passing the record to inner. All of flow's
// logging goes through it (spec 15 §2).
func NewRedactingHandler(inner slog.Handler) slog.Handler {
	return &redactingHandler{inner: inner}
}

// Enabled implements slog.Handler.
func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle implements slog.Handler, redacting the message and every attribute.
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

// WithAttrs implements slog.Handler.
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(clean)}
}

// WithGroup implements slog.Handler.
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr scrubs one attribute, recursing into groups.
func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		members := v.Group()
		clean := make([]any, 0, len(members))
		for _, m := range members {
			clean = append(clean, redactAttr(m))
		}
		return slog.Group(a.Key, clean...)
	}
	if secretKeyRe.MatchString(a.Key) {
		return slog.String(a.Key, redactedSecret)
	}
	if v.Kind() == slog.KindString {
		return slog.String(a.Key, Redact(v.String()))
	}
	// Non-string values can still stringify into something sensitive.
	if s := v.String(); strings.ContainsAny(s, "_-.?&") {
		if red := Redact(s); red != s {
			return slog.String(a.Key, red)
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
