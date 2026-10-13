//go:build flowtest

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// What `flow apply` does when there is a person at a terminal to ask (spec 02
// §2, spec 06 §3, spec 12 §4). The decision is the same one the plain prompt
// makes; what is proved here is that the command reaches the screen at all,
// and that the key a person presses is what the world ends up agreeing with.

// atTerminal runs flow with the streams it is given, having told it both of
// them are terminals. It is the only way to exercise the drawn half of a
// confirmation: a test process has no terminal, and a command that can only
// ever take the plain path leaves the other one unproven.
func atTerminal(t *testing.T, dir, typed string, args ...string) (code int, stdout string) {
	t.Helper()
	// A terminal and a CI job are different claims, and flow believes the
	// second one first (spec 02 §1). The runner this suite goes on to live in
	// sets CI, so a test that only said "both ends are terminals" would be
	// answered no there and nowhere else.
	t.Setenv("CI", "")
	stdin := io.Reader(strings.NewReader(typed))
	var out, errs bytes.Buffer

	was := terminalCheck
	t.Cleanup(func() { terminalCheck = was })
	terminalCheck = func(stream any) bool {
		return stream == stdin || stream == io.Writer(&out)
	}

	code = run(append([]string{"-C", dir}, args...), stdin, &out, &errs)
	return code, out.String()
}

// TestApplyAsksOnScreenAndAbidesByTheAnswer states both halves of the
// confirmation at once: pressing n applies nothing and says so, and pressing
// y in the same repository applies everything that was planned. Two runs
// against one world is what makes the first one a refusal rather than a world
// that had nothing to do.
func TestApplyAsksOnScreenAndAbidesByTheAnswer(t *testing.T) {
	dir := fakeRepo(t)

	code, stdout := atTerminal(t, dir, "n", "apply")
	if code != core.ExitActionRequired {
		t.Fatalf("a refused apply exited %d, want %d", code, core.ExitActionRequired)
	}
	if !strings.Contains(stdout, "nothing was applied") {
		t.Errorf("a refused apply did not say so:\n%q", stdout)
	}

	// The question was drawn rather than printed: a screen leaves the cursor
	// and the terminal's own modes in the output, and the plain prompt does
	// not.
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("nothing was drawn, so the plain prompt answered instead:\n%q", stdout)
	}
	if strings.Contains(stdout, "[y/N]") {
		t.Errorf("the plain prompt was used at a terminal:\n%q", stdout)
	}

	// Nothing may have been written on the way to being refused, which is
	// what the next apply proves by still having everything to do.
	if refused, _ := applyJSON(t, dir, "--auto-approve"); len(refused.mutations()) == 0 {
		t.Fatal("the refused apply had already made the changes")
	}
}

// TestApplyOnScreenMakesTheChangesWhenItIsAllowedTo is the other key. It is
// its own repository, because the point is that y alone is what let the
// changes through.
func TestApplyOnScreenMakesTheChangesWhenItIsAllowedTo(t *testing.T) {
	dir := fakeRepo(t)

	code, stdout := atTerminal(t, dir, "y", "apply")
	if code != core.ExitActionRequired {
		t.Fatalf("an approved apply exited %d, want %d", code, core.ExitActionRequired)
	}
	if strings.Contains(stdout, "nothing was applied") {
		t.Fatalf("an approved apply applied nothing:\n%q", stdout)
	}
	if second, _ := applyJSON(t, dir, "--auto-approve"); len(second.mutations()) != 0 {
		t.Errorf("the approved apply left work behind: %v", second.mutations())
	}
}

// TestADumbTerminalGetsThePlainPrompt states that the narrower rule has
// somewhere to fall back to. A terminal that says it cannot draw is still a
// person who can answer, so the question is still asked — in the shape that
// terminal can show.
func TestADumbTerminalGetsThePlainPrompt(t *testing.T) {
	t.Setenv("TERM", "dumb")
	dir := fakeRepo(t)

	code, stdout := atTerminal(t, dir, "n\n", "apply")
	if code != core.ExitActionRequired {
		t.Fatalf("a refused apply exited %d, want %d", code, core.ExitActionRequired)
	}
	if !strings.Contains(stdout, "[y/N]") {
		t.Errorf("a dumb terminal was not offered the plain prompt:\n%q", stdout)
	}
	if !strings.Contains(stdout, "nothing was applied") {
		t.Errorf("a refused apply did not say so:\n%q", stdout)
	}
}
