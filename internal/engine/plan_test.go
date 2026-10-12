package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// testRegistry is the shipped capability declarations, decoded. The planner's
// tests run against the real research: a plan that is right about a provider
// nobody declared is not worth reading.
func testRegistry(t *testing.T) core.Registry {
	t.Helper()
	registry, err := core.LoadRegistry(testCapabilities(t))
	if err != nil {
		t.Fatalf("load the capability declarations: %v", err)
	}
	return registry
}

// planConfigHash is a stand-in for the hash of the configuration the plan was
// made for. The root hashes the file it read; the engine is handed the answer.
const planConfigHash = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// world is one set of providers to plan against.
type world struct {
	forge   *coretest.FakeForge
	tracker *coretest.FakeTracker
	deploys *coretest.FakeDeploys
	lock    *config.Lock
}

func (w world) adapters() engine.Adapters {
	return engine.Adapters{Forge: w.forge, Tracker: w.tracker, Deploys: w.deploys}
}

// forgeFixture is the repository both worlds are planned against, so that the
// difference between them is the tracker and nothing else. Its trunk is
// protected but rewritable, it requires one check that is not flow's, it
// allows two merge strategies where the configuration allows one, and it has
// the CI workflow but neither template.
func forgeFixture(t *testing.T) coretest.ForgeFixture {
	t.Helper()
	fixture := coretest.ContractForge()
	fixture.Kind = "github"
	fixture.Capabilities = testRegistry(t)["github"]
	fixture.Permissions = core.Permissions{
		Tokens:   []string{"admin", "statuses_write", "contents_write", "pulls_write"},
		Evidence: []string{"repository permission: admin"},
	}
	return fixture
}

func deployFixture(t *testing.T) coretest.DeployFixture {
	t.Helper()
	fixture := coretest.ContractDeploys()
	fixture.Capabilities = testRegistry(t)["deploys"]
	return fixture
}

// githubProjectsWorld is a board that is most of the way there: four of the
// five columns, two of the six labels, one column flow created and no longer
// wants, and the built-in "Pull request merged" automation somebody has
// vouched for.
//
// Nothing observes that automation, because the Projects API will not say
// whether it is on; a human's word is the only evidence there is, which is
// what `remediation: attest` means (spec 05 §4).
func githubProjectsWorld(t *testing.T) world {
	t.Helper()
	tracker := coretest.ContractTracker()
	tracker.Kind = "github-projects"
	tracker.Capabilities = testRegistry(t)["github-projects"]
	tracker.Permissions = core.Permissions{
		Tokens:   []string{"project_read", "project_write", "issues_write"},
		Evidence: []string{"X-OAuth-Scopes: repo, project"},
	}
	tracker.Observations = map[string]core.Observation{
		"tracker.project": {Exists: true},
		"tracker.status_options": {Exists: true, Items: []core.Item{
			{ID: "opt-backlog", Name: "Backlog"},
			{ID: "opt-in-progress", Name: "In progress"},
			{ID: "opt-in-review", Name: "In review"},
			{ID: "opt-done", Name: "Done"},
			{ID: "opt-icebox", Name: "Icebox"},
		}},
		"tracker.labels": {Exists: true, Items: []core.Item{
			{ID: "lbl-1", Name: "blocked", Fields: map[string]string{"color": "b60205"}},
			{ID: "lbl-2", Name: "flow-followup", Fields: map[string]string{"color": "d73a4a"}},
		}},
	}

	lock := config.NewLock("v0.3.0")
	lock.ConfigHash = planConfigHash
	lock.Record(config.Creation{
		Kind: engine.CreationKind(core.ItemTrackerStatusOptions),
		ID:   "opt-icebox",
		Name: "Icebox",
		At:   coretest.ContractNow.Add(-30 * 24 * time.Hour),
	})
	lock.Attestations = append(lock.Attestations,
		config.Attestation{
			Check:     core.ItemTrackerExpedite,
			By:        "@17twenty",
			Note:      "the Expedite view is saved on the board",
			At:        coretest.ContractNow.Add(-24 * time.Hour),
			ExpiresAt: coretest.ContractNow.Add(29 * 24 * time.Hour),
		},
		config.Attestation{
			Check:     core.NativeItem("pr_merged"),
			By:        "@17twenty",
			Note:      "the built-in 'Pull request merged' workflow is on and points at Done",
			At:        coretest.ContractNow.Add(-24 * time.Hour),
			ExpiresAt: coretest.ContractNow.Add(29 * 24 * time.Hour),
		})

	return world{
		forge:   coretest.NewFakeForge(forgeFixture(t)),
		tracker: coretest.NewFakeTracker(tracker),
		deploys: coretest.NewFakeDeploys(deployFixture(t)),
		lock:    lock,
	}
}

// jiraWorld is the same configuration in front of a tracker that can be made
// to do different things: it can be told its columns, it can report its
// work-in-progress limits, and it cannot be asked about an issue closing at
// all.
func jiraWorld(t *testing.T) world {
	t.Helper()
	tracker := coretest.ContractTracker()
	tracker.Kind = "jira-cloud"
	tracker.Capabilities = testRegistry(t)["jira-cloud"]
	tracker.Permissions = core.Permissions{
		Tokens:   []string{"create_issues", "transition_issues"},
		Evidence: []string{"/rest/api/3/mypermissions: BROWSE_PROJECTS, CREATE_ISSUES"},
	}
	tracker.Observations = map[string]core.Observation{
		"tracker.project": {Exists: true},
		"tracker.status_options": {Exists: true, Items: []core.Item{
			{ID: "10001", Name: "To Do"},
			{ID: "10002", Name: "In Progress"},
			{ID: "10003", Name: "Done"},
		}},
		"tracker.labels": {Exists: true},
		"tracker.wip": {Exists: true, Items: []core.Item{
			{Name: "In progress", Fields: map[string]string{engine.FieldPerPerson: "2"}},
			{Name: "In review", Fields: map[string]string{engine.FieldLimit: "5"}},
		}},
	}
	return world{
		forge:   coretest.NewFakeForge(forgeFixture(t)),
		tracker: coretest.NewFakeTracker(tracker),
		deploys: coretest.NewFakeDeploys(deployFixture(t)),
	}
}

// planFor observes a world and plans the lean-kanban workflow against it.
func planFor(t *testing.T, w world) engine.Plan {
	t.Helper()
	ctx := context.Background()
	cfg := preset(t, config.PresetLeanKanban)
	state := desired(t, cfg)

	observations, err := engine.Observe(ctx, state, w.adapters())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	providers, err := w.adapters().Providers(ctx)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}

	plan, err := engine.NewPlan(engine.PlanInput{
		Config:       cfg,
		Desired:      state,
		Observations: observations,
		Providers:    providers,
		Lock:         w.lock,
		ConfigHash:   planConfigHash,
		FlowVersion:  "v0.3.0",
		Now:          coretest.ContractNow,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// TestPlanGolden pins what flow would do about the same workflow in front of
// two different trackers. The plan is the thing a human approves, so a change
// to it should be a change to a file somebody had to read.
//
// Run go test ./internal/engine -run TestPlanGolden -update to rewrite them.
func TestPlanGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		world func(*testing.T) world
	}{
		{name: "github-projects", world: githubProjectsWorld},
		{name: "jira-cloud", world: jiraWorld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planFor(t, tc.world(t))

			rendered, err := plan.JSON()
			if err != nil {
				t.Fatalf("render the plan as JSON: %v", err)
			}
			goldenFile(t, filepath.Join("plan", tc.name+".txt"), []byte(plan.Text()))
			goldenFile(t, filepath.Join("plan", tc.name+".json"), rendered)
		})
	}
}

// goldenFile compares bytes against the file at testdata/name, rewriting it under
// -update.
func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("make %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s does not match:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// TestPlanOutcomes states what each world's plan decides, so that the goldens
// are checked against something other than themselves.
func TestPlanOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		world func(*testing.T) world
		want  map[string]engine.Outcome
		exit  int
	}{
		{
			name:  "github-projects",
			world: githubProjectsWorld,
			want: map[string]engine.Outcome{
				core.ItemTrackerProject:              engine.OutcomeNoop,
				core.ItemTrackerStatusOptions:        engine.OutcomeNative,
				core.ItemTrackerLabels:               engine.OutcomeNative,
				core.NativeItem("changes_requested"): engine.OutcomeAttest,
				core.NativeItem("pr_merged"):         engine.OutcomeNoop,
				core.NativeItem("issue_closed"):      engine.OutcomeAttest,
				core.ItemTrackerWIP:                  engine.OutcomeCompensated,
				core.ItemTrackerExpedite:             engine.OutcomeNoop,
				core.ItemForgeDefaultBranch:          engine.OutcomeNoop,
				core.ItemForgeProtection:             engine.OutcomeNative,
				core.ItemForgeRequiredStatus:         engine.OutcomeNative,
				core.ItemForgeMergeSettings:          engine.OutcomeNative,
				core.ItemForgeFiles:                  engine.OutcomeNative,
				core.ItemDeploysRecords:              engine.OutcomeNoop,
			},
			exit: 2,
		},
		{
			name:  "jira-cloud",
			world: jiraWorld,
			want: map[string]engine.Outcome{
				// Jira can be told its columns, but not by this credential.
				core.ItemTrackerStatusOptions: engine.OutcomeFollowUp,
				// Labels are free-form, so flow just makes them.
				core.ItemTrackerLabels: engine.OutcomeNative,
				// Jira reports its own limits, and this one disagrees; it
				// cannot be told otherwise, so flow enforces it at the gate.
				core.ItemTrackerWIP: engine.OutcomeCompensated,
				// Nothing can be asked of Jira about an issue closing.
				core.NativeItem("issue_closed"): engine.OutcomeUnsupported,
			},
			exit: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planFor(t, tc.world(t))
			for id, want := range tc.want {
				item, ok := plan.Find(id)
				if !ok {
					t.Fatalf("%s is not in the plan", id)
				}
				if item.Outcome != want {
					t.Errorf("%s: outcome is %s, want %s (%s)", id, item.Outcome, want, item.Why)
				}
			}
			if got := plan.ExitCode(); got != tc.exit {
				t.Errorf("exit code is %d, want %d", got, tc.exit)
			}
		})
	}
}

// TestWhatCountsAsNothingToDo states the exit code one outcome at a time,
// because the two worlds above both have plenty outstanding and so cannot
// tell which outcome is the one keeping them at 2.
//
// The case that matters is compensated: the intent is enforced, by something
// other than what the configuration named, and nothing anybody types will
// ever change that. A run that reported it as pending would exit 2 for ever.
func TestWhatCountsAsNothingToDo(t *testing.T) {
	for _, tc := range []struct {
		outcome engine.Outcome
		exit    int
	}{
		{engine.OutcomeNoop, 0},
		{engine.OutcomeCompensated, 0},
		{engine.OutcomeNative, 2},
		{engine.OutcomeFollowUp, 2},
		{engine.OutcomeAttest, 2},
		{engine.OutcomeUnsupported, 1},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			plan := engine.Plan{Items: []engine.PlanItem{
				{ID: "tracker.project", Outcome: engine.OutcomeNoop},
				{ID: "tracker.labels", Outcome: tc.outcome},
			}}
			if got := plan.ExitCode(); got != tc.exit {
				t.Errorf("a plan whose only outstanding item is %s exits %d, want %d", tc.outcome, got, tc.exit)
			}
		})
	}
}

// TestPlanChanges states the diff itself: what is missing is created, what
// disagrees is updated against the provider's own id, and what flow created
// and no longer wants is removed.
func TestPlanChanges(t *testing.T) {
	plan := planFor(t, githubProjectsWorld(t))

	columns, ok := plan.Find(core.ItemTrackerStatusOptions)
	if !ok {
		t.Fatal("the status options are not in the plan")
	}
	want := []core.Change{
		{What: core.ItemTrackerStatusOptions, Op: core.ChangeCreate, Item: core.Item{Name: "Ready"}},
		{What: core.ItemTrackerStatusOptions, Op: core.ChangeDelete, Item: core.Item{ID: "opt-icebox", Name: "Icebox"}},
	}
	if len(columns.Changes) != len(want) {
		t.Fatalf("changes are %+v, want %+v", columns.Changes, want)
	}
	for i, change := range columns.Changes {
		if change.Op != want[i].Op || !change.Item.Equal(want[i].Item) {
			t.Errorf("change %d is %+v, want %+v", i, change, want[i])
		}
	}

	protection, ok := plan.Find(core.ItemForgeProtection)
	if !ok {
		t.Fatal("the protection is not in the plan")
	}
	if len(protection.Changes) != 1 || protection.Changes[0].Op != core.ChangeUpdate {
		t.Fatalf("protection changes are %+v, want one update", protection.Changes)
	}
	if got := protection.Changes[0].Item.Field(engine.FieldRequireLinearHistory); got != "true" {
		t.Errorf("the update asks for require_linear_history=%q, want true", got)
	}
}

// TestPlanKeepsWhatFlowDidNotCreate proves spec 06 §4's rule: an option the
// lock does not record as flow's own is left alone, however unwanted it is.
func TestPlanKeepsWhatFlowDidNotCreate(t *testing.T) {
	w := githubProjectsWorld(t)
	w.lock.Created = nil

	plan := planFor(t, w)
	columns, _ := plan.Find(core.ItemTrackerStatusOptions)
	for _, change := range columns.Changes {
		if change.Op == core.ChangeDelete {
			t.Fatalf("flow proposes to delete %q, which it did not create", change.Item.Name)
		}
	}
}

// TestPlanAttestationLapsesWithTheConfig proves that a human's word covers the
// workflow it was given about. Rewrite the configuration and the promise is
// about something else (spec 03 §3).
func TestPlanAttestationLapsesWithTheConfig(t *testing.T) {
	w := githubProjectsWorld(t)
	if item, _ := planFor(t, w).Find(core.ItemTrackerExpedite); item.Outcome != engine.OutcomeNoop {
		t.Fatalf("the attested view plans as %s, want noop", item.Outcome)
	}

	w.lock.ConfigHash = "sha256:ffff"
	item, _ := planFor(t, w).Find(core.ItemTrackerExpedite)
	if item.Outcome != engine.OutcomeAttest {
		t.Errorf("after the configuration changed the view plans as %s, want attest", item.Outcome)
	}
}

// TestPlanAttestationLapsesWithTheItemItCovered proves the finer rule: a
// promise that names what it was about lapses when that one item is
// rewritten, and not when anything else in the file is.
//
// Both halves matter. Without the first, flow would ask a person to vouch
// again for something nobody had touched, every time a colleague edited a
// label somewhere else. Without the second, a promise would outlive the thing
// it was made about, which is the way `flow attest` becomes a way to sign off
// a workflow that no longer exists.
func TestPlanAttestationLapsesWithTheItemItCovered(t *testing.T) {
	w := githubProjectsWorld(t)
	covers, _ := planFor(t, w).Find(core.ItemTrackerExpedite)
	at := -1
	for i, a := range w.lock.Attestations {
		if a.Check == core.ItemTrackerExpedite {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("the fixture has no attestation for %s", core.ItemTrackerExpedite)
	}

	// Pin the promise to the item it was made about, which is what
	// `flow attest` writes.
	w.lock.Attestations[at].Inputs = covers.DesiredHash
	if item, _ := planFor(t, w).Find(core.ItemTrackerExpedite); item.Outcome != engine.OutcomeNoop {
		t.Fatalf("a promise about this very item plans as %s, want noop", item.Outcome)
	}

	w.lock.Attestations[at].Inputs = "sha256:0000"
	item, _ := planFor(t, w).Find(core.ItemTrackerExpedite)
	if item.Outcome != engine.OutcomeAttest {
		t.Errorf("a promise about a different shape of the item plans as %s, want attest", item.Outcome)
	}
	if w.lock.ConfigHash != planConfigHash {
		t.Errorf("the configuration hash moved, so this proves nothing finer than the file")
	}
}

// TestPlanFilters proves --target and --only select without changing what is
// decided about what is left.
func TestPlanFilters(t *testing.T) {
	ctx := context.Background()
	cfg := preset(t, config.PresetLeanKanban)
	state := desired(t, cfg)
	w := githubProjectsWorld(t)
	observations, err := engine.Observe(ctx, state, w.adapters())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	providers, err := w.adapters().Providers(ctx)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	build := func(in engine.PlanInput) engine.Plan {
		t.Helper()
		in.Config, in.Desired, in.Observations, in.Providers = cfg, state, observations, providers
		in.Now = coretest.ContractNow
		plan, err := engine.NewPlan(in)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return plan
	}

	forge := build(engine.PlanInput{Targets: []string{engine.TargetForge}})
	if len(forge.Items) == 0 {
		t.Fatal("--target forge planned nothing")
	}
	for _, item := range forge.Items {
		if item.Target != engine.TargetForge {
			t.Errorf("--target forge planned %s, which is a %s item", item.ID, item.Target)
		}
	}

	only := build(engine.PlanInput{Only: []string{core.CapLabels}})
	if len(only.Items) != 1 || only.Items[0].ID != core.ItemTrackerLabels {
		t.Fatalf("--only labels planned %v, want just the labels", only.IDs())
	}

	full := build(engine.PlanInput{})
	whole, _ := full.Find(core.ItemTrackerLabels)
	if only.Items[0].Outcome != whole.Outcome || only.Items[0].ObservedHash != whole.ObservedHash {
		t.Error("filtering changed what the plan decided about the labels")
	}
}

// TestPlanJSONRoundTrip proves the plan survives being written and read back,
// which is what `--out` and `flow apply --plan` depend on.
func TestPlanJSONRoundTrip(t *testing.T) {
	plan := planFor(t, githubProjectsWorld(t))
	data, err := plan.JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	read, err := engine.ParsePlan(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	again, err := read.JSON()
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	if string(again) != string(data) {
		t.Errorf("the plan changed on the way through:\n%s\n%s", data, again)
	}
	if why, stale := read.StaleAgainst(plan); stale {
		t.Errorf("a plan is stale against itself: %s", why)
	}
}

// TestParsePlanRefusesAnotherSchema proves a document flow does not
// understand is refused rather than read for what can be made of it.
func TestParsePlanRefusesAnotherSchema(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"version":"flow.plan/v2","desired_hash":"sha256:1","items":[]}`),
		[]byte(`{"desired_hash":"sha256:1","items":[]}`),
		[]byte(`{"version":"flow.plan/v1","surprise":true}`),
		[]byte(`not json at all`),
	} {
		if _, err := engine.ParsePlan(data); err == nil {
			t.Errorf("%s was accepted", data)
		}
	}
}

// TestPlanStaleAgainst states every way a plan stops being safe to apply.
func TestPlanStaleAgainst(t *testing.T) {
	plan := planFor(t, githubProjectsWorld(t))

	for _, tc := range []struct {
		name  string
		spoil func(engine.Plan) engine.Plan
	}{
		{
			name:  "the configuration changed",
			spoil: func(p engine.Plan) engine.Plan { p.ConfigHash = "sha256:ffff"; return p },
		},
		{
			name:  "what it asks for changed",
			spoil: func(p engine.Plan) engine.Plan { p.DesiredHash = "sha256:ffff"; return p },
		},
		{
			name: "the provider changed",
			spoil: func(p engine.Plan) engine.Plan {
				p.Items[0].ObservedHash = "sha256:ffff"
				return p
			},
		},
		{
			name: "the item is gone",
			spoil: func(p engine.Plan) engine.Plan {
				p.Items = p.Items[1:]
				return p
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := clonePlan(plan)
			if why, stale := plan.StaleAgainst(tc.spoil(fresh)); !stale {
				t.Errorf("the plan is not stale: %q", why)
			}
		})
	}
}

// clonePlan copies a plan deeply enough that spoiling one does not spoil the
// other.
func clonePlan(p engine.Plan) engine.Plan {
	items := make([]engine.PlanItem, len(p.Items))
	copy(items, p.Items)
	p.Items = items
	return p
}

// TestPlanNeedsAConfiguration proves the planner refuses to make one up.
func TestPlanNeedsAConfiguration(t *testing.T) {
	if _, err := engine.NewPlan(engine.PlanInput{}); err == nil {
		t.Fatal("a plan was made with no configuration")
	}
}
