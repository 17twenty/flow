//go:build flowtest

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// The gate scenarios of spec 19, run through the whole command: kong, the
// configuration `flow init` wrote, the fake providers, the engine and the
// rendering. What each one asserts is what somebody looking at the pull
// request would see — the status, the comment, and where the card ended up.

// gateRepo is a configured repository whose providers hold the world a
// scenario needs, and a way of reaching that world once a command has built
// it. The world is born inside the first command, so a test cannot populate
// it beforehand and cannot hold a pointer to it until then either.
func gateRepo(t *testing.T, seed func(*fakeWorld)) (string, func() *fakeWorld) {
	t.Helper()
	dir := fakeRepo(t)
	var world *fakeWorld
	seedFakeWorld(dir, func(w *fakeWorld) {
		world = w
		seed(w)
	})
	return dir, func() *fakeWorld {
		t.Helper()
		if world == nil {
			t.Fatal("no command has built this repository's providers yet")
		}
		return world
	}
}

// gateDocument is the shape of a `flow check pr --json` document a test reads.
type gateDocument struct {
	Schema      string        `json:"schema"`
	PR          int           `json:"pr"`
	SHA         string        `json:"sha"`
	Draft       bool          `json:"draft"`
	State       string        `json:"state"`
	Description string        `json:"description"`
	Pass        bool          `json:"pass"`
	Findings    []gateFinding `json:"findings"`
	Status      string        `json:"status"`
	Comment     string        `json:"comment"`
	ReadOnly    bool          `json:"read_only"`
	Moves       []gateMove    `json:"moves"`
}

// gateFinding is one rule's verdict in that document.
type gateFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	OK       bool   `json:"ok"`
	Skipped  bool   `json:"skipped"`
	Message  string `json:"message"`
}

// gateMove is one ticket transition in that document.
type gateMove struct {
	Ticket string `json:"ticket"`
	From   string `json:"from"`
	To     string `json:"to"`
	Event  string `json:"event"`
}

// gateJSON runs `flow check pr --json` and parses the one document it printed.
func gateJSON(t *testing.T, dir string, args ...string) (gateDocument, int) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "check", "pr"}, args...)...)
	var doc gateDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("check pr --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return doc, code
}

// finding is one rule's verdict, which the test insists was reached at all:
// a rule the gate never evaluated is not a rule that passed.
func (d gateDocument) finding(t *testing.T, rule string) gateFinding {
	t.Helper()
	for _, found := range d.Findings {
		if found.Rule == rule {
			return found
		}
	}
	t.Fatalf("the gate did not judge %s; it judged %v", rule, d.rules())
	return gateFinding{}
}

// rules are the rule ids the gate reported, for a failure message.
func (d gateDocument) rules() []string {
	var ids []string
	for _, found := range d.Findings {
		ids = append(ids, found.Rule)
	}
	return ids
}

// failures are the rules that blocked the pull request.
func (d gateDocument) failures() []string {
	var ids []string
	for _, found := range d.Findings {
		if !found.OK && !found.Skipped {
			ids = append(ids, found.Rule)
		}
	}
	return ids
}

// gatePR is an ordinary open pull request for a scenario to adjust.
func gatePR(number int, title string) core.PullRequest {
	return core.PullRequest{
		Number:  number,
		Title:   title,
		Author:  "dev",
		State:   core.PRStateOpen,
		HeadSHA: "head-of-" + title,
		HeadRef: "work",
		BaseRef: "main",
		URL:     "https://github.test/acme/api/pull/1",
	}
}

// readyTicket is a ticket in the Ready column that meets the Definition of
// Ready the lean-kanban preset asks for: filled-in acceptance criteria, a
// parent, and nothing blocking it.
func readyTicket(key string) core.Ticket {
	return core.Ticket{
		Key:      key,
		Title:    "add x",
		Body:     "## Acceptance criteria\n\n- x is added\n",
		Parent:   "#1",
		Status:   core.Status{ID: "Ready", Name: "Ready"},
		ColumnID: "Ready",
		URL:      "https://github.test/acme/api/issues/12",
	}
}

// ticketNow is the ticket as the tracker holds it at this moment.
func ticketNow(t *testing.T, world *fakeWorld, key string) core.Ticket {
	t.Helper()
	ticket, err := world.tracker.Ticket(context.Background(), key)
	if err != nil {
		t.Fatalf("the tracker lost %s: %v", key, err)
	}
	return ticket
}

// prNow is the pull request as the forge holds it at this moment.
func prNow(t *testing.T, world *fakeWorld, number int) core.PullRequest {
	t.Helper()
	pr, err := world.forge.PullRequest(context.Background(), number)
	if err != nil {
		t.Fatalf("the forge lost #%d: %v", number, err)
	}
	return pr
}

// editFlowConfig rewrites one piece of the written configuration, so that a
// scenario can describe a team whose agreement differs from the preset's.
func editFlowConfig(t *testing.T, dir, old, replacement string) {
	t.Helper()
	path := filepath.Join(dir, ".flow.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf(".flow.yaml was not written: %v", err)
	}
	if !strings.Contains(string(content), old) {
		t.Fatalf(".flow.yaml does not contain %q", old)
	}
	edited := strings.Replace(string(content), old, replacement, 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatalf("rewrite .flow.yaml: %v", err)
	}
}

// TestScenario_G01_GateGreenPullRequestMovesItsCard is scenario G01: a draft pull request
// that breaks no rule, linked to a Ready ticket with acceptance criteria and a
// parent, and the two transitions flow owns.
//
// The scenario says "status success". Spec 09 §2's Decision says a draft is
// judged in full and published as `pending`, because a red cross — or a green
// tick — on work somebody is still writing says something nobody asked. Both
// are asserted here as what they agree on: every rule passes, and the status
// turns green the moment the pull request is offered for review.
func TestScenario_G01_GateGreenPullRequestMovesItsCard(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(7, "feat(engine): add x")
		pr.Draft = true
		pr.Body = "Closes #12"
		pr.ClosingIssues = []string{"#12"}
		w.forge.SetPullRequest(pr)
		w.tracker.SetTicket(readyTicket("#12"))
	})

	draft, code := gateJSON(t, dir, "--pr", "7")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d: the status carries the verdict", code, core.ExitOK)
	}
	if draft.Schema != engine.GateVersion {
		t.Errorf("schema is %q, want %q (spec 02 §3)", draft.Schema, engine.GateVersion)
	}
	if !draft.Pass {
		t.Errorf("a pull request breaking no rule did not pass: %v", draft.failures())
	}
	if draft.State != core.StatusPending || draft.Description != engine.DraftDescription {
		t.Errorf("a draft published %q/%q, want %q/%q (spec 09 §2)",
			draft.State, draft.Description, core.StatusPending, engine.DraftDescription)
	}
	if draft.Status != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q", draft.Status, engine.GatePosted)
	}
	assertMove(t, draft, gateMove{Ticket: "#12", From: "ready", To: "in_progress", Event: "pr_opened_draft"})

	world := worldOf()
	if got := ticketNow(t, world, "#12").Status.Name; got != "In progress" {
		t.Fatalf("#12 is in %q, want In progress: the draft should have started it", got)
	}

	// Offered for review, the same gate finds the same pull request green and
	// takes the card the one further step the team agreed to.
	ready := prNow(t, world, 7)
	ready.Draft = false
	world.forge.SetPullRequest(ready)

	reviewed, code := gateJSON(t, dir, "--pr", "7")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d", code, core.ExitOK)
	}
	if reviewed.State != core.StatusSuccess {
		t.Errorf("a green pull request published %q/%q, want %q",
			reviewed.State, reviewed.Description, core.StatusSuccess)
	}
	assertMove(t, reviewed, gateMove{Ticket: "#12", From: "in_progress", To: "in_review", Event: "pr_ready_for_review"})
	if got := ticketNow(t, world, "#12").Status.Name; got != "In review" {
		t.Errorf("#12 is in %q, want In review", got)
	}
}

// assertMove insists the gate reported exactly that transition.
func assertMove(t *testing.T, doc gateDocument, want gateMove) {
	t.Helper()
	for _, move := range doc.Moves {
		if move == want {
			return
		}
	}
	t.Errorf("the gate's moves are %+v, want one of %+v", doc.Moves, want)
}

// TestScenario_G02_GateExplainsItsFailuresInOneComment is scenario G02: two rules fail, the
// status names both within its 140 characters, and however often the gate runs
// there is one comment on the pull request — which goes away when the problems
// do.
func TestScenario_G02_GateExplainsItsFailuresInOneComment(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(8, "Added x")
		pr.ClosingIssues = []string{"#13"}
		w.forge.SetPullRequest(pr)
		ticket := readyTicket("#13")
		ticket.Body = "we should probably do this at some point"
		w.tracker.SetTicket(ticket)
	})

	failed, code := gateJSON(t, dir, "--pr", "8")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d: a failing gate is a posted status, not a failed job", code, core.ExitOK)
	}
	if failed.State != core.StatusFailure {
		t.Fatalf("the status is %q, want %q", failed.State, core.StatusFailure)
	}
	if n := utf8.RuneCountInString(failed.Description); n > engine.MaxStatusDescription {
		t.Errorf("the description is %d characters, over the limit of %d (spec 09 §2)", n, engine.MaxStatusDescription)
	}
	for _, rule := range []string{engine.RuleTicketReady, engine.RulePRTitleConventional} {
		if finding := failed.finding(t, rule); finding.OK {
			t.Errorf("%s passed; the scenario breaks it", rule)
		}
		if !strings.Contains(failed.Description, rule) {
			t.Errorf("the description %q does not name %s", failed.Description, rule)
		}
	}

	world := worldOf()
	first := world.forge.Comments(8)
	if len(first) != 1 {
		t.Fatalf("the gate left %d comments, want exactly 1", len(first))
	}
	if !strings.Contains(first[0].Body, engine.RuleTicketReady) {
		t.Errorf("the comment does not explain %s:\n%s", engine.RuleTicketReady, first[0].Body)
	}
	// The scenario says one `flow:policy` comment, and the marker is what
	// makes it that comment rather than one that happens to be the only one
	// here: it is how the next run finds this one to edit, and how a person
	// reading the thread knows which comments are flow's (spec 09 §2).
	if marker := engine.PolicyMarker().String(); !strings.Contains(first[0].Body, marker) {
		t.Errorf("the comment carries no %s marker, so nothing can find it again:\n%s", marker, first[0].Body)
	}

	// Run again against the same world: the explanation is edited, never
	// added to.
	if again, _ := gateJSON(t, dir, "--pr", "8"); again.Comment != engine.GatePosted {
		t.Errorf("the second run's comment outcome is %q, want %q", again.Comment, engine.GatePosted)
	}
	second := world.forge.Comments(8)
	if len(second) != 1 {
		t.Fatalf("a second run left %d comments, want still 1 (spec 09 §2)", len(second))
	}
	if second[0].ID != first[0].ID {
		t.Errorf("the comment was replaced (%s → %s), not edited", first[0].ID, second[0].ID)
	}

	// Fix both, and the comment is taken down rather than left arguing with a
	// status that has moved on.
	fixed := prNow(t, world, 8)
	fixed.Title = "feat(engine): add x"
	world.forge.SetPullRequest(fixed)
	world.tracker.SetTicket(readyTicket("#13"))

	green, _ := gateJSON(t, dir, "--pr", "8")
	if green.State != core.StatusSuccess {
		t.Fatalf("after fixing both the status is %q/%q, want %q", green.State, green.Description, core.StatusSuccess)
	}
	if green.Comment != engine.GateDeleted {
		t.Errorf("the comment outcome is %q, want %q", green.Comment, engine.GateDeleted)
	}
	if left := world.forge.Comments(8); len(left) != 0 {
		t.Errorf("%d comments survive a green gate, want none", len(left))
	}
}

// TestScenario_G03_GateLetsAnUnlinkedChoreThrough is scenario G03: the plumbing a
// repository needs does not have to be invented a ticket first.
func TestScenario_G03_GateLetsAnUnlinkedChoreThrough(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPullRequest(gatePR(9, "ci: cache modules"))
	})

	doc, code := gateJSON(t, dir, "--pr", "9")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d", code, core.ExitOK)
	}
	if doc.State != core.StatusSuccess {
		t.Fatalf("an unlinked chore published %q/%q, want %q: %v",
			doc.State, doc.Description, core.StatusSuccess, doc.failures())
	}
	if linked := doc.finding(t, engine.RulePRTicketLinked); !linked.Skipped {
		t.Errorf("%s was enforced on a ci pull request; unlinked_types says it may merge without a ticket", engine.RulePRTicketLinked)
	}
}

// TestScenario_G04_GateHoldsAgentWorkUntilAPersonApproves is scenario G04: an agent's pull
// request is blocked until somebody who is not an agent has approved it.
func TestScenario_G04_GateHoldsAgentWorkUntilAPersonApproves(t *testing.T) {
	const agent = "17twenty-flow-agent[bot]"

	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(10, "ci: cache modules")
		pr.Author = agent
		w.forge.SetPullRequest(pr)
	})
	editFlowConfig(t, dir, "authors: []", "authors: ['"+agent+"']")

	held, code := gateJSON(t, dir, "--pr", "10")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d", code, core.ExitOK)
	}
	if held.State != core.StatusFailure {
		t.Fatalf("unapproved agent work published %q, want %q", held.State, core.StatusFailure)
	}
	if got := held.failures(); len(got) != 1 || got[0] != engine.RuleAgentHumanReview {
		t.Fatalf("the failures are %v, want only %s", got, engine.RuleAgentHumanReview)
	}

	world := worldOf()
	approved := prNow(t, world, 10)
	approved.Reviews = []core.Review{{Author: "17twenty", State: core.ReviewApproved}}
	world.forge.SetPullRequest(approved)

	green, _ := gateJSON(t, dir, "--pr", "10")
	if green.State != core.StatusSuccess {
		t.Errorf("after a person approved it the status is %q, want %q: %v",
			green.State, core.StatusSuccess, green.failures())
	}
}

// TestScenario_G05_GateOnAForkPostsNothing is scenario G05: a pull request from a fork runs
// with a credential that may not write, and the gate reports its verdict and
// succeeds rather than failing a job nobody can fix.
func TestScenario_G05_GateOnAForkPostsNothing(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(11, "Added x")
		w.forge.SetPullRequest(pr)
		w.forge.Fail("SetCommitStatus", core.Errorf(core.ErrPermission, "set a commit status").
			WithWhy("this credential is read-only"))
	})

	doc, code := gateJSON(t, dir, "--pr", "11")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d, want %d: a fork's gate is not a broken build", code, core.ExitOK)
	}
	if !doc.ReadOnly || doc.Status != engine.GateReadOnly {
		t.Errorf("read_only is %v and the status outcome %q, want true and %q", doc.ReadOnly, doc.Status, engine.GateReadOnly)
	}
	if doc.State != core.StatusFailure {
		t.Errorf("the verdict is %q, want %q: it was still reached, only not published", doc.State, core.StatusFailure)
	}

	world := worldOf()
	statuses, err := world.forge.CommitStatuses(context.Background(), doc.SHA)
	if err != nil {
		t.Fatalf("read the statuses: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("%d statuses were published by a read-only credential", len(statuses))
	}
	if left := world.forge.Comments(11); len(left) != 0 {
		t.Errorf("%d comments were written by a read-only credential", len(left))
	}
}

// TestScenario_G06_GateDoesNotRepeatAStatusItAlreadyPosted is scenario G06: the same
// evaluation twice is one line on the pull request's timeline, not two.
func TestScenario_G06_GateDoesNotRepeatAStatusItAlreadyPosted(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPullRequest(gatePR(12, "ci: cache modules"))
	})

	if first, _ := gateJSON(t, dir, "--pr", "12"); first.Status != engine.GatePosted {
		t.Fatalf("the first run's status outcome is %q, want %q", first.Status, engine.GatePosted)
	}
	second, _ := gateJSON(t, dir, "--pr", "12")
	if second.Status != engine.GateUnchanged {
		t.Errorf("the second run's status outcome is %q, want %q", second.Status, engine.GateUnchanged)
	}
	if posts := worldOf().forge.Calls("SetCommitStatus"); posts != 1 {
		t.Errorf("the gate posted the status %d times, want 1 (spec 09 §2)", posts)
	}
}

// TestGateDryRunDecidesAndPostsNothing holds `--dry-run` to what it promises:
// the verdict is reached, and the world is left exactly as it was found.
func TestGateDryRunDecidesAndPostsNothing(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(13, "feat(engine): add x")
		pr.Draft = true
		pr.ClosingIssues = []string{"#12"}
		w.forge.SetPullRequest(pr)
		w.tracker.SetTicket(readyTicket("#12"))
	})

	doc, code := gateJSON(t, dir, "--pr", "13", "--dry-run")
	if code != core.ExitOK {
		t.Fatalf("check pr --dry-run exited %d, want %d", code, core.ExitOK)
	}
	if doc.Status != engine.GateDryRun || doc.Comment != engine.GateDryRun {
		t.Errorf("the outcomes are %q/%q, want %q", doc.Status, doc.Comment, engine.GateDryRun)
	}
	assertMove(t, doc, gateMove{Ticket: "#12", From: "ready", To: "in_progress", Event: "pr_opened_draft"})

	world := worldOf()
	if posts := world.forge.Calls("SetCommitStatus"); posts != 0 {
		t.Errorf("a dry run posted %d statuses", posts)
	}
	if got := ticketNow(t, world, "#12").Status.Name; got != "Ready" {
		t.Errorf("a dry run moved #12 to %q", got)
	}
}

// TestGateNoTransitionsGatesWithoutMovingAnything holds `--no-transitions`:
// the status still goes out, and the board is not touched.
func TestGateNoTransitionsGatesWithoutMovingAnything(t *testing.T) {
	dir, worldOf := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(14, "feat(engine): add x")
		pr.ClosingIssues = []string{"#12"}
		w.forge.SetPullRequest(pr)
		w.tracker.SetTicket(readyTicket("#12"))
	})

	doc, _ := gateJSON(t, dir, "--pr", "14", "--no-transitions")
	if doc.Status != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q", doc.Status, engine.GatePosted)
	}
	if len(doc.Moves) != 0 {
		t.Errorf("--no-transitions reported %+v", doc.Moves)
	}
	if got := ticketNow(t, worldOf(), "#12").Status.Name; got != "Ready" {
		t.Errorf("--no-transitions moved #12 to %q", got)
	}
}

// TestGateExitCodeFailsLocally is the one case where the job's exit code
// carries the verdict: somebody running the gate at their desk, who has no
// commit status to look at.
func TestGateExitCodeFailsLocally(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPullRequest(gatePR(15, "Added x"))
	})

	if _, code := gateJSON(t, dir, "--pr", "15", "--exit-code"); code != core.ExitFail {
		t.Errorf("check pr --exit-code exited %d on a failing gate, want %d", code, core.ExitFail)
	}
	if _, code := gateJSON(t, dir, "--pr", "15"); code != core.ExitOK {
		t.Errorf("check pr without --exit-code exited %d, want %d", code, core.ExitOK)
	}
}

// TestGateTextNamesTheRulesThatFailed holds the terminal rendering to the one
// thing it is for: saying what is wrong without anybody parsing JSON.
func TestGateTextNamesTheRulesThatFailed(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPullRequest(gatePR(16, "Added x"))
	})

	code, stdout, stderr := execAt(t, dir, "check", "pr", "--pr", "16")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, engine.RulePRTitleConventional) {
		t.Errorf("the text rendering does not name %s:\n%s", engine.RulePRTitleConventional, stdout)
	}
}
