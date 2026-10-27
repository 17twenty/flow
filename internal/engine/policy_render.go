package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// How the gate reports (spec 09 §2). As everywhere else in the engine, the
// words are the engine's and the layout is somebody else's: the commit status,
// the pull request comment and the terminal must not be able to disagree about
// what the gate found.

// DefaultStatusContext is the commit status the gate publishes under when the
// configuration does not name one.
const DefaultStatusContext = "flow/policy"

// MaxStatusDescription is how long a commit status description may be. It is
// the shortest limit among the forges flow speaks to, and is counted in
// characters rather than bytes because that is what the forges count.
const MaxStatusDescription = 140

// DraftDescription is what the gate says about a pull request still being
// written. A draft is a request for help, not a request to merge.
const DraftDescription = "draft — not enforced"

// RulesDoc is the page that explains every rule the gate applies and every way
// to resolve each: what a "Why am I seeing this?" link points at.
const RulesDoc = "https://github.com/17twenty/flow/blob/main/docs/rules.md"

// RuleDocURL is where a rule is explained. The anchor is the one GitHub makes
// of a heading that is the rule's id: lower case, with the dots dropped.
func RuleDocURL(rule string) string {
	return RulesDoc + "#" + strings.ToLower(strings.ReplaceAll(rule, ".", ""))
}

// StatusContext is the commit status name the gate publishes under.
func StatusContext(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.Policy.StatusContext) == "" {
		return DefaultStatusContext
	}
	return cfg.Policy.StatusContext
}

// State is the commit status state the verdict publishes.
//
// A draft is pending rather than failing. Nothing is being asked for yet, and
// a red cross on work somebody is still thinking about teaches people to
// ignore red crosses.
func (r PolicyResult) State() string {
	switch {
	case r.Draft:
		return core.StatusPending
	case r.Pass():
		return core.StatusSuccess
	default:
		return core.StatusFailure
	}
}

// Description is the verdict in one line of at most [MaxStatusDescription]
// characters: what failed, and which rules, as many as will fit.
func (r PolicyResult) Description() string {
	switch failures, warnings := r.Failures(), r.Warnings(); {
	case r.Draft:
		return DraftDescription
	case len(failures) > 0:
		return describeFindings("✗", "error", "errors", failures)
	case len(warnings) > 0:
		return describeFindings("⚠", "warning", "warnings", warnings)
	default:
		return "✓ every rule passes"
	}
}

// Status is the commit status to publish. The target URL is the caller's: only
// it knows which CI run produced this.
func (r PolicyResult) Status(cfg *config.Config) core.CommitStatus {
	return core.CommitStatus{
		Context:     StatusContext(cfg),
		State:       r.State(),
		Description: r.Description(),
	}
}

// StatusUnchanged reports whether a status already published says exactly what
// flow is about to say. Publishing it again would add another line to the
// pull request's timeline without adding anything to it (spec 09 §2).
func StatusUnchanged(latest, want core.CommitStatus) bool {
	return latest.Context == want.Context &&
		latest.State == want.State &&
		latest.Description == want.Description
}

// describeFindings writes "✗ 2 errors — ticket.ready, pr.title.conventional", fitting
// as many rule ids as the limit allows and counting the rest.
//
// The count at the front is always the true one: a description that said "✗ 2
// errors" when there were nine would be a lie told to save eleven characters.
// It is the list that gives way.
func describeFindings(symbol, one, many string, findings []Finding) string {
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.Rule)
	}
	noun := many
	if len(ids) == 1 {
		noun = one
	}
	head := fmt.Sprintf("%s %d %s", symbol, len(ids), noun)
	for shown := len(ids); shown > 0; shown-- {
		line := head + " — " + strings.Join(ids[:shown], ", ")
		if rest := len(ids) - shown; rest > 0 {
			line += fmt.Sprintf(", +%d more", rest)
		}
		if utf8.RuneCountInString(line) <= MaxStatusDescription {
			return line
		}
	}
	return head
}

// PolicyMarker identifies the gate's comment, so that there is one of them per
// pull request however many times the gate runs (spec 09 §2, spec 11 §1).
func PolicyMarker() core.Marker { return core.NewMarker(core.MarkerPolicy) }

// CommentPlan is what the gate wants its comment to be.
type CommentPlan struct {
	// Want reports whether a comment should exist. When it is false any
	// comment flow left before is removed: a stale explanation of a problem
	// somebody has since fixed is worse than none.
	Want bool
	// Body is the comment to write, when one is wanted.
	Body string
}

// Comment decides what the gate's comment should be.
//
// `never` removes the comment rather than leaving the last one in place. A
// team that turns comments off is saying they do not want flow writing on
// their pull requests, and honouring that only for new comments would leave
// the ones it already wrote arguing with a status that has moved on.
func (r PolicyResult) Comment(cfg *config.Config) CommentPlan {
	switch commentWhen(cfg) {
	case config.CommentNever:
		return CommentPlan{}
	case config.CommentAlways:
		return CommentPlan{Want: true, Body: r.CommentBody()}
	default:
		if len(r.Problems()) == 0 {
			return CommentPlan{}
		}
		return CommentPlan{Want: true, Body: r.CommentBody()}
	}
}

// commentWhen is the configured comment policy, defaulting to on_failure.
func commentWhen(cfg *config.Config) string {
	if cfg == nil || cfg.Policy.Comment == "" {
		return config.CommentOnFailure
	}
	return cfg.Policy.Comment
}

// CommentBody renders the gate's comment: what did not hold, and exactly what
// to do about each of it. Rules that passed are not listed. Somebody reading
// this has a pull request to fix, and a list of nine things with two crosses
// in it makes them find the crosses.
func (r PolicyResult) CommentBody() string {
	var body strings.Builder
	body.WriteString("### flow policy\n\n")
	problems := r.Problems()
	if len(problems) == 0 {
		body.WriteString("Every rule passes.\n")
	} else {
		body.WriteString("| | rule | what is wrong | fix |\n|---|---|---|---|\n")
		for _, finding := range problems {
			fmt.Fprintf(&body, "| %s | [`%s`](%s) | %s | %s |\n",
				finding.Symbol(), finding.Rule, RuleDocURL(finding.Rule), tableCell(finding.Message), tableCell(finding.Fix))
		}
		for _, finding := range problems {
			writeFindingEvidence(&body, finding)
		}
		fmt.Fprintf(&body, "\nWhy am I seeing this? Each rule links to what it asks and every way to resolve it: <%s>\n", RulesDoc)
	}
	if r.Draft {
		body.WriteString("\nThis is a draft, so the status is pending and nothing is enforced yet.\n")
	}
	return core.MarkBody(strings.TrimRight(body.String(), "\n")+"\n", PolicyMarker(), true)
}

// writeFindingEvidence lists what a rule saw, beneath the table, for the rules
// that saw more than one thing. A rule whose message already says everything
// gets no list.
func writeFindingEvidence(body *strings.Builder, finding Finding) {
	if len(finding.Evidence) == 0 {
		return
	}
	fmt.Fprintf(body, "\n**%s**\n\n", finding.Rule)
	for _, item := range finding.Evidence {
		body.WriteString("- ")
		body.WriteString(item.Text)
		if item.URL != "" {
			fmt.Fprintf(body, " <%s>", item.URL)
		}
		body.WriteString("\n")
	}
}

// tableCell makes a string safe to put in a markdown table: a pipe would end the
// column and a newline would end the row.
func tableCell(text string) string {
	text = strings.ReplaceAll(text, "|", "\\|")
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}

// Text renders the verdict for a terminal: one line per rule, in rule order,
// with what to do beneath the ones that did not hold.
func (r PolicyResult) Text() string {
	var out strings.Builder
	for _, finding := range r.Findings {
		fmt.Fprintf(&out, "%s %s", finding.Symbol(), finding.Rule)
		if finding.Message != "" {
			out.WriteString("  ")
			out.WriteString(finding.Message)
		}
		out.WriteString("\n")
		writeEvidenceLines(&out, finding.Evidence)
		if finding.Failed() && finding.Fix != "" {
			out.WriteString("    → ")
			out.WriteString(finding.Fix)
			out.WriteString("\n")
		}
	}
	out.WriteString(r.Description())
	out.WriteString("\n")
	return out.String()
}

// writeEvidenceLines lists evidence under a rule, up to [EvidenceShown] of it.
func writeEvidenceLines(out *strings.Builder, evidence []Evidence) {
	shown := min(len(evidence), EvidenceShown)
	for _, item := range evidence[:shown] {
		out.WriteString("    - ")
		out.WriteString(item.Text)
		if item.URL != "" {
			out.WriteString("  ")
			out.WriteString(item.URL)
		}
		out.WriteString("\n")
	}
	if rest := len(evidence) - shown; rest > 0 {
		fmt.Fprintf(out, "    … and %d more\n", rest)
	}
}
