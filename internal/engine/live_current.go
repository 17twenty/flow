package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/17twenty/flow/internal/core"
)

// CheckLivePRsCurrent is whether the pull requests ready to land are up to
// date with the trunk.
const CheckLivePRsCurrent = "live.prs.current"

// prsCurrent asks whether any pull request the gate has passed is held up only
// by being behind the trunk, and under --fix brings it up to date (issue #234).
//
// A trunk that requires branches to be up to date — which flow asks for, so
// that green is a claim about what lands — makes every other open pull
// request fall behind each time one merges. Somebody then has to press
// "Update branch" on each of them, and wait for its checks, before it can
// land. With several in flight that is a queue of chores nobody owns, and
// the tool a team already has will do it: so flow does, for exactly the ones
// whose only remaining obstacle it is. A draft, a failing gate or a conflict
// is somebody's work and is left alone.
func (a *audit) prsCurrent() Check {
	return Check{
		ID:        CheckLivePRsCurrent,
		Title:     "No pull request that is ready to land waits only on being behind",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			open, err := a.inFlight(ctx, env)
			if err != nil {
				return Failed(err)
			}
			updater, canUpdate := env.Adapters.Forge.(core.BranchUpdater)
			canUpdate = canUpdate && env.Adapters.Forge.Capabilities().Can(core.CapUpdateBranch)
			var t tally
			var caught []Evidence
			for _, pr := range open {
				if pr.Draft || pr.Mergeability != core.MergeabilityBehind {
					continue
				}
				passed, err := gatePassed(ctx, env, pr)
				if err != nil {
					return Failed(err)
				}
				if !passed {
					continue
				}
				if !env.Fix || !canUpdate {
					t.broke(pr.URL, "#%d %s — passed the gate and is behind %s", pr.Number, pr.Title, pr.BaseRef)
					continue
				}
				if err := updater.UpdateBranch(ctx, pr.Number, pr.HeadSHA); err != nil {
					t.broke(pr.URL, "#%d %s — flow could not bring it up to date: %v", pr.Number, pr.Title, err)
					continue
				}
				t.held()
				caught = append(caught, Evidence{
					Text: fmt.Sprintf("#%d %s — passed the gate and was behind %s, so flow brought it up to date", pr.Number, pr.Title, pr.BaseRef),
					URL:  pr.URL,
				})
			}
			result := t.grade(env, false)
			for _, evidence := range caught {
				result = result.Saw(evidence.Text, evidence.URL)
			}
			switch {
			case t.counts.Total == 0:
				return result.Said("no pull request that passed the gate is behind its base")
			case result.State == StatePass:
				return result.Said("flow brought all %d that passed the gate and were behind up to date", len(caught))
			}
			result = result.Said("%d of %d pull requests that passed the gate wait only on being behind", t.missed(), t.counts.Total)
			if !env.Fix && canUpdate {
				return result.Fix(Remediation{Kind: RemedyAuto, Summary: "flow can bring them up to date", Command: "flow sync"})
			}
			// Where flow cannot update a branch it is somebody's chore, not a
			// fault: it warns, so a sync run every hour on such a forge does
			// not fail over what only a person can do (issue #240).
			if !canUpdate {
				result.State = StateWarn
			}
			return result.Fix(Remediation{
				Kind:    RemedyManual,
				Summary: "bring them up to date with their base",
				Steps: append(env.Adapters.Forge.Capabilities().Capability(core.CapUpdateBranch).ByHand.Steps,
					"check auth.forge in this report: updating a branch is a write, and needs a credential that may make one"),
			})
		},
	}
}

// gatePassed reports whether flow's own status on the pull request's head
// says the gate passed.
func gatePassed(ctx context.Context, env *Env, pr core.PullRequest) (bool, error) {
	statuses, err := env.Adapters.Forge.CommitStatuses(ctx, pr.HeadSHA)
	if err != nil {
		return false, core.Explain(err, "read the gate's status on #%d", pr.Number)
	}
	gate := StatusContext(env.Config)
	return slices.ContainsFunc(statuses, func(s core.CommitStatus) bool {
		return s.Context == gate && s.State == core.StatusSuccess
	}), nil
}
