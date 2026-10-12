//go:build flowtest

package main

import (
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// Only the exact value "1" selects the fakes. A variable somebody exported to
// an empty string, or set to a word that reads like yes, must not quietly
// produce a fabricated world: the fakes answer every question confidently and
// a run that reached them by accident would look like a passing one.
func TestFakesAreSelectedOnlyByTheExactValue(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	for _, name := range []string{"GITHUB_TOKEN", "FLOW_GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}

	for _, value := range []string{"", "0", "true", "yes", "11"} {
		t.Run("FLOW_TEST_FAKES="+value, func(t *testing.T) {
			t.Setenv(fakeEnv, value)
			code, stdout, stderr := exec(t, "-C", dir, "plan")
			if code != core.ExitNoPerm {
				t.Fatalf("plan exited %d, want %d: this is not a request for the fakes (stderr: %s)", code, core.ExitNoPerm, stderr)
			}
			if stdout != "" {
				t.Errorf("a plan was produced:\n%s", stdout)
			}
		})
	}
}
