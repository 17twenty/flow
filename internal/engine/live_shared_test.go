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

// A board shared by several repositories (spec 03 §1, issue #89). flow runs in
// one repository and can only see that repository's pull requests, so a
// sibling's ticket reaching Done is indistinguishable from work that never
// went through the trunk. `tracker.shared_with` is how a team says which
// repositories are audited by the flow running there.

// sharedConfig is liveConfig with one sibling named.
func sharedConfig(siblings ...string) *config.Config {
	cfg := liveConfig()
	cfg.Tracker.SharedWith = siblings
	return cfg
}

// from is a ticket of another repository: the same board item, built the way
// the GitHub Projects adapter builds one for an issue that is not this
// repository's — a key that carries the repository, and the repository itself.
func from(repo, key, title, column string, updated time.Time) core.Ticket {
	ticket := boarded(key, title, column, updated)
	ticket.Repo = repo
	ticket.Key = repo + key
	return ticket
}

// TestASiblingsFinishedWorkIsNotThisRepositorysToAccountFor is the whole point
// of the key. The frontend's ticket merged in the frontend; this flow has no
// pull request for it and never will, and counting it would report a healthy
// team as unhealthy every day for ever.
func TestASiblingsFinishedWorkIsNotThisRepositorysToAccountFor(t *testing.T) {
	prs := []core.PullRequest{landedPR(4, "feat(api): add the thing", daysAgo(2))}
	env, _ := boardEnv(t, sharedConfig("acme/web"), coretest.ForgeFixture{PullRequests: prs},
		boarded(ticketFor(4), "the thing", "Done", daysAgo(2)),
		from("acme/web", "#7", "their thing", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	messageMentions(t, result, "not counting 1 ticket")
}

// TestATicketFromARepositoryNobodyNamedIsStillAFinding is the other half. An
// unnamed sibling is either a repository running no flow at all or a line
// somebody forgot to write, and flow saying nothing would hide both.
func TestATicketFromARepositoryNobodyNamedIsStillAFinding(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		from("acme/web", "#7", "their thing", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "acme/web#7")
}

// TestARepositoryIsNamedWhateverCaseItIsWrittenIn. GitHub answers a repository
// in the case it was created with and a person writes it in the case they
// remember; a team whose configuration said Acme/Web would otherwise have
// their siblings' work counted against them with nothing to show why.
func TestARepositoryIsNamedWhateverCaseItIsWrittenIn(t *testing.T) {
	env, _ := boardEnv(t, sharedConfig("ACME/Web"), coretest.ForgeFixture{},
		from("acme/web", "#7", "their thing", "Done", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	messageMentions(t, result, "not counting 1 ticket")
}

// TestWorkWaitingForReviewElsewhereIsNotWaitingHere. The review column is one
// board for both repositories, and the pull request offering a sibling's
// ticket for review is open in the sibling.
func TestWorkWaitingForReviewElsewhereIsNotWaitingHere(t *testing.T) {
	open := []core.PullRequest{inFlightPR(2, "feat(api): offered for review", daysAgo(1))}
	env, _ := boardEnv(t, sharedConfig("acme/web"), coretest.ForgeFixture{PullRequests: open},
		boarded(ticketFor(2), "ours", "In review", daysAgo(1)),
		from("acme/web", "#7", "theirs", "In review", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsReviewHasPR)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	messageMentions(t, result, "not counting 1 ticket")
}

// TestWorkRunningElsewhereIsNotThisDeploymentsToCarry is the same for
// `done_on: deployed`. The sibling's merge is in the sibling's trunk, so
// nothing this repository has deployed could ever contain it.
func TestWorkRunningElsewhereIsNotThisDeploymentsToCarry(t *testing.T) {
	cfg := sharedConfig("acme/web")
	cfg.Workflow.DoneOn = config.DoneOnDeployed
	cfg.Workflow.Transitions[config.EventDeployed] = config.Transition{Owner: config.OwnerFlow, To: "done"}
	cfg.Deploys = config.Deploys{Source: config.DeploySourceForge, Environment: "production"}

	prs := []core.PullRequest{landedPR(4, "feat(api): add the thing", daysAgo(2))}
	env, _ := boardEnv(t, cfg, coretest.ForgeFixture{PullRequests: prs},
		from("acme/web", "#7", "theirs", "Done", daysAgo(1)),
	)
	env.Adapters.Deploys = coretest.NewFakeDeploys(coretest.DeployFixture{
		Deployments: []core.Deployment{{
			ID: "d1", Environment: "production", SHA: prs[0].MergeSHA,
			State: core.DeploySuccess, CreatedAt: daysAgo(1), UpdatedAt: daysAgo(1),
		}},
	})

	result := liveResult(t, env, engine.CheckLiveTicketsDoneDeployed)
	wantState(t, result, engine.StatePass)
	messageMentions(t, result, "not counting 1 ticket")
}

// TestFlowDoesNotMoveAnotherRepositorysTicket is the one that matters under
// --fix. Every reason to leave a sibling's ticket where it is — an open pull
// request of theirs, a review they are waiting on — is invisible here, so a
// move flow made from this side would be a move made in ignorance.
func TestFlowDoesNotMoveAnotherRepositorysTicket(t *testing.T) {
	// A pull request of this repository's that closes a sibling's ticket:
	// the one way a flow running here can find a reason to move a ticket
	// it has no business moving. The sibling may have three more pull
	// requests open for the same ticket, and this run cannot see one of them.
	ours := inFlightPR(2, "feat(api): our half of their ticket", daysAgo(2))
	ours.ClosingIssues = []string{"acme/web#7"}
	theirs := from("acme/web", "#7", "theirs", "In progress", daysAgo(2))

	result, tracker := catchUp(t, sharedConfig("acme/web"), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{ours}}, theirs)

	if got := movesTo(t, tracker, theirs.Key); got != "In progress" {
		t.Errorf("flow moved a sibling's ticket to %q; it is not this flow's to move", got)
	}
	if got := tracker.Calls("Move"); got != 0 {
		t.Errorf("the tracker was asked to move something %d times, want 0", got)
	}
	wantState(t, result, engine.StatePass)
}

// TestOurOwnTicketIsStillCaughtUp is the control for it. A guard that stopped
// every move would look exactly the same in the test above.
func TestOurOwnTicketIsStillCaughtUp(t *testing.T) {
	ours := inFlightPR(2, "feat(api): ours", daysAgo(2))
	mine := boarded(ticketFor(2), "ours", "In progress", daysAgo(2))
	mine.Repo = "acme/api"

	_, tracker := catchUp(t, sharedConfig("acme/web"), true,
		coretest.ForgeFixture{PullRequests: []core.PullRequest{ours}}, mine)

	if got := movesTo(t, tracker, mine.Key); got != "In review" {
		t.Errorf("this repository's own ticket is in %q, want In review", got)
	}
}

// TestABoardOfOneRepositorysWorkSaysSo is live.tickets.shared_board on the
// ordinary repository: nothing to report, and it says so rather than counting
// a board it has nothing to say about.
func TestABoardOfOneRepositorysWorkSaysSo(t *testing.T) {
	// The ticket states its repository, as every GitHub ticket does. A
	// check that counted this repository's own work as foreign would
	// report the ordinary single-repository board as a finding.
	mine := boarded("#1", "ours", "Ready", daysAgo(1))
	mine.Repo = "acme/api"
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{}, mine)

	result := liveResult(t, env, engine.CheckLiveTicketsSharedBoard)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	messageMentions(t, result, "acme/api")
}

// TestTheBoardSaysWhichRepositoryNobodyAccountedFor is flow giving the
// instruction. The remediation has to name the repository it found: "add it to
// tracker.shared_with" is a sentence anybody could have written, and the
// repository's name is the part the team does not have.
func TestTheBoardSaysWhichRepositoryNobodyAccountedFor(t *testing.T) {
	mine := boarded("#1", "ours", "Ready", daysAgo(1))
	mine.Repo = "acme/api"
	env, _ := boardEnv(t, sharedConfig("acme/web"), coretest.ForgeFixture{},
		mine,
		from("acme/web", "#7", "named", "Ready", daysAgo(1)),
		from("acme/docs", "#9", "a stranger", "Ready", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsSharedBoard)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "acme/docs#9")
	wantStep(t, result, "add acme/docs to tracker.shared_with in .flow.yaml")
}

// TestOneUnnamedRepositoryIsEnoughToFail. A configuration gap is not a quality
// trend: nineteen accounted-for siblings do not make the twentieth any more
// watched, and a percentage would let it pass.
func TestOneUnnamedRepositoryIsEnoughToFail(t *testing.T) {
	named := []string{}
	board := []core.Ticket{}
	for i := 1; i <= 19; i++ {
		repo := "acme/sibling" + string(rune('a'+i-1))
		named = append(named, repo)
		board = append(board, from(repo, "#1", "theirs", "Ready", daysAgo(1)))
	}
	board = append(board, from("acme/docs", "#9", "a stranger", "Ready", daysAgo(1)))

	env, _ := boardEnv(t, sharedConfig(named...), coretest.ForgeFixture{}, board...)

	result := liveResult(t, env, engine.CheckLiveTicketsSharedBoard)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 19, 20)
}

// TestEveryStrangerOnTheBoardIsNamedOnceAndInOrder. Two runs of the same board
// must give the same instruction, and a repository with thirty tickets on the
// board is still one line to write.
func TestEveryStrangerOnTheBoardIsNamedOnceAndInOrder(t *testing.T) {
	env, _ := boardEnv(t, liveConfig(), coretest.ForgeFixture{},
		from("acme/web", "#7", "theirs", "Ready", daysAgo(1)),
		from("acme/docs", "#9", "theirs too", "Ready", daysAgo(1)),
		from("acme/web", "#8", "theirs again", "Ready", daysAgo(1)),
	)

	result := liveResult(t, env, engine.CheckLiveTicketsSharedBoard)
	var added []string
	for _, step := range result.Remediation.Steps {
		if name, ok := strings.CutPrefix(step, "add "); ok {
			added = append(added, strings.TrimSuffix(name, " to tracker.shared_with in .flow.yaml"))
		}
	}
	want := []string{"acme/docs", "acme/web"}
	if len(added) != len(want) || added[0] != want[0] || added[1] != want[1] {
		t.Errorf("the instruction names %v, want %v exactly once each and in order", added, want)
	}
}

// messageMentions is for the part of an answer the counts cannot carry: how
// much of the board belonged to somebody else, which is the difference between
// a quiet fortnight and a board this flow is not the one watching.
func messageMentions(t *testing.T, got engine.Result, want string) {
	t.Helper()
	if !strings.Contains(got.Message, want) {
		t.Errorf("the message is %q, which does not mention %q", got.Message, want)
	}
}

// wantStep asserts one line of the remediation.
func wantStep(t *testing.T, got engine.Result, want string) {
	t.Helper()
	for _, step := range got.Remediation.Steps {
		if step == want {
			return
		}
	}
	t.Errorf("no step says %q: %v", want, got.Remediation.Steps)
}

// TestAMisconfiguredSiblingListNeverSilencesThisRepositorysOwnWork. The
// configuration is refused by `flow doctor`'s config.semantics check, but the
// live checks do not wait on it: a run with a bad `shared_with` must still
// report what it found, because a doctor that audits nothing and says it is
// green is worse than one that refuses to start.
func TestAMisconfiguredSiblingListNeverSilencesThisRepositorysOwnWork(t *testing.T) {
	mine := boarded("#900", "moved by hand", "Done", daysAgo(1))
	mine.Repo = "acme/api"

	for _, wrong := range [][]string{{"acme/api"}, {""}} {
		cfg := sharedConfig(wrong...)
		ours := mine
		if wrong[0] == "" {
			// A tracker that records no repository is the other way to
			// reach it: an empty name must match no ticket, not every
			// ticket Jira and Linear answer with.
			ours.Repo = ""
		}
		env, _ := boardEnv(t, cfg, coretest.ForgeFixture{}, ours)

		result := liveResult(t, env, engine.CheckLiveTicketsDoneMerged)
		if result.Counts == nil || result.Counts.Total != 1 {
			t.Errorf("shared_with %q counted %s; this repository's own work is not somebody else's",
				wrong, result.Counts)
		}
	}
}
