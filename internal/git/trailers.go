package git

import (
	"context"
	"strings"
)

// Trailer is one key/value pair from the end of a commit message, such as
// `Refs: #12` or `Flag-Added: checkout_v2`.
type Trailer struct {
	// Key is the trailer's name, as written.
	Key string
	// Value is everything after the separator, with folded continuation lines
	// joined into one.
	Value string
}

// Trailers parses the trailer block of a commit message, using git itself.
//
// Where the trailers end and the body begins is a question only git can answer
// consistently: it depends on trailer.* configuration, on which keys the
// repository already uses, and on rules about blank lines that have changed
// over git's life. flow asks rather than guesses (spec 08).
func (r *Repo) Trailers(ctx context.Context, message string) ([]Trailer, error) {
	if strings.TrimSpace(message) == "" {
		return nil, nil
	}
	out, err := output(ctx, r.dir, message, "interpret-trailers", "--parse")
	if err != nil {
		return nil, err
	}

	var trailers []Trailer
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		trailers = append(trailers, Trailer{
			Key:   strings.TrimSpace(key),
			Value: strings.TrimSpace(value),
		})
	}
	return trailers, nil
}

// Lookup returns the value of the last trailer with the given key, and whether
// there was one. Keys are matched without regard to case, as the convention
// treats them. The last wins because a later trailer is an amendment of an
// earlier one.
func Lookup(trailers []Trailer, key string) (string, bool) {
	value, found := "", false
	for _, t := range trailers {
		if strings.EqualFold(t.Key, key) {
			value, found = t.Value, true
		}
	}
	return value, found
}

// Values returns every value recorded under key, in order.
func Values(trailers []Trailer, key string) []string {
	var out []string
	for _, t := range trailers {
		if strings.EqualFold(t.Key, key) {
			out = append(out, t.Value)
		}
	}
	return out
}
