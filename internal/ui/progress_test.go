package ui_test

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// progressOf builds the screen the way the root does.
func progressOf(t *testing.T, opts ui.Options) ui.Progress {
	t.Helper()
	return ui.New(opts).Progress()
}

// advance drives the screen the way the program does and reports what it
// settled on, and whether it asked to quit.
func advance(t *testing.T, screen ui.Progress, msgs ...tea.Msg) (ui.Progress, bool) {
	t.Helper()
	var quit bool
	model := tea.Model(screen)
	for _, msg := range msgs {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				quit = true
			}
		}
	}
	final, ok := model.(ui.Progress)
	if !ok {
		t.Fatalf("the screen updated into a %T", model)
	}
	return final, quit
}

// reaching is the step a run reports as it arrives at a check.
func reaching(id string, asked, total int) engine.DoctorStep {
	return engine.DoctorStep{Check: engine.Check{ID: id, Title: id}, Asked: asked, Total: total}
}

// answering is the step a run reports when a check has answered.
func answering(checked engine.Checked, asked, total int) engine.DoctorStep {
	return engine.DoctorStep{
		Check:  engine.Check{ID: checked.ID, Title: checked.Title, Category: checked.Category},
		Asked:  asked,
		Total:  total,
		Result: checked.Result,
	}
}

// walked is the whole of a run as a progress display hears it: each check
// reached and then answered, in the order the report has them.
func walked(report engine.Report) []tea.Msg {
	msgs := make([]tea.Msg, 0, 2*len(report.Checks))
	for i, checked := range report.Checks {
		msgs = append(msgs, reaching(checked.ID, i, len(report.Checks)))
		msgs = append(msgs, answering(checked, i+1, len(report.Checks)))
	}
	return msgs
}

// TestTheScreenNamesWhatItIsAskingNow states what the display is for. A
// doctor run reads a fortnight of pull requests before it prints a word, and
// the question somebody has while they wait is which part is taking the time.
func TestTheScreenNamesWhatItIsAskingNow(t *testing.T) {
	t.Parallel()
	screen, _ := advance(t, progressOf(t, ui.Options{}), reaching("live.prs.linked", 6, 20))

	view := screen.View().Content
	if !strings.Contains(view, "live.prs.linked") {
		t.Errorf("the screen does not say what is being asked:\n%s", view)
	}
	if !strings.Contains(view, "7 of 20") {
		t.Errorf("the screen does not say how far through it is:\n%s", view)
	}
}

// TestABlankRunSaysItHasStarted states what is on the screen before the first
// check is reached. Building the world is the slowest part of some runs, and
// a display that drew nothing until the first check answered would be absent
// for exactly the wait that makes somebody wonder whether flow had hung.
func TestABlankRunSaysItHasStarted(t *testing.T) {
	t.Parallel()
	view := progressOf(t, ui.Options{}).View().Content

	if !strings.Contains(view, "asking") {
		t.Errorf("a run that has reached nothing says nothing:\n%s", view)
	}
	if strings.Contains(view, " of ") {
		t.Errorf("a run that has reached nothing counted something:\n%s", view)
	}
}

// TestAnAnsweredCheckIsTheEnginesOwnLine states that the display adds no
// words. What a finding says, which glyph it gets and what follows it are the
// engine's (spec 12), so a check that has answered reads on this screen
// exactly as it will read in the report that follows.
func TestAnAnsweredCheckIsTheEnginesOwnLine(t *testing.T) {
	t.Parallel()
	report := sampleReport()
	first := report.Checks[0]
	screen, _ := advance(t, progressOf(t, ui.Options{}), walked(engine.Report{Checks: []engine.Checked{first}})...)

	if !strings.Contains(screen.View().Content, first.Line()) {
		t.Errorf("the screen rewrote the engine's line:\n%s\nwant it to contain:\n%s", screen.View().Content, first.Line())
	}
}

// TestOnlyTheLastFewFindingsStayOnScreen states the bound on the display. The
// report that follows carries every finding; what this screen is for is
// seeing the run move, and a run of forty checks that printed forty lines
// would push the shell session it is part of off the top of the window.
func TestOnlyTheLastFewFindingsStayOnScreen(t *testing.T) {
	t.Parallel()
	report := sampleReport()
	if len(report.Checks) < 7 {
		t.Fatalf("the sample has %d checks, too few to overflow the display", len(report.Checks))
	}
	screen, _ := advance(t, progressOf(t, ui.Options{}), walked(report)...)

	view := screen.View().Content
	last := report.Checks[len(report.Checks)-1]
	if !strings.Contains(view, last.ID) {
		t.Errorf("the newest finding is not on the screen:\n%s", view)
	}
	first := report.Checks[0]
	if strings.Contains(view, first.ID) {
		t.Errorf("the oldest finding never left the screen:\n%s", view)
	}
	if got, want := strings.Count(view, "\n"), 5; got != want {
		t.Errorf("the display is %d lines of findings, want %d:\n%s", got, want, view)
	}
}

// TestTheRunEndsWithAnEmptyScreen states what happens when the run is over.
// The report is printed next, into the same scrollback, and a progress
// display left above it would be the same findings twice.
func TestTheRunEndsWithAnEmptyScreen(t *testing.T) {
	t.Parallel()
	screen, quit := advance(t, progressOf(t, ui.Options{}), append(walked(sampleReport()), ui.Finished{})...)

	if got := screen.View().Content; got != "" {
		t.Errorf("the finished screen still draws:\n%s", got)
	}
	if !quit {
		t.Error("the finished screen did not let go of the terminal")
	}
}

// TestNoKeyStopsTheRun states that the display is not a prompt. The run is
// already in flight and this screen cannot stop it; what stops it is ctrl-c
// reaching the context the run was given, which is not this model's to
// answer.
func TestNoKeyStopsTheRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{name: "q", msg: press("q")},
		{name: "escape", msg: named(tea.KeyEscape, 0)},
		{name: "return", msg: named(tea.KeyEnter, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen, quit := advance(t, progressOf(t, ui.Options{}), reaching("forge.protection", 0, 3), tc.msg)
			if quit {
				t.Error("a key press stopped the run")
			}
			if got := screen.View().Content; !strings.Contains(got, "forge.protection") {
				t.Errorf("a key press emptied the screen:\n%s", got)
			}
		})
	}
}

// TestEveryLineFitsTheTerminal states the one layout rule this display has. A
// check's line is one line by construction; a line that folded would make the
// display taller every time it redrew, and a run of thirty checks would
// scroll the session away.
func TestEveryLineFitsTheTerminal(t *testing.T) {
	t.Parallel()
	const width = 40
	screen, _ := advance(t, progressOf(t, ui.Options{}),
		append(append([]tea.Msg{tea.WindowSizeMsg{Width: width, Height: 10}}, walked(sampleReport())...),
			reaching("live.prs.linked", 8, 9))...)

	for _, line := range strings.Split(screen.View().Content, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("a line is %d columns wide in a %d-column terminal: %q", got, width, line)
		}
	}
}

// TestANarrowTerminalKeepsTheCount states what a line being asked gives up
// first. The id is long and the count is the number somebody is watching, so
// the end of the line is the wrong thing to cut.
func TestANarrowTerminalKeepsTheCount(t *testing.T) {
	t.Parallel()
	screen, _ := advance(t, progressOf(t, ui.Options{}),
		tea.WindowSizeMsg{Width: 28, Height: 10}, reaching("live.transitions.flow", 3, 9))

	if got := screen.View().Content; !strings.Contains(got, "4 of 9") {
		t.Errorf("the narrow screen lost the count:\n%s", got)
	}
}

// TestTheDisplayIsDrawnInASCIIWhenItIsAskedTo states that --ascii reaches the
// spinner too. A terminal that cannot draw a glyph is a terminal that cannot
// draw a braille spinner, and a mark that renders as boxes beside every line
// is worse than no mark at all.
func TestTheDisplayIsDrawnInASCIIWhenItIsAskedTo(t *testing.T) {
	t.Parallel()
	screen, _ := advance(t, progressOf(t, ui.Options{ASCII: true}), walked(sampleReport())...)

	view := screen.View().Content
	for _, unicode := range []string{"✓", "⚠", "✗", "✎", "↻", "·", "…", "⠋", "⠙", "⠹"} {
		if strings.Contains(view, unicode) {
			t.Errorf("an ASCII display still contains %q:\n%s", unicode, view)
		}
	}
}

// TestColourOnlyAddsColourToTheProgress states for the display what the text
// renderer promises for every document (spec 12): the styled screen with the
// escape sequences taken off is the plain one, character for character.
func TestColourOnlyAddsColourToTheProgress(t *testing.T) {
	t.Parallel()
	msgs := walked(sampleReport())
	plainScreen, _ := advance(t, progressOf(t, ui.Options{}), msgs...)
	colouredScreen, _ := advance(t, progressOf(t, ui.Options{Colour: true, Dark: true}), msgs...)
	plain, coloured := plainScreen.View().Content, colouredScreen.View().Content

	if !strings.Contains(coloured, "\x1b[") {
		t.Fatal("colour was asked for and none was painted, so the test proves nothing")
	}
	var out bytes.Buffer
	if err := ui.Fprint(&out, coloured); err != nil {
		t.Fatalf("print: %v", err)
	}
	if out.String() != plain {
		t.Errorf("the coloured display is laid out differently:\n--- stripped\n%s\n--- plain\n%s", out.String(), plain)
	}
}

// Ctrl-c on the progress screen is passed on as an interrupt (issue #289).
// The screen holds the terminal in raw mode, where ctrl-c is a key and not a
// signal; a screen that ignored it was a run nobody could stop. Other keys are
// still ignored: the run is already happening.
func TestCtrlCOnTheProgressScreenInterrupts(t *testing.T) {
	screen := progressOf(t, ui.Options{})
	_, cmd := screen.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl-c did nothing")
	}
	if _, ok := cmd().(tea.InterruptMsg); !ok {
		t.Errorf("ctrl-c produced %T, want an interrupt", cmd())
	}
	if _, cmd := screen.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil {
		t.Errorf("q produced %T, want nothing", cmd())
	}
}

// Once the last check has answered there is no next one to count (issue
// #289): the screen said "44 of 43" while it waited to be told to leave.
func TestTheCountNeverPassesItsTotal(t *testing.T) {
	screen, _ := advance(t, progressOf(t, ui.Options{}),
		reaching("live.agents.rework", 42, 43),
		answering(engine.Checked{ID: "live.agents.rework", Title: "live.agents.rework"}, 43, 43))
	view := screen.View().Content
	if strings.Contains(view, "44 of 43") || !strings.Contains(view, "43 of 43") {
		t.Errorf("the count after the last answer reads:\n%s", view)
	}
}
