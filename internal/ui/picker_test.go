package ui_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/ui"
)

// boards is the question a placeholder picker usually asks: which of the
// boards this credential can see is the one this repository's work belongs
// on.
func boards() []core.Candidate {
	return []core.Candidate{
		{What: "tracker.project", ID: "PVT_1", Name: "Delivery", Detail: "17 items · updated today"},
		{What: "tracker.project", ID: "PVT_2", Name: "Platform roadmap", Detail: "4 items"},
		{What: "tracker.project", ID: "PVT_3", Name: "Design debt", Detail: "no items"},
	}
}

// picker builds the screen the way the root does.
func picker(t *testing.T, opts ui.Options, candidates []core.Candidate) ui.Picker {
	t.Helper()
	return ui.New(opts).Picker("which of these should tracker.project be?", candidates)
}

// choose drives the screen the way a terminal does and reports what it
// settled on, and whether it asked to quit.
//
// It delivers what the commands produce, which a harness that only looked for
// a quit would not: filtering a list is a command, and a test that dropped
// its answer would be filtering nothing and choosing from the whole list
// without ever noticing.
func choose(t *testing.T, screen ui.Picker, msgs ...tea.Msg) (ui.Picker, bool) {
	t.Helper()
	var quit bool
	model := tea.Model(screen)

	var send func(msg tea.Msg)
	send = func(msg tea.Msg) {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		for _, produced := range deliver(t, cmd) {
			if _, ok := produced.(tea.QuitMsg); ok {
				quit = true
				continue
			}
			send(produced)
		}
	}
	for _, msg := range msgs {
		send(msg)
	}

	final, ok := model.(ui.Picker)
	if !ok {
		t.Fatalf("the screen updated into a %T", model)
	}
	return final, quit
}

// deliver runs a command and returns what it produced, flattening a batch.
//
// A command that has not answered inside blinkWait is a timer — the cursor
// blink a focused filter starts, which waits out its interval before saying
// anything and then asks to be run again. Nothing the screen decides depends
// on one, and following the cycle would never end.
func deliver(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	answered := make(chan tea.Msg, 1)
	go func() { answered <- cmd() }()
	select {
	case msg := <-answered:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var msgs []tea.Msg
			for _, each := range batch {
				msgs = append(msgs, deliver(t, each)...)
			}
			return msgs
		}
		return []tea.Msg{msg}
	case <-time.After(blinkWait):
		return nil
	}
}

// blinkWait is how long a command gets to answer before it is taken for a
// timer. It is well under the cursor's blink interval and well over the time
// it takes to filter a list, which is the only command on this screen whose
// answer matters.
const blinkWait = 100 * time.Millisecond

// typing is the key presses a person makes to type a word.
func typing(text string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(text))
	for _, r := range text {
		msgs = append(msgs, press(string(r)))
	}
	return msgs
}

// filtered is the keys that start a filter, type it and apply it.
func filtered(text string) []tea.Msg {
	return append(append([]tea.Msg{press("/")}, typing(text)...), named(tea.KeyEnter, 0))
}

// TestOnlyReturnPicksACandidate states what the screen is for. A placeholder
// is resolved by somebody saying which of the tracker's answers is the right
// one, and the key that says it is return; everything else either moves the
// cursor, leaves the question open, or refuses it.
func TestOnlyReturnPicksACandidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		msgs     []tea.Msg
		answered bool
		picked   string
		quit     bool
	}{
		{name: "return picks what the cursor is on", msgs: []tea.Msg{named(tea.KeyEnter, 0)}, answered: true, picked: "PVT_1", quit: true},
		{name: "down then return picks the next one", msgs: []tea.Msg{press("j"), named(tea.KeyEnter, 0)}, answered: true, picked: "PVT_2", quit: true},
		{name: "escape refuses", msgs: []tea.Msg{named(tea.KeyEscape, 0)}, answered: true, quit: true},
		{name: "q refuses", msgs: []tea.Msg{press("q")}, answered: true, quit: true},
		{name: "ctrl-c refuses", msgs: []tea.Msg{named('c', tea.ModCtrl)}, answered: true, quit: true},
		{name: "a resize does not answer", msgs: []tea.Msg{tea.WindowSizeMsg{Width: 40, Height: 10}}},
		{name: "a letter that is not a key does not answer", msgs: []tea.Msg{press("z")}},
		{name: "nothing at all does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			final, quit := choose(t, picker(t, ui.Options{}, boards()), tc.msgs...)
			if final.Answered() != tc.answered {
				t.Errorf("answered = %v, want %v", final.Answered(), tc.answered)
			}
			if final.Picked() != (tc.picked != "") {
				t.Errorf("picked = %v, want %v", final.Picked(), tc.picked != "")
			}
			if got := final.Chosen().ID; got != tc.picked {
				t.Errorf("chose %q, want %q", got, tc.picked)
			}
			if quit != tc.quit {
				t.Errorf("asked to quit = %v, want %v", quit, tc.quit)
			}
		})
	}
}

// TestTheScreenSettlesOnce states that a key arriving after the question has
// been answered — a terminal's buffer draining while the program tears the
// screen down — cannot answer it again. The placeholder is written to the
// lock, so a second answer would be a board nobody picked.
func TestTheScreenSettlesOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		msgs []tea.Msg
		want string
	}{
		{name: "a key after the pick does not move it", msgs: []tea.Msg{named(tea.KeyEnter, 0), press("j"), named(tea.KeyEnter, 0)}, want: "PVT_1"},
		{name: "a key after the refusal does not pick", msgs: []tea.Msg{named(tea.KeyEscape, 0), named(tea.KeyEnter, 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			final, _ := choose(t, picker(t, ui.Options{}, boards()), tc.msgs...)
			if got := final.Chosen().ID; got != tc.want {
				t.Errorf("chose %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAKeyBelongsToTheFilterFirst states the rule that makes a fuzzy list
// usable at all: while a filter is being typed, return applies it and escape
// abandons it, and neither answers the question. A picker that read the
// return at the end of a filter as a choice would pick whatever happened to
// be under the cursor, and one that read the escape as a refusal would make
// a mistyped filter unrecoverable.
func TestAKeyBelongsToTheFilterFirst(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		msgs     []tea.Msg
		answered bool
		picked   string
	}{
		{name: "return applies the filter", msgs: filtered("plat")},
		{name: "escape abandons the filter", msgs: append([]tea.Msg{press("/")}, append(typing("plat"), named(tea.KeyEscape, 0))...)},
		{name: "q is a letter in a filter", msgs: append([]tea.Msg{press("/")}, typing("q")...)},
		{name: "and then return picks what the filter found", msgs: append(filtered("plat"), named(tea.KeyEnter, 0)), answered: true, picked: "PVT_2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			final, _ := choose(t, picker(t, ui.Options{}, boards()), tc.msgs...)
			if final.Answered() != tc.answered {
				t.Errorf("answered = %v, want %v", final.Answered(), tc.answered)
			}
			if got := final.Chosen().ID; got != tc.picked {
				t.Errorf("chose %q, want %q", got, tc.picked)
			}
		})
	}
}

// TestEscapeTakesTheFilterOffBeforeItRefuses states what escape means when
// there is a filter applied: it takes the filter off. Only once there is
// nothing left to back out of is it a refusal, so a person who filtered to
// nothing can get the whole list back without losing the question.
func TestEscapeTakesTheFilterOffBeforeItRefuses(t *testing.T) {
	t.Parallel()
	applied := filtered("plat")

	cleared, _ := choose(t, picker(t, ui.Options{}, boards()), append(applied, named(tea.KeyEscape, 0))...)
	if cleared.Answered() {
		t.Fatal("escape with a filter applied refused the question")
	}
	if view := cleared.View().Content; !strings.Contains(view, "Design debt") {
		t.Errorf("the filter is still on after escape:\n%s", view)
	}

	refused, _ := choose(t, picker(t, ui.Options{}, boards()), append(applied, named(tea.KeyEscape, 0), named(tea.KeyEscape, 0))...)
	if !refused.Answered() {
		t.Error("a second escape, with no filter left to clear, did not refuse")
	}
}

// TestCtrlCRefusesEvenMidFilter states the one key that is never the
// filter's. A person stopping a program expects it to stop, and a filter
// being typed is the state in which that is easiest to get wrong.
func TestCtrlCRefusesEvenMidFilter(t *testing.T) {
	t.Parallel()
	final, quit := choose(t, picker(t, ui.Options{}, boards()),
		append([]tea.Msg{press("/")}, append(typing("plat"), named('c', tea.ModCtrl))...)...)
	if !final.Answered() || final.Picked() {
		t.Errorf("answered = %v, picked = %v; want a refusal", final.Answered(), final.Picked())
	}
	if !quit {
		t.Error("ctrl-c did not stop the screen")
	}
}

// TestAFilterThatMatchesNothingLeavesTheWholeListToChooseFrom states what a
// person who typed too much gets back: every candidate, because a filter
// matching none of them is not a narrowing of anything.
func TestAFilterThatMatchesNothingLeavesTheWholeListToChooseFrom(t *testing.T) {
	t.Parallel()
	final, _ := choose(t, picker(t, ui.Options{}, boards()), filtered("zzzz")...)
	if final.Answered() {
		t.Error("a filter that found nothing answered the question")
	}
	for _, want := range []string{"Delivery", "Platform roadmap", "Design debt"} {
		if !strings.Contains(final.View().Content, want) {
			t.Errorf("%q is not on the screen any more:\n%s", want, final.View().Content)
		}
	}
}

// TestReturnWithNothingToChooseFromIsNotAnAnswer states the reading of a key
// pressed over an empty list. There is no candidate under the cursor, and a
// screen that answered anyway would have to invent one.
func TestReturnWithNothingToChooseFromIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	final, quit := choose(t, picker(t, ui.Options{}, nil), named(tea.KeyEnter, 0))
	if final.Answered() || final.Picked() {
		t.Errorf("answered = %v, picked = %v; want neither", final.Answered(), final.Picked())
	}
	if quit {
		t.Error("the screen closed on a question it had not answered")
	}
}

// TestTheZeroScreenHasPickedNothing states the reading of an answer nobody
// gave: a program that came back as something else, a terminal that closed.
// The engine takes it as the placeholder still being a question, which is
// what it was before anybody was asked.
func TestTheZeroScreenHasPickedNothing(t *testing.T) {
	t.Parallel()
	var nobody ui.Picker
	if nobody.Answered() || nobody.Picked() || nobody.Chosen().ID != "" {
		t.Error("the zero screen claimed an answer")
	}
}

// TestTheScreenShowsTheQuestionAndEveryCandidate states that the thing being
// decided is on the screen it is decided on, with the detail the tracker
// offered to tell two boards of the same name apart.
func TestTheScreenShowsTheQuestionAndEveryCandidate(t *testing.T) {
	t.Parallel()
	view := picker(t, ui.Options{}, boards()).View().Content
	for _, want := range []string{
		"which of these should tracker.project be?",
		"Delivery", "Platform roadmap", "Design debt", "17 items",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen does not say %q:\n%s", want, view)
		}
	}
}

// TestACandidateWithNoDetailTakesOneLine states that the rows are only as
// tall as they need to be. A detail nobody supplied is a blank line under
// every candidate, which halves how many a terminal can show.
func TestACandidateWithNoDetailTakesOneLine(t *testing.T) {
	t.Parallel()
	plain := []core.Candidate{{ID: "1", Name: "Delivery"}, {ID: "2", Name: "Platform"}}
	screen, _ := choose(t, picker(t, ui.Options{}, plain), tea.WindowSizeMsg{Width: 40, Height: 12})
	view := screen.View().Content

	rows := strings.Index(view, "Delivery")
	next := strings.Index(view, "Platform")
	if rows < 0 || next < 0 {
		t.Fatalf("both candidates should be on the screen:\n%s", view)
	}
	if lines := strings.Count(view[rows:next], "\n"); lines != 1 {
		t.Errorf("%d lines between the candidates, want 1:\n%s", lines, view)
	}
}

// TestTheCursorMarksWhatReturnWouldPick states that the screen says which
// candidate the key would take. Two rows that differ only in colour are the
// same row to a reader with no colour, and --no-color is the mode every pipe
// and every CI log is in.
func TestTheCursorMarksWhatReturnWouldPick(t *testing.T) {
	t.Parallel()
	screen, _ := choose(t, picker(t, ui.Options{}, boards()), press("j"))
	view := screen.View().Content

	marked := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "> ") {
			marked++
			if !strings.Contains(line, "Platform roadmap") {
				t.Errorf("the cursor is on %q, want Platform roadmap", line)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d rows are marked, want exactly 1:\n%s", marked, view)
	}
}

// TestANameTooWideForTheTerminalIsCutShort states that a board with a long
// name folds nothing. A row that wraps puts the next candidate's name on the
// cursor's line, and the list counts rows rather than lines.
func TestANameTooWideForTheTerminalIsCutShort(t *testing.T) {
	t.Parallel()
	long := []core.Candidate{{ID: "1", Name: strings.Repeat("board ", 20)}}
	for _, tc := range []struct {
		name     string
		opts     ui.Options
		ellipsis string
	}{
		{name: "unicode", ellipsis: "…"},
		{name: "ascii", opts: ui.Options{ASCII: true}, ellipsis: "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen, _ := choose(t, picker(t, tc.opts, long), tea.WindowSizeMsg{Width: 24, Height: 10})
			for _, line := range strings.Split(screen.View().Content, "\n") {
				if drawn := lipgloss.Width(line); drawn > 24 {
					t.Errorf("a line is %d columns wide, want at most 24: %q", drawn, line)
				}
			}
			if !strings.Contains(screen.View().Content, tc.ellipsis) {
				t.Errorf("nothing says the name was cut short:\n%s", screen.View().Content)
			}
		})
	}
}

// TestTheScreenFitsTheTerminalItWasGiven states that nothing the picker
// draws is wider than the terminal it is drawn on. A line that overflows is
// folded wherever the terminal runs out, which puts a candidate's name on
// the row below and makes the cursor point at the wrong one.
func TestTheScreenFitsTheTerminalItWasGiven(t *testing.T) {
	t.Parallel()
	for _, opts := range []ui.Options{{}, {ASCII: true}} {
		for _, width := range []int{20, 24, 40, 60, 100} {
			sized := []tea.Msg{tea.WindowSizeMsg{Width: width, Height: 12}}
			for _, state := range [][]tea.Msg{
				sized,
				append(sized, press("/")),
				append(sized, filtered("t")...),
			} {
				screen, _ := choose(t, picker(t, opts, manyBoards()), state...)
				for _, line := range strings.Split(screen.View().Content, "\n") {
					if drawn := lipgloss.Width(line); drawn > width {
						t.Errorf("ascii=%v width=%d: a line is %d columns wide: %q", opts.ASCII, width, drawn, line)
					}
				}
			}
		}
	}
}

// TestAnAsciiScreenIsAllAscii states what --ascii means here. The screen is
// drawn rather than decorated, so nothing takes it through the engine's
// glyph table on the way out, and a row of characters a terminal cannot draw
// is exactly the thing that flag exists to prevent.
func TestAnAsciiScreenIsAllAscii(t *testing.T) {
	t.Parallel()
	for _, state := range [][]tea.Msg{
		{tea.WindowSizeMsg{Width: 60, Height: 10}},
		{tea.WindowSizeMsg{Width: 60, Height: 10}, press("/")},
	} {
		screen, _ := choose(t, picker(t, ui.Options{ASCII: true}, manyBoards()), state...)
		for _, r := range screen.View().Content {
			if r > 127 {
				t.Fatalf("the screen draws %q, which is not ascii:\n%s", r, screen.View().Content)
			}
		}
	}
}

// TestPaginationSaysWhichPageWithoutColour states that the two marks differ
// as characters. The pages of a list are told apart by a dot of a different
// colour, and flow is frequently run with no colour at all.
func TestPaginationSaysWhichPageWithoutColour(t *testing.T) {
	t.Parallel()
	screen, _ := choose(t, picker(t, ui.Options{}, manyBoards()), tea.WindowSizeMsg{Width: 60, Height: 10})
	view := screen.View().Content
	if !strings.Contains(view, "●") || !strings.Contains(view, "○") {
		t.Errorf("the pages are not told apart by anything but colour:\n%s", view)
	}
}

// manyBoards is more candidates than a short terminal can show at once.
func manyBoards() []core.Candidate {
	var many []core.Candidate
	for _, name := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight"} {
		many = append(many, core.Candidate{ID: name, Name: name, Detail: "a board called " + name})
	}
	return many
}

// TestADetailIsGivenRoomOnTheTerminalItIsDrawnOn states that a row two lines
// tall is counted as two lines. A list decides how many candidates a page
// holds by dividing the space it has by the height of a row, so a row that
// lies about its height fills the page with twice the candidates it can show
// and the overflow lands wherever the terminal was already using.
func TestADetailIsGivenRoomOnTheTerminalItIsDrawnOn(t *testing.T) {
	t.Parallel()
	for _, height := range []int{10, 12, 20} {
		screen, _ := choose(t, picker(t, ui.Options{}, manyBoards()), tea.WindowSizeMsg{Width: 60, Height: height})
		view := screen.View().Content
		if drawn := lipgloss.Height(view); drawn > height {
			t.Errorf("the screen is %d lines tall on a terminal of %d:\n%s", drawn, height, view)
		}
		if !strings.Contains(view, "a board called one") {
			t.Errorf("the detail that tells two boards apart is not on the screen:\n%s", view)
		}
	}
}

// TestTheMarkThatSaysANameWasCutShortIsNeverAMatch states that the ellipsis
// belongs to flow rather than to the filter. Highlighting it tells a person
// that what they typed was found in a part of the name they cannot see, and
// the one thing that character means is that they cannot see it.
func TestTheMarkThatSaysANameWasCutShortIsNeverAMatch(t *testing.T) {
	t.Parallel()
	// Long enough to be cut at this width, with the only z sitting exactly
	// where the ellipsis lands.
	name := strings.Repeat("a", 21) + "z" + strings.Repeat("b", 20)
	screen, _ := choose(t, picker(t, ui.Options{}, []core.Candidate{{ID: "1", Name: name}}),
		tea.WindowSizeMsg{Width: 24, Height: 10})
	screen, _ = choose(t, screen, append([]tea.Msg{press("/")}, typing("z")...)...)

	view := screen.View().Content
	if !strings.Contains(view, "a…") {
		t.Errorf("the name was not cut short where it was expected to be:\n%q", view)
	}
}

// TestAKeyThatWouldDoNothingIsNotOffered states that the key line is the keys
// that are live. While a filter is being typed the movement keys, the filter
// key and the key that picks a candidate are all switched off — the list
// switches them off itself — and a line that named them anyway would be flow
// inviting a person to press keys it has arranged to ignore.
func TestAKeyThatWouldDoNothingIsNotOffered(t *testing.T) {
	t.Parallel()
	screen, _ := choose(t, picker(t, ui.Options{}, manyBoards()),
		tea.WindowSizeMsg{Width: 120, Height: 12}, press("/"))
	keys := lastLine(screen.View().Content)

	if !strings.Contains(keys, "cancel") {
		t.Errorf("the key line does not say how to back out of a filter: %q", keys)
	}
	for _, dead := range []string{"up", "down", "choose", "filter"} {
		if strings.Contains(keys, dead) {
			t.Errorf("the key line offers %q while a filter is being typed: %q", dead, keys)
		}
	}
}

// TestTheFilterIsDrawnInFlowsColoursOrNoneAtAll states that the input a
// person types a filter into is flow's, not the library's. It is built by the
// list rather than by this package, and a screen asked for no colour that
// paints one box in somebody else's palette is a screen that ignored the
// flag.
func TestTheFilterIsDrawnInFlowsColoursOrNoneAtAll(t *testing.T) {
	t.Parallel()
	screen, _ := choose(t, picker(t, ui.Options{}, manyBoards()),
		tea.WindowSizeMsg{Width: 60, Height: 12}, press("/"))

	if coloured := colourCodes(screen.View().Content); len(coloured) > 0 {
		t.Errorf("a screen asked for no colour was painted in %v:\n%q", coloured, screen.View().Content)
	}
}

// sgr matches one select-graphic-rendition escape, which is the only kind of
// escape anything here draws.
var sgr = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// colourCodes is the SGR parameters in a rendering that set a colour. Underline
// and reverse video are not colours: they are the two ways a terminal with no
// colour at all still tells one run of characters from another, which is why
// flow's own no-colour styles use them.
func colourCodes(view string) []int {
	var found []int
	for _, match := range sgr.FindAllStringSubmatch(view, -1) {
		for _, field := range strings.Split(match[1], ";") {
			code, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			switch {
			case code >= 30 && code <= 49, code >= 90 && code <= 107:
				found = append(found, code)
			}
		}
	}
	return found
}

// lastLine is the key line: the bottom of whatever was drawn.
func lastLine(view string) string {
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	return lines[len(lines)-1]
}
