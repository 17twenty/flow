package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// sentBack is a pull request somebody asked for changes on and then approved,
// which is what rework looks like by the time it has merged.
func sentBack(number int, title string, days int) core.PullRequest {
	pr := landedPR(number, title, daysAgo(days))
	pr.Author = "flow-bot"
	pr.Reviews = []core.Review{
		{Author: "dev", State: core.ReviewChangesRequested, SubmittedAt: daysAgo(days + 1)},
		{Author: "dev", State: core.ReviewApproved, SubmittedAt: daysAgo(days)},
	}
	return pr
}

// landedFirstTime is agent work nobody sent back.
func landedFirstTime(number int, title string, days int) core.PullRequest {
	pr := landedPR(number, title, daysAgo(days))
	pr.Author = "flow-bot"
	pr.Reviews = []core.Review{{Author: "dev", State: core.ReviewApproved, SubmittedAt: daysAgo(days)}}
	return pr
}

// TestReworkIsCountedFromWhatHappenedNotFromWhatIsStillAsked: every merged
// pull request has been approved by the time it lands, so the question
// core.PullRequest.ChangesRequested answers — is anybody asking for changes
// now — is false of all of them. Rework is history.
func TestReworkIsCountedFromWhatHappenedNotFromWhatIsStillAsked(t *testing.T) {
	rework := sentBack(3, "feat(api): the hard one", 3)
	clean := landedFirstTime(4, "feat(api): the easy one", 2)
	if rework.ChangesRequested() {
		t.Fatal("the fixture still asks for changes, so it does not test history")
	}

	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{rework, clean},
	})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#3")
	if !strings.Contains(result.Message, "50%") {
		t.Fatalf("the share is not in the message: %q", result.Message)
	}
	if !strings.Contains(result.Message, "2 pull requests") {
		t.Fatalf("the throughput is not in the message, so the share has no denominator: %q", result.Message)
	}
}

// TestAMeasurementIsNeverAVerdict: there is no share of rework that is wrong,
// so this one cannot fail a run however bad it looks. A threshold nobody can
// justify is a number flow made up.
func TestAMeasurementIsNeverAVerdict(t *testing.T) {
	var prs []core.PullRequest
	for number := 1; number <= 10; number++ {
		prs = append(prs, sentBack(number, "feat(api): all of it came back", 1))
	}

	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 10)
	if !strings.Contains(result.Message, "100%") {
		t.Fatalf("the share is not in the message: %q", result.Message)
	}
}

// TestWorkAPersonWroteIsNobodysReworkRate: the measurement is about how the
// team uses a machine, so a person's pull request is not in the denominator
// however much it came back.
func TestWorkAPersonWroteIsNobodysReworkRate(t *testing.T) {
	theirs := sentBack(3, "feat(api): a person wrote this", 3)
	theirs.Author = "dev"
	ours := landedFirstTime(4, "feat(api): a machine wrote this", 2)

	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{theirs, ours},
	})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantCounts(t, result, 1, 1)
	if !strings.Contains(result.Message, "none of them sent back") {
		t.Fatalf("a person's rework was counted against the machine: %q", result.Message)
	}
}

// TestNobodyAskedAMachineForAnythingIsNotAPerfectScore: a share of nothing
// reads as a team doing well. What happened is that there was no agent work
// in the window at all, and the report has to say that instead.
func TestNobodyAskedAMachineForAnythingIsNotAPerfectScore(t *testing.T) {
	theirs := landedPR(3, "feat(api): a person wrote this", daysAgo(3))

	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{theirs},
	})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if strings.Contains(result.Message, "%") {
		t.Fatalf("a share of nothing was reported as a share: %q", result.Message)
	}
	if !strings.Contains(result.Message, "nothing a machine wrote merged") {
		t.Fatalf("the report does not say the window was empty of agent work: %q", result.Message)
	}
}

// TestATeamWithNoMachineInItIsNotMeasuredForOne keeps the check to the teams
// it is about: there is nothing to count, and 0/0 beside a team of people
// would read as a reproach.
func TestATeamWithNoMachineInItIsNotMeasuredForOne(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.Authors = nil
	cfg.Agents.Labels = nil

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(3, "feat(api): a person wrote this", daysAgo(3))},
	})

	wantState(t, liveResult(t, env, engine.CheckLiveAgentsRework), engine.StateSkip)
}

// TestAMachineKnownByItsLabelIsMeasuredToo keeps every reading of "agent
// work" in the audit and the gate the same one.
func TestAMachineKnownByItsLabelIsMeasuredToo(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.Authors = nil
	cfg.Agents.Labels = []string{"agent"}
	labelled := sentBack(3, "feat(api): a machine wrote this", 3)
	labelled.Author = "dev"
	labelled.Labels = []string{"agent"}

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{PullRequests: []core.PullRequest{labelled}})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantCounts(t, result, 0, 1)
}

// TestTheShareIsRoundedTheWayAPersonWouldRoundIt: two of three is 67%, not
// 66%. The number goes into a report somebody reads over a fortnight, and a
// share that truncates always reads better than the truth.
func TestTheShareIsRoundedTheWayAPersonWouldRoundIt(t *testing.T) {
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			sentBack(3, "feat(api): one", 3),
			sentBack(4, "feat(api): two", 2),
			landedFirstTime(5, "feat(api): three", 1),
		},
	})

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	if !strings.Contains(result.Message, "67%") {
		t.Fatalf("two of three is not 67%%: %q", result.Message)
	}
}

// TestAForgeThatWouldNotAnswerIsNotAQuietFortnight: a measurement nobody can
// fail is still a measurement, and a run that could not read the pull
// requests has not measured anything. Reporting "nothing a machine wrote
// merged" from a refusal would put a number in the health issue that no
// window supports.
func TestAForgeThatWouldNotAnswerIsNotAQuietFortnight(t *testing.T) {
	env, forge := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{sentBack(3, "feat(api): the hard one", 3)},
	})
	forge.Fail("PullRequests", errors.New("the credential was refused"))

	result := liveResult(t, env, engine.CheckLiveAgentsRework)
	wantState(t, result, engine.StateError)
	if strings.Contains(result.Message, "nothing a machine wrote merged") {
		t.Fatalf("a refusal was reported as an empty window: %q", result.Message)
	}
}
