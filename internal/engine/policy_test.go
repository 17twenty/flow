package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// A ticket that satisfies the lean-kanban Definition of Ready, so that a test
// about one rule is not accidentally a test about another.
func linkedTicket(key string) core.Ticket {
	return core.Ticket{
		Key:    key,
		URL:    "https://github.com/17twenty/flow/issues/1",
		Title:  "do the thing",
		Body:   "## Acceptance criteria\n\n- the thing is done\n",
		Parent: "#1",
	}
}

// goodPR is a pull request nothing is wrong with.
func goodPR() core.PullRequest {
	return core.PullRequest{
		Number:  7,
		Title:   "feat(engine): hold a pull request to the workflow",
		Author:  "nick",
		State:   core.PRStateOpen,
		HeadSHA: "abc1234",
	}
}

// snapshot is a passing snapshot of the lean-kanban preset, for a test to
// break in exactly one place.
func snapshot(t *testing.T) engine.PolicySnapshot {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	return engine.PolicySnapshot{
		PR:      goodPR(),
		Tickets: []engine.LinkedTicket{{Ticket: linkedTicket("#12"), Column: "ready", InProject: true}},
		Config:  cfg,
		Now:     time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
	}
}

// finding returns one rule's verdict, failing the test when the rule was not
// evaluated at all.
func finding(t *testing.T, result engine.PolicyResult, rule string) engine.Finding {
	t.Helper()
	for _, f := range result.Findings {
		if f.Rule == rule {
			return f
		}
	}
	t.Fatalf("%s was not evaluated; the result has %d findings", rule, len(result.Findings))
	return engine.Finding{}
}

func TestPolicyPassesACleanPullRequest(t *testing.T) {
	result := engine.CheckPolicy(snapshot(t))
	if !result.Pass() {
		t.Fatalf("a clean pull request did not pass: %v", result.Failures())
	}
	for _, f := range result.Findings {
		if !f.OK {
			t.Errorf("%s did not hold: %s", f.Rule, f.Message)
		}
	}
	if got, want := len(result.Findings), len(engine.PolicyRuleIDs()); got != want {
		t.Errorf("evaluated %d rules, want %d", got, want)
	}
}

func TestPolicyReportsRulesInSpecOrder(t *testing.T) {
	result := engine.CheckPolicy(snapshot(t))
	for i, f := range result.Findings {
		if want := engine.PolicyRuleIDs()[i]; f.Rule != want {
			t.Fatalf("finding %d is %s, want %s", i, f.Rule, want)
		}
	}
}

func TestPolicyIsDeterministic(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "nonsense"
	s.Tickets = append(s.Tickets, engine.LinkedTicket{
		Ticket: core.Ticket{Key: "#13", Blocked: true}, Column: "backlog",
	})
	first := engine.CheckPolicy(s).Text()
	for range 20 {
		if got := engine.CheckPolicy(s).Text(); got != first {
			t.Fatalf("two runs of the same snapshot disagreed:\n%s\n---\n%s", first, got)
		}
	}
}

func TestPolicyTicketLinked(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		tickets  []engine.LinkedTicket
		unlinked []string
		ok       bool
		skipped  bool
	}{
		{name: "a linked ticket", title: "feat(cli): add --json", tickets: []engine.LinkedTicket{{Ticket: linkedTicket("#12")}}, ok: true},
		{name: "nothing linked", title: "feat(cli): add --json"},
		{name: "an exempt type needs none", title: "ci(ci): pin the runner", ok: true, skipped: true},
		{name: "an exempt type with a ticket is not a skip", title: "ci(ci): pin the runner", tickets: []engine.LinkedTicket{{Ticket: linkedTicket("#12")}}, ok: true},
		{name: "a key the tracker does not have", title: "feat(cli): add --json", unlinked: []string{"#99"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.PR.Title = test.title
			s.Tickets = test.tickets
			s.Unlinkable = test.unlinked
			got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketLinked)
			if got.OK != test.ok || got.Skipped != test.skipped {
				t.Fatalf("ok=%v skipped=%v, want ok=%v skipped=%v (%s)", got.OK, got.Skipped, test.ok, test.skipped, got.Message)
			}
			if len(test.unlinked) > 0 && len(got.Evidence) != 1 {
				t.Errorf("a key the tracker does not have was not reported as evidence: %v", got.Evidence)
			}
		})
	}
}

func TestPolicyTicketLinkedIsAnErrorEvenWhenRelaxed(t *testing.T) {
	s := snapshot(t)
	s.Config.Policy.Strictness = config.StrictnessRelaxed
	s.Tickets = nil
	s.PR.Title = "feat(cli): add --json"
	got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketLinked)
	if !got.Blocking() {
		t.Fatalf("relaxed strictness let an unlinked pull request through: %+v", got)
	}
}

func TestPolicyTicketInProject(t *testing.T) {
	t.Run("an issue that is not an item", func(t *testing.T) {
		s := snapshot(t)
		s.Tickets[0].InProject = false
		got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketInProject)
		if got.OK {
			t.Fatal("an issue that is not on the board passed")
		}
		if got.Fix != "add the issue to the project" {
			t.Errorf("fix is %q", got.Fix)
		}
	})
	t.Run("an issue that was archived", func(t *testing.T) {
		s := snapshot(t)
		s.Tickets[0].InProject = false
		s.Tickets[0].Ticket.Archived = true
		got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketInProject)
		if got.OK {
			t.Fatal("an archived issue passed, and the board does not show it")
		}
		if len(got.Evidence) != 1 || !strings.Contains(got.Evidence[0].Text, "archived") {
			t.Errorf("the evidence is %+v, want it to say the issue is archived rather than missing", got.Evidence)
		}
		if !strings.Contains(got.Fix, "restore") || strings.Contains(got.Fix, "add") {
			t.Errorf("fix is %q, want it to ask for the issue back on the board and not to add an issue the project already has", got.Fix)
		}
	})
	t.Run("one archived and one never added", func(t *testing.T) {
		s := snapshot(t)
		s.Tickets[0].InProject = false
		s.Tickets[0].Ticket.Archived = true
		s.Tickets = append(s.Tickets, engine.LinkedTicket{Ticket: linkedTicket("#13"), Column: "ready"})
		got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketInProject)
		if len(got.Evidence) != 2 {
			t.Fatalf("the evidence is %+v, want both issues", got.Evidence)
		}
		if !strings.Contains(got.Fix, "restore") || !strings.Contains(got.Fix, "add") {
			t.Errorf("fix is %q, want it to cover both the archived issue and the missing one", got.Fix)
		}
	})
	t.Run("a tracker with no separate board", func(t *testing.T) {
		s := snapshot(t)
		s.Config.Tracker.Type = config.TrackerJiraCloud
		s.Tickets[0].InProject = false
		got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketInProject)
		if !got.Skipped || !got.OK {
			t.Fatalf("jira was asked a GitHub question: %+v", got)
		}
	})
	t.Run("nothing linked", func(t *testing.T) {
		s := snapshot(t)
		s.Tickets = nil
		got := finding(t, engine.CheckPolicy(s), engine.RulePRTicketInProject)
		if !got.Skipped {
			t.Fatalf("%+v", got)
		}
	})
}

func TestPolicyTicketReady(t *testing.T) {
	tests := []struct {
		name    string
		ticket  core.Ticket
		column  string
		ok      bool
		skipped bool
	}{
		{name: "ready in ready", ticket: linkedTicket("#12"), column: "ready", ok: true},
		{name: "not ready in ready", ticket: core.Ticket{Key: "#12"}, column: "ready"},
		{name: "not ready but already started", ticket: core.Ticket{Key: "#12"}, column: "in_progress", ok: true, skipped: true},
		{name: "not ready but already in review", ticket: core.Ticket{Key: "#12"}, column: "in_review", ok: true, skipped: true},
		{name: "not ready but already done", ticket: core.Ticket{Key: "#12"}, column: "done", ok: true, skipped: true},
		{name: "not ready in a column flow does not know", ticket: core.Ticket{Key: "#12"}, column: "triage"},
		{name: "not ready with no column at all", ticket: core.Ticket{Key: "#12"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.Tickets = []engine.LinkedTicket{{Ticket: test.ticket, Column: test.column, InProject: true}}
			got := finding(t, engine.CheckPolicy(s), engine.RuleTicketReady)
			if got.OK != test.ok || got.Skipped != test.skipped {
				t.Fatalf("ok=%v skipped=%v, want ok=%v skipped=%v (%s)", got.OK, got.Skipped, test.ok, test.skipped, got.Message)
			}
		})
	}
}

func TestPolicyTicketReadySaysWhatIsMissing(t *testing.T) {
	s := snapshot(t)
	s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12"}, Column: "ready", InProject: true}}
	got := finding(t, engine.CheckPolicy(s), engine.RuleTicketReady)
	if len(got.Evidence) != 1 {
		t.Fatalf("evidence is %v", got.Evidence)
	}
	if !strings.Contains(got.Evidence[0].Text, "Acceptance criteria") {
		t.Errorf("the evidence does not say what the ticket is missing: %q", got.Evidence[0].Text)
	}
}

func TestPolicyTitleConventional(t *testing.T) {
	tests := []struct {
		name  string
		title string
		ok    bool
		fixes string
	}{
		{name: "a good header", title: "feat(engine): hold the line", ok: true},
		{name: "not conventional at all", title: "fixed the thing", fixes: "cc.header.format"},
		{name: "a type nobody allowed", title: "wibble(cli): do it", fixes: "cc.type.allowed"},
		{name: "too long", title: "feat(cli): " + strings.Repeat("x", 80), fixes: "cc.header.length"},
		{name: "a trailing full stop is style, not policy", title: "feat(cli): do it.", ok: true},
		{name: "a capital is style, not policy", title: "feat(cli): Do it", ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.PR.Title = test.title
			s.Tickets[0].Ticket.Key = "#12"
			got := finding(t, engine.CheckPolicy(s), engine.RulePRTitleConventional)
			if got.OK != test.ok {
				t.Fatalf("ok=%v, want %v (%s)", got.OK, test.ok, got.Message)
			}
			if test.ok {
				return
			}
			if got.Message == "" || got.Fix == "" {
				t.Errorf("a failing title was not told what to do: %+v", got)
			}
		})
	}
}

// TestPolicyJudgesTheHeaderThatLandsAndNotTheTitle is the bug this rule had on
// flow's own repository: GitHub appends " (#104)" to a squash title, so a
// 66-character title the gate passed landed as a 73-character header, and the
// live audit reported a fortnight later that the trunk broke a rule the gate
// had said was satisfied. The convention is about the trunk's history, so the
// gate holds the pull request to what the forge says it will write.
func TestPolicyJudgesTheHeaderThatLandsAndNotTheTitle(t *testing.T) {
	title := "feat(engine): " + strings.Repeat("x", 58)
	if utf8.RuneCountInString(title) != 72 {
		t.Fatalf("the fixture title is %d characters; it has to be exactly at the limit", utf8.RuneCountInString(title))
	}

	s := snapshot(t)
	s.PR.Title = title
	s.PR.SquashHeader = title + " (#104)"
	got := finding(t, engine.CheckPolicy(s), engine.RulePRTitleConventional)
	if got.OK {
		t.Fatalf("the gate passed a title that lands over the limit; that is how the trunk got a 73-character header")
	}
	if !strings.Contains(got.Message, "79") {
		t.Errorf("the message counts %q, want the 79 characters that land", got.Message)
	}

	var said string
	for _, e := range got.Evidence {
		if strings.Contains(e.Text, "(#104)") {
			said = e.Text
		}
	}
	if said == "" {
		t.Fatalf("nothing in the finding says what the header that lands is, so %q is read against a title a person counts to 72: %+v", got.Message, got.Evidence)
	}
	if !strings.Contains(said, "7 characters") {
		t.Errorf("the evidence does not say how much the forge added: %q", said)
	}
}

// TestPolicySaysNothingAboutALandedHeaderThatIsTheTitle: on a forge that
// appends nothing the two are the same string, and a note explaining a
// difference that is not there would be noise on every failing title.
func TestPolicySaysNothingAboutALandedHeaderThatIsTheTitle(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "wibble(engine): do it"
	s.PR.SquashHeader = s.PR.Title
	got := finding(t, engine.CheckPolicy(s), engine.RulePRTitleConventional)
	if got.OK {
		t.Fatalf("the title is not conventional and the gate passed it")
	}
	if len(got.Evidence) != 0 {
		t.Errorf("the finding explains a difference between the title and the header that lands, and there is none: %+v", got.Evidence)
	}
}

// TestPolicyJudgesTheTitleWhenTheForgeSaidNothing: a pull request built by
// hand, or read through an adapter that has not been taught the question, is
// judged on its title rather than not judged at all.
func TestPolicyJudgesTheTitleWhenTheForgeSaidNothing(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "feat(engine): " + strings.Repeat("x", 80)
	s.PR.SquashHeader = ""
	got := finding(t, engine.CheckPolicy(s), engine.RulePRTitleConventional)
	if got.OK {
		t.Fatalf("a title 94 characters long passed the gate")
	}
	if !strings.Contains(got.Message, "94") {
		t.Errorf("the message is %q, want it to count the 94 characters of the title it had", got.Message)
	}
	if len(got.Evidence) != 0 {
		t.Errorf("the finding invented a difference between a title and a header nobody described: %+v", got.Evidence)
	}
}

func TestPolicyTitleConventionalListsEveryViolationWhenThereIsMoreThanOne(t *testing.T) {
	s := snapshot(t)
	s.Config.Commits.Scopes.Required = true
	s.Config.Commits.Scopes.Allowed = []string{"engine"}
	s.PR.Title = "wibble: " + strings.Repeat("x", 90)
	got := finding(t, engine.CheckPolicy(s), engine.RulePRTitleConventional)
	if len(got.Evidence) < 2 {
		t.Fatalf("only %d violations were listed: %+v", len(got.Evidence), got)
	}
}

func TestPolicyBreakingExplained(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		ok    bool
	}{
		{name: "no breaking marker", title: "feat(cli): add --json", ok: true},
		{name: "explained", title: "feat(cli)!: drop --old", body: "## Breaking change\n\n--old is gone.\n", ok: true},
		{name: "unexplained", title: "feat(cli)!: drop --old", body: "it is time."},
		{name: "a body is not an explanation on a pull request", title: "feat(cli)!: drop --old", body: "lots of prose about nothing"},
		{name: "a title that does not parse has no marker to explain", title: "dropped --old!", ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.PR.Title, s.PR.Body = test.title, test.body
			got := finding(t, engine.CheckPolicy(s), engine.RulePRBreakingExplained)
			if got.OK != test.ok {
				t.Fatalf("ok=%v, want %v (%s)", got.OK, test.ok, got.Message)
			}
		})
	}
}

func TestPolicyAgentHumanReview(t *testing.T) {
	approved := func(logins ...string) []core.Review {
		var reviews []core.Review
		for _, login := range logins {
			reviews = append(reviews, core.Review{Author: login, State: core.ReviewApproved})
		}
		return reviews
	}
	tests := []struct {
		name    string
		author  string
		labels  []string
		reviews []core.Review
		require bool
		ok      bool
		skipped bool
	}{
		{name: "a person opened it", author: "nick", require: true, ok: true, skipped: true},
		{name: "an agent with no approval", author: "flow-bot", require: true},
		{name: "an agent a person approved", author: "flow-bot", reviews: approved("nick"), require: true, ok: true},
		{name: "an agent another agent approved", author: "flow-bot", reviews: approved("other-bot"), require: true},
		{name: "a label marks agent work", author: "nick", labels: []string{"agent"}, require: true},
		{name: "the team does not require it", author: "flow-bot", require: false, ok: true, skipped: true},
		{name: "a dismissed approval does not count", author: "flow-bot", require: true,
			reviews: []core.Review{{Author: "nick", State: core.ReviewApproved}, {Author: "nick", State: core.ReviewDismissed}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.Config.Agents.Authors = []string{"flow-bot", "other-bot"}
			s.Config.Agents.RequireHumanReview = test.require
			s.PR.Author = test.author
			s.PR.Labels = test.labels
			s.PR.Reviews = test.reviews
			got := finding(t, engine.CheckPolicy(s), engine.RuleAgentHumanReview)
			if got.OK != test.ok || got.Skipped != test.skipped {
				t.Fatalf("ok=%v skipped=%v, want ok=%v skipped=%v (%s)", got.OK, got.Skipped, test.ok, test.skipped, got.Message)
			}
		})
	}
}

// TestPolicyAgentHumanReviewExemptsTheClassTheTeamWroteDown: a team may say
// that a class of change needs no person. The permission is a type in
// `.flow.yaml`, which only a person can change, and never a marker on the
// pull request, which an agent with push can give its own work (spec 09 §2).
func TestPolicyAgentHumanReviewExemptsTheClassTheTeamWroteDown(t *testing.T) {
	const workflow = ".github/workflows/flow.yml"
	touching := func(paths ...string) core.FileSummary {
		return core.FileSummary{Changed: len(paths), Paths: paths}
	}
	tests := []struct {
		name   string
		title  string
		squash string
		files  core.FileSummary
		ok     bool
		says   string
	}{
		{
			name:  "a chore lands unattended",
			title: "chore(deps): bump the toolchain",
			files: touching("go.mod"),
			ok:    true,
			says:  `a "chore" pull request may merge without a person approving it`,
		},
		{
			name:  "a feature still wants a person",
			title: "feat(engine): hold a pull request to the workflow",
			files: touching("internal/engine/policy.go"),
		},
		{
			name:  "a header that does not parse names no type at all",
			title: "bump the toolchain",
			files: touching("go.mod"),
		},
		{
			name:  "a breaking chore is not routine",
			title: "chore(deps)!: drop the old toolchain",
			files: touching("go.mod"),
			says:  "a breaking change is never routine",
		},
		{
			name:  "the configuration cannot excuse itself",
			title: "chore(config): let chores through",
			files: touching(".flow.yaml"),
			says:  "it changes .flow.yaml, which is where that permission is written",
		},
		{
			name:  "nor can the workflow that runs the gate",
			title: "chore(ci): tidy the workflow",
			files: touching(workflow),
			says:  "it changes " + workflow,
		},
		{
			name:  "a change flow cannot see the whole of",
			title: "chore(deps): bump everything",
			files: core.FileSummary{Changed: 40, Paths: []string{"go.mod"}},
			says:  "flow cannot see every file it changes",
		},
		{
			name:  "a change the forge named no file of",
			title: "chore(deps): bump the toolchain",
			files: core.FileSummary{Changed: 1},
			says:  "flow cannot see every file it changes",
		},
		{
			name:  "a change the forge said nothing at all about",
			title: "chore(deps): bump the toolchain",
			says:  "flow cannot see every file it changes",
		},
		{
			name:  "a template is not what decides anything",
			title: "chore(ci): reword the pull request template",
			files: touching(".github/pull_request_template.md"),
			ok:    true,
			says:  `a "chore" pull request may merge`,
		},
		{
			name:   "the type is the one that lands, not the one typed",
			title:  "chore(ui): tidy the wizard",
			squash: "feat(ui): tidy the wizard (#7)",
			files:  touching("internal/ui/wizard.go"),
		},
		{
			name:   "and the other way round",
			title:  "feat(ui): tidy the wizard",
			squash: "chore(ui): tidy the wizard (#7)",
			files:  touching("internal/ui/wizard.go"),
			ok:     true,
			says:   `a "chore" pull request may merge`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.Config.Agents.Authors = []string{"flow-bot"}
			s.Config.Agents.RequireHumanReview = true
			s.Config.Agents.UnreviewedTypes = []string{"chore"}
			s.PR.Author = "flow-bot"
			s.PR.Title = test.title
			s.PR.SquashHeader = test.squash
			s.PR.Files = test.files

			got := finding(t, engine.CheckPolicy(s), engine.RuleAgentHumanReview)
			if got.OK != test.ok || got.Skipped != test.ok {
				t.Fatalf("ok=%v skipped=%v, want both %v (%s)", got.OK, got.Skipped, test.ok, got.Message)
			}
			if test.says != "" && !strings.Contains(said(got), test.says) {
				t.Fatalf("the gate does not say why: %s", said(got))
			}
		})
	}
}

// TestAnExemptionNobodyAskedForChangesNothing: the key is absent from every
// shipped preset, so the rule a machine cannot relax stays absolute until a
// person writes a type into it.
func TestAnExemptionNobodyAskedForChangesNothing(t *testing.T) {
	s := snapshot(t)
	s.Config.Agents.Authors = []string{"flow-bot"}
	s.Config.Agents.RequireHumanReview = true
	s.PR.Author = "flow-bot"
	s.PR.Title = "chore(deps): bump the toolchain"
	s.PR.Files = core.FileSummary{Changed: 1, Paths: []string{"go.mod"}}

	if got := finding(t, engine.CheckPolicy(s), engine.RuleAgentHumanReview); got.OK {
		t.Fatalf("a chore merged unattended with no type written down: %s", got.Message)
	}
}

// said is everything a finding tells a reader: its message and its evidence.
func said(f engine.Finding) string {
	parts := []string{f.Message}
	for _, evidence := range f.Evidence {
		parts = append(parts, evidence.Text)
	}
	return strings.Join(parts, "\n")
}

// inFlight is one thing in a column, with an arrival time the tests can order.
func inFlight(key string, minute int, assignees ...string) engine.WIPItem {
	return engine.WIPItem{
		Key:       key,
		Assignees: assignees,
		Since:     time.Date(2026, 3, 1, 8, minute, 0, 0, time.UTC),
	}
}

func TestPolicyWIPReview(t *testing.T) {
	tests := []struct {
		name     string
		review   []engine.WIPItem
		mine     string
		approved bool
		expedite []string
		ok       bool
		skipped  bool
	}{
		{name: "within the limit", review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2)}, mine: "#12", ok: true},
		{name: "at the limit", review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#12", 3)}, mine: "#12", ok: true},
		{name: "over it, and this is what broke it",
			review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#12", 4)}, mine: "#12"},
		{name: "over it, but this was here first",
			review: []engine.WIPItem{inFlight("#12", 1), inFlight("#1", 2), inFlight("#2", 3), inFlight("#3", 4)}, mine: "#12", ok: true},
		{name: "over it, but this is approved",
			review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#12", 4)}, mine: "#12", approved: true, ok: true},
		{name: "over it, and this is not even in review",
			review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#4", 4)}, mine: "#12"},
		{name: "expedited work does not count",
			review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2),
				{Key: "#4", Labels: []string{"incident"}, Since: time.Unix(10, 0)}, inFlight("#12", 5)}, mine: "#12", ok: true},
		{name: "an expedited pull request is not held up", mine: "#12", expedite: []string{"incident"}, ok: true, skipped: true,
			review: []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#12", 4)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			ticket := linkedTicket(test.mine)
			ticket.Labels = test.expedite
			s.Tickets = []engine.LinkedTicket{{Ticket: ticket, Column: "in_review", InProject: true}}
			s.WIP.Review = test.review
			if test.approved {
				s.PR.Reviews = []core.Review{{Author: "sam", State: core.ReviewApproved}}
			}
			got := finding(t, engine.CheckPolicy(s), engine.RuleWIPReview)
			if got.OK != test.ok || got.Skipped != test.skipped {
				t.Fatalf("ok=%v skipped=%v, want ok=%v skipped=%v (%s)", got.OK, got.Skipped, test.ok, test.skipped, got.Message)
			}
		})
	}
}

func TestPolicyWIPReviewWillNotExcuseAPullRequestWithoutArrivalTimes(t *testing.T) {
	s := snapshot(t)
	s.Tickets = []engine.LinkedTicket{{Ticket: linkedTicket("#12"), Column: "in_review", InProject: true}}
	s.WIP.Review = []engine.WIPItem{{Key: "#12"}, {Key: "#1"}, {Key: "#2"}, {Key: "#3"}}
	got := finding(t, engine.CheckPolicy(s), engine.RuleWIPReview)
	if got.OK {
		t.Fatal("a tracker that would not say when anything arrived still got a pull request let off")
	}
}

func TestPolicyWIPReviewIsSkippedWithoutALimit(t *testing.T) {
	s := snapshot(t)
	s.Config.Workflow.Columns[3].WIP = nil
	s.WIP.Review = []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#4", 4)}
	got := finding(t, engine.CheckPolicy(s), engine.RuleWIPReview)
	if !got.Skipped || !got.OK {
		t.Fatalf("%+v", got)
	}
}

func TestPolicyWIPInProgress(t *testing.T) {
	tests := []struct {
		name     string
		progress []engine.WIPItem
		author   string
		ok       bool
		skipped  bool
	}{
		{name: "within the limit", author: "nick", ok: true,
			progress: []engine.WIPItem{inFlight("#1", 1, "nick"), inFlight("#2", 2, "nick")}},
		{name: "over it", author: "nick",
			progress: []engine.WIPItem{inFlight("#1", 1, "nick"), inFlight("#2", 2, "nick"), inFlight("#3", 3, "nick")}},
		{name: "somebody else's work is not theirs", author: "nick", ok: true,
			progress: []engine.WIPItem{inFlight("#1", 1, "nick"), inFlight("#2", 2, "sam"), inFlight("#3", 3, "sam")}},
		{name: "logins differing in case are the same person", author: "Nick",
			progress: []engine.WIPItem{inFlight("#1", 1, "nick"), inFlight("#2", 2, "nick"), inFlight("#3", 3, "nick")}},
		{name: "an author the forge would not name", author: "", ok: true, skipped: true,
			progress: []engine.WIPItem{inFlight("#1", 1, "nick"), inFlight("#2", 2, "nick"), inFlight("#3", 3, "nick")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.PR.Author = test.author
			s.WIP.InProgress = test.progress
			got := finding(t, engine.CheckPolicy(s), engine.RuleWIPInProgress)
			if got.OK != test.ok || got.Skipped != test.skipped {
				t.Fatalf("ok=%v skipped=%v, want ok=%v skipped=%v (%s)", got.OK, got.Skipped, test.ok, test.skipped, got.Message)
			}
		})
	}
}

func TestPolicyTicketNotBlocked(t *testing.T) {
	tests := []struct {
		name   string
		ticket core.Ticket
		ok     bool
	}{
		{name: "not blocked", ticket: linkedTicket("#12"), ok: true},
		{name: "the tracker says blocked", ticket: core.Ticket{Key: "#12", Blocked: true}},
		{name: "the label says blocked", ticket: core.Ticket{Key: "#12", Labels: []string{"blocked"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.Tickets = []engine.LinkedTicket{{Ticket: test.ticket, Column: "in_progress", InProject: true}}
			got := finding(t, engine.CheckPolicy(s), engine.RuleTicketNotBlocked)
			if got.OK != test.ok {
				t.Fatalf("ok=%v, want %v (%s)", got.OK, test.ok, got.Message)
			}
			if !test.ok && got.Blocking() {
				t.Error("a blocked ticket failed the pull request rather than warning about it")
			}
		})
	}
}

func TestPolicyStrictness(t *testing.T) {
	tests := []struct {
		strictness string
		rule       string
		want       string
	}{
		{strictness: config.StrictnessStandard, rule: engine.RuleTicketReady, want: config.SeverityError},
		{strictness: config.StrictnessStandard, rule: engine.RuleWIPReview, want: config.SeverityWarn},
		{strictness: config.StrictnessRelaxed, rule: engine.RuleTicketReady, want: config.SeverityWarn},
		{strictness: config.StrictnessRelaxed, rule: engine.RulePRTicketLinked, want: config.SeverityError},
		{strictness: config.StrictnessRelaxed, rule: engine.RuleAgentHumanReview, want: config.SeverityError},
		// WIP is the board's business: strict does not let it block a merge
		// (issue #231). policy.rules still can, below.
		{strictness: config.StrictnessStrict, rule: engine.RuleWIPReview, want: config.SeverityWarn},
		{strictness: config.StrictnessStrict, rule: engine.RuleWIPInProgress, want: config.SeverityWarn},
		{strictness: config.StrictnessStrict, rule: engine.RuleTicketNotBlocked, want: config.SeverityError},
		{strictness: "pedantic", rule: engine.RuleWIPReview, want: config.SeverityWarn},
		{strictness: "", rule: engine.RuleTicketReady, want: config.SeverityError},
	}
	for _, test := range tests {
		t.Run(test.strictness+"/"+test.rule, func(t *testing.T) {
			cfg := preset(t, "lean-kanban")
			cfg.Policy.Strictness = test.strictness
			if got := engine.PolicySeverity(cfg, test.rule); got != test.want {
				t.Fatalf("%s under %s is %s, want %s", test.rule, test.strictness, got, test.want)
			}
		})
	}

	// A team that wants the gate to refuse on WIP says so in so many words.
	cfg := preset(t, "lean-kanban")
	cfg.Policy.Rules = map[string]string{engine.RuleWIPReview: config.SeverityError}
	if got := engine.PolicySeverity(cfg, engine.RuleWIPReview); got != config.SeverityError {
		t.Errorf("policy.rules asked for wip.review to be an error and got %s", got)
	}
}

func TestPolicyRuleOverrideBeatsStrictness(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Policy.Strictness = config.StrictnessStrict
	cfg.Policy.Rules = map[string]string{engine.RuleWIPReview: config.SeverityWarn}
	if got := engine.PolicySeverity(cfg, engine.RuleWIPReview); got != config.SeverityWarn {
		t.Fatalf("the override lost to strictness: %s", got)
	}
}

func TestPolicyOffRuleIsNotEvaluated(t *testing.T) {
	s := snapshot(t)
	s.Config.Policy.Rules = map[string]string{engine.RuleTicketReady: config.SeverityOff}
	s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12"}, Column: "ready", InProject: true}}
	result := engine.CheckPolicy(s)
	for _, f := range result.Findings {
		if f.Rule == engine.RuleTicketReady {
			t.Fatalf("a rule switched off was still answered: %+v", f)
		}
	}
	if !result.Pass() {
		t.Fatalf("switching the rule off did not let the pull request through: %v", result.Failures())
	}
}

func TestPolicyState(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		if got := engine.CheckPolicy(snapshot(t)).State(); got != core.StatusSuccess {
			t.Fatalf("state is %q", got)
		}
	})
	t.Run("failing", func(t *testing.T) {
		s := snapshot(t)
		s.PR.Title = "nonsense"
		if got := engine.CheckPolicy(s).State(); got != core.StatusFailure {
			t.Fatalf("state is %q", got)
		}
	})
	t.Run("warning only", func(t *testing.T) {
		s := snapshot(t)
		s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12", Blocked: true}, Column: "in_progress", InProject: true}}
		result := engine.CheckPolicy(s)
		if got := result.State(); got != core.StatusSuccess {
			t.Fatalf("a warning failed the pull request: %q", got)
		}
		if len(result.Warnings()) != 1 {
			t.Fatalf("warnings are %v", result.Warnings())
		}
	})
	t.Run("draft", func(t *testing.T) {
		s := snapshot(t)
		s.PR.Draft = true
		s.PR.Title = "nonsense"
		result := engine.CheckPolicy(s)
		if got := result.State(); got != core.StatusPending {
			t.Fatalf("a draft is %q", got)
		}
		if got := result.Description(); got != engine.DraftDescription {
			t.Fatalf("a draft says %q", got)
		}
		if len(result.Failures()) == 0 {
			t.Error("a draft was not told what is wrong with it")
		}
	})
}

func TestPolicyDescription(t *testing.T) {
	tests := []struct {
		name string
		make func(*engine.PolicySnapshot)
		want string
	}{
		{name: "clean", make: func(*engine.PolicySnapshot) {}, want: "✓ every rule passes"},
		{name: "one error", want: "✗ 1 error — pr.title.conventional", make: func(s *engine.PolicySnapshot) {
			s.PR.Title = "nonsense"
		}},
		{name: "two errors", want: "✗ 2 errors — ticket.ready, pr.title.conventional", make: func(s *engine.PolicySnapshot) {
			s.PR.Title = "nonsense"
			s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12"}, Column: "ready", InProject: true}}
		}},
		{name: "one warning", want: "⚠ 1 warning — ticket.not_blocked", make: func(s *engine.PolicySnapshot) {
			s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12", Blocked: true}, Column: "in_progress", InProject: true}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			test.make(&s)
			if got := engine.CheckPolicy(s).Description(); got != test.want {
				t.Fatalf("description is %q, want %q", got, test.want)
			}
		})
	}
}

// TestPolicyDescriptionGolden pins what the gate says when everything it can
// check has gone wrong at once, which is the case the 140-character limit is
// for. Run go test ./internal/engine -run Golden -update to rewrite it.
func TestPolicyDescriptionGolden(t *testing.T) {
	s := snapshot(t)
	s.Config.Policy.Strictness = config.StrictnessStrict
	// WIP only blocks when a team says so (issue #231); this one does, so
	// that every rule can fail at once.
	s.Config.Policy.Rules = map[string]string{engine.RuleWIPReview: config.SeverityError, engine.RuleWIPInProgress: config.SeverityError}
	s.Config.Agents.Authors = []string{"flow-bot"}
	s.Config.Agents.RequireHumanReview = true
	s.PR.Title = "wibble(nope)!: do the thing"
	s.PR.Author = "flow-bot"
	s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12", Blocked: true}, Column: "ready"}}
	s.WIP.Review = []engine.WIPItem{inFlight("#1", 1), inFlight("#2", 2), inFlight("#3", 3), inFlight("#12", 4)}
	s.WIP.InProgress = []engine.WIPItem{inFlight("#5", 1, "flow-bot"), inFlight("#6", 2, "flow-bot"), inFlight("#7", 3, "flow-bot")}

	result := engine.CheckPolicy(s)
	if len(result.Failures()) < 8 {
		t.Fatalf("only %d rules failed; the golden is meant to overflow the line", len(result.Failures()))
	}
	description := result.Description()
	if n := utf8.RuneCountInString(description); n > engine.MaxStatusDescription {
		t.Fatalf("the description is %d characters, over the limit of %d: %q", n, engine.MaxStatusDescription, description)
	}
	if !strings.Contains(description, "more") {
		t.Fatalf("the list was not cut short, so the golden pins nothing: %q", description)
	}
	checkGolden(t, filepath.Join("testdata", "policy", "description.golden"), description+"\n")
}

func TestPolicyDescriptionAlwaysCountsTruthfully(t *testing.T) {
	findings := make([]engine.Finding, 0)
	for _, rule := range engine.PolicyRuleIDs() {
		findings = append(findings, engine.Finding{Rule: rule, Severity: config.SeverityError})
	}
	result := engine.PolicyResult{Findings: findings}
	description := result.Description()
	if want := fmt.Sprintf("✗ %d errors", len(engine.PolicyRuleIDs())); !strings.HasPrefix(description, want) {
		t.Fatalf("the count was trimmed along with the list: %q", description)
	}
	if !strings.Contains(description, "more") {
		t.Fatalf("nothing said that the list was cut short: %q", description)
	}
	if n := utf8.RuneCountInString(description); n > engine.MaxStatusDescription {
		t.Fatalf("%d characters: %q", n, description)
	}
}

func TestPolicyDescriptionWithOneUnprintableLongRule(t *testing.T) {
	result := engine.PolicyResult{Findings: []engine.Finding{
		{Rule: strings.Repeat("x", 200), Severity: config.SeverityError},
	}}
	description := result.Description()
	if n := utf8.RuneCountInString(description); n > engine.MaxStatusDescription {
		t.Fatalf("%d characters: %q", n, description)
	}
	if description != "✗ 1 error" {
		t.Fatalf("description is %q", description)
	}
}

func TestPolicyStatus(t *testing.T) {
	s := snapshot(t)
	result := engine.CheckPolicy(s)
	status := result.Status(s.Config)
	if status.Context != "flow/policy" {
		t.Fatalf("context is %q", status.Context)
	}
	s.Config.Policy.StatusContext = ""
	if got := engine.StatusContext(s.Config); got != engine.DefaultStatusContext {
		t.Fatalf("an unset context is %q", got)
	}
	if !engine.StatusUnchanged(status, status) {
		t.Fatal("a status is different from itself")
	}
	other := status
	other.Description = "something else"
	if engine.StatusUnchanged(status, other) {
		t.Fatal("two different descriptions were taken for one")
	}
	other = status
	other.TargetURL = "https://example.invalid/run/2"
	if !engine.StatusUnchanged(status, other) {
		t.Fatal("a new run of the same verdict was republished")
	}
}

func TestPolicyCommentWhen(t *testing.T) {
	tests := []struct {
		name  string
		when  string
		clean bool
		want  bool
	}{
		{name: "on_failure with a failure", when: config.CommentOnFailure, want: true},
		{name: "on_failure when clean", when: config.CommentOnFailure, clean: true},
		{name: "always when clean", when: config.CommentAlways, clean: true, want: true},
		{name: "never with a failure", when: config.CommentNever},
		{name: "unset with a failure", when: "", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot(t)
			s.Config.Policy.Comment = test.when
			if !test.clean {
				s.PR.Title = "nonsense"
			}
			plan := engine.CheckPolicy(s).Comment(s.Config)
			if plan.Want != test.want {
				t.Fatalf("want=%v, expected %v", plan.Want, test.want)
			}
			if plan.Want && !strings.Contains(plan.Body, "flow:policy") {
				t.Errorf("the comment carries no marker:\n%s", plan.Body)
			}
		})
	}
}

func TestPolicyCommentIsFoundByItsMarker(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "nonsense"
	body := engine.CheckPolicy(s).CommentBody()
	if !core.HasMarker(body, engine.PolicyMarker()) {
		t.Fatalf("the comment does not carry the policy marker:\n%s", body)
	}
}

func TestPolicyCommentEscapesTableCells(t *testing.T) {
	result := engine.PolicyResult{Findings: []engine.Finding{{
		Rule: "pr.title.conventional", Severity: config.SeverityError,
		Message: "the title is `a|b`", Fix: "write\nit\nagain",
	}}}
	body := result.CommentBody()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| pr.title") && !strings.HasPrefix(line, "| ✗") {
			continue
		}
		if strings.Count(line, "|")-strings.Count(line, "\\|") != 5 {
			t.Fatalf("a cell broke the row: %q", line)
		}
	}
	if strings.Contains(body, "write\nit") {
		t.Fatal("a newline survived into a table cell")
	}
}

// TestPolicyCommentGolden pins the comment a failing pull request gets.
func TestPolicyCommentGolden(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "nonsense"
	s.PR.Body = "no ticket, no convention."
	s.Tickets = []engine.LinkedTicket{
		{Ticket: core.Ticket{Key: "#12", URL: "https://example.invalid/12"}, Column: "ready"},
		{Ticket: core.Ticket{Key: "#13", URL: "https://example.invalid/13", Blocked: true}, Column: "ready"},
	}
	checkGolden(t, filepath.Join("testdata", "policy", "comment.golden"), engine.CheckPolicy(s).CommentBody())
}

// TestPolicyTextGolden pins what the gate prints in a terminal.
func TestPolicyTextGolden(t *testing.T) {
	s := snapshot(t)
	s.PR.Title = "nonsense"
	s.Tickets = []engine.LinkedTicket{{Ticket: core.Ticket{Key: "#12", URL: "https://example.invalid/12"}, Column: "ready"}}
	checkGolden(t, filepath.Join("testdata", "policy", "text.golden"), engine.CheckPolicy(s).Text())
}

// checkGolden compares got with a golden file, writing it under -update.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("make golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestCommitSettingsFallsBackToTheDefaults(t *testing.T) {
	settings := engine.CommitSettings(nil)
	if settings.HeaderMax == 0 || len(settings.Types) == 0 {
		t.Fatalf("no configuration gave no convention: %+v", settings)
	}
	cfg := preset(t, "lean-kanban")
	cfg.Commits.Types = nil
	cfg.Commits.HeaderMax = 0
	settings = engine.CommitSettings(cfg)
	if settings.HeaderMax != 72 || len(settings.Types) == 0 {
		t.Fatalf("an empty convention was not filled in: %+v", settings)
	}
}
