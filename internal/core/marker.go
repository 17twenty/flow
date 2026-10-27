package core

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Marker kinds. A marker says which of flow's own writings a comment or ticket
// is, so flow can find it again and update it instead of writing a second one.
const (
	// MarkerFollowUp is a ticket flow filed for work it could not do (spec 07 §4).
	MarkerFollowUp = "followup"
	// MarkerPolicy is the pull request gate's comment (spec 09 §2).
	MarkerPolicy = "policy"
	// MarkerHealth is the pinned repository health issue (spec 07 §10).
	MarkerHealth = "health"
	// MarkerFlag is a feature-flag cleanup ticket (spec 07 §6).
	MarkerFlag = "flag"
	// MarkerClosed is flow's note on an issue it closed because the pull
	// request that closes it merged and the forge did not (spec 07 §6).
	MarkerClosed = "closed"
)

// MarkerPrefix begins every marker, in both renderings.
const MarkerPrefix = "flow:"

// Marker identifies something flow wrote, and what it was about: a kind, and
// the fields that make it one particular instance of that kind — the check it
// came from, the repository it concerns, the flag it tracks.
//
// Markers are how flow stays idempotent in places that have no other identity:
// a comment is updated rather than repeated, and a follow-up is found before
// it is filed (spec 11 §1).
type Marker struct {
	// Kind is what sort of writing this is, from the constants above.
	Kind string
	// Fields narrow it to one instance. Keys are rendered in sorted order so
	// that the same marker always renders the same way.
	Fields map[string]string
}

// NewMarker builds a marker from a kind and alternating key/value pairs. An
// odd trailing key is ignored, since a key with no value narrows nothing.
func NewMarker(kind string, pairs ...string) Marker {
	marker := Marker{Kind: kind}
	for i := 0; i+1 < len(pairs); i += 2 {
		if marker.Fields == nil {
			marker.Fields = map[string]string{}
		}
		marker.Fields[pairs[i]] = pairs[i+1]
	}
	return marker
}

// IsZero reports whether the marker identifies nothing.
func (m Marker) IsZero() bool { return m.Kind == "" }

// String renders the marker as `flow:<kind> k=v`, the form that is readable on
// its own line where a tracker strips HTML comments.
func (m Marker) String() string {
	var b strings.Builder
	b.WriteString(MarkerPrefix)
	b.WriteString(m.Kind)
	for _, key := range slices.Sorted(maps.Keys(m.Fields)) {
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(quoteField(m.Fields[key]))
	}
	return b.String()
}

// HTML renders the marker as an HTML comment, which GitHub preserves and does
// not show.
func (m Marker) HTML() string { return "<!-- " + m.String() + " -->" }

// Equal reports whether two markers identify exactly the same thing.
func (m Marker) Equal(other Marker) bool {
	return m.Kind == other.Kind && maps.Equal(m.Fields, other.Fields)
}

// Matches reports whether m identifies something other also identifies: the
// kinds agree and every field m names has that value in other. It is how a
// search for `flow:followup check=config.semantics` finds a marker that also
// carries a repository.
func (m Marker) Matches(other Marker) bool {
	if m.Kind != other.Kind {
		return false
	}
	for key, value := range m.Fields {
		if other.Fields[key] != value {
			return false
		}
	}
	return true
}

// quoteField renders a value, quoting it only when it would otherwise not read
// back as one field. Anything that is not a plain printable run — whitespace
// of any kind, a control character, a quote, a backslash, the separator itself,
// or a byte that is not valid UTF-8 — is quoted, because a marker that does not
// survive a round trip would make flow file a duplicate instead of finding its
// own work.
func quoteField(value string) string {
	if value == "" || !utf8.ValidString(value) || strings.ContainsFunc(value, needsQuoting) {
		return strconv.Quote(value)
	}
	return value
}

// needsQuoting reports whether a rune would not read back as part of a bare
// field.
func needsQuoting(r rune) bool {
	return unicode.IsSpace(r) || !unicode.IsPrint(r) || strings.ContainsRune(`"\'=`, r)
}

// ParseMarker reads one marker from a single line, in either rendering. The
// second result is false for a line that is not a marker.
func ParseMarker(line string) (Marker, bool) {
	text := strings.TrimSpace(line)
	if html, ok := strings.CutPrefix(text, "<!--"); ok {
		trimmed, closed := strings.CutSuffix(strings.TrimSpace(html), "-->")
		if !closed {
			return Marker{}, false
		}
		text = strings.TrimSpace(trimmed)
	}
	rest, ok := strings.CutPrefix(text, MarkerPrefix)
	if !ok {
		return Marker{}, false
	}

	fields, err := splitFields(rest)
	if err != nil || len(fields) == 0 || fields[0] == "" {
		return Marker{}, false
	}
	marker := Marker{Kind: fields[0]}
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return Marker{}, false
		}
		unquoted, err := unquoteField(value)
		if err != nil {
			return Marker{}, false
		}
		if marker.Fields == nil {
			marker.Fields = map[string]string{}
		}
		marker.Fields[key] = unquoted
	}
	return marker, true
}

// splitFields splits a marker body on spaces, keeping quoted values whole.
func splitFields(text string) ([]string, error) {
	var (
		fields  []string
		current strings.Builder
		quoted  bool
		escaped bool
		started bool
	)
	flush := func() {
		if started {
			fields = append(fields, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range text {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && quoted:
			current.WriteRune(r)
			escaped = true
		case r == '"':
			quoted = !quoted
			current.WriteRune(r)
			started = true
		case (r == ' ' || r == '\t') && !quoted:
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoting")
	}
	flush()
	return fields, nil
}

// unquoteField reverses quoteField.
func unquoteField(value string) (string, error) {
	if strings.HasPrefix(value, `"`) {
		return strconv.Unquote(value)
	}
	if strings.ContainsAny(value, `"\`) {
		return "", fmt.Errorf("a bare value cannot hold a quote")
	}
	return value, nil
}

// FindMarker returns the first marker in a body, and where it is. Markers are
// written on their own line, at the end, but flow reads a body it did not
// necessarily write last.
func FindMarker(body string) (Marker, bool) {
	for line := range strings.Lines(body) {
		if marker, ok := ParseMarker(line); ok {
			return marker, true
		}
	}
	return Marker{}, false
}

// HasMarker reports whether a body carries a marker that want matches.
func HasMarker(body string, want Marker) bool {
	for line := range strings.Lines(body) {
		if marker, ok := ParseMarker(line); ok && want.Matches(marker) {
			return true
		}
	}
	return false
}

// Marked reports whether a body carries any of flow's markers, which is how
// flow recognises its own writing where it has no particular marker in mind:
// a comment flow wrote is not somebody explaining themselves.
func Marked(body string) bool {
	for line := range strings.Lines(body) {
		if _, ok := ParseMarker(line); ok {
			return true
		}
	}
	return false
}

// MarkBody returns body with the marker on its own last line, replacing any
// marker already there. html chooses the rendering: an HTML comment where the
// provider preserves one, a visible line where it does not (spec 11 §1).
func MarkBody(body string, marker Marker, html bool) string {
	rendered := marker.String()
	if html {
		rendered = marker.HTML()
	}
	trimmed := strings.TrimRight(StripMarkers(body), "\n")
	if trimmed == "" {
		return rendered + "\n"
	}
	return trimmed + "\n\n" + rendered + "\n"
}

// StripMarkers returns body without any of flow's markers, for showing a body
// to a person or comparing two bodies that differ only in their marker.
func StripMarkers(body string) string {
	var b strings.Builder
	for line := range strings.Lines(body) {
		if _, ok := ParseMarker(line); ok {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
