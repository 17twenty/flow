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

// The live-health audit of spec 07 §6: what each check counts, where it draws
// the line between a warning and a failure, and what it refuses to call ill
// health. The frame these run in — blocking, exit codes, rendering — is
// doctor_test.go's.

// liveNow is the instant every live test is anchored to. It is the one
// doctorEnv's clock reports, so a time computed here and a window computed by
// the engine are about the same fortnight.
var liveNow = time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)

// daysAgo is a moment inside the default fortnight.
func daysAgo(days int) time.Time { return liveNow.AddDate(0, 0, -days) }

// liveConfig is a team working the way flow describes: conventional commits,
// a machine contributor, a person who has to approve its work, and a board
// with the four columns spec 07 §6's ticket checks ask about.
func liveConfig() *config.Config {
	cfg := setupConfig()
	cfg.Commits = config.Commits{Convention: "conventional", UnlinkedTypes: []string{"chore"}}
	cfg.Agents = config.Agents{Authors: []string{"flow-bot"}, RequireHumanReview: true}
	cfg.Workflow = config.Workflow{
		DoneOn:  config.DoneOnMerged,
		Blocked: config.Blocked{Label: "blocked", RequireComment: true},
		Columns: []config.Column{
			{ID: "backlog", Name: "Backlog", Category: config.CategoryTodo},
			{ID: "ready", Name: "Ready", Category: config.CategoryTodo, Gate: config.GateDefinitionOfReady},
			{ID: "in_progress", Name: "In progress", Category: config.CategoryInProgress},
			{ID: "in_review", Name: "In review", Category: config.CategoryInProgress},
			{ID: "done", Name: "Done", Category: config.CategoryDone},
		},
		Transitions: map[string]config.Transition{
			config.EventPROpenedDraft:    {Owner: config.OwnerFlow, To: "in_progress"},
			config.EventPRReadyForReview: {Owner: config.OwnerFlow, To: "in_review"},
			config.EventPRMerged:         {Owner: config.OwnerNative, To: "done"},
			config.EventIssueClosed:      {Owner: config.OwnerNative, To: "done"},
		},
	}
	cfg.DefinitionOfReady = config.DefinitionOfReady{
		Rules: []string{config.DoRAcceptanceCriteria, config.DoRNotBlocked},
	}
	return cfg
}

// landedPR is a pull request that landed on the trunk, with the fields every
// live check reads and nothing else.
func landedPR(number int, title string, at time.Time) core.PullRequest {
	return core.PullRequest{
		Number:        number,
		Title:         title,
		Author:        "dev",
		State:         core.PRStateMerged,
		BaseRef:       "main",
		HeadSHA:       fmt.Sprintf("head%03d", number),
		MergeSHA:      fmt.Sprintf("merge%03d", number),
		ClosingIssues: []string{fmt.Sprintf("#%d", 100+number)},
		MergedAt:      at,
		UpdatedAt:     at,
		URL:           fmt.Sprintf("https://github.com/acme/api/pull/%d", number),
	}
}

// passing is the gate's own success on a pull request's head, published just
// before it merged.
func passing(prs ...core.PullRequest) map[string][]core.CommitStatus {
	statuses := map[string][]core.CommitStatus{}
	for _, pr := range prs {
		statuses[pr.HeadSHA] = []core.CommitStatus{{
			Context:   engine.DefaultStatusContext,
			State:     core.StatusSuccess,
			CreatedAt: pr.MergedAt.Add(-time.Minute),
		}}
	}
	return statuses
}

// liveEnv is the world the live checks are asked about: a forge holding the
// fixture, a tracker that reads the forge's own closing references, and the
// fortnight doctorEnv sets.
func liveEnv(t *testing.T, cfg *config.Config, fx coretest.ForgeFixture) (*engine.Env, *coretest.FakeForge) {
	t.Helper()
	if fx.Repo.DefaultBranch == "" {
		fx.Repo = core.RepoInfo{Owner: "acme", Name: "api", DefaultBranch: "main"}
	}
	forge := coretest.NewFakeForge(fx)
	env := doctorEnv(t)
	env.Config = cfg
	env.Adapters = engine.Adapters{
		Forge:   forge,
		Tracker: coretest.NewFakeTracker(coretest.TrackerFixture{}),
	}
	// A working copy whose commits announce nothing. Doctor always has one;
	// a nil parser is a run that could not look, which is a failure rather
	// than a fortnight in which nobody added a feature flag.
	env.Trailers = newTrailers()
	return env, forge
}

// liveRun runs the whole live audit as doctor would, behind the one check
// every live check waits for. The registry refuses a dependency that is not
// there, which is the point of it: `forge.repo` is a setup check, and the
// live checks are never registered without it.
func liveRun(t *testing.T, env *engine.Env, only []string) engine.Report {
	t.Helper()
	checks := append([]engine.Check{
		answering(engine.CheckForgeRepo, engine.CategoryForge, engine.Pass("the repository is there")),
		answering(engine.CheckAuthTracker, engine.CategoryAuth, engine.Pass("the tracker credential is good")),
	}, engine.LiveChecks(env.Config)...)
	return doctorRun(t, env, only, checks...)
}

// liveResult runs one live check on its own and returns what it decided.
func liveResult(t *testing.T, env *engine.Env, id string) engine.Result {
	t.Helper()
	for _, check := range engine.LiveChecks(env.Config) {
		if check.ID != id {
			continue
		}
		result := check.Run(t.Context(), env)
		if result.State == "" {
			t.Fatalf("%s answered nothing", id)
		}
		return result
	}
	t.Fatalf("there is no %s check", id)
	return engine.Result{}
}

// wantCounts fails unless the check looked at what it was supposed to.
func wantCounts(t *testing.T, got engine.Result, pass, total int) {
	t.Helper()
	if got.Counts == nil {
		t.Fatalf("a live check reported no counts: %+v", got)
	}
	if got.Counts.Pass != pass || got.Counts.Total != total {
		t.Errorf("counts are %s, want %d/%d", got.Counts, pass, total)
	}
}

// evidenceMentions fails unless some piece of evidence names the thing.
func evidenceMentions(t *testing.T, got engine.Result, want string) {
	t.Helper()
	for _, seen := range got.Evidence {
		if strings.Contains(seen.Text, want) {
			return
		}
	}
	t.Errorf("no evidence mentions %q: %+v", want, got.Evidence)
}

// evidenceLinks fails unless the piece of evidence naming want also links to
// it. A finding that names a pull request and does not say where it is leaves
// a person to search for it, which is the difference between a report and a
// number.
func evidenceLinks(t *testing.T, got engine.Result, want, url string) {
	t.Helper()
	for _, seen := range got.Evidence {
		if !strings.Contains(seen.Text, want) {
			continue
		}
		if seen.URL != url {
			t.Errorf("the evidence naming %q links to %q, want %q", want, seen.URL, url)
		}
		return
	}
	t.Errorf("no evidence mentions %q: %+v", want, got.Evidence)
}

// liveCheckIDs is every check spec 07 §6 names in the families that exist:
// the pull requests that merged, the board they came off, the transitions the
// tracker promised to perform itself — one check per native event, which
// setupConfig gives two of — the one check for all the transitions flow
// performs, and the feature flags the window announced.
func liveCheckIDs() []string {
	return []string{
		engine.CheckLivePRsLinked,
		engine.CheckLivePRsGated,
		engine.CheckLivePRsConventional,
		engine.CheckLivePRsReviewed,
		engine.CheckLiveDeploysRecent,
		engine.CheckLiveDeploysLag,
		engine.CheckLiveTicketsDoneMerged,
		engine.CheckLiveTicketsDoneDeployed,
		engine.CheckLiveTicketsReviewHasPR,
		engine.CheckLiveTicketsProgressStale,
		engine.CheckLiveTicketsReadyDoR,
		engine.CheckLiveTicketsBlockedExplained,
		engine.LiveTransitionCheck(config.EventPRMerged),
		engine.LiveTransitionCheck(config.EventIssueClosed),
		engine.CheckLiveTransitionsFlow,
		engine.CheckLiveFlagsCleanup,
		engine.CheckLiveAgentsRework,
	}
}

// TestTheLiveAuditIsRegisteredAsSpecSixDescribesIt holds the properties of the
// list itself: every check §6 names is there once, under the live category,
// and none of them is asked before flow knows it can reach what it is about
// to interrogate.
func TestTheLiveAuditIsRegisteredAsSpecSixDescribesIt(t *testing.T) {
	in := engine.SetupInput{Config: setupConfig(), Local: setupLocal()}
	registry, err := engine.DoctorRegistry(in)
	if err != nil {
		t.Fatalf("the doctor registry does not build: %v", err)
	}
	reachable := []string{engine.CheckForgeRepo, engine.CheckAuthTracker}
	for _, id := range liveCheckIDs() {
		check, ok := registry.Find(id)
		if !ok {
			t.Fatalf("the registry has no %s", id)
		}
		if check.Category != engine.CategoryLive {
			t.Errorf("%s is in category %q, want %q", id, check.Category, engine.CategoryLive)
		}
		if len(check.DependsOn) == 0 {
			t.Errorf("%s waits for nothing, so it would ask a provider nobody has reached", id)
		}
		for _, on := range check.DependsOn {
			if !slices.Contains(reachable, on) {
				t.Errorf("%s waits for %s, which is not one of the checks that prove a provider answers", id, on)
			}
		}
	}
}

// TestAQuietFortnightIsNotIllHealth is the property every live check shares.
// Nothing merged is nothing broken, and a check that reported "all 0 of them
// were fine" would be unreadable on the dashboard that is the whole point of
// the daily run.
func TestAQuietFortnightIsNotIllHealth(t *testing.T) {
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{})
	answered := 0
	for _, id := range liveCheckIDs() {
		result := liveResult(t, env, id)
		if result.State == engine.StateSkip {
			continue
		}
		answered++
		if result.State != engine.StatePass {
			t.Errorf("%s is %q on an empty window, want pass: %s", id, result.State, result.Message)
		}
		if result.Counts == nil || result.Counts.Total != 0 {
			t.Errorf("%s counted something in an empty window: %+v", id, result.Counts)
		}
		if strings.Contains(result.Message, "all 0") {
			t.Errorf("%s says %q, which reads as health rather than silence", id, result.Message)
		}
	}
	if want := len(liveCheckIDs()) - 3; answered != want {
		t.Errorf("%d live checks had an opinion about a quiet fortnight, want %d: the two "+
			"deployment checks and live.tickets.done_deployed do not apply to a team that "+
			"records no deployments and finishes work on the merge", answered, want)
	}
}

// TestAMergeThatNamedNoTicketIsCountedAgainstTheWindow is live.prs.linked
// doing its job: the pull request nobody can trace is named, with a link, and
// the ones that were fine are not.
func TestAMergeThatNamedNoTicketIsCountedAgainstTheWindow(t *testing.T) {
	orphan := landedPR(7, "feat(api): add the thing", daysAgo(2))
	orphan.ClosingIssues = nil
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			landedPR(5, "fix(api): stop the thing", daysAgo(3)),
			orphan,
		},
	})

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#7")
	if got := result.Evidence[0].URL; got != orphan.URL {
		t.Errorf("the evidence links %q, want %q", got, orphan.URL)
	}
	if !result.Remediation.Actionable() {
		t.Error("a merge with no ticket is something a team can act on, so it must not be a hard failure")
	}
}

// TestATypeThatMayMergeUnlinkedDoesNotCountAgainstIt holds the exemption spec
// §6 gives: a type in commits.unlinked_types is allowed to have no ticket, and
// the gate and the audit must agree about which types those are.
func TestATypeThatMayMergeUnlinkedDoesNotCountAgainstIt(t *testing.T) {
	chore := landedPR(9, "chore(deps): bump the thing", daysAgo(1))
	chore.ClosingIssues = nil
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{chore},
	})

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
}

// TestWithNoTrackerTheForgesOwnLinksAreWhatThereIs: a run that could not reach
// the tracker still knows which pull requests named an issue, because the
// forge records the closing reference itself. Calling all of them unlinked
// would report the team's week as broken when it was flow that could not look.
func TestWithNoTrackerTheForgesOwnLinksAreWhatThereIs(t *testing.T) {
	orphan := landedPR(8, "feat(api): add the thing", daysAgo(2))
	orphan.ClosingIssues = nil
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			landedPR(6, "fix(api): stop the thing", daysAgo(3)),
			orphan,
		},
	})
	env.Adapters.Tracker = nil

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#8")
}

// TestScenario_V02_AnUngatedMergeFailsHoweverFewThereAre is scenario V02. One merge in ten
// is 90%, which is exactly the threshold a warning lives above — and the point
// of marking a check critical is that the threshold does not apply to it.
func TestScenario_V02_AnUngatedMergeFailsHoweverFewThereAre(t *testing.T) {
	var prs []core.PullRequest
	for number := 1; number <= 10; number++ {
		prs = append(prs, landedPR(number, "feat(api): number", daysAgo(number)))
	}
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: prs,
		Statuses:     passing(prs[1:]...),
	})

	result := liveResult(t, env, engine.CheckLivePRsGated)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 9, 10)
	evidenceMentions(t, result, "#1")
	evidenceMentions(t, result, engine.DefaultStatusContext)

	report := liveRun(t, env, nil)
	if got := report.Exit(); got != core.ExitActionRequired {
		t.Errorf("an ungated merge exits %d, want %d", got, core.ExitActionRequired)
	}
}

// TestAVerdictPublishedAfterTheMergeDoesNotProveTheGate is the difference
// between a rule being evaluated and a rule being enforced. A green status
// that arrived once the merge was already done stopped nothing.
func TestAVerdictPublishedAfterTheMergeDoesNotProveTheGate(t *testing.T) {
	pr := landedPR(4, "feat(api): add the thing", daysAgo(2))
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{pr},
		Statuses: map[string][]core.CommitStatus{pr.HeadSHA: {{
			Context:   engine.DefaultStatusContext,
			State:     core.StatusSuccess,
			CreatedAt: pr.MergedAt.Add(time.Minute),
		}}},
	})

	result := liveResult(t, env, engine.CheckLivePRsGated)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestAFailingVerdictIsNotASuccess guards the other half of the same read: the
// gate having run is not the gate having passed.
func TestAFailingVerdictIsNotASuccess(t *testing.T) {
	pr := landedPR(4, "feat(api): add the thing", daysAgo(2))
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{pr},
		Statuses: map[string][]core.CommitStatus{pr.HeadSHA: {{
			Context:   engine.DefaultStatusContext,
			State:     core.StatusFailure,
			CreatedAt: pr.MergedAt.Add(-time.Minute),
		}}},
	})

	wantState(t, liveResult(t, env, engine.CheckLivePRsGated), engine.StateFail)
}

// TestTheWindowIsAboutWhenWorkMergedNotWhenItWasTouched guards the second
// filter. Forges index pull requests by update time, and a comment on an old
// merge drags it into any window; counting it would make last quarter's
// mistakes this fortnight's health.
func TestTheWindowIsAboutWhenWorkMergedNotWhenItWasTouched(t *testing.T) {
	old := landedPR(2, "feat(api): last quarter", daysAgo(90))
	old.ClosingIssues = nil
	old.UpdatedAt = daysAgo(1)
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{old},
	})

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 0, 0)
}

// TestAnEnvThatWasNotToldHowFarBackIsNotBlind is the window's own default. A
// zero window reads to every adapter as "no bound", so the mistake would not
// look like a mistake: the audit would quietly become the whole history of
// the repository, or — once the second filter is applied — nothing at all.
func TestAnEnvThatWasNotToldHowFarBackIsNotBlind(t *testing.T) {
	orphan := landedPR(3, "feat(api): add the thing", daysAgo(1))
	orphan.ClosingIssues = nil
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{orphan},
	})
	env.Window = 0

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestABadFortnightWarnsBeforeItFails is spec §6's thresholds on a check that
// is not critical: everything holding is a pass, one in twenty is a warning,
// and three in twenty is below fail_below.
func TestABadFortnightWarnsBeforeItFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		unlinked int
		want     engine.State
	}{
		{"all of them linked", 0, engine.StatePass},
		{"one in twenty", 1, engine.StateWarn},
		{"three in twenty", 3, engine.StateFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prs []core.PullRequest
			for number := 1; number <= 20; number++ {
				pr := landedPR(number, "feat(api): number", daysAgo(1))
				if number <= tc.unlinked {
					pr.ClosingIssues = nil
				}
				prs = append(prs, pr)
			}
			env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs})
			wantState(t, liveResult(t, env, engine.CheckLivePRsLinked), tc.want)
		})
	}
}

// TestTheTeamsOwnThresholdDecidesWhereTheLineIs proves fail_below is read
// rather than assumed: the same fortnight that warns at the default fails for
// a team that asked for more.
func TestTheTeamsOwnThresholdDecidesWhereTheLineIs(t *testing.T) {
	var prs []core.PullRequest
	for number := 1; number <= 20; number++ {
		pr := landedPR(number, "feat(api): number", daysAgo(1))
		if number == 1 {
			pr.ClosingIssues = nil
		}
		prs = append(prs, pr)
	}
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs})
	env.FailBelow = 1

	wantState(t, liveResult(t, env, engine.CheckLivePRsLinked), engine.StateFail)
}

// TestAnEnvThatWasNotToldIsNotLenient is the other side of that: a zero
// threshold must not mean "nothing can fall below the line", because an Env
// built without one is the easiest mistake to make.
func TestAnEnvThatWasNotToldIsNotLenient(t *testing.T) {
	var prs []core.PullRequest
	for number := 1; number <= 10; number++ {
		pr := landedPR(number, "feat(api): number", daysAgo(1))
		if number <= 5 {
			pr.ClosingIssues = nil
		}
		prs = append(prs, pr)
	}
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs})
	env.FailBelow = 0

	wantState(t, liveResult(t, env, engine.CheckLivePRsLinked), engine.StateFail)
}

// TestTheTrunkIsLintedAndGitsOwnCommitsAreNot is live.prs.conventional. The
// history is what makes the log the list of work, and a commit git composed
// itself was nobody's wording to get right.
func TestTheTrunkIsLintedAndGitsOwnCommitsAreNot(t *testing.T) {
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		Commits: []core.Commit{
			{SHA: "aaaaaaaaaaaa", Message: "feat(api): add the thing", CommittedAt: daysAgo(1)},
			{SHA: "bbbbbbbbbbbb", Message: "Merge branch 'main' into topic", CommittedAt: daysAgo(2)},
			{SHA: "cccccccccccc", Message: "fixed the broken thing", CommittedAt: daysAgo(3)},
		},
	})

	result := liveResult(t, env, engine.CheckLivePRsConventional)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 2, 3)
	evidenceMentions(t, result, "cccccc")
	evidenceMentions(t, result, "fixed the broken thing")
}

// TestTheTrunksLintIsTheTeamsOwnConfiguration proves the audit lints by the
// convention the team wrote down rather than the linter's defaults: a scope
// nobody allowed is a scope that should never have landed.
func TestTheTrunksLintIsTheTeamsOwnConfiguration(t *testing.T) {
	cfg := liveConfig()
	cfg.Commits.Scopes = config.Scopes{Required: true, Allowed: []string{"api"}}
	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{
		Commits: []core.Commit{
			{SHA: "dddddddddddd", Message: "feat(billing): charge for the thing", CommittedAt: daysAgo(1)},
		},
	})

	result := liveResult(t, env, engine.CheckLivePRsConventional)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestAPersonMustHaveApprovedWhatAMachineWrote is live.prs.reviewed. An agent
// approving an agent is the failure mode the rule exists for, and work a
// person opened is not what this check is counting.
func TestAPersonMustHaveApprovedWhatAMachineWrote(t *testing.T) {
	human := landedPR(1, "feat(api): a person wrote this", daysAgo(1))
	reviewed := landedPR(2, "feat(api): a machine wrote this", daysAgo(2))
	reviewed.Author = "flow-bot"
	reviewed.Reviews = []core.Review{{Author: "dev", State: core.ReviewApproved}}
	nodded := landedPR(3, "feat(api): and this", daysAgo(3))
	nodded.Author = "flow-bot"
	nodded.Reviews = []core.Review{{Author: "flow-bot", State: core.ReviewApproved}}

	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{human, reviewed, nodded},
	})

	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 1, 2)
	evidenceMentions(t, result, "#3")
}

// TestOneUnreviewedAgentMergeFailsHoweverFewThereAre is the other half of
// spec §6's "critical when require_human_review": the condition is what makes
// the check exist at all, so whenever it runs there is no warning band. One
// agent merge in twenty with nobody's approval is 95%, comfortably above
// fail_below, and it is still a failure.
func TestOneUnreviewedAgentMergeFailsHoweverFewThereAre(t *testing.T) {
	var prs []core.PullRequest
	for number := 1; number <= 20; number++ {
		pr := landedPR(number, "feat(api): number", daysAgo(1))
		pr.Author = "flow-bot"
		if number > 1 {
			pr.Reviews = []core.Review{{Author: "dev", State: core.ReviewApproved}}
		}
		prs = append(prs, pr)
	}
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{PullRequests: prs})

	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 19, 20)
	evidenceMentions(t, result, "#1")
}

// TestTheAuditLetsThroughWhatTheGateLetsThrough: a gate that passes work the
// audit reports red a fortnight later is two readings of one rule, which is
// the disagreement issue #106 was. The exemption is read once, by both.
func TestTheAuditLetsThroughWhatTheGateLetsThrough(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.UnreviewedTypes = []string{"chore"}

	routine := landedPR(3, "chore(deps): bump the toolchain", daysAgo(3))
	routine.Author = "flow-bot"
	routine.Files = core.FileSummary{Changed: 1, Paths: []string{"go.mod"}}
	feature := landedPR(4, "feat(api): a machine wrote this", daysAgo(2))
	feature.Author = "flow-bot"
	feature.Files = core.FileSummary{Changed: 1, Paths: []string{"api/thing.go"}}

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{routine, feature},
	})

	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
	evidenceMentions(t, result, "#4")
	for _, evidence := range result.Evidence {
		if strings.Contains(evidence.Text, "#3") {
			t.Fatalf("the audit reported work the gate excused: %s", evidence.Text)
		}
	}
}

// TestAFortnightOfRoutineWorkSaysSoRatherThanNothing: an exempt pull request
// is not a pull request nobody wrote. Reporting "nothing a machine wrote
// merged" over a fortnight of agent chores would be a quiet lie about a
// window flow read perfectly well.
func TestAFortnightOfRoutineWorkSaysSoRatherThanNothing(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.UnreviewedTypes = []string{"chore"}

	routine := landedPR(3, "chore(deps): bump the toolchain", daysAgo(3))
	routine.Author = "flow-bot"
	routine.Files = core.FileSummary{Changed: 1, Paths: []string{"go.mod"}}

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{PullRequests: []core.PullRequest{routine}})

	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "unattended") {
		t.Fatalf("the report does not say what it let through: %q", result.Message)
	}
}

// TestWorkAPersonApprovedIsCountedBesideWorkThatNeededNobody: the share the
// check grades is of the work the team asked to be reviewed, and the rest is
// still worth saying out loud.
func TestWorkAPersonApprovedIsCountedBesideWorkThatNeededNobody(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.UnreviewedTypes = []string{"chore"}

	routine := landedPR(3, "chore(deps): bump the toolchain", daysAgo(3))
	routine.Author = "flow-bot"
	routine.Files = core.FileSummary{Changed: 1, Paths: []string{"go.mod"}}
	feature := landedPR(4, "feat(api): a machine wrote this", daysAgo(2))
	feature.Author = "flow-bot"
	feature.Files = core.FileSummary{Changed: 1, Paths: []string{"api/thing.go"}}
	feature.Reviews = []core.Review{{Author: "dev", State: core.ReviewApproved}}

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{
		PullRequests: []core.PullRequest{routine, feature},
	})

	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StatePass)
	wantCounts(t, result, 1, 1)
	if !strings.Contains(result.Message, "1 more needed none") {
		t.Fatalf("the report does not say what it let through: %q", result.Message)
	}
}

// TestATeamThatDidNotAskForAHumanReviewIsNotAudited: there is no claim to hold
// them to, which is the same answer the gate's agent.human_review rule gives.
func TestATeamThatDidNotAskForAHumanReviewIsNotAudited(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.RequireHumanReview = false
	nodded := landedPR(3, "feat(api): a machine wrote this", daysAgo(3))
	nodded.Author = "flow-bot"

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{PullRequests: []core.PullRequest{nodded}})
	wantState(t, liveResult(t, env, engine.CheckLivePRsReviewed), engine.StateSkip)
}

// TestAnAgentIsRecognisedByItsLabelToo keeps the audit and the gate reading
// the same definition of agent work (spec 09 §2).
func TestAnAgentIsRecognisedByItsLabelToo(t *testing.T) {
	cfg := liveConfig()
	cfg.Agents.Labels = []string{"agent"}
	labelled := landedPR(4, "feat(api): a machine wrote this", daysAgo(1))
	labelled.Labels = []string{"agent"}

	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{PullRequests: []core.PullRequest{labelled}})
	result := liveResult(t, env, engine.CheckLivePRsReviewed)
	wantState(t, result, engine.StateFail)
	wantCounts(t, result, 0, 1)
}

// TestTheWindowIsNamedInOneOrderHoweverTheForgeAnswers: the forge returns
// pull requests in whatever order its own index is in — most recently touched,
// here — and a report whose evidence reshuffles between runs is noise. Item
// 5.7 writes this list into an issue that is edited in place, so the order has
// to be the audit's own.
func TestTheWindowIsNamedInOneOrderHoweverTheForgeAnswers(t *testing.T) {
	recent := landedPR(9, "feat(api): the newer one", daysAgo(1))
	recent.ClosingIssues = nil
	older := landedPR(2, "feat(api): the older one", daysAgo(3))
	older.ClosingIssues = nil
	env, _ := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{recent, older},
	})

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantCounts(t, result, 0, 2)
	var named []string
	for _, seen := range result.Evidence {
		named = append(named, strings.Fields(seen.Text)[0])
	}
	if want := []string{"#2", "#9"}; !slices.Equal(named, want) {
		t.Errorf("the evidence names %v, want %v", named, want)
	}
}

// TestTheWindowIsReadOnceForEveryCheckThatNeedsIt is why [engine.LiveChecks]
// returns a slice built together. Nine checks asking the providers the same
// questions would be nine times the rate limit and nine slightly different
// fortnights. There are exactly three questions: the trunk, the merges in the
// window, and what is still open — and the board, which boardEnv's own test
// counts.
func TestTheWindowIsReadOnceForEveryCheckThatNeedsIt(t *testing.T) {
	prs := []core.PullRequest{landedPR(1, "feat(api): add the thing", daysAgo(1))}
	env, forge := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: prs,
		Statuses:     passing(prs...),
	})

	liveRun(t, env, nil)

	if got := forge.Calls("PullRequests"); got != 2 {
		t.Errorf("the pull requests were read %d times, want 2: the window's merges and what is open", got)
	}
	if got := forge.Calls("Repo"); got != 1 {
		t.Errorf("the repository was read %d times, want 1", got)
	}
}

// TestACheckNobodyAskedForCostsNothing is the other half of reading once:
// `--check live.prs.conventional` must not pay for the pull requests it is
// never going to look at.
func TestACheckNobodyAskedForCostsNothing(t *testing.T) {
	prs := []core.PullRequest{landedPR(1, "feat(api): add the thing", daysAgo(1))}
	env, forge := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: prs,
		Statuses:     passing(prs...),
	})

	liveRun(t, env, []string{engine.CheckLivePRsConventional})

	if got := forge.Calls("PullRequests"); got != 0 {
		t.Errorf("the pull requests were read %d times for a check that does not read them", got)
	}
}

// TestAForgeThatWillNotAnswerIsAnErrorNotHealth: a live check that could not
// find out must say so. Reporting an empty window would be the one failure
// mode a daily health check cannot have — silence that looks like health.
func TestAForgeThatWillNotAnswerIsAnErrorNotHealth(t *testing.T) {
	env, forge := liveEnv(t, liveConfig(), coretest.ForgeFixture{
		PullRequests: []core.PullRequest{landedPR(1, "feat(api): add the thing", daysAgo(1))},
	})
	forge.Fail("PullRequests", core.Errorf(core.ErrUnavailable, "read the pull requests").
		WithWhy("the forge is having a bad day"))

	result := liveResult(t, env, engine.CheckLivePRsLinked)
	wantState(t, result, engine.StateError)
	if result.Err == nil {
		t.Error("an error result carries no error, so the run cannot choose its exit code")
	}
}
