//go:build !flowtest

package main

import (
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// The fakes are selectable only in a build carrying the flowtest tag (spec
// 14). This is the other half of that sentence: the shipped binary does not
// contain fakes_flowtest.go, so FLOW_TEST_FAKES means nothing to it and
// cannot be made to mean anything.
//
// The proof is that a release build asked for fakes still goes looking for a
// credential it has not been given. A build that honoured the variable would
// need no credential and would print a plan — so this test fails loudly if
// the fakes ever leak out of the tag.
func TestReleaseBuildsCannotSelectTheFakes(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	for _, name := range []string{"GITHUB_TOKEN", "FLOW_GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("FLOW_TEST_FAKES", "1")

	code, stdout, stderr := exec(t, "-C", dir, "plan")
	if code != core.ExitNoPerm {
		t.Fatalf("plan exited %d, want %d: a release build must still need a credential (stderr: %s)", code, core.ExitNoPerm, stderr)
	}
	if stdout != "" {
		t.Errorf("a release build asked for fakes produced a plan:\n%s", stdout)
	}
	if !strings.Contains(stderr, "credential") {
		t.Errorf("the error is not about the missing credential:\n%s", stderr)
	}
}
