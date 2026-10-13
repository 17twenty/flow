package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The native-transition half of the live audit (spec 07 §6, 04 §5). `owner:
// native` is flow declining to do something because the tracker's automation
// already does it, and apply can only take somebody's word for that. These
// checks are the evidence.

// minutesAgo is a moment inside the grace period, or just outside it.
func minutesAgo(minutes int) time.Time { return liveNow.Add(-time.Duration(minutes) * time.Minute) }

// TestScenario_V03_AMergeThatLeftItsTicketBehindIsNamed is scenario V03: pull requests
// merged, their tickets are still in review a quarter of an hour later, and
// the workflow says the tracker was going to move them.
func TestScenario_V03_AMergeThatLeftItsTicketBehindIsNamed(t *testing.T) {
	moved := landedPR(2, "feat(api): the one that worked", daysAgo(2))
	stuck := landedPR(3, "feat(api): the one that did not", daysAgo(1))
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{moved, stuck},
	},
		boarded(ticketFor(2), "moved by the automation", "Done", daysAgo(2)),
		boarded(ticketFor(3), "left where it was", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, ticketFor(3))
	evidenceMentions(t, result, "still in In review")
}

// TestTheAutomationIsGivenAQuarterOfAnHour: a merge two minutes old whose
// ticket has not moved is a webhook in flight, not a broken workflow. Spec 07
// §6 gives the tracker fifteen minutes, and a check that did not would go red
// every time doctor ran beside a merge.
func TestTheAutomationIsGivenAQuarterOfAnHour(t *testing.T) {
	justNow := landedPR(4, "feat(api): merged a moment ago", minutesAgo(2))
	late := landedPR(5, "feat(api): merged a while ago", minutesAgo(20))
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{justNow, late},
	},
		boarded(ticketFor(4), "too early to judge", "In review", minutesAgo(2)),
		boarded(ticketFor(5), "late enough to judge", "In review", minutesAgo(20)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, ticketFor(5))
}

// TestATicketAlreadyPastTheColumnIsNotLate: the target is where the event
// sends a ticket, not where it has to stay. Work that merged and then went
// further on — a board with a Released column after Done — has not been left
// behind by the automation, and reporting it would make the check fire on the
// tidiest boards.
func TestATicketAlreadyPastTheColumnIsNotLate(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{
		Owner: config.OwnerNative,
		To:    "in_review",
	}
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(6, "feat(api): long done", daysAgo(2))},
	},
		boarded(ticketFor(6), "further on than the merge asked for", "Done", daysAgo(2)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestATicketWithWorkStillOpenIsNotTheAutomationsFault is spec 04 §5 rule 4
// from the tracker's side. A ticket is done when all of its work is, so a
// tracker that left a ticket alone while a second pull request was still open
// did the right thing — and a check that demanded the move would be asking
// the automation to make the board lie.
func TestATicketWithWorkStillOpenIsNotTheAutomationsFault(t *testing.T) {
	first := landedPR(7, "feat(api): the first half", daysAgo(2))
	second := inFlightPR(8, "feat(api): the second half", daysAgo(1))
	second.ClosingIssues = first.ClosingIssues
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{first, second},
	},
		boarded(ticketFor(7), "half of it has landed", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
}

// TestClosingATicketIsJudgedFromTheBoard: `issue_closed` is the one native
// event that is not about a pull request at all, so the occasion the
// automation had to work is read from the board's own close date.
func TestClosingATicketIsJudgedFromTheBoard(t *testing.T) {
	stuck := boarded("#930", "closed by hand, left in review", "In review", daysAgo(1))
	stuck.Closed, stuck.ClosedAt = true, daysAgo(1)
	finished := boarded("#931", "closed and moved", "Done", daysAgo(1))
	finished.Closed, finished.ClosedAt = true, daysAgo(1)
	open := boarded("#932", "still open", "In review", daysAgo(1))
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, stuck, finished, open)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventIssueClosed))
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#930")
}

// TestAReviewAskingForChangesIsVerifiedBackwards: `changes_requested` is the
// one native transition that moves a ticket against the board's order, so the
// question is the mirror of the others — a ticket further on than the column
// the review should have sent it back to is the failure.
func TestAReviewAskingForChangesIsVerifiedBackwards(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{
		Owner: config.OwnerNative,
		To:    "in_progress",
	}
	rejected := inFlightPR(9, "feat(api): needs another go", daysAgo(2))
	rejected.Reviews = []core.Review{
		{Author: "dev", State: core.ReviewChangesRequested, SubmittedAt: daysAgo(2)},
	}
	sentBack := inFlightPR(10, "feat(api): also needs another go", daysAgo(2))
	sentBack.Reviews = rejected.Reviews
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{rejected, sentBack},
	},
		boarded(ticketFor(9), "still sitting in review", "In review", daysAgo(2)),
		boarded(ticketFor(10), "sent back as it should be", "In progress", daysAgo(2)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventChangesRequested))
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, ticketFor(9))
}

// TestAnEventFlowCannotDateIsNotJudged: nothing records when a pull request
// was marked ready for review, so the last time anything happened to it
// stands in — always at or after the real moment, so the grace period is only
// ever generous. An event with no date at all would be judged from the zero
// time, which would report every ticket as years late.
func TestAnEventFlowCannotDateIsNotJudged(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventPRReadyForReview] = config.Transition{
		Owner: config.OwnerNative,
		To:    "in_review",
	}
	touched := inFlightPR(11, "feat(api): commented on a moment ago", minutesAgo(1))
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{touched},
	},
		boarded(ticketFor(11), "not moved yet", "In progress", minutesAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRReadyForReview))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
}

// TestATransitionFlowOwnsIsNotTheTrackersToAnswerFor: the check exists per
// native event, and a workflow that took the event back from the tracker has
// nothing for it to verify. `flow check pr` performs it and doctor's catch-up
// repairs it; blaming the tracker would point the remediation at a workflow
// nobody turned on.
func TestATransitionFlowOwnsIsNotTheTrackersToAnswerFor(t *testing.T) {
	// The registry is built from the configuration the run loaded and the
	// checks read Env.Config, so the two can differ — which is how a check
	// gets to say "not mine" rather than answering about a workflow that has
	// changed under it.
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(13, "feat(api): merged and forgotten", daysAgo(1))},
	},
		boarded(ticketFor(13), "left where it was", "In review", daysAgo(1)),
	)
	env.Config = liveConfig()
	env.Config.Workflow.Transitions[config.EventPRMerged] = config.Transition{
		Owner: config.OwnerFlow,
		To:    "done",
	}

	result := runCheck(t, env, engine.LiveChecks(liveConfig()), engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StateSkip)
}

// runCheck runs one check out of a list built over a different configuration
// from the one the run is holding.
func runCheck(t *testing.T, env *engine.Env, checks []engine.Check, id string) engine.Result {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return check.Run(t.Context(), env)
		}
	}
	t.Fatalf("there is no %s check", id)
	return engine.Result{}
}

// TestOnlyTheEventsTheWorkflowGivesTheTrackerAreAsked: there is one check per
// native transition, named for the event, because an automation can be on for
// merges and off for closes and one verdict could not say which.
func TestOnlyTheEventsTheWorkflowGivesTheTrackerAreAsked(t *testing.T) {
	cfg := liveConfig()
	delete(cfg.Workflow.Transitions, config.EventIssueClosed)
	var ids []string
	for _, check := range engine.LiveChecks(cfg) {
		if strings.HasPrefix(check.ID, engine.CheckLiveTransitionsPrefix) {
			ids = append(ids, check.ID)
		}
	}
	if want := []string{engine.LiveTransitionCheck(config.EventPRMerged)}; len(ids) != 1 || ids[0] != want[0] {
		t.Errorf("the audit verifies %v, want %v: pr_merged is the only transition left to the tracker", ids, want)
	}
}

// TestTheRemedyForAMissedTransitionIsNeverAFix: last Tuesday's merge cannot be
// re-fired, so the way out is to turn the built-in workflow back on or to take
// the event off the tracker altogether. The check points at the attestation
// that said it was on.
func TestTheRemedyForAMissedTransitionIsNeverAFix(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(12, "feat(api): merged and forgotten", daysAgo(1))},
	},
		boarded(ticketFor(12), "left where it was", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StateFail)
	if len(result.Remediation.Steps) == 0 {
		t.Fatalf("a missed transition offers nothing to do about it: %+v", result)
	}
	if got := result.Remediation.Kind; got != engine.RemedyManual {
		t.Errorf("the remedy is %q, want %q: doctor cannot re-fire last week's merge", got, engine.RemedyManual)
	}
	var steps string
	for _, step := range result.Remediation.Steps {
		steps += step + "\n"
	}
	for _, want := range []string{"tracker.native.pr_merged", "owner: flow"} {
		if !strings.Contains(steps, want) {
			t.Errorf("the remedy does not mention %q:\n%s", want, steps)
		}
	}
}

// TestATicketInAColumnTheWorkflowDoesNotNameIsNotEvidence is spec 04 §5
// rule 3 from the audit's side. flow does not know where a ticket in a status
// the configuration never mentions actually is, so it cannot say the
// automation left it behind — and a board with an extra Blocked column would
// otherwise fail every transition check it has.
func TestATicketInAColumnTheWorkflowDoesNotNameIsNotEvidence(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(14, "feat(api): merged", daysAgo(1))},
	},
		boarded(ticketFor(14), "parked somewhere flow does not know", "Blocked", daysAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	wantSays(t, result, "flow can place on the board")
}

// TestAMergeForATicketNobodyPutOnTheBoardIsNotTheTrackersFault: a project's
// automation can only move what is in the project. Counting an issue nobody
// added would report the board's membership as a broken workflow, which is a
// different complaint with a different remedy.
func TestAMergeForATicketNobodyPutOnTheBoardIsNotTheTrackersFault(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(15, "feat(api): merged", daysAgo(1))},
	},
		boarded("#940", "something else entirely", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventPRMerged))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	wantSays(t, result, "flow can place on the board")
}

// TestAnEventOlderThanTheWindowIsSomebodyElsesFortnight: the audit is about
// the window it was given. A review that asked for changes three months ago
// is not this fortnight's evidence, and judging it would make the daily report
// carry the same complaint for ever.
func TestAnEventOlderThanTheWindowIsSomebodyElsesFortnight(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{
		Owner: config.OwnerNative,
		To:    "in_progress",
	}
	stale := inFlightPR(16, "feat(api): abandoned after a review", liveNow.AddDate(0, 0, -90))
	stale.Reviews = []core.Review{
		{Author: "dev", State: core.ReviewChangesRequested, SubmittedAt: liveNow.AddDate(0, 0, -90)},
	}
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{stale},
	},
		boarded(ticketFor(16), "never went back", "In review", liveNow.AddDate(0, 0, -90)),
	)

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventChangesRequested))
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
}

// TestAnEventNoTrackerCanBeAskedAboutIsSkippedNotPassed: `deployed` is the one
// event in spec 04 §4's vocabulary that flow has no dated record of from the
// forge, and a check that answered "nothing in the window" would be a green
// tick for a question nobody asked. Vacuous passes are how an audit comes to
// mean nothing.
func TestAnEventNoTrackerCanBeAskedAboutIsSkippedNotPassed(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{
		Owner: config.OwnerNative,
		To:    "done",
	}
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{})

	result := liveResult(t, env, engine.LiveTransitionCheck(config.EventDeployed))
	wantState(t, result, engine.StateSkip)
	wantSays(t, result, "cannot tell when deployed last happened")
}

// wantSays fails unless the result's message contains the words.
func wantSays(t *testing.T, got engine.Result, want string) {
	t.Helper()
	if !strings.Contains(got.Message, want) {
		t.Errorf("the message is %q, want it to mention %q", got.Message, want)
	}
}
