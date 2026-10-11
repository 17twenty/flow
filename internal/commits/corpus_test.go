package commits_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/commits"
)

// corpusDir holds the shared commit samples: the linter's expectations and the
// fuzz seeds come from the same files (spec 08 §4).
const corpusDir = "../../assets/testdata/commits"

// baselineSettings is the configuration the corpus is written against. Scope
// rules are off by default so samples stay generic; a sample switches them on
// with a "# settings:" directive.
func baselineSettings() commits.Settings {
	return commits.Settings{
		Types: []string{
			"feat", "fix", "perf", "refactor", "test",
			"docs", "build", "ci", "chore", "revert", "style",
		},
		HeaderMax: 72,
	}
}

// sample is one corpus entry with its directives.
type sample struct {
	// text is the commit message itself, directives removed.
	text string
	// expect lists the rule ids the sample must produce, empty for valid.txt.
	expect []string
	// settings is baselineSettings with any per-sample directive applied.
	settings commits.Settings
	// line is the 1-based line the sample starts on, for failure messages.
	line int
}

// readCorpus parses one corpus file. Samples are separated by a line holding
// only "---". A sample may be preceded by "# expect: <rule-ids>" and
// "# settings: <directives>" lines.
func readCorpus(t *testing.T, name string) []sample {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatalf("read corpus %s: %v", name, err)
	}

	var out []sample
	line := 1
	for _, block := range strings.Split(string(raw), "\n---\n") {
		s := sample{settings: baselineSettings(), line: line}
		line += strings.Count(block, "\n") + 2

		var body []string
		for _, l := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(l, "# expect:"):
				s.expect = splitList(strings.TrimPrefix(l, "# expect:"))
			case strings.HasPrefix(l, "# settings:"):
				applySettings(t, &s.settings, strings.TrimPrefix(l, "# settings:"))
			default:
				body = append(body, l)
			}
		}
		s.text = strings.Trim(strings.Join(body, "\n"), "\n")
		if s.text != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitList splits a comma-separated directive value.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applySettings interprets a "# settings:" directive.
func applySettings(t *testing.T, s *commits.Settings, directive string) {
	t.Helper()
	for _, d := range splitList(directive) {
		switch {
		case d == "scope_required":
			s.ScopeRequired = true
		case strings.HasPrefix(d, "scopes="):
			s.AllowedScopes = strings.Split(strings.TrimPrefix(d, "scopes="), "|")
		default:
			t.Fatalf("unknown corpus settings directive %q", d)
		}
	}
}

// The corpus must hold samples, or every test below passes vacuously.
func TestCorpusIsPopulated(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"valid.txt", "invalid.txt"} {
		if got := readCorpus(t, name); len(got) < 10 {
			t.Errorf("%s yielded %d samples, want a populated corpus", name, len(got))
		}
	}
}

// Every sample in valid.txt must lint completely clean, warnings included.
func TestCorpusValidSamplesAreClean(t *testing.T) {
	t.Parallel()
	for _, s := range readCorpus(t, "valid.txt") {
		m := commits.Parse(s.text)
		if !m.Conventional() {
			t.Errorf("valid.txt:%d did not parse:\n%s", s.line, s.text)
			continue
		}
		got := commits.Lint(m, commits.LintOptions{Settings: s.settings})
		for _, v := range got {
			t.Errorf("valid.txt:%d produced %s (%s): %s\n%s",
				s.line, v.Rule, v.Severity, v.Message, s.text)
		}
	}
}

// Every sample in invalid.txt must produce exactly the rule ids it declares:
// no missing rule, and no surprise extras.
func TestCorpusInvalidSamplesMatchExpectations(t *testing.T) {
	t.Parallel()
	for _, s := range readCorpus(t, "invalid.txt") {
		if len(s.expect) == 0 {
			t.Errorf("invalid.txt:%d has no '# expect:' directive:\n%s", s.line, s.text)
			continue
		}
		m := commits.Parse(s.text)
		var got []string
		for _, v := range commits.Lint(m, commits.LintOptions{Settings: s.settings}) {
			if !slices.Contains(got, v.Rule) {
				got = append(got, v.Rule)
			}
		}
		want := slices.Clone(s.expect)
		slices.Sort(want)
		sorted := slices.Clone(got)
		slices.Sort(sorted)
		if !slices.Equal(sorted, want) {
			t.Errorf("invalid.txt:%d produced %v, want %v:\n%s", s.line, got, s.expect, s.text)
		}
	}
}

// A sample expecting cc.header.format must fail the grammar; every other
// sample must parse, since its problem is a rule rather than the grammar.
func TestCorpusInvalidSamplesMatchHeaderFormat(t *testing.T) {
	t.Parallel()
	for _, s := range readCorpus(t, "invalid.txt") {
		expectsFormat := slices.Contains(s.expect, commits.RuleHeaderFormat)
		if got := commits.Parse(s.text).Conventional(); got == expectsFormat {
			t.Errorf("invalid.txt:%d parsed = %v but expects cc.header.format = %v:\n%s",
				s.line, got, expectsFormat, s.text)
		}
	}
}

// The corpus must exercise every rule flow implements, so a new rule cannot
// land without a sample proving it fires.
func TestCorpusCoversEveryRule(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, s := range readCorpus(t, "invalid.txt") {
		for _, rule := range s.expect {
			seen[rule] = true
		}
	}
	for _, rule := range commits.AllRules() {
		if !seen[rule] {
			t.Errorf("no sample in invalid.txt exercises %s", rule)
		}
	}
}

// Rule ids declared by the corpus must exist, which catches the retired v1 ids
// (cc.footer.*, cc.trailer.*, cc.body.wrap, cc.description.imperative).
func TestCorpusUsesOnlyKnownRules(t *testing.T) {
	t.Parallel()
	for _, s := range readCorpus(t, "invalid.txt") {
		for _, rule := range s.expect {
			if !slices.Contains(commits.AllRules(), rule) {
				t.Errorf("invalid.txt:%d expects unknown rule %q", s.line, rule)
			}
		}
	}
}
