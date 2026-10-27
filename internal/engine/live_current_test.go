package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// live.prs.current (issue #234). A trunk that requires branches to be up to
// date makes every open pull request fall behind each time one merges; flow
// brings up to date exactly the ones whose only obstacle that is.

// updatingForge is the reference forge with the "Update branch" button.
type updatingForge struct {
	*coretest.FakeForge
	updated map[int]string
}

func (f *updatingForge) UpdateBranch(_ context.Context, number int, headSHA string) error {
	f.updated[number] = headSHA
	return nil
}

// readyAndBehind is an open pull request the gate passed and the base has
// moved past.
func readyAndBehind(number int, title string) core.PullRequest {
	pr := inFlightPR(number, title, daysAgo(1))
	pr.Mergeability = core.MergeabilityBehind
	return pr
}

func passed(prs ...core.PullRequest) map[string][]core.CommitStatus {
	statuses := map[string][]core.CommitStatus{}
	for _, pr := range prs {
		statuses[pr.HeadSHA] = []core.CommitStatus{{Context: engine.DefaultStatusContext, State: core.StatusSuccess}}
	}
	return statuses
}

func currentResult(t *testing.T, fix bool, updates bool, fx coretest.ForgeFixture) (engine.Result, *updatingForge) {
	t.Helper()
	env, forge := liveEnv(t, liveConfig(), fx)
	env.Fix = fix
	wrapped := &updatingForge{FakeForge: forge, updated: map[int]string{}}
	if updates {
		env.Adapters.Forge = wrapped
	}
	return liveResult(t, env, engine.CheckLivePRsCurrent), wrapped
}

func TestAPullRequestReadyButBehindIsNamed(t *testing.T) {
	ready := readyAndBehind(1, "feat(api): ready")
	result, forge := currentResult(t, false, true, coretest.ForgeFixture{PullRequests: []core.PullRequest{ready}, Statuses: passed(ready)})

	wantState(t, result, engine.StateFail)
	evidenceMentions(t, result, "#1")
	if result.Remediation.Command != "flow sync" {
		t.Errorf("the remedy is %+v, want flow sync: flow can do this itself", result.Remediation)
	}
	if len(forge.updated) != 0 {
		t.Errorf("a run without --fix updated %v", forge.updated)
	}
}

func TestFixBringsItUpToDateAtTheHeadItJudged(t *testing.T) {
	ready := readyAndBehind(1, "feat(api): ready")
	result, forge := currentResult(t, true, true, coretest.ForgeFixture{PullRequests: []core.PullRequest{ready}, Statuses: passed(ready)})

	wantState(t, result, engine.StatePass)
	if got := forge.updated[1]; got != ready.HeadSHA {
		t.Errorf("#1 was updated at %q, want the head the gate judged, %q", got, ready.HeadSHA)
	}
	evidenceMentions(t, result, "brought it up to date")
}

// A draft, a failing gate and a conflict are somebody's work, not a chore.
func TestOnlyTheReadyAreUpdated(t *testing.T) {
	draft := readyAndBehind(1, "feat(api): still writing")
	draft.Draft = true
	failing := readyAndBehind(2, "feat(api): red")
	conflicted := readyAndBehind(3, "feat(api): clashes")
	conflicted.Mergeability = core.MergeabilityConflicted
	statuses := passed(draft, conflicted)
	statuses[failing.HeadSHA] = []core.CommitStatus{{Context: engine.DefaultStatusContext, State: core.StatusFailure}}

	result, forge := currentResult(t, true, true, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{draft, failing, conflicted}, Statuses: statuses,
	})

	wantCounts(t, result, 0, 0)
	if len(forge.updated) != 0 {
		t.Errorf("flow updated %v, none of which was ready to land", forge.updated)
	}
}

// A forge flow cannot update a branch on gets the steps for a person, and a
// warning rather than a failure: sync runs every hour, and failing it for a
// chore only a person can do would fail it for ever (issue #240).
func TestAForgeThatCannotUpdateGetsTheButton(t *testing.T) {
	ready := readyAndBehind(1, "feat(api): ready")
	result, _ := currentResult(t, true, false, coretest.ForgeFixture{PullRequests: []core.PullRequest{ready}, Statuses: passed(ready)})

	wantState(t, result, engine.StateWarn)
	if result.Remediation.Kind != engine.RemedyManual || !strings.Contains(strings.Join(result.Remediation.Steps, " "), "auth.forge") {
		t.Errorf("the remedy is %+v, want a person's steps", result.Remediation)
	}
}
