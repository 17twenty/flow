package core_test

import (
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// Every row is Gitea's answer, and most come in pairs that differ in it: a
// matcher that gets one of a pair right by accident gets the other wrong.
// Where path.Match would answer differently the row says so, because that is
// the reading this replaces (issue #293).
func TestMatchCheckPattern(t *testing.T) {
	tests := []struct {
		pattern, context string
		want             bool
	}{
		{"flow/policy", "flow/policy", true},
		{"flow/policy", "flow/policyx", false},
		{"flow/*", "flow/policy", true},
		{"flow/*", "ci/test", false},
		// path.Match stops `*` at a slash; Gitea compiles without separators.
		{"ci/*", "ci/build/linux", true},
		{"*", "flow/policy", true},
		{"**", "ci/build/linux", true},
		{"ci/?", "ci/a", true},
		{"ci/?", "ci/ab", false},
		{"ci/[ab]", "ci/b", true},
		{"ci/[ab]", "ci/c", false},
		{"ci/[!ab]", "ci/c", true},
		{"ci/[!ab]", "ci/a", false},
		{"ci/[a-c]", "ci/b", true},
		{"ci/[a-c]", "ci/d", false},
		// A dash with nothing before it is a dash, not a range.
		{"ci/[-a]", "ci/-", true},
		// The class's brackets and dots mean themselves inside it.
		{"ci/[.]", "ci/.", true},
		{"ci/[.]", "ci/x", false},
		// path.Match has no alternatives at all.
		{"{flow,ci}/policy", "ci/policy", true},
		{"{flow,ci}/policy", "cd/policy", false},
		{`flow/\*`, "flow/*", true},
		{`flow/\*`, "flow/policy", false},
		// Outside braces a comma and a closing brace are characters.
		{"ci,cd", "ci,cd", true},
		{"ci,cd", "ci", false},
		{"ci}/x", "ci}/x", true},
		{"ci}/x", "ci", false},
		// An escaped bracket stays inside its class, and so does what
		// follows it.
		{`ci/[\]*]`, "ci/*", true},
		{`ci/[\]*]`, "ci/]x]", false},
		{`ci/[\]?]`, "ci/?", true},
		{`ci/[\]?]`, "ci/.", false},
		// A class ends at its bracket, and what follows is a glob again.
		{"ci/[ab]*", "ci/ax", true},
		{"ci/[ab]*", "ci/xa", false},
		// Everything else is literal, including what a regexp would read.
		{"ci+", "ci+", true},
		{"ci+", "cii", false},
		{"ci|cd", "ci", false},
		{"ci.test (linux)", "ci.test (linux)", true},
		{"ci.test", "ciXtest", false},
		// A pattern Gitea cannot compile requires nothing.
		{"ci/[ab", "ci/a", false},
		{"ci/[]", "ci/]", false},
		{"{flow,ci/policy", "flow", false},
		{`flow\`, `flow\`, false},
		{"ci/[z-a]", "ci/b", false},
	}
	for _, tt := range tests {
		if got := core.MatchCheckPattern(tt.pattern, tt.context); got != tt.want {
			t.Errorf("MatchCheckPattern(%q, %q) = %v, want %v", tt.pattern, tt.context, got, tt.want)
		}
	}
}

// A protection whose checks are names never matches one as a pattern: GitHub
// and GitLab require a context called `flow/*` only of a status called that.
func TestRequiresCheckReadsPatternsOnlyWhereTheForgeDoes(t *testing.T) {
	p := core.Protection{RequiredChecks: []string{"flow/*"}}
	if p.RequiresCheck("flow/policy") {
		t.Error("a literal flow/* requires flow/policy")
	}
	p.CheckPatterns = true
	if !p.RequiresCheck("flow/policy") {
		t.Error("the pattern flow/* does not require flow/policy")
	}
	if p.RequiresCheck("ci/test") {
		t.Error("the pattern flow/* requires ci/test")
	}
}
