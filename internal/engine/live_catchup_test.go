package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The catch-up (spec 07 §6, 04 §5). The native half of the transition audit
// can only ever complain; this half is about the moves flow owns, which flow
// can still make. Every test here is as much about what `--fix` must not
// touch as about what it must.

// catchUp runs the check with the board, the forge and whether flow is
// allowed to act, and hands back the tracker so a test can see what was done
// to it.
func catchUp(t *testing.T, cfg *config.Config, fix bool, fx coretest.ForgeFixture, tickets ...core.Ticket) (engine.Result, *coretest.FakeTracker) {
	t.Helper()
	env, tracker := boardEnv(t, cfg, fx, tickets...)
	env.Fix = fix
	return liveResult(t, env, engine.CheckLiveTransitionsFlow), tracker
}

// movesTo is the column the tracker ended up with a ticket in.
func movesTo(t *testing.T, tracker *coretest.FakeTracker, key string) string {
	t.Helper()
	tickets, err := tracker.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("the board cannot be read back: %v", err)
	}
	for _, ticket := range tickets {
		if ticket.Key == key {
			return ticket.Status.Name
		}
	}
	t.Fatalf("there is no %s on the board", key)
	return ""
}

// TestScenario_V04_ADroppedEventIsSomethingFlowCanStillPutRight is scenario V04. A pull
// request was marked ready for review two days ago and no CI run ever told
// flow, so the ticket is still in the column the draft put it in. Reporting
// that is worth little: flow owns the transition, the decision is the same
// one `flow check pr` would have made, and nothing about it has gone stale.
func TestScenario_V04_ADroppedEventIsSomethingFlowCanStillPutRight(t *testing.T) {
	ready := inFlightPR(2, "feat(api): offered for review and never announced", daysAgo(2))
	board := []core.Ticket{boarded(ticketFor(2), "left in progress", "In progress", daysAgo(2))}
	fixture := coretest.ForgeFixture{PullRequests: []core.PullRequest{ready}}

	told, quiet := catchUp(t, liveConfig(), false, fixture, board...)
	wantState(t, told, engine.StateFail)
	wantCounts(t, told, 0, 1)
	evidenceMentions(t, told, ticketFor(2))
	if told.Remediation.Kind != engine.RemedyAuto || told.Remediation.Command != "flow doctor --fix" {
		t.Errorf("the remedy is %+v, want flow's own command", told.Remediation)
	}
	if got := quiet.Calls("Move"); got != 0 {
		t.Errorf("a run without --fix moved %d tickets, want 0", got)
	}

	fixed, tracker := catchUp(t, liveConfig(), true, fixture, board...)
	wantState(t, fixed, engine.StatePass)
	wantCounts(t, fixed, 1, 1)
	if got := tracker.Calls("Move"); got != 1 {
		t.Errorf("--fix made %d moves, want 1", got)
	}
	if got := movesTo(t, tracker, ticketFor(2)); got != "In review" {
		t.Errorf("the ticket is in %q, want In review", got)
	}
	evidenceMentions(t, fixed, "moved it from In progress to In review")
}

// TestWorkThatOnlyJustArrivedIsNotAMoveFlowIsLateWith: the same fifteen
// minutes the tracker's automation gets (spec 07 §6). A pull request offered
// for review two minutes ago may well have a CI run in flight that is about
// to make the move, and a doctor that raced it would both report a fault
// that is not there and perform a move somebody else is already performing.
func TestWorkThatOnlyJustArrivedIsNotAMoveFlowIsLateWith(t *testing.T) {
	result, tracker := catchUp(t, liveConfig(), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{
			inFlightPR(3, "feat(api): offered a moment ago", minutesAgo(2)),
		}},
		boarded(ticketFor(3), "not yet flow's problem", "In progress", minutesAgo(2)),
	)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow moved %d tickets inside the grace period, want 0", got)
	}
	if !strings.Contains(result.Message, "less than 15 minutes") {
		t.Errorf("the message is %q, which does not say the move is still young", result.Message)
	}
}

// TestFlowDoesNotCatchUpWhatItDoesNotOwn: `pr_merged` is the tracker's in this
// workflow. A ticket the tracker's automation left behind is a finding of
// live.transitions.native.pr_merged and nothing for flow to act on —
// performing it anyway would make `owner: native` a comment rather than a
// decision, and would hide a broken automation by papering over it daily.
func TestFlowDoesNotCatchUpWhatItDoesNotOwn(t *testing.T) {
	result, tracker := catchUp(t, liveConfig(), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{
			landedPR(4, "feat(api): merged and left behind", daysAgo(2)),
		}},
		boarded(ticketFor(4), "the automation never moved it", "In review", daysAgo(2)),
	)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow performed %d moves the tracker owns, want 0", got)
	}
}

// TestATicketTheWorkflowCannotPlaceIsLeftAlone is spec 04 §5 rule 3 under
// --fix, where getting it wrong is no longer only a wrong sentence. Flow does
// not know what a column it has never heard of means, so it does not know
// whether moving out of it is forward, backward or destructive.
func TestATicketTheWorkflowCannotPlaceIsLeftAlone(t *testing.T) {
	result, tracker := catchUp(t, liveConfig(), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{
			inFlightPR(5, "feat(api): ready for review", daysAgo(2)),
		}},
		boarded(ticketFor(5), "parked somewhere flow has never heard of", "Triage", daysAgo(2)),
	)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow moved %d tickets out of a column it cannot place, want 0", got)
	}
}

// TestATicketWithWorkStillOpenIsNotFinishedEarly is spec 04 §5 rule 4 from
// the catch-up's side. One merge of two is not a finished ticket, and a
// catch-up that read the merge on its own would move work to Done while a
// colleague was still waiting for review on the rest of it.
func TestATicketWithWorkStillOpenIsNotFinishedEarly(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{Owner: config.OwnerFlow, To: "done"}
	half := landedPR(6, "feat(api): the first half", daysAgo(2))
	rest := inFlightPR(7, "feat(api): the second half", daysAgo(1))
	rest.ClosingIssues = half.ClosingIssues

	result, tracker := catchUp(t, cfg, true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{half, rest}},
		boarded(ticketFor(6), "half delivered", "In review", daysAgo(1)),
	)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow finished %d tickets whose work was still open, want 0", got)
	}
}

// TestAHalfReadBoardIsNotActedOn: the catch-up reads the forge strictly, and
// the other live checks do not. They only ever describe what they saw, so a
// pull request they could not read costs them a little accuracy. This one
// acts, and a ticket's column is decided by *all* of its work — so a reading
// that is missing the open half of it is exactly the reading that moves work
// to Done too early. Nothing moves on a partial answer.
func TestAHalfReadBoardIsNotActedOn(t *testing.T) {
	env, tracker := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{inFlightPR(8, "feat(api): ready for review", daysAgo(2))},
	},
		boarded(ticketFor(8), "waiting on a move", "In progress", daysAgo(2)),
	)
	env.Fix = true
	forge, ok := env.Adapters.Forge.(*coretest.FakeForge)
	if !ok {
		t.Fatalf("the live tests no longer use the reference forge: %T", env.Adapters.Forge)
	}
	forge.Fail("PullRequests", errors.New("the forge is having a bad morning"))

	result := liveResult(t, env, engine.CheckLiveTransitionsFlow)

	wantState(t, result, engine.StateError)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow made %d moves on a reading it could not finish, want 0", got)
	}
}

// TestABoardThatRefusesTheMoveIsReportedNotRetried: a credential that may read
// the board and not write it is the ordinary way a catch-up fails, and the
// remedy flow prints afterwards must not be the command that just failed.
func TestABoardThatRefusesTheMoveIsReportedNotRetried(t *testing.T) {
	env, tracker := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{inFlightPR(9, "feat(api): ready for review", daysAgo(2))},
	},
		boarded(ticketFor(9), "waiting on a move", "In progress", daysAgo(2)),
	)
	env.Fix = true
	tracker.Fail("Move", core.Errorf(core.ErrPermission, "move an item").WithWhy("the token may only read"))

	result := liveResult(t, env, engine.CheckLiveTransitionsFlow)

	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "flow could not move it to In review")
	if result.Remediation.Kind != engine.RemedyManual {
		t.Errorf("the remedy is %q, want a manual one: --fix has already been tried", result.Remediation.Kind)
	}
	if result.Remediation.Command != "" {
		t.Errorf("the remedy offers %q, which is the command that just failed", result.Remediation.Command)
	}
}

// TestADeployedTicketIsOnlyFinishedOnceItIsRunning: `deployed` is the one
// event with no date of its own on the pull request. Dating it from the pull
// request's last edit — which is what every other event falls back to — would
// read a merge from last week as deployed last week, and finish a ticket the
// moment the deployment started rather than once it had succeeded.
func TestADeployedTicketIsOnlyFinishedOnceItIsRunning(t *testing.T) {
	cfg := shippingConfig()
	cfg.Workflow.DoneOn = config.DoneOnDeployed
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{Owner: config.OwnerFlow, To: "in_review"}
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{Owner: config.OwnerFlow, To: "done"}

	running := landedPR(10, "feat(api): merged and released", daysAgo(2))
	waiting := landedPR(11, "feat(api): merged and still waiting", daysAgo(2))
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{running, waiting},
		Comparisons: map[string]core.Comparison{
			running.MergeSHA + "...ship001": {Status: core.CompareAhead},
			waiting.MergeSHA + "...ship001": {Status: core.CompareDiverged},
		},
	}, shipment("ship001", daysAgo(2).Add(time.Hour)))
	tracker := coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: []core.Ticket{
		boarded(ticketFor(10), "released", "In review", daysAgo(2)),
		boarded(ticketFor(11), "merged only", "In review", daysAgo(2)),
	}})
	env.Adapters.Tracker = tracker
	env.Fix = true

	result := liveResult(t, env, engine.CheckLiveTransitionsFlow)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 2, 2)
	if got := tracker.Calls("Move"); got != 1 {
		t.Errorf("flow made %d moves, want 1: only the released ticket is finished", got)
	}
	if got := movesTo(t, tracker, ticketFor(10)); got != "Done" {
		t.Errorf("the released ticket is in %q, want Done", got)
	}
	if got := movesTo(t, tracker, ticketFor(11)); got != "In review" {
		t.Errorf("the ticket that never shipped is in %q, want In review", got)
	}
}

// TestADeployTransitionWithNoRecordsToReadIsAnError: a workflow that finishes
// tickets on deployment and cannot read deployments is one where the last
// column is unreachable for ever. Skipping quietly would report a board that
// is permanently one move short as correct.
func TestADeployTransitionWithNoRecordsToReadIsAnError(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{Owner: config.OwnerFlow, To: "done"}

	result, tracker := catchUp(t, cfg, true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{
			landedPR(12, "feat(api): merged", daysAgo(2)),
		}},
		boarded(ticketFor(12), "merged", "In review", daysAgo(2)),
	)

	wantState(t, result, engine.StateError)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow made %d moves with no deployment records to read, want 0", got)
	}
}

// TestAWorkflowFlowOwnsNothingInIsSkipped: every transition native is a team
// that uses flow for the gate and the board's own automation for the moves.
// There is nothing to catch up, and a pass would claim flow had checked
// something it never looked at.
func TestAWorkflowFlowOwnsNothingInIsSkipped(t *testing.T) {
	cfg := liveConfig()
	for event, transition := range cfg.Workflow.Transitions {
		transition.Owner = config.OwnerNative
		cfg.Workflow.Transitions[event] = transition
	}

	result, _ := catchUp(t, cfg, true, coretest.ForgeFixture{})

	wantState(t, result, engine.StateSkip)
}

// TestATicketClosedWithNoPullRequestAtAllStillMoves: closing is the one event
// that is about the ticket rather than about anybody's work (spec 04 §5), so
// a ticket with no pull requests is not automatically a ticket nothing has
// happened to. A catch-up that only looked at tickets with work would leave
// every piece of spiked, duplicated or abandoned work sitting in the column
// it was closed out of.
func TestATicketClosedWithNoPullRequestAtAllStillMoves(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventIssueClosed] = config.Transition{Owner: config.OwnerFlow, To: "done"}
	shut := boarded("#200", "turned out to be a duplicate", "In progress", daysAgo(2))
	shut.Closed, shut.ClosedAt = true, daysAgo(2)

	result, tracker := catchUp(t, cfg, true, coretest.ForgeFixture{}, shut)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if got := movesTo(t, tracker, "#200"); got != "Done" {
		t.Errorf("the closed ticket is in %q, want Done", got)
	}
}

// TestADeploymentGetsTheSameQuarterOfAnHourAsEverythingElse: `flow check
// deploy` runs on the deployment and makes this move itself. A catch-up that
// acted the instant a release succeeded would be racing the command whose job
// it is, on the one event where the two are most likely to overlap — the
// deployment has only just finished, so the command is still running.
func TestADeploymentGetsTheSameQuarterOfAnHourAsEverythingElse(t *testing.T) {
	cfg := shippingConfig()
	cfg.Workflow.DoneOn = config.DoneOnDeployed
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{Owner: config.OwnerFlow, To: "in_review"}
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{Owner: config.OwnerFlow, To: "done"}

	fresh := landedPR(13, "feat(api): released two minutes ago", minutesAgo(30))
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{fresh},
		Comparisons: map[string]core.Comparison{
			fresh.MergeSHA + "...ship002": {Status: core.CompareAhead},
		},
	}, shipment("ship002", minutesAgo(2)))
	tracker := coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: []core.Ticket{
		boarded(ticketFor(13), "just released", "In review", minutesAgo(30)),
	}})
	env.Adapters.Tracker = tracker
	env.Fix = true

	result := liveResult(t, env, engine.CheckLiveTransitionsFlow)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("flow made %d moves on a deployment two minutes old, want 0", got)
	}
	if got := movesTo(t, tracker, ticketFor(13)); got != "In review" {
		t.Errorf("the ticket is in %q, want In review until the grace is up", got)
	}
}

// TestAMoveOwedForDaysIsNotDeferredByFreshWork: two pull requests ask for the
// same move and the grace period runs from the first of them. A ticket that
// should have been in review since Monday is not made to wait another quarter
// of an hour because a colleague opened a second pull request a minute ago.
func TestAMoveOwedForDaysIsNotDeferredByFreshWork(t *testing.T) {
	old := inFlightPR(14, "feat(api): ready since Monday", daysAgo(3))
	fresh := inFlightPR(15, "feat(api): and one more, just now", minutesAgo(1))
	fresh.ClosingIssues = old.ClosingIssues

	result, tracker := catchUp(t, liveConfig(), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{old, fresh}},
		boarded(ticketFor(14), "waiting since Monday", "In progress", minutesAgo(1)),
	)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if got := movesTo(t, tracker, ticketFor(14)); got != "In review" {
		t.Errorf("the ticket is in %q, want In review: it has been owed the move for days", got)
	}
}
