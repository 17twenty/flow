//go:build flowtest

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/git"
	"github.com/17twenty/flow/internal/ui"
)

// What `flow init` does when there is a person at a terminal to ask (spec 02
// §2, spec 12 §1).
//
// The acceptance criterion of the item is one sentence — the plain equivalent
// produces an identical configuration — and it is a sentence about two code
// paths being one. The wizard collects the flags and hands them back to the
// command, so what is proved here is that the file somebody answered their
// way to and the file somebody typed their way to are the same bytes, and
// that the command line the review offered them is the one that does it.

// wizardKeysFor is a whole session at the screen: accept the forge flow
// detected and the tracker the preset names, type an owner and a board, drop
// to the second preset — which is also the answer to when a ticket is done —
// and write it.
const wizardKeysFor = "\r\racme\r7\rj\ry"

// theSameByFlags is the command line that gets there without being asked. It
// is spelled out rather than taken from the screen so that the test fails
// when either half moves, not when both move together.
var theSameByFlags = []string{
	"init", "--force",
	"--preset", "deploy-done",
	"--owner", "acme",
	"--project", "7",
}

// TestTheWizardAndTheFlagsWriteTheSameFile is the item's acceptance criterion.
//
// The command line is also what the review showed the person who answered,
// so the two are asserted together: a wizard that wrote the right file and
// then told somebody the wrong way to write it again would have passed half
// of this.
func TestTheWizardAndTheFlagsWriteTheSameFile(t *testing.T) {
	asked := hookRepo(t)
	gitIn(t, asked, "remote", "add", "origin", "https://github.com/acme/widget.git")
	if code, stdout := atTerminal(t, asked, wizardKeysFor, "init"); code != 0 {
		t.Fatalf("the wizard exited %d:\n%s", code, stdout)
	}

	typed := hookRepo(t)
	gitIn(t, typed, "remote", "add", "origin", "https://github.com/acme/widget.git")
	if code, _, stderr := exec(t, append([]string{"-C", typed}, theSameByFlags...)...); code != 0 {
		t.Fatalf("the flags exited %d: %s", code, stderr)
	}

	if answered, written := readConfig(t, asked), readConfig(t, typed); answered != written {
		t.Errorf("the wizard and the flags disagree:\n--- answered ---\n%s\n--- typed ---\n%s", answered, written)
	}

	// The screen itself cannot be read back — a bubbletea program takes its
	// frames off the terminal as it leaves — so the command line the review
	// showed is asserted from the answers it was showing. The answers are
	// written out here rather than taken from the wizard, because a test that
	// asked the wizard what it had collected would agree with it whatever it
	// collected.
	offered := wizardAnswers.Command(wizardStarted)
	if want := "flow init " + strings.Join(theSameByFlags[2:], " "); offered != want {
		t.Errorf("the review offers %q, want %q", offered, want)
	}
}

// wizardStarted is where that session began: the preset flow defaults to, the
// forge the remote names, and the rest of the lean-kanban preset's own
// answers filled in behind them.
var wizardStarted = ui.Answers{
	Preset:  "lean-kanban",
	Forge:   "github",
	Tracker: "github-projects",
}

// wizardAnswers is where it ended.
var wizardAnswers = ui.Answers{
	Preset:  "deploy-done",
	Forge:   "github",
	Tracker: "github-projects",
	Owner:   "acme",
	Project: "7",
}

// TestTheScreenStartsFromTheFileFlowWouldHaveWritten states what the first
// cursor is pointed at and what the review's command line is shortened
// against, which are one value: the flags somebody passed, the forge the
// remote names, and the preset's own answer to the rest.
//
// Both halves have to be asserted here rather than at the screen, because
// each is masked somewhere else. A repository on github.com cannot tell the
// detected forge from the preset's, and a tracker filled in from the preset
// reaches the same file as a tracker nobody filled in — it only changes
// whether the review tells somebody to pass `--tracker` to get what flow
// does unasked.
func TestTheScreenStartsFromTheFileFlowWouldHaveWritten(t *testing.T) {
	dir := hookRepo(t)
	gitIn(t, dir, "remote", "add", "origin", "https://codeberg.org/acme/widget.git")

	repo, err := git.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	app := &Context{Ctx: t.Context(), CLI: &CLI{}, Stdout: io.Discard, Stderr: io.Discard}
	cmd := &InitCmd{Preset: "lean-kanban"}

	want := ui.Answers{Preset: "lean-kanban", Forge: "gitea", Tracker: "github-projects"}
	if got := cmd.detected(app, repo); got != want {
		t.Errorf("the screen starts from\n%+v\nwant\n%+v", got, want)
	}
}

// TestTheWizardWritesNothingWhenNobodyAgrees states the refusal. Escape backs
// out of the first question, which is the only way out of a wizard that is
// not an interrupt, and a repository that was never configured must still not
// be.
func TestTheWizardWritesNothingWhenNobodyAgrees(t *testing.T) {
	dir := hookRepo(t)
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/acme/widget.git")

	code, stdout := atTerminal(t, dir, "\x1b", "init")
	if code != 0 {
		t.Fatalf("backing out exited %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "nothing was written") {
		t.Errorf("a wizard nobody finished did not say so:\n%q", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, configFile)); !os.IsNotExist(err) {
		t.Errorf("a wizard nobody finished wrote %s anyway", configFile)
	}
}

// TestTheWizardIsNotDrawnForSomebodyWhoSaidNotToAsk states the other half of
// the gate. --yes is what a person says when they do not want to be asked,
// and it has to work at a terminal, which is the only place the question
// could have been put to them.
func TestTheWizardIsNotDrawnForSomebodyWhoSaidNotToAsk(t *testing.T) {
	dir := hookRepo(t)
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/acme/widget.git")

	code, stdout := atTerminal(t, dir, "", "init", "--yes")
	if code != 0 {
		t.Fatalf("flow init --yes exited %d:\n%s", code, stdout)
	}
	if strings.Contains(stdout, "which forge") {
		t.Errorf("--yes was asked anyway:\n%q", stdout)
	}
	if !strings.Contains(readConfig(t, dir), "preset: lean-kanban") {
		t.Errorf("--yes did not write the default preset:\n%s", readConfig(t, dir))
	}
}

// readConfig is the configuration a run left behind.
func readConfig(t *testing.T, dir string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(dir, configFile))
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}
	return string(source)
}
