package ui_test

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/ui"
)

// The init wizard (spec 12 §1). It is driven here the way a terminal drives
// it — one key at a time through Update — because that is the only way to
// hold a screen to what it does with a key press rather than to what its
// author believed it did.
//
// What it collects is the flags of spec 02 §2 and nothing else, so most of
// what is stated here is about the answers: which questions a configuration
// has, what each of them opens on, and what survives going back.

// aWizard is a screen started from what flow detected, with a preview that
// always succeeds.
func aWizard(t *testing.T, detected ui.Answers) ui.Wizard {
	t.Helper()
	return ui.New(ui.Options{}).Wizard(detected,
		func(a ui.Answers) ([]byte, error) {
			return []byte("preset: " + a.Preset + "\nproject: " + a.Project + "\n"), nil
		})
}

// press puts keys to a screen in order and hands back what it became.
func pressing(t *testing.T, w ui.Wizard, keys ...string) ui.Wizard {
	t.Helper()
	var model tea.Model = w
	for _, key := range keys {
		model, _ = model.Update(wizardKey(key))
		next, ok := model.(ui.Wizard)
		if !ok {
			t.Fatalf("the screen came back as %T", model)
		}
		w = next
	}
	return w
}

// keyPress is one key as the terminal would deliver it.
func wizardKey(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}

// typed is one key per rune, for a step that is a field.
func typed(word string) []string {
	var keys []string
	for _, letter := range word {
		keys = append(keys, string(letter))
	}
	return keys
}

// aWholeSession is a GitHub Projects session answered all the way through:
// accept the forge flow detected and the tracker its preset names, type an
// owner and a board, drop to the second preset, and write it.
var aWholeSession = func() []string {
	keys := append([]string{"enter", "enter"}, typed("acme")...)
	keys = append(keys, "enter")
	keys = append(keys, typed("7")...)
	return append(keys, "enter", "down", "enter", "y")
}()

// everyQuestion is enter once per question of a GitHub Projects
// configuration, which leaves the screen on the review.
var everyQuestion = []string{"enter", "enter", "enter", "enter", "enter"}

// detected is the ordinary starting point: a GitHub repository, the preset
// flow uses when nobody says otherwise, and that preset's own tracker. It is
// what the command works out before it draws anything, so the answers start
// there and the review's command line is shortened against it.
var detected = ui.Answers{Preset: "lean-kanban", Forge: "github", Tracker: "github-projects"}

// TestTheWizardCollectsTheFlags walks the whole screen the way somebody
// would, and states what it comes back with. Every field is a flag, which is
// the property the plain equivalent rests on.
func TestTheWizardCollectsTheFlags(t *testing.T) {
	w := pressing(t, aWizard(t, detected), aWholeSession...)
	if !w.Confirmed() {
		t.Fatal("the screen was answered and did not say so")
	}
	want := ui.Answers{
		Preset:  "deploy-done",
		Forge:   "github",
		Tracker: "github-projects",
		Owner:   "acme",
		Project: "7",
	}
	if got := w.Answers(); got != want {
		t.Errorf("the wizard collected\n%+v\nwant\n%+v", got, want)
	}
}

// TestThePresetStepIsWhereTheLastTwoQuestionsAreAnswered states where spec 12
// §1\u2019s "done_on + deploy source" went. Neither is a key `flow init` can set on
// its own — `workflow.done_on` has to agree with `workflow.transitions` and
// `deploys.source` with the rest of its block — so the preset is the answer to
// both, and the step that chooses it has to say so or the wizard would have
// dropped two questions silently.
func TestThePresetStepIsWhereTheLastTwoQuestionsAreAnswered(t *testing.T) {
	keys := append([]string{"enter", "enter"}, typed("acme")...)
	keys = append(keys, "enter", "enter")

	view := pressing(t, aWizard(t, detected), keys...).View().Content
	if !strings.Contains(view, "which workflow should it start from?") {
		t.Fatalf("the fifth question is not the preset:\n%s", view)
	}
	for _, want := range []string{"done when a pull request merges", "until the deployment is running", "when a ticket is done", "deployments"} {
		if !strings.Contains(view, want) {
			t.Errorf("the preset step does not say %q:\n%s", want, view)
		}
	}
}

// TestAQuestionThisConfigurationDoesNotHaveIsNotAsked states the skipping.
// An owner is a GitHub Projects idea and an address is a Jira one, and a
// wizard that asked everybody everything would be a form.
func TestAQuestionThisConfigurationDoesNotHaveIsNotAsked(t *testing.T) {
	keys := append([]string{"enter", "down", "enter"}, typed("https://acme.atlassian.net")...)
	keys = append(keys, "enter")
	keys = append(keys, typed("WID")...)
	keys = append(keys, "enter", "enter", "y")

	w := pressing(t, aWizard(t, detected), keys...)
	if got := w.Answers(); got.Tracker != "jira-cloud" || got.TrackerURL != "https://acme.atlassian.net" {
		t.Fatalf("the Jira session collected %+v", got)
	}
	if got := w.Answers().Owner; got != "" {
		t.Errorf("a Jira project was asked for an owner, and answered %q", got)
	}
	if got := w.Answers().Project; got != "WID" {
		t.Errorf("the project came back as %q, want WID", got)
	}
}

// TestGoingBackKeepsTheAnswer states what escape costs. Somebody who goes
// back to check what they said must find it still said, or the screen is a
// reason not to look.
func TestGoingBackKeepsTheAnswer(t *testing.T) {
	keys := append([]string{"enter", "enter"}, typed("acme")...)
	keys = append(keys, "enter", "esc")

	back := pressing(t, aWizard(t, detected), keys...)
	if view := back.View().Content; !strings.Contains(view, "acme") {
		t.Errorf("going back lost the answer:\n%s", view)
	}
	if view := back.View().Content; !strings.Contains(view, "whose board is it?") {
		t.Errorf("escape did not go back to the previous question:\n%s", view)
	}
}

// TestGoingBackKeepsAChosenAnswerToo states the same thing for a list. An
// answer given by moving a cursor is as easy to lose as one that was typed,
// and a step that came back pointing at its first option would silently
// change what somebody had chosen the moment they pressed enter again.
func TestGoingBackKeepsAChosenAnswerToo(t *testing.T) {
	reviewing := pressing(t, aWizard(t, detected), aWholeSession[:len(aWholeSession)-1]...)
	back := pressing(t, reviewing, "esc")

	if view := back.View().Content; !strings.Contains(view, "which workflow should it start from?") {
		t.Fatalf("escape did not go back to the preset:\n%s", view)
	}
	if got := pressing(t, back, "enter", "y").Answers().Preset; got != "deploy-done" {
		t.Errorf("the preset came back as %q, want the one that was chosen", got)
	}
}

// TestBackingOutOfTheFirstQuestionIsCancelling states the way out. There is
// nothing behind the first question, and a screen that swallowed escape there
// would be one with no exit but an interrupt.
func TestBackingOutOfTheFirstQuestionIsCancelling(t *testing.T) {
	w := pressing(t, aWizard(t, detected), "esc")
	if w.Confirmed() {
		t.Error("backing out of the first question agreed to something")
	}

	var model tea.Model = aWizard(t, detected)
	_, cmd := model.Update(wizardKey("esc"))
	if cmd == nil {
		t.Fatal("backing out of the first question left the screen running")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("backing out ran %T, want a quit", cmd())
	}
}

// TestNobodyAnsweringIsANo states the zero screen. An interrupt, a terminal
// closing and a model that came back as something else all arrive here as a
// screen that was never confirmed, and nothing may be written on that.
func TestNobodyAnsweringIsANo(t *testing.T) {
	for name, keys := range map[string][]string{
		"an interrupt":   {"ctrl+c"},
		"quitting":       {"q"},
		"a plain no":     append(everyQuestion, "n"),
		"nothing at all": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if pressing(t, aWizard(t, detected), keys...).Confirmed() {
				t.Error("the screen was confirmed")
			}
		})
	}
}

// TestTheFirstAnswerIsTheAnswer states that a key already in the terminal's
// buffer when somebody pressed y cannot turn their yes into a no on the way
// out.
func TestTheFirstAnswerIsTheAnswer(t *testing.T) {
	answered := pressing(t, aWizard(t, detected), append(everyQuestion, "y", "n", "esc")...)
	if !answered.Confirmed() {
		t.Error("a key arriving after the answer unanswered it")
	}
	// The screen does not move either. A wizard that went on taking keys
	// after it had quit would be drawing a question somebody had already
	// answered past, in the frames the program is leaving the terminal in.
	if view := answered.View().Content; !strings.Contains(view, "y write it") {
		t.Errorf("a key arriving after the answer moved the screen off the review:\n%s", view)
	}
}

// TestWhatWasTypedIsTrimmed states that the spaces around an answer are not
// part of it. A board reference with a space on the end is a reference flow
// would look up and not find, and the space is invisible on the screen that
// would have to explain it.
func TestWhatWasTypedIsTrimmed(t *testing.T) {
	keys := append([]string{"enter", "enter"}, typed("  acme ")...)
	keys = append(keys, "enter", "enter", "enter", "y")
	if got := pressing(t, aWizard(t, detected), keys...).Answers().Owner; got != "acme" {
		t.Errorf("the owner came back as %q, want it trimmed", got)
	}
}

// TestQIsALetterInAField states the one place the quit key is not a key. A
// board called "quarterly" has to be typeable.
func TestQIsALetterInAField(t *testing.T) {
	keys := append([]string{"enter", "enter"}, typed("quarterly")...)
	w := pressing(t, aWizard(t, detected), keys...)
	if view := w.View().Content; !strings.Contains(view, "quarterly") {
		t.Errorf("q quit a field instead of being typed into it:\n%s", view)
	}
	if w = pressing(t, w, "ctrl+c"); w.Confirmed() {
		t.Error("ctrl-c in a field confirmed the screen")
	}
}

// TestTheCursorGoesRoundTheList states the wrap. Three options and an up key
// on the first of them is the case where a list that stopped would feel
// broken.
func TestTheCursorGoesRoundTheList(t *testing.T) {
	w := pressing(t, aWizard(t, detected), append(append([]string{"up"}, everyQuestion...), "y")...)
	if got := w.Answers().Forge; got != "gitlab" {
		t.Errorf("up from the first option chose %q, want the last", got)
	}
}

// TestTheReviewShowsTheFileAndTheCommandThatWritesItAgain states the last
// step. The file is the answer to every question before it, and the command
// line is how somebody never answers them again.
func TestTheReviewShowsTheFileAndTheCommandThatWritesItAgain(t *testing.T) {
	view := pressing(t, aWizard(t, detected), aWholeSession[:len(aWholeSession)-1]...).View().Content
	for _, want := range []string{
		".flow.yaml",
		"preset: deploy-done",
		"flow init --preset deploy-done --owner acme --project 7",
		"y write it",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the review does not show %q:\n%s", want, view)
		}
	}
}

// TestTheCommandOmitsWhatFlowWouldHaveDoneAnyway states why that command line
// is short. Every flag in it is one more thing a reader has to decide whether
// somebody meant, so the ones that only repeat the defaults are not there.
func TestTheCommandOmitsWhatFlowWouldHaveDoneAnyway(t *testing.T) {
	started := detected
	if got := started.Command(started); got != "flow init" {
		t.Errorf("answering nothing offers %q, want a bare flow init", got)
	}

	changed := started
	changed.Project = "7"
	if got := changed.Command(started); got != "flow init --project 7" {
		t.Errorf("one answer offers %q", got)
	}
}

// TestAPreviewThatFailedIsOnTheScreen states that the review reports rather
// than hides. A configuration flow would refuse to read is a thing to know
// before it is in the working tree, not after.
func TestAPreviewThatFailedIsOnTheScreen(t *testing.T) {
	screen := ui.New(ui.Options{}).Wizard(detected,
		func(ui.Answers) ([]byte, error) { return nil, errors.New("tracker.owner is required") })

	view := pressing(t, screen, everyQuestion...).View().Content
	if !strings.Contains(view, "tracker.owner is required") {
		t.Errorf("the review hid the reason the file could not be built:\n%s", view)
	}
}

// TestTheFileIsCutToTheTerminal states the one thing the review does with a
// size. A preview taller than the terminal would push the command line and
// the keys off the top, which is the half of the screen that does something.
func TestTheFileIsCutToTheTerminal(t *testing.T) {
	long := strings.Repeat("a line of configuration\n", 100)
	screen := ui.New(ui.Options{}).Wizard(detected,
		func(ui.Answers) ([]byte, error) { return []byte(long), nil })

	var model tea.Model = screen
	model, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reviewed := pressing(t, model.(ui.Wizard), everyQuestion...)

	view := reviewed.View().Content
	if lines := strings.Count(view, "\n"); lines > 24 {
		t.Errorf("the review is %d lines on a 24-line terminal:\n%s", lines, view)
	}
	if !strings.Contains(view, "more lines") {
		t.Errorf("the review cut the file and did not say so:\n%s", view)
	}
	if !strings.Contains(view, "y write it") {
		t.Errorf("the keys were pushed off the screen:\n%s", view)
	}
}

// TestAStepThatWasNotStartedFromIsStillAnswered states what an answer flow
// did not work out costs. A screen that began knowing no tracker still has to
// ask for an owner, because the question that decides whether there is one is
// answered by the step before it and not by what flow detected.
func TestAStepThatWasNotStartedFromIsStillAnswered(t *testing.T) {
	screen := ui.New(ui.Options{}).Wizard(ui.Answers{Preset: "lean-kanban"},
		func(ui.Answers) ([]byte, error) { return []byte("version: 1\n"), nil })

	keys := append([]string{"enter", "enter"}, typed("acme")...)
	keys = append(keys, "enter", "enter", "enter", "y")
	answered := pressing(t, screen, keys...)
	if got := answered.Answers().Owner; got != "acme" {
		t.Errorf("the owner came back as %q", got)
	}
	if got := answered.Answers().Tracker; got != "github-projects" {
		t.Errorf("the tracker came back as %q, want the option the cursor was on", got)
	}
}

// TestTheProgressLineCountsOnlyTheQuestionsThereAre states the heading. A
// Jira configuration and a GitHub Projects one have different numbers of
// questions, and a count that included the ones being skipped would stall.
func TestTheProgressLineCountsOnlyTheQuestionsThereAre(t *testing.T) {
	if view := aWizard(t, detected).View().Content; !strings.Contains(view, "1 of 5") {
		t.Errorf("a GitHub Projects session does not open on 1 of 5:\n%s", view)
	}
	jira := pressing(t, aWizard(t, detected), "enter", "down", "enter")
	if view := jira.View().Content; !strings.Contains(view, "3 of 5") {
		t.Errorf("a Jira session is not on 3 of 5:\n%s", view)
	}
	if view := jira.View().Content; !strings.Contains(view, "address") {
		t.Errorf("the Jira session is not being asked for an address:\n%s", view)
	}
}

// TestTheScreenIsASCIIWhenItIsHeldToIt states that the wizard obeys the same
// transliteration as every other rendering: a terminal that cannot draw an
// arrow must not be sent one (spec 12 §3).
func TestTheScreenIsASCIIWhenItIsHeldToIt(t *testing.T) {
	screen := ui.New(ui.Options{ASCII: true}).Wizard(detected,
		func(ui.Answers) ([]byte, error) { return []byte("version: 1\n"), nil })

	view := screen.View().Content
	if strings.ContainsAny(view, "→·") {
		t.Errorf("an ASCII screen drew characters it was told not to:\n%s", view)
	}
	if !strings.Contains(view, "->") {
		t.Errorf("the option under the hand is not marked:\n%s", view)
	}
}

// TestAPlainScreenPaintsNothing states that the field obeys the palette too.
// A text input keeping the library's own colours would be the one thing on
// the screen --no-color did not turn off, and the one thing that looked like
// a different program.
//
// The cursor survives, because a cursor drawn into the line is the only way
// that line can show one, and reverse video is not colour.
func TestAPlainScreenPaintsNothing(t *testing.T) {
	keys := append([]string{"enter", "enter"}, typed("acme")...)
	view := pressing(t, aWizard(t, detected), keys...).View().Content
	if !strings.Contains(view, "acme") {
		t.Fatalf("the field is not on the screen:\n%s", view)
	}
	cursor := strings.NewReplacer("\x1b[7m", "", "\x1b[m", "")
	if painted := cursor.Replace(view); strings.Contains(painted, "\x1b[") {
		t.Errorf("a plain screen painted something:\n%q", painted)
	}
}
