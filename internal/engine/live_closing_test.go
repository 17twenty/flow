package engine_test

import (
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// live.prs.closed_issues (issue #201). A pull request the gate armed is merged
// for the Actions token, and GitHub closes nothing it names; flow finishes
// the job on the next --fix, and only then.

// closedIssues runs the check with the board, the forge and whether flow is
// allowed to act.
func closedIssues(t *testing.T, fix bool, prs []core.PullRequest, tickets ...core.Ticket) (engine.Result, *coretest.FakeTracker) {
	t.Helper()
	env, tracker := boardEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs}, tickets...)
	env.Fix = fix
	return liveResult(t, env, engine.CheckLivePRsClosedIssues), tracker
}

// isClosed reads a ticket back from the tracker.
func isClosed(t *testing.T, tracker *coretest.FakeTracker, key string) bool {
	t.Helper()
	ticket, err := tracker.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("read %s back: %v", key, err)
	}
	return ticket.Closed
}

func TestAMergeThatLeftItsIssueOpenIsNamed(t *testing.T) {
	prs := []core.PullRequest{landedPR(1, "fix(api): the thing", daysAgo(2)), landedPR(2, "feat(api): another", daysAgo(1))}
	done := boarded(ticketFor(1), "the thing", "Done", daysAgo(2))
	done.Closed = true
	result, tracker := closedIssues(t, false, prs, done, boarded(ticketFor(2), "another", "In review", daysAgo(1)))

	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, ticketFor(2))
	if isClosed(t, tracker, ticketFor(2)) || tracker.Calls("Close") != 0 {
		t.Errorf("a run without --fix closed something")
	}
}

func TestFixClosesWhatAMergeLeftOpenAndSaysWhy(t *testing.T) {
	prs := []core.PullRequest{landedPR(2, "feat(api): another", daysAgo(1))}
	result, tracker := closedIssues(t, true, prs, boarded(ticketFor(2), "another", "In review", daysAgo(1)))

	wantState(t, result, engine.StatePass)
	if !isClosed(t, tracker, ticketFor(2)) {
		t.Fatalf("%s is still open after --fix", ticketFor(2))
	}
	if tracker.Calls("Comment") != 1 {
		t.Errorf("flow closed %s without saying which merge did the work", ticketFor(2))
	}
	evidenceMentions(t, result, "flow closed it")
}

// An issue the tracker does not hold — another repository's, or one deleted
// since — is not this run's to judge, and is not counted either way.
func TestAnIssueTheTrackerDoesNotHoldIsLeftAlone(t *testing.T) {
	prs := []core.PullRequest{landedPR(3, "fix(api): elsewhere", daysAgo(1))}
	result, tracker := closedIssues(t, true, prs)

	wantCounts(t, result, 0, 0)
	if tracker.Calls("Close") != 0 {
		t.Errorf("flow closed something it could not read")
	}
}
