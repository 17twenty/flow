package ui_test

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// press is the key event a terminal sends for a printable character.
func press(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(text)[0], Text: text}
}

// named is the key event for a key with no text of its own, such as escape.
func named(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

// answer drives the screen the way a terminal does and reports what it
// settled on, and whether it asked to quit.
func answer(t *testing.T, screen ui.Confirmation, msgs ...tea.Msg) (ui.Confirmation, bool) {
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
	final, ok := model.(ui.Confirmation)
	if !ok {
		t.Fatalf("the screen updated into a %T", model)
	}
	return final, quit
}

func confirmation(t *testing.T, opts ui.Options, plan engine.Plan) ui.Confirmation {
	t.Helper()
	return ui.New(opts).Confirmation(plan, "apply 5 changes?")
}

// TestYesIsTheOnlyWayToApprove states the whole safety property of the screen:
// one key approves, several refuse, and everything else leaves the question
// open. A screen that could be answered by a resize or a stray escape
// sequence would be an apply nobody agreed to.
func TestYesIsTheOnlyWayToApprove(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		msgs     []tea.Msg
		answered bool
		approved bool
		quit     bool
	}{
		{name: "y approves", msgs: []tea.Msg{press("y")}, answered: true, approved: true, quit: true},
		{name: "a held shift still approves", msgs: []tea.Msg{press("Y")}, answered: true, approved: true, quit: true},
		{name: "n refuses", msgs: []tea.Msg{press("n")}, answered: true, quit: true},
		{name: "q refuses", msgs: []tea.Msg{press("q")}, answered: true, quit: true},
		{name: "escape refuses", msgs: []tea.Msg{named(tea.KeyEscape, 0)}, answered: true, quit: true},
		{name: "ctrl-c refuses", msgs: []tea.Msg{named('c', tea.ModCtrl)}, answered: true, quit: true},
		{name: "return does not answer", msgs: []tea.Msg{named(tea.KeyEnter, 0)}},
		{name: "space does not answer", msgs: []tea.Msg{press(" ")}},
		{name: "another letter does not answer", msgs: []tea.Msg{press("a")}},
		{name: "a resize does not answer", msgs: []tea.Msg{tea.WindowSizeMsg{Width: 10, Height: 4}}},
		{name: "nothing at all does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			final, quit := answer(t, confirmation(t, ui.Options{}, samplePlan()), tc.msgs...)
			if final.Answered() != tc.answered {
				t.Errorf("answered = %v, want %v", final.Answered(), tc.answered)
			}
			if final.Approved() != tc.approved {
				t.Errorf("approved = %v, want %v", final.Approved(), tc.approved)
			}
			if quit != tc.quit {
				t.Errorf("asked to quit = %v, want %v", quit, tc.quit)
			}
		})
	}
}

// TestAnAnsweredScreenStaysAnswered states that the screen settles once. A
// key arriving after the program has been told to quit — a terminal's buffer
// draining, a person leaning on the keyboard — must not turn a refusal into
// an apply.
func TestAnAnsweredScreenStaysAnswered(t *testing.T) {
	t.Parallel()
	final, _ := answer(t, confirmation(t, ui.Options{}, samplePlan()), press("n"), press("y"))
	if final.Approved() {
		t.Fatal("a key after the answer approved the apply")
	}
}

// TestTheScreenGroupsThePlanByOutcome states the shape spec 12 §4 asks for:
// one heading per outcome the plan has, carrying how many items are under it,
// in the engine's own order.
func TestTheScreenGroupsThePlanByOutcome(t *testing.T) {
	t.Parallel()
	view := confirmation(t, ui.Options{}, samplePlan()).View().Content

	var seen []string
	for _, outcome := range engine.Outcomes {
		want := outcome.Symbol() + " " + outcome.Label() + " (1)"
		index := strings.Index(view, want)
		if index < 0 {
			t.Fatalf("no heading for %s:\n%s", outcome, view)
		}
		seen = append(seen, want)
		if len(seen) > 1 && index < strings.Index(view, seen[len(seen)-2]) {
			t.Errorf("%q came before %q, which is not the order the engine counts in", want, seen[len(seen)-2])
		}
	}
	for _, item := range samplePlan().Items {
		if !strings.Contains(view, item.ID) {
			t.Errorf("the table left out %s:\n%s", item.ID, view)
		}
	}
}

// TestAGroupSaysHowManyAreInIt states that the count on a heading is this
// group's and not a decoration. How much of an apply is flow's own work and
// how much is being handed to somebody is the first thing a reader wants, and
// a plan with one item of every outcome cannot tell a count from a constant.
func TestAGroupSaysHowManyAreInIt(t *testing.T) {
	t.Parallel()
	plan := samplePlan()
	plan.Items = append(plan.Items, engine.PlanItem{
		ID: "forge.pr_template_body", Outcome: engine.OutcomeNative, Why: "the template says something else",
	}, engine.PlanItem{
		ID: "forge.labels", Outcome: engine.OutcomeNative, Why: "two labels are missing",
	})
	view := confirmation(t, ui.Options{}, plan).View().Content

	if !strings.Contains(view, engine.OutcomeNative.Label()+" (3)") {
		t.Errorf("three native items are not counted as three:\n%s", view)
	}
	if !strings.Contains(view, engine.OutcomeAttest.Label()+" (1)") {
		t.Errorf("the one attestation is not counted as one:\n%s", view)
	}
}

// TestAnOutcomeWithNoItemsHasNoHeading states that the table describes this
// plan and not every plan flow can make: a heading reading "0" would have a
// reader looking for a group that is not there.
func TestAnOutcomeWithNoItemsHasNoHeading(t *testing.T) {
	t.Parallel()
	plan := samplePlan()
	plan.Items = []engine.PlanItem{{
		ID: "forge.branch_protection", Outcome: engine.OutcomeNative, Why: "main is unprotected",
	}}
	view := confirmation(t, ui.Options{}, plan).View().Content

	if !strings.Contains(view, engine.OutcomeNative.Label()+" (1)") {
		t.Errorf("the one outcome in the plan has no heading:\n%s", view)
	}
	for _, outcome := range engine.Outcomes {
		if outcome == engine.OutcomeNative {
			continue
		}
		if strings.Contains(view, outcome.Label()+" (") {
			t.Errorf("%s has a heading and no items:\n%s", outcome, view)
		}
	}
}

// TestAPlanWithNothingInItSaysSo states that an empty plan is words and not
// an empty table. Approving nothing is a reasonable thing to be asked, and a
// frame of borders with no rows says less than the engine's own summary.
func TestAPlanWithNothingInItSaysSo(t *testing.T) {
	t.Parallel()
	view := confirmation(t, ui.Options{}, engine.Plan{Schema: engine.PlanVersion}).View().Content

	if !strings.Contains(view, "nothing to plan") {
		t.Errorf("an empty plan did not say so:\n%s", view)
	}
	if strings.Contains(view, "─") || strings.Contains(view, "│") {
		t.Errorf("an empty plan was drawn as a table:\n%s", view)
	}
}

// TestTheQuestionAndItsKeysAreAlwaysOnScreen states that the screen is
// answerable: the question it is asking and the keys that answer it are
// present whatever the plan looks like.
func TestTheQuestionAndItsKeysAreAlwaysOnScreen(t *testing.T) {
	t.Parallel()
	for _, plan := range map[string]engine.Plan{"a full plan": samplePlan(), "an empty plan": {}} {
		view := confirmation(t, ui.Options{}, plan).View().Content
		for _, want := range []string{"apply 5 changes?", "y apply", "n cancel"} {
			if !strings.Contains(view, want) {
				t.Errorf("the screen does not say %q:\n%s", want, view)
			}
		}
	}
}

// TestTheScreenIsDrawnInASCIIWhenItIsAskedTo states that --ascii reaches the
// screen as well as the documents: a terminal that cannot draw a box-drawing
// character cannot draw this table's borders either, and a confirmation
// nobody can read is one nobody can answer.
func TestTheScreenIsDrawnInASCIIWhenItIsAskedTo(t *testing.T) {
	t.Parallel()
	view := confirmation(t, ui.Options{ASCII: true}, samplePlan()).View().Content

	for _, unicode := range []string{"─", "│", "┌", "✓", "⚠", "✗", "✎", "↻", "→", "·", "—"} {
		if strings.Contains(view, unicode) {
			t.Errorf("an ASCII screen still contains %q:\n%s", unicode, view)
		}
	}
	if !strings.Contains(view, "+ native (1)") {
		t.Errorf("the native heading lost its ASCII glyph:\n%s", view)
	}
	if !strings.Contains(view, "-> flow attest tracker.saved_view") {
		t.Errorf("the detail column lost its ASCII arrow:\n%s", view)
	}
	if !strings.Contains(view, "y apply - n cancel") {
		t.Errorf("the key line kept a Unicode separator:\n%s", view)
	}
}

// TestTheScreenRendersTheSamePlanTheSameWay is the golden: the shape of the
// table, the padding, the borders and the order, held to a file so that a
// change to any of them is something somebody chose.
func TestTheScreenRendersTheSamePlanTheSameWay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts ui.Options
	}{
		{name: "confirm.txt", opts: ui.Options{}},
		{name: "confirm.ascii.txt", opts: ui.Options{ASCII: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			golden(t, tc.name, confirmation(t, tc.opts, samplePlan()).View().Content)
		})
	}
}

// TestColourOnlyAddsColourToTheScreen states for the confirmation what the
// text renderer already promises for every document (spec 12): the styled
// screen with the escape sequences taken off is the plain one, character for
// character. A palette that moved a column would mean two layouts, and the
// one a test reads would not be the one a person sees.
func TestColourOnlyAddsColourToTheScreen(t *testing.T) {
	t.Parallel()
	plain := confirmation(t, ui.Options{}, samplePlan()).View().Content
	coloured := confirmation(t, ui.Options{Colour: true, Dark: true}, samplePlan()).View().Content

	if !strings.Contains(coloured, "\x1b[") {
		t.Fatal("colour was asked for and none was painted, so the test proves nothing")
	}

	// Printing to a buffer is printing to something that is not a terminal,
	// which is exactly when the colour comes off again.
	var out bytes.Buffer
	if err := ui.Fprint(&out, coloured); err != nil {
		t.Fatalf("print: %v", err)
	}
	if out.String() != plain {
		t.Errorf("the coloured screen is laid out differently:\n--- stripped\n%s\n--- plain\n%s", out.String(), plain)
	}
}

// TestTheTableFitsTheTerminalItIsToldAbout states that the screen takes the
// width the program reports and draws inside it. A table wider than the
// terminal is folded by the terminal at whatever column it runs out of, which
// turns a grouped table into a wall and an approval into a guess.
func TestTheTableFitsTheTerminalItIsToldAbout(t *testing.T) {
	t.Parallel()
	const width = 60
	narrowed, _ := answer(t, confirmation(t, ui.Options{}, samplePlan()), tea.WindowSizeMsg{Width: width, Height: 24})

	for _, line := range strings.Split(narrowed.View().Content, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("a line is %d columns wide in a %d-column terminal: %q", got, width, line)
		}
	}
	if lipgloss.Width(strings.Split(confirmation(t, ui.Options{}, samplePlan()).View().Content, "\n")[0]) <= width {
		t.Fatal("the unconstrained table already fits, so narrowing it proves nothing")
	}
}

// TestASizeIsNotAnAnswer states that the one message that does change the
// model still does not settle the question. A terminal emits a size before
// the first frame and again whenever the window moves, and a resize that
// counted as a key press would approve an apply nobody looked at.
func TestASizeIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	final, quit := answer(t, confirmation(t, ui.Options{}, samplePlan()),
		tea.WindowSizeMsg{Width: 60, Height: 24}, tea.WindowSizeMsg{Width: 200, Height: 50})
	if final.Answered() || final.Approved() || quit {
		t.Fatalf("a resize answered the question: answered=%v approved=%v quit=%v",
			final.Answered(), final.Approved(), quit)
	}
}

// colours is the set of colours a line is painted with. The resets are left
// out: every styled cell ends with one whatever it was painted in, so a set
// that counted them would never be empty and "painted in one colour" would be
// a thing no assertion could see.
func colours(t *testing.T, view, contains string) map[string]bool {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, contains) {
			continue
		}
		found := map[string]bool{}
		for _, part := range strings.Split(line, "\x1b[")[1:] {
			end := strings.IndexByte(part, 'm')
			if end < 0 {
				continue
			}
			if sequence := "\x1b[" + part[:end+1]; sequence != "\x1b[0m" && sequence != "\x1b[m" {
				found[sequence] = true
			}
		}
		if len(found) == 0 {
			t.Fatalf("the line containing %q is painted with nothing: %q", contains, line)
		}
		return found
	}
	t.Fatalf("no line contains %q:\n%s", contains, view)
	return nil
}

// TestEachGroupIsPaintedByWhatItMeans states that the heading rows carry the
// colour their outcome already has everywhere else flow prints it, and that
// an item is not painted like its heading. A table in one colour would be
// grouped only for a reader who reads every row, which is the reader a
// confirmation screen exists to spare.
func TestEachGroupIsPaintedByWhatItMeans(t *testing.T) {
	t.Parallel()
	view := confirmation(t, ui.Options{Colour: true, Dark: true}, samplePlan()).View().Content

	native := colours(t, view, "native (1)")
	followUp := colours(t, view, "follow-up (1)")
	unsupported := colours(t, view, "unsupported (1)")
	item := colours(t, view, "forge.branch_protection")

	for _, pair := range []struct {
		name  string
		a, b  map[string]bool
		these string
	}{
		{name: "what flow does and what it hands over", a: native, b: followUp},
		{name: "what it hands over and what nobody can do", a: followUp, b: unsupported},
		{name: "a heading and an item under it", a: native, b: item},
	} {
		if maps.Equal(pair.a, pair.b) {
			t.Errorf("%s are painted the same:\n%s", pair.name, view)
		}
	}

	// An item row is an id and a reason, and those are not the same kind of
	// thing: a row in one colour would read as one long sentence.
	if len(item) < 2 {
		t.Errorf("an item row is painted in one colour (%d found):\n%s", len(item), view)
	}
}
