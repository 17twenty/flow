package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Duration is a span of time written the way people say it. It accepts Go's
// own syntax plus the two units teams actually use for delivery objectives,
// `d` (24h) and `w` (7d), so that `max_age: 7d` needs no arithmetic from the
// reader (spec 03 §1).
type Duration time.Duration

// Day and Week are the units Go's time package leaves out. A day here is
// exactly 24 hours: flow measures elapsed time, never calendar days, so
// daylight saving never enters into it.
const (
	Day  = Duration(24 * time.Hour)
	Week = 7 * Day
)

// units are the suffixes ParseDuration accepts, longest first where one is a
// prefix of another, so that "ms" never parses as "m".
var units = []struct {
	suffix string
	size   time.Duration
}{
	{"ns", time.Nanosecond},
	{"us", time.Microsecond},
	{"µs", time.Microsecond}, // U+00B5 micro sign
	{"μs", time.Microsecond}, // U+03BC Greek small letter mu
	{"ms", time.Millisecond},
	{"s", time.Second},
	{"m", time.Minute},
	{"h", time.Hour},
	{"d", 24 * time.Hour},
	{"w", 7 * 24 * time.Hour},
}

// ParseDuration parses a duration in Go's syntax extended with d and w.
//
// The input is a signed sequence of decimal numbers each with a unit, such as
// "90m", "2h30m", "7d" or "1w12h". A plain "0" is allowed and means zero;
// every other number needs a unit, because an unqualified number is a question
// rather than a value.
func ParseDuration(s string) (time.Duration, error) {
	rest := s
	neg := false
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		neg = rest[0] == '-'
		rest = rest[1:]
	}
	if rest == "0" {
		return 0, nil
	}
	if rest == "" {
		return 0, invalidDuration(s)
	}

	var total time.Duration
	for rest != "" {
		digits := 0
		for digits < len(rest) && (rest[digits] == '.' || (rest[digits] >= '0' && rest[digits] <= '9')) {
			digits++
		}
		if digits == 0 {
			return 0, invalidDuration(s)
		}
		number, remainder := rest[:digits], rest[digits:]

		unit, size, ok := leadingUnit(remainder)
		if !ok {
			return 0, invalidDuration(s)
		}
		rest = remainder[len(unit):]

		part, err := scale(number, size)
		if err != nil {
			return 0, fmt.Errorf("%q is not a duration: %w", s, err)
		}
		if total > math.MaxInt64-part {
			return 0, fmt.Errorf("%q is not a duration: it is longer than %s", s, time.Duration(math.MaxInt64))
		}
		total += part
	}
	if neg {
		return -total, nil
	}
	return total, nil
}

// leadingUnit matches the unit suffix at the start of s.
func leadingUnit(s string) (string, time.Duration, bool) {
	for _, u := range units {
		if strings.HasPrefix(s, u.suffix) {
			return u.suffix, u.size, true
		}
	}
	return "", 0, false
}

// scale multiplies a decimal number by a unit, refusing to wrap around.
// Whole numbers stay in integer arithmetic so that common values are exact.
func scale(number string, unit time.Duration) (time.Duration, error) {
	if !strings.Contains(number, ".") {
		n, err := strconv.ParseInt(number, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s is out of range", number)
		}
		if n != 0 && n > int64(math.MaxInt64)/int64(unit) {
			return 0, fmt.Errorf("it is longer than %s", time.Duration(math.MaxInt64))
		}
		return time.Duration(n) * unit, nil
	}
	f, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not a number", number)
	}
	scaled := f * float64(unit)
	if scaled >= math.MaxInt64 {
		return 0, fmt.Errorf("it is longer than %s", time.Duration(math.MaxInt64))
	}
	return time.Duration(scaled), nil
}

// invalidDuration explains the syntax once, in the terms of the configuration
// file rather than the parser.
func invalidDuration(s string) error {
	return fmt.Errorf("%q is not a duration: write a number and a unit, for example 90m, 2h, 7d or 1w", s)
}

// Std returns the duration as a plain [time.Duration].
func (d Duration) Std() time.Duration { return time.Duration(d) }

// IsZero reports whether the duration is unset.
func (d Duration) IsZero() bool { return d == 0 }

// String renders the duration in the compact form flow accepts back, using
// days as the largest unit: "2h", "7d", "1h30m". A week is rendered as seven
// days, because "7d" and "1w" are the same span and one spelling is enough.
func (d Duration) String() string {
	n := time.Duration(d)
	if n == 0 {
		return "0"
	}
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
		n = -n
	}
	if n%time.Second != 0 {
		// Sub-second precision has no place in a delivery objective, but if it
		// is there we must not silently drop it.
		b.WriteString(n.String())
		return b.String()
	}
	for _, part := range []struct {
		size   time.Duration
		suffix string
	}{
		{24 * time.Hour, "d"},
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	} {
		if count := n / part.size; count > 0 {
			fmt.Fprintf(&b, "%d%s", count, part.suffix)
			n %= part.size
		}
	}
	return b.String()
}

// UnmarshalYAML decodes a duration from a YAML scalar. Errors are returned as
// a yaml.TypeError carrying the node's line, so that a bad duration is located
// and reported alongside every other problem in the file rather than stopping
// the decode at the first one.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return yamlError(node, "a duration must be written as text, for example 2h or 7d")
	}
	parsed, err := ParseDuration(text)
	if err != nil {
		return yamlError(node, "%s", err)
	}
	*d = Duration(parsed)
	return nil
}

// yamlError builds a located decoding error in the form yaml.v3 itself uses.
func yamlError(node *yaml.Node, format string, args ...any) error {
	return &yaml.TypeError{Errors: []string{
		fmt.Sprintf("line %d: %s", node.Line, fmt.Sprintf(format, args...)),
	}}
}

// MarshalYAML encodes the duration in the compact form of [Duration.String].
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// MarshalJSON encodes the duration as the string a human would have typed.
func (d Duration) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

// UnmarshalJSON decodes a duration written as a string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	text, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("a duration must be written as text, for example 2h or 7d")
	}
	parsed, err := ParseDuration(text)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
