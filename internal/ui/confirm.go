package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/17twenty/flow/internal/engine"
)

// The apply confirmation (spec 12 §4). It is the one screen that decides
// something: everything else flow draws is a report somebody reads, and this
// is a question somebody answers.
//
// What it changes is the shape, not the decision. Whether flow may ask at all
// is the root's business (spec 02 §1), what the outcomes are called and in
// what order they come is the engine's (spec 05 §5), and the same plan laid
// out as text and answered at a plain prompt is the same apply. So the model
// holds a rendering and an answer and nothing else: there is no state here
// that the plain path does not also have.

// Confirmation is the screen a person approves an apply on: the plan grouped
// by what flow would do about each item, and a yes or a no.
//
// It is a [tea.Model], so a test drives it the way a terminal does — through
// Update — and its view is a pure function of the plan it was built from.
type Confirmation struct {
	// renderer draws the table in the same options every other document this
	// run produces was drawn in.
	renderer Renderer
	// plan is what is being agreed to.
	plan engine.Plan
	// width is the terminal's, as the program reported it. Zero means it has
	// not said yet, and an unconstrained table is what gets drawn: guessing
	// eighty columns would fold a plan that fits on a wide terminal.
	width int
	// question is what is about to happen, in the caller's words.
	question string
	// keys are the two answers, in the vocabulary bubbles matches against.
	keys confirmKeys
	// answered is whether a key press settled it. A screen torn down before
	// an answer — an interrupt, a terminal closing — is a no, which is what
	// [Confirmation.Approved] reports.
	answered bool
	// approved is the answer itself.
	approved bool
}

// confirmKeys are the bindings the screen accepts. There are two, because
// there are two answers; a third would be a third thing to explain on a line
// that has to fit on one row.
type confirmKeys struct {
	yes key.Binding
	no  key.Binding
}

// confirmBindings is the keyboard: y for yes, and n, q, escape or ctrl-c for
// no. The capitals are listed because a key event carries the text the
// terminal produced, so a held shift arrives as "Y" and would otherwise go
// unrecognised on the one screen where being ignored is baffling.
func confirmBindings() confirmKeys {
	return confirmKeys{
		yes: key.NewBinding(key.WithKeys("y", "Y"), key.WithHelp("y", "apply")),
		no:  key.NewBinding(key.WithKeys("n", "N", "q", "Q", "esc", "ctrl+c"), key.WithHelp("n", "cancel")),
	}
}

// Confirmation builds the screen for a plan and the question that goes under
// it. The question is the caller's, because what is being agreed to — a count
// of changes — is the command's business and not this package's.
func (r Renderer) Confirmation(plan engine.Plan, question string) Confirmation {
	return Confirmation{
		renderer: r,
		plan:     plan,
		question: question,
		keys:     confirmBindings(),
	}
}

// Init implements [tea.Model]. The screen asks the world for nothing: it has
// the plan already, and a confirmation that went looking would be able to say
// something different from the plan it is confirming.
func (c Confirmation) Init() tea.Cmd { return nil }

// Update implements [tea.Model]. Only a key press means anything here, and
// only two of them; anything else leaves the screen exactly as it was, so a
// stray escape sequence or a resize cannot answer for the person reading it.
func (c Confirmation) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		c.width = size.Width
		return c, nil
	}
	// Quitting is a command and not an exit, so a key already in the
	// terminal's buffer when the question was answered is still delivered.
	// The first answer is the answer: a refusal that a stray keystroke could
	// turn into an apply would be worse than no confirmation at all.
	if c.answered {
		return c, nil
	}
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, nil
	}
	switch {
	case key.Matches(press, c.keys.yes):
		c.answered, c.approved = true, true
		return c, tea.Quit
	case key.Matches(press, c.keys.no):
		c.answered, c.approved = true, false
		return c, tea.Quit
	default:
		return c, nil
	}
}

// View implements [tea.Model]: the table, the question, and the two keys that
// answer it.
func (c Confirmation) View() tea.View {
	var out strings.Builder
	out.WriteString(c.renderer.planTable(c.plan, c.width))
	out.WriteString("\n\n")
	out.WriteString(c.renderer.styles.Accent.Render(c.question))
	out.WriteString("\n")
	out.WriteString(c.renderer.styles.Muted.Render(c.renderer.helpLine(c.keys)))
	out.WriteString("\n")
	return tea.NewView(out.String())
}

// Answered reports whether a key press settled the question. A screen that
// was never answered and one that was answered no both refuse the apply, but
// only one of them is a person saying no.
func (c Confirmation) Answered() bool { return c.answered }

// Approved reports whether flow may make the changes. The zero screen is a
// no, which is what makes an apply that was never answered — an interrupt, a
// terminal closing, a model that came back as something else — refuse rather
// than proceed.
func (c Confirmation) Approved() bool { return c.approved }

// helpLine is the key line: what each answer is called and what it does.
func (r Renderer) helpLine(keys confirmKeys) string {
	var offered []string
	for _, binding := range []key.Binding{keys.yes, keys.no} {
		help := binding.Help()
		offered = append(offered, fmt.Sprintf("%s %s", help.Key, help.Desc))
	}
	return strings.Join(offered, r.words(" · "))
}

// confirmRow is one line of the table: the cells, and the style the whole
// line is painted in when it is a heading rather than an item.
//
// The style travels with the row because lipgloss hands a style function
// coordinates and not content, and which rows are headings is a property of
// this table rather than of an index. Reading the outcome back out of the
// heading text would be the same knowledge written twice.
type confirmRow struct {
	cells   []string
	heading lipgloss.Style
	isGroup bool
}

// planTable lays the plan out as the outcome-grouped table spec 12 §4 asks
// for. The grouping is the point of the shape: an apply is agreed to as a few
// kinds of thing — this many flow will do, these it will hand to somebody,
// this one nobody can do — and a reader deciding in one key press should not
// have to count symbols down a column to find that out.
//
// The order of the groups and the words in them are the engine's, from the
// same list its summary line counts in, so the table and the summary can
// never disagree about what comes first.
func (r Renderer) planTable(plan engine.Plan, width int) string {
	if len(plan.Items) == 0 {
		return r.styles.Muted.Render(r.words(plan.Summary()))
	}
	var rows []confirmRow
	for _, outcome := range engine.Outcomes {
		items := grouped(plan, outcome)
		if len(items) == 0 {
			continue
		}
		style, _ := r.styles.forGlyph(outcome.Symbol())
		rows = append(rows, confirmRow{
			cells:   []string{r.heading(outcome, len(items)), "", ""},
			heading: style,
			isGroup: true,
		})
		for _, item := range items {
			rows = append(rows, confirmRow{cells: []string{
				"  " + item.ID, r.words(detailCell(item)), r.words(item.Why),
			}})
		}
	}
	border := lipgloss.NormalBorder()
	if r.ascii {
		border = lipgloss.ASCIIBorder()
	}
	return table.New().
		Width(width).
		Border(border).
		BorderStyle(r.styles.Muted).
		BorderColumn(false).
		BorderRow(false).
		StyleFunc(r.cellStyle(rows)).
		Rows(cells(rows)...).
		Render()
}

// heading is the row that opens a group: the outcome's glyph, its name, and
// how many items are under it.
func (r Renderer) heading(outcome engine.Outcome, count int) string {
	return fmt.Sprintf("%s %s (%d)", r.glyphText(outcome.Symbol()), outcome.Label(), count)
}

// detailCell is what an outcome puts after its arrow — the mechanism that
// compensates, the person a follow-up went to, the command that attests — and
// the arrow with it, because that is how the engine writes it everywhere else
// and a cell that dropped it would read as a second kind of thing.
func detailCell(item engine.PlanItem) string {
	if detail := item.Detail(); detail != "" {
		return "→ " + detail
	}
	return ""
}

// grouped is the plan's items with this outcome, in the plan's own order.
func grouped(plan engine.Plan, outcome engine.Outcome) []engine.PlanItem {
	var items []engine.PlanItem
	for _, item := range plan.Items {
		if item.Outcome == outcome {
			items = append(items, item)
		}
	}
	return items
}

// cells is the rows as lipgloss takes them.
func cells(rows []confirmRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.cells)
	}
	return out
}

// cellStyle paints the table: a heading in the colour its glyph already has
// everywhere else flow prints it, an item in the style ids are always in, and
// the reason it is there muted.
func (r Renderer) cellStyle(rows []confirmRow) table.StyleFunc {
	return func(row, col int) lipgloss.Style {
		style := r.styles.Muted
		switch {
		case row < 0 || row >= len(rows):
		case rows[row].isGroup:
			style = rows[row].heading
		case col == 0:
			style = r.styles.Code
		}
		return style.Padding(0, 1)
	}
}
