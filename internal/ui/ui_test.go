package ui_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// TestTextIsTheEnginesLayoutUntouched holds the UI to the one thing that
// makes two renderings of the same run comparable: with no colour and no
// ASCII, what flow prints is exactly what the engine said, byte for byte.
// A terminal view that had drifted from the plain one would be a second
// layout nobody is maintaining.
func TestTextIsTheEnginesLayoutUntouched(t *testing.T) {
	plan := samplePlan()
	got, err := ui.New(ui.Options{}).Plan(plan)
	if err != nil {
		t.Fatalf("render the plan: %v", err)
	}
	if got != plan.Text() {
		t.Errorf("the plain plan is not the engine's:\n--- got\n%s\n--- want\n%s", got, plan.Text())
	}

	report := sampleReport()
	got, err = ui.New(ui.Options{Format: ui.FormatText}).Report(report)
	if err != nil {
		t.Fatalf("render the report: %v", err)
	}
	if got != report.Text() {
		t.Errorf("the plain report is not the engine's:\n--- got\n%s\n--- want\n%s", got, report.Text())
	}
}

// TestColourIsOnlyColour proves the palette adds nothing but escape
// sequences: take them out again and the plain rendering is back. This is
// what lets --no-color be a presentation flag rather than a second code path.
func TestColourIsOnlyColour(t *testing.T) {
	for _, dark := range []bool{true, false} {
		report := sampleReport()
		styled, err := ui.New(ui.Options{Colour: true, Dark: dark}).Report(report)
		if err != nil {
			t.Fatalf("render the report: %v", err)
		}
		if !strings.Contains(styled, "\x1b[") {
			t.Fatalf("colour was asked for and none was painted (dark=%v)", dark)
		}

		// Writing to a buffer is writing to something that is not a
		// terminal, which is exactly when the colour must come off.
		var out bytes.Buffer
		if err := ui.Fprint(&out, styled); err != nil {
			t.Fatalf("print: %v", err)
		}
		if out.String() != report.Text() {
			t.Errorf("colour changed the words (dark=%v):\n--- got\n%s\n--- want\n%s", dark, out.String(), report.Text())
		}
	}
}

// TestLightAndDarkDiffer checks the two palettes are actually two palettes.
// Picking a colour pair and using the same half of it twice is a bug that
// only somebody with the wrong terminal background would ever notice.
func TestLightAndDarkDiffer(t *testing.T) {
	plan := samplePlan()
	dark, err := ui.New(ui.Options{Colour: true, Dark: true}).Plan(plan)
	if err != nil {
		t.Fatal(err)
	}
	light, err := ui.New(ui.Options{Colour: true, Dark: false}).Plan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if dark == light {
		t.Error("the dark and light palettes rendered the same bytes")
	}
}

// TestPlainPaletteIsPlain holds the zero palette to painting nothing. Every
// renderer paints unconditionally and lets the palette decide, so a style
// that decorated in the plain palette would put ANSI in a pipe.
func TestPlainPaletteIsPlain(t *testing.T) {
	plain := ui.PlainStyles()
	for name, style := range map[string]func() string{
		"Ok":     func() string { return plain.Ok.Render("x") },
		"Warn":   func() string { return plain.Warn.Render("x") },
		"Err":    func() string { return plain.Err.Render("x") },
		"Info":   func() string { return plain.Info.Render("x") },
		"Muted":  func() string { return plain.Muted.Render("x") },
		"Accent": func() string { return plain.Accent.Render("x") },
		"Code":   func() string { return plain.Code.Render("x") },
	} {
		if got := style(); got != "x" {
			t.Errorf("the plain %s style rendered %q", name, got)
		}
	}
}

// TestEveryStyleIsItsOwn checks the seven styles are seven, not five and two
// aliases. Two findings painted the same colour are two findings a reader
// cannot tell apart.
func TestEveryStyleIsItsOwn(t *testing.T) {
	styles := ui.NewStyles(true)
	seen := map[string]string{}
	for name, rendered := range map[string]string{
		"Ok":     styles.Ok.Render("x"),
		"Warn":   styles.Warn.Render("x"),
		"Err":    styles.Err.Render("x"),
		"Info":   styles.Info.Render("x"),
		"Muted":  styles.Muted.Render("x"),
		"Accent": styles.Accent.Render("x"),
		"Code":   styles.Code.Render("x"),
	} {
		if rendered == "x" {
			t.Errorf("the %s style painted nothing", name)
		}
		if other, clash := seen[rendered]; clash {
			t.Errorf("%s and %s are the same style", name, other)
		}
		seen[rendered] = name
	}
}

// TestASCIIStandsInForEveryGlyph walks every glyph the engine can produce and
// insists the terminal that cannot draw it still gets something, and that no
// two findings end up looking alike. A fallback that mapped warn and error
// both to "!" would make --ascii lie.
func TestASCIIStandsInForEveryGlyph(t *testing.T) {
	outcomes := []engine.Outcome{
		engine.OutcomeNoop, engine.OutcomeNative, engine.OutcomeCompensated,
		engine.OutcomeFollowUp, engine.OutcomeAttest, engine.OutcomeUnsupported,
	}
	states := []engine.State{
		engine.StatePass, engine.StateFail, engine.StateWarn, engine.StateInput,
		engine.StateBlocked, engine.StateExternal, engine.StateAttest,
		engine.StateDrift, engine.StateSkip, engine.StateError,
	}

	symbols := map[string]bool{}
	for _, outcome := range outcomes {
		symbols[outcome.Symbol()] = true
	}
	for _, state := range states {
		symbols[state.Symbol()] = true
	}

	ascii := ui.New(ui.Options{ASCII: true})
	stood := map[string]string{}
	for symbol := range symbols {
		rendered := ascii.Line(symbol + " thing")
		stand, _, _ := strings.Cut(rendered, " ")
		if stand == "" {
			t.Fatalf("the %q glyph rendered as nothing", symbol)
		}
		for _, r := range stand {
			if r > 127 {
				t.Errorf("the ASCII fallback for %q is %q, which is not ASCII", symbol, stand)
			}
		}
		if other, clash := stood[stand]; clash && other != symbol {
			t.Errorf("%q and %q both fall back to %q", symbol, other, stand)
		}
		stood[stand] = symbol
	}
}

// TestASCIILeavesProseAlone checks the fallback translates a line's glyph and
// its punctuation and nothing else. The error glyph is "!", and a message
// with an exclamation mark in it must come through as somebody wrote it.
func TestASCIILeavesProseAlone(t *testing.T) {
	ascii := ui.New(ui.Options{ASCII: true})
	if got, want := ascii.Line("! live.deploys  nothing deployed. check it!"), "E live.deploys  nothing deployed. check it!"; got != want {
		t.Errorf("ASCII mangled the message:\n got %q\nwant %q", got, want)
	}
	if got, want := ascii.Line("⚠ follow-up → @platform  tracker.automation"), "! follow-up -> @platform  tracker.automation"; got != want {
		t.Errorf("ASCII did not translate the line:\n got %q\nwant %q", got, want)
	}
}

// TestJSONIsNeitherColouredNorTranslated holds the machine-readable document
// apart from the two things that would break the machine reading it.
func TestJSONIsNeitherColouredNorTranslated(t *testing.T) {
	renderer := ui.New(ui.Options{Format: ui.FormatJSON, Colour: true, ASCII: true, Dark: true})
	rendered, err := renderer.Report(sampleReport())
	if err != nil {
		t.Fatalf("render the report: %v", err)
	}
	if strings.Contains(rendered, "\x1b[") {
		t.Error("the JSON document carries escape sequences")
	}
	plain, err := sampleReport().JSON()
	if err != nil {
		t.Fatal(err)
	}
	if rendered != string(plain) {
		t.Errorf("the JSON document is not the engine's:\n--- got\n%s\n--- want\n%s", rendered, plain)
	}
}

// TestAFormatNothingRendersIsRefused checks flow does not answer a question
// it was not asked. A caller that wanted JSON and got a table would hand it
// to something waiting to parse it.
func TestAFormatNothingRendersIsRefused(t *testing.T) {
	renderer := ui.New(ui.Options{Format: "html"})
	if _, err := renderer.Plan(samplePlan()); !errors.Is(err, core.ErrValidation) {
		t.Errorf("rendering a plan as html gave %v", err)
	}
	if _, err := renderer.Report(sampleReport()); !errors.Is(err, core.ErrValidation) {
		t.Errorf("rendering a report as html gave %v", err)
	}
}

// TestDarkBackgroundDoesNotInterrogateAPipe is the whole reason that
// detection is guarded: the query is an escape sequence written to the
// terminal and an answer read back from it, and a pipe would get the question
// in its output and never send a reply.
func TestDarkBackgroundDoesNotInterrogateAPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	done := make(chan bool, 1)
	go func() { done <- ui.DarkBackground(reader, writer) }()
	select {
	case dark := <-done:
		if !dark {
			t.Error("a terminal that could not be asked was assumed light")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DarkBackground asked a pipe and waited for an answer")
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var wrote bytes.Buffer
	if _, err := wrote.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if wrote.Len() != 0 {
		t.Errorf("DarkBackground wrote %q down a pipe", wrote.String())
	}
}

// TestColourSaysWhatKindOfFindingItIs pins the glyph-to-colour mapping. The
// golden files cannot: they are written through a pipe, where the colour has
// already been taken off, so painting a warning in the colour of a failure
// would look perfect in every other test in this file.
func TestColourSaysWhatKindOfFindingItIs(t *testing.T) {
	styles := ui.NewStyles(true)
	renderer := ui.New(ui.Options{Colour: true, Dark: true})
	for symbol, want := range map[string]lipgloss.Style{
		"✓": styles.Ok,
		"✗": styles.Err,
		"!": styles.Err,
		"⚠": styles.Warn,
		"≠": styles.Warn,
		"↻": styles.Info,
		"⏳": styles.Info,
		"?": styles.Info,
		"✎": styles.Accent,
		"·": styles.Muted,
		"⏸": styles.Muted,
	} {
		line := renderer.Line(symbol + " a.check  something")
		if painted := want.Render(symbol); !strings.HasPrefix(line, painted) {
			t.Errorf("%q was painted %q, wanted %q", symbol, line, painted)
		}
		if !strings.HasSuffix(line, " a.check  something") {
			t.Errorf("%q had its words painted too: %q", symbol, line)
		}
	}
}

// TestColourSaysWhatKindOfLineItIs pins the rest of the palette's work: a
// heading, a hazard, a thing to run and the detail under a finding are all
// told apart by colour alone, and nothing else on the line is touched.
func TestColourSaysWhatKindOfLineItIs(t *testing.T) {
	styles := ui.NewStyles(true)
	renderer := ui.New(ui.Options{Colour: true, Dark: true})
	for name, tc := range map[string]struct {
		line string
		want string
	}{
		"a category heading": {
			line: engine.CategoryTracker,
			want: styles.Accent.Render(engine.CategoryTracker),
		},
		"a hazard": {
			line: "    hazard: this clears what flow does not send",
			want: "    " + styles.Warn.Render("hazard: this clears what flow does not send"),
		},
		"a remedy": {
			line: "      fix: protect main",
			want: "      " + styles.Info.Render("fix: protect main"),
		},
		"a command": {
			line: "      run: flow apply",
			want: "      " + styles.Info.Render("run: flow apply"),
		},
		"the detail under a finding": {
			line: "    main is unprotected",
			want: "    " + styles.Muted.Render("main is unprotected"),
		},
		"a line flow does not recognise": {
			line: "flow.plan/v1",
			want: "flow.plan/v1",
		},
	} {
		if got := renderer.Line(tc.line); got != tc.want {
			t.Errorf("%s was painted %q, wanted %q", name, got, tc.want)
		}
	}
}

// TestTextGolden pins what a terminal sees, with and without the glyphs.
func TestTextGolden(t *testing.T) {
	plan, report := samplePlan(), sampleReport()
	for _, tc := range []struct {
		name  string
		opts  ui.Options
		plan  string
		doctr string
	}{
		{name: "unicode", opts: ui.Options{}, plan: "plan.txt", doctr: "doctor.txt"},
		{name: "ascii", opts: ui.Options{ASCII: true}, plan: "plan.ascii.txt", doctr: "doctor.ascii.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			renderer := ui.New(tc.opts)
			rendered, err := renderer.Plan(plan)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tc.plan, rendered)

			rendered, err = renderer.Report(report)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tc.doctr, rendered)
		})
	}
}

// TestMarkdownGolden pins what an issue comment and a --report file get.
func TestMarkdownGolden(t *testing.T) {
	plan, report := samplePlan(), sampleReport()
	for _, tc := range []struct {
		name  string
		opts  ui.Options
		plan  string
		doctr string
	}{
		{
			name: "unicode",
			opts: ui.Options{Format: ui.FormatMarkdown},
			plan: "plan.md", doctr: "doctor.md",
		},
		{
			name: "ascii",
			opts: ui.Options{Format: ui.FormatMarkdown, ASCII: true},
			plan: "plan.ascii.md", doctr: "doctor.ascii.md",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			renderer := ui.New(tc.opts)
			rendered, err := renderer.Plan(plan)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tc.plan, rendered)

			rendered, err = renderer.Report(report)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tc.doctr, rendered)
		})
	}
}

// TestMarkdownTablesSurviveWhatPeopleWrite checks a message with a pipe or a
// newline in it cannot break the table it is rendered into.
func TestMarkdownTablesSurviveWhatPeopleWrite(t *testing.T) {
	report := sampleReport()
	report.Checks = []engine.Checked{{
		ID:       "config.syntax",
		Title:    "the configuration parses",
		Category: engine.CategoryConfig,
		Result:   engine.Fail("a|b is wrong\nand so is c"),
	}}
	rendered, err := ui.New(ui.Options{Format: ui.FormatMarkdown}).Report(report)
	if err != nil {
		t.Fatal(err)
	}
	row := ""
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "config.syntax") {
			row = line
		}
	}
	if strings.Count(row, "|")-strings.Count(row, "\\|") != 4 {
		t.Errorf("the row has the wrong number of columns: %q", row)
	}
	if strings.Contains(row, "\n") || !strings.Contains(row, "and so is c") {
		t.Errorf("the message did not survive the table: %q", row)
	}
}
