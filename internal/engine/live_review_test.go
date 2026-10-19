package engine_test

import (
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The review column's two checks (spec 07 §6). Between them they cover the
// gap the board hides best: work that is finished, approved and going
// nowhere, with every check on it green because they were computed on a head
// nothing has touched since.

// reviewable is an open pull request somebody has approved, with the forge's
// reading of whether it would take it.
func reviewable(number int, title, mergeability string, updated time.Time) core.PullRequest {
	pr := inFlightPR(number, title, updated)
	pr.Reviews = []core.Review{{
		Author:      "reviewer",
		State:       core.ReviewApproved,
		SubmittedAt: updated,
	}}
	pr.Mergeability = mergeability
	return pr
}

// TestWorkThatIsApprovedAndCannotLandIsAFinding is the whole point of the
// check: a conflict is the one thing that goes wrong on a pull request
// without anything going red, because no forge sends mail about it and the
// checks that passed stay green on a head that has not moved.
func TestWorkThatIsApprovedAndCannotLandIsAFinding(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			reviewable(11, "feat(api): conflicted", core.MergeabilityConflicted, daysAgo(1)),
			reviewable(12, "feat(api): landable", core.MergeabilityClean, daysAgo(1)),
		},
	},
		boarded(ticketFor(11), "stuck", "In review", daysAgo(1)),
		boarded(ticketFor(12), "fine", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "it conflicts with main")
	evidenceLinks(t, result, "#11", "https://github.com/acme/api/pull/11")
}

// TestWorkBehindAStrictTrunkIsStuckForADifferentReason: a branch that merely
// fell behind needs a push, not a resolution, and telling somebody to go and
// resolve a conflict that is not there wastes the half hour the check was
// meant to save. The two readings come from the forge and stay apart.
func TestWorkBehindAStrictTrunkIsStuckForADifferentReason(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			reviewable(13, "feat(api): behind", core.MergeabilityBehind, daysAgo(1)),
		},
	},
		boarded(ticketFor(13), "overtaken", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "it is behind main")
}

// TestAForgeThatHasNotWorkedItOutHasNotSaidNo: GitHub computes mergeability
// in the background and answers null until it has. Reading silence as a
// refusal would make the check fail hardest on the pull request somebody
// opened a moment ago, which is the one nothing is wrong with.
func TestAForgeThatHasNotWorkedItOutHasNotSaidNo(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			reviewable(14, "feat(api): just opened", core.MergeabilityUnknown, daysAgo(1)),
		},
	},
		boarded(ticketFor(14), "too soon to say", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestNothingIsStuckUntilSomebodyHasApprovedIt: falling behind the trunk is
// the ordinary condition of every pull request in a queue the moment one of
// them merges. A check that counted those would report the whole queue every
// day and mean nothing by it.
func TestNothingIsStuckUntilSomebodyHasApprovedIt(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			conflicted(inFlightPR(15, "feat(api): nobody has looked at it", daysAgo(1))),
		},
	},
		boarded(ticketFor(15), "waiting on a person", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if want := "nothing in In review has been approved yet"; result.Message != want {
		t.Errorf("the check says %q, want %q", result.Message, want)
	}
}

// conflicted is an open pull request the forge would not take.
func conflicted(pr core.PullRequest) core.PullRequest {
	pr.Mergeability = core.MergeabilityConflicted
	return pr
}

// TestAnApprovalSomebodyElseHasOverturnedIsNotAnApproval: one reviewer
// approving and another asking for changes puts the work back with its
// author, so whatever else is in the way, a push to the branch is not what it
// is waiting for. The approval is still there — counting it would be reading
// only half of what the reviewers said.
func TestAnApprovalSomebodyElseHasOverturnedIsNotAnApproval(t *testing.T) {
	pr := reviewable(16, "feat(api): sent back", core.MergeabilityConflicted, daysAgo(1))
	pr.Reviews = append(pr.Reviews, core.Review{
		Author:      "another reviewer",
		State:       core.ReviewChangesRequested,
		SubmittedAt: daysAgo(1).Add(time.Hour),
	})
	if len(pr.Approvals()) == 0 {
		t.Fatal("the fixture has no approval left, so it cannot tell the two rules apart")
	}
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{pr},
	},
		boarded(ticketFor(16), "back with its author", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantCounts(t, result, 0, 0)
}

// TestADraftIsNotInTheQueueHoweverManyApprovalsItHas: offering a pull request
// for review is the act that puts it in the queue at all, and a draft nobody
// can merge is not a queue that has stopped.
func TestADraftIsNotInTheQueueHoweverManyApprovalsItHas(t *testing.T) {
	pr := reviewable(17, "feat(api): approved early", core.MergeabilityConflicted, daysAgo(1))
	pr.Draft = true
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{pr},
	},
		boarded(ticketFor(17), "still a draft", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStuck)
	wantCounts(t, result, 0, 0)
}

// TestAWorkflowWithNoReviewColumnIsNotAskedAboutOne: both of these checks
// find their column through the transition that lands work there, and a
// workflow that names none has not asked the question.
func TestAWorkflowWithNoReviewColumnIsNotAskedAboutOne(t *testing.T) {
	for _, id := range []string{engine.CheckLiveTicketsReviewStuck, engine.CheckLiveTicketsReviewStale} {
		t.Run(id, func(t *testing.T) {
			cfg := liveConfig()
			delete(cfg.Workflow.Transitions, config.EventPRReadyForReview)
			env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
				boarded("#930", "whatever this is", "In review", daysAgo(1)),
			)

			result := liveResult(t, env, id)
			wantState(t, result, engine.StateSkip)
		})
	}
}

// TestAReviewNobodyHasDoneIsOnlyEverAWarning: the review queue is the slowest
// part of most workflows and the only one nothing measures, but a slow review
// is a fact about what people had time for. A repository marked unhealthy for
// it would teach the team that red means nothing.
func TestAReviewNobodyHasDoneIsOnlyEverAWarning(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#931", "forgotten in review", "In review", liveNow.AddDate(0, 0, -30)),
		boarded("#932", "also forgotten", "In review", liveNow.AddDate(0, 0, -30)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStale)
	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 0, 2)
	evidenceMentions(t, result, "#931")
}

// TestAPushCountsAsTheReviewMoving: a ticket in review is often untouched for
// a week while its branch is rebased twice a day. Reading only the tracker's
// own timestamp would report the most active work on the board as stale.
func TestAPushCountsAsTheReviewMoving(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{inFlightPR(18, "feat(api): busy branch", daysAgo(1))},
	},
		boarded(ticketFor(18), "quiet card, busy branch", "In review", liveNow.AddDate(0, 0, -30)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestHowLongIsTooLongIsTheTeamsToSay: the five working days were a constant
// in the engine. A team that has said a fortnight has said it for both the
// columns that have a patience, and the number the report prints is the
// number it judged by.
func TestHowLongIsTooLongIsTheTeamsToSay(t *testing.T) {
	cfg := liveConfig()
	cfg.Health.StaleAfter = 20
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
		boarded("#933", "quiet for a fortnight", "In review", liveNow.AddDate(0, 0, -14)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if want := "all 1 ticket in In review have moved within 20 working days"; result.Message != want {
		t.Errorf("the check says %q, want %q", result.Message, want)
	}
}

// TestTheSamePatienceAnswersForWorkInProgress: one key, not one per column. A
// team has one answer to "how long is too long", and the column work waits in
// before review is judged by it too.
func TestTheSamePatienceAnswersForWorkInProgress(t *testing.T) {
	cfg := liveConfig()
	cfg.Health.StaleAfter = 20
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
		boarded("#934", "quiet for a fortnight", "In progress", liveNow.AddDate(0, 0, -14)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsProgressStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestATrackerThatWillNotSayWhenAReviewStartedIsNotAskedHowLongItHasBeen: an
// undated board is a board flow cannot judge for staleness, and saying
// "nothing is in In review" of a column with work in it would be a lie about
// the one thing the check exists to see.
func TestATrackerThatWillNotSayWhenAReviewStartedIsNotAskedHowLongItHasBeen(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#935", "no dates at all", "In review", time.Time{}),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if want := "nothing in In review has a date to judge it by"; result.Message != want {
		t.Errorf("the check says %q, want %q", result.Message, want)
	}
}
