package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The init wizard (spec 12 §1). It is the first thing anybody sees of flow,
// and the only screen that runs before there is a configuration to read.
//
// What it collects is exactly the flags of spec 02 §2 — the wizard is a way
// of typing them, not a second way of configuring a repository. That is what
// makes the plain equivalent plain: the same answers given as flags produce
// the same file byte for byte, because they go down the same road. The review
// step prints that command line, so somebody who has just answered eight
// questions can put the answer in a script and never be asked again.

// Answers is what a wizard collected, one field per flag it fills. An empty
// field is a question nobody answered, which the preset's own value — or its
// placeholder — survives into the written file.
type Answers struct {
	// Preset is the workflow to start from.
	Preset string
	// Forge is the forge kind: github, gitea or gitlab.
	Forge string
	// Tracker is the tracker kind.
	Tracker string
	// TrackerURL is the tracker's base URL, for the self-hosted ones.
	TrackerURL string
	// Owner is the GitHub Projects owner login.
	Owner string
	// Project is the board, by whatever reference the tracker uses.
	Project string
}

// Command is the plain equivalent: the `flow init` that writes this same
// configuration without asking anything.
//
// Only answers that differ from what flow would have done unasked are
// printed. A command line repeating the detected forge and the default preset
// would be longer, and every extra flag in it is a thing somebody has to
// decide whether they meant.
func (a Answers) Command(detected Answers) string {
	out := []string{"flow", "init"}
	for _, flag := range []struct{ name, value, already string }{
		{"--preset", a.Preset, detected.Preset},
		{"--forge", a.Forge, detected.Forge},
		{"--tracker", a.Tracker, detected.Tracker},
		{"--tracker-url", a.TrackerURL, detected.TrackerURL},
		{"--owner", a.Owner, detected.Owner},
		{"--project", a.Project, detected.Project},
	} {
		if flag.value == "" || flag.value == flag.already {
			continue
		}
		out = append(out, flag.name, flag.value)
	}
	return strings.Join(out, " ")
}

// Preview renders the configuration a set of answers produces, so that the
// review step shows the file itself rather than a summary of it. It is the
// command's own code path — this package cannot build a configuration and
// should not learn how — and it may fail, which the screen reports instead of
// the file.
type Preview func(Answers) ([]byte, error)

// Wizard is the screen spec 12 §1 describes: the forge flow detected, the
// tracker and its board, the workflow to start from — which is also the
// answer to when a ticket is done and where deployments are read from — and
// then the rendered `.flow.yaml` to agree to.
//
// It is a [tea.Model], so a test drives it through Update exactly as a
// terminal does, and every view is a pure function of the answers so far.
type Wizard struct {
	renderer Renderer
	// steps are the questions, in order. Which of them apply depends on the
	// answers, so the list is fixed and some of its entries are skipped.
	steps []wizardStep
	// at is the question being asked. len(steps) is the review.
	at int
	// answers is what has been said so far, starting from what flow worked
	// out by itself.
	answers Answers
	// detected is what flow would have written had nobody been asked: the
	// starting point with its blanks filled in. The review prints only the
	// flags that differ from it, because a command line repeating what flow
	// does unasked is longer and every flag in it is one more thing somebody
	// has to decide whether they meant.
	detected Answers
	// input is the field a text step is typed into. It is one model reused
	// by each text step, because only one of them is ever on the screen.
	input textinput.Model
	// preview renders the file for the review step.
	preview Preview
	// width and height are the terminal's, as the program reported them.
	// Zero means it has not said yet.
	width, height int
	// keys are the bindings, which differ between a question and the review.
	keys wizardKeys
	// confirmed is whether somebody said yes to the rendered file. The zero
	// screen is a no: an interrupt, a terminal closing or a model that came
	// back as something else all mean nothing was agreed to.
	confirmed bool
	// done is whether the screen has finished, either way.
	done bool
}

// wizardStep is one question. A step with options is a list to move through;
// a step without them is a line to type.
type wizardStep struct {
	// flag is the command-line flag this step fills, which is also what the
	// step is called in the progress line.
	flag string
	// prompt is the question.
	prompt string
	// options are the answers to choose between. Empty makes it a text step.
	options []wizardOption
	// placeholder is what an empty text answer means, shown in the field.
	placeholder string
	// note is a line under the answers, for a question whose answer carries
	// more than the word it is spelled with.
	note string
	// when reports whether this step applies to these answers. Nil is always.
	when func(Answers) bool
	// get and set are where the answer lives on [Answers]. They are closures
	// rather than a name because flow does not reflect over its own types,
	// and eight of them written out is eight things somebody can read.
	get func(Answers) string
	set func(*Answers, string)
	// cursor is which option is under the hand.
	cursor int
}

// wizardOption is one answer to a choice step, and the reason to pick it.
type wizardOption struct{ value, why string }

// wizardKeys are the bindings. A question and the review are answered
// differently, so which of these are live depends on where the screen is.
type wizardKeys struct {
	up, down, next, back, quit, yes, no key.Binding
}

// wizardBindings is the keyboard. Back is escape rather than an arrow because
// a text step is being typed into, and left is where the cursor goes. Quit is
// q on a list and not in a field, where q is a letter somebody is spelling a
// board's name with; ctrl-c is both, because ctrl-c is always both.
func wizardBindings() wizardKeys {
	return wizardKeys{
		up:   key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/down", "choose")),
		down: key.NewBinding(key.WithKeys("down", "j")),
		next: key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("enter", "next")),
		back: key.NewBinding(key.WithKeys("esc", "shift+tab"), key.WithHelp("esc", "back")),
		quit: key.NewBinding(key.WithKeys("q", "Q", "ctrl+c"), key.WithHelp("q", "cancel")),
		yes:  key.NewBinding(key.WithKeys("y", "Y", "enter"), key.WithHelp("y", "write it")),
		no:   key.NewBinding(key.WithKeys("n", "N", "q", "Q", "ctrl+c"), key.WithHelp("n", "cancel")),
	}
}

// Wizard builds the screen. The detected answers are what flow would have
// written had nobody been asked — the forge the remote names, the preset it
// defaults to and that preset's own tracker — and they are both the starting
// position of every question and the baseline the review's command line is
// shortened against. A person who presses enter all the way through gets
// exactly the file `flow init --yes` writes.
func (r Renderer) Wizard(detected Answers, preview Preview) Wizard {
	input := textinput.New()
	input.Prompt = "> "
	input.SetVirtualCursor(true)
	input.SetStyles(r.fieldStyles())
	wizard := Wizard{
		renderer: r,
		steps:    wizardSteps(),
		answers:  detected,
		detected: detected,
		input:    input,
		preview:  preview,
		keys:     wizardBindings(),
		at:       -1,
	}
	// The screen opens on its first question rather than on step zero: the
	// cursor has to be pointed at what flow already worked out, and whether
	// the first step is even one of this configuration's questions is a thing
	// only the answers know.
	return wizard.advance()
}

// fieldStyles paints a text field out of flow's own palette rather than the
// library's. A field keeping its own colours would be the one thing on the
// screen that --no-color did not turn off, and the one thing that looked
// like a different program.
func (r Renderer) fieldStyles() textinput.Styles {
	state := textinput.StyleState{
		Text:        r.styles.Code,
		Placeholder: r.styles.Muted,
		Suggestion:  r.styles.Muted,
		Prompt:      r.styles.Muted,
	}
	return textinput.Styles{Focused: state, Blurred: state}
}

// wizardSteps is the order of spec 12 §1: the forge flow found, the tracker
// and the board on it, then the workflow to start from — which is where spec
// 12's last two questions are answered, because in flow's configuration they
// are not separate from it. The file comes last, because it is the answer to
// all of them.
func wizardSteps() []wizardStep {
	projects := func(a Answers) bool { return a.Tracker == "github-projects" }
	return []wizardStep{{
		flag:   "--forge",
		prompt: "which forge is this repository on?",
		options: []wizardOption{
			{"github", "github.com or GitHub Enterprise Server"},
			{"gitea", "Gitea, Forgejo or Codeberg"},
			{"gitlab", "gitlab.com or self-managed GitLab"},
		},
		get: func(a Answers) string { return a.Forge },
		set: func(a *Answers, v string) { a.Forge = v },
	}, {
		flag:   "--tracker",
		prompt: "where does the team's work live?",
		options: []wizardOption{
			{"github-projects", "a GitHub Projects board"},
			{"jira-cloud", "a Jira Cloud project"},
			{"linear", "a Linear team"},
		},
		get: func(a Answers) string { return a.Tracker },
		set: func(a *Answers, v string) { a.Tracker = v },
	}, {
		flag:        "--owner",
		prompt:      "whose board is it?",
		placeholder: "the user or organisation login",
		when:        projects,
		get:         func(a Answers) string { return a.Owner },
		set:         func(a *Answers, v string) { a.Owner = v },
	}, {
		flag:        "--tracker-url",
		prompt:      "what is the tracker's address?",
		placeholder: "https://your-team.atlassian.net",
		when:        func(a Answers) bool { return a.Tracker == "jira-cloud" },
		get:         func(a Answers) string { return a.TrackerURL },
		set:         func(a *Answers, v string) { a.TrackerURL = v },
	}, {
		flag:        "--project",
		prompt:      "which board?",
		placeholder: "leave it blank and flow doctor will offer you the list",
		get:         func(a Answers) string { return a.Project },
		set:         func(a *Answers, v string) { a.Project = v },
	}, {
		flag:   "--preset",
		prompt: "which workflow should it start from?",
		options: []wizardOption{
			{"lean-kanban", "five columns, work in progress limited; done when a pull request merges"},
			{"deploy-done", "the same, but nothing is done until the deployment is running"},
			{"minimal", "the fewest rules flow can still prove anything about; done on merge"},
		},
		note: "this is also the answer to when a ticket is done; every preset reads deployments from the forge's own records, which the file below is where you change",
		get:  func(a Answers) string { return a.Preset },
		set:  func(a *Answers, v string) { a.Preset = v },
	}}
}

// Init implements [tea.Model]. The screen asks the world for nothing: it was
// built on its first question, and everything it draws after that is an
// answer somebody gave it.
func (w Wizard) Init() tea.Cmd { return nil }

// Update implements [tea.Model].
func (w Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		w.input.SetWidth(fieldWidth(msg.Width))
		return w, nil
	case tea.KeyPressMsg:
		if w.done {
			// The first answer is the answer. A key already in the
			// terminal's buffer when somebody pressed y must not be able to
			// turn a yes into a no on the way out.
			return w, nil
		}
		return w.press(msg)
	default:
		return w, nil
	}
}

// press is one key, routed by where the screen is: the review has two
// answers, a choice step moves a cursor, and a text step is mostly the
// field's own business.
func (w Wizard) press(press tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if w.reviewing() {
		return w.reviewPress(press)
	}
	step := w.steps[w.at]
	typing := len(step.options) == 0
	switch {
	case key.Matches(press, w.keys.back):
		return w.leaving(w.retreat())
	case key.Matches(press, w.keys.next):
		return w.advance(), nil
	case key.Matches(press, w.keys.quit) && (!typing || press.String() == "ctrl+c"):
		w.done = true
		return w, tea.Quit
	case typing:
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(press)
		return w, cmd
	case key.Matches(press, w.keys.up):
		w.steps[w.at].cursor = wrap(step.cursor-1, len(step.options))
		return w, nil
	case key.Matches(press, w.keys.down):
		w.steps[w.at].cursor = wrap(step.cursor+1, len(step.options))
		return w, nil
	default:
		return w, nil
	}
}

// leaving carries a retreat out of the program when it ran out of questions
// to go back to. Backing out of the first question is cancelling, and a
// screen that recorded that and went on drawing would be a screen with no way
// out but an interrupt.
func (w Wizard) leaving(after Wizard) (tea.Model, tea.Cmd) {
	if after.done {
		return after, tea.Quit
	}
	return after, nil
}

// reviewPress answers the last question, which is the only one whose answer
// is not written down: yes writes the file, no writes nothing.
func (w Wizard) reviewPress(press tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(press, w.keys.back):
		return w.leaving(w.retreat())
	case key.Matches(press, w.keys.yes):
		w.confirmed, w.done = true, true
		return w, tea.Quit
	case key.Matches(press, w.keys.no):
		w.done = true
		return w, tea.Quit
	default:
		return w, nil
	}
}

// advance records this step's answer and moves to the next question that
// applies, or to the review when there is none. It is also how the screen
// opens, from before the first step.
//
// The answer is taken here rather than as it is typed, because a step that
// recorded every keystroke would change which later steps apply while
// somebody was still in the middle of deciding.
func (w Wizard) advance() Wizard {
	w.answers = w.answered()
	for at := w.at + 1; at < len(w.steps); at++ {
		if w.steps[at].applies(w.answers) {
			return w.arrive(at)
		}
	}
	return w.arrive(len(w.steps))
}

// retreat goes back to the previous question that applied. Backing out of the
// first one is cancelling: there is nothing behind it, and a screen that
// swallowed escape there would be a screen with no way out but ctrl-c.
func (w Wizard) retreat() Wizard {
	for at := w.at - 1; at >= 0; at-- {
		if w.steps[at].applies(w.answers) {
			return w.arrive(at)
		}
	}
	w.done = true
	return w
}

// arrive puts the screen on a step, which means pointing the cursor or the
// field at what that step already says. Revisiting a question shows the
// answer given last time, not an empty field: backing up to check something
// must not cost the answer.
func (w Wizard) arrive(at int) Wizard {
	w.at = at
	if at >= len(w.steps) {
		w.input.Blur()
		return w
	}
	step := w.steps[at]
	if len(step.options) > 0 {
		w.steps[at].cursor = optionIndex(step.options, step.get(w.answers))
		w.input.Blur()
		return w
	}
	w.input.SetValue(step.get(w.answers))
	w.input.CursorEnd()
	w.input.Focus()
	return w
}

// answered is the answers with this step's reading of itself folded in. The
// screen before the first question and the review are both places where
// there is no step to read, and both leave the answers as they are.
func (w Wizard) answered() Answers {
	if w.at < 0 || w.reviewing() {
		return w.answers
	}
	answers, step := w.answers, w.steps[w.at]
	if len(step.options) > 0 {
		step.set(&answers, step.options[step.cursor].value)
		return answers
	}
	step.set(&answers, strings.TrimSpace(w.input.Value()))
	return answers
}

// applies reports whether a step is one of this configuration's questions.
func (s wizardStep) applies(a Answers) bool { return s.when == nil || s.when(a) }

// reviewing reports whether the screen is past the last question.
func (w Wizard) reviewing() bool { return w.at >= len(w.steps) }

// Confirmed reports whether somebody agreed to the file. A screen nobody
// answered is a no, which is what makes an interrupted init write nothing.
func (w Wizard) Confirmed() bool { return w.confirmed }

// Answers is what was collected. They are only meaningful alongside
// [Wizard.Confirmed]: a cancelled wizard has answers too, and they are
// answers to a question that was withdrawn.
func (w Wizard) Answers() Answers { return w.answers }

// View implements [tea.Model].
func (w Wizard) View() tea.View {
	if w.reviewing() {
		return tea.NewView(w.review())
	}
	return tea.NewView(w.question())
}

// question draws one step: where the screen is, what is being asked, and the
// answers to it.
func (w Wizard) question() string {
	step := w.steps[w.at]
	var out strings.Builder
	out.WriteString(w.heading())
	out.WriteString("\n\n  ")
	out.WriteString(w.renderer.styles.Accent.Render(w.renderer.words(step.prompt)))
	out.WriteString("\n\n")
	if len(step.options) > 0 {
		out.WriteString(w.options(step))
		if step.note != "" {
			out.WriteString("  " + w.renderer.styles.Muted.Render(w.renderer.words(step.note)) + "\n")
		}
		out.WriteString("\n")
		out.WriteString(w.renderer.styles.Muted.Render(w.renderer.helpLine(
			w.keys.up, w.keys.next, w.keys.back, w.keys.quit)))
	} else {
		out.WriteString("  " + w.input.View() + "\n")
		if step.placeholder != "" {
			out.WriteString("  " + w.renderer.styles.Muted.Render(w.renderer.words(step.placeholder)) + "\n")
		}
		out.WriteString("\n")
		out.WriteString(w.renderer.styles.Muted.Render(w.renderer.helpLine(w.keys.next, w.keys.back)))
	}
	out.WriteString("\n")
	return out.String()
}

// options is a choice step's list: one row per answer, the one under the hand
// marked, and the reason for each beside it.
func (w Wizard) options(step wizardStep) string {
	var out strings.Builder
	for i, option := range step.options {
		marker, value := "  ", w.renderer.styles.Code.Render(option.value)
		if i == step.cursor {
			marker = w.renderer.words("→") + " "
			value = w.renderer.styles.Accent.Render(option.value)
		}
		out.WriteString("  " + marker + value)
		if option.why != "" {
			out.WriteString("  " + w.renderer.styles.Muted.Render(w.renderer.words(option.why)))
		}
		out.WriteString("\n")
	}
	return out.String()
}

// review draws the file these answers make, and the command line that makes
// it again without the questions.
func (w Wizard) review() string {
	var out strings.Builder
	out.WriteString(w.heading())
	out.WriteString("\n\n")
	out.WriteString(w.file())
	out.WriteString("\n  ")
	out.WriteString(w.renderer.styles.Muted.Render(w.renderer.words("the same file, without the questions:")))
	out.WriteString("\n  ")
	out.WriteString(w.renderer.styles.Code.Render(w.answers.Command(w.detected)))
	out.WriteString("\n\n")
	out.WriteString(w.renderer.styles.Muted.Render(w.renderer.helpLine(w.keys.yes, w.keys.no, w.keys.back)))
	out.WriteString("\n")
	return out.String()
}

// file is the rendered configuration, cut to what is left of the terminal.
//
// It is cut rather than folded, and cut rather than scrolled: the review is
// there to show somebody that the answers became a file they recognise, and
// the file itself is about to be in their working tree where they can read
// all of it.
func (w Wizard) file() string {
	if w.preview == nil {
		return "  " + w.renderer.styles.Muted.Render(w.renderer.words("(no preview)")) + "\n"
	}
	source, err := w.preview(w.answers)
	if err != nil {
		return "  " + w.renderer.styles.Err.Render(w.renderer.words(err.Error())) + "\n"
	}
	lines := strings.Split(strings.TrimRight(string(source), "\n"), "\n")
	shown, hidden := lines, 0
	if room := w.room(); room > 0 && len(lines) > room {
		shown, hidden = lines[:room], len(lines)-room
	}
	var out strings.Builder
	out.WriteString("  " + w.renderer.styles.Accent.Render(configFileName) + "\n")
	for _, line := range shown {
		shown, _ := truncate(line, w.lineWidth(), w.renderer.ascii)
		out.WriteString("  " + w.renderer.styles.Muted.Render(shown) + "\n")
	}
	if hidden > 0 {
		out.WriteString("  " + w.renderer.styles.Muted.Render(
			w.renderer.words(fmt.Sprintf("… and %d more lines", hidden))) + "\n")
	}
	return out.String()
}

// lineWidth is how much of a line of the file fits, once the indent has had
// its two columns. A terminal that has not said how wide it is gets the file
// uncut, for the same reason it gets it unshortened.
func (w Wizard) lineWidth() int {
	const indent = 2
	if w.width <= indent {
		return maxLineWidth
	}
	return w.width - indent
}

// maxLineWidth is what a line is cut to when nobody has said: more than any
// line of a preset, so cutting at it changes nothing.
const maxLineWidth = 1 << 20

// room is how many lines of the file the terminal has left once the heading,
// the command line and the keys have had theirs. Zero means the terminal has
// not said how tall it is, and the whole file is drawn: guessing twenty-four
// rows would cut a file that fits on a tall terminal.
func (w Wizard) room() int {
	if w.height <= 0 {
		return 0
	}
	const furniture = 9
	if room := w.height - furniture; room > 0 {
		return room
	}
	return 1
}

// configFileName is what the wizard calls the file it is previewing. It is
// the root's `configFile`, repeated here because this package renders and
// does not import the command.
const configFileName = ".flow.yaml"

// heading is the line that says where in the wizard the screen is. The
// questions are numbered so that somebody can tell a long form from a short
// one before they start answering it.
func (w Wizard) heading() string {
	at, total := w.place()
	dot := w.renderer.words("·")
	where := fmt.Sprintf("flow init %s review", dot)
	if total > 0 {
		where = fmt.Sprintf("flow init %s %d of %d", dot, at, total)
	}
	return w.renderer.styles.Accent.Render(where)
}

// place is which question this is and how many there are, counting only the
// ones that apply to the answers so far. The review has no number: it is not
// a question, and numbering it would make the count go up at the end.
func (w Wizard) place() (at, total int) {
	for i, step := range w.steps {
		if !step.applies(w.answers) {
			continue
		}
		total++
		if i == w.at {
			at = total
		}
	}
	if w.reviewing() {
		return 0, 0
	}
	return at, total
}

// fieldWidth is how wide a text field is drawn: most of the terminal, with
// room for the indent and the prompt, and never so narrow that an answer
// cannot be read back.
func fieldWidth(terminal int) int {
	const minimum, margin = 20, 8
	if terminal-margin > minimum {
		return terminal - margin
	}
	return minimum
}

// optionIndex is where a value sits in a step's options, or the first of them
// when it is not one of the answers. A configuration whose preset flow does
// not offer still has to put the cursor somewhere.
func optionIndex(options []wizardOption, value string) int {
	for i, option := range options {
		if option.value == value {
			return i
		}
	}
	return 0
}

// wrap moves a cursor around a list, so that up from the top is the bottom.
func wrap(at, length int) int {
	switch {
	case length == 0:
		return 0
	case at < 0:
		return length - 1
	case at >= length:
		return 0
	}
	return at
}
