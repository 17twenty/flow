package commits_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/commits"
)

// corpusDir holds the shared commit samples used by the linter tests and as
// fuzz seeds.
const corpusDir = "../../assets/testdata/commits"

// readSamples returns the samples in one corpus file. Samples are separated by
// a line containing only "---" (spec 08 §4).
func readSamples(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatalf("read corpus %s: %v", name, err)
	}
	var out []string
	for _, sample := range strings.Split(string(raw), "\n---\n") {
		if strings.TrimSpace(sample) != "" {
			out = append(out, sample)
		}
	}
	return out
}

// The corpus must actually exist and hold samples, or the fuzz seeds below are
// silently empty.
func TestCorpusIsReadable(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"valid.txt", "invalid.txt"} {
		if got := readSamples(t, name); len(got) < 5 {
			t.Errorf("%s yielded %d samples, want a populated corpus", name, len(got))
		}
	}
}

// Every sample in valid.txt must parse as a Conventional Commit. The seeds
// carry footers from an earlier design, which this package ignores: trailers
// are read by internal/git, not here (spec 08 §1).
func TestCorpusValidSamplesParse(t *testing.T) {
	t.Parallel()
	for _, sample := range readSamples(t, "valid.txt") {
		m := commits.Parse(sample)
		if !m.Conventional() {
			t.Errorf("valid corpus sample did not parse:\n%s", sample)
		}
	}
}

// Samples whose expectation is cc.header.format must fail to parse; every other
// invalid sample must parse, since its problem is a linter rule, not grammar.
func TestCorpusInvalidSamplesMatchHeaderFormat(t *testing.T) {
	t.Parallel()
	for _, sample := range readSamples(t, "invalid.txt") {
		expectsFormat := strings.Contains(firstLine(sample), "cc.header.format")
		m := commits.Parse(sample)
		if expectsFormat && m.Conventional() {
			t.Errorf("sample expecting cc.header.format parsed anyway:\n%s", sample)
		}
		if !expectsFormat && !m.Conventional() {
			t.Errorf("sample expecting a non-grammar rule failed to parse:\n%s", sample)
		}
	}
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FuzzParse checks that parsing never panics and that a Message which claims to
// be conventional is internally consistent, whatever the input.
func FuzzParse(f *testing.F) {
	for _, name := range []string{"valid.txt", "invalid.txt"} {
		raw, err := os.ReadFile(filepath.Join(corpusDir, name))
		if err != nil {
			f.Fatalf("read corpus %s: %v", name, err)
		}
		for _, sample := range strings.Split(string(raw), "\n---\n") {
			f.Add(sample, byte('#'))
		}
	}
	// Shapes the corpus does not cover.
	for _, seed := range []string{
		"", " ", "\n", "\r\n", ":", "feat", "feat:", "feat: ", "feat()!: x",
		"feat(a,b,c)!: x\n\nBREAKING CHANGE: y", "Merge branch 'x'",
		"fixup! feat: x", `Revert "feat: x"`,
		"# ------------------------ >8 ------------------------",
		"feat(\u0000): x", "feat: \u00ff\u00fe", strings.Repeat("a", 300) + ": x",
	} {
		f.Add(seed, byte('#'))
	}

	f.Fuzz(func(t *testing.T, raw string, commentChar byte) {
		m := commits.ParseWithOptions(raw, commits.Options{CommentChar: commentChar})

		if m.Raw != raw {
			t.Fatalf("Raw was modified: %q != %q", m.Raw, raw)
		}
		if strings.ContainsAny(m.Header, "\n\r") {
			t.Fatalf("Header spans lines: %q", m.Header)
		}
		if strings.Contains(m.Body, "\r") {
			t.Fatalf("Body kept a carriage return: %q", m.Body)
		}
		if m.HasBody != (m.Body != "") {
			t.Fatalf("HasBody = %v but Body = %q", m.HasBody, m.Body)
		}
		if !m.HasBody && !m.BodyLeadingBlank {
			t.Fatal("a message with no body cannot fail the leading-blank rule")
		}

		if !m.Conventional() {
			// Nothing else is promised about unparsed input.
			if m.Scope != "" || m.Breaking || m.Description != "" {
				t.Fatalf("non-conventional message carries header fields: %+v", m)
			}
			return
		}

		for _, r := range m.Type {
			if r > utf8.RuneSelf || !isASCIILetter(byte(r)) {
				t.Fatalf("Type %q contains a non-letter", m.Type)
			}
		}
		if strings.TrimSpace(m.Description) == "" {
			t.Fatalf("conventional message has a blank description: %q", m.Header)
		}
		if m.TypeColumn != 1 {
			t.Fatalf("TypeColumn = %d, want 1", m.TypeColumn)
		}
		headerLen := m.HeaderLen()
		for name, col := range map[string]int{
			"ScopeColumn":       m.ScopeColumn,
			"BreakingColumn":    m.BreakingColumn,
			"DescriptionColumn": m.DescriptionColumn,
		} {
			if col < 0 || col > headerLen+1 {
				t.Fatalf("%s = %d is outside the %d-rune header %q", name, col, headerLen, m.Header)
			}
		}
		if m.HasScope != (m.ScopeColumn > 0) {
			t.Fatalf("HasScope = %v but ScopeColumn = %d", m.HasScope, m.ScopeColumn)
		}
		if m.Breaking != (m.BreakingColumn > 0) {
			t.Fatalf("Breaking = %v but BreakingColumn = %d", m.Breaking, m.BreakingColumn)
		}
		if !strings.HasPrefix(m.Header, m.Type) {
			t.Fatalf("Header %q does not begin with Type %q", m.Header, m.Type)
		}
		if !strings.HasSuffix(m.Header, m.Description) {
			t.Fatalf("Header %q does not end with Description %q", m.Header, m.Description)
		}
		if m.HasScope && len(m.Scopes) == 0 && strings.Trim(m.Scope, " ,") != "" {
			t.Fatalf("scope %q produced no entries", m.Scope)
		}
	})
}

// isASCIILetter reports whether b is an ASCII letter.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
