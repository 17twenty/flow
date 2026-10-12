package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The setup checks of spec 07 §5. These tests are about what each check
// decides, which is the part a person reads every morning; the frame that runs
// them — ordering, blocking, exit codes — is doctor_test.go's.

// setupConfig is a workflow that is wired up: a GitHub repository, a board,
// commit linting on, and work finished on merge. A test that is about one
// check changes the one field it is about and leaves the rest alone, so that
// the thing under test is the only difference from a working setup.
func setupConfig() *config.Config {
	return &config.Config{
		Version: config.Version,
		Forge: config.Forge{
			Type: "github",
			Repo: "acme/api",
			Auth: config.Auth{TokenEnv: "GITHUB_TOKEN"},
		},
		Tracker: config.Tracker{
			Type: "github-projects",
			Auth: config.Auth{TokenEnv: "FLOW_PROJECT_TOKEN"},
		},
		Workflow: config.Workflow{
			DoneOn: config.DoneOnMerged,
			Transitions: map[string]config.Transition{
				"pr_merged":       {Owner: config.OwnerNative, To: "Done"},
				"issue_closed":    {Owner: config.OwnerNative, To: "Done"},
				"pr_opened_draft": {Owner: config.OwnerFlow, To: "In progress"},
			},
		},
		Commits: config.Commits{Convention: "conventional"},
	}
}

// setupLocal is a working copy that is the repository the configuration is
// about, with flow's hook installed.
func setupLocal() engine.Local {
	return engine.Local{
		Repo:          "acme/api",
		Remote:        "https://github.com/acme/api.git",
		Branch:        "main",
		HookPath:      "/repo/.git/hooks/commit-msg",
		HookInstalled: true,
	}
}

// setupEnv is the world the setup checks are asked in.
func setupEnv(t *testing.T, cfg *config.Config) *engine.Env {
	t.Helper()
	env := doctorEnv(t)
	env.Config = cfg
	return env
}

// setupResult runs one setup check on its own and returns what it decided.
//
// Running the one check rather than the whole registry is deliberate: a
// dependency that fails would turn the answer into `blocked`, and a test about
// what a check decides must not be able to pass because something else broke
// first.
func setupResult(t *testing.T, in engine.SetupInput, env *engine.Env, id string) engine.Result {
	t.Helper()
	for _, check := range engine.SetupChecks(in) {
		if check.ID != id {
			continue
		}
		result := check.Run(t.Context(), env)
		if result.State == "" {
			t.Fatalf("%s answered nothing", id)
		}
		return result
	}
	t.Fatalf("there is no %s check; the registry holds %v", id, setupIDs(in))
	return engine.Result{}
}

// setupIDs is every check id, for a failure message that says what was there.
func setupIDs(in engine.SetupInput) []string {
	var ids []string
	for _, check := range engine.SetupChecks(in) {
		ids = append(ids, check.ID)
	}
	return ids
}

// wantState fails unless the check reached the state the spec gives it.
func wantState(t *testing.T, got engine.Result, want engine.State) {
	t.Helper()
	if got.State != want {
		t.Errorf("state is %q, want %q: %s", got.State, want, got.Message)
	}
}

// TestSetupRegistryBuildsAndCoversTheSpecsList holds the one property the list
// itself has: every check spec 07 §5 names is registered, each exactly once,
// and the registry accepts the dependencies they declare.
func TestSetupRegistryBuildsAndCoversTheSpecsList(t *testing.T) {
	in := engine.SetupInput{Config: setupConfig(), Local: setupLocal()}
	if _, err := engine.SetupRegistry(in); err != nil {
		t.Fatalf("the setup registry does not build: %v", err)
	}

	seen := map[string]int{}
	for _, id := range setupIDs(in) {
		seen[id]++
	}
	for _, id := range []string{
		engine.CheckConfigSyntax, engine.CheckConfigSemantics, engine.CheckConfigSecrets,
		engine.CheckLockConsistent, engine.CheckLocalGit, engine.CheckLocalHook,
		engine.CheckAuthForge, engine.CheckAuthTracker, engine.CheckForgeRepo,
		engine.CheckDeploysSource, engine.CheckFollowUpsHealth,
		core.ItemTrackerProject, core.ItemTrackerStatusOptions, core.ItemTrackerLabels,
		core.ItemTrackerWIP, core.ItemTrackerExpedite,
		core.ItemForgeProtection, core.ItemForgeRequiredStatus,
		core.ItemForgeMergeSettings, core.ItemForgeFiles,
	} {
		if seen[id] != 1 {
			t.Errorf("%s is registered %d times, want once", id, seen[id])
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s is registered %d times", id, n)
		}
	}
}

// A native transition is a check of its own, because it is a thing the tracker
// is supposed to be doing by itself and nobody would otherwise notice it had
// stopped. The order must come from the configuration's content and not from
// its map iteration, or two runs of one file would report the same checks in a
// different order every time.
func TestNativeTransitionsBecomeChecksInASettledOrder(t *testing.T) {
	in := engine.SetupInput{Config: setupConfig(), Local: setupLocal()}

	var native []string
	for _, id := range setupIDs(in) {
		if strings.HasPrefix(id, "tracker.native.") {
			native = append(native, id)
		}
	}
	want := []string{"tracker.native.issue_closed", "tracker.native.pr_merged"}
	if len(native) != len(want) {
		t.Fatalf("the native transitions are %v, want %v", native, want)
	}
	for i := range want {
		if native[i] != want[i] {
			t.Fatalf("the native transitions are %v, want %v (sorted)", native, want)
		}
	}

	// A flow-owned transition is flow's own job, performed by `flow check
	// pr`, so there is nothing about the tracker to check.
	for _, id := range native {
		if id == "tracker.native.pr_opened_draft" {
			t.Error("a flow-owned transition was registered as a native one")
		}
	}
}

// A configuration nobody can read is the one failure that must still produce a
// report rather than a refusal to start: it is the whole reason doctor loads
// the session with [Context.inspect] instead of [Context.open].
func TestConfigSyntaxReportsWhyItCouldNotBeRead(t *testing.T) {
	in := engine.SetupInput{
		ConfigPath: "repo/.flow.yaml",
		ConfigErr:  errors.New("line 7: field trackr not found"),
	}
	result := setupResult(t, in, setupEnv(t, nil), engine.CheckConfigSyntax)

	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Message, "repo/.flow.yaml") {
		t.Errorf("the failure does not name the file: %s", result.Message)
	}
	if len(result.Evidence) == 0 || !strings.Contains(result.Evidence[0].Text, "line 7") {
		t.Errorf("the failure does not carry the parse error: %+v", result.Evidence)
	}
	if result.Remediation.Kind != engine.RemedyManual {
		t.Errorf("remedy is %q, want %q: flow cannot fix a file it cannot read", result.Remediation.Kind, engine.RemedyManual)
	}
}

// A file that parses passes, and names itself while doing so: a run in the
// wrong directory is the commonest confusion there is.
func TestConfigSyntaxPasses(t *testing.T) {
	in := engine.SetupInput{Config: setupConfig(), ConfigPath: "repo/.flow.yaml"}
	result := setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckConfigSyntax)

	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "repo/.flow.yaml") {
		t.Errorf("the passing line does not name the file: %s", result.Message)
	}
}

// Spec 15: a credential in a file that is committed is a credential that has
// been published. The refusal must say where to look without printing what it
// found — a doctor that quotes the secret into CI output has leaked it twice.
func TestConfigSecretsSaysWhereButNotWhat(t *testing.T) {
	secret := "gh" + "p_" + strings.Repeat("A", 36)
	source := "version: 1\nforge:\n  token: " + secret + "\n"
	in := engine.SetupInput{Config: setupConfig(), ConfigPath: ".flow.yaml", Source: []byte(source)}

	result := setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckConfigSecrets)

	wantState(t, result, engine.StateFail)
	if len(result.Evidence) == 0 || !strings.Contains(result.Evidence[0].Text, "line 3") {
		t.Fatalf("the refusal does not say which line: %+v", result.Evidence)
	}
	whole := result.Message + result.Remediation.Summary + strings.Join(result.Remediation.Steps, " ")
	for _, evidence := range result.Evidence {
		whole += evidence.Text
	}
	if strings.Contains(whole, secret) {
		t.Error("the refusal printed the credential it was complaining about")
	}
	if !strings.Contains(strings.Join(result.Remediation.Steps, " "), "revoke") {
		t.Errorf("the remedy does not say to revoke it: %+v", result.Remediation.Steps)
	}
}

// The scan is of the bytes flow read. Where it did not read them — a session
// built some other way — the honest answer is that it did not look, not that
// the file is clean.
func TestConfigSecretsSkipsWhatItDidNotRead(t *testing.T) {
	in := engine.SetupInput{Config: setupConfig()}
	wantState(t, setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckConfigSecrets), engine.StateSkip)
}

func TestConfigSecretsPassesAFileThatNamesItsCredentials(t *testing.T) {
	source := "version: 1\nforge:\n  auth:\n    token_env: GITHUB_TOKEN\n"
	in := engine.SetupInput{Config: setupConfig(), Source: []byte(source)}
	wantState(t, setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckConfigSecrets), engine.StatePass)
}

// The lock is flow's memory. One written by a newer flow is one this build
// must not edit: it would silently drop whatever that flow knew and the team
// would lose attestations they had made.
func TestLockFromANewerFlowIsRefusedRatherThanRewritten(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Lock = config.NewLock("v9.0.0")
	env.Lock.Version = config.LockVersion + 1

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckLockConsistent)

	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Remediation.Summary, "upgrade") {
		t.Errorf("the remedy does not say to upgrade flow: %q", result.Remediation.Summary)
	}
}

// Editing the workflow is ordinary, and must not read as a broken lock. What
// it does mean is that an attestation taken against the whole file has lapsed,
// and the passing line says so rather than staying silent about it.
func TestAConfigurationEditedSinceTheLockStillPasses(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Lock = config.NewLock("v0.1.0")
	env.Lock.ConfigHash = "sha256:old"
	env.Plan = engine.Plan{ConfigHash: "sha256:new"}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckLockConsistent)

	wantState(t, result, engine.StatePass)
	if len(result.Evidence) == 0 || !strings.Contains(result.Evidence[0].Text, "lapsed") {
		t.Errorf("the pass does not mention the lapsed attestation: %+v", result.Evidence)
	}
}

// A repository with no lock yet has not got one to be wrong about.
func TestNoLockIsNotAFailure(t *testing.T) {
	wantState(t, setupResult(t, engine.SetupInput{Config: setupConfig()}, setupEnv(t, setupConfig()), engine.CheckLockConsistent), engine.StateSkip)
}

// Running flow in the wrong clone makes everything else it says true of some
// other repository, and wrong without looking wrong. This is the check that
// catches it.
func TestLocalGitCatchesTheWrongWorkingCopy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local engine.Local
		state engine.State
		says  string
	}{
		{"the right one", setupLocal(), engine.StatePass, "acme/api"},
		{
			"another repository",
			engine.Local{Repo: "acme/web", Remote: "https://github.com/acme/web.git"},
			engine.StateFail, "acme/web",
		},
		{
			"no origin flow recognised",
			engine.Local{Remote: "/srv/git/api.git"},
			engine.StateFail, "acme/api",
		},
		{
			"not a working copy at all",
			engine.Local{Err: errors.New("not a git repository")},
			engine.StateFail, "working copy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := engine.SetupInput{Config: setupConfig(), Local: tc.local}
			result := setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckLocalGit)
			wantState(t, result, tc.state)
			if !strings.Contains(result.Message, tc.says) {
				t.Errorf("the line does not mention %q: %s", tc.says, result.Message)
			}
		})
	}
}

// A forge that does not care about case in a slug must not produce a red check
// every morning for a clone whose remote is spelled Acme/API.
func TestLocalGitDoesNotCareAboutCase(t *testing.T) {
	local := setupLocal()
	local.Repo = "Acme/API"
	in := engine.SetupInput{Config: setupConfig(), Local: local}

	wantState(t, setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckLocalGit), engine.StatePass)
}

// The hook is the one check whose subject is a convenience, and the states it
// can reach are what make it worth having: an uninstalled hook is silent until
// CI says so, and somebody else's hook is not flow's to overwrite.
func TestLocalHook(t *testing.T) {
	foreign := setupLocal()
	foreign.HookInstalled, foreign.HookForeign = false, true

	missing := setupLocal()
	missing.HookInstalled = false

	managed := setupLocal()
	managed.HookInstalled = false
	managed.HookManagers = []string{"pre-commit"}

	for _, tc := range []struct {
		name  string
		local engine.Local
		linty bool
		state engine.State
		remdy string
	}{
		{"installed", setupLocal(), true, engine.StatePass, ""},
		{"missing", missing, true, engine.StateFail, engine.RemedyAuto},
		{"somebody else's", foreign, true, engine.StateFail, engine.RemedyManual},
		{"a hook manager owns it", managed, true, engine.StateSkip, ""},
		{"no convention to lint", missing, false, engine.StateSkip, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupConfig()
			if !tc.linty {
				cfg.Commits.Convention = "none"
			}
			in := engine.SetupInput{Config: cfg, Local: tc.local}
			result := setupResult(t, in, setupEnv(t, cfg), engine.CheckLocalHook)

			wantState(t, result, tc.state)
			if result.Remediation.Kind != tc.remdy {
				t.Errorf("remedy is %q, want %q", result.Remediation.Kind, tc.remdy)
			}
			if tc.remdy == engine.RemedyAuto && result.Remediation.Command != "flow hook install" {
				t.Errorf("the automatic remedy is %q, want flow hook install", result.Remediation.Command)
			}
		})
	}
}

// probeFails is an adapter whose credential is refused. It wraps a working
// fake rather than reimplementing one, so the test says only what it is about.
type probeFails struct {
	core.Forge
	err error
}

func (p probeFails) Probe(context.Context) (core.Permissions, error) {
	return core.Permissions{}, p.err
}

// repoFails is the same for reachability: a credential that is accepted and a
// repository it still cannot see.
type repoFails struct {
	core.Forge
	err error
}

func (r repoFails) Repo(context.Context) (core.RepoInfo, error) { return core.RepoInfo{}, r.err }

// setupForge is a forge that answers.
func setupForge(t *testing.T) *coretest.FakeForge {
	t.Helper()
	return coretest.NewFakeForge(coretest.ForgeFixture{
		Kind:        "github",
		Permissions: core.Permissions{Tokens: []string{"admin"}, Evidence: []string{"repository permission: admin"}},
		Repo:        core.RepoInfo{Owner: "acme", Name: "api", DefaultBranch: "main"},
		Clock:       coretest.NewClock(time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)),
	})
}

// A credential that resolves but allows nothing is still a pass. What a token
// may do is the planner's business item by item (spec 05 §4): a missing
// permission becomes a follow-up on the one thing it blocks, and turning it
// into a red mark against the whole provider would hide which thing that is.
func TestAuthPassesEvenWhenTheTokenAllowsNothing(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Adapters.Forge = coretest.NewFakeForge(coretest.ForgeFixture{Kind: "github"})

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckAuthForge)

	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "github") {
		t.Errorf("the line does not name the provider that accepted it: %s", result.Message)
	}
}

// A credential the provider refused is the credential's problem, and the
// remedy is a human's: flow cannot mint a token.
func TestAuthReportsARefusedCredential(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Adapters.Forge = probeFails{Forge: setupForge(t), err: errors.New("401 Bad credentials")}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckAuthForge)

	if result.State != engine.StateFail && result.State != engine.StateError {
		t.Errorf("state is %q, want a failure", result.State)
	}
	if result.Remediation.Kind != engine.RemedyInput {
		t.Errorf("remedy is %q, want %q", result.Remediation.Kind, engine.RemedyInput)
	}
	if !strings.Contains(strings.Join(result.Remediation.Steps, " "), "GITHUB_TOKEN") {
		t.Errorf("the remedy does not name where flow looks: %+v", result.Remediation.Steps)
	}
}

// A credential flow could not resolve at all is the same answer from the other
// direction, and is what [DoctorCmd.world] produces when connecting fails:
// `auth.forge` and `auth.tracker` fail, rather than doctor refusing to run.
func TestAuthReportsACredentialThatWasNeverResolved(t *testing.T) {
	in := engine.SetupInput{
		Config:      setupConfig(),
		Unavailable: map[string]error{engine.TargetTracker: errors.New("FLOW_PROJECT_TOKEN is not set")},
	}
	result := setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckAuthTracker)

	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Message, engine.TargetTracker) {
		t.Errorf("the line does not say which provider: %s", result.Message)
	}
	if !strings.Contains(strings.Join(result.Remediation.Steps, " "), "FLOW_PROJECT_TOKEN") {
		t.Errorf("the remedy does not name the tracker's variable: %+v", result.Remediation.Steps)
	}
}

// Reachability and the trunk are one check because they fail together: a slug
// that is wrong, a repository that was renamed and a credential that cannot
// see it all produce the same reply.
func TestForgeRepoReportsOneProblemOnce(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Adapters.Forge = repoFails{Forge: setupForge(t), err: errors.New("404 Not Found")}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckForgeRepo)

	if result.State != engine.StateFail && result.State != engine.StateError {
		t.Errorf("state is %q, want a failure", result.State)
	}
	if !strings.Contains(result.Message, "acme/api") {
		t.Errorf("the failure does not name the repository: %s", result.Message)
	}
}

// Where nothing in the configuration asks for a particular trunk, reachability
// was the whole question, and the answer names the branch it found so that a
// mismatch is visible even when it is not an error.
func TestForgeRepoPassesAndNamesTheDefaultBranch(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Adapters.Forge = setupForge(t)

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckForgeRepo)

	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "main") {
		t.Errorf("the line does not name the default branch: %s", result.Message)
	}
}

// A board flow has not been told which one it is is not broken: it is
// unanswered. `input` is what `--fix` and `--set` act on, and it is not a
// failure of the setup (spec 07 §3).
func TestAnUnresolvedBoardIsAQuestionNotAFailure(t *testing.T) {
	in := engine.SetupInput{
		Config: setupConfig(),
		Placeholders: []engine.Answer{{
			Placeholder: engine.Placeholder{Path: core.ItemTrackerProject, Name: "project", Discoverable: true},
			How:         engine.HowAsked,
			Candidates: []core.Candidate{
				{What: core.ItemTrackerProject, ID: "1", Name: "flow"},
				{What: core.ItemTrackerProject, ID: "2", Name: "platform"},
			},
		}},
	}
	result := setupResult(t, in, setupEnv(t, setupConfig()), core.ItemTrackerProject)

	wantState(t, result, engine.StateInput)
}

// Everything else about the board stands behind this check, so an unanswered
// board blocks the tracker checks rather than failing them one by one: twenty
// red lines for one unanswered question is how a report stops being read.
func TestAnUnresolvedBoardBlocksTheTrackerChecksRatherThanFailingThem(t *testing.T) {
	in := engine.SetupInput{
		Config: setupConfig(),
		Local:  setupLocal(),
		Placeholders: []engine.Answer{{
			Placeholder: engine.Placeholder{Path: core.ItemTrackerProject, Name: "project", Discoverable: true},
			How:         engine.HowAsked,
		}},
	}
	env := setupEnv(t, setupConfig())
	env.Adapters.Forge = setupForge(t)
	env.Adapters.Tracker = coretest.NewFakeTracker(coretest.TrackerFixture{Kind: "github-projects"})

	report := doctorRun(t, env, nil, engine.SetupChecks(in)...)

	wantState(t, mustFind(t, report, core.ItemTrackerProject), engine.StateInput)
	for _, id := range []string{core.ItemTrackerStatusOptions, core.ItemTrackerLabels, core.ItemTrackerWIP} {
		if got := mustFind(t, report, id); got.State != engine.StateBlocked {
			t.Errorf("%s is %q, want %q: it stands behind an unanswered board", id, got.State, engine.StateBlocked)
		}
	}
}

// mustFind is one check's result out of a report.
func mustFind(t *testing.T, report engine.Report, id string) engine.Result {
	t.Helper()
	checked, ok := report.Find(id)
	if !ok {
		t.Fatalf("the report has no %s", id)
	}
	return checked.Result
}

// Most teams record no deployments and never ask flow to read one, so the
// deployment check is a skip that says why rather than a failure nobody can
// act on.
func TestDeploysSourceIsSkippedWhenThereAreNoRecords(t *testing.T) {
	cfg := setupConfig()
	cfg.Deploys = config.Deploys{Source: config.DeploySourceNone}

	result := setupResult(t, engine.SetupInput{Config: cfg}, setupEnv(t, cfg), engine.CheckDeploysSource)

	wantState(t, result, engine.StateSkip)
	if !strings.Contains(result.Message, "deploys.source") {
		t.Errorf("the skip does not say why: %s", result.Message)
	}
}

// A team whose work is done when it merges still holds itself to `max_lag`,
// and that audit reads the records this check proves are readable. Gating the
// check on `done_on: deployed` would skip it for exactly the repositories
// whose live-health audit depends on it — flow's own among them.
func TestDeploysSourceIsAskedOfATeamThatFinishesOnTheMerge(t *testing.T) {
	cfg := setupConfig()
	cfg.Workflow.DoneOn = config.DoneOnMerged
	cfg.Deploys = config.Deploys{
		Source:      config.DeploySourceForge,
		Environment: "production",
		MaxLag:      config.Duration(2 * time.Hour),
	}
	env := setupEnv(t, cfg)
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:      core.ItemDeploysRecords,
		Target:  engine.TargetDeploys,
		Outcome: engine.OutcomeNoop,
		Why:     "production has deployment records flow can read",
	}}}

	result := setupResult(t, engine.SetupInput{Config: cfg}, env, engine.CheckDeploysSource)

	wantState(t, result, engine.StatePass)
}

// A follow-up is how flow admits it cannot do something. One nobody has acted
// on for a month is a thing silently not happening, which is exactly what a
// daily check exists to stop — but it is somebody else's work, so it reads as
// `external` rather than as flow being broken.
func TestAStaleFollowUpStopsBeingHealthy(t *testing.T) {
	now := time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)
	env := setupEnv(t, setupConfig())
	env.Clock = coretest.NewClock(now)
	env.Lock = config.NewLock("v0.1.0")
	env.Lock.FollowUps = []config.FollowUp{{
		Check:    core.ItemForgeProtection,
		Ticket:   "acme/api#41",
		State:    engine.FollowUpFiled,
		OpenedAt: now.Add(-engine.StaleFollowUp - time.Hour),
	}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckFollowUpsHealth)

	wantState(t, result, engine.StateExternal)
	if !strings.Contains(result.Message, "acme/api#41") {
		t.Errorf("the line does not name the ticket that is stuck: %s", result.Message)
	}
	if result.Remediation.Kind != engine.RemedyFollowUp {
		t.Errorf("remedy is %q, want %q", result.Remediation.Kind, engine.RemedyFollowUp)
	}
}

// A follow-up filed yesterday is a team working normally, not a failure.
func TestAFreshFollowUpIsHealthy(t *testing.T) {
	now := time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)
	env := setupEnv(t, setupConfig())
	env.Clock = coretest.NewClock(now)
	env.Lock = config.NewLock("v0.1.0")
	env.Lock.FollowUps = []config.FollowUp{{
		Check: core.ItemForgeProtection, Ticket: "acme/api#41",
		State: engine.FollowUpFiled, OpenedAt: now.Add(-24 * time.Hour),
	}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckFollowUpsHealth)

	wantState(t, result, engine.StatePass)
	if !strings.Contains(result.Message, "1 follow-up open") {
		t.Errorf("the passing line does not say what is outstanding: %s", result.Message)
	}
}

// A follow-up that has been verified or withdrawn is finished work, and must
// not keep the check company for ever.
func TestASettledFollowUpIsNotOutstanding(t *testing.T) {
	now := time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)
	env := setupEnv(t, setupConfig())
	env.Clock = coretest.NewClock(now)
	env.Lock = config.NewLock("v0.1.0")
	env.Lock.FollowUps = []config.FollowUp{
		{Check: "a", Ticket: "acme/api#1", State: engine.FollowUpVerified, OpenedAt: now.Add(-365 * 24 * time.Hour)},
		{Check: "b", Ticket: "acme/api#2", State: engine.FollowUpWithdrawn, OpenedAt: now.Add(-365 * 24 * time.Hour)},
	}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, engine.CheckFollowUpsHealth)

	wantState(t, result, engine.StatePass)
	if strings.Contains(result.Message, "open") {
		t.Errorf("a settled follow-up is still being counted: %s", result.Message)
	}
}

// A follow-up flow cannot route is one that will sit unassigned. That is worth
// knowing before there is anything to route, which is why the owners are
// checked whether or not anything is open.
func TestFollowUpsNeedSomebodyToGiveThemTo(t *testing.T) {
	in := engine.SetupInput{
		Config: setupConfig(),
		Placeholders: []engine.Answer{{
			Placeholder: engine.Placeholder{Path: "followups.owners.forge_admin", Name: "forge_admin"},
			How:         engine.HowAsked,
		}},
	}
	result := setupResult(t, in, setupEnv(t, setupConfig()), engine.CheckFollowUpsHealth)

	wantState(t, result, engine.StateFail)
	if !strings.Contains(result.Remediation.Command, "followups.owners.forge_admin=") {
		t.Errorf("the remedy does not say how to answer it: %q", result.Remediation.Command)
	}
}

// outcomeResult is the mapping from what apply would do to what doctor says
// (spec 05 §4, spec 07 §2). It is the busiest line in the file: sixteen of the
// twenty-odd checks are nothing but this.
func TestAPlannedItemBecomesTheStateItsOutcomeImplies(t *testing.T) {
	for _, tc := range []struct {
		outcome engine.Outcome
		state   engine.State
		remedy  string
	}{
		{engine.OutcomeNoop, engine.StatePass, ""},
		{engine.OutcomeCompensated, engine.StatePass, ""},
		{engine.OutcomeNative, engine.StateFail, engine.RemedyAuto},
		{engine.OutcomeFollowUp, engine.StateFail, engine.RemedyFollowUp},
		{engine.OutcomeAttest, engine.StateAttest, engine.RemedyAttest},
		{engine.OutcomeUnsupported, engine.StateSkip, ""},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			env := setupEnv(t, setupConfig())
			env.Plan = engine.Plan{Items: []engine.PlanItem{{
				ID:        core.ItemForgeProtection,
				Target:    engine.TargetForge,
				Outcome:   tc.outcome,
				Why:       "because the test said so",
				Mechanism: "a branch rule",
			}}}

			result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

			wantState(t, result, tc.state)
			if result.Remediation.Kind != tc.remedy {
				t.Errorf("remedy is %q, want %q", result.Remediation.Kind, tc.remedy)
			}
			if tc.state != engine.StateAttest && !strings.Contains(result.Message, "because the test said so") {
				t.Errorf("the line does not carry the plan's reason: %s", result.Message)
			}
		})
	}
}

// A compensated item passes, and the passing line has to say what is
// enforcing it. The intent holds, but not by the mechanism the configuration
// named, and a pass that keeps that to itself hides a real difference between
// what was asked for and what is there — which is the next person's surprise
// when they edit the thing the configuration names and nothing changes.
func TestACompensatedItemNamesWhatIsEnforcingItInstead(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:        core.ItemForgeProtection,
		Target:    engine.TargetForge,
		Outcome:   engine.OutcomeCompensated,
		Why:       "the trunk is protected",
		Mechanism: "a branch rule",
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	wantState(t, result, engine.StatePass)
	if len(result.Evidence) == 0 || !strings.Contains(result.Evidence[0].Text, "a branch rule") {
		t.Errorf("the pass does not say what is enforcing it instead: %+v", result.Evidence)
	}
}

// An item this configuration does not ask for is not a question about this
// team. A workflow with no deployments must not be told every morning that its
// deployment records are missing.
func TestAnItemTheConfigurationDoesNotAskForIsSkipped(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: nil}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	wantState(t, result, engine.StateSkip)
}

// An outcome flow does not know is an error and not a silent pass. The planner
// and the setup checks are two lists of the same six names, and a seventh
// added to one and not the other must be loud.
func TestAnUnknownOutcomeIsAnError(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID: core.ItemForgeProtection, Outcome: engine.Outcome("teleport"),
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	wantState(t, result, engine.StateError)
	if !strings.Contains(result.Message, "teleport") {
		t.Errorf("the error does not name the outcome it did not understand: %s", result.Message)
	}
}

// A refused item is the one case where flow knows the whole of a change and
// is only short of permission to make it. The ticket it raises is therefore
// the one ticket that can carry the change in full — and it has to, because
// "somebody with the access has to do it" was the summary of every refused
// item in a repository, which made two such tickets indistinguishable in the
// issue list (spec 07 §4).
func TestARefusedItemHandsOverEverythingFlowKnows(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:      core.ItemForgeProtection,
		Target:  engine.TargetForge,
		Outcome: engine.OutcomeFollowUp,
		Why:     "the trunk takes anything anyone pushes",
		Missing: []string{"admin"},
		Changes: []core.Change{{
			Op:   core.ChangeCreate,
			What: "ruleset",
			Item: core.Item{
				ID:     core.ItemForgeProtection,
				Name:   "flow",
				Fields: map[string]string{"required_approvals": "1", "branch": "main"},
			},
		}},
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	wantState(t, result, engine.StateFail)
	// The summary titles the ticket. It has to name this item's work and the
	// access that is short, or two of them read the same.
	if !strings.Contains(result.Remediation.Summary, "The trunk is protected") {
		t.Errorf("the summary does not name the work: %q", result.Remediation.Summary)
	}
	if !strings.Contains(result.Remediation.Summary, "admin") {
		t.Errorf("the summary does not say what access is short: %q", result.Remediation.Summary)
	}
	steps := strings.Join(result.Remediation.Steps, "\n")
	if !strings.Contains(steps, "admin") {
		t.Errorf("no step asks for the access that was refused:\n%s", steps)
	}
	// The change, fields and all. A person reproducing it by hand needs the
	// values, not a summary of them.
	if !strings.Contains(steps, `create ruleset "flow"`) {
		t.Errorf("the steps do not carry the change flow planned:\n%s", steps)
	}
	if !strings.Contains(steps, "branch=main") || !strings.Contains(steps, "required_approvals=1") {
		t.Errorf("the steps do not carry the fields flow would have set:\n%s", steps)
	}
	// Fields are sorted, so the same refusal reads the same every morning and
	// the ticket is not rewritten by map order alone.
	if !strings.Contains(steps, "with branch=main, required_approvals=1") {
		t.Errorf("the fields are not in a settled order:\n%s", steps)
	}
	if !hasEvidence(result, `create ruleset "flow"`) {
		t.Errorf("the report itself does not show the change:\n%+v", result.Evidence)
	}
}

// A refused item with nothing to show is still a ticket somebody has to be
// able to tell apart, so the summary names the work even when flow has no
// permission token and no change to offer.
func TestARefusedItemWithNothingToShowStillNamesTheWork(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:      core.ItemForgeProtection,
		Target:  engine.TargetForge,
		Outcome: engine.OutcomeFollowUp,
		Why:     "the trunk takes anything anyone pushes",
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	if !strings.Contains(result.Remediation.Summary, "The trunk is protected") {
		t.Errorf("the summary does not name the work: %q", result.Remediation.Summary)
	}
}

// A note is the provider's own explanation of why a finding is a finding, and
// the report is the only place it can land, because flow compares nothing on
// it. Here it is the difference between telling somebody to write the trunk's
// protection and telling them it is already written and switched off.
func TestAFindingSaysWhatTheProviderVolunteered(t *testing.T) {
	for _, outcome := range []engine.Outcome{engine.OutcomeNative, engine.OutcomeFollowUp} {
		t.Run(string(outcome), func(t *testing.T) {
			env := setupEnv(t, setupConfig())
			env.Plan = engine.Plan{Items: []engine.PlanItem{{
				ID:      core.ItemForgeProtection,
				Target:  engine.TargetForge,
				Outcome: outcome,
				Why:     "the trunk takes anything anyone pushes",
				Notes:   []core.Note{dormantNote},
			}}}

			result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

			wantState(t, result, engine.StateFail)
			if !hasEvidence(result, `the rule "Flow" covers main and is not in force`) {
				t.Errorf("the finding does not say what the provider volunteered:\n%+v", result.Evidence)
			}
		})
	}
}

// The ticket leads with the cheapest way to close it. A mechanism that is
// written and merely switched off is one click; the change flow would have
// made from scratch is an afternoon, and printing the afternoon first buries
// the click under it.
func TestATicketLeadsWithTheCheapestWayToCloseIt(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:      core.ItemForgeProtection,
		Target:  engine.TargetForge,
		Outcome: engine.OutcomeFollowUp,
		Why:     "the trunk takes anything anyone pushes",
		Missing: []string{"admin"},
		Notes:   []core.Note{dormantNote},
		Changes: []core.Change{{
			Op:   core.ChangeCreate,
			What: "ruleset",
			Item: core.Item{ID: core.ItemForgeProtection, Name: "flow"},
		}},
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	steps := result.Remediation.Steps
	if len(steps) < 3 {
		t.Fatalf("steps are %v, want the switch, the access and the change", steps)
	}
	if !strings.Contains(steps[0], `switch the rule "Flow" on`) {
		t.Errorf("the first step is not the one that costs least: %q", steps[0])
	}
	if !strings.Contains(steps[0], dormantNote.URL) {
		t.Errorf("the step does not say where the switch is: %q", steps[0])
	}
}

// A note explains why something is not true. On a check that passes it is a
// fact about a mechanism nobody is relying on, and a passing line that lists
// the ways the team might also have been safe is noise every morning.
func TestAPassDoesNotMentionAMechanismNobodyIsRelyingOn(t *testing.T) {
	env := setupEnv(t, setupConfig())
	env.Plan = engine.Plan{Items: []engine.PlanItem{{
		ID:        core.ItemForgeProtection,
		Target:    engine.TargetForge,
		Outcome:   engine.OutcomeCompensated,
		Why:       "the trunk is protected",
		Mechanism: "a branch rule",
		Notes:     []core.Note{dormantNote},
	}}}

	result := setupResult(t, engine.SetupInput{Config: setupConfig()}, env, core.ItemForgeProtection)

	wantState(t, result, engine.StatePass)
	if hasEvidence(result, "not in force") {
		t.Errorf("a pass carries a note about something nobody is relying on:\n%+v", result.Evidence)
	}
}

// dormantNote is what the GitHub adapter volunteers about a ruleset somebody
// wrote and nobody switched on.
var dormantNote = core.Note{
	Text: `the rule "Flow" covers main and is not in force: disabled`,
	URL:  "https://forge.test/acme/widget/rules/1",
	Fix:  `switch the rule "Flow" on - it is already written`,
}

// hasEvidence reports whether any evidence line says this.
func hasEvidence(result engine.Result, want string) bool {
	for _, evidence := range result.Evidence {
		if strings.Contains(evidence.Text, want) {
			return true
		}
	}
	return false
}
