package engine

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The policy gate (spec 09 §2).
//
// The gate is the one place where the workflow a team wrote down is held
// against one particular pull request. Everything here is a pure function of
// the snapshot it is given: the same pull request must get the same verdict on
// a laptop, in CI and in doctor's live audit, and a gate that could disagree
// with itself between those three would be worth less than no gate at all.
//
// Gathering the snapshot — asking the forge for the pull request, the tracker
// for its tickets and the board for its counts — is the command's work, not
// this file's.

// Policy rule ids (spec 09 §2). They are a public contract: they appear in
// `policy.rules`, in the commit status description and in the pull request
// comment, so they must stay stable.
const (
	// RulePRTicketLinked requires the pull request to say what it is for.
	RulePRTicketLinked = "pr.ticket.linked"
	// RulePRTicketInProject requires the linked issue to be on the board.
	RulePRTicketInProject = "pr.ticket.in_project"
	// RuleTicketReady requires the linked tickets to meet the Definition of
	// Ready.
	RuleTicketReady = "ticket.ready"
	// RulePRTitleConventional requires the title to be the commit header it
	// will become.
	RulePRTitleConventional = "pr.title.conventional"
	// RulePRBreakingExplained requires a breaking change to be described.
	RulePRBreakingExplained = "pr.breaking.explained"
	// RuleAgentHumanReview requires a human to stay accountable for agent
	// work.
	RuleAgentHumanReview = "agent.human_review"
	// RuleWIPReview holds the review column to its limit.
	RuleWIPReview = "wip.review"
	// RuleWIPInProgress holds one person to their own limit.
	RuleWIPInProgress = "wip.in_progress"
	// RuleTicketNotBlocked notices work that is already stuck.
	RuleTicketNotBlocked = "ticket.not_blocked"
	// RulePRStacked notices a pull request standing on another one that has
	// not landed (issue #235).
	RulePRStacked = "pr.stacked"
)

// PolicyRuleIDs is every rule, in the order spec 09 §2 lists them, which is
// also the order findings are reported in. Nothing downstream sorts them
// again, so this slice is what makes the gate's output deterministic.
func PolicyRuleIDs() []string {
	return []string{
		RulePRTicketLinked,
		RulePRTicketInProject,
		RuleTicketReady,
		RulePRTitleConventional,
		RulePRBreakingExplained,
		RuleAgentHumanReview,
		RuleWIPReview,
		RuleWIPInProgress,
		RuleTicketNotBlocked,
		RulePRStacked,
	}
}

// policyBaseline is the standard strictness column of spec 09 §2's table.
var policyBaseline = map[string]string{
	RulePRTicketLinked:      config.SeverityError,
	RulePRTicketInProject:   config.SeverityError,
	RuleTicketReady:         config.SeverityError,
	RulePRTitleConventional: config.SeverityError,
	RulePRBreakingExplained: config.SeverityError,
	RuleAgentHumanReview:    config.SeverityError,
	RuleWIPReview:           config.SeverityWarn,
	RuleWIPInProgress:       config.SeverityWarn,
	RuleTicketNotBlocked:    config.SeverityWarn,
	RulePRStacked:           config.SeverityWarn,
}

// policyAlwaysError are the two rules relaxed strictness does not relax. A
// pull request that says what it is for, and a human who is accountable for
// what a machine wrote, are the parts of the workflow that stop being true the
// moment they are advisory.
var policyAlwaysError = []string{RulePRTicketLinked, RuleAgentHumanReview}

// PolicySeverity is the severity in force for one rule: the strictness
// baseline, then whatever `policy.rules` says about it.
//
// An unknown strictness is read as standard rather than refused. Configuration
// validation rejects one, so reaching here with it means a binary older than
// the file, and the baseline is the honest answer to "what would flow have
// done before this word existed".
func PolicySeverity(cfg *config.Config, rule string) string {
	severity, known := policyBaseline[rule]
	if !known {
		severity = config.SeverityError
	}
	if cfg == nil {
		return severity
	}
	switch cfg.Policy.Strictness {
	case config.StrictnessRelaxed:
		if !slices.Contains(policyAlwaysError, rule) {
			severity = config.SeverityWarn
		}
	case config.StrictnessStrict:
		severity = config.SeverityError
	}
	if override, set := cfg.Policy.Rules[rule]; set && override != "" {
		severity = override
	}
	return severity
}

// LinkedTicket is one ticket a pull request is for, with the two things the
// gate knows about it that the ticket itself does not carry.
type LinkedTicket struct {
	// Ticket is the ticket as the tracker describes it.
	Ticket core.Ticket
	// Column is the configured column id the ticket sits in, resolved through
	// the lock by whoever built the snapshot, and empty when the ticket is in
	// a column the configuration does not name. The engine never looks a
	// tracker column up itself: the mapping lives in the lock (spec 04 §2).
	Column string
	// InProject reports that the tracker holds this ticket on the configured
	// board. It is only meaningful for a tracker whose tickets and board are
	// separate things, which is github-projects (spec 04 §3).
	InProject bool
}

// WIPItem is one piece of work in flight, as the board reports it.
type WIPItem struct {
	// Key is the ticket's key.
	Key string
	// Assignees are the logins it is assigned to.
	Assignees []string
	// Labels are its labels, which is how expedited work is recognised.
	Labels []string
	// Since is when it entered the column, where the tracker records it. The
	// zero time means the tracker would not say.
	Since time.Time
	// URL is where to go and look at it.
	URL string
}

// WIPCounts is what is in flight, by column. Spec 04 §8 has the counts taken
// from the board and cross-checked against the forge, with the larger of the
// two used; that reconciliation happens before the snapshot reaches the gate,
// because it needs the network and the gate must not.
type WIPCounts struct {
	// Review is the work in the column the review transition targets.
	Review []WIPItem
	// InProgress is the work in the column the opened transition targets.
	InProgress []WIPItem
}

// PolicySnapshot is everything the gate reads. It is taken once, at the start
// of a run, so that no two rules can be judging different worlds.
type PolicySnapshot struct {
	// PR is the pull request under judgement.
	PR core.PullRequest
	// Tickets are the tickets it is for, in the order the tracker linked
	// them.
	Tickets []LinkedTicket
	// Unlinkable are keys the pull request named that the tracker could not
	// produce. Spec 04 §3 has unknown keys reported rather than trusted, so
	// they are not tickets — they are evidence.
	Unlinkable []string
	// WIP is what else is in flight.
	WIP WIPCounts
	// Config is the workflow being enforced.
	Config *config.Config
	// Now is the moment the snapshot was taken.
	Now time.Time
	// StackedOn are the open pull requests this one stands on.
	StackedOn []StackedPR
}

// StackedPR is an open pull request another one stands on, and how.
type StackedPR struct {
	// Number is the pull request, zero when the base branch has none.
	Number int
	// Title is its title, for the evidence.
	Title string
	// Why is how the one under judgement stands on it.
	Why string
}

// Finding is one rule's verdict.
type Finding struct {
	// Rule is the rule id.
	Rule string `json:"rule"`
	// Severity is what was in force for it: warn or error.
	Severity string `json:"severity"`
	// OK reports whether the pull request satisfies it.
	OK bool `json:"ok"`
	// Skipped reports a rule that did not apply: no limit is set, the tracker
	// has no such concept, the trigger is absent. A skipped rule is
	// satisfied — the gate does not hold a pull request up for a question
	// nobody asked.
	Skipped bool `json:"skipped,omitempty"`
	// Message is what is wrong, in one line, written for whoever has to fix
	// it.
	Message string `json:"message,omitempty"`
	// Fix is the exact thing to do about it.
	Fix string `json:"fix,omitempty"`
	// Evidence is what the rule saw.
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Failed reports a rule that did not hold and was not skipped.
func (f Finding) Failed() bool { return !f.OK && !f.Skipped }

// Blocking reports a failure that fails the commit status.
func (f Finding) Blocking() bool { return f.Failed() && f.Severity == config.SeverityError }

// Symbol is the glyph that stands for the finding, in the gate's comment and
// in the terminal.
func (f Finding) Symbol() string {
	switch {
	case f.Blocking():
		return "✗"
	case f.Failed():
		return "⚠"
	case f.Skipped:
		return "·"
	default:
		return "✓"
	}
}

// PolicyResult is the whole verdict on one pull request.
type PolicyResult struct {
	// PR is the pull request number judged.
	PR int `json:"pr"`
	// SHA is the head commit the verdict is about.
	SHA string `json:"sha"`
	// Draft reports a pull request not yet offered for review.
	Draft bool `json:"draft"`
	// Findings are the verdicts, in [PolicyRuleIDs] order.
	Findings []Finding `json:"findings"`
}

// Failures are the blocking findings.
func (r PolicyResult) Failures() []Finding { return r.filter(Finding.Blocking) }

// Warnings are the findings that did not hold but do not block.
func (r PolicyResult) Warnings() []Finding {
	return r.filter(func(f Finding) bool { return f.Failed() && !f.Blocking() })
}

// Problems are everything that did not hold, errors before warnings, which is
// the order the comment lists them in.
func (r PolicyResult) Problems() []Finding {
	return append(r.Failures(), r.Warnings()...)
}

// filter returns the findings satisfying keep, in rule order.
func (r PolicyResult) filter(keep func(Finding) bool) []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if keep(finding) {
			out = append(out, finding)
		}
	}
	return out
}

// Pass reports whether nothing blocks the pull request.
func (r PolicyResult) Pass() bool { return len(r.Failures()) == 0 }

// CheckPolicy applies the configured policy to one snapshot.
//
// A rule turned off in `policy.rules` is not evaluated and does not appear in
// the result at all. "Off" is the team saying they do not want the question
// asked; reporting it as a passing or skipped rule would be flow answering it
// anyway.
func CheckPolicy(snapshot PolicySnapshot) PolicyResult {
	result := PolicyResult{
		PR:    snapshot.PR.Number,
		SHA:   snapshot.PR.HeadSHA,
		Draft: snapshot.PR.Draft,
	}
	for _, rule := range PolicyRuleIDs() {
		severity := PolicySeverity(snapshot.Config, rule)
		if severity == config.SeverityOff {
			continue
		}
		finding := policyRules[rule](snapshot)
		finding.Rule = rule
		finding.Severity = severity
		result.Findings = append(result.Findings, finding)
	}
	return result
}

// policyRule is one rule's implementation. It fills in everything but the id
// and the severity, which [CheckPolicy] owns.
type policyRule func(snapshot PolicySnapshot) Finding

// policyRules are spec 09 §2's rules, by id.
var policyRules = map[string]policyRule{
	RulePRTicketLinked:      prTicketLinked,
	RulePRTicketInProject:   prTicketInProject,
	RuleTicketReady:         ticketReady,
	RulePRTitleConventional: prTitleConventional,
	RulePRBreakingExplained: prBreakingExplained,
	RuleAgentHumanReview:    agentHumanReview,
	RuleWIPReview:           wipReview,
	RuleWIPInProgress:       wipInProgress,
	RuleTicketNotBlocked:    ticketNotBlocked,
	RulePRStacked:           prStacked,
}

// prStacked notices a pull request standing on work that has not landed
// (issue #235). Its checks passed against a parent that is still changing,
// and when the parent lands as a squash the trunk holds that work as a
// commit the child does not have: the child then carries the parent's
// original commits, and a merge of the trunk into it can drop a line without
// a conflict to say so. The fix is to land the parent first and look again.
func prStacked(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	if len(s.StackedOn) == 0 {
		finding.Message = "this stands on nothing but the trunk"
		return finding
	}
	finding.OK = false
	var names []string
	for _, parent := range s.StackedOn {
		text := parent.Why
		if parent.Number > 0 {
			names = append(names, fmt.Sprintf("#%d", parent.Number))
			text = fmt.Sprintf("#%d %s — %s", parent.Number, parent.Title, parent.Why)
		}
		finding.Evidence = append(finding.Evidence, Evidence{Text: text})
	}
	finding.Message = "this stands on work that has not landed"
	if len(names) > 0 {
		finding.Message = "this stands on " + strings.Join(names, ", ") + ", which has not landed"
	}
	finding.Fix = "land what this stands on first, then bring this up to date with the trunk and check it again: a squash merge leaves this branch carrying commits the trunk no longer has"
	return finding
}

// prTicketLinked insists a pull request says what it is for. The exemption is
// by title type, so that the plumbing a repository needs — a CI fix, a
// dependency bump — does not have to be invented a ticket first.
func prTicketLinked(s PolicySnapshot) Finding {
	finding := Finding{}
	if len(s.Tickets) > 0 {
		finding.OK = true
		return finding
	}
	if kind, exempt := unlinkedType(s.Config, s.PR.Title); exempt {
		finding.OK, finding.Skipped = true, true
		finding.Message = fmt.Sprintf("a %q pull request may merge without a ticket", kind)
		return finding
	}
	finding.Message = "it is not linked to a ticket, and its type is not one that may merge without one"
	finding.Fix = linkFix(s.Config)
	for _, key := range s.Unlinkable {
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: fmt.Sprintf("%s is named but the tracker has no such ticket", key),
		})
	}
	return finding
}

// prTicketInProject insists the linked issue is on the board. On
// github-projects an issue and a board item are different things, and an issue
// that is not an item is invisible to everything else flow does: it has no
// column, so no transition can move it and no WIP count includes it.
func prTicketInProject(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	if trackerType(s.Config) != config.TrackerGitHubProjects {
		finding.Skipped = true
		finding.Message = "this tracker keeps its tickets on the board already"
		return finding
	}
	if len(s.Tickets) == 0 {
		finding.Skipped = true
		finding.Message = "there is no linked ticket to look for"
		return finding
	}
	archived := 0
	for _, ticket := range s.Tickets {
		if ticket.InProject {
			continue
		}
		finding.OK = false
		// Archived is not absent. The item is still the project's, in
		// the column it was archived in, and adding it again is not the
		// thing to do: the board has to show it before any transition
		// can move it.
		text := fmt.Sprintf("%s is not an item of the project", ticket.Ticket.Key)
		if ticket.Ticket.Archived {
			archived++
			text = fmt.Sprintf("%s is archived, so the board does not show it and no transition can move it", ticket.Ticket.Key)
		}
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: text,
			URL:  ticket.Ticket.URL,
		})
	}
	if !finding.OK {
		finding.Message = countOf(len(finding.Evidence), "a linked issue is not on the board", "%d linked issues are not on the board")
		finding.Fix = prInProjectFix(archived, len(finding.Evidence))
	}
	return finding
}

// prInProjectFix is what to do about the issues the board does not show, which
// depends on why it does not show them: an issue nobody added has to be added,
// and one somebody archived has to be restored.
func prInProjectFix(archived, total int) string {
	switch {
	case archived == 0:
		return "add the issue to the project"
	case archived == total:
		return "restore the issue to the board, or unlink it if the work is abandoned"
	default:
		return "add the missing issues to the project and restore the archived ones to the board"
	}
}

// ticketReady holds the linked tickets to the Definition of Ready, and only
// until the work has started. The Definition of Ready is the gate into
// in-progress; a ticket that is already past that gate has been through it,
// and re-litigating every edit somebody makes while they work would turn a
// one-off agreement into a running argument.
func ticketReady(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	if len(s.Tickets) == 0 {
		finding.Skipped = true
		finding.Message = "there is no linked ticket to judge"
		return finding
	}
	judged := 0
	for _, ticket := range s.Tickets {
		if started(s.Config, ticket.Column) {
			continue
		}
		judged++
		readiness := Judge(ticket.Ticket, s.Config)
		if readiness.Ready() {
			continue
		}
		finding.OK = false
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: readiness.Summary(),
			URL:  ticket.Ticket.URL,
		})
	}
	switch {
	case judged == 0:
		finding.Skipped = true
		finding.Message = "the work has already started; the Definition of Ready was its entry gate"
	case !finding.OK:
		finding.Message = countOf(len(finding.Evidence),
			"a linked ticket does not meet the Definition of Ready",
			"%d linked tickets do not meet the Definition of Ready")
		finding.Fix = "fill in what the ticket is missing, or move it back until it is ready"
	}
	return finding
}

// titleRules are the commit rules the gate's title check reports. The rest of
// the linter's rules are about a commit message's body, which a pull request
// title does not have, or about the breaking-change marker, which is its own
// policy rule.
var titleRules = []string{
	commits.RuleHeaderFormat,
	commits.RuleTypeAllowed,
	commits.RuleScopeRequired,
	commits.RuleScopeAllowed,
	commits.RuleHeaderLength,
}

// prTitleConventional lints the commit header the pull request is about to
// become. Under a squash merge that header is the only thing that ends up in
// the trunk's history, so it is the only thing worth linting here — and it is
// not always the title, because GitHub and Gitea append the pull request's
// number to it (see [core.PullRequest.MergeHeader]).
func prTitleConventional(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	violations := lintTitle(s, titleRules)
	if len(violations) == 0 {
		return finding
	}
	finding.OK = false
	if len(violations) == 1 {
		finding.Message = violations[0].Message
		finding.Fix = violations[0].Fix
	} else {
		finding.Message = fmt.Sprintf("the title breaks %d rules of the commit convention", len(violations))
		finding.Fix = violations[0].Fix
		for _, violation := range violations {
			finding.Evidence = append(finding.Evidence, Evidence{
				Text: fmt.Sprintf("%s: %s — %s", violation.Rule, violation.Message, violation.Fix),
			})
		}
	}
	if note, differs := landedHeaderNote(s.PR); differs {
		finding.Evidence = append(finding.Evidence, Evidence{Text: note})
	}
	return finding
}

// landedHeaderNote explains a verdict passed on something other than the title
// the author typed, and reports whether there is anything to explain.
//
// Without it a person reads "header is 73 characters" against a title they can
// count to 66 and concludes the gate is broken. The number is the forge's
// doing and the author cannot delete it, so the only honest thing to show is
// the whole header and where the difference came from.
func landedHeaderNote(pr core.PullRequest) (string, bool) {
	header := pr.MergeHeader()
	if header == pr.Title {
		return "", false
	}
	return fmt.Sprintf("the header that lands is %q: the forge appends the number when it squashes, which is %d characters the title does not show",
		header, utf8.RuneCountInString(header)-utf8.RuneCountInString(pr.Title)), true
}

// prBreakingExplained insists that a "!" is followed by an explanation. A
// title that does not parse carries no "!" anybody can see, so the rule has
// nothing to say about it and pr.title.conventional already has.
func prBreakingExplained(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	violations := lintTitle(s, []string{commits.RuleBreakingExplained})
	if len(violations) == 0 {
		return finding
	}
	finding.OK = false
	finding.Message = violations[0].Message
	finding.Fix = violations[0].Fix
	return finding
}

// agentHumanReview keeps a person accountable for what a machine wrote. The
// approval must come from somebody who is not themselves an agent, or the rule
// would be satisfied by one robot nodding at another.
func agentHumanReview(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	if s.Config == nil || !s.Config.Agents.RequireHumanReview {
		finding.Skipped = true
		finding.Message = "the team does not require a human review of agent work"
		return finding
	}
	if !agentAuthored(s.Config, s.PR) {
		finding.Skipped = true
		finding.Message = "a person opened this"
		return finding
	}
	review := unattended(s.Config, s.PR)
	if review.Exempt {
		finding.Skipped = true
		finding.Message = fmt.Sprintf("a %q pull request may merge without a person approving it", review.Type)
		return finding
	}
	if login, approved := humanApprover(s.Config, s.PR); approved {
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: fmt.Sprintf("%s approved it", login),
		})
		return finding
	}
	finding.OK = false
	finding.Message = "an agent opened this and no person has approved it"
	finding.Fix = "ask somebody to review it and approve"
	if review.Listed {
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: fmt.Sprintf("a %q pull request usually merges unattended, but %s", review.Type, review.Because),
		})
	}
	for _, login := range s.PR.Approvals() {
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: fmt.Sprintf("%s approved it, and is an agent too", login),
		})
	}
	return finding
}

// wipReview holds the review column to its limit, without blaming this pull
// request for somebody else's queue. An approved pull request is never held
// up: it is the thing that would reduce the count.
func wipReview(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	column, found := columnFor(s.Config, config.EventPRReadyForReview)
	if !found || column.WIP == nil || column.WIP.Limit <= 0 {
		finding.Skipped = true
		finding.Message = "no limit is set on the review column"
		return finding
	}
	if expedited(s) {
		finding.Skipped = true
		finding.Message = "this is expedited, and expedited work is the agreed exception"
		return finding
	}
	limit := column.WIP.Limit
	items := countable(s.Config, s.WIP.Review)
	switch {
	case len(items) <= limit:
		return finding
	case len(s.PR.Approvals()) > 0:
		finding.Message = "the review column is over its limit, but this is already approved"
		return finding
	case joinedWithin(items, limit, ticketKeys(s)):
		finding.Message = "the review column is over its limit, but this entered review before it was"
		return finding
	}
	finding.OK = false
	finding.Message = fmt.Sprintf("%s holds %d, over its limit of %d", column.Name, len(items), limit)
	finding.Fix = fmt.Sprintf("get something out of %s before putting more in", column.Name)
	finding.Evidence = wipEvidence(items)
	return finding
}

// wipInProgress holds one person to their own limit. It counts what the board
// says they are assigned, which is the number the team agreed to cap — not
// what they happen to have open on the forge.
func wipInProgress(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	column, found := columnFor(s.Config, config.EventPROpenedDraft)
	if !found || column.WIP == nil || column.WIP.PerPerson <= 0 {
		finding.Skipped = true
		finding.Message = "no per-person limit is set on the in-progress column"
		return finding
	}
	if expedited(s) {
		finding.Skipped = true
		finding.Message = "this is expedited, and expedited work is the agreed exception"
		return finding
	}
	author := s.PR.Author
	if author == "" {
		finding.Skipped = true
		finding.Message = "the forge did not say who opened this"
		return finding
	}
	var theirs []WIPItem
	for _, item := range countable(s.Config, s.WIP.InProgress) {
		if slices.ContainsFunc(item.Assignees, func(login string) bool {
			return strings.EqualFold(login, author)
		}) {
			theirs = append(theirs, item)
		}
	}
	if len(theirs) <= column.WIP.PerPerson {
		return finding
	}
	finding.OK = false
	finding.Message = fmt.Sprintf("%s has %d items in %s, over the limit of %d per person",
		author, len(theirs), column.Name, column.WIP.PerPerson)
	finding.Fix = "finish or hand over something already started before taking more on"
	finding.Evidence = wipEvidence(theirs)
	return finding
}

// ticketNotBlocked notices work the team has already said cannot move. It is a
// warning rather than an error: the pull request may well be what unblocks it,
// and a gate that refused would be arguing with somebody who already knows.
func ticketNotBlocked(s PolicySnapshot) Finding {
	finding := Finding{OK: true}
	if len(s.Tickets) == 0 {
		finding.Skipped = true
		finding.Message = "there is no linked ticket to be blocked"
		return finding
	}
	label := blockedLabel(s.Config)
	for _, linked := range s.Tickets {
		ticket := linked.Ticket
		if !ticket.Blocked && (label == "" || !ticket.HasLabel(label)) {
			continue
		}
		finding.OK = false
		finding.Evidence = append(finding.Evidence, Evidence{
			Text: fmt.Sprintf("%s is blocked", ticket.Key),
			URL:  ticket.URL,
		})
	}
	if !finding.OK {
		finding.Message = countOf(len(finding.Evidence),
			"a linked ticket is marked blocked", "%d linked tickets are marked blocked")
		finding.Fix = "clear the blocker, or say on the ticket that this pull request is what clears it"
	}
	return finding
}

// CommitSettings maps the commits section of a configuration onto what the
// linter reads. internal/commits never reads configuration itself (spec 01
// §2), so somebody has to, and the gate is the first caller that does.
func CommitSettings(cfg *config.Config) commits.Settings {
	if cfg == nil {
		return commits.DefaultSettings()
	}
	settings := commits.Settings{
		Types:         cfg.Commits.Types,
		HeaderMax:     cfg.Commits.HeaderMax,
		ScopeRequired: cfg.Commits.Scopes.Required,
		AllowedScopes: cfg.Commits.Scopes.Allowed,
	}
	if len(settings.Types) == 0 {
		settings.Types = commits.DefaultTypes()
	}
	if settings.HeaderMax == 0 {
		settings.HeaderMax = commits.DefaultHeaderMax
	}
	return settings
}

// lintTitle lints the header the pull request will land as and keeps only the
// rules asked for.
//
// It is [core.PullRequest.MergeHeader] rather than the title because the
// convention is a rule about the trunk's history, and the trunk gets whatever
// the forge writes. The two differ only at the end, so every rule about the
// front of a header — the type, the scope, the breaking marker — reads the
// same string either way, and the one rule that counts to the end of it now
// counts the characters that are really there.
func lintTitle(s PolicySnapshot, keep []string) []commits.Violation {
	message := commits.Parse(s.PR.MergeHeader())
	violations := commits.Lint(message, commits.LintOptions{
		Settings: CommitSettings(s.Config),
		Subject:  commits.SubjectPullRequest,
		Body:     s.PR.Body,
	})
	var out []commits.Violation
	for _, violation := range violations {
		if slices.Contains(keep, violation.Rule) {
			out = append(out, violation)
		}
	}
	return out
}

// unlinkedType reports the title's type and whether it is one that may merge
// without a ticket.
func unlinkedType(cfg *config.Config, title string) (string, bool) {
	kind := commits.Parse(title).Type
	if kind == "" || cfg == nil {
		return kind, false
	}
	return kind, slices.Contains(cfg.Commits.UnlinkedTypes, kind)
}

// GateFiles are the files that say what the gate will ask. The configuration
// is where a team writes down which changes a machine may land unattended,
// and the workflow is what runs the gate that asks. A pull request that
// changes one of them is changing the question, so it is never routine
// whatever its type (spec 09 §2).
func GateFiles() []string {
	files := []string{config.DefaultPath}
	for _, file := range proposedFiles {
		if file.Workflow {
			files = append(files, file.Path)
		}
	}
	return files
}

// unattendedReview is what the team has said about a machine landing this
// particular change with nobody approving it.
type unattendedReview struct {
	// Type is the type of the header a merge would land, empty when the
	// header does not parse.
	Type string
	// Listed reports that agents.unreviewed_types names that type.
	Listed bool
	// Exempt reports that this pull request wants no human approval.
	Exempt bool
	// Because is why a listed type is not exempt after all, and is empty
	// when it is.
	Because string
}

// unattended asks whether the team has said a machine may land this class of
// change without a person approving it (spec 09 §2).
//
// The permission is a class of change written in `.flow.yaml`, never a marker
// on the pull request: an agent with push can label its own work, so a
// per-pull-request signal is one an agent can give itself. A change to the
// file that grants the permission can therefore never use it, and neither can
// a change flow cannot see the whole of — a pull request that quietly edits
// the configuration would otherwise excuse itself by name.
//
// The type is read from the header a merge would land rather than the title,
// because that is the commit the team will live with (spec 09 §2, issue #106).
func unattended(cfg *config.Config, pr core.PullRequest) unattendedReview {
	if cfg == nil {
		return unattendedReview{}
	}
	message := commits.Parse(pr.MergeHeader())
	review := unattendedReview{Type: message.Type}
	if message.Type == "" || !slices.Contains(cfg.Agents.UnreviewedTypes, message.Type) {
		return review
	}
	review.Listed = true
	switch decides, seen := gateFileIn(pr); {
	case message.Breaking:
		review.Because = "a breaking change is never routine, whatever its type"
	case !seen:
		review.Because = fmt.Sprintf("flow cannot see every file it changes, and one of them might be %s", config.DefaultPath)
	case decides != "":
		review.Because = fmt.Sprintf("it changes %s, which is where that permission is written", decides)
	default:
		review.Exempt = true
	}
	return review
}

// gateFileIn reports the first file of [GateFiles] a pull request changes,
// and whether flow can see what it changes at all. A forge that names no file
// or caps the list has not told flow enough to excuse anything: the counts
// come from the pull request itself, so a list shorter than the count is a
// list with something missing from it.
func gateFileIn(pr core.PullRequest) (string, bool) {
	if len(pr.Files.Paths) == 0 || pr.Files.Changed > len(pr.Files.Paths) {
		return "", false
	}
	for _, path := range GateFiles() {
		if slices.Contains(pr.Files.Paths, path) {
			return path, true
		}
	}
	return "", true
}

// linkFix is how this tracker wants a pull request linked. GitHub has a
// structured link; the others read their own key out of the branch name or the
// title, which is the convention they document themselves (spec 04 §3).
func linkFix(cfg *config.Config) string {
	if trackerType(cfg) == config.TrackerGitHubProjects {
		return `add a closing keyword to the description, such as "Closes #123", or link the issue from the Development sidebar`
	}
	return "put the ticket key in the branch name or at the start of the title"
}

// trackerType is the configured tracker, or empty.
func trackerType(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Tracker.Type
}

// started reports whether a column is at or past the first in-progress
// column: the work has begun. A column the configuration does not name is not
// one of its columns, so the answer is no.
func started(cfg *config.Config, column string) bool {
	if cfg == nil || column == "" {
		return false
	}
	at := cfg.Workflow.ColumnIndex(column)
	if at < 0 {
		return false
	}
	for i, candidate := range cfg.Workflow.Columns {
		if candidate.Category == config.CategoryInProgress {
			return at >= i
		}
	}
	return false
}

// columnFor is the column an event's transition takes a ticket to. The review
// column is the one `pr_ready_for_review` lands in and the in-progress column
// is the one `pr_opened_draft` lands in, by definition of what those events
// mean — which is better than matching column ids flow does not own.
func columnFor(cfg *config.Config, event string) (config.Column, bool) {
	if cfg == nil {
		return config.Column{}, false
	}
	transition, set := cfg.Workflow.Transitions[event]
	if !set || transition.To == "" {
		return config.Column{}, false
	}
	return cfg.Workflow.Column(transition.To)
}

// expedited reports whether this pull request's work carries one of the
// expedite labels. Expedited work is the team's own agreed way past a WIP
// limit, and a gate that warned about it anyway would be telling them off for
// following their own process (spec 04 §8).
func expedited(s PolicySnapshot) bool {
	for _, linked := range s.Tickets {
		if expediteLabelled(s.Config, linked.Ticket.Labels) {
			return true
		}
	}
	return expediteLabelled(s.Config, s.PR.Labels)
}

// countable drops the expedited items from a column's contents, since they are
// the agreed exception to the limit they would otherwise break.
func countable(cfg *config.Config, items []WIPItem) []WIPItem {
	var out []WIPItem
	for _, item := range items {
		if !expediteLabelled(cfg, item.Labels) {
			out = append(out, item)
		}
	}
	return out
}

// expediteLabelled reports whether any label is an expedite label.
func expediteLabelled(cfg *config.Config, labels []string) bool {
	if cfg == nil {
		return false
	}
	for _, want := range cfg.Workflow.Expedite.Labels {
		for _, have := range labels {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// joinedWithin reports whether every one of this pull request's tickets was in
// the column before the limit was broken: it is somewhere in the first limit
// arrivals, by the order the board records.
//
// The clause needs an order, and a tracker that will not say when an item
// arrived cannot give one. Rather than guess, the exemption simply does not
// apply then: a pull request is let off only on evidence.
func joinedWithin(items []WIPItem, limit int, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	ordered := make([]WIPItem, len(items))
	copy(ordered, items)
	for _, item := range ordered {
		if item.Since.IsZero() {
			return false
		}
	}
	slices.SortStableFunc(ordered, func(a, b WIPItem) int {
		if a.Since.Equal(b.Since) {
			return strings.Compare(a.Key, b.Key)
		}
		if a.Since.Before(b.Since) {
			return -1
		}
		return 1
	})
	arrived := make(map[string]int, len(ordered))
	for i, item := range ordered {
		arrived[item.Key] = i
	}
	seen := false
	for _, key := range keys {
		place, in := arrived[key]
		if !in {
			continue
		}
		seen = true
		if place >= limit {
			return false
		}
	}
	return seen
}

// ticketKeys are the keys of this pull request's tickets.
func ticketKeys(s PolicySnapshot) []string {
	keys := make([]string, 0, len(s.Tickets))
	for _, linked := range s.Tickets {
		keys = append(keys, linked.Ticket.Key)
	}
	return keys
}

// wipEvidence lists what is in a column, in the order the board reported it.
func wipEvidence(items []WIPItem) []Evidence {
	evidence := make([]Evidence, 0, len(items))
	for _, item := range items {
		evidence = append(evidence, Evidence{Text: item.Key, URL: item.URL})
	}
	return evidence
}

// agentAuthored reports whether a machine opened this pull request.
func agentAuthored(cfg *config.Config, pr core.PullRequest) bool {
	if cfg == nil {
		return false
	}
	if isAgent(cfg, pr.Author) {
		return true
	}
	for _, want := range cfg.Agents.Labels {
		for _, have := range pr.Labels {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// humanApprover is the first person who approved and is not themselves an
// agent, and whether there was one. A rule satisfied by one robot nodding at
// another is not a rule.
func humanApprover(cfg *config.Config, pr core.PullRequest) (string, bool) {
	for _, login := range pr.Approvals() {
		if !isAgent(cfg, login) {
			return login, true
		}
	}
	return "", false
}

// isAgent reports whether a login is one of the configured agents.
func isAgent(cfg *config.Config, login string) bool {
	if cfg == nil || login == "" {
		return false
	}
	return slices.ContainsFunc(cfg.Agents.Authors, func(author string) bool {
		return strings.EqualFold(author, login)
	})
}

// countOf picks the singular or the count-bearing form.
func countOf(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf(many, n)
}
