package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// live.trunk.built (spec 20 §6). A merge nothing ran on is a merge whose
// events were lost or whose pipeline is broken, and the finding is the same
// on every forge.

// checkRunForge is the reference forge with GitHub's other way of reporting
// CI: check runs, which a reader of statuses alone never sees.
type checkRunForge struct {
	*coretest.FakeForge
	runs map[string][]core.CommitStatus
}

func (f checkRunForge) CheckRuns(_ context.Context, sha string) ([]core.CommitStatus, error) {
	return f.runs[sha], nil
}

// trunkResult runs the check with the build the configuration requires.
func trunkResult(t *testing.T, fx coretest.ForgeFixture, wrap func(*coretest.FakeForge) core.Forge, autoMerge bool) engine.Result {
	t.Helper()
	cfg := liveConfig()
	cfg.Forge.RequiredChecks = []string{"test"}
	cfg.Forge.AutoMerge = autoMerge
	env, forge := liveEnv(t, cfg, fx)
	if wrap != nil {
		env.Adapters.Forge = wrap(forge)
	}
	return liveResult(t, env, engine.CheckLiveTrunkBuilt)
}

func TestAMergeNothingBuiltIsNamed(t *testing.T) {
	built, silent := landedPR(1, "fix(api): built", daysAgo(2)), landedPR(2, "fix(api): silent", daysAgo(1))
	result := trunkResult(t, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{built, silent},
		Statuses:     map[string][]core.CommitStatus{built.MergeSHA: {{Context: "test", State: core.StatusSuccess}}},
	}, nil, false)

	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#2")
}

// A build that ran and failed is still a build that ran: a red trunk is the
// build's own news, and this check is about whether anything started.
func TestAFailedBuildIsStillABuild(t *testing.T) {
	pr := landedPR(1, "fix(api): red", daysAgo(1))
	result := trunkResult(t, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{pr},
		Statuses:     map[string][]core.CommitStatus{pr.MergeSHA: {{Context: "test", State: core.StatusFailure}}},
	}, nil, false)

	wantState(t, result, engine.StatePass)
}

// GitHub Actions reports as check runs, so a forge that has them is asked for
// them as well as for statuses.
func TestABuildReportedAsACheckRunCounts(t *testing.T) {
	pr := landedPR(1, "fix(api): built by actions", daysAgo(1))
	result := trunkResult(t, coretest.ForgeFixture{PullRequests: []core.PullRequest{pr}},
		func(f *coretest.FakeForge) core.Forge {
			return checkRunForge{FakeForge: f, runs: map[string][]core.CommitStatus{pr.MergeSHA: {{Context: "test", State: core.StatusSuccess}}}}
		}, false)

	wantState(t, result, engine.StatePass)
}

// A merge from a minute ago may still be building.
func TestARecentMergeIsWaitedFor(t *testing.T) {
	pr := landedPR(1, "fix(api): just now", liveNow.Add(-time.Minute))
	result := trunkResult(t, coretest.ForgeFixture{PullRequests: []core.PullRequest{pr}}, nil, false)

	wantCounts(t, result, 0, 0)
	if !strings.Contains(result.Message, "too recently") {
		t.Errorf("message = %q, want it to say a merge is being waited for", result.Message)
	}
}

func TestNoRequiredBuildIsNothingToCheck(t *testing.T) {
	cfg := liveConfig()
	cfg.Forge.RequiredChecks = nil
	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{PullRequests: []core.PullRequest{landedPR(1, "fix(api): x", daysAgo(1))}})

	wantState(t, liveResult(t, env, engine.CheckLiveTrunkBuilt), engine.StateSkip)
}

// Where the forge lands pull requests itself, what the forge declares about
// that is the likely reason, and the remedy says it.
func TestTheRemedyCarriesWhatTheForgeSaysAboutAutoMerge(t *testing.T) {
	pr := landedPR(2, "fix(api): silent", daysAgo(1))
	fx := coretest.ForgeFixture{PullRequests: []core.PullRequest{pr}}
	fx.Capabilities = testRegistry(t)["github"]

	result := trunkResult(t, fx, nil, true)

	wantState(t, result, engine.StateFail)
	if steps := strings.Join(result.Remediation.Steps, "\n"); !strings.Contains(steps, "Actions token") {
		t.Errorf("the remedy does not carry the forge's hazard about auto-merge:\n%s", steps)
	}
}
