package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// Whether flow draws a screen or prints a document (spec 12), and what comes
// back when it draws one.
//
// The terminal check is handed in rather than asked of the real world: a test
// is never a terminal, so a decision that consulted one could only ever go
// the same way, and the half of the rule that matters is the half that
// happens when both ends really are terminals.

// terminals makes flow believe exactly the streams named are terminals, for
// the length of one test.
func terminals(t *testing.T, streams ...any) {
	t.Helper()
	was := terminalCheck
	t.Cleanup(func() { terminalCheck = was })
	terminalCheck = func(stream any) bool {
		for _, named := range streams {
			if named == stream {
				return true
			}
		}
		return false
	}
}

// TestAScreenNeedsBothEndsOfATerminal states every reason flow declines to
// draw one. Each is a different kind of reason — a person said not to, a
// machine is reading, the terminal has said it cannot — and every one of them
// has a plain equivalent that still asks the question, so declining to draw
// never means declining to ask.
func TestAScreenNeedsBothEndsOfATerminal(t *testing.T) {
	stdin, stdout := io.Reader(strings.NewReader("")), io.Writer(&bytes.Buffer{})
	cases := []struct {
		name  string
		flags CLI
		env   map[string]string
		ttys  []any
		draws bool
	}{
		{name: "a person at a terminal", ttys: []any{stdin, stdout}, draws: true},
		{name: "output going somewhere else", ttys: []any{stdin}},
		{name: "input coming from somewhere else", ttys: []any{stdout}},
		{name: "neither end is a terminal"},
		{name: "--json", flags: CLI{JSON: true}, ttys: []any{stdin, stdout}},
		{name: "--non-interactive", flags: CLI{NonInteractive: true}, ttys: []any{stdin, stdout}},
		{name: "a CI job", env: map[string]string{"CI": "true"}, ttys: []any{stdin, stdout}},
		{name: "a dumb terminal", env: map[string]string{"TERM": "dumb"}, ttys: []any{stdin, stdout}},
		{name: "a terminal shouting it is dumb", env: map[string]string{"TERM": "DUMB"}, ttys: []any{stdin, stdout}},
		{name: "a terminal that is not dumb", env: map[string]string{"TERM": "xterm-256color"}, ttys: []any{stdin, stdout}, draws: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("TERM", "")
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			terminals(t, tc.ttys...)
			flags := tc.flags
			app := &Context{CLI: &flags, Stdin: stdin, Stdout: stdout}
			if got := app.tui(); got != tc.draws {
				t.Errorf("draws a screen = %v, want %v", got, tc.draws)
			}
		})
	}
}

// TestTheQuestionIsAskedWhicheverWayItIsDrawn states that the narrower rule
// never swallows the question: anywhere flow will not draw a screen but will
// still talk to a person, the plain prompt is what is left.
func TestTheQuestionIsAskedWhicheverWayItIsDrawn(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("TERM", "")
	stdin, stdout := io.Reader(strings.NewReader("")), io.Writer(&bytes.Buffer{})
	terminals(t, stdin, stdout)
	app := &Context{CLI: &CLI{JSON: true}, Stdin: stdin, Stdout: stdout}

	if app.tui() {
		t.Fatal("--json drew a screen")
	}
	if !app.interactive() {
		t.Fatal("--json stopped flow asking at all, so there is no plain equivalent")
	}
}

// TestWhatAPersonPressesIsWhatTheApplyDoes runs the screen the way a terminal
// does — a real program with a reader and a writer behind it — and states
// that the key reaches the decision. The model's own rules are held to in
// internal/ui; what is proved here is the wiring, that flow reads the answer
// back out of the model the program hands it rather than out of the bytes it
// put in.
func TestWhatAPersonPressesIsWhatTheApplyDoes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typed    string
		approved bool
	}{
		{name: "y applies", typed: "y", approved: true},
		{name: "n does not", typed: "n"},
		{name: "a stray key and then no does not", typed: "xn"},
		{name: "a stray key and then yes does", typed: "xy", approved: true},
		{name: "a second key after no does not", typed: "ny", approved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := &Context{
				Ctx:    context.Background(),
				CLI:    &CLI{},
				Stdin:  strings.NewReader(tc.typed),
				Stdout: &out,
			}
			screen := ui.New(ui.Options{}).Confirmation(engine.Plan{}, "apply 1 change?")
			approved, err := app.onScreen(screen)
			if err != nil {
				t.Fatalf("ask on screen: %v", err)
			}
			if approved != tc.approved {
				t.Errorf("approved = %v, want %v", approved, tc.approved)
			}
			if out.Len() == 0 {
				t.Error("the screen wrote nothing to the terminal it was given")
			}
		})
	}
}

// TestAQuestionNobodyCouldAnswerApprovesNothing states what happens when the
// run ends while the question is still on screen — the deadline expiring, the
// process being told to stop. Nothing was agreed to, so nothing may be
// applied, and the reason says so rather than being reported as a person
// having answered no.
//
// Ctrl-c is not this case: it is one of the screen's own keys, and a person
// pressing it is a refusal that arrives through [ui.Confirmation.Update].
func TestAQuestionNobodyCouldAnswerApprovesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	app := &Context{Ctx: ctx, CLI: &CLI{}, Stdin: strings.NewReader(""), Stdout: &out}
	approved, err := app.onScreen(ui.New(ui.Options{}).Confirmation(engine.Plan{}, "apply 1 change?"))
	if approved {
		t.Error("a cancelled run approved the apply")
	}
	if err == nil {
		t.Error("a cancelled run reported nothing at all")
	}
}
