package engine_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The work-in-progress half of the live-health audit (spec 07 §6). The gate
// holds one pull request to the limits as it opens; this asks how often the
// team was over them, which no provider will answer and the pull requests
// have to be made to.

// wipConfig is liveConfig with limits on the two columns work is in flight
// in. A zero means that limit is not set, which is how a team says it does
// not cap that one.
func wipConfig(limit, perPerson int) *config.Config {
	cfg := liveConfig()
	cfg.Workflow.Expedite = config.Expedite{Labels: []string{"expedite"}}
	for i, column := range cfg.Workflow.Columns {
		switch {
		case column.ID == "in_review" && limit > 0:
			cfg.Workflow.Columns[i].WIP = &config.WIP{Limit: limit}
		case column.ID == "in_progress" && perPerson > 0:
			cfg.Workflow.Columns[i].WIP = &config.WIP{PerPerson: perPerson}
		}
	}
	return cfg
}

// wipEnv is a world with a short window, so that the days a test is about are
// the days it has to account for: four of them, 2025-03-01 to 2025-03-04.
func wipEnv(t *testing.T, cfg *config.Config, prs []core.PullRequest, tickets ...core.Ticket) *engine.Env {
	t.Helper()
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{PullRequests: prs}, tickets...)
	env.Window = 3 * 24 * time.Hour
	return env
}

// offered is a pull request somebody has offered for review, open from one
// moment until another. The zero time for finished means it is open still.
func offered(number int, author string, opened, finished time.Time) core.PullRequest {
	pr := core.PullRequest{
		Number:        number,
		Title:         fmt.Sprintf("feat(api): change %d", number),
		Author:        author,
		State:         core.PRStateOpen,
		BaseRef:       "main",
		HeadSHA:       fmt.Sprintf("head%03d", number),
		ClosingIssues: []string{fmt.Sprintf("#%d", 100+number)},
		CreatedAt:     opened,
		UpdatedAt:     opened,
		URL:           fmt.Sprintf("https://github.com/acme/api/pull/%d", number),
	}
	if !finished.IsZero() {
		pr.State = core.PRStateMerged
		pr.MergeSHA = fmt.Sprintf("merge%03d", number)
		pr.MergedAt, pr.ClosedAt, pr.UpdatedAt = finished, finished, finished
	}
	return pr
}

// drafted is the same pull request, not offered to anybody yet.
func drafted(pr core.PullRequest) core.PullRequest {
	pr.Draft = true
	return pr
}

// day is a moment inside one of the window's days, named the way the report
// names it.
func wipDay(day int) time.Time {
	return time.Date(2025, 3, day, 9, 0, 0, 0, time.UTC)
}

// evidenceSilentAbout fails if any evidence names the thing. A check that
// named every day would pass every test that only looks for the days it
// should have named.
func evidenceSilentAbout(t *testing.T, got engine.Result, want string) {
	t.Helper()
	for _, seen := range got.Evidence {
		if strings.Contains(seen.Text, want) {
			t.Errorf("evidence names %q and should not: %q", want, seen.Text)
		}
	}
}

// TestATeamThatCappedNothingIsNotAuditedForIt is the skip. A limit is a thing
// the team agreed, and a team that agreed none has made no claim to hold them
// to — but the check is still registered and still in the report, because a
// check that vanishes is indistinguishable from a check nobody wrote.
func TestATeamThatCappedNothingIsNotAuditedForIt(t *testing.T) {
	env := wipEnv(t, liveConfig(), nil)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateSkip)
	if !strings.Contains(result.Message, "WIP limit") {
		t.Errorf("the skip does not say why: %q", result.Message)
	}
}

// TestTheDaysTheReviewQueueWasOverItsLimitAreNamed is the check doing its
// job. Three pull requests waiting for review against a limit of two is the
// team carrying more than it agreed to, on every day all three were open.
//
// It is also where the grading is proved: half the window over a limit would
// be a failure by the ordinary thresholds, and spec 07 §6 marks this one warn
// only — a check that could fail a daily run is one somebody turns off.
func TestTheDaysTheReviewQueueWasOverItsLimitAreNamed(t *testing.T) {
	prs := []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		offered(2, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		offered(3, "other", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
	}
	env := wipEnv(t, wipConfig(2, 0), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 2, 4)
	evidenceMentions(t, result, "2025-03-01")
	evidenceMentions(t, result, "2025-03-02")
	evidenceMentions(t, result, "In review held 3, over its limit of 2")
	evidenceSilentAbout(t, result, "2025-03-03")
}

// TestTodayIsReadFromTheBoardAndNotReconstructed is the one day flow does not
// have to guess about. The board is right there, so the last day of the
// window is a reading; the days before it are not, and nothing about the
// board now says anything about them.
func TestTodayIsReadFromTheBoardAndNotReconstructed(t *testing.T) {
	env := wipEnv(t, wipConfig(2, 0), nil,
		boarded("#1", "one", "In review", wipDay(4)),
		boarded("#2", "two", "In review", wipDay(4)),
		boarded("#3", "three", "In review", wipDay(4)),
	)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 3, 4)
	evidenceMentions(t, result, "2025-03-04")
	evidenceSilentAbout(t, result, "2025-03-01")
}

// TestTheEvidenceSaysWhatWasReadAndWhatWasReconstructed is the honesty the
// check owes. The past is an approximation built out of how long pull
// requests were open, and a report that presented it as a record of the board
// would be flow asserting something no provider told it — so the sentence is
// evidence on every run, including the ones where nothing was found.
func TestTheEvidenceSaysWhatWasReadAndWhatWasReconstructed(t *testing.T) {
	env := wipEnv(t, wipConfig(2, 0), nil)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	leadsWith(t, result, "reconstructed")

	// And it leads what the check found, rather than trailing it: a reader who
	// stops after the first finding has to have read the caveat already.
	found := liveResult(t, wipEnv(t, wipConfig(1, 0), []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{}),
		offered(2, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{}),
	}), engine.CheckLiveWIP)

	wantState(t, found, engine.StateWarn)
	leadsWith(t, found, "reconstructed")
}

// leadsWith fails unless the first thing the check says is the thing.
func leadsWith(t *testing.T, got engine.Result, want string) {
	t.Helper()
	if len(got.Evidence) == 0 || !strings.Contains(got.Evidence[0].Text, want) {
		t.Errorf("the first evidence does not mention %q: %+v", want, got.Evidence)
	}
}

// TestAPullRequestNobodyIsRecordedAsHavingOpenedIsNobodysWork is the one
// thing a per-person limit needs and a forge does not always have. A deleted
// account leaves a pull request with no author, and a limit on what one
// person carries cannot be applied to a person nobody named — the gate says
// the same of the pull request in front of it.
func TestAPullRequestNobodyIsRecordedAsHavingOpenedIsNobodysWork(t *testing.T) {
	prs := []core.PullRequest{
		drafted(offered(1, "", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(2, "", wipDay(1).AddDate(0, 0, -3), time.Time{})),
	}
	env := wipEnv(t, wipConfig(0, 1), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestTwoPeopleOverTheLimitAreNamedInOneOrder is what makes two runs of the
// same window the same report. The names come out of a map, and a report that
// reordered itself every morning is one nobody can diff.
func TestTwoPeopleOverTheLimitAreNamedInOneOrder(t *testing.T) {
	prs := []core.PullRequest{
		drafted(offered(1, "zoe", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(2, "zoe", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(3, "alice", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(4, "alice", wipDay(1).AddDate(0, 0, -3), time.Time{})),
	}
	env := wipEnv(t, wipConfig(0, 1), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	evidenceMentions(t, result, "alice had 2")
	for _, seen := range result.Evidence {
		alice, zoe := strings.Index(seen.Text, "alice"), strings.Index(seen.Text, "zoe")
		if alice >= 0 && zoe >= 0 && zoe < alice {
			t.Errorf("the people are named out of order: %q", seen.Text)
		}
	}
}

// TestADayEndsWhereUTCSaysItDoes is the day the report counts. A team reads
// its health from a dashboard and from a terminal in whatever zone they are
// sitting in, and a window whose days moved with the reader would give two
// people two different counts of the same fortnight.
func TestADayEndsWhereUTCSaysItDoes(t *testing.T) {
	env := wipEnv(t, wipConfig(2, 0), nil,
		boarded("#1", "one", "In review", wipDay(4)),
		boarded("#2", "two", "In review", wipDay(4)),
		boarded("#3", "three", "In review", wipDay(4)),
	)
	east := time.FixedZone("east", 13*60*60)
	env.Clock = coretest.NewClock(time.Date(2025, 3, 4, 9, 0, 0, 0, east))

	result := liveResult(t, env, engine.CheckLiveWIP)

	evidenceMentions(t, result, "2025-03-03 — In review held 3")
	evidenceSilentAbout(t, result, "2025-03-04")
}

// TestAnExpeditedPullRequestIsOutsideTheLimits is spec 04 §8's exception, the
// same one `wip.review` makes. A team that labelled work as the agreed way
// past the limit is following its own process, and a report that told them
// off for it would be arguing with the configuration.
func TestAnExpeditedPullRequestIsOutsideTheLimits(t *testing.T) {
	jump := offered(3, "other", wipDay(1).AddDate(0, 0, -3), wipDay(2))
	jump.Labels = []string{"expedite"}
	prs := []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		offered(2, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		jump,
	}
	env := wipEnv(t, wipConfig(2, 0), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestWorkTheBoardCallsExpeditedIsOutsideTheLimitsToo is the other half of
// it. The label a team uses to expedite goes on the ticket as often as on the
// pull request, and the gate reads both — so the audit has to as well, or the
// same work is the exception on Monday and a finding on Tuesday.
func TestWorkTheBoardCallsExpeditedIsOutsideTheLimitsToo(t *testing.T) {
	prs := []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		offered(2, "dev", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
		offered(3, "other", wipDay(1).AddDate(0, 0, -3), wipDay(2)),
	}
	urgent := boarded("#103", "the urgent one", "Done", wipDay(2))
	urgent.Labels = []string{"expedite"}
	env := wipEnv(t, wipConfig(2, 0), prs, urgent)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestTheBoardsOwnExpeditedWorkIsOutsideTheLimitsToday is the exception on
// the one day flow reads rather than reconstructs. The gate drops expedited
// items before it counts a column, and a report that counted them would say
// the team was over a limit on the very day the gate was letting work
// through.
func TestTheBoardsOwnExpeditedWorkIsOutsideTheLimitsToday(t *testing.T) {
	urgent := boarded("#3", "three", "In review", wipDay(4))
	urgent.Labels = []string{"expedite"}
	env := wipEnv(t, wipConfig(2, 0), nil,
		boarded("#1", "one", "In review", wipDay(4)),
		boarded("#2", "two", "In review", wipDay(4)),
		urgent,
	)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestExpeditedWorkIsNotCountedAgainstThePersonCarryingItEither is the same
// exception on the other limit. Somebody who picked up the urgent thing has
// not broken their own cap by doing what the team asked of them.
func TestExpeditedWorkIsNotCountedAgainstThePersonCarryingItEither(t *testing.T) {
	ordinary := boarded("#1", "one", "In progress", wipDay(4))
	ordinary.Assignees = []string{"dev"}
	urgent := boarded("#2", "two", "In progress", wipDay(4))
	urgent.Assignees, urgent.Labels = []string{"dev"}, []string{"expedite"}
	env := wipEnv(t, wipConfig(0, 1), nil, ordinary, urgent)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestADraftIsWorkInProgressAndNotAQueueForReview is the column each pull
// request is counted in. A draft is somebody still writing it; nobody is
// waiting to review it, and counting it against the review limit would report
// a queue that does not exist.
func TestADraftIsWorkInProgressAndNotAQueueForReview(t *testing.T) {
	prs := []core.PullRequest{
		drafted(offered(1, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(2, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(3, "other", wipDay(1).AddDate(0, 0, -3), time.Time{})),
	}
	env := wipEnv(t, wipConfig(2, 0), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestOnePersonCarryingMoreThanTheyAgreedIsNamed is the per-person limit,
// which is about a person rather than a column: three people with one each is
// the team working, one person with three is somebody who cannot finish any
// of them.
func TestOnePersonCarryingMoreThanTheyAgreedIsNamed(t *testing.T) {
	prs := []core.PullRequest{
		drafted(offered(1, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(2, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(3, "other", wipDay(1).AddDate(0, 0, -3), time.Time{})),
	}
	env := wipEnv(t, wipConfig(0, 1), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 1, 4)
	evidenceMentions(t, result, "dev had 2 in In progress, over the limit of 1 each")
	evidenceSilentAbout(t, result, "other had")
}

// TestAPersonsOwnWorkIsCountedFromTheBoardToday is the per-person limit on
// the day it can be read. The board says who has what, which is the number
// the team agreed to cap — a pull request is somebody's work in progress only
// until somebody else picks the ticket up.
func TestAPersonsOwnWorkIsCountedFromTheBoardToday(t *testing.T) {
	first := boarded("#1", "one", "In progress", wipDay(4))
	first.Assignees = []string{"Dev"}
	second := boarded("#2", "two", "In progress", wipDay(4))
	second.Assignees = []string{"dev"}
	env := wipEnv(t, wipConfig(0, 1), nil, first, second)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 3, 4)
	evidenceMentions(t, result, "dev had 2 in In progress")
}

// TestAPullRequestCountsOnTheDayItOpenedAndTheDayItMerged is where the span
// begins and ends. A day it was in flight for an hour was still a day
// somebody was carrying it, at both ends — and a pull request nobody has
// finished is in flight on every day since.
func TestAPullRequestCountsOnTheDayItOpenedAndTheDayItMerged(t *testing.T) {
	prs := []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{}),
		offered(2, "other", wipDay(2).Add(14*time.Hour), wipDay(3)),
	}
	env := wipEnv(t, wipConfig(1, 0), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 2, 4)
	evidenceMentions(t, result, "2025-03-02")
	evidenceMentions(t, result, "2025-03-03")
	evidenceSilentAbout(t, result, "2025-03-01")
}

// TestTheReportSaysWhichLimitsItHeldTheTeamTo is what a passing run is worth.
// "All four days were inside the limits" means nothing without the numbers:
// a team reading its own report has to be able to see which limits were
// audited, or a limit quietly unset reads the same as a limit kept.
func TestTheReportSaysWhichLimitsItHeldTheTeamTo(t *testing.T) {
	env := wipEnv(t, wipConfig(2, 1), nil)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	messageMentions(t, result, "the limit of 2 on In review")
	messageMentions(t, result, "the limit of 1 each in In progress")
}

// TestAFortnightNobodyOpenedAnythingInIsADayCount is where this check parts
// company with the rest of the audit. Every other live check counts things
// that happened, so an empty window is nothing looked at; this one counts
// days, and a day nobody was carrying anything was a day inside the limits.
// Saying so is the point — a quiet window is the one shape of healthy the
// others cannot distinguish from silence.
func TestAFortnightNobodyOpenedAnythingInIsADayCount(t *testing.T) {
	env := wipEnv(t, wipConfig(2, 1), nil)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 4, 4)
}

// TestADayOverBothLimitsIsStillOneDay is the counting. The window is days,
// not breaches: a day on which the review queue and one person's work were
// both over is one day the team was carrying too much, and counting it twice
// would make a team look worse the more limits it set.
func TestADayOverBothLimitsIsStillOneDay(t *testing.T) {
	prs := []core.PullRequest{
		offered(1, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{}),
		offered(2, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{}),
		drafted(offered(3, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
		drafted(offered(4, "dev", wipDay(1).AddDate(0, 0, -3), time.Time{})),
	}
	env := wipEnv(t, wipConfig(1, 1), prs)

	result := liveResult(t, env, engine.CheckLiveWIP)

	wantCounts(t, result, 1, 4)
	evidenceMentions(t, result, "In review held 2, over its limit of 1")
	evidenceMentions(t, result, "dev had 2 in In progress, over the limit of 1 each")
}
