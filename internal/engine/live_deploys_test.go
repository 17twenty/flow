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

// The deployment half of the live-health audit (spec 07 §6, spec 13): whether
// anything is reaching the environment, and whether the merges of the window
// reached it in the time the team gave themselves.

// shippingConfig is a team that records deployments and holds itself to an
// objective, which is how flow's own repository is configured: work is done
// when it merges, and `max_lag` is what proves every merge was released
// (spec 13 §2).
func shippingConfig() *config.Config {
	cfg := liveConfig()
	cfg.Deploys = config.Deploys{
		Source:      config.DeploySourceForge,
		Environment: "production",
		MaxLag:      config.Duration(2 * time.Hour),
		MaxAge:      config.Duration(72 * time.Hour),
	}
	return cfg
}

// shipment is one successful deployment of a commit to production.
func shipment(sha string, at time.Time) core.Deployment {
	return core.Deployment{
		Environment: "production",
		SHA:         sha,
		State:       core.DeploySuccess,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

// shippingEnv is the world the deployment checks are asked about: the forge
// holding the merges and the comparisons, and a source holding the records.
func shippingEnv(t *testing.T, cfg *config.Config, fx coretest.ForgeFixture, records ...core.Deployment) (*engine.Env, *coretest.FakeDeploys) {
	t.Helper()
	env, _ := liveEnv(t, cfg, fx)
	deploys := coretest.NewFakeDeploys(coretest.DeployFixture{Deployments: records})
	env.Adapters.Deploys = deploys
	return env, deploys
}

// TestScenario_V01_AMergeThatNeverReachedTheEnvironmentIsNamed is scenario V01: three
// merges, two of them deployed inside the objective and one that never
// shipped at all. The check warns rather than fails — nothing in the setup
// can make a deployment faster — and names the merge that is still waiting,
// because "2/3" without a link is a number nobody can act on.
func TestScenario_V01_AMergeThatNeverReachedTheEnvironmentIsNamed(t *testing.T) {
	first := landedPR(1, "feat(api): the first", daysAgo(3))
	second := landedPR(2, "feat(api): the second", daysAgo(2))
	missing := landedPR(3, "feat(api): the one still waiting", daysAgo(1))
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{first, second, missing},
		Comparisons: map[string]core.Comparison{
			first.MergeSHA + "...ship001":  {Status: core.CompareAhead},
			second.MergeSHA + "...ship002": {Status: core.CompareAhead},
		},
	},
		shipment("ship001", daysAgo(3).Add(time.Hour)),
		shipment("ship002", daysAgo(2).Add(time.Hour)),
	)

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 2, 3)
	evidenceLinks(t, result, "#3", missing.URL)
}

// A deployment that slipped is never worse than a warning. Nothing in the
// configuration can stop a release being slow, and a check that could fail
// the daily run for it is one a team turns off.
func TestALateDeploymentIsNeverWorseThanAWarning(t *testing.T) {
	first := landedPR(1, "feat(api): the first", daysAgo(3))
	second := landedPR(2, "feat(api): the second", daysAgo(2))
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{first, second},
		Comparisons: map[string]core.Comparison{
			first.MergeSHA + "...ship001":  {Status: core.CompareAhead},
			second.MergeSHA + "...ship001": {Status: core.CompareAhead},
		},
	}, shipment("ship001", daysAgo(1)))

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StateWarn)
	wantCounts(t, result, 0, 2)
}

// A merge whose objective has not expired yet has not missed it. Counting it
// as a miss would make every run that happened to catch one report ill health
// — the audit would be worse the more often it ran.
func TestAMergeStillInsideItsObjectiveIsNotJudged(t *testing.T) {
	fresh := landedPR(4, "feat(api): merged minutes ago", liveNow.Add(-30*time.Minute))
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{fresh},
	}, shipment("ship001", daysAgo(1)))

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
	if !strings.Contains(result.Message, "still inside") {
		t.Errorf("the message is %q, and does not say the merge has not missed anything yet", result.Message)
	}
}

// Being inside the objective excuses a merge that has not shipped yet; it
// does not withhold credit from one that has. A team that deploys in minutes
// merges everything inside its own objective, so setting those aside first
// would leave the check permanently counting nothing and permanently passing.
func TestAMergeAlreadyRunningIsCreditedInsideItsObjective(t *testing.T) {
	quick := landedPR(11, "feat(api): merged and shipped the same hour", liveNow.Add(-30*time.Minute))
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{quick},
		Comparisons: map[string]core.Comparison{
			quick.MergeSHA + "...ship001": {Status: core.CompareAhead},
		},
	}, shipment("ship001", liveNow.Add(-10*time.Minute)))

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if strings.Contains(result.Message, "still inside") {
		t.Errorf("the message is %q: work that is already running was set aside as waiting", result.Message)
	}
}

// Spec 07 §6 asks whether a merge reached some deployed commit, not whether
// it is in the newest one. A deployment that rolled production back to an
// earlier commit did not un-ship the work that was released on time, and
// reading only the latest record would blame a team for a rollback.
func TestARollbackDoesNotUnshipAMergeThatWasReleasedOnTime(t *testing.T) {
	shipped := landedPR(5, "feat(api): released on time", daysAgo(3))
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{shipped},
		Comparisons: map[string]core.Comparison{
			shipped.MergeSHA + "...ship001": {Status: core.CompareAhead},
			shipped.MergeSHA + "...old001":  {Status: core.CompareDiverged},
		},
	},
		shipment("ship001", daysAgo(3).Add(time.Hour)),
		shipment("old001", daysAgo(3).Add(90*time.Minute)),
	)

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// A forge that merged a pull request without saying what the merge produced
// leaves nothing to compare. Spec 13 §3 does not guess, and counting it as
// late would report a gap in the forge's own records as a gap in the team's
// delivery.
func TestAMergeTheForgeCannotPlaceIsNotGuessedAt(t *testing.T) {
	unplaceable := landedPR(6, "feat(api): merged somehow", daysAgo(2))
	unplaceable.MergeSHA = ""
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{unplaceable},
	}, shipment("ship001", daysAgo(1)))

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
}

// live.deploys.recent is the one live check for which silence is the finding
// rather than the exemption: a pipeline that has stopped is exactly what the
// team asked to be told about when they set `max_age`.
func TestAPipelineThatHasStoppedIsAFailure(t *testing.T) {
	cfg := shippingConfig()
	cfg.Deploys.MaxAge = config.Duration(24 * time.Hour)
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{}, shipment("ship001", daysAgo(5)))

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StateFail)
	for _, want := range []string{"production", "ship001"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("the message is %q, and does not name %s", result.Message, want)
		}
	}
}

// An environment nothing has ever reached fails for the same reason, and has
// to say so differently: there is no last deployment to date.
func TestAnEnvironmentNothingHasReachedIsAFailure(t *testing.T) {
	env, _ := shippingEnv(t, shippingConfig(), coretest.ForgeFixture{})

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Message, "production") {
		t.Errorf("the message is %q, and does not say which environment", result.Message)
	}
}

// A deployment inside `max_age` is health, and the report says which commit
// and when rather than counting a sample of one.
func TestARecentDeploymentIsHealth(t *testing.T) {
	cfg := shippingConfig()
	cfg.Deploys.MaxAge = config.Duration(24 * time.Hour)
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{},
		shipment("ship000", daysAgo(5)), shipment("ship001", daysAgo(1)))

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StatePass)
	if result.Counts != nil {
		t.Errorf("one fact about an environment was counted as a sample: %s", result.Counts)
	}
	if !strings.Contains(result.Message, "ship001") {
		t.Errorf("the message is %q, and does not say what was deployed", result.Message)
	}
}

// A record's state is read before it is dated. A failed deployment is not a
// deployment, and treating the newest record as one would report a broken
// pipeline as a healthy one — which is the exact opposite of the check.
func TestAFailedDeploymentIsNotADeployment(t *testing.T) {
	cfg := shippingConfig()
	cfg.Deploys.MaxAge = config.Duration(24 * time.Hour)
	broke := shipment("ship002", daysAgo(1))
	broke.State = core.DeployFailure
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{}, shipment("ship001", daysAgo(5)), broke)

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StateFail)
}

// A deployment is dated by the moment the environment started running the
// commit, which is when it succeeded and not when somebody started it. A
// release that takes an hour took an hour, and dating it by its creation
// would call a pipeline stopped while it was still finishing.
func TestADeploymentIsDatedByWhenItFinished(t *testing.T) {
	cfg := shippingConfig()
	cfg.Deploys.MaxAge = config.Duration(24 * time.Hour)
	slow := shipment("ship001", liveNow.Add(-25*time.Hour))
	slow.UpdatedAt = liveNow.Add(-23 * time.Hour)
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{}, slow)

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StatePass)
}

// A team that records no deployments is not asked about them. This is the
// condition the desired state uses to decide whether flow wants deployment
// records at all, so the plan and the audit cannot disagree about which
// repositories have deployments.
func TestAWorkflowThatRecordsNoDeploymentsIsNotAudited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deploys config.Deploys
		says    string
	}{
		{"no source", config.Deploys{Source: config.DeploySourceNone}, "deploys.source"},
		{"no environment", config.Deploys{Source: config.DeploySourceForge}, "deploys.environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := liveConfig()
			cfg.Deploys = tc.deploys
			env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{
				PullRequests: []core.PullRequest{landedPR(7, "feat(api): the thing", daysAgo(2))},
			})

			for _, id := range []string{engine.CheckLiveDeploysRecent, engine.CheckLiveDeploysLag} {
				result := liveResult(t, env, id)
				wantState(t, result, engine.StateSkip)
				if !strings.Contains(result.Message, tc.says) {
					t.Errorf("%s skipped saying %q, which does not say why", id, result.Message)
				}
			}
		})
	}
}

// A number the team did not set is not a number flow invents. Each check is
// about one of them, and each says which key would switch it on.
func TestANumberNobodySetIsNotAnObjective(t *testing.T) {
	for _, tc := range []struct {
		check string
		key   string
		drop  func(*config.Config)
	}{
		{engine.CheckLiveDeploysRecent, "max_age", func(c *config.Config) { c.Deploys.MaxAge = 0 }},
		{engine.CheckLiveDeploysLag, "max_lag", func(c *config.Config) { c.Deploys.MaxLag = 0 }},
	} {
		t.Run(tc.key, func(t *testing.T) {
			cfg := shippingConfig()
			tc.drop(cfg)
			env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{},
				shipment("ship001", daysAgo(9)))

			result := liveResult(t, env, tc.check)

			wantState(t, result, engine.StateSkip)
			if !strings.Contains(result.Message, tc.key) {
				t.Errorf("%s skipped saying %q, which does not name %s", tc.check, result.Message, tc.key)
			}
		})
	}
}

// Both deployment checks are about one history, so it is read once. Two reads
// of overlapping spans would be two round trips for the same fortnight and,
// worse, two slightly different answers about what is running.
func TestTheDeploymentHistoryIsReadOnceForBothChecks(t *testing.T) {
	cfg := shippingConfig()
	env, deploys := shippingEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(8, "feat(api): the thing", daysAgo(2))},
		Comparisons: map[string]core.Comparison{
			"merge008...ship001": {Status: core.CompareAhead},
		},
	}, shipment("ship001", daysAgo(2).Add(time.Hour)))

	liveRun(t, env, []string{"live.deploys.*"})

	if got := deploys.Calls("Deployments"); got != 1 {
		t.Errorf("the deployment history was read %d times, want once", got)
	}
}

// The window the records are read over covers both questions. `max_age` may
// be longer than the audit window — a team that deploys monthly still wants
// to know the last one happened — and a read bounded by the window alone
// would report that environment as never deployed to.
func TestAGapLongerThanTheWindowIsStillSeen(t *testing.T) {
	cfg := shippingConfig()
	cfg.Deploys.MaxAge = config.Duration(30 * 24 * time.Hour)
	env, _ := shippingEnv(t, cfg, coretest.ForgeFixture{},
		shipment("ship001", liveNow.AddDate(0, 0, -20)))

	result := liveResult(t, env, engine.CheckLiveDeploysRecent)

	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "ship001") {
		t.Errorf("the message is %q, and does not name the deployment 20 days back", result.Message)
	}
}

// The audit asks whatever it was given to ask, which is the ancestry cache
// when there is one. A fortnight of merges compared afresh every morning is
// the same question put to the forge every day for answers that cannot change
// (spec 13 §3).
func TestTheAuditComparesThroughTheCacheItWasGiven(t *testing.T) {
	shipped := landedPR(9, "feat(api): released", daysAgo(3))
	fresh := landedPR(10, "feat(api): merged since the last release", liveNow.Add(-30*time.Minute))
	env, forge := liveEnv(t, shippingConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{shipped, fresh},
	})
	env.Adapters.Deploys = coretest.NewFakeDeploys(coretest.DeployFixture{
		Deployments: []core.Deployment{shipment("ship001", daysAgo(3).Add(time.Hour))},
	})
	cache := &countingComparer{status: core.CompareAhead}
	env.Ancestry = cache

	result := liveResult(t, env, engine.CheckLiveDeploysLag)

	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if cache.calls != 1 {
		t.Errorf("the cache was asked %d times, want once: a deployment that finished "+
			"before a merge cannot contain it, and the records run out in merge order", cache.calls)
	}
	if got := forge.Calls("Compare"); got != 0 {
		t.Errorf("the forge was compared with %d times behind the cache's back", got)
	}
}
