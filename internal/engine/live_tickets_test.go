package engine_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The board half of the live-health audit (spec 07 §6). The pull-request
// checks ask the forge whether what landed was done the way the team agreed;
// these ask the board the same question from the other side.

// boarded is a ticket sitting in a column, named by the column's display name
// because that is what [engine.ColumnOf] matches on: the tracker's own column
// id is its own business, and the lock is what maps it back.
func boarded(key, title, column string, updated time.Time) core.Ticket {
	return core.Ticket{
		Key:       key,
		Title:     title,
		Body:      "## Acceptance criteria\n\n- it works\n",
		Status:    core.Status{ID: strings.ToLower(column), Name: column},
		ColumnID:  core.ColumnID(strings.ToLower(column)),
		UpdatedAt: updated,
		URL:       fmt.Sprintf("https://github.com/acme/api/issues/%s", strings.TrimPrefix(key, "#")),
	}
}

// ticketFor is the key landedPR gives the ticket its pull request closes.
func ticketFor(pr int) string { return fmt.Sprintf("#%d", 100+pr) }

// boardEnv is liveEnv with a board on it, and hands back the tracker so a
// test can count what was asked of it.
func boardEnv(t *testing.T, cfg *config.Config, fx coretest.ForgeFixture, tickets ...core.Ticket) (*engine.Env, *coretest.FakeTracker) {
	t.Helper()
	env, _ := liveEnv(t, cfg, fx)
	tracker := coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: tickets})
	env.Adapters.Tracker = tracker
	return env, tracker
}

// inFlightPR is a pull request still open for the ticket of the same number.
func inFlightPR(number int, title string, updated time.Time) core.PullRequest {
	pr := landedPR(number, title, updated)
	pr.State = core.PRStateOpen
	pr.MergedAt, pr.MergeSHA = time.Time{}, ""
	return pr
}

// TestTheBoardIsReadOnceHoweverManyChecksAskAboutIt is the ticket half of
// reading once. Five checks each pulling the board would be five round trips
// for one answer, and on a tracker that pages that is five walks of it.
func TestTheBoardIsReadOnceHoweverManyChecksAskAboutIt(t *testing.T) {
	env, tracker := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#1", "something ready", "Ready", daysAgo(1)),
		boarded("#2", "something in flight", "In progress", daysAgo(1)),
	)

	liveRun(t, env, nil)

	if got := tracker.Calls("Tickets"); got != 1 {
		t.Errorf("the board was read %d times, want 1", got)
	}
}

// TestATicketThatReachedDoneWithNoMergeIsNamed is live.tickets.done_merged
// doing its job. Work that arrived in Done without going through the trunk is
// either a hand on the board or a change nobody reviewed.
func TestATicketThatReachedDoneWithNoMergeIsNamed(t *testing.T) {
	prs := []core.PullRequest{landedPR(4, "feat(api): add the thing", daysAgo(2))}
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs},
		boarded(ticketFor(4), "the thing", "Done", daysAgo(2)),
		boarded("#900", "moved by hand", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#900")
}

// TestWorkThatWasDroppedIsNotWorkThatWentMissing is spec §6's exemption. A
// ticket closed as not planned is expected to have no merge behind it: that
// is what abandoning it means, and counting it would push a team towards
// leaving dead work open to keep the number up.
func TestWorkThatWasDroppedIsNotWorkThatWentMissing(t *testing.T) {
	dropped := boarded("#901", "not doing this after all", "Done", daysAgo(1))
	dropped.Closed, dropped.CloseReason = true, core.ClosedNotPlanned
	dropped.ClosedAt = daysAgo(1)
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, dropped)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestFlowsOwnTicketsAreNotDeliveredWork is issue #203. A follow-up flow
// verified and closed is settings work that never has a pull request, and the
// health issue is flow's report; neither is counted. A flag cleanup ticket is
// flow's writing too, but removing a flag is a change to the code, so it is.
func TestFlowsOwnTicketsAreNotDeliveredWork(t *testing.T) {
	followUp := boarded("#1", "flow: The trunk is protected", "Done", daysAgo(1))
	followUp.Body = core.MarkBody("set it", engine.FollowUpMarker("forge.protection", "acme/api"), true)
	health := boarded("#2", "flow health: acme/api", "Done", daysAgo(1))
	health.Body = core.MarkBody("report", engine.HealthMarker("acme/api"), true)
	flag := boarded("#3", "remove the new-checkout flag", "Done", daysAgo(1))
	flag.Body = core.MarkBody("remove it", core.NewMarker(core.MarkerFlag, "flag", "new-checkout"), true)
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, followUp, health, flag)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "#3")
	if !strings.Contains(result.Message, "not counting 2 of flow's own tickets") {
		t.Errorf("the result does not say what it left out: %q", result.Message)
	}
}

// TestTheDoneWindowIsAboutWhenTheWorkFinished: a Done column holds everything
// the team has ever finished, and an audit that judged all of it would report
// a year-old ticket whose pull request is long outside the window as having no
// work behind it. The close date is the date the audit is about.
func TestTheDoneWindowIsAboutWhenTheWorkFinished(t *testing.T) {
	old := boarded("#902", "finished last quarter", "Done", liveNow.AddDate(0, 0, -90))
	old.Closed, old.ClosedAt = true, liveNow.AddDate(0, 0, -90)
	recent := boarded("#903", "finished by hand yesterday", "Done", daysAgo(1))
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, old, recent)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "#903")
}

// TestATicketStillOpenInDoneIsJudgedByItsLastEdit: a board column is a view,
// not the issue's state, so Done holds items the tracker still calls open and
// those have no close date at all. Judging them by the last edit is the only
// thing there is; not judging them would let the whole check be dodged by
// leaving everything open.
func TestATicketStillOpenInDoneIsJudgedByItsLastEdit(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#904", "done but not closed", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestATicketWaitingForReviewMustHaveSomethingToReview is
// live.tickets.review_has_pr. A column asking for somebody's attention should
// say what to look at.
func TestATicketWaitingForReviewMustHaveSomethingToReview(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{inFlightPR(6, "feat(api): the reviewable one", daysAgo(1))},
	},
		boarded(ticketFor(6), "has a pull request", "In review", daysAgo(1)),
		boarded("#905", "waiting on nothing", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewHasPR)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "nothing open is for it")
}

// TestADraftIsNotSomethingToReview: marking a pull request ready for review is
// the act the column is named after, so a ticket sitting there behind a draft
// is waiting for its own author.
func TestADraftIsNotSomethingToReview(t *testing.T) {
	draft := inFlightPR(7, "feat(api): still working on it", daysAgo(1))
	draft.Draft = true
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{draft},
	},
		boarded(ticketFor(7), "behind a draft", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewHasPR)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "still a draft")
}

// TestAMergedPullRequestIsNotSomethingToReview: the review check reads what is
// open, so a ticket left in review after its work merged is reported. The
// transition that should have moved it is somebody else's check, but a board
// that still says "in review" is wrong either way.
func TestAMergedPullRequestIsNotSomethingToReview(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(8, "feat(api): already landed", daysAgo(1))},
	},
		boarded(ticketFor(8), "left behind", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewHasPR)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestWorkThatHasGoneQuietIsOnlyEverAWarning is spec §6's "(warn only)".
// Everything in progress has stopped moving, which is as bad as this check can
// see — and it still must not fail the run, because work goes quiet for
// reasons flow cannot see and a doctor that goes red for them teaches the team
// to ignore it.
func TestWorkThatHasGoneQuietIsOnlyEverAWarning(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#906", "forgotten", "In progress", liveNow.AddDate(0, 0, -30)),
		boarded("#907", "also forgotten", "In progress", liveNow.AddDate(0, 0, -30)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsProgressStale)
	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 0, 2)
	evidenceMentions(t, result, "#906")
}

// TestAPullRequestMovingCountsAsTheTicketMoving: the ticket itself is often
// untouched for a week while its pull request is reviewed twice a day. Reading
// only the tracker's own timestamp would report the most active work on the
// board as stale.
func TestAPullRequestMovingCountsAsTheTicketMoving(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{inFlightPR(9, "feat(api): busy", daysAgo(1))},
	},
		boarded(ticketFor(9), "quiet ticket, busy branch", "In progress", liveNow.AddDate(0, 0, -30)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsProgressStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestFiveWorkingDaysIsAWeekAndAWeekendOfQuiet: the bound spec §6 states is in
// working days, so work last touched on the Friday before last is stale on a
// Tuesday while work touched a week ago on the calendar is not.
func TestFiveWorkingDaysIsAWeekAndAWeekendOfQuiet(t *testing.T) {
	// liveNow is Tuesday 4 March 2025. Five working days back is Tuesday the
	// 25th of February; seven calendar days back is only the 25th as well —
	// so the weekend is what the two disagree about.
	justInside := boarded("#908", "touched a week ago", "In progress", liveNow.AddDate(0, 0, -7))
	outside := boarded("#909", "touched eight days ago", "In progress", liveNow.AddDate(0, 0, -8))
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, justInside, outside)

	result := liveResult(t, env, engine.CheckLiveTicketsProgressStale)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#909")
}

// TestATicketCalledReadyMustBeReady is live.tickets.ready_dor. The gate
// refuses a pull request for a ticket that is not ready; a board full of work
// that will be refused when somebody picks it up is the same failure found a
// day earlier.
func TestATicketCalledReadyMustBeReady(t *testing.T) {
	bare := boarded("#910", "no acceptance criteria", "Ready", daysAgo(1))
	bare.Body = "we should probably do this"
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#911", "properly written up", "Ready", daysAgo(1)),
		bare,
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReadyDoR)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#910")
}

// TestTheReadyColumnIsFoundByItsGateNotItsName: the Definition of Ready is
// what makes a column the ready one. A team that renamed theirs has not
// stopped meaning it, and matching on the id "ready" would quietly stop
// auditing them.
func TestTheReadyColumnIsFoundByItsGateNotItsName(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Columns[1] = config.Column{
		ID:       "up_next",
		Name:     "Up next",
		Category: config.CategoryTodo,
		Gate:     config.GateDefinitionOfReady,
	}
	bare := boarded("#912", "no acceptance criteria", "Up next", daysAgo(1))
	bare.Body = "we should probably do this"
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{}, bare)

	result := liveResult(t, env, engine.CheckLiveTicketsReadyDoR)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestABoardWithNoReadyGateIsNotAudited: a team that gates no column on the
// Definition of Ready has not asked this question of their board, and flow
// does not hold work up for a question nobody asked.
func TestABoardWithNoReadyGateIsNotAudited(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Columns[1].Gate = ""
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
		boarded("#913", "whatever this is", "Ready", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReadyDoR)
	wantState(t, result, engine.StateSkip)
}

// TestAColumnTheWorkflowDoesNotNameIsNotAudited: every ticket check finds its
// column through the transition that lands work there, so a workflow that
// never says where review happens has no review column to audit. Skipping is
// the honest answer; judging the whole board instead would report every
// ticket on it as missing a pull request.
func TestAColumnTheWorkflowDoesNotNameIsNotAudited(t *testing.T) {
	cfg := liveConfig()
	delete(cfg.Workflow.Transitions, config.EventPRReadyForReview)
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
		boarded("#914", "waiting on nothing", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewHasPR)
	wantState(t, result, engine.StateSkip)
}

// TestWhereDoneIsDeployedTheBoardIsAuditedAgainstWhatIsRunning is
// live.tickets.done_deployed. A team whose done means deployed is making a
// claim about production, and the deployment record is the only thing that
// can settle it.
func TestWhereDoneIsDeployedTheBoardIsAuditedAgainstWhatIsRunning(t *testing.T) {
	cfg := deployedDoneConfig()
	shipped := landedPR(2, "feat(api): shipped", daysAgo(3))
	waiting := landedPR(3, "feat(api): merged but not out", daysAgo(2))
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{shipped, waiting},
		Comparisons: map[string]core.Comparison{
			shipped.MergeSHA + "...live001": {Status: core.CompareAhead},
			waiting.MergeSHA + "...live001": {Status: core.CompareDiverged},
		},
	},
		boarded(ticketFor(2), "shipped", "Done", daysAgo(3)),
		boarded(ticketFor(3), "not out yet", "Done", daysAgo(2)),
	)
	env.Adapters.Deploys = coretest.NewFakeDeploys(coretest.DeployFixture{
		Deployments: []core.Deployment{{
			Environment: "production",
			SHA:         "live001",
			State:       core.DeploySuccess,
			CreatedAt:   daysAgo(1),
		}},
	})

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, ticketFor(3))
}

// TestATeamWhoseWorkIsDoneWhenItMergesIsNotAskedAboutDeployments: the check
// exists for `done_on: deployed` only. Asking it of everybody would report
// every repository without deployments as unhealthy for not having them.
func TestATeamWhoseWorkIsDoneWhenItMergesIsNotAskedAboutDeployments(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#915", "finished", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StateSkip)
}

// TestAWorkflowThatDeploysNothingCannotFinishAnything: `done_on: deployed`
// with no successful deployment on record is not a quiet fortnight. It is a
// team whose definition of done can never be met, and a pass would be flow
// agreeing that work nobody shipped is finished.
func TestAWorkflowThatDeploysNothingCannotFinishAnything(t *testing.T) {
	env, _ := boardEnv(t, deployedDoneConfig(), coretest.ForgeFixture{},
		boarded("#916", "called done", "Done", daysAgo(1)),
	)
	env.Adapters.Deploys = coretest.NewFakeDeploys(coretest.DeployFixture{})

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Message, "production") {
		t.Errorf("the message is %q, and does not say which environment", result.Message)
	}
}

// deployedDoneConfig is a team whose work is not done until it is running.
func deployedDoneConfig() *config.Config {
	cfg := liveConfig()
	cfg.Workflow.DoneOn = config.DoneOnDeployed
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{
		Owner: config.OwnerFlow,
		To:    "done",
	}
	cfg.Deploys = config.Deploys{Environment: "production"}
	return cfg
}

// TestATicketIsRunningOnlyWhenAllOfItsWorkIs is spec 04 §5 rule 4 read from
// the board: a ticket big enough to need two pull requests is not finished
// when the first of them ships. Judging it by any one merge would call work
// done while half of it is still on the trunk.
func TestATicketIsRunningOnlyWhenAllOfItsWorkIs(t *testing.T) {
	shipped := landedPR(2, "feat(api): the first half", daysAgo(3))
	waiting := landedPR(3, "feat(api): the second half", daysAgo(2))
	waiting.ClosingIssues = shipped.ClosingIssues
	env, _ := boardEnv(t, deployedDoneConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{shipped, waiting},
		Comparisons: map[string]core.Comparison{
			shipped.MergeSHA + "...live001": {Status: core.CompareAhead},
			waiting.MergeSHA + "...live001": {Status: core.CompareDiverged},
		},
	},
		boarded(ticketFor(2), "both halves", "Done", daysAgo(2)),
	)
	env.Adapters.Deploys = runningDeploys()

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "1 of its 2 merges")
}

// TestATicketWithNoMergeAtAllIsOneMistakeNotTwo: whether a ticket in Done has
// any work behind it is done_merged's question. Counting it here as well
// would report one hand on the board as two separate failures, and a team
// fixing it would see only half the number move.
func TestATicketWithNoMergeAtAllIsOneMistakeNotTwo(t *testing.T) {
	shipped := landedPR(2, "feat(api): shipped", daysAgo(3))
	env, _ := boardEnv(t, deployedDoneConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{shipped},
		Comparisons: map[string]core.Comparison{
			shipped.MergeSHA + "...live001": {Status: core.CompareAhead},
		},
	},
		boarded(ticketFor(2), "shipped", "Done", daysAgo(3)),
		boarded("#917", "moved by hand", "Done", daysAgo(1)),
	)
	env.Adapters.Deploys = runningDeploys()

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestWhereDoneMeansDeployedTheColumnIsWhereDeploymentLandsWork: a team whose
// board has both a Done and a Released column finishes work in the one their
// `deployed` transition names. Auditing the merge column instead would ask
// the question of tickets that have not reached the end of the board.
func TestWhereDoneMeansDeployedTheColumnIsWhereDeploymentLandsWork(t *testing.T) {
	cfg := deployedDoneConfig()
	cfg.Workflow.Columns = append(cfg.Workflow.Columns,
		config.Column{ID: "released", Name: "Released", Category: config.CategoryDone})
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{
		Owner: config.OwnerFlow,
		To:    "released",
	}
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{},
		boarded("#918", "merged, waiting to ship", "Done", daysAgo(1)),
		boarded("#919", "out, with nothing behind it", "Released", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "#919")
}

// TestATrackerThatWillNotSayWhenAnItemMovedIsNotAskedWhetherItStopped: not
// every tracker records an update time, and reading a missing one as the zero
// date would report every ticket on such a board as years stale. The honest
// answer is that there is nothing to judge, and the message says so rather
// than claiming the column is empty.
func TestATrackerThatWillNotSayWhenAnItemMovedIsNotAskedWhetherItStopped(t *testing.T) {
	undated := boarded("#920", "a board that keeps no dates", "In progress", time.Time{})
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, undated)

	result := liveResult(t, env, engine.CheckLiveTicketsProgressStale)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if !strings.Contains(result.Message, "date to judge") {
		t.Errorf("the message is %q, which reads as an empty column rather than an unanswerable question",
			result.Message)
	}
}

// TestOneSpellingOfAKeyIsEnough: a tracker and a forge disagree about the case
// of the tracker's own keys — Jira writes PROJ-12 in the board and accepts
// proj-12 in a branch name — and an index that did not fold them would report
// a ticket with a merge behind it as having none.
func TestOneSpellingOfAKeyIsEnough(t *testing.T) {
	pr := landedPR(5, "feat(api): the work", daysAgo(2))
	pr.ClosingIssues = []string{"proj-12"}
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: []core.PullRequest{pr}},
		boarded("PROJ-12", "written the other way up", "Done", daysAgo(2)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestTheBoardIsNamedInOneOrderHoweverTheTrackerAnswers is the ticket half of
// the report being stable. A tracker answers in its own order — newest first,
// by position on the board — and item 5.7 edits this evidence into an issue in
// place, where a reshuffle between runs is noise in every diff of it.
func TestTheBoardIsNamedInOneOrderHoweverTheTrackerAnswers(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		boarded("#923", "third on the board", "Done", daysAgo(1)),
		boarded("#921", "first on the board", "Done", daysAgo(1)),
		boarded("#922", "second on the board", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantCounts(t, result, 0, 3)
	var named []string
	for _, seen := range result.Evidence {
		named = append(named, strings.Fields(seen.Text)[0])
	}
	if want := []string{"#921", "#922", "#923"}; !slices.Equal(named, want) {
		t.Errorf("the evidence names %v, want %v", named, want)
	}
}

// runningDeploys is one successful deployment of live001 to production.
func runningDeploys() *coretest.FakeDeploys {
	return coretest.NewFakeDeploys(coretest.DeployFixture{
		Deployments: []core.Deployment{{
			Environment: "production",
			SHA:         "live001",
			State:       core.DeploySuccess,
			CreatedAt:   daysAgo(1),
		}},
	})
}
