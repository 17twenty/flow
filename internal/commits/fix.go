package commits

import (
	"slices"
	"strings"
	"unicode"
)

// Fix applies the safe autofixes of spec 08 §3 using git's default comment
// character.
func Fix(raw string) (string, []string) { return FixWithOptions(raw, ParseOptions{}) }

// FixWithOptions rewrites raw to correct the four problems flow is willing to
// fix without asking: a type in the wrong case, whitespace around the header, a
// missing blank line between header and body, and a full stop ending the
// description. It returns the new message and the ids of the rules it addressed.
//
// Nothing else is ever rewritten. In particular cc.description.case is left
// alone: lowercasing a first letter would corrupt an acronym or an identifier,
// and the rule is only a warning. Comments, the scissors section and the body
// are preserved byte for byte.
//
// Fix is idempotent: fixing an already-fixed message returns it unchanged and
// reports no rules.
func FixWithOptions(raw string, opts ParseOptions) (string, []string) {
	comment := opts.CommentChar
	if comment == 0 {
		comment = defaultCommentChar
	}

	// Work in LF and restore the original ending, so a CRLF message stays CRLF.
	crlf := strings.Contains(raw, "\r\n")
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	// Everything from the scissors line on is git's, not the author's.
	limit := len(lines)
	for i, line := range lines {
		if scissors(line, comment) {
			limit = i
			break
		}
	}

	header := headerLine(lines, limit, comment)
	if header < 0 {
		return raw, nil // nothing but comments, blanks or a scissors section
	}

	var fixed []string
	record := func(rule string) {
		if !slices.Contains(fixed, rule) {
			fixed = append(fixed, rule)
		}
	}

	// Whitespace first: a header indented by a stray space does not parse at
	// all, so trimming it is what lets the remaining fixes see a grammar.
	if trimmed := trimHeader(lines[header], comment); trimmed != lines[header] {
		lines[header] = trimmed
		record(RuleHeaderFormat)
	}

	if h, rules := fixHeader(lines[header]); h != lines[header] {
		lines[header] = h
		for _, r := range rules {
			record(r)
		}
	}

	// A blank line is inserted directly after the header even when comments sit
	// between header and body: git strips those, so the blank must precede them
	// to survive cleanup.
	if next := nextUncommented(lines, header+1, limit, comment); next >= 0 && lines[next] != "" {
		lines = slices.Insert(lines, header+1, "")
		record(RuleBodyLeadingBlank)
	}

	out := strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if out == raw {
		return raw, nil
	}
	return out, fixed
}

// trimHeader removes whitespace from both ends of the header, except where
// removing the leading whitespace would expose the comment character. git
// decides what is a comment by looking at the first character of the line, so
// turning " #ops: thing" into "#ops: thing" would not tidy the header — it would
// delete it from the commit entirely, and a line starting a scissors marker
// would truncate everything after it. Indentation is left in place rather than
// risk destroying the author's message.
func trimHeader(line string, comment byte) string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == comment {
		return strings.TrimRightFunc(line, unicode.IsSpace)
	}
	return trimmed
}

// headerLine returns the index of the first line that carries the header: the
// first line before limit that is neither a comment nor blank.
func headerLine(lines []string, limit int, comment byte) int {
	for i := 0; i < limit; i++ {
		if len(lines[i]) > 0 && lines[i][0] == comment {
			continue
		}
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		return i
	}
	return -1
}

// nextUncommented returns the index of the next line at or after from, before
// limit, that is not a comment, or -1 if there is none.
func nextUncommented(lines []string, from, limit int, comment byte) int {
	for i := from; i < limit && i < len(lines); i++ {
		if len(lines[i]) > 0 && lines[i][0] == comment {
			continue
		}
		return i
	}
	return -1
}

// fixHeader lowercases the type and strips full stops from the end of the
// description. A header that does not parse is returned untouched: without a
// grammar there is no way to tell a type from the first word of a sentence.
func fixHeader(h string) (string, []string) {
	var m Message
	m.Header = h
	parseHeader(&m)
	if !m.Conventional() {
		return h, nil
	}

	var rules []string
	out := h

	// A type is ASCII letters only, so lowercasing cannot change its length and
	// the offsets computed from the parse stay valid.
	if lower := strings.ToLower(m.Type); lower != m.Type {
		out = lower + out[len(m.Type):]
		rules = append(rules, RuleTypeAllowed)
	}

	start := len(out) - len(m.Description)
	desc := out[start:]
	// Trim stops together with any trailing space, using the same definition of
	// space as trimHeader. If the two disagreed — a cutset of " \t." against
	// unicode.IsSpace, say — stripping a stop could expose a stray carriage
	// return for the next pass to trim, and Fix would not settle.
	trimmed := strings.TrimRightFunc(desc, func(r rune) bool {
		return r == '.' || unicode.IsSpace(r)
	})
	switch {
	case trimmed == "":
		// A description of nothing but stops and spaces would leave an
		// unparseable header. Refusing to fix beats writing something worse.
	case trimmed != desc:
		out = out[:start] + trimmed
		if strings.Contains(desc[len(trimmed):], ".") {
			rules = append(rules, RuleDescriptionPeriod)
		}
	}

	return out, rules
}
