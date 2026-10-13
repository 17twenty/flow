package ui

import (
	"fmt"
	"io"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/core"
)

// The placeholder picker (spec 12 §2). A placeholder is the one thing
// `.flow.yaml` is allowed to not know, and the tracker is the only thing that
// can say what the answers are; when it offers more than one, somebody has to
// look at them and say which (spec 07 §3).
//
// The decision is not here. The engine walks the ladder, decides that this
// rung has been reached, and calls a [engine.Chooser]; this is what a terminal
// makes of that call. A person who escapes has not chosen, which the engine
// reads as "not here, not now" and falls through to telling them what to run.

// Picker is the screen a person answers a placeholder on: the candidates the
// tracker offered, a fuzzy filter over their names, and one of them picked.
//
// It is a [tea.Model], so a test drives it the way a terminal does — through
// Update.
type Picker struct {
	// renderer paints the question and the key line, in the same options
	// every other document this run produces was drawn in.
	renderer Renderer
	// question is what is being decided, in the caller's words.
	question string
	// list is the candidates, the cursor, the filter and the pages.
	// Browsing a filtered list is a solved problem and bubbles is on the
	// dependency list precisely so that flow does not solve it again
	// (spec 01 §4); what this package keeps is which candidate a key press
	// means and what the screen is painted in.
	list list.Model
	// choose is the one binding the list has no opinion about: a list is
	// something you browse, and picking from it is this screen's own idea.
	choose key.Binding
	// width and height are the terminal's, as the program reported them.
	width, height int
	// answered is whether a key press settled it. A screen torn down before
	// an answer — an interrupt, a terminal closing — has not picked
	// anything, which the engine reads as a question still unanswered.
	answered bool
	// picked is whether the answer was a candidate rather than a refusal.
	picked bool
	// chosen is the candidate itself.
	chosen core.Candidate
}

// pickerWidth and pickerHeight are the size the screen draws at until the
// program has said how big the terminal is.
//
// A table can be drawn unconstrained and left for the terminal to fold, which
// is what the apply confirmation does; a list cannot, because its height is
// how many candidates it shows and zero of them is an empty screen. So this
// one guesses, and the guess is the size of the smallest terminal anybody
// still has.
const (
	pickerWidth  = 80
	pickerHeight = 24
)

// Picker builds the screen for a question and the candidates that answer it.
// The question is the caller's, for the same reason the apply confirmation's
// is: what is being asked about is the command's business and not this
// package's.
func (r Renderer) Picker(question string, candidates []core.Candidate) Picker {
	items := make([]list.Item, 0, len(candidates))
	detailed := false
	for _, candidate := range candidates {
		items = append(items, pickerItem{candidate: candidate})
		detailed = detailed || candidate.Detail != ""
	}

	choose := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "choose"))
	model := list.New(items, pickerDelegate{styles: r.styles, detailed: detailed, ascii: r.ascii, choose: choose},
		pickerWidth, pickerHeight)
	model.SetFilteringEnabled(true)
	// The question and the key line are drawn by this screen, so that both
	// of them fit the terminal: a question is wrapped and a key line is cut
	// short, and the list gets what is left over.
	model.SetShowTitle(false)
	model.SetShowStatusBar(false)
	model.SetShowHelp(false)
	// The list's own quit binding is what cancelling is, so that the key
	// that leaves this screen is the key that leaves every other list the
	// person has used. Only the word for it changes: nothing is being quit
	// here, the question is being left unanswered.
	model.KeyMap.Quit = key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("esc", "cancel"))
	model.Styles = r.listStyles()
	// The paginator is given its marks when the list is built, so a palette
	// assigned afterwards has to be handed to it again.
	model.Paginator.ActiveDot = model.Styles.ActivePaginationDot.String()
	model.Paginator.InactiveDot = model.Styles.InactivePaginationDot.String()
	// Nor is the filter: the list builds its own input and leaves it in the
	// library's colours, which is a screen with colour on it in the mode
	// that asked for none.
	model.FilterInput.SetStyles(model.Styles.Filter)
	if r.ascii {
		asciiKeys(&model)
	}

	screen := Picker{renderer: r, question: question, list: model, choose: choose}
	return screen.resize(pickerWidth, pickerHeight)
}

// asciiKeys names the movement keys in ASCII. bubbles names them with arrows,
// and nothing a screen draws goes through the engine's glyph table on its way
// out — a screen is drawn rather than decorated — so the line explaining
// which key does what would otherwise be the one line on an --ascii screen
// that is not ASCII.
func asciiKeys(model *list.Model) {
	model.KeyMap.CursorUp.SetHelp("up/k", "up")
	model.KeyMap.CursorDown.SetHelp("down/j", "down")
	model.KeyMap.PrevPage.SetHelp("left/pgup", "prev page")
	model.KeyMap.NextPage.SetHelp("right/pgdn", "next page")
}

// resize lays the screen out for a terminal of this size: the question at the
// top, the key line at the bottom, and every row the list can draw in
// between.
func (p Picker) resize(width, height int) Picker {
	p.width, p.height = width, height
	spent := lipgloss.Height(p.head()) + lipgloss.Height(p.foot())
	p.list.SetSize(width, max(height-spent, 1))
	return p
}

// head is the question, wrapped to the terminal. A question is the one thing
// on the screen that must not be cut short: a person who cannot read it
// cannot answer it.
func (p Picker) head() string {
	return p.renderer.styles.Accent.Render(lipgloss.NewStyle().Width(p.width).Render(p.question)) + "\n"
}

// foot is the key line, cut to the terminal. It is the list's own account of
// itself — which keys are live depends on whether a filter is being typed —
// with this screen's one binding added by the delegate.
func (p Picker) foot() string {
	line, _ := truncate(p.renderer.helpLine(p.list.ShortHelp()...), p.width, p.renderer.ascii)
	return "\n" + p.renderer.styles.Muted.Render(line)
}

// Init implements [tea.Model]. The screen asks the world for nothing: it was
// handed the candidates, and a picker that went looking would be able to
// offer something the engine did not.
func (p Picker) Init() tea.Cmd { return nil }

// Update implements [tea.Model].
//
// Two things make this more than a wrapper. A key belongs to the filter
// before it belongs to the screen — enter applies a filter and escape
// abandons one, and a picker that read either of those as an answer would
// make typing a name impossible. And the first answer is the answer, as it is
// on the confirmation: a key already in the terminal's buffer when the
// question was settled is still delivered, and must not settle it again.
func (p Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return p.resize(size.Width, size.Height), nil
	}
	if p.answered {
		return p, nil
	}
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p.delegate(msg)
	}
	switch {
	case key.Matches(press, p.list.KeyMap.ForceQuit):
		return p.cancel()
	case p.list.FilterState() == list.Filtering:
		// Every other key is the filter's while one is being typed. The
		// list disables its own quit and clear bindings for exactly this
		// reason, and borrowing them is how this screen inherits it.
		return p.delegate(msg)
	case key.Matches(press, p.choose):
		return p.pick()
	case key.Matches(press, p.list.KeyMap.ClearFilter):
		// Escape with a filter applied takes the filter off. It is only a
		// refusal once there is nothing left to back out of, which is what
		// the list has already decided by enabling this binding or not.
		return p.delegate(msg)
	case key.Matches(press, p.list.KeyMap.Quit):
		return p.cancel()
	default:
		return p.delegate(msg)
	}
}

// delegate hands a message to the list and keeps what it made of it.
func (p Picker) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := p.list.Update(msg)
	p.list = updated
	return p, cmd
}

// pick takes the candidate under the cursor. A filter that matches nothing
// has no candidate under it, and enter on an empty list is not an answer —
// it is a person who has typed too much.
func (p Picker) pick() (tea.Model, tea.Cmd) {
	item, ok := p.list.SelectedItem().(pickerItem)
	if !ok {
		return p, nil
	}
	p.answered, p.picked, p.chosen = true, true, item.candidate
	return p, tea.Quit
}

// cancel leaves the question unanswered.
func (p Picker) cancel() (tea.Model, tea.Cmd) {
	p.answered = true
	return p, tea.Quit
}

// View implements [tea.Model]: the question, the candidates, and the keys
// that answer it.
func (p Picker) View() tea.View {
	return tea.NewView(p.head() + p.list.View() + p.foot())
}

// Answered reports whether a key press settled the question, whether by
// picking a candidate or by refusing to.
func (p Picker) Answered() bool { return p.answered }

// Picked reports whether a candidate was chosen. The zero screen has not
// picked anything, which is what makes a picker that was never answered — an
// interrupt, a terminal closing, a model that came back as something else —
// leave the placeholder for somebody to resolve by hand.
func (p Picker) Picked() bool { return p.picked }

// Chosen is the candidate, meaningful only when [Picker.Picked] is true.
func (p Picker) Chosen() core.Candidate { return p.chosen }

// pickerItem is a candidate as something the list can show and filter.
//
// Only the name is filtered on. A fuzzy match reports which characters of the
// filter value it matched so that they can be highlighted, and highlighting
// them means the string being filtered and the string being drawn are the
// same string; filtering over the identifier as well would light up
// characters of a name that had nothing to do with what was typed. A person
// who has the identifier does not need a picker — they have `--set`.
type pickerItem struct{ candidate core.Candidate }

// FilterValue implements [list.Item].
func (i pickerItem) FilterValue() string { return i.candidate.Name }

// pickerDelegate draws one candidate.
//
// bubbles ships a delegate that does this, and it comes with a palette of its
// own: flow has seven styles and a mode with no colour at all (spec 12), and
// a row painted in somebody else's hues would ignore both. So the mechanism
// is the list's and the painting is this package's.
type pickerDelegate struct {
	styles Styles
	// detailed is whether any candidate has a line under its name. One
	// candidate with a detail gives every row two lines, because a list
	// whose rows are different heights puts the cursor somewhere different
	// depending on what is above it.
	detailed bool
	ascii    bool
	choose   key.Binding
}

// Height implements [list.ItemDelegate].
func (d pickerDelegate) Height() int {
	if d.detailed {
		return 2
	}
	return 1
}

// Spacing implements [list.ItemDelegate].
func (d pickerDelegate) Spacing() int { return 0 }

// Update implements [list.ItemDelegate]. A row has no state of its own.
func (d pickerDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// ShortHelp implements [help.KeyMap], which is how the list learns about the
// one binding that is not its own. The keys on the screen are then the list's
// account of itself, and cannot fall out of step with what Update does.
func (d pickerDelegate) ShortHelp() []key.Binding { return []key.Binding{d.choose} }

// FullHelp implements [help.KeyMap].
func (d pickerDelegate) FullHelp() [][]key.Binding { return [][]key.Binding{{d.choose}} }

// pickerCursor is what marks the row a key press would take. It is two
// characters wide in both alphabets, so that a name starts in the same column
// whether or not it is selected.
const (
	pickerCursor = "> "
	pickerIndent = "  "
)

// Render implements [list.ItemDelegate].
func (d pickerDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	row, ok := item.(pickerItem)
	if !ok {
		return
	}
	width := m.Width() - len(pickerCursor)
	if width <= 0 {
		return
	}

	name, matched := truncate(row.candidate.Name, width, d.ascii)
	style, cursor := d.styles.Code, pickerIndent
	if index == m.Index() {
		style, cursor = d.styles.Accent, pickerCursor
	}
	if m.FilterState() != list.Unfiltered {
		name = lipgloss.StyleRunes(name, within(m.MatchesForItem(index), matched),
			style.Inline(true).Underline(true), style.Inline(true))
	} else {
		name = style.Render(name)
	}
	fmt.Fprint(w, cursor+name)

	if !d.detailed {
		return
	}
	detail, _ := truncate(row.candidate.Detail, width, d.ascii)
	fmt.Fprint(w, "\n"+pickerIndent+d.styles.Muted.Render(detail))
}

// truncate cuts a string to a width, reporting how many runes survived so
// that highlights past the cut can be dropped.
//
// It counts runes rather than cells. A board named in Japanese will be cut
// short of the width available rather than past it, which costs a column of
// terminal and never a broken line.
func truncate(text string, width int, ascii bool) (string, int) {
	runes := []rune(text)
	if len(runes) <= width {
		return text, len(runes)
	}
	ellipsis := "…"
	if ascii {
		ellipsis = "..."
	}
	kept := max(width-len([]rune(ellipsis)), 0)
	return string(runes[:kept]) + ellipsis, kept
}

// within is the matched rune positions that are still on the screen.
func within(matches []int, kept int) []int {
	var visible []int
	for _, at := range matches {
		if at < kept {
			visible = append(visible, at)
		}
	}
	return visible
}

// listStyles is the list's chrome in flow's palette: the question, the filter
// prompt, the pagination dots and the line that says nothing matched.
func (r Renderer) listStyles() list.Styles {
	active, inactive := r.dots()
	styles := list.Styles{
		TitleBar:              lipgloss.NewStyle().Padding(0, 0, 1, 0),
		Title:                 r.styles.Accent,
		Spinner:               r.styles.Muted,
		NoItems:               r.styles.Muted,
		PaginationStyle:       r.styles.Muted,
		ActivePaginationDot:   r.styles.Accent.SetString(active),
		InactivePaginationDot: r.styles.Muted.SetString(inactive),
		ArabicPagination:      r.styles.Muted,
		DividerDot:            r.styles.Muted.SetString(" " + inactive + " "),

		StatusBar:                   r.styles.Muted,
		StatusEmpty:                 r.styles.Muted,
		StatusBarActiveFilter:       r.styles.Accent,
		StatusBarFilterCount:        r.styles.Muted,
		DefaultFilterCharacterMatch: r.styles.Accent.Underline(true),
	}
	styles.Filter.Focused.Prompt = r.styles.Accent
	styles.Filter.Focused.Text = r.styles.Code
	styles.Filter.Focused.Placeholder = r.styles.Muted
	styles.Filter.Blurred = styles.Filter.Focused
	return styles
}

// dots are the pagination marks: which page of candidates is on the screen.
//
// They are not findings, so they are not in the glyph table the engine's
// symbols go through (spec 12); they are furniture belonging to a list. What
// they share with that table is the rule that makes it worth having: the two
// must still differ when there is no colour to tell them apart.
func (r Renderer) dots() (active, inactive string) {
	if r.ascii {
		return "*", "-"
	}
	return "●", "○"
}
