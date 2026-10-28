package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit, deployment half (spec 07 §6, spec 13). The pull
// request checks ask whether work landed the way the team agreed and the board
// checks ask whether the board says so; these two ask the only question left,
// which is whether any of it is running.
//
// Neither is about `done_on`. A team whose work is not finished until it is
// deployed has the board checks for that; a library that finishes work on the
// merge still wants to know that every merge was released, which is exactly
// what spec 13 §2 gives `max_lag` for and what flow's own repository uses. So
// what switches these off is `deploys.source: none` — no records to read — and
// an objective the workflow did not set.
//
// They divide the question between them. `recent` asks whether the pipeline is
// alive at all, which is one fact about an environment and fails when the
// answer is no. `lag` asks whether it is keeping up with the trunk, which is a
// share of the merges and only ever warns: nothing in the setup can stop a
// deployment being slow, and the check that catches a pipeline that has
// actually stopped is the other one.

// The live-health checks about deployments (spec 07 §6).
const (
	// CheckLiveDeploysRecent is whether anything has reached the environment
	// lately.
	CheckLiveDeploysRecent = "live.deploys.recent"
	// CheckLiveDeploysLag is whether the merges reached it in the time the
	// team gave themselves.
	CheckLiveDeploysLag = "live.deploys.lag"
)

// deployChecks builds the deployment half of the audit, in report order. Like
// the others it shares the run's one [audit], so the records are read once
// however many of the checks are selected.
func (a *audit) deployChecks() []Check {
	return []Check{
		a.deploysRecent(),
		a.deploysLag(),
	}
}

// ReadsDeployments reports whether this workflow has deployment records to
// read at all, and says why not when it has not.
//
// It is the condition the desired state already uses to decide whether flow
// wants deployment records readable (spec 06 §1), so the audit and the plan
// agree about which repositories have deployments by construction rather than
// by two codebases happening to spell the same test the same way.
func ReadsDeployments(cfg *config.Config) (bool, string) {
	if cfg == nil || cfg.Deploys.Source == "" || cfg.Deploys.Source == config.DeploySourceNone {
		return false, "deploys.source records nothing, so there is no deployment for flow to read"
	}
	if cfg.Deploys.Environment == "" {
		return false, "deploys.environment names no environment to ask about"
	}
	return true, ""
}

// deploysRecent asks whether anything has reached the environment lately.
//
// It is the one live check for which silence is the finding rather than the
// exemption. Nothing merging in a fortnight means a quiet team; nothing
// deploying for longer than `max_age` means the thing that does the deploying
// has stopped, and that is what the team asked to be told about when they set
// the number.
//
// It counts nothing: this is a single fact about an environment, not a sample
// of items, and "1/1" on a dashboard would say less than the sentence does.
func (a *audit) deploysRecent() Check {
	return Check{
		ID:        CheckLiveDeploysRecent,
		Title:     "Something has reached the environment lately",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if reads, why := ReadsDeployments(env.Config); !reads {
				return Skip("%s", why)
			}
			age := env.Config.Deploys.MaxAge
			if age.Std() <= 0 {
				return Skip("deploys.max_age is what would make a gap too long, and this workflow sets none")
			}
			where := env.Config.Deploys.Environment
			records, err := a.shipped(ctx, env)
			if err != nil {
				return Failed(err)
			}
			stale := Remediation{
				Kind:    RemedyManual,
				Summary: "whatever deploys " + where + " is not recording that it did, or is not running",
				Steps: []string{
					"check deploys.source in this report: it says which records flow reads and where from",
					"a deployer must record a successful deployment of the application commit it shipped; see assets/docs/deploy-patterns.md",
					"check live.deploys.lag: the merges waiting for this are the same gap counted the other way",
				},
			}
			if len(records) == 0 {
				return Fail("nothing is recorded as deployed to %s in the last %s", where, age).Fix(stale)
			}
			newest := records[0]
			at := deployedAt(newest)
			if at.Before(env.Now().Add(-age.Std())) {
				return Fail("%s last took %s on %s, and deploys.max_age is %s",
					where, shortSHA(newest.SHA), day(at), age).Fix(stale)
			}
			return Pass("%s took %s on %s", where, shortSHA(newest.SHA), day(at))
		},
	}
}

// deploysLag asks whether the merges of the window reached the environment in
// the time the team gave themselves.
//
// A merge whose objective has not yet expired is not judged at all, the way
// spec 04 §5's fifteen minutes keep a webhook still in flight out of the
// transition audit: a pull request that merged a minute ago has not missed a
// two-hour objective, and counting it as a miss would make every run that
// happened to catch one report ill health.
func (a *audit) deploysLag() Check {
	return Check{
		ID:        CheckLiveDeploysLag,
		Title:     "Every merge reached the environment in the agreed time",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if reads, why := ReadsDeployments(env.Config); !reads {
				return Skip("%s", why)
			}
			lag := env.Config.Deploys.MaxLag
			if lag.Std() <= 0 {
				return Skip("deploys.max_lag is the objective this would hold the team to, and this workflow sets none")
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			records, err := a.shipped(ctx, env)
			if err != nil {
				return Failed(err)
			}
			// Reading the window is what proves there is a forge: a run
			// without one has already failed on the merges, so by here the
			// comparer is the cache or the forge and never nothing.
			comparer := env.Comparer()
			where := env.Config.Deploys.Environment
			now := env.Now()
			waiting, gone := 0, 0
			var t tally
			for _, pr := range prs {
				// A forge that merged a pull request without saying what the
				// merge produced leaves nothing to compare, and spec 13 §3
				// does not guess (item 4.4).
				if pr.MergeSHA == "" {
					continue
				}
				deadline := pr.MergedAt.Add(lag.Std())
				running, err := runningBy(ctx, comparer, pr, records, deadline)
				if err != nil {
					return Failed(err)
				}
				switch {
				case running:
					t.held()
				case deadline.After(now):
					waiting++
				default:
					// Asked only of a merge that would otherwise be a miss,
					// so a healthy run costs nothing more (issue #278).
					onTrunk, err := a.onTrunk(ctx, env, pr)
					if err != nil {
						return Failed(err)
					}
					if !onTrunk {
						gone++
						continue
					}
					t.broke(pr.URL, "#%d merged on %s and is still not running in %s",
						pr.Number, day(pr.MergedAt), where)
				}
			}
			result := t.gradeWarning(env)
			if gone > 0 {
				result = result.Saw(fmt.Sprintf("left out %d %s no longer on the trunk, which can never reach %s",
					gone, plural(gone, "merge"), where), "")
			}
			switch {
			case t.counts.Total == 0 && waiting > 0:
				return result.Said("the %d %s merged in the window %s still inside the %s objective",
					waiting, plural(waiting, "pull request"), isAre(waiting), lag)
			case t.counts.Total == 0:
				return result.Said("nothing merged in the window")
			case result.State == StatePass:
				return result.Said("all %d merged %s reached %s within %s",
					t.counts.Total, plural(t.counts.Total, "pull request"), where, lag)
			}
			return result.Said("%d of %d merged pull requests took longer than %s to reach %s",
				t.missed(), t.counts.Total, lag, where).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "work that merged and never shipped is work nobody can use",
					Steps: []string{
						"check live.deploys.recent in this report: a pipeline that stopped is this same gap seen whole",
						"check deploys.source: a deployment flow cannot read is one flow cannot credit",
						"deploys.max_lag is the team's own objective; change it if the number no longer describes how you ship",
					},
				})
		},
	}
}

// onTrunk reports whether a pull request's merge is still on the trunk. A trunk
// that was rewritten — reset, force-pushed — no longer holds merges it once
// did, and those can never reach production: counting them as late held a
// team to work that no longer exists (issue #278).
//
// It asks the forge rather than the ancestry cache. The cache is keyed by a
// pair of commits that never change, and the trunk is a name that does.
func (a *audit) onTrunk(ctx context.Context, env *Env, pr core.PullRequest) (bool, error) {
	trunk, err := a.trunk(ctx, env)
	if err != nil {
		return false, err
	}
	comparison, err := env.Adapters.Forge.Compare(ctx, pr.MergeSHA, trunk)
	switch {
	case errors.Is(err, core.ErrNotFound):
		// The forge has forgotten the commit: nothing on the trunk holds it.
		return false, nil
	case err != nil:
		return false, core.Explain(err, "tell whether #%d's merge is still on %s", pr.Number, trunk)
	case !comparison.Known():
		return false, core.Errorf(core.ErrUnavailable, "tell whether #%d's merge is still on %s", pr.Number, trunk).
			WithWhy("the forge reports their ancestry as %q, which flow does not understand", comparison.Status)
	}
	return comparison.Contains(), nil
}

// shipped is every successful deployment of the environment in the period the
// two checks are about, newest first, read once.
//
// The period is the longer of the audit window and `max_age`: `lag` is about
// the merges of the window and `recent` about a gap the team may well allow to
// be longer than one, and two reads of overlapping spans would be two round
// trips for one history.
func (a *audit) shipped(ctx context.Context, env *Env) ([]core.Deployment, error) {
	a.shipOnce.Do(func() {
		if env.Adapters.Deploys == nil {
			a.shipErr = errNoDeploys
			return
		}
		where := env.Config.Deploys.Environment
		since := env.Since()
		if age := env.Config.Deploys.MaxAge.Std(); age > 0 {
			if start := env.Now().Add(-age); start.Before(since) {
				since = start
			}
		}
		records, err := env.Adapters.Deploys.Deployments(ctx, core.DeployQuery{Environment: where, Since: since})
		if err != nil {
			a.shipErr = core.Explain(err, "read the deployments of %s", where)
			return
		}
		for _, record := range records {
			if record.Succeeded() {
				a.ships = append(a.ships, record)
			}
		}
		slices.SortStableFunc(a.ships, func(x, y core.Deployment) int {
			return deployedAt(y).Compare(deployedAt(x))
		})
	})
	return a.ships, a.shipErr
}

// runningBy reports whether some deployment at or before the deadline was of a
// commit containing this merge.
//
// The records arrive newest first and that is the order they are asked about:
// the newest deployment inside the deadline is the one most likely to contain
// the merge, so the usual case is one comparison. Older ones are still asked
// about, because a hotfix deployment of an earlier commit breaks the ordering
// that would otherwise let the walk stop.
func runningBy(ctx context.Context, comparer core.Comparer, pr core.PullRequest, records []core.Deployment, deadline time.Time) (bool, error) {
	for _, record := range records {
		at := deployedAt(record)
		if at.After(deadline) {
			continue
		}
		// Nothing deployed before the merge can contain it, and the records
		// are in order, so the rest are older still.
		if at.Before(pr.MergedAt) {
			break
		}
		running, err := deployedWith(ctx, comparer, pr, record.SHA)
		if err != nil {
			return false, err
		}
		if running {
			return true, nil
		}
	}
	return false, nil
}

// deployedAt is when the environment started running a commit, which is when
// the deployment succeeded rather than when somebody started it. A deployment
// that takes twenty minutes took twenty minutes, and the record keeps both
// (spec 13 §3).
func deployedAt(record core.Deployment) time.Time {
	if !record.UpdatedAt.IsZero() {
		return record.UpdatedAt
	}
	return record.CreatedAt
}

// isAre agrees the verb with what was counted.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
