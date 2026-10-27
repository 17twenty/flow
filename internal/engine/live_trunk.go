package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// CheckLiveTrunkBuilt is whether every merge on the trunk started the build the
// team requires of it.
const CheckLiveTrunkBuilt = "live.trunk.built"

// trunkBuilt asks whether every merge in the window has one of
// `forge.required_checks` reported against the merge commit itself (spec 20
// §6).
//
// A merge nothing was reported against is a merge whose events were lost or
// whose pipeline is broken, and either way the work that follows a push to the
// trunk — the build, the release, the deployment — did not happen. That is the
// same finding on every forge; why it happened is the forge's, so the remedy
// carries what the forge's declaration says about arming merges.
//
// Reported, not passed: a red build on the trunk is the build's own news, and
// this is about whether anything ran at all. A merge younger than the grace
// period may still be building, and is waited for rather than counted.
func (a *audit) trunkBuilt() Check {
	return Check{
		ID:        CheckLiveTrunkBuilt,
		Title:     "Every merge on the trunk started the build it requires",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			required := env.Config.Forge.RequiredChecks
			if len(required) == 0 {
				return Skip("forge.required_checks names nothing the trunk must build")
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			deadline := env.Now().Add(-TransitionGrace)
			var t tally
			var waiting int
			for _, pr := range prs {
				if pr.MergeSHA == "" {
					continue
				}
				reported, err := reportedAgainst(ctx, env, pr.MergeSHA)
				if err != nil {
					return Failed(core.Explain(err, "read what ran on #%d's merge %s", pr.Number, pr.MergeSHA))
				}
				switch {
				case slices.ContainsFunc(required, func(name string) bool { return slices.Contains(reported, name) }):
					t.held()
				case pr.MergedAt.After(deadline):
					waiting++
				default:
					t.broke(pr.URL, "#%d %s — merged %s and nothing it requires (%s) ran on %s",
						pr.Number, pr.Title, day(pr.MergedAt), strings.Join(required, ", "), short(pr.MergeSHA))
				}
			}
			result := t.grade(env, false)
			note := ""
			if waiting > 0 {
				note = fmt.Sprintf(", and %d merged too recently to judge", waiting)
			}
			switch {
			case t.counts.Total == 0:
				return result.Said("no merge in the window is old enough to judge%s", note)
			case result.State == StatePass:
				return result.Said("all %d %s started %s%s", t.counts.Total, plural(t.counts.Total, "merge"), strings.Join(required, ", "), note)
			}
			return result.Said("%d of %d merges started nothing the trunk requires%s", t.missed(), t.counts.Total, note).
				Fix(trunkBuiltRemedy(env))
		},
	}
}

// reportedAgainst is every check name reported against a commit: its statuses,
// and its check runs where the forge reports some of its CI that way.
func reportedAgainst(ctx context.Context, env *Env, sha string) ([]string, error) {
	statuses, err := env.Adapters.Forge.CommitStatuses(ctx, sha)
	if err != nil {
		return nil, err
	}
	if reader, ok := env.Adapters.Forge.(core.CheckRunReader); ok {
		runs, err := reader.CheckRuns(ctx, sha)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, runs...)
	}
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		names = append(names, status.Context)
	}
	return names, nil
}

// trunkBuiltRemedy is what to do about merges that started nothing. Where the
// team has the forge land pull requests for it, the forge's own word on what
// that does to the events after a merge comes first, because it is the likely
// reason.
func trunkBuiltRemedy(env *Env) Remediation {
	steps := []string{
		"open the merge commits named here and check whether any workflow ran on them",
	}
	if env.Config.Forge.AutoMerge && env.Adapters.Forge != nil {
		for _, hazard := range env.Adapters.Forge.Capabilities().Capability(core.CapAutoMerge).Hazards {
			steps = append(steps, "forge.auto_merge is on, and the forge says: "+hazard)
		}
	}
	steps = append(steps,
		"check that the workflow which builds the trunk runs on a push to it, and that its job names match forge.required_checks",
	)
	return Remediation{
		Kind:    RemedyManual,
		Summary: "work merged and nothing built it, so nothing that follows a build happened either",
		Steps:   steps,
	}
}
