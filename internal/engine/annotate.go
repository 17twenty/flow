package engine

import "strings"

// CI annotations (spec 02 §2's `flow doctor --ci`). A CI job has an interface
// of its own — a list of findings attached to the run, read by people who
// never open the log — and this is what flow puts in it.
//
// As with the terminal and the markdown report, the words are the engine's:
// which findings are worth interrupting somebody for, how loudly each is
// said, and what it says are properties of the decision, so that a reader who
// only ever sees the annotations and a reader who reads the whole report
// cannot be told two different things. What the UI adds is the syntax the
// runner happens to parse.

// AnnotationLevel is how loudly a finding is said.
type AnnotationLevel string

// The levels flow uses. There is no third, quieter one: an annotation is for
// something somebody must do, and a finding that asks nothing of anybody
// belongs in the report, where it does not compete with the ones that do.
const (
	// AnnotationError is for a finding flow cannot help with.
	AnnotationError AnnotationLevel = "error"
	// AnnotationWarning is for a finding somebody can act on.
	AnnotationWarning AnnotationLevel = "warning"
)

// Annotation is one finding, as a CI job's own interface shows it.
type Annotation struct {
	// Level is how loudly to say it.
	Level AnnotationLevel
	// Title is the check's id, which is also what `--check` takes: the
	// shortest thing a reader can act on is the way to ask about it again.
	Title string
	// Message is what was found and what to do about it, one finding per
	// line.
	Message string
}

// Annotations are the findings worth attaching to a CI run, in the order the
// checks were asked.
//
// A run where everything held has none: a job's interface is for what went
// wrong, and twenty green annotations would make the one red one harder to
// find than no annotations at all.
func (r Report) Annotations() []Annotation {
	var annotations []Annotation
	for _, checked := range r.Checks {
		if annotation, ok := checked.Annotation(); ok {
			annotations = append(annotations, annotation)
		}
	}
	return annotations
}

// Annotation is what one check tells a CI job, and whether it tells it
// anything at all.
func (c Checked) Annotation() (Annotation, bool) {
	level, said := annotationLevel(c.Result)
	if !said {
		return Annotation{}, false
	}
	return Annotation{Level: level, Title: c.ID, Message: c.annotation()}, true
}

// annotationLevel grades a result the way [Report.Exit] does, because the
// level a reader sees and the code the job exits on are the same judgement:
// a failure flow has a remedy for is somebody's afternoon, a failure it has
// nothing to suggest about is flow saying something is wrong that nobody has
// been told how to fix, and a check that could not find out is in the second
// group rather than the first (spec 07 §2).
//
// Pass and skip say nothing. Nor does blocked: the check that did not hold is
// annotated, and a second line about the same fault, one step removed from
// it, sends the reader to the wrong place.
func annotationLevel(result Result) (AnnotationLevel, bool) {
	switch result.State {
	case StateError:
		return AnnotationError, true
	case StateFail:
		if result.Remediation.Actionable() {
			return AnnotationWarning, true
		}
		return AnnotationError, true
	case StateWarn, StateInput, StateExternal, StateAttest, StateDrift:
		return AnnotationWarning, true
	default:
		return "", false
	}
}

// annotation is the finding in the check's own words, with what to do about
// it beneath. The evidence is not here: it is in the log, in the report and
// in the JSON, and an annotation that repeats five pull request links is the
// report again with less around it to make sense of.
func (c Checked) annotation() string {
	var out strings.Builder
	out.WriteString(c.Title)
	if c.Result.Counts != nil {
		out.WriteString(" ")
		out.WriteString(c.Result.Counts.String())
	}
	if c.Result.Message != "" {
		out.WriteString(": ")
		out.WriteString(c.Result.Message)
	}
	for _, line := range c.Result.Remediation.Lines() {
		out.WriteString("\n")
		out.WriteString(line)
	}
	return out.String()
}
