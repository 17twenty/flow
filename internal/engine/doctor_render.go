package engine

import (
	"fmt"

	"strings"
)

// Doctor rendering (spec 07 §9). As with the plan, the words are the engine's
// and the layout is the UI's: the terminal, the markdown report and the CI
// annotation must not be able to disagree about what a check found or how many
// things there are to do.

// EvidenceShown is how many offending items a check lists before it starts
// counting them instead (spec 07 §9). The rest are in --json and --report: a
// terminal that scrolls a hundred pull requests past a reader has told them
// nothing, and the number is the part that matters on screen.
const EvidenceShown = 5

// Line is the one line that stands for a check: what it found, what it is,
// and how much of what it looked at was right.
func (c Checked) Line() string {
	var line strings.Builder
	line.WriteString(c.Result.State.Symbol())
	line.WriteString(" ")
	line.WriteString(c.ID)
	if c.Result.Counts != nil {
		line.WriteString(" ")
		line.WriteString(c.Result.Counts.String())
	}
	if c.Result.Message != "" {
		line.WriteString("  ")
		line.WriteString(c.Result.Message)
	}
	return line.String()
}

// Text renders a report in plain text, grouped by category, with each check's
// evidence and remedy beneath it and the summary at the end.
func (r Report) Text() string {
	var out strings.Builder
	for _, category := range Categories {
		checks := r.Category(category)
		if len(checks) == 0 {
			continue
		}
		out.WriteString(category)
		out.WriteString("\n")
		for _, checked := range checks {
			out.WriteString("  ")
			out.WriteString(checked.Line())
			out.WriteString("\n")
			writeEvidence(&out, checked)
			writeRemedy(&out, checked.Result.Remediation)
		}
		out.WriteString("\n")
	}
	out.WriteString(r.Summary())
	out.WriteString("\n")
	return out.String()
}

// Detail is one check on its own and in full: what it found, the question it
// was asked, everything it saw and what to do about it.
//
// It is what the watch screen's pane shows (spec 12 §3), and it is the one
// rendering that does not cap the evidence. [EvidenceShown] exists because a
// report that scrolled a hundred pull requests past a reader would have told
// them nothing; a pane a person opened on one check, and can scroll, is the
// case that cap was protecting them from.
func (c Checked) Detail() string {
	var out strings.Builder
	out.WriteString(c.Line())
	out.WriteString("\n")
	if c.Title != "" {
		out.WriteString("      ")
		out.WriteString(c.Title)
		out.WriteString("\n")
	}
	writeAllEvidence(&out, c)
	writeRemedy(&out, c.Result.Remediation)
	return out.String()
}

// writeEvidence lists what a check saw, up to [EvidenceShown] of it.
func writeEvidence(out *strings.Builder, checked Checked) {
	writeSomeEvidence(out, checked, EvidenceShown)
}

// writeAllEvidence lists everything a check saw.
func writeAllEvidence(out *strings.Builder, checked Checked) {
	writeSomeEvidence(out, checked, len(checked.Result.Evidence))
}

// writeSomeEvidence lists what a check saw, up to a limit, and says how much
// it left out.
func writeSomeEvidence(out *strings.Builder, checked Checked, limit int) {
	evidence := checked.Result.Evidence
	shown := min(len(evidence), limit)
	for _, item := range evidence[:shown] {
		out.WriteString("      - ")
		out.WriteString(item.Text)
		if item.URL != "" {
			out.WriteString("  ")
			out.WriteString(item.URL)
		}
		out.WriteString("\n")
	}
	if rest := len(evidence) - shown; rest > 0 {
		fmt.Fprintf(out, "      … and %d more\n", rest)
	}
}

// Lines is what to do about a check, one line each, in the order somebody
// would do them. A remedy with nothing to be done about it has none.
//
// The terminal indents them under the finding and a CI annotation carries
// them on the end of its message; what they say is settled here, so the two
// cannot come to word the same advice differently.
func (r Remediation) Lines() []string {
	if !r.Actionable() {
		return nil
	}
	var lines []string
	if r.Summary != "" {
		line := "fix: " + r.Summary
		if r.Owner != "" {
			line += " (" + r.Owner + ")"
		}
		lines = append(lines, line)
	}
	if r.Command != "" {
		lines = append(lines, "run: "+r.Command)
	}
	for i, step := range r.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, step))
	}
	return lines
}

// writeRemedy says what to do about a check, when there is anything to do.
func writeRemedy(out *strings.Builder, remedy Remediation) {
	for _, line := range remedy.Lines() {
		out.WriteString("      ")
		out.WriteString(line)
		out.WriteString("\n")
	}
}

// Category returns the report's checks in one category, in run order.
func (r Report) Category(category string) []Checked {
	var checks []Checked
	for _, checked := range r.Checks {
		if checked.Category == category {
			checks = append(checks, checked)
		}
	}
	return checks
}

// Summary is spec 07 §9's last line: how much is yours, how much is somebody
// else's, how much needs your word, and what the shell will see.
func (r Report) Summary() string {
	actions, attestations := r.Actions(), r.Attestations()
	return fmt.Sprintf("%d %s for you · %d waiting on others · %d %s due · exit %d",
		actions, plural(actions, "action"),
		r.Waiting(),
		attestations, plural(attestations, "attestation"),
		r.Exit())
}
