package ui_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// recorder is a flow that remembers what the screen asked it for.
type recorder struct {
	// runs is the fix flag of every run asked for, in order.
	runs []bool
	// attestations is what was vouched for, as "check: note".
	attestations []string
	// report is what a run answers with, and runErr why it did not.
	report    engine.Report
	runErr    error
	attestErr error
}

// Doctor implements [ui.Runner].
func (r *recorder) Doctor(fix bool) (engine.Report, error) {
	r.runs = append(r.runs, fix)
	return r.report, r.runErr
}

// Attest implements [ui.Runner].
func (r *recorder) Attest(check, note string) error {
	r.attestations = append(r.attestations, check+": "+note)
	return r.attestErr
}

// watching builds the screen the way the root does, with a terminal's first
// size already delivered, and returns the flow it is driving.
func watching(t *testing.T, opts ui.Options) (ui.Watch, *recorder) {
	t.Helper()
	flow := &recorder{report: sampleReport()}
	screen := ui.New(opts).Watch(flow)
	return screen, flow
}

// drive delivers messages to the screen and returns where it got to, with
// whatever the commands it produced answered — a run and an attestation both
// arrive that way, and a harness that dropped them would be testing a screen
// nothing ever happened to.
//
// Two things it does not follow. The refresh clock answers after a minute, so
// a test that waited for it would wait a minute; and the spinner asks to be
// told the time again for ever, which is a cycle rather than a consequence.
func drive(t *testing.T, screen ui.Watch, msgs ...tea.Msg) (ui.Watch, bool) {
	t.Helper()
	var quit bool
	model := tea.Model(screen)

	var send func(msg tea.Msg)
	send = func(msg tea.Msg) {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		for _, produced := range answers(t, cmd) {
			switch produced.(type) {
			case tea.QuitMsg:
				quit = true
			case spinner.TickMsg:
			default:
				send(produced)
			}
		}
	}
	for _, msg := range msgs {
		send(msg)
	}

	final, ok := model.(ui.Watch)
	if !ok {
		t.Fatalf("the screen updated into a %T", model)
	}
	return final, quit
}

// answers runs a command and returns what it produced, flattening a batch.
//
// It waits only long enough for a command that answers by calling something:
// a run and an attestation both do, and everything else this screen produces
// is a timer — the refresh clock, the spinner, the cursor blink — which is a
// cycle rather than a consequence and would never finish being followed.
func answers(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	answered := make(chan tea.Msg, 1)
	go func() { answered <- cmd() }()
	select {
	case msg := <-answered:
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			return []tea.Msg{msg}
		}
		var msgs []tea.Msg
		for _, each := range batch {
			msgs = append(msgs, answers(t, each)...)
		}
		return msgs
	case <-time.After(callWait):
		return nil
	}
}

// callWait is how long a command gets to answer before it is taken for a
// timer. Well under the shortest interval any of this screen's timers waits
// out, and far over a function call.
const callWait = 20 * time.Millisecond

// started is the screen with its first run already answered, which is what
// every key is pressed against.
func started(t *testing.T, opts ui.Options) (ui.Watch, *recorder) {
	t.Helper()
	screen, flow := watching(t, opts)
	screen, _ = drive(t, screen, answers(t, screen.Init())...)
	return screen, flow
}

// attestable is the check in the sample report that is waiting on a human's
// word, and index is where it sits in the list.
const (
	attestable      = "tracker.saved_view"
	attestableIndex = 5
)

// down is the keys that move the cursor onto a row.
func down(n int) []tea.Msg {
	msgs := make([]tea.Msg, 0, n)
	for range n {
		msgs = append(msgs, press("j"))
	}
	return msgs
}

// TestTheScreenAsksAsSoonAsItOpens states that the screen does not wait for a
// key. Somebody who typed `flow doctor --watch` has already asked the
// question, and a screen that opened empty until they pressed r would be a
// menu.
func TestTheScreenAsksAsSoonAsItOpens(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})

	if got := flow.runs; len(got) != 1 || got[0] {
		t.Errorf("the screen opened with runs %v, want one run that does not fix", got)
	}
	if got := screen.Report().Summary(); got != sampleReport().Summary() {
		t.Errorf("the screen is showing %q, want the run it asked for", got)
	}
	view := screen.View().Content
	if !strings.Contains(view, sampleReport().Summary()) {
		t.Errorf("the screen does not say how the repository stands:\n%s", view)
	}
	for _, checked := range sampleReport().Checks[:4] {
		if !strings.Contains(view, checked.ID) {
			t.Errorf("%s is not on the screen:\n%s", checked.ID, view)
		}
	}
}

// TestTheScreenTakesTheTerminal states the one thing that makes this screen
// different from everything else flow draws. It is a display of something
// that keeps changing rather than a document, so it goes on the alternate
// screen and leaves the scrollback as it found it.
func TestTheScreenTakesTheTerminal(t *testing.T) {
	t.Parallel()
	screen, _ := started(t, ui.Options{})
	if !screen.View().AltScreen {
		t.Error("the watch screen does not take the alternate screen")
	}
}

// TestEachKeyDoesWhatTheKeyLineSays states the bindings. These are the whole
// of what a person can do on this screen, and spec 12 §3 names them.
func TestEachKeyDoesWhatTheKeyLineSays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		msgs []tea.Msg
		runs []bool
		quit bool
	}{
		{name: "r asks again", msgs: []tea.Msg{press("r")}, runs: []bool{false, false}},
		{name: "f asks again and fixes what it can", msgs: []tea.Msg{press("f")}, runs: []bool{false, true}},
		{name: "q lets go of the terminal", msgs: []tea.Msg{press("q")}, runs: []bool{false}, quit: true},
		{name: "escape lets go of the terminal", msgs: []tea.Msg{named(tea.KeyEscape, 0)}, runs: []bool{false}, quit: true},
		{name: "ctrl-c lets go of the terminal", msgs: []tea.Msg{named('c', tea.ModCtrl)}, runs: []bool{false}, quit: true},
		{name: "a letter that is not a key asks nothing", msgs: []tea.Msg{press("z")}, runs: []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen, flow := started(t, ui.Options{})
			_, quit := drive(t, screen, tc.msgs...)

			if got := flow.runs; !equalBools(got, tc.runs) {
				t.Errorf("the flow was asked for runs %v, want %v", got, tc.runs)
			}
			if quit != tc.quit {
				t.Errorf("asked to quit = %v, want %v", quit, tc.quit)
			}
		})
	}
}

// equalBools reports whether two run logs are the same.
func equalBools(got, want []bool) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestOnlyOneRunAtATime states what stops a key from costing a forge a second
// fortnight of pull requests. Two runs answering the same question would also
// race to put their answer on the screen, and the slower one would win.
func TestOnlyOneRunAtATime(t *testing.T) {
	t.Parallel()
	screen, flow := watching(t, ui.Options{})
	// The opening run is left in flight: its command is never delivered, so
	// nothing has answered when the key arrives.
	screen, _ = drive(t, screen, press("r"))

	if got := len(flow.runs); got != 0 {
		t.Errorf("%d runs were started while one was in flight, want none", got)
	}
	if view := screen.View().Content; !strings.Contains(view, "already asking") {
		t.Errorf("the screen did not say why nothing happened:\n%s", view)
	}
}

// TestARunThatWentWrongKeepsWhatWasTrue states what a failure does to the
// display. The findings on the screen were true a minute ago, and clearing
// them would replace something known with nothing; the screen says the asking
// went wrong and leaves them where they are.
func TestARunThatWentWrongKeepsWhatWasTrue(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	// A run that failed answers with no report at all, which is what makes
	// keeping the last one a decision rather than an accident.
	flow.report, flow.runErr = engine.Report{}, errors.New("the forge refused the credential")
	screen, _ = drive(t, screen, press("r"))

	view := screen.View().Content
	if !strings.Contains(view, "the forge refused the credential") {
		t.Errorf("the screen does not say the run went wrong:\n%s", view)
	}
	if !strings.Contains(view, "forge.protection") {
		t.Errorf("a failed run erased the findings it could not replace:\n%s", view)
	}
	if got := screen.Report().Summary(); got != sampleReport().Summary() {
		t.Errorf("the screen is exiting on %q, want the last run that finished", got)
	}
}

// TestThePaneShowsTheCheckUnderTheCursor states what the second half of the
// screen is for: one finding in full, with everything it saw and what to do
// about it, which is more than the list's one line per check can hold.
func TestThePaneShowsTheCheckUnderTheCursor(t *testing.T) {
	t.Parallel()
	screen, _ := started(t, ui.Options{})
	first := sampleReport().Checks[0]

	view := screen.View().Content
	if !strings.Contains(view, first.Title) {
		t.Errorf("the pane does not name the question %s answers:\n%s", first.ID, view)
	}

	moved, _ := drive(t, screen, press("j"))
	second := sampleReport().Checks[1]
	if !strings.Contains(moved.View().Content, second.Result.Remediation.Command) {
		t.Errorf("the pane did not follow the cursor onto %s:\n%s", second.ID, moved.View().Content)
	}
}

// TestTheCursorStaysOnTheCheckItWasOn states what a refresh must not do. The
// screen asks again every minute, and a cursor that jumped back to the top
// would take somebody off the finding they were in the middle of fixing.
func TestTheCursorStaysOnTheCheckItWasOn(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, down(attestableIndex)...)

	// The run that answers next has the same checks in a different order,
	// which is what a cursor that remembered an index rather than a check
	// would get wrong.
	shuffled := sampleReport()
	shuffled.Checks = append(shuffled.Checks[2:], shuffled.Checks[:2]...)
	flow.report = shuffled
	screen, _ = drive(t, screen, press("r"))

	screen, _ = drive(t, screen, press("a"))
	if view := screen.View().Content; !strings.Contains(view, "why does "+attestable+" hold?") {
		t.Errorf("the cursor did not stay on %s:\n%s", attestable, view)
	}
}

// TestOnlyACheckWaitingOnSomebodysWordMayBeAttested states the rule that
// keeps the key honest. Which checks may be attested is the engine's
// judgement (spec 07 §8); a screen that let somebody vouch for anything would
// be a way to silence a failure flow could have proved.
func TestOnlyACheckWaitingOnSomebodysWordMayBeAttested(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, press("a"))

	view := screen.View().Content
	if !strings.Contains(view, "config.syntax is not waiting on anybody's word") {
		t.Errorf("the screen offered to attest a check flow answered itself:\n%s", view)
	}
	screen, _ = drive(t, screen, append(typing("it holds"), named(tea.KeyEnter, 0))...)
	if got := flow.attestations; len(got) != 0 {
		t.Errorf("something was attested: %v", got)
	}
	if view := screen.View().Content; strings.Contains(view, "it holds") {
		t.Errorf("the screen took a note against a check that may not be attested:\n%s", view)
	}
}

// TestAnAttestationIsWrittenAndTheQuestionAskedAgain states the whole of what
// the key does. A promise that landed changes what the checks would say, so
// the screen asks them again rather than leaving a red line on the display
// until the clock comes round.
func TestAnAttestationIsWrittenAndTheQuestionAskedAgain(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, down(attestableIndex)...)
	screen, _ = drive(t, screen, press("a"))

	if view := screen.View().Content; !strings.Contains(view, "why does "+attestable+" hold?") {
		t.Errorf("the screen did not ask what is being vouched for:\n%s", view)
	}
	screen, _ = drive(t, screen, append(typing("the view is there, I looked"), named(tea.KeyEnter, 0))...)

	want := attestable + ": the view is there, I looked"
	if got := flow.attestations; len(got) != 1 || got[0] != want {
		t.Errorf("the flow recorded %v, want [%q]", got, want)
	}
	if got := flow.runs; len(got) != 2 || got[1] {
		t.Errorf("the flow was asked for runs %v, want the attestation followed by a plain run", got)
	}
	if view := screen.View().Content; !strings.Contains(view, attestable+" is attested") {
		t.Errorf("the screen did not say the promise landed:\n%s", view)
	}
}

// TestAnAttestationThatWasNotWrittenSaysSo states that a refused promise is
// not a silent one. The lock is on disk and the note was typed; a screen that
// went back to the findings as though it had been recorded would be the one
// case where flow says nothing about work it did not do.
func TestAnAttestationThatWasNotWrittenSaysSo(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	flow.attestErr = errors.New("the lock is read-only")
	screen, _ = drive(t, screen, down(attestableIndex)...)
	screen, _ = drive(t, screen, press("a"))
	screen, _ = drive(t, screen, append(typing("I looked"), named(tea.KeyEnter, 0))...)

	if view := screen.View().Content; !strings.Contains(view, attestable+" was not attested: the lock is read-only") {
		t.Errorf("the screen hid the refusal:\n%s", view)
	}
	if got := flow.runs; len(got) != 1 {
		t.Errorf("the flow was asked for runs %v, want nothing after a promise that did not land", got)
	}
}

// TestAnEmptyNoteIsNotAnAttestation states what an attestation is. It is a
// record of what somebody confirmed and how (spec 07 §8); an empty one
// vouches for nothing, so the question stays open rather than being answered
// by a return.
func TestAnEmptyNoteIsNotAnAttestation(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, down(attestableIndex)...)
	screen, _ = drive(t, screen, press("a"), named(tea.KeyEnter, 0))

	if got := flow.attestations; len(got) != 0 {
		t.Errorf("an empty note was recorded: %v", got)
	}
	view := screen.View().Content
	if !strings.Contains(view, "an attestation has to say what was confirmed") {
		t.Errorf("the screen did not say why nothing was recorded:\n%s", view)
	}
	if !strings.Contains(view, "why does "+attestable+" hold?") {
		t.Errorf("the screen stopped asking:\n%s", view)
	}
}

// TestEveryKeyBelongsToTheNoteWhileOneIsBeingTyped states the rule that makes
// the note usable. A person writing "flagged by hand" must not have the f
// taken as fix, and the only keys that mean anything else are the two that
// settle the note.
func TestEveryKeyBelongsToTheNoteWhileOneIsBeingTyped(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, down(attestableIndex)...)
	screen, _ = drive(t, screen, press("a"))
	screen, _ = drive(t, screen, typing("flagged by hand, requires a rerun")...)

	if got := flow.runs; len(got) != 1 {
		t.Errorf("typing a note asked for runs %v, want only the opening one", got)
	}
	_, quit := drive(t, screen, named(tea.KeyEnter, 0))
	if quit {
		t.Error("a q in the note let go of the terminal")
	}
	want := attestable + ": flagged by hand, requires a rerun"
	if got := flow.attestations; len(got) != 1 || got[0] != want {
		t.Errorf("the flow recorded %v, want [%q]", got, want)
	}
}

// TestEscapeAbandonsTheNote states how somebody gets out of a question they
// did not mean to ask. Escape quits this screen everywhere else, so a note
// that swallowed it would be a trap; a note that recorded what had been typed
// so far would be a promise nobody made.
func TestEscapeAbandonsTheNote(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	screen, _ = drive(t, screen, down(attestableIndex)...)
	screen, _ = drive(t, screen, press("a"))
	screen, quit := drive(t, screen, append(typing("half a thought"), named(tea.KeyEscape, 0))...)

	if quit {
		t.Error("escape in a note let go of the terminal")
	}
	if got := flow.attestations; len(got) != 0 {
		t.Errorf("an abandoned note was recorded: %v", got)
	}
	view := screen.View().Content
	if !strings.Contains(view, "nothing was attested") {
		t.Errorf("the screen did not say the note was abandoned:\n%s", view)
	}
	if strings.Contains(view, "why does") {
		t.Errorf("the screen is still asking for a note:\n%s", view)
	}
	// What was abandoned is gone, rather than waiting in the input for the
	// next check somebody opens a note on.
	screen, _ = drive(t, screen, press("a"))
	if view := screen.View().Content; strings.Contains(view, "half a thought") {
		t.Errorf("the next note opened with the abandoned one still in it:\n%s", view)
	}
}

// TestAReportWithNothingInItEmptiesThePane states what a narrowed run does to
// the half of the screen that was showing a check. `--check` can answer about
// one check or about none, and a pane still showing the last run's finding
// would be the screen asserting something nobody asked it.
func TestAReportWithNothingInItEmptiesThePane(t *testing.T) {
	t.Parallel()
	screen, flow := started(t, ui.Options{})
	first := sampleReport().Checks[0]
	if !strings.Contains(screen.View().Content, first.Title) {
		t.Fatalf("the pane never showed %s, so emptying it proves nothing", first.ID)
	}

	flow.report = engine.Report{GeneratedAt: time.Date(2026, 3, 1, 9, 31, 0, 0, time.UTC)}
	screen, _ = drive(t, screen, press("r"))

	if view := screen.View().Content; strings.Contains(view, first.Title) {
		t.Errorf("a run that answered about nothing left %s in the pane:\n%s", first.ID, view)
	}
}

// TestTheFindingsGetMoreOfTheScreenThanThePane states which half of the
// screen is the point of it. The list is for seeing what is wrong and the
// pane is for the one finding being worked on, so a split that showed a
// check's whole evidence and four findings would be the wrong way round.
func TestTheFindingsGetMoreOfTheScreenThanThePane(t *testing.T) {
	t.Parallel()
	screen, _ := started(t, ui.Options{})
	screen, _ = drive(t, screen, tea.WindowSizeMsg{Width: 80, Height: 14})

	// Two lines go to the summary and two to the keys, which leaves ten for
	// the findings and the pane. Six of them are findings.
	view := screen.View().Content
	shown := 0
	for _, checked := range sampleReport().Checks {
		if strings.Contains(view, checked.ID) {
			shown++
		}
	}
	if want := 5; shown != want {
		t.Errorf("%d findings are on a 14-line screen, want %d:\n%s", shown, want, view)
	}
}

// TestTheScreenFitsTheTerminalItIsToldAbout states that the layout is the
// terminal's size and not a guess. A screen taller than the window scrolls
// its own key line out of view, which is where the keys are named.
func TestTheScreenFitsTheTerminalItIsToldAbout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{name: "a small window", width: 60, height: 16},
		{name: "a tall window", width: 100, height: 40},
		{name: "a window with barely any room", width: 40, height: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen, _ := started(t, ui.Options{})
			screen, _ = drive(t, screen, tea.WindowSizeMsg{Width: tc.width, Height: tc.height})

			if got := strings.Count(screen.View().Content, "\n") + 1; got > tc.height {
				t.Errorf("the screen is %d lines in a %d-line terminal:\n%s", got, tc.height, screen.View().Content)
			}
			if !strings.Contains(screen.View().Content, "r rerun") {
				t.Errorf("the key line is not on the screen:\n%s", screen.View().Content)
			}
		})
	}
}

// TestTheWatchScreenIsDrawnInASCIIWhenItIsAskedTo states that --ascii reaches the
// one screen that redraws itself. A terminal that cannot draw a glyph will
// not learn to while the window is open.
func TestTheWatchScreenIsDrawnInASCIIWhenItIsAskedTo(t *testing.T) {
	t.Parallel()
	screen, _ := started(t, ui.Options{ASCII: true})

	view := screen.View().Content
	for _, unicode := range []string{"✓", "⚠", "✗", "✎", "↻", "→", "·", "…"} {
		if strings.Contains(view, unicode) {
			t.Errorf("an ASCII screen still contains %q:\n%s", unicode, view)
		}
	}
}

// TestColourOnlyAddsColourToTheWatchScreen states for the watch screen what
// the text renderer promises for every document (spec 12): the styled screen
// with the escape sequences taken off is the plain one, character for
// character.
func TestColourOnlyAddsColourToTheWatchScreen(t *testing.T) {
	t.Parallel()
	plainScreen, _ := started(t, ui.Options{})
	colouredScreen, _ := started(t, ui.Options{Colour: true, Dark: true})
	plain, coloured := plainScreen.View().Content, colouredScreen.View().Content

	if !strings.Contains(coloured, "\x1b[") {
		t.Fatal("colour was asked for and none was painted, so the test proves nothing")
	}
	var out bytes.Buffer
	if err := ui.Fprint(&out, coloured); err != nil {
		t.Fatalf("print: %v", err)
	}
	if out.String() != plain {
		t.Errorf("the coloured screen is laid out differently:\n--- stripped\n%s\n--- plain\n%s", out.String(), plain)
	}
}

// TestAScreenWithNoFlowBehindItDrawsAnyway states that the model is drawable
// on its own. It is what a test of the layout does, and a screen that
// dereferenced a runner it was never given would be a panic in the one place
// flow has taken the whole terminal over.
func TestAScreenWithNoFlowBehindItDrawsAnyway(t *testing.T) {
	t.Parallel()
	screen := ui.New(ui.Options{}).Watch(nil)
	screen, _ = drive(t, screen, answers(t, screen.Init())...)
	screen, _ = drive(t, screen, press("r"), press("f"), press("a"))

	if view := screen.View().Content; !strings.Contains(view, "asking") {
		t.Errorf("a screen with nothing behind it does not say so:\n%s", view)
	}
}
