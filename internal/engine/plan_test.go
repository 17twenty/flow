package engine_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
	return planWith(t, w, preset(t, config.PresetLeanKanban))
}

// planWith observes a world and plans a configuration of the caller's own
// against it, for the tests whose subject is a key the preset does not set.
func planWith(t *testing.T, w world, cfg *config.Config) engine.Plan {
	t.Helper()
	ctx := context.Background()
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
// -update. Every golden in this package goes through it.
//
// Content hashes are blanked first. A golden is a record of what flow says —
// the items, the outcomes, the changes — and a hash is derived from bytes the
// golden does not show: one changed in a CI template rewrote a line in four
// goldens, and two pull requests that each touched a template conflicted on
// that line every time, which is how a stale golden reached the trunk. That
// the hashes are there and stable is a property of its own, tested by
// TestEveryPlannedItemCarriesItsHashes and the determinism tests.
func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	got = contentHash.ReplaceAll(got, []byte("sha256:…"))
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

// contentHash is a sha256 content hash as flow writes one.
var contentHash = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// TestEveryPlannedItemCarriesItsHashes is what the goldens no longer say:
// every item of a plan records the hash of what was wanted and of what was
// observed, which is what lets a saved plan refuse to apply against a world
// that has moved on (spec 06 §3).
func TestEveryPlannedItemCarriesItsHashes(t *testing.T) {
	for _, world := range []func(*testing.T) world{githubProjectsWorld, jiraWorld} {
		for _, item := range planFor(t, world(t)).Items {
			if !contentHash.MatchString(item.DesiredHash) {
				t.Errorf("%s has no desired hash: %q", item.ID, item.DesiredHash)
			}
			if item.Exists && !contentHash.MatchString(item.ObservedHash) {
				t.Errorf("%s was observed and has no observed hash: %q", item.ID, item.ObservedHash)
			}
		}
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
				// A status is the project workflow's business and a column is
				// the board's, and neither is writable: somebody who
				// administers the project has to do it (spec 11 §3).
				core.ItemTrackerStatusOptions: engine.OutcomeFollowUp,
				// A Jira label is free-form in the other direction: there is no
				// registry to create one in and none to read one back from, so
				// a person vouches for it instead.
				core.ItemTrackerLabels: engine.OutcomeAttest,
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
			// What each tracker makes of a transition it owns is the
			// subject, so the merge transitions are left to it here.
			plan := planWith(t, tc.world(t), nativeMerges(preset(t, config.PresetLeanKanban)))
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
	// The item is whole, because the forge is given all of it. What differs
	// is kept beside it, because a person is told only that part: this trunk
	// is already enabled and already refuses deletions.
	if got := protection.Changes[0].Differs; !slices.Equal(got, []string{engine.FieldRequireLinearHistory, engine.FieldStrictChecks}) {
		t.Errorf("the update differs in %v, want the two settings the trunk does not already have", got)
	}
}

// TestAPlanAsksForAutoMergeOnlyWhereTheRepositoryRefusesIt.
//
// The desired side of this is TestDesiredAsksForAutoMergeOnlyWhereItWasAsked;
// this is the other half, and the half that needs the provider to have been
// asked. A forge is observed as it is, so a repository that already permits a
// pull request to land itself is a repository with nothing to change — and an
// observation that answered "false" for everybody would have flow proposing,
// every morning, a setting somebody turned on months ago.
func TestAPlanAsksForAutoMergeOnlyWhereTheRepositoryRefusesIt(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Forge.AutoMerge = true

	for _, tc := range []struct {
		name      string
		permitted bool
		want      bool
	}{
		{"a repository that refuses it", false, true},
		{"a repository that already permits it", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := githubProjectsWorld(t)
			fx := forgeFixture(t)
			fx.Repo.Merge.AllowAutoMerge = tc.permitted
			w.forge = coretest.NewFakeForge(fx)

			planned, ok := planWith(t, w, cfg).Find(core.ItemForgeMergeSettings)
			if !ok {
				t.Fatal("the merge settings are not in the plan")
			}
			if len(planned.Changes) != 1 {
				t.Fatalf("the merge settings changes are %+v, want one update", planned.Changes)
			}
			got := slices.Contains(planned.Changes[0].Differs, engine.FieldAllowAutoMerge)
			if got != tc.want {
				t.Errorf("the update differs in %v; it %s name %s",
					planned.Changes[0].Differs, map[bool]string{true: "must", false: "must not"}[tc.want], engine.FieldAllowAutoMerge)
			}
		})
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

// TestPlanRenamesAColumnThatDiffersOnlyInCase proves that a column flow reads
// back by a folded name is the column it plans against (issue #189). GitHub
// starts a board with "In Progress"; a plan that wanted "In progress" beside
// it left two columns that every move and every audit treated as one. The
// column is renamed in place under its own id, so the items in it and the
// automations pointing at it follow, and it is never proposed for removal —
// even where the lock records it as flow's own.
func TestPlanRenamesAColumnThatDiffersOnlyInCase(t *testing.T) {
	w := githubProjectsWorld(t)
	w.tracker = coretest.NewFakeTracker(trackerWithStatusOptions(t,
		core.Item{ID: "opt-backlog", Name: "Backlog"},
		core.Item{ID: "opt-in-progress", Name: "In Progress"},
		core.Item{ID: "opt-ready", Name: "Ready"},
		core.Item{ID: "opt-in-review", Name: "In review"},
		core.Item{ID: "opt-done", Name: "Done"},
	))
	w.lock.Record(config.Creation{
		Kind: engine.CreationKind(core.ItemTrackerStatusOptions),
		ID:   "opt-in-progress",
		Name: "In Progress",
		At:   coretest.ContractNow.Add(-30 * 24 * time.Hour),
	})

	item, _ := planFor(t, w).Find(core.ItemTrackerStatusOptions)
	want := core.Change{What: core.ItemTrackerStatusOptions, Op: core.ChangeUpdate, Item: core.Item{ID: "opt-in-progress", Name: "In progress"}}
	if len(item.Changes) != 1 || item.Changes[0].Op != want.Op || !item.Changes[0].Item.Equal(want.Item) {
		t.Errorf("changes are %+v, want only %+v", item.Changes, want)
	}
}

// TestPlanPrefersTheExactColumnToAFoldedOne proves that a board already
// holding both spellings is planned against the one the configuration wrote,
// and the other is left to whoever made it.
func TestPlanPrefersTheExactColumnToAFoldedOne(t *testing.T) {
	w := githubProjectsWorld(t)
	w.tracker = coretest.NewFakeTracker(trackerWithStatusOptions(t,
		core.Item{ID: "opt-backlog", Name: "Backlog"},
		core.Item{ID: "opt-github-in-progress", Name: "In Progress"},
		core.Item{ID: "opt-in-progress", Name: "In progress"},
		core.Item{ID: "opt-ready", Name: "Ready"},
		core.Item{ID: "opt-in-review", Name: "In review"},
		core.Item{ID: "opt-done", Name: "Done"},
	))

	item, _ := planFor(t, w).Find(core.ItemTrackerStatusOptions)
	if len(item.Changes) != 0 {
		t.Errorf("a board with every configured column wants %+v", item.Changes)
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

// TestProtectionACredentialCannotReadIsNotCalledUndone is issue #255. A daily
// run on a non-admin credential sees that the trunk is protected and which
// checks it requires, and not the rest. What it cannot see is taken as asked
// for with a note that says so, and doctor reports that as a warning — not a
// change for an admin to make, which is how the follow-ups an admin's apply
// had verified were reopened the next morning.
func TestProtectionACredentialCannotReadIsNotCalledUndone(t *testing.T) {
	w := githubProjectsWorld(t)
	fx := forgeFixture(t)
	fx.Protections["main"] = core.Protection{Branch: "main", Enabled: true, Unreadable: true, RequiredChecks: []string{engine.DefaultStatusContext}}
	w.forge = coretest.NewFakeForge(fx)
	plan := planFor(t, w)

	item, ok := plan.Find(core.ItemForgeProtection)
	if !ok {
		t.Fatal("forge.protection is not in the plan")
	}
	if item.Outcome != engine.OutcomeNoop {
		t.Errorf("forge.protection is %s (%s), want noop: what could not be read is not undone", item.Outcome, item.Why)
	}
	if !slices.ContainsFunc(item.Notes, func(n core.Note) bool { return n.Unverified }) {
		t.Errorf("the plan does not say what it could not read: %+v", item.Notes)
	}

	env := setupEnv(t, preset(t, config.PresetLeanKanban))
	env.Plan = plan
	result := setupResult(t, engine.SetupInput{Config: env.Config}, env, core.ItemForgeProtection)
	wantState(t, result, engine.StateWarn)
}

// TestChecksACredentialCannotReadAreNotCalledUnrequired is issue #291. GitLab
// names a required verdict in an external status check, which a credential
// below maintainer may not read, so it cannot tell whether the gate is
// required. What it cannot see is taken as asked for, with a note that doctor
// reports as a warning — not a check for apply to add on a branch that may
// already require it.
func TestChecksACredentialCannotReadAreNotCalledUnrequired(t *testing.T) {
	w := githubProjectsWorld(t)
	fx := forgeFixture(t)
	fx.Protections["main"] = core.Protection{Branch: "main", Enabled: true, Unreadable: true, ChecksUnreadable: true}
	w.forge = coretest.NewFakeForge(fx)
	plan := planFor(t, w)

	item, ok := plan.Find(core.ItemForgeRequiredStatus)
	if !ok {
		t.Fatal("forge.required_status is not in the plan")
	}
	if item.Outcome != engine.OutcomeNoop {
		t.Errorf("forge.required_status is %s (%s), want noop: a check that could not be read is not unrequired", item.Outcome, item.Why)
	}
	if !slices.ContainsFunc(item.Notes, func(n core.Note) bool { return n.Unverified }) {
		t.Errorf("the plan does not say what it could not read: %+v", item.Notes)
	}

	env := setupEnv(t, preset(t, config.PresetLeanKanban))
	env.Plan = plan
	result := setupResult(t, engine.SetupInput{Config: env.Config}, env, core.ItemForgeRequiredStatus)
	wantState(t, result, engine.StateWarn)
}

// Protection kept where only an admin can read it is a change for apply to
// make even when it says everything asked of it (issue #258): left there, a
// run on any other credential can never see it. Where it says something flow
// could not carry, it is no change at all — a warning naming what is in the
// way, because only a person can move it.
func TestProtectionOnlyAnAdminCanReadIsMovedUnlessItCannotBe(t *testing.T) {
	planned := func(adminOnly bool, immovable ...string) engine.Plan {
		w := githubProjectsWorld(t)
		fx := forgeFixture(t)
		protection := fx.Protections["main"]
		protection.RequireLinearHistory, protection.StrictChecks = true, true
		protection.AdminOnly, protection.Immovable = adminOnly, immovable
		fx.Protections["main"] = protection
		w.forge = coretest.NewFakeForge(fx)
		return planFor(t, w)
	}
	if plain, _ := planned(false).Find(core.ItemForgeProtection); plain.Outcome != engine.OutcomeNoop {
		t.Fatalf("forge.protection plans as %s %+v where anybody can read it, want noop for this test to mean anything", plain.Outcome, plain.Changes)
	}

	item, _ := planned(true).Find(core.ItemForgeProtection)
	if item.Outcome != engine.OutcomeNative || len(item.Changes) != 1 || item.Changes[0].Op != core.ChangeUpdate {
		t.Fatalf("admin-only protection plans as %s %+v, want one update", item.Outcome, item.Changes)
	}
	if got := item.Changes[0].Item.Field(engine.FieldAdminOnly); got != "false" {
		t.Errorf("the update asks for admin_only=%q, want false", got)
	}
	if !slices.ContainsFunc(item.Notes, func(n core.Note) bool { return !n.Unverified && strings.Contains(n.Fix, "flow apply moves it") }) {
		t.Errorf("the plan does not say why it would move the protection: %+v", item.Notes)
	}

	stuck := planned(true, "who may push")
	item, _ = stuck.Find(core.ItemForgeProtection)
	if item.Outcome != engine.OutcomeNoop {
		t.Errorf("protection flow cannot move plans as %s %+v, want noop", item.Outcome, item.Changes)
	}
	if !slices.ContainsFunc(item.Notes, func(n core.Note) bool { return n.Unverified && strings.Contains(n.Text, "who may push") }) {
		t.Errorf("the plan does not say what is in the way: %+v", item.Notes)
	}
	env := setupEnv(t, preset(t, config.PresetLeanKanban))
	env.Plan = stuck
	wantState(t, setupResult(t, engine.SetupInput{Config: env.Config}, env, core.ItemForgeProtection), engine.StateWarn)
}

// A rule the forge holds for the trunk and does not enforce is a fact about
// the repository, not a difference flow would act on. It has to reach the
// report, because it is the difference between "somebody has to write this"
// and "somebody has to switch this on" — and it must not reach the hashes,
// because a plan that changed when a disabled ruleset appeared would read as
// drift and would ask to be approved again for nothing.
func TestARuleNobodyEnabledIsANoteAndNotADifference(t *testing.T) {
	plain := planFor(t, githubProjectsWorld(t))

	w := githubProjectsWorld(t)
	fx := forgeFixture(t)
	protection := fx.Protections["main"]
	protection.Dormant = []core.DormantRule{{
		Name:  "Flow",
		State: "disabled",
		URL:   "https://forge.test/acme/widget/rules/1",
	}}
	fx.Protections["main"] = protection
	w.forge = coretest.NewFakeForge(fx)
	plan := planFor(t, w)

	for _, id := range []string{core.ItemForgeProtection, core.ItemForgeRequiredStatus} {
		t.Run(id, func(t *testing.T) {
			item, ok := plan.Find(id)
			if !ok {
				t.Fatalf("%s is not in the plan", id)
			}
			if len(item.Notes) != 1 {
				t.Fatalf("notes are %+v, want the one rule nobody enabled", item.Notes)
			}
			note := item.Notes[0]
			if !strings.Contains(note.Text, `"Flow"`) || !strings.Contains(note.Text, "disabled") {
				t.Errorf("the note does not say which rule is not in force, or why: %q", note.Text)
			}
			if !strings.Contains(note.Fix, "switch") {
				t.Errorf("the note does not say the rule is already written: %q", note.Fix)
			}
			if note.URL != "https://forge.test/acme/widget/rules/1" {
				t.Errorf("the note does not say where to go: %q", note.URL)
			}

			was, _ := plain.Find(id)
			if item.ObservedHash != was.ObservedHash {
				t.Errorf("observed hash is %s, want %s: a note is not drift", item.ObservedHash, was.ObservedHash)
			}
			if item.Outcome != was.Outcome {
				t.Errorf("outcome is %q, want %q: a note decides nothing", item.Outcome, was.Outcome)
			}
		})
	}
}

// nothingHasDeployedWorld is the board of a repository whose deploy job
// writes no deployment record: `deploys.records` becomes somebody else's
// job, which is the ticket this whole mechanism was reported against.
func nothingHasDeployedWorld(t *testing.T) world {
	t.Helper()
	w := githubProjectsWorld(t)
	fixture := deployFixture(t)
	fixture.Deployments = nil
	w.deploys = coretest.NewFakeDeploys(fixture)
	return w
}

// A recipe is written once, for every repository the adapter will ever be
// pointed at, and read by one person holding one of them. The plan is where
// the general case becomes theirs: `{{name}}` is a sentence about nobody,
// and a brace that reached a ticket would be the declaration's template read
// out loud at somebody who never asked to see it.
//
// It rides only on an item flow is not going to make true itself. Every
// other outcome is flow saying it will see to this, or that it is already
// so, and instructions for doing by hand a thing nobody has to do are the
// longest part of a document whose whole job is to be read before it is
// approved.
func TestAPlanCarriesByHandInstructionsOnlyWhereSomebodyHasToAct(t *testing.T) {
	var owed int
	for _, tc := range []struct {
		name  string
		world func(*testing.T) world
	}{
		{name: "github-projects", world: githubProjectsWorld},
		{name: "jira-cloud", world: jiraWorld},
		{name: "nothing has deployed", world: nothingHasDeployedWorld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, item := range planFor(t, tc.world(t)).Items {
				// An attestation is somebody acting too: they make it true
				// and vouch for it, and are told how where the provider
				// says (issue #281).
				if item.Outcome == engine.OutcomeAttest {
					continue
				}
				if item.Outcome != engine.OutcomeFollowUp {
					if item.Recipe.Written() {
						t.Errorf("%s is %s and still carries instructions for doing it by hand: %+v",
							item.ID, item.Outcome, item.Recipe)
					}
					continue
				}
				if !item.Recipe.Written() {
					t.Errorf("%s is somebody else's job and says nothing about how to do it", item.ID)
					continue
				}
				owed++
				for _, text := range append([]string{item.Recipe.What, item.Recipe.Where}, item.Recipe.Steps...) {
					if strings.Contains(text, "{{") {
						t.Errorf("%s carries a placeholder nobody filled in: %q", item.ID, text)
					}
				}
			}
		})
	}
	if owed == 0 {
		t.Fatal("nothing in any of these plans was somebody else's job, so the test proved nothing")
	}
}

// The names in a recipe are this run's. A declaration that says "a record
// that a commit reached {{name}}" is asking flow for the one thing only flow
// knows; an adapter that could not ask would have to write for the general
// case and be read by somebody holding a particular one.
//
// This is the reported ticket: `create deploys.records "production"`, which
// named the environment and said nothing else anybody could act on.
func TestTheNamesInARecipeAreTheOnesThisRunIsAbout(t *testing.T) {
	plan := planFor(t, nothingHasDeployedWorld(t))

	item, ok := planItem(plan, core.ItemDeploysRecords)
	if !ok {
		t.Fatalf("no %s in the plan", core.ItemDeploysRecords)
	}
	if item.Outcome != engine.OutcomeFollowUp {
		t.Fatalf("%s is %s, so nobody is handed its recipe", item.ID, item.Outcome)
	}

	recipe := strings.Join(append([]string{item.Recipe.What, item.Recipe.Where}, item.Recipe.Steps...), "\n")
	// The environment the ticket is about, rather than "the environment".
	if !strings.Contains(item.Recipe.What, "production") {
		t.Errorf("the recipe does not name the environment it is about:\n%s", item.Recipe.What)
	}
	// And the repository, which is the other thing a declaration cannot know.
	repo := preset(t, config.PresetLeanKanban).Forge.Repo
	if !strings.Contains(recipe, repo) {
		t.Errorf("the recipe does not say which repository it is about (%s):\n%s", repo, recipe)
	}
	// The last step out is the one the reporter wanted and did not get: a
	// team that deploys nowhere is owed a way to stop being asked.
	if !strings.Contains(recipe, "deploys.source: none") {
		t.Errorf("the recipe never says how to stop flow asking:\n%s", recipe)
	}
}

// A provider is free to list the same thing twice, and a recipe that is
// about "Icebox and Icebox" reads as flow having lost count rather than as a
// board with two of them. The names on a recipe are the things the change is
// about, and a thing is one thing however many times it was listed.
func TestARecipeNamesAThingOnceHoweverOftenItWasListed(t *testing.T) {
	w := jiraWorld(t)
	// Two options of the same name, both flow's own doing, so both are
	// removed — and the whole of the change is still about Icebox.
	w.tracker = coretest.NewFakeTracker(trackerWithStatusOptions(t,
		core.Item{ID: "10001", Name: "To Do"},
		core.Item{ID: "opt-icebox", Name: "Icebox"},
		core.Item{ID: "opt-icebox-again", Name: "Icebox"},
	))
	w.lock = config.NewLock("v0.3.0")
	w.lock.ConfigHash = planConfigHash
	for _, id := range []string{"opt-icebox", "opt-icebox-again"} {
		w.lock.Record(config.Creation{
			Kind: engine.CreationKind(core.ItemTrackerStatusOptions),
			ID:   id,
			Name: "Icebox",
			At:   coretest.ContractNow.Add(-30 * 24 * time.Hour),
		})
	}

	item, ok := planItem(planFor(t, w), core.ItemTrackerStatusOptions)
	if !ok {
		t.Fatal("the status options are not in the plan")
	}
	if item.Outcome != engine.OutcomeFollowUp {
		t.Fatalf("%s is %s, so nobody is handed its recipe", item.ID, item.Outcome)
	}
	var removals int
	for _, change := range item.Changes {
		if change.Op == core.ChangeDelete && change.Item.Name == "Icebox" {
			removals++
		}
	}
	if removals != 2 {
		t.Fatalf("the plan removes Icebox %d times, so the recipe cannot be asked to name it once", removals)
	}

	recipe := strings.Join(append([]string{item.Recipe.What, item.Recipe.Where}, item.Recipe.Steps...), "\n")
	if n := strings.Count(recipe, "Icebox and Icebox"); n > 0 {
		t.Errorf("the recipe says Icebox twice over:\n%s", recipe)
	}
	if !strings.Contains(recipe, "Icebox") {
		t.Errorf("the recipe does not name the column it is about:\n%s", recipe)
	}
}

// A recipe may ask for a name the item it is declared on does not have: the
// board itself is an id to find, not a thing to alter, so there is nothing
// for `{{name}}` to be. The brace stays visible. A declaration written for
// the wrong item is a fault in the declaration, and a sentence with a hole
// where the word should be hides it from the person who would have to fix
// it.
func TestARecipeKeepsAPlaceholderTheItemHasNoWordFor(t *testing.T) {
	tracker := coretest.ContractTracker()
	tracker.Kind = "jira-cloud"
	tracker.Capabilities = capabilitiesAsking(t, "{{name}} of {{repo}}")
	tracker.Permissions = core.Permissions{Tokens: []string{"create_issues"}}
	tracker.Observations = map[string]core.Observation{"tracker.project": {Exists: false}}

	w := jiraWorld(t)
	w.tracker = coretest.NewFakeTracker(tracker)

	item, ok := planItem(planFor(t, w), core.ItemTrackerProject)
	if !ok {
		t.Fatal("the project is not in the plan")
	}
	if item.Outcome != engine.OutcomeFollowUp {
		t.Fatalf("%s is %s, so nobody is handed its recipe", item.ID, item.Outcome)
	}
	if len(item.Changes) != 0 {
		t.Fatalf("the board has changes after all, so it has names: %+v", item.Changes)
	}
	// The repository is known whatever the item is; the name is not.
	want := "{{name}} of " + preset(t, config.PresetLeanKanban).Forge.Repo
	if item.Recipe.What != want {
		t.Errorf("what = %q, want %q", item.Recipe.What, want)
	}
}

// capabilitiesAsking is Jira's own declaration with the board's recipe
// asking for the words the caller gives it, for the tests about what a
// recipe may ask for. The rest of the declaration is the shipped one: a
// capability set has to be whole, and the subject here is one sentence of
// it.
func capabilitiesAsking(t *testing.T, what string) core.CapabilitySet {
	t.Helper()
	set := testRegistry(t)["jira-cloud"]
	set.Capabilities = maps.Clone(set.Capabilities)
	board := set.Capabilities[core.CapProject]
	board.ByHand.What = what
	set.Capabilities[core.CapProject] = board
	return set
}

// trackerWithStatusOptions is the jira fixture with a board of the caller's
// own, for the tests whose subject is what the provider listed.
func trackerWithStatusOptions(t *testing.T, items ...core.Item) coretest.TrackerFixture {
	t.Helper()
	tracker := coretest.ContractTracker()
	tracker.Kind = "jira-cloud"
	tracker.Capabilities = testRegistry(t)["jira-cloud"]
	tracker.Permissions = core.Permissions{
		Tokens:   []string{"create_issues", "transition_issues"},
		Evidence: []string{"/rest/api/3/mypermissions: BROWSE_PROJECTS, CREATE_ISSUES"},
	}
	tracker.Observations = map[string]core.Observation{
		"tracker.project":        {Exists: true},
		"tracker.status_options": {Exists: true, Items: items},
		"tracker.labels":         {Exists: true},
	}
	return tracker
}

// planItem finds one item of a plan by id.
func planItem(plan engine.Plan, id string) (engine.PlanItem, bool) {
	for _, item := range plan.Items {
		if item.ID == id {
			return item, true
		}
	}
	return engine.PlanItem{}, false
}

// TestCreationKindsAreNamed is issue #204: a kind is the singular of the set,
// spelled out, not the set's id with its last letter trimmed.
func TestCreationKindsAreNamed(t *testing.T) {
	for id, want := range map[string]string{
		core.ItemTrackerStatusOptions: "tracker.status_option",
		core.ItemTrackerLabels:        "tracker.label",
		core.ItemForgeRequiredStatus:  "forge.required_status",
		core.ItemForgeFiles:           "forge.file",
	} {
		if got := engine.CreationKind(id); got != want {
			t.Errorf("CreationKind(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestThePresetAsksNoTrackerForWhatItCannotDo is spec 20 §4 on Jira: flow
// owns the merge transitions, so the shipped workflow no longer asks Jira for
// an issue-closed automation it does not have, and planning it is not a
// refusal (exit 1) but ordinary pending work (exit 2).
func TestThePresetAsksNoTrackerForWhatItCannotDo(t *testing.T) {
	plan := planFor(t, jiraWorld(t))
	for _, item := range plan.Items {
		if item.Outcome == engine.OutcomeUnsupported {
			t.Errorf("%s is unsupported under the shipped preset: %s", item.ID, item.Why)
		}
	}
	if got := plan.ExitCode(); got != 2 {
		t.Errorf("plan exits %d, want 2", got)
	}
}

// A follow-up's owner is who the lock says holds the role its target needs,
// once doctor has resolved it, and the default's holder when only that is
// known. The plan answered from the configuration alone and showed the role
// as `${pending:owners.tracker_admin}` after the lock had a name for it
// (#271).
func TestAFollowUpGoesToTheOwnerTheLockResolved(t *testing.T) {
	everybody := map[string]string{
		"followups.owners.default":       "@dev",
		"followups.owners.tracker_admin": "@board-admin",
		"followups.owners.forge_admin":   "@repo-admin",
		"followups.owners.platform":      "@sre",
	}
	worlds := map[string]func(t *testing.T) world{
		core.ItemTrackerStatusOptions: jiraWorld,
		core.ItemDeploysRecords:       nothingHasDeployedWorld,
		core.ItemForgeProtection: func(t *testing.T) world {
			w := jiraWorld(t)
			fixture := forgeFixture(t)
			fixture.Permissions = core.Permissions{Tokens: []string{"statuses_write", "contents_write", "pulls_write"}}
			w.forge = coretest.NewFakeForge(fixture)
			return w
		},
	}
	for _, tc := range []struct {
		name     string
		item     string
		resolved map[string]string
		want     string
	}{
		{name: "nobody yet", item: core.ItemTrackerStatusOptions, want: ""},
		{name: "the default holder", item: core.ItemTrackerStatusOptions, resolved: map[string]string{"followups.owners.default": "@dev"}, want: "@dev"},
		{name: "the board's", item: core.ItemTrackerStatusOptions, resolved: everybody, want: "@board-admin"},
		{name: "the repository's", item: core.ItemForgeProtection, resolved: everybody, want: "@repo-admin"},
		{name: "the platform's", item: core.ItemDeploysRecords, resolved: everybody, want: "@sre"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worlds[tc.item](t)
			w.lock = config.NewLock("v0.3.0")
			for path, handle := range tc.resolved {
				w.lock.Resolve(path, handle)
			}
			item, ok := planFor(t, w).Find(tc.item)
			if !ok || item.Outcome != engine.OutcomeFollowUp {
				t.Fatalf("%s is %+v, want a follow-up for this test to mean anything", tc.item, item)
			}
			if item.Owner != tc.want {
				t.Errorf("owner = %q, want %q", item.Owner, tc.want)
			}
		})
	}
}
