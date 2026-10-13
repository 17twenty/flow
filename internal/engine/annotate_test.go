package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/engine"
)

// CI annotations (item 5.8, spec 02 §2). A job's own interface holds a few
// lines, read by somebody who never opens the log, so what goes in it is a
// decision about which findings are worth somebody's attention — and these
// hold flow to it.

// annotated is one check's answer, as a CI job would be told it.
func annotated(t *testing.T, checked engine.Checked) (engine.Annotation, bool) {
	t.Helper()
	return checked.Annotation()
}

// check is a checked result with the given id and answer.
func check(id string, result engine.Result) engine.Checked {
	return engine.Checked{ID: id, Title: "the trunk is protected", Category: "forge", Result: result}
}

// actionable is a failure flow has something to suggest about.
func actionable() engine.Result {
	result := engine.Fail("the trunk is not protected")
	result.Remediation = engine.Remediation{
		Kind:    engine.RemedyManual,
		Summary: "protect main",
		Owner:   "@lead",
		Command: "flow apply",
		Steps:   []string{"open the settings", "add a rule"},
	}
	return result
}

// TestOnlyWhatSomebodyMustDoIsAnnotated: an annotation competes with every
// other annotation on the run, so a passing check must not file one, and a
// check that was never asked because something else failed must not file a
// second one about that same failure.
func TestOnlyWhatSomebodyMustDoIsAnnotated(t *testing.T) {
	cases := []struct {
		name   string
		result engine.Result
		level  engine.AnnotationLevel
		said   bool
	}{
		{"a check that held", engine.Pass("the trunk is protected"), "", false},
		{"a check that does not apply", engine.Skip("no trunk is configured"), "", false},
		{"a check nobody asked", engine.Result{State: engine.StateBlocked, Message: "not asked"}, "", false},
		{"a failure with a remedy", actionable(), engine.AnnotationWarning, true},
		{"a failure with nothing to suggest", engine.Fail("the trunk is not protected"), engine.AnnotationError, true},
		{"a check that could not find out", engine.Failed(errors.New("the forge said no")), engine.AnnotationError, true},
		{"a live check below its threshold", engine.Result{State: engine.StateWarn, Message: "3 of 10"}, engine.AnnotationWarning, true},
		{"a value only a person has", engine.Result{State: engine.StateInput, Message: "which board?"}, engine.AnnotationWarning, true},
		{"something somebody else was asked for", engine.Result{State: engine.StateExternal, Message: "#12 is open"}, engine.AnnotationWarning, true},
		{"a promise that has lapsed", engine.Result{State: engine.StateAttest, Message: "attest it again"}, engine.AnnotationWarning, true},
		{"something that stopped holding", engine.Result{State: engine.StateDrift, Message: "the column was renamed"}, engine.AnnotationWarning, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			annotation, said := annotated(t, check("forge.protection", tc.result))
			if said != tc.said {
				t.Fatalf("annotated = %v, want %v", said, tc.said)
			}
			if said && annotation.Level != tc.level {
				t.Errorf("level %q, want %q", annotation.Level, tc.level)
			}
		})
	}
}

// TestAFailureFlowCanHelpWithIsNotShoutedAt: the level a reader sees is the
// same judgement as the code the run exits on (spec 07 §2). A failure with a
// remedy is somebody's afternoon and exits 2; a failure flow has nothing to
// say about is the run exiting 1, and the two must not look alike.
func TestAFailureFlowCanHelpWithIsNotShoutedAt(t *testing.T) {
	soft := engine.Report{Checks: []engine.Checked{check("forge.protection", actionable())}}
	hard := engine.Report{Checks: []engine.Checked{check("forge.protection", engine.Fail("the trunk is not protected"))}}

	if got := soft.Annotations()[0].Level; got != engine.AnnotationWarning {
		t.Errorf("a remediable failure is %q, want warning (it exits %d)", got, soft.Exit())
	}
	if got := hard.Annotations()[0].Level; got != engine.AnnotationError {
		t.Errorf("a failure with no remedy is %q, want error (it exits %d)", got, hard.Exit())
	}
}

// TestAnAnnotationSaysWhatWasFoundAndWhatToDo: the whole of it is the title
// a person reads, the count a live check turned in, the finding and the
// remedy. A reader who sees only this must not have to open the log to know
// whether it is theirs.
func TestAnAnnotationSaysWhatWasFoundAndWhatToDo(t *testing.T) {
	result := actionable()
	result.Counts = &engine.Counts{Pass: 3, Total: 10}
	annotation, said := annotated(t, check("forge.protection", result))
	if !said {
		t.Fatal("a failure was not annotated")
	}
	if annotation.Title != "forge.protection" {
		t.Errorf("title %q, want the check's id, which is what --check takes", annotation.Title)
	}
	for _, want := range []string{
		"the trunk is protected",
		"3/10",
		"the trunk is not protected",
		"fix: protect main (@lead)",
		"run: flow apply",
		"1. open the settings",
		"2. add a rule",
	} {
		if !strings.Contains(annotation.Message, want) {
			t.Errorf("the annotation does not say %q:\n%s", want, annotation.Message)
		}
	}
}

// TestAnAnnotationLeavesTheEvidenceInTheReport: a live check holding sixty
// pull requests would fill the run's interface with the log, and the thing
// annotations are for is the one line that says which log to open.
func TestAnAnnotationLeavesTheEvidenceInTheReport(t *testing.T) {
	result := engine.Result{State: engine.StateWarn, Message: "3 of 10 merged without a ticket"}
	result.Evidence = []engine.Evidence{{Text: "#41 said nothing", URL: "https://example.test/41"}}
	annotation, said := annotated(t, check("live.prs.linked", result))
	if !said {
		t.Fatal("a warning was not annotated")
	}
	if strings.Contains(annotation.Message, "#41") {
		t.Errorf("the annotation repeats the evidence:\n%s", annotation.Message)
	}
}

// TestAnnotationsComeInTheOrderTheChecksWereAsked: the report is grouped by
// category and ordered by dependency, and an interface that reordered the
// same findings would make the two documents impossible to read together.
func TestAnnotationsComeInTheOrderTheChecksWereAsked(t *testing.T) {
	report := engine.Report{Checks: []engine.Checked{
		check("config.syntax", engine.Pass("it parses")),
		check("forge.protection", actionable()),
		check("tracker.columns", engine.Skip("no board")),
		check("live.prs.linked", engine.Result{State: engine.StateWarn, Message: "3 of 10"}),
	}}
	var ids []string
	for _, annotation := range report.Annotations() {
		ids = append(ids, annotation.Title)
	}
	want := []string{"forge.protection", "live.prs.linked"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("annotated %v, want %v", ids, want)
	}
}

// TestARunThatFoundNothingAnnotatesNothing: a green morning says nothing at
// all, so that the morning something is wrong says exactly one thing.
func TestARunThatFoundNothingAnnotatesNothing(t *testing.T) {
	if got := greenReport().Annotations(); len(got) != 0 {
		t.Errorf("a run where everything held filed %d annotations: %v", len(got), got)
	}
}

// TestTheRemedyIsWordedOnceForEveryReader: the terminal indents the remedy
// under the finding and the annotation carries it on the end of a message,
// and both take the words from the same place (spec 05 §5).
func TestTheRemedyIsWordedOnceForEveryReader(t *testing.T) {
	report := engine.Report{Checks: []engine.Checked{check("forge.protection", actionable())}}
	annotation := report.Annotations()[0]
	for _, line := range strings.Split(annotation.Message, "\n")[1:] {
		if !strings.Contains(report.Text(), "      "+line+"\n") {
			t.Errorf("the terminal does not carry the annotation's line %q:\n%s", line, report.Text())
		}
	}
	if !strings.Contains(annotation.Message, "fix: ") {
		t.Fatalf("the annotation carries no remedy, so this proves nothing:\n%s", annotation.Message)
	}
}

// TestNothingToDoIsNotWordedAsASuggestion: a remedy of none is flow saying
// there is nothing to be done here, and a message ending in a blank line of
// advice would read as advice that went missing.
func TestNothingToDoIsNotWordedAsASuggestion(t *testing.T) {
	result := engine.Fail("the trunk is not protected")
	result.Remediation = engine.Remediation{Kind: engine.RemedyNone, Summary: "nothing to be done"}
	annotation, _ := annotated(t, check("forge.protection", result))
	if strings.Contains(annotation.Message, "\n") {
		t.Errorf("a remedy of none still wrote advice:\n%s", annotation.Message)
	}
}
