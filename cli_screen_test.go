package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/core"
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
			answered, err := onScreen(app, "ask for confirmation", screen)
			if err != nil {
				t.Fatalf("ask on screen: %v", err)
			}
			if approved := answered.Approved(); approved != tc.approved {
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
	answered, err := onScreen(app, "ask for confirmation", ui.New(ui.Options{}).Confirmation(engine.Plan{}, "apply 1 change?"))
	if answered.Approved() {
		t.Error("a cancelled run approved the apply")
	}
	if err == nil {
		t.Error("a cancelled run reported nothing at all")
	}
}

// TestWhatAPersonPicksIsWhatTheRunResolves is the picker's half of the same
// wiring: that the candidate a key press lands on is the candidate the engine
// is told about, and that a person who declines to pick leaves the
// placeholder a question rather than answering it badly.
func TestWhatAPersonPicksIsWhatTheRunResolves(t *testing.T) {
	candidates := []core.Candidate{
		{What: core.ItemTrackerProject, ID: "PVT_1", Name: "Delivery"},
		{What: core.ItemTrackerProject, ID: "PVT_2", Name: "Platform"},
	}
	for _, tc := range []struct {
		name   string
		typed  string
		wantID string
	}{
		{name: "enter takes the one under the cursor", typed: "\r", wantID: "PVT_1"},
		{name: "a filter moves the cursor first", typed: "/plat\r\r", wantID: "PVT_2"},
		{name: "quitting picks nothing", typed: "q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("TERM", "")
			var out bytes.Buffer
			stdin := io.Reader(strings.NewReader(tc.typed))
			stdout := io.Writer(&out)
			terminals(t, stdin, stdout)
			app := &Context{Ctx: context.Background(), CLI: &CLI{}, Stdin: stdin, Stdout: stdout}

			choose := app.chooser(&session{})
			if choose == nil {
				t.Fatal("a person at a terminal was offered no picker")
			}
			picked, err := choose(context.Background(), engine.Placeholder{Path: core.ItemTrackerProject}, candidates)
			if tc.wantID == "" {
				if err == nil {
					t.Fatalf("picking nothing resolved %q", picked.ID)
				}
				if !errors.Is(err, core.ErrNeedsInput) {
					t.Errorf("picking nothing failed with %v, want a request for input", err)
				}
				var user *core.UserError
				if errors.As(err, &user) && !strings.Contains(user.Fix, "--set "+core.ItemTrackerProject) {
					t.Errorf("the way out is %q, which does not say how to answer by hand", user.Fix)
				}
				return
			}
			if err != nil {
				t.Fatalf("ask which to use: %v", err)
			}
			if picked.ID != tc.wantID {
				t.Errorf("picked %q, want %q", picked.ID, tc.wantID)
			}
		})
	}
}

// TestWithNoTerminalThereIsNobodyToAsk states that flow does not offer a
// picker it cannot draw. The engine reads a nil chooser as a rung of the
// ladder it cannot stand on and falls through to naming the candidates in the
// report, which is the whole of the plain equivalent (spec 12 §2).
func TestWithNoTerminalThereIsNobodyToAsk(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("TERM", "")
	stdin, stdout := io.Reader(strings.NewReader("")), io.Writer(&bytes.Buffer{})
	terminals(t)
	app := &Context{Ctx: context.Background(), CLI: &CLI{}, Stdin: stdin, Stdout: stdout}

	if app.chooser(&session{}) != nil {
		t.Error("flow offered to draw a picker on something that is not a terminal")
	}
}

// quitsWhenTold is a screen that leaves only when it is told the work is over,
// which is what the progress screen does.
type quitsWhenTold struct{}

func (quitsWhenTold) Init() tea.Cmd { return nil }

func (q quitsWhenTold) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, over := msg.(ui.Finished); over {
		return q, tea.Quit
	}
	return q, nil
}

func (quitsWhenTold) View() tea.View { return tea.NewView("") }

// TestAScreenIsToldTheWorkIsOver is issue #289. `flow doctor` on a terminal
// answered every check and then spun on the last one for ever: whileWorking
// waited for the screen to leave, and nothing told it to. The screen's own
// test sent Finished by hand, so it proved the screen leaves when told and
// nothing proved anybody told it.
func TestAScreenIsToldTheWorkIsOver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app := &Context{CLI: &CLI{}, Ctx: ctx, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}

	left := make(chan error, 1)
	go func() { left <- app.whileWorking(quitsWhenTold{}, func(context.Context, func(tea.Msg)) {}) }()

	select {
	case err := <-left:
		if err != nil {
			t.Fatalf("the screen left with an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the work was over and the screen was never told, so it never let go of the terminal")
	}
}

// interrupts is a screen somebody pressed ctrl-c on as soon as it appeared.
type interrupts struct{}

func (interrupts) Init() tea.Cmd                         { return tea.Interrupt }
func (i interrupts) Update(tea.Msg) (tea.Model, tea.Cmd) { return i, nil }
func (interrupts) View() tea.View                        { return tea.NewView("") }

// TestCtrlCOnAScreenStopsTheWork is the other half of issue #289. A screen
// holds the terminal in raw mode, where ctrl-c is a key and not a signal, so
// the run kept going and the screen could not be left at all. Pressing it now
// cancels the work and is reported as an interrupt, as the signal would be.
func TestCtrlCOnAScreenStopsTheWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app := &Context{CLI: &CLI{}, Ctx: ctx, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}

	stopped := make(chan error, 1)
	go func() {
		stopped <- app.whileWorking(interrupts{}, func(work context.Context, _ func(tea.Msg)) {
			<-work.Done() // a run that only ends when it is told to
		})
	}()

	select {
	case err := <-stopped:
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("whileWorking = %v, want it to say the run was interrupted", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctrl-c on the screen did not stop the work")
	}
}
