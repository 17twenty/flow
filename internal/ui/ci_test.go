package ui_test

import (
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// CI annotations (item 5.8, spec 02 §2). The runner lifts lines of a
// particular shape out of a job's log and shows them on the run itself. This
// file is about that shape; which findings get one is the engine's.

// annotations renders the sample report's annotations with default options.
func annotations(t *testing.T, report engine.Report) []string {
	t.Helper()
	text := ui.New(ui.Options{}).Annotations(report)
	if text == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("the last annotation is not a line of its own:\n%q", text)
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// TestAnAnnotationIsOneLineTheRunnerRecognises: the runner reads lines, so
// every finding is exactly one line in the command syntax.
func TestAnAnnotationIsOneLineTheRunnerRecognises(t *testing.T) {
	lines := annotations(t, sampleReport())
	if len(lines) == 0 {
		t.Fatal("the sample report annotated nothing")
	}
	if len(lines) != len(sampleReport().Annotations()) {
		t.Errorf("%d lines for %d annotations", len(lines), len(sampleReport().Annotations()))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "::error title=") && !strings.HasPrefix(line, "::warning title=") {
			t.Errorf("a runner would not recognise %q", line)
		}
		if strings.Count(line, "::") < 2 {
			t.Errorf("%q has no message", line)
		}
	}
	want := "::warning title=forge.protection::the trunk is protected: main may be pushed to directly%0Afix: protect main%0Arun: flow apply"
	if lines[0] != want {
		t.Errorf("the first annotation is\n%s\nwant\n%s", lines[0], want)
	}
}

// TestNothingInAMessageCanSpeakToTheRunner is the one that matters for
// safety: the runner reads every line a job writes, and a check's message
// carries repository names, pull request titles and provider errors. A
// newline in any of them would end the command and offer the rest to the
// parser as a command of its own (spec 15).
func TestNothingInAMessageCanSpeakToTheRunner(t *testing.T) {
	report := engine.Report{Checks: []engine.Checked{{
		ID:    "forge.protection",
		Title: "the trunk is protected",
		Result: engine.Fail("%s", "a branch called 100% of it\n"+
			"::error::this is not flow speaking\n::stop-commands::x"),
	}}}
	text := ui.New(ui.Options{}).Annotations(report)
	if got := strings.Count(text, "\n"); got != 1 {
		t.Fatalf("a message with newlines in it became %d lines:\n%s", got+1, text)
	}
	if strings.Contains(text, "\n::") {
		t.Errorf("a message started a command of its own:\n%s", text)
	}
	for _, want := range []string{"%0A::error::", "100%25 of it", "%0A::stop-commands::"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message is not escaped as %q:\n%s", want, text)
		}
	}
}

// TestATitleCannotAddPropertiesOfItsOwn: a colon ends the property list and
// a comma starts another property, so a check id carrying either must arrive
// as part of the title.
func TestATitleCannotAddPropertiesOfItsOwn(t *testing.T) {
	report := engine.Report{Checks: []engine.Checked{{
		ID:     "forge.protection:file=.flow.yaml,line=1",
		Title:  "the trunk is protected",
		Result: engine.Fail("main may be pushed to directly"),
	}}}
	text := ui.New(ui.Options{}).Annotations(report)
	if !strings.Contains(text, "forge.protection%3Afile=.flow.yaml%2Cline=1::") {
		t.Errorf("the title was not escaped:\n%s", text)
	}
}

// TestARunWithNothingToSayWritesNothing: an empty string, not a blank line.
// A blank line in a CI log is noise, and a job that writes one every green
// morning teaches its readers to ignore the stream.
func TestARunWithNothingToSayWritesNothing(t *testing.T) {
	report := engine.Report{Checks: []engine.Checked{{
		ID:     "config.syntax",
		Title:  "the configuration parses",
		Result: engine.Pass(".flow.yaml is valid"),
	}}}
	if got := ui.New(ui.Options{}).Annotations(report); got != "" {
		t.Errorf("a green run wrote %q", got)
	}
}

// TestAnnotationsAreTheSameWhoeverAskedForThem: colour is ANSI in a field a
// web page shows again, the ASCII glyphs are for a terminal with a font
// missing, and JSON is a different document entirely. None of them is a
// property of the annotation stream.
func TestAnnotationsAreTheSameWhoeverAskedForThem(t *testing.T) {
	plain := ui.New(ui.Options{}).Annotations(sampleReport())
	for _, opts := range []ui.Options{
		{Colour: true, Dark: true},
		{ASCII: true},
		{Format: ui.FormatJSON},
		{Format: ui.FormatMarkdown},
	} {
		if got := ui.New(opts).Annotations(sampleReport()); got != plain {
			t.Errorf("options %+v rendered different annotations:\n%s\nwant\n%s", opts, got, plain)
		}
	}
}
