package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Definition of Ready (spec 04 §7).
//
// A Definition of Ready is the team's answer to "what does a ticket need
// before anybody starts on it". Flow's contribution is not to invent one — it
// is to make the one the team wrote down apply by itself, at the two moments
// it matters: when a ticket is pulled into Ready, and when a pull request
// claims to be for it.
//
// Every rule here is a pure function of a ticket and the configuration. No
// rule asks the network a question, because the same ticket must get the same
// answer in `flow check pr` on a developer's laptop, in CI, and in `flow
// doctor`'s live audit — and a rule that could disagree with itself between
// those three is worse than no rule at all.

// LabelUserFacing and LabelUI are the two labels the rules read. Their names
// are fixed by spec 04 §7 rather than configured: a rule whose trigger label
// could be renamed per repository is a rule nobody can reason about across
// repositories, and flow creates both labels so a team can start using them.
const (
	LabelUserFacing = "user-facing"
	LabelUI         = "ui"
)

// FlagLabelPrefix marks a ticket as carrying a named feature flag. Spec 04 §7
// writes it `flag:<name>`, and the name is the team's own.
const FlagLabelPrefix = "flag:"

// HeadingAcceptanceCriteria and HeadingDesign are the headings the rules look
// for. The issue form flow proposes guarantees the first (spec 17 §2).
const (
	HeadingAcceptanceCriteria = "acceptance criteria"
	HeadingDesign             = "design"
)

// ReadyRule is one rule's verdict.
type ReadyRule struct {
	// Rule is the rule id from spec 04 §7.
	Rule string
	// OK reports whether the ticket satisfies it.
	OK bool
	// Why is what is missing, in the words a person reads. It is written for
	// whoever has to fix the ticket, so it says what to add rather than which
	// rule failed.
	Why string
	// Skipped reports that the rule could not apply: the tracker records no
	// estimate, the trigger label is absent. A skipped rule is satisfied —
	// flow does not hold work up for a question nobody asked.
	Skipped bool
}

// Readiness is the whole Definition of Ready, judged against one ticket.
type Readiness struct {
	// Ticket is the key judged.
	Ticket string
	// Rules are the verdicts, in the order the configuration lists them.
	Rules []ReadyRule
}

// Ready reports whether every rule held.
func (r Readiness) Ready() bool {
	for _, rule := range r.Rules {
		if !rule.OK {
			return false
		}
	}
	return true
}

// Missing returns the rules that did not hold.
func (r Readiness) Missing() []ReadyRule {
	var missing []ReadyRule
	for _, rule := range r.Rules {
		if !rule.OK {
			missing = append(missing, rule)
		}
	}
	return missing
}

// Reasons is what is missing, one line each, for a comment or a check output.
func (r Readiness) Reasons() []string {
	missing := r.Missing()
	reasons := make([]string, 0, len(missing))
	for _, rule := range missing {
		reasons = append(reasons, rule.Why)
	}
	return reasons
}

// Summary is the verdict in one line.
func (r Readiness) Summary() string {
	if r.Ready() {
		return fmt.Sprintf("%s meets the Definition of Ready", r.Ticket)
	}
	return fmt.Sprintf("%s is not ready: %s", r.Ticket, strings.Join(r.Reasons(), "; "))
}

// readyRule is one rule's implementation: the ticket, the configuration, and
// a verdict.
type readyRule func(ticket core.Ticket, cfg *config.Config) ReadyRule

// readyRules are the rules of spec 04 §7, by id.
var readyRules = map[string]readyRule{
	config.DoRAcceptanceCriteria: acceptanceCriteria,
	config.DoRParentLink:         parentLink,
	config.DoRNotBlocked:         notBlocked,
	config.DoRFlagIfUserFacing:   flagIfUserFacing,
	config.DoRDesignIfUI:         designIfUI,
}

// ReadyRuleIDs are the rules a configuration may name, for validation and for
// the help text.
func ReadyRuleIDs() []string {
	return []string{
		config.DoRAcceptanceCriteria,
		config.DoRParentLink,
		config.DoRNotBlocked,
		config.DoRFlagIfUserFacing,
		config.DoRDesignIfUI,
	}
}

// Judge applies the configured Definition of Ready to one ticket.
//
// A configuration that names no rules has no Definition of Ready, and every
// ticket is ready. That is a real answer rather than a degenerate one: a team
// that has not agreed what "ready" means should not have flow inventing it.
func Judge(ticket core.Ticket, cfg *config.Config) Readiness {
	judged := Readiness{Ticket: ticket.Key}
	if cfg == nil {
		return judged
	}
	for _, id := range cfg.DefinitionOfReady.Rules {
		rule, known := readyRules[id]
		if !known {
			// Configuration validation rejects an unknown rule, so reaching
			// here means a build that knows fewer rules than the file does.
			// Refusing to judge is the honest answer: silently ignoring a
			// rule the team wrote down would let work through a gate they
			// believe is closed.
			judged.Rules = append(judged.Rules, ReadyRule{
				Rule: id,
				Why:  fmt.Sprintf("this build of flow does not know the rule %q", id),
			})
			continue
		}
		judged.Rules = append(judged.Rules, rule(ticket, cfg))
	}
	if cfg.DefinitionOfReady.MaxSize > 0 {
		judged.Rules = append(judged.Rules, maxSize(ticket, cfg))
	}
	return judged
}

// acceptanceCriteria demands a heading somebody filled in. The heading alone
// is not enough: the issue form flow proposes puts the heading there for you,
// so an empty one means the template was accepted and nothing was written.
func acceptanceCriteria(ticket core.Ticket, _ *config.Config) ReadyRule {
	rule := ReadyRule{Rule: config.DoRAcceptanceCriteria}
	switch found, filled := section(ticket.Body, HeadingAcceptanceCriteria); {
	case !found:
		rule.Why = "it has no \"Acceptance criteria\" heading — say how anybody will know this is done"
	case !filled:
		rule.Why = "its \"Acceptance criteria\" heading is empty — the template put it there, somebody has to fill it in"
	default:
		rule.OK = true
	}
	return rule
}

// parentLink demands the ticket hang off something bigger, so that a board of
// forty items is still a story about a handful of outcomes.
func parentLink(ticket core.Ticket, _ *config.Config) ReadyRule {
	rule := ReadyRule{Rule: config.DoRParentLink, OK: ticket.Parent != ""}
	if !rule.OK {
		rule.Why = "it has no parent — link it to the epic or bigger piece of work it belongs to"
	}
	return rule
}

// notBlocked refuses to let work start that is already stuck. Pulling a
// blocked ticket into progress is how a board starts lying.
func notBlocked(ticket core.Ticket, cfg *config.Config) ReadyRule {
	rule := ReadyRule{Rule: config.DoRNotBlocked, OK: true}
	label := blockedLabel(cfg)
	switch {
	case ticket.Blocked:
		rule.OK, rule.Why = false, "the tracker has it flagged as blocked — clear the blocker before starting"
	case label != "" && ticket.HasLabel(label):
		rule.OK, rule.Why = false, fmt.Sprintf("it is labelled %q — clear the blocker before starting", label)
	}
	return rule
}

// flagIfUserFacing demands a feature flag on work users will see. Trunk-based
// delivery means the half-finished thing is already in production; the flag
// is what makes that safe, and deciding on it afterwards is deciding too late.
func flagIfUserFacing(ticket core.Ticket, _ *config.Config) ReadyRule {
	rule := ReadyRule{Rule: config.DoRFlagIfUserFacing, OK: true}
	if !ticket.HasLabel(LabelUserFacing) {
		rule.Skipped = true
		return rule
	}
	for _, label := range ticket.Labels {
		if strings.HasPrefix(strings.ToLower(label), FlagLabelPrefix) &&
			strings.TrimSpace(label[len(FlagLabelPrefix):]) != "" {
			return rule
		}
	}
	rule.OK = false
	rule.Why = fmt.Sprintf("it is labelled %q but carries no %s<name> label — "+
		"decide the flag before the work starts, not after it ships", LabelUserFacing, FlagLabelPrefix)
	return rule
}

// designIfUI demands a design link on work that changes what people look at,
// so that the first time anybody sees the design is not in code review.
func designIfUI(ticket core.Ticket, _ *config.Config) ReadyRule {
	rule := ReadyRule{Rule: config.DoRDesignIfUI, OK: true}
	if !ticket.HasLabel(LabelUI) {
		rule.Skipped = true
		return rule
	}
	found, _ := section(ticket.Body, HeadingDesign)
	if found && linked(sectionBody(ticket.Body, HeadingDesign)) {
		return rule
	}
	rule.OK = false
	rule.Why = fmt.Sprintf("it is labelled %q but has no link under a \"Design\" heading — "+
		"point at the design before the work starts", LabelUI)
	return rule
}

// maxSize bounds an estimate, for the trackers that record one. Where the
// tracker has no size field the rule cannot apply, and a rule that cannot
// apply does not hold work up: flow will not refuse a ticket for failing to
// carry a number its tracker has nowhere to put.
func maxSize(ticket core.Ticket, cfg *config.Config) ReadyRule {
	limit := time.Duration(cfg.DefinitionOfReady.MaxSize)
	rule := ReadyRule{Rule: "max_size", OK: true}
	switch {
	case ticket.Size <= 0:
		rule.Skipped = true
	case ticket.Size > limit:
		rule.OK = false
		rule.Why = fmt.Sprintf("it is estimated at %s, over the limit of %s — split it",
			config.Duration(ticket.Size).String(), config.Duration(limit).String())
	}
	return rule
}

// blockedLabel is the label the workflow uses for stuck work.
func blockedLabel(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Workflow.Blocked.Label)
}

// section reports whether a markdown body has a heading, and whether anything
// was written under it. Headings are matched at any level and without regard
// to case, because "## Acceptance Criteria" and "### acceptance criteria" are
// the same heading to everybody except a parser.
func section(body, heading string) (found, filled bool) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !isHeading(line, heading) {
			continue
		}
		found = true
		for _, next := range lines[i+1:] {
			if _, _, isNext := headingOf(next); isNext {
				// The next heading ends the section, whatever its level: a
				// subheading with nothing under it is still nothing written.
				break
			}
			if strings.TrimSpace(next) != "" {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

// sectionBody is everything written under a heading, up to the next one.
func sectionBody(body, heading string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !isHeading(line, heading) {
			continue
		}
		var out []string
		for _, next := range lines[i+1:] {
			if _, _, ok := headingOf(next); ok {
				break
			}
			out = append(out, next)
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// isHeading reports whether a line is the named ATX heading. Trailing hashes
// and punctuation are ignored: "## Acceptance criteria:" is the same heading.
func isHeading(line, want string) bool {
	_, text, ok := headingOf(line)
	if !ok {
		return false
	}
	return strings.EqualFold(text, want)
}

// headingOf splits an ATX heading into its level and its text.
func headingOf(line string) (level int, text string, ok bool) {
	trimmed := strings.TrimSpace(line)
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := trimmed[level:]
	if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		// "#hashtag" is not a heading.
		return 0, "", false
	}
	text = strings.TrimSpace(rest)
	text = strings.TrimRight(text, "#")
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, ":")
	return level, strings.TrimSpace(text), true
}

// linked reports whether a piece of markdown contains a link. All three
// spellings count — a bare URL somebody pasted, an inline `[text](url)`, and
// a reference-style `[text][1]`. Refusing any of them would be flow enforcing
// markdown style rather than a Definition of Ready, and a reference whose
// definition is missing is a markdown mistake rather than a missing design.
func linked(text string) bool {
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		return true
	}
	for _, pair := range [][2]string{{"](", ")"}, {"][", "]"}} {
		open := strings.Index(text, pair[0])
		if open < 0 {
			continue
		}
		rest := text[open+len(pair[0]):]
		if close := strings.Index(rest, pair[1]); close > 0 {
			return true
		}
	}
	return false
}
