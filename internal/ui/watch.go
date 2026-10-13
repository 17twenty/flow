package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/engine"
)

// The doctor watch screen (spec 12 §3). `flow doctor` answers the question
// once and prints it; `--watch` is for the half hour somebody spends putting
// a repository right, where the answer changes under them and they want to
// see it change.
//
// It takes the alternate screen, which is the thing that makes it different
// from every other rendering flow produces: nothing it draws lands in the
// scrollback. That is the trade — the findings are a live display here rather
// than a document, and the document is what `flow doctor` on its own is for.

// Runner is what the watch screen can ask flow to do: ask the questions
// again, and record a human's word about one of them.
//
// It takes no context. A run belongs to the invocation, not to the screen,
// and the root closes over the one it was given — which is also what makes
// ctrl-c reach a run that is already in flight.
type Runner interface {
	// Doctor asks every check again, applying the automatic remediations
	// when fix is set, and returns what it found.
	Doctor(fix bool) (engine.Report, error)
	// Attest records a human's word that a check holds (spec 07 §8).
	Attest(check, note string) error
}

// WatchEvery is how often the screen asks again on its own.
//
// A run reads a fortnight of pull requests from a forge, so this is a rate
// limit as much as a refresh: often enough that a person who has just fixed
// something sees it go green without reaching for a key, rare enough that
// leaving the window open all afternoon is not a reason flow runs out of API
// quota. `r` is there for the person who does not want to wait.
const WatchEvery = time.Minute

// Watch is the screen `flow doctor --watch` draws: every check and what it
// found, the selected one in full, and the keys that act on it.
type Watch struct {
	renderer Renderer
	runner   Runner

	// checks is the findings, and pane is the selected one in full.
	checks list.Model
	pane   viewport.Model
	// spinner turns while a run is in flight.
	spinner spinner.Model
	// note is where a person types what they are vouching for, shown only
	// while attesting.
	note textinput.Model

	// report is the last run that finished, and reportErr why the last run
	// did not. A run that failed does not erase the findings it failed to
	// replace: the screen says the asking went wrong and leaves what was
	// true a minute ago on it, which is more than it could say by clearing.
	report    engine.Report
	reportErr error
	// running is whether a run is in flight, which is what stops two.
	running bool
	// attesting is which check the note is being typed about.
	attesting string
	// said is the last thing that happened that is not a finding: an
	// attestation recorded, a key that meant nothing here.
	said string

	// clock waits out the refresh interval and says it is time to ask
	// again. It is the screen's rather than a package function so that a
	// test can have the minute back: a clock a test had to wait out could
	// only be tested by not testing it.
	clock func() tea.Cmd

	keys          watchKeys
	width, height int
	ready         bool
}

// watchKeys are the bindings this screen owns. The list's own movement keys
// are not among them: they belong to the list, which is also what draws the
// part of the key line that names them.
type watchKeys struct {
	fix, rerun, attest, quit key.Binding
	up, down                 key.Binding
}

// newWatchKeys names what each key does, in the words the key line shows.
func newWatchKeys() watchKeys {
	return watchKeys{
		fix:    key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fix")),
		rerun:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rerun")),
		attest: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "attest")),
		quit:   key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
		up:     key.NewBinding(key.WithKeys("shift+up"), key.WithHelp("shift+up", "scroll")),
		down:   key.NewBinding(key.WithKeys("shift+down"), key.WithHelp("shift+down", "scroll")),
	}
}

// watchWidth and watchHeight are the size the screen draws at until the
// program has said how big the terminal is.
const (
	watchWidth  = 80
	watchHeight = 24
)

// Watch builds the screen. The runner is how it asks flow for anything; a
// screen with none can still be drawn, which is what a test of the layout
// does, and asks for nothing.
func (r Renderer) Watch(runner Runner) Watch {
	mark := spinner.New(spinner.WithSpinner(spinner.Dot))
	if r.ascii {
		mark = spinner.New(spinner.WithSpinner(spinner.Line))
	}
	mark.Style = r.styles.Muted

	// Filtering is off. `f` is fix on this screen, and a list that swallowed
	// it to start typing a filter would make the key that acts on a
	// repository depend on where the cursor had been. Narrowing a run is
	// what `--check` is for, and it narrows the asking rather than the
	// display.
	checks := list.New(nil, watchDelegate{renderer: r}, watchWidth, watchHeight)
	checks.SetFilteringEnabled(false)
	checks.SetShowTitle(false)
	checks.SetShowStatusBar(false)
	checks.SetShowHelp(false)
	checks.SetShowFilter(false)
	checks.Styles = r.listStyles()
	checks.Paginator.ActiveDot = checks.Styles.ActivePaginationDot.String()
	checks.Paginator.InactiveDot = checks.Styles.InactivePaginationDot.String()
	checks.KeyMap.Quit = key.NewBinding(key.WithDisabled())

	note := textinput.New()
	note.Prompt = ""
	note.SetStyles(r.inputStyles())

	screen := Watch{
		renderer: r,
		runner:   runner,
		checks:   checks,
		pane:     viewport.New(),
		spinner:  mark,
		note:     note,
		keys:     newWatchKeys(),
		// The screen opens asking. [Watch.Init] starts the first run and
		// cannot say so — a bubbletea Init takes no model and returns none —
		// so the run in flight is recorded here, where the screen that is
		// about to make it is built. A screen that opened idle would take an
		// r in that first second as a second run of the same question.
		running: runner != nil,
		clock:   watchTick,
	}
	return screen.resize(watchWidth, watchHeight)
}

// Init implements [tea.Model]: the spinner starts, the first run goes out,
// and the clock that asks again starts ticking.
func (w Watch) Init() tea.Cmd {
	return tea.Batch(w.spinner.Tick, w.ask(false), w.wind())
}

// watchRan is a run that has finished, however it went.
type watchRan struct {
	report engine.Report
	err    error
}

// watchAttested is an attestation that has been written, or has not.
type watchAttested struct {
	check string
	err   error
}

// watchTicked is the clock saying it is time to ask again.
type watchTicked struct{}

// watchTick waits out the refresh interval.
func watchTick() tea.Cmd {
	return tea.Tick(WatchEvery, func(time.Time) tea.Msg { return watchTicked{} })
}

// tick is the clock coming round.
//
// It winds itself again whatever it decides, and it is the only thing that
// winds it. That is what makes [WatchEvery] a rate limit rather than a
// starting point: a clock wound by each run that finished instead would leave
// a second chain of ticks behind every time somebody pressed r, and an
// afternoon of putting a repository right would end with flow asking a forge
// every few seconds.
func (w Watch) tick() (tea.Model, tea.Cmd) {
	if w.running || w.attesting != "" {
		return w, w.wind()
	}
	screen, ask := w.start(false)
	return screen, tea.Batch(ask, w.wind())
}

// wind sets the clock going again.
func (w Watch) wind() tea.Cmd {
	if w.clock == nil {
		return watchTick()
	}
	return w.clock()
}

// ask runs the checks, off the goroutine drawing the screen.
func (w Watch) ask(fix bool) tea.Cmd {
	if w.runner == nil {
		return nil
	}
	runner := w.runner
	return func() tea.Msg {
		report, err := runner.Doctor(fix)
		return watchRan{report: report, err: err}
	}
}

// Update implements [tea.Model].
func (w Watch) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return w.resize(msg.Width, msg.Height), nil
	case watchRan:
		return w.ran(msg)
	case watchAttested:
		return w.attested(msg)
	case watchTicked:
		return w.tick()
	case tea.KeyPressMsg:
		return w.press(msg)
	default:
		return w.background(msg)
	}
}

// background hands a message to the parts of the screen that animate
// themselves.
func (w Watch) background(msg tea.Msg) (tea.Model, tea.Cmd) {
	mark, spin := w.spinner.Update(msg)
	w.spinner = mark
	if w.attesting != "" {
		note, typed := w.note.Update(msg)
		w.note = note
		return w, tea.Batch(spin, typed)
	}
	return w, spin
}

// press reads a key.
//
// While a note is being typed every key but the two that settle it belongs to
// the input, for the same reason a filter owns the keyboard on the picker: a
// person writing "flagged by hand" must not have the f taken as fix.
func (w Watch) press(press tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if w.attesting != "" {
		return w.typing(press)
	}
	switch {
	case key.Matches(press, w.keys.quit):
		return w, tea.Quit
	case key.Matches(press, w.keys.rerun):
		return w.start(false)
	case key.Matches(press, w.keys.fix):
		return w.start(true)
	case key.Matches(press, w.keys.attest):
		return w.askWhy()
	case key.Matches(press, w.keys.up):
		w.pane.ScrollUp(1)
		return w, nil
	case key.Matches(press, w.keys.down):
		w.pane.ScrollDown(1)
		return w, nil
	default:
		checks, cmd := w.checks.Update(press)
		w.checks = checks
		return w.showSelected(), cmd
	}
}

// typing is a key while a note is being written.
func (w Watch) typing(press tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch press.String() {
	case "esc":
		w.attesting, w.said = "", "nothing was attested"
		w.note.Reset()
		return w, nil
	case "enter":
		return w.vouch()
	default:
		note, cmd := w.note.Update(press)
		w.note = note
		return w, cmd
	}
}

// askWhy opens the note for the selected check, when a human's word is what
// that check is waiting for.
//
// Which checks may be attested is the engine's judgement, carried on the
// result as a remedy (spec 07 §8). A screen that let somebody attest anything
// would be a way to silence a failure flow could have proved.
func (w Watch) askWhy() (tea.Model, tea.Cmd) {
	checked, ok := w.selected()
	switch {
	case !ok:
		w.said = "there is nothing on the screen to attest"
		return w, nil
	case checked.Result.Remediation.Kind != engine.RemedyAttest:
		w.said = fmt.Sprintf("%s is not waiting on anybody's word", checked.ID)
		return w, nil
	}
	w.attesting, w.said = checked.ID, ""
	w.note.Focus()
	return w, textinput.Blink
}

// vouch records what was typed. An empty note is not a refusal and not a
// record: there is nothing to write down, so the question stays open.
func (w Watch) vouch() (tea.Model, tea.Cmd) {
	note := strings.TrimSpace(w.note.Value())
	if note == "" {
		w.said = "an attestation has to say what was confirmed"
		return w, nil
	}
	runner := w.runner
	if runner == nil {
		return w, nil
	}
	check := w.attesting
	w.attesting, w.said = "", ""
	w.note.Reset()
	w.note.Blur()
	w.running = true
	return w, func() tea.Msg {
		return watchAttested{check: check, err: runner.Attest(check, note)}
	}
}

// attested takes the answer to a recorded promise. A promise that landed
// changes what the checks would say, so the screen asks them again rather
// than waiting for the clock.
func (w Watch) attested(msg watchAttested) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		w.running = false
		w.said = fmt.Sprintf("%s was not attested: %v", msg.check, msg.err)
		return w, nil
	}
	w.said = msg.check + " is attested"
	return w, w.ask(false)
}

// start asks the checks again, unless a run is already in flight. Two runs at
// once would be two sets of API calls answering the same question, and the
// slower one would overwrite the newer answer.
func (w Watch) start(fix bool) (tea.Model, tea.Cmd) {
	if w.running {
		w.said = "already asking"
		return w, nil
	}
	ask := w.ask(fix)
	if ask == nil {
		return w, nil
	}
	w.running, w.said = true, ""
	return w, ask
}

// ran takes a finished run and puts it on the screen, keeping the cursor on
// whatever check it was on.
func (w Watch) ran(msg watchRan) (tea.Model, tea.Cmd) {
	w.running = false
	w.reportErr = msg.err
	if msg.err != nil {
		return w, nil
	}
	w.report = msg.report
	selected := ""
	if checked, ok := w.selected(); ok {
		selected = checked.ID
	}

	items := make([]list.Item, 0, len(msg.report.Checks))
	for _, checked := range msg.report.Checks {
		items = append(items, watchItem{checked: checked})
	}
	w.checks.SetItems(items)
	for i, checked := range msg.report.Checks {
		if checked.ID == selected {
			w.checks.Select(i)
		}
	}
	w.ready = true
	return w.showSelected(), nil
}

// selected is the check under the cursor.
func (w Watch) selected() (engine.Checked, bool) {
	item, ok := w.checks.SelectedItem().(watchItem)
	return item.checked, ok
}

// showSelected puts the check under the cursor in the pane, keeping the pane
// at the top when the check has changed under it.
func (w Watch) showSelected() Watch {
	checked, ok := w.selected()
	if !ok {
		w.pane.SetContent("")
		return w
	}
	w.pane.SetContent(w.renderer.decorate(w.paneText(checked)))
	return w
}

// paneText is the selected check in full, wrapped to the pane. The words and
// what is indented under what are the engine's; the wrapping is the
// terminal's business and nothing else's.
func (w Watch) paneText(checked engine.Checked) string {
	wrap := lipgloss.NewStyle().Width(max(w.pane.Width(), 1))
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(checked.Detail(), "\n"), "\n") {
		out.WriteString(wrap.Render(line))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// resize lays the screen out: the summary at the top, the keys at the bottom,
// and the rest shared between the findings and the pane.
func (w Watch) resize(width, height int) Watch {
	w.width, w.height = width, height
	spent := lipgloss.Height(w.head()) + lipgloss.Height(w.foot())
	room := max(height-spent, 2)
	// The findings take two thirds and the pane the rest. A pane that took
	// half would show a check's whole evidence and leave eight findings
	// visible out of thirty, which is the wrong way round: the screen is for
	// seeing what is wrong, and the pane is for the one being worked on.
	rows := max(room*2/3, 1)
	w.checks.SetSize(width, rows)
	w.pane.SetWidth(width)
	w.pane.SetHeight(max(room-rows, 1))
	w.note.SetWidth(max(width-lipgloss.Width(w.notePrompt())-1, 1))
	return w.showSelected()
}

// head is the line that says how the repository stands, and whether flow is
// in the middle of asking.
func (w Watch) head() string {
	var line string
	switch {
	case w.reportErr != nil:
		line = w.renderer.styles.Err.Render(w.renderer.words("the run went wrong: " + w.reportErr.Error()))
	case !w.ready:
		line = w.renderer.styles.Muted.Render(w.renderer.words("asking\u2026"))
	default:
		line = w.renderer.Line(w.report.Summary())
	}
	if w.running {
		line = w.spinner.View() + " " + line
	}
	cut, _ := truncate(line, w.width, w.renderer.ascii)
	if lipgloss.Width(line) <= w.width {
		cut = line
	}
	return cut + "\n"
}

// foot is what a key does, or the note being typed, and the last thing that
// happened.
func (w Watch) foot() string {
	var bottom string
	if w.attesting != "" {
		bottom = w.renderer.styles.Accent.Render(w.notePrompt()) + " " + w.note.View()
	} else {
		keys := append(w.checks.ShortHelp(), w.keys.rerun, w.keys.fix, w.keys.attest, w.keys.up, w.keys.quit)
		line, _ := truncate(w.renderer.helpLine(keys...), w.width, w.renderer.ascii)
		bottom = w.renderer.styles.Muted.Render(line)
	}
	if w.said == "" {
		return "\n" + bottom
	}
	said, _ := truncate(w.said, w.width, w.renderer.ascii)
	return "\n" + w.renderer.styles.Info.Render(said) + "\n" + bottom
}

// notePrompt is what the note is being asked for.
func (w Watch) notePrompt() string {
	if w.attesting == "" {
		return ""
	}
	return w.renderer.words("why does " + w.attesting + " hold?")
}

// View implements [tea.Model].
//
// This is the one screen flow draws on the alternate screen. The others are
// a question and an answer and belong in the scrollback with the rest of the
// session; this one is a display of something that keeps changing, and
// leaving a minute's worth of stale reports behind it would be worse than
// leaving nothing.
func (w Watch) View() tea.View {
	view := tea.NewView(w.head() + w.checks.View() + "\n" + w.pane.View() + w.foot())
	view.AltScreen = true
	return view
}

// Report is the last run that finished, which is what the command exits on.
func (w Watch) Report() engine.Report { return w.report }

// Failing is why the last run did not answer, and nil when it did. A session
// that was closed before anything ever came back has no verdict to exit on,
// and this is the reason there is none.
func (w Watch) Failing() error { return w.reportErr }

// watchItem is a finding as something the list can show.
type watchItem struct{ checked engine.Checked }

// FilterValue implements [list.Item]. Nothing filters this list, and the id
// is what somebody would have typed if anything did.
func (i watchItem) FilterValue() string { return i.checked.ID }

// watchDelegate draws one finding, which is the engine's own line for it
// painted the way every other rendering of that line is painted.
type watchDelegate struct{ renderer Renderer }

// Height implements [list.ItemDelegate].
func (d watchDelegate) Height() int { return 1 }

// Spacing implements [list.ItemDelegate].
func (d watchDelegate) Spacing() int { return 0 }

// Update implements [list.ItemDelegate]. A row has no state of its own.
func (d watchDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// Render implements [list.ItemDelegate].
func (d watchDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	row, ok := item.(watchItem)
	if !ok {
		return
	}
	width := m.Width() - len(pickerCursor)
	if width <= 0 {
		return
	}
	cursor := pickerIndent
	if index == m.Index() {
		cursor = pickerCursor
	}
	line, _ := truncate(row.checked.Line(), width, d.renderer.ascii)
	fmt.Fprint(w, cursor+d.renderer.Line(line))
}

// inputStyles is a text input in flow's palette rather than the library's,
// for the same reason the picker's filter is.
func (r Renderer) inputStyles() textinput.Styles {
	styles := textinput.DefaultStyles(false)
	styles.Focused.Prompt = r.styles.Accent
	styles.Focused.Text = r.styles.Code
	styles.Focused.Placeholder = r.styles.Muted
	styles.Blurred = styles.Focused
	return styles
}
