package core

import (
	"regexp"
	"strings"
)

// MatchCheckPattern reports whether a required status context written as a
// pattern matches a context somebody reports.
//
// The syntax is Gitea's, because Gitea is the forge that reads required
// contexts as patterns (issue #293). It compiles each one with its own
// modules/glob and no separators, and this is that translation to a regular
// expression, kept rule for rule: `*` and `**` match any run of characters
// including `/`, `?` matches one character, `[abc]`, `[a-z]` and `[!abc]` are
// a class handed to the regexp as written, `{a,b}` matches either
// alternative, `\` makes the next character mean itself, and the rest is
// literal. This is not path.Match: there `ci/*` stops at the next slash and
// `{a,b}` means nothing, so a reading built on it would tell a team that
// requires `ci/*` that `ci/build/linux` is not required when Gitea says it
// is.
//
// A pattern Gitea cannot compile is one it logs and skips rather than
// requires, so it matches nothing here either.
func MatchCheckPattern(pattern, context string) bool {
	var b strings.Builder
	b.WriteString(`^`)
	writeCheckPattern([]rune(pattern), 0, false, &b)
	b.WriteString(`$`)
	// Every pattern Gitea refuses — a class or a brace left open, a
	// backslash with nothing to escape — is written through as the same
	// unfinished regexp Gitea writes, and refused here by the same compiler.
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(context)
}

// writeCheckPattern writes the pattern from pos on as a regular expression,
// and returns where it stopped. Inside braces a comma separates alternatives
// and a closing brace ends them; outside, both are literal, as they are to
// Gitea. `**` is `*` twice, which matches what one does.
func writeCheckPattern(pattern []rune, pos int, braced bool, b *strings.Builder) int {
	for pos < len(pattern) {
		c := pattern[pos]
		pos++
		switch {
		case braced && c == '}':
			b.WriteString(`)`)
			return pos
		case c == '*':
			b.WriteString(`.*`)
		case c == '?':
			b.WriteString(`.`)
		case c == '[':
			pos = writeCheckClass(pattern, pos, b)
		case c == '{':
			b.WriteString(`(`)
			pos = writeCheckPattern(pattern, pos, true, b)
		case braced && c == ',':
			b.WriteString(`|`)
		case c == '\\':
			b.WriteString(`\`)
			if pos < len(pattern) {
				b.WriteRune(pattern[pos])
				pos++
			}
		case strings.ContainsRune(`.^$()|+`, c):
			b.WriteString(`\`)
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}
	return pos
}

// writeCheckClass writes the character class whose opening bracket is just
// before pos, and returns the position after its closing one. The class
// reaches the regexp as written, but for `!` becoming `^`.
func writeCheckClass(pattern []rune, pos int, b *strings.Builder) int {
	b.WriteString(`[`)
	if pos < len(pattern) && pattern[pos] == '!' {
		pos++
		b.WriteString(`^`)
	}
	for pos < len(pattern) {
		c := pattern[pos]
		pos++
		b.WriteRune(c)
		switch {
		case c == ']':
			return pos
		case c == '\\' && pos < len(pattern):
			b.WriteRune(pattern[pos])
			pos++
		}
	}
	return pos
}
