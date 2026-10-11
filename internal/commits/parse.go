// Package commits parses and lints Conventional Commit messages. It writes
// nothing: flow has no commit composer, changelog or version tooling (spec 08).
// The package depends only on the standard library.
package commits

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind classifies a message by its form, which decides whether the linter
// applies at all. Git generates merge, revert and autosquash messages itself,
// so they are not held to the Conventional Commits grammar.
type Kind int

const (
	// KindNormal is an ordinary, human-authored message.
	KindNormal Kind = iota
	// KindMerge is a git-generated merge commit ("Merge branch ...").
	KindMerge
	// KindRevert is a git-generated revert commit (`Revert "..."`). A message
	// using the conventional `revert:` type is KindNormal with Type "revert".
	KindRevert
	// KindFixup is an autosquash fixup ("fixup! ...").
	KindFixup
	// KindSquash is an autosquash squash ("squash! ...").
	KindSquash
	// KindAmend is an autosquash amend ("amend! ...").
	KindAmend
)

// Generated reports whether git produced this message rather than a human, in
// which case the linter skips it.
func (k Kind) Generated() bool { return k != KindNormal }

// String returns the kind's lowercase name.
func (k Kind) String() string {
	switch k {
	case KindMerge:
		return "merge"
	case KindRevert:
		return "revert"
	case KindFixup:
		return "fixup"
	case KindSquash:
		return "squash"
	case KindAmend:
		return "amend"
	default:
		return "normal"
	}
}

// defaultCommentChar is git's core.commentChar default.
const defaultCommentChar = '#'

// Options tunes parsing for one repository.
type ParseOptions struct {
	// CommentChar begins a git comment line. Zero means git's default, '#'.
	// Callers read the real value from core.commentChar via internal/git.
	CommentChar byte
}

// Message is a parsed commit message. Parsing never fails: input that is not a
// Conventional Commit yields a Message with an empty Type, which the linter
// reports as cc.header.format.
type Message struct {
	// Raw is the input exactly as supplied.
	Raw string
	// Header is the first meaningful line, after comments and scissors are
	// removed. It is the subject the linter measures and the PR title under a
	// squash merge.
	Header string
	// Kind classifies the message's form.
	Kind Kind

	// Type is the commit type as written, for example "feat". It is empty when
	// the header does not parse; the linter checks its case and membership.
	Type string
	// Scope is the raw text between the parentheses, empty when absent. It may
	// hold several comma-separated scopes; see Scopes.
	Scope string
	// HasScope distinguishes an absent scope from an empty one.
	HasScope bool
	// Scopes holds Scope split on commas and trimmed, for per-scope checks.
	Scopes []string
	// Breaking reports the "!" marker before the colon.
	Breaking bool
	// Description is the text after ": ", exactly as written, including any
	// trailing whitespace so that the autofix can see it.
	Description string

	// Body is everything after the header and its blank line, with trailing
	// blank lines removed.
	Body string
	// HasBody reports whether any body text remains after cleaning.
	HasBody bool
	// BodyLeadingBlank reports whether a blank line separates header and body.
	// It is true when there is no body, so the rule passes vacuously.
	BodyLeadingBlank bool
	// BreakingChange holds the text of a "BREAKING CHANGE:" (or
	// "BREAKING-CHANGE:") footer, if one is present.
	BreakingChange string
	// HasBreakingChange reports whether such a footer was found.
	HasBreakingChange bool

	// TypeColumn is the 1-based rune column of the type within Header.
	TypeColumn int
	// ScopeColumn is the 1-based rune column of the scope's first character,
	// or 0 when there is no scope.
	ScopeColumn int
	// BreakingColumn is the 1-based rune column of "!", or 0 when absent.
	BreakingColumn int
	// DescriptionColumn is the 1-based rune column where the description
	// begins, or 0 when the header does not parse.
	DescriptionColumn int
}

// Conventional reports whether the header parsed as type(scope)!: description.
func (m Message) Conventional() bool { return m.Type != "" }

// HeaderLen returns the header's length in runes, which is what
// commits.header_max limits.
func (m Message) HeaderLen() int { return utf8.RuneCountInString(m.Header) }

// Parse parses a commit message using git's default comment character.
func Parse(raw string) Message { return ParseWithOptions(raw, ParseOptions{}) }

// ParseWithOptions parses a commit message. It never returns an error: callers
// inspect Type to learn whether the header is conventional.
func ParseWithOptions(raw string, opts ParseOptions) Message {
	m := Message{Raw: raw}

	comment := opts.CommentChar
	if comment == 0 {
		comment = defaultCommentChar
	}

	lines := clean(raw, comment)
	if len(lines) == 0 {
		// Nothing but comments, blank lines or a scissors section.
		m.BodyLeadingBlank = true
		return m
	}

	m.Header = lines[0]
	m.Kind = classify(m.Header)

	rest := lines[1:]
	switch {
	case len(rest) == 0:
		m.BodyLeadingBlank = true
	case rest[0] == "":
		m.BodyLeadingBlank = true
		m.Body = strings.Join(trimBlankEdges(rest[1:]), "\n")
	default:
		m.Body = strings.Join(trimBlankEdges(rest), "\n")
	}
	m.HasBody = m.Body != ""
	if !m.HasBody {
		// A rule about the separator cannot fail when there is nothing to separate.
		m.BodyLeadingBlank = true
	}
	m.BreakingChange, m.HasBreakingChange = findBreakingChange(m.Body)

	parseHeader(&m)
	return m
}

// scissors reports whether a comment line is git's scissors marker, after which
// everything is discarded.
func scissors(line string, comment byte) bool {
	if len(line) == 0 || line[0] != comment {
		return false
	}
	body := strings.TrimSpace(line[1:])
	if !strings.Contains(body, ">8") {
		return false
	}
	for _, r := range body {
		if r != '-' && r != '>' && r != '8' && r != ' ' {
			return false
		}
	}
	return true
}

// clean normalises line endings and removes the scissors section, comment lines
// and blank lines at either end, mirroring git's own message cleanup.
func clean(raw string, comment byte) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	var kept []string
	for _, line := range strings.Split(raw, "\n") {
		if scissors(line, comment) {
			break
		}
		if len(line) > 0 && line[0] == comment {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	return trimBlankEdges(kept)
}

// trimBlankEdges drops blank lines from both ends of lines.
func trimBlankEdges(lines []string) []string {
	start := 0
	for start < len(lines) && lines[start] == "" {
		start++
	}
	end := len(lines)
	for end > start && lines[end-1] == "" {
		end--
	}
	return lines[start:end]
}

// autosquashPrefixes maps git's autosquash markers to their kinds.
var autosquashPrefixes = []struct {
	prefix string
	kind   Kind
}{
	{"fixup! ", KindFixup},
	{"squash! ", KindSquash},
	{"amend! ", KindAmend},
}

// classify identifies git-generated message forms from the header.
func classify(header string) Kind {
	for _, p := range autosquashPrefixes {
		if strings.HasPrefix(header, p.prefix) {
			return p.kind
		}
	}
	if strings.HasPrefix(header, "Merge ") {
		return KindMerge
	}
	// git revert writes: Revert "original subject"
	if strings.HasPrefix(header, `Revert "`) {
		return KindRevert
	}
	return KindNormal
}

// findBreakingChange locates a BREAKING CHANGE footer in the body. Both the
// spelling with a space and the one with a hyphen are recognised.
func findBreakingChange(body string) (string, bool) {
	if body == "" {
		return "", false
	}
	for _, line := range strings.Split(body, "\n") {
		for _, prefix := range []string{"BREAKING CHANGE:", "BREAKING-CHANGE:"} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(line[len(prefix):]), true
			}
		}
	}
	return "", false
}

// isAlpha reports whether b is an ASCII letter, the only thing a type may hold.
func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// validScopeRune reports whether r may appear in a scope (spec 08 §1).
func validScopeRune(r rune) bool {
	switch r {
	case '-', '_', '/', '.', ',', ' ':
		return true
	}
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// column converts a byte offset within s to a 1-based rune column.
func column(s string, offset int) int {
	return utf8.RuneCountInString(s[:offset]) + 1
}

// parseHeader applies the grammar to m.Header, filling in the fields on
// success and leaving Type empty on failure.
func parseHeader(m *Message) {
	h := m.Header

	i := 0
	for i < len(h) && isAlpha(h[i]) {
		i++
	}
	if i == 0 {
		return // no type: not conventional
	}
	typ := h[:i]

	scope, scopeCol := "", 0
	hasScope := false
	if i < len(h) && h[i] == '(' {
		end := strings.IndexByte(h[i:], ')')
		if end < 0 {
			return // unterminated scope
		}
		scope = h[i+1 : i+end]
		if scope == "" {
			return // empty scope
		}
		for _, r := range scope {
			if !validScopeRune(r) {
				return
			}
		}
		scopeCol = column(h, i+1)
		hasScope = true
		i += end + 1
	}

	breaking, breakingCol := false, 0
	if i < len(h) && h[i] == '!' {
		breaking = true
		breakingCol = column(h, i)
		i++
	}

	if !strings.HasPrefix(h[i:], ": ") {
		return // the separator is ": ", exactly
	}
	i += 2

	desc := h[i:]
	if strings.TrimSpace(desc) == "" {
		return // a description is required
	}

	m.Type = typ
	m.TypeColumn = 1
	m.Scope = scope
	m.HasScope = hasScope
	m.ScopeColumn = scopeCol
	m.Breaking = breaking
	m.BreakingColumn = breakingCol
	m.Description = desc
	m.DescriptionColumn = column(h, i)

	if hasScope {
		for _, s := range strings.Split(scope, ",") {
			if s = strings.TrimSpace(s); s != "" {
				m.Scopes = append(m.Scopes, s)
			}
		}
	}
}
