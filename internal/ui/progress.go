package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/engine"
)

// The doctor progress display (spec 12 §3). A doctor run reads a fortnight of
// pull requests, a board, a branch's protection and a deployment history, and
// it does all of it before it prints anything: the report is one document and
// a document half-written is not one. So a terminal gets nothing for however
// long that takes, which is the thing this screen exists to fix.
//
// It is inline rather than full-screen. The report is what the person came
// for and it has to land in their scrollback where the rest of their shell
// session is; an alternate screen would take the terminal over and hand it
// back empty. `--watch` is the one that takes the screen, and it is a
// different thing to want.

// Progress is the screen a doctor run draws while it is running: the checks
// that have answered, and a spinner on the one being asked.
//
// It is a [tea.Model] fed by [engine.DoctorStep]s, which the run produces as
// it reaches each check. The steps arrive from another goroutine, so they go
// through the program's own message queue rather than into the model.
type Progress struct {
	// renderer paints the lines, in the options the report that follows will
	// be painted in.
	renderer Renderer
	// spinner is the mark that says flow is still asking.
	spinner spinner.Model
	// asking is the check in flight, empty before the first one and after
	// the last.
	asking engine.Check
	// answered is the checks that have answered, newest last.
	answered []engine.Checked
	// asked and total are where the run has got to.
	asked, total int
	// done is whether the run has finished, which empties the screen: the
	// report is about to be printed and a progress display left above it
	// would be the same findings twice.
	done bool
	// width is the terminal's, as the program reported it.
	width int
}

// progressTail is how many answered checks stay on the screen. Enough to see
// the run moving and to read the last finding as it lands; few enough that
// the display stays a few lines of a shell session rather than taking the
// window. Everything they say is in the report that follows them.
const progressTail = 5

// progressWidth is the width lines are cut to until the program has said how
// wide the terminal is. A check's line is one line by construction, and a
// line that folds would make the display taller every time it redrew.
const progressWidth = 80

// Progress builds the screen for a run that is about to start.
func (r Renderer) Progress() Progress {
	mark := spinner.New(spinner.WithSpinner(spinner.Dot))
	if r.ascii {
		mark = spinner.New(spinner.WithSpinner(spinner.Line))
	}
	mark.Style = r.styles.Muted
	return Progress{renderer: r, spinner: mark, width: progressWidth}
}

// Init implements [tea.Model]: the spinner starts turning.
func (p Progress) Init() tea.Cmd { return p.spinner.Tick }

// Update implements [tea.Model].
//
// A key press is not handled, and that is deliberate. The run is already
// happening and this screen cannot stop it; ctrl-c reaches the run through
// the context the program was given, which is the one thing that can.
func (p Progress) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		return p, nil
	case engine.DoctorStep:
		return p.step(msg)
	case Finished:
		p.done = true
		return p, tea.Quit
	default:
		mark, cmd := p.spinner.Update(msg)
		p.spinner = mark
		return p, cmd
	}
}

// Finished is the message that says the run is over. It is the program's to
// send, not the run's: a run that failed before it reached its first check
// still has to let go of the terminal.
type Finished struct{}

// step takes a check being reached or a check that has answered.
func (p Progress) step(step engine.DoctorStep) (tea.Model, tea.Cmd) {
	if !step.Answered() {
		p.asking = step.Check
		p.asked, p.total = step.Asked, step.Total
		return p, nil
	}
	p.asked, p.total = step.Asked, step.Total
	p.answered = append(p.answered, engine.Checked{
		ID:       step.Check.ID,
		Title:    step.Check.Title,
		Category: step.Check.Category,
		Result:   step.Result,
	})
	if len(p.answered) > progressTail {
		p.answered = p.answered[len(p.answered)-progressTail:]
	}
	return p, nil
}

// View implements [tea.Model]: the last few findings, and what is being asked
// now.
//
// A finished run draws nothing. The program is about to print the report, and
// a view left behind would put the same findings on the screen twice.
func (p Progress) View() tea.View {
	if p.done {
		return tea.NewView("")
	}
	var out strings.Builder
	for _, checked := range p.answered {
		line, _ := truncate(checked.Line(), p.width, p.renderer.ascii)
		out.WriteString(p.renderer.Line(line))
		out.WriteString("\n")
	}
	out.WriteString(p.waiting())
	return tea.NewView(out.String())
}

// waiting is the line that says what flow is asking and how far through it
// is. A run that has not reached a check yet has nothing to name, so it says
// only that it has started — the spinner is the part that matters there.
//
// What it is cut to is the id, not the count: a line that lost its last
// characters to a narrow terminal would lose the one number somebody is
// watching for.
func (p Progress) waiting() string {
	mark := p.spinner.View()
	room := max(p.width-lipgloss.Width(mark)-1, 0)
	if p.asking.ID == "" {
		started, _ := truncate(p.renderer.words("asking…"), room, p.renderer.ascii)
		return mark + " " + p.renderer.styles.Muted.Render(started)
	}
	tail := p.renderer.words(" · " + p.counted())
	id, _ := truncate(p.asking.ID, max(room-len([]rune(tail)), 0), p.renderer.ascii)
	return mark + " " + p.renderer.styles.Code.Render(id) + p.renderer.styles.Muted.Render(tail)
}

// counted is how far through the run is, in the words the summary line of a
// report would use. It is a proportion rather than a bar: the checks take
// wildly different times, so a bar that moved evenly would be a lie, and the
// number somebody wants is how many are left.
func (p Progress) counted() string {
	return fmt.Sprintf("%d of %d", p.asked+1, p.total)
}
