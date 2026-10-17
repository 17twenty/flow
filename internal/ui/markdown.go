package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// Markdown rendering (spec 07 §9–10, spec 12). This is the one rendering that
// is not read in a terminal: it goes in an issue comment, a pull request or a
// --report file, where there is no colour, where a table is read more easily
// than an indented list, and where a link is a link.
//
// It is still the engine's words. What changes is the shape: findings become
// rows, so that a reader meets the whole run at once, and the detail each one
// carries follows beneath the table rather than interrupting it.

// planMarkdown renders a plan as markdown.
func (r Renderer) planMarkdown(plan engine.Plan) string {
	var out strings.Builder
	out.WriteString("## flow plan\n\n")
	if providers := r.providerLine(plan); providers != "" {
		out.WriteString(providers)
		out.WriteString("\n\n")
	}
	if len(plan.Items) > 0 {
		out.WriteString("| | item | plan | why |\n|---|---|---|---|\n")
		for _, item := range plan.Items {
			fmt.Fprintf(&out, "| %s | `%s` | %s | %s |\n",
				r.glyphText(item.Outcome.Symbol()), item.ID, r.cell(planCell(item)), r.cell(item.Why))
		}
		for _, item := range plan.Items {
			r.writeItemDetail(&out, item)
		}
		out.WriteString("\n")
	}
	out.WriteString(r.words(plan.Summary()))
	out.WriteString("\n")
	return out.String()
}

// providerLine names the adapter behind each target, in the order the engine
// reports them.
func (r Renderer) providerLine(plan engine.Plan) string {
	var named []string
	for _, target := range []string{engine.TargetForge, engine.TargetTracker, engine.TargetDeploys} {
		if kind := plan.Providers[target]; kind != "" {
			named = append(named, fmt.Sprintf("%s `%s`", target, kind))
		}
	}
	return strings.Join(named, r.words(" · "))
}

// planCell is what the plan column says: the outcome, and whatever the
// outcome puts after the arrow.
func planCell(item engine.PlanItem) string {
	if detail := item.Detail(); detail != "" {
		return item.Outcome.Label() + " → " + detail
	}
	return item.Outcome.Label()
}

// writeItemDetail lists an item's changes and hazards beneath the table. An
// item with neither gets no block: a heading with nothing under it is noise
// in a comment somebody has to scroll past.
func (r Renderer) writeItemDetail(out *strings.Builder, item engine.PlanItem) {
	if len(item.Changes) == 0 && len(item.Hazards) == 0 {
		return
	}
	fmt.Fprintf(out, "\n**`%s`**\n\n", item.ID)
	for _, change := range item.Changes {
		fmt.Fprintf(out, "- %s\n", r.words(changeLine(change)))
	}
	for _, hazard := range item.Hazards {
		fmt.Fprintf(out, "- hazard: %s\n", r.words(hazard))
	}
}

// changeLine is one mutation in a line: what would be done, to what, with
// what. The fields are the ones [core.Change.Fields] names, in name order, as
// everywhere else flow prints them, so that two renderings of the same change
// are the same text.
func changeLine(change core.Change) string {
	var line strings.Builder
	fmt.Fprintf(&line, "`%s` %s", change.Op, strconv.Quote(change.Item.Name))
	for _, field := range change.Fields() {
		fmt.Fprintf(&line, " %s=%s", field, strconv.Quote(change.Item.Fields[field]))
	}
	return line.String()
}

// reportMarkdown renders a doctor report as markdown: the report that goes in
// the pinned health issue (spec 07 §10).
func (r Renderer) reportMarkdown(report engine.Report) string {
	var out strings.Builder
	out.WriteString("## flow doctor\n\n")
	if report.Window != "" {
		fmt.Fprintf(&out, "Live health over the last `%s`.\n\n", report.Window)
	}
	for _, category := range engine.Categories {
		checks := report.Category(category)
		if len(checks) == 0 {
			continue
		}
		fmt.Fprintf(&out, "### %s\n\n", category)
		out.WriteString("| | check | found |\n|---|---|---|\n")
		for _, checked := range checks {
			fmt.Fprintf(&out, "| %s | `%s` | %s |\n",
				r.glyphText(checked.Result.State.Symbol()), checked.ID, r.cell(foundCell(checked)))
		}
		for _, checked := range checks {
			r.writeCheckDetail(&out, checked)
		}
		out.WriteString("\n")
	}
	out.WriteString(r.words(report.Summary()))
	out.WriteString("\n")
	return out.String()
}

// foundCell is what a check found: how much of what it looked at held, and
// its own one-line answer.
func foundCell(checked engine.Checked) string {
	var cell strings.Builder
	if checked.Result.Counts != nil {
		cell.WriteString(checked.Result.Counts.String())
		cell.WriteString(" ")
	}
	cell.WriteString(checked.Result.Message)
	return cell.String()
}

// writeCheckDetail lists a check's evidence and remedy beneath the table,
// with the same five-item limit the terminal uses (spec 07 §9): a comment
// listing a hundred pull requests has told its reader nothing either.
func (r Renderer) writeCheckDetail(out *strings.Builder, checked engine.Checked) {
	remedy := checked.Result.Remediation
	if len(checked.Result.Evidence) == 0 && !remedy.Actionable() {
		return
	}
	fmt.Fprintf(out, "\n**`%s`** %s\n\n", checked.ID, r.words("— "+checked.Title))
	evidence := checked.Result.Evidence
	shown := min(len(evidence), engine.EvidenceShown)
	for _, item := range evidence[:shown] {
		out.WriteString("- ")
		out.WriteString(r.words(item.Text))
		if item.URL != "" {
			fmt.Fprintf(out, " <%s>", item.URL)
		}
		out.WriteString("\n")
	}
	if rest := len(evidence) - shown; rest > 0 {
		fmt.Fprintf(out, "- %s\n", r.words(fmt.Sprintf("… and %d more", rest)))
	}
	if shown > 0 && remedy.Actionable() {
		out.WriteString("\n")
	}
	r.writeRemedy(out, remedy)
}

// writeRemedy says what to do about a check, when there is anything to do.
// It is a list rather than a paragraph because markdown folds consecutive
// lines into one: "fix: …" and "run: …" written as prose would arrive as a
// single run-on sentence in the issue comment nobody can then copy from.
func (r Renderer) writeRemedy(out *strings.Builder, remedy engine.Remediation) {
	if !remedy.Actionable() {
		return
	}
	indent := ""
	if remedy.Summary != "" {
		fmt.Fprintf(out, "- fix: %s", r.words(remedy.Summary))
		if remedy.Owner != "" {
			fmt.Fprintf(out, " (%s)", remedy.Owner)
		}
		out.WriteString("\n")
		indent = "  "
	}
	if remedy.Command != "" {
		fmt.Fprintf(out, "- run: `%s`\n", remedy.Command)
	}
	for i, step := range remedy.Steps {
		fmt.Fprintf(out, "%s%d. %s\n", indent, i+1, r.words(step))
	}
}

// cell makes a string safe to put in a markdown table: a pipe would end the
// column and a newline would end the row.
func (r Renderer) cell(text string) string {
	text = r.words(text)
	text = strings.ReplaceAll(text, "|", "\\|")
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}
