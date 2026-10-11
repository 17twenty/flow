package engine_test

import (
	"flag"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// testTemplates is the shipped file templates, read from the tree. The root
// passes assets.Templates(); the engine's tests use the same bytes, so that a
// template nobody can read is a failing test rather than a broken proposal.
func testTemplates(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(filepath.Join("..", "..", "assets", "templates"))
}

// testPresets is the shipped configuration presets, read from the tree.
func testPresets(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(filepath.Join("..", "..", "assets", "presets"))
}

// preset loads one shipped preset.
func preset(t *testing.T, name string) *config.Config {
	t.Helper()
	cfg, err := config.LoadPreset(testPresets(t), name)
	if err != nil {
		t.Fatalf("load preset %s: %v", name, err)
	}
	return cfg
}

// desired derives the desired state from a configuration.
func desired(t *testing.T, cfg *config.Config) engine.Desired {
	t.Helper()
	state, err := engine.NewDesired(cfg, testTemplates(t))
	if err != nil {
		t.Fatalf("desired state: %v", err)
	}
	return state
}

// TestDesiredGolden pins what each shipped preset asks of a provider. The
// desired state is the whole of what flow will try to change, so a change to
// it should be a change to a file somebody had to read.
//
// Run go test ./internal/engine -run TestDesiredGolden -update to rewrite them.
func TestDesiredGolden(t *testing.T) {
	for _, name := range config.PresetNames {
		t.Run(name, func(t *testing.T) {
			state := desired(t, preset(t, name))
			encoded := state.Canonical()

			golden := filepath.Join("testdata", "desired", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with -update to write it)", err)
			}
			if string(encoded) != string(want) {
				t.Errorf("the %s preset wants something different than the golden file:\n--- got\n%s\n--- want\n%s", name, encoded, want)
			}
		})
	}
}

// TestDesiredIsPure insists that the same configuration always asks for the
// same thing. A desired state that depended on a map's iteration order would
// make every plan a coin toss.
func TestDesiredIsPure(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	first := desired(t, cfg).Hash()
	for range 32 {
		if again := desired(t, cfg).Hash(); again != first {
			t.Fatalf("the same configuration hashed to %s and then %s", first, again)
		}
	}
}

// TestDesiredCoversSpecTable checks the desired items against spec 06 §1,
// which is the list flow is held to for the default preset on GitHub.
func TestDesiredCoversSpecTable(t *testing.T) {
	state := desired(t, preset(t, config.PresetLeanKanban))
	want := []string{
		engine.ItemTrackerProject,
		engine.ItemTrackerStatusOptions,
		engine.ItemTrackerLabels,
		engine.NativeItem(config.EventChangesRequested),
		engine.NativeItem(config.EventPRMerged),
		engine.NativeItem(config.EventIssueClosed),
		engine.ItemTrackerWIP,
		engine.ItemTrackerExpedite,
		engine.ItemForgeDefaultBranch,
		engine.ItemForgeProtection,
		engine.ItemForgeRequiredStatus,
		engine.ItemForgeMergeSettings,
		engine.ItemForgeFiles,
		engine.ItemDeploysRecords,
	}
	got := state.IDs()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("desired items are\n%v\nspec 06 §1 asks for\n%v", got, want)
	}
}

// TestDesiredDependencyOrder insists that an item never appears before
// something it depends on, which is the order apply executes in (spec 06 §3).
func TestDesiredDependencyOrder(t *testing.T) {
	for _, name := range config.PresetNames {
		t.Run(name, func(t *testing.T) {
			state := desired(t, preset(t, name))
			var seen []string
			for _, item := range state.Items {
				for _, dependency := range item.DependsOn {
					if _, ok := state.Find(dependency); !ok {
						t.Errorf("%s depends on %s, which is not wanted at all", item.ID, dependency)
					}
					if !slices.Contains(seen, dependency) {
						t.Errorf("%s comes before %s, which it depends on", item.ID, dependency)
					}
				}
				seen = append(seen, item.ID)
			}
		})
	}
}

// TestDesiredCapabilitiesAreInTheRegistry holds the desired state to spec 05
// §2: a capability flow asks for by a name no adapter can declare would be
// unsupported for ever, and nobody would know why.
func TestDesiredCapabilitiesAreInTheRegistry(t *testing.T) {
	kinds := map[string]string{
		engine.TargetTracker: core.KindTracker,
		engine.TargetForge:   core.KindForge,
		engine.TargetDeploys: core.KindDeploys,
	}
	for _, name := range config.PresetNames {
		for _, item := range desired(t, preset(t, name)).Items {
			if item.Capability == "" {
				continue
			}
			kind, ok := kinds[item.Target]
			if !ok {
				t.Errorf("%s belongs to target %q, which is not a kind of adapter", item.ID, item.Target)
				continue
			}
			if !core.KnownCapability(kind, item.Capability) {
				t.Errorf("%s wants %s capability %q, which is not in the registry", item.ID, kind, item.Capability)
			}
		}
	}
}

// TestDesiredResolveItemsHaveNoContents: an item that only has to be found
// has nothing to compare, and an item that has to be made so does.
func TestDesiredResolveItemsHaveNoContents(t *testing.T) {
	for _, item := range desired(t, preset(t, config.PresetLeanKanban)).Items {
		if item.Resolve && len(item.Items) > 0 {
			t.Errorf("%s only has to be found, but wants %d contents", item.ID, len(item.Items))
		}
		if !item.Resolve && len(item.Items) == 0 {
			t.Errorf("%s has to be made so, but says nothing about what it should look like", item.ID)
		}
	}
}

// TestDesiredLabels derives the label set from the configuration, including
// the fixed Definition of Ready vocabulary spec 04 §7 names.
func TestDesiredLabels(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	item, ok := desired(t, cfg).Find(engine.ItemTrackerLabels)
	if !ok {
		t.Fatal("no labels are wanted")
	}
	var got []string
	for _, label := range item.Items {
		got = append(got, label.Name)
		if len(label.Fields) > 0 {
			t.Errorf("label %q carries %v; flow has no opinion about a label beyond its existing", label.Name, label.Fields)
		}
	}
	want := []string{"agent", "blocked", "flow-followup", "incident", "ui", "user-facing"}
	if !slices.Equal(got, want) {
		t.Errorf("labels are %v, spec 06 §1 asks for %v", got, want)
	}

	cfg.Workflow.Blocked.Label = "stuck"
	cfg.Workflow.Expedite.Labels = nil
	item, _ = desired(t, cfg).Find(engine.ItemTrackerLabels)
	if _, found := labelled(item, "stuck"); !found {
		t.Error("renaming the blocked label did not change what flow wants")
	}
	if _, found := labelled(item, "incident"); found {
		t.Error("a team that does not expedite is still asked for an incident label")
	}
}

func labelled(item engine.DesiredItem, name string) (core.Item, bool) {
	for _, label := range item.Items {
		if label.Name == name {
			return label, true
		}
	}
	return core.Item{}, false
}

// TestDesiredNativeTransitions wants one item per transition the team handed
// to the tracker, and none for the ones flow performs itself.
func TestDesiredNativeTransitions(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	state := desired(t, cfg)
	for event, transition := range cfg.Workflow.Transitions {
		item, ok := state.Find(engine.NativeItem(event))
		if transition.Owner != config.OwnerNative {
			if ok {
				t.Errorf("%s is owned by flow, but flow asks the tracker to do it", event)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s is owned by the tracker, but flow asks it for nothing", event)
		}
		want, _ := core.NativeTransitionCapability(event)
		if item.Capability != want {
			t.Errorf("%s wants capability %q, want %q", item.ID, item.Capability, want)
		}
		column, _ := cfg.Workflow.Column(transition.To)
		if got := item.Items[0].Field(engine.FieldTo); got != column.Name {
			t.Errorf("%s lands in %q, want the column's display name %q", item.ID, got, column.Name)
		}
	}
}

// TestDesiredOmitsWhatWasNotAskedFor: nothing in the desired state is there
// because flow likes it. A team that does not deploy, does not expedite, does
// not limit work in progress and publishes no status is asked for none of it.
func TestDesiredOmitsWhatWasNotAskedFor(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Deploys.Source = config.DeploySourceNone
	cfg.Workflow.Expedite.Labels = nil
	cfg.Policy.StatusContext = ""
	for i := range cfg.Workflow.Columns {
		cfg.Workflow.Columns[i].WIP = nil
	}
	state := desired(t, cfg)
	for _, id := range []string{
		engine.ItemDeploysRecords,
		engine.ItemTrackerExpedite,
		engine.ItemForgeRequiredStatus,
		engine.ItemTrackerWIP,
	} {
		if _, ok := state.Find(id); ok {
			t.Errorf("%s is wanted by a configuration that did not ask for it", id)
		}
	}
}

// TestDesiredFiles proposes GitHub's files to a GitHub repository, asks the
// workflow to run flow's commands, and proposes nothing to a forge whose
// layout flow does not ship.
func TestDesiredFiles(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	item, ok := desired(t, cfg).Find(engine.ItemForgeFiles)
	if !ok {
		t.Fatal("no files are proposed to a GitHub repository")
	}
	workflow, found := labelled(item, ".github/workflows/flow.yml")
	if !found {
		t.Fatal("the CI workflow is not proposed")
	}
	if got := workflow.Field(engine.FieldRuns); got != "flow check pr" {
		t.Errorf("the workflow must run %q; done_on is merged, so there is no deploy to check", got)
	}
	for _, file := range item.Files {
		if len(file.Content) == 0 {
			t.Errorf("%s is proposed with no content", file.Path)
		}
	}
	if len(item.Files) != len(item.Items) {
		t.Errorf("%d files are proposed for %d wanted paths", len(item.Files), len(item.Items))
	}

	cfg.Workflow.DoneOn = config.DoneOnDeployed
	item, _ = desired(t, cfg).Find(engine.ItemForgeFiles)
	workflow, _ = labelled(item, ".github/workflows/flow.yml")
	if got := workflow.Field(engine.FieldRuns); !strings.Contains(got, "flow check deploy") {
		t.Errorf("done means deployed, but the workflow is only asked to run %q", got)
	}

	cfg.Forge.Type = config.ForgeGitLab
	if _, ok := desired(t, cfg).Find(engine.ItemForgeFiles); ok {
		t.Error("GitHub's files are proposed to a GitLab repository")
	}
}

// TestDesiredRefusesToGuessTemplates: flow proposes the files it ships, so a
// build without them is an error rather than a proposal of nothing.
func TestDesiredRefusesToGuessTemplates(t *testing.T) {
	if _, err := engine.NewDesired(preset(t, config.PresetLeanKanban), nil); err == nil {
		t.Fatal("the desired state was derived without the templates it proposes")
	}
	empty := os.DirFS(t.TempDir())
	if _, err := engine.NewDesired(preset(t, config.PresetLeanKanban), empty); err == nil {
		t.Fatal("a missing template passed unnoticed")
	}
	if _, err := engine.NewDesired(nil, testTemplates(t)); err == nil {
		t.Fatal("the desired state was derived from no configuration at all")
	}
}

// TestDesiredMergeSettings allows the configured strategy and refuses the
// others, so that the header the gate checked is the header that lands.
func TestDesiredMergeSettings(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		fields   map[string]string
	}{
		{config.MergeSquash, map[string]string{
			"allow_squash": "true", "allow_merge": "false", "allow_rebase": "false",
			"squash_title": core.SquashTitlePR, "squash_message": core.SquashMessagePRBody,
		}},
		{config.MergeCommit, map[string]string{
			"allow_squash": "false", "allow_merge": "true", "allow_rebase": "false",
		}},
		{config.MergeRebase, map[string]string{
			"allow_squash": "false", "allow_merge": "false", "allow_rebase": "true",
		}},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			cfg := preset(t, config.PresetLeanKanban)
			cfg.Commits.MergeStrategy = tc.strategy
			state := desired(t, cfg)

			item, ok := state.Find(engine.ItemForgeMergeSettings)
			if !ok {
				t.Fatal("no merge settings are wanted")
			}
			if !maps.Equal(item.Items[0].Fields, tc.fields) {
				t.Errorf("merge settings are %v, want %v", item.Items[0].Fields, tc.fields)
			}

			protection, _ := state.Find(engine.ItemForgeProtection)
			linear := protection.Items[0].Field(engine.FieldRequireLinearHistory)
			if want := tc.strategy != config.MergeCommit; (linear == "true") != want {
				t.Errorf("a %s merge wants linear history %q", tc.strategy, linear)
			}
		})
	}
}

// TestDesiredProtectionIsWhatTheConfigurationImplies: flow protects the trunk
// and does not invent a review policy nobody wrote down (spec 06 §4).
func TestDesiredProtectionIsWhatTheConfigurationImplies(t *testing.T) {
	item, ok := desired(t, preset(t, config.PresetLeanKanban)).Find(engine.ItemForgeProtection)
	if !ok {
		t.Fatal("the trunk is not protected")
	}
	fields := item.Items[0].Fields
	for field, want := range map[string]string{
		engine.FieldEnabled:              "true",
		engine.FieldAllowForcePush:       "false",
		engine.FieldAllowDeletions:       "false",
		engine.FieldRequireLinearHistory: "true",
	} {
		if fields[field] != want {
			t.Errorf("protection %s is %q, want %q", field, fields[field], want)
		}
	}
	if _, invented := fields["required_approvals"]; invented {
		t.Error("flow invented a required approval count; spec 09's gate is what insists on review")
	}
}

// TestHashIgnoresOrder is the point of a canonical form: the same board
// observed twice hashes the same, whatever order the provider answered in
// (spec 06 §2).
func TestHashIgnoresOrder(t *testing.T) {
	items := []core.Item{
		{ID: "2", Name: "Ready", Fields: map[string]string{"position": "1", "colour": "blue"}},
		{ID: "1", Name: "Backlog", Fields: map[string]string{"position": "0"}},
		{ID: "3", Name: "Done", Fields: map[string]string{"position": "2"}},
	}
	observation := core.Observation{What: engine.ItemTrackerStatusOptions, Exists: true, Items: items}
	want := engine.HashObservation(observation)

	for range 64 {
		shuffled := slices.Clone(items)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for i, item := range shuffled {
			// Rebuild each map so that its internal order differs too.
			rebuilt := map[string]string{}
			maps.Copy(rebuilt, item.Fields)
			shuffled[i].Fields = rebuilt
		}
		got := engine.HashObservation(core.Observation{What: engine.ItemTrackerStatusOptions, Exists: true, Items: shuffled})
		if got != want {
			t.Fatalf("the same board hashed to %s and then %s", want, got)
		}
	}
}

// TestHashNoticesEveryDifference: a hash that missed a change would let drift
// through unseen (spec 07 §7).
func TestHashNoticesEveryDifference(t *testing.T) {
	base := core.Observation{
		What:   engine.ItemTrackerStatusOptions,
		Exists: true,
		Items:  []core.Item{{ID: "1", Name: "Backlog", Fields: map[string]string{"position": "0"}}},
	}
	for name, changed := range map[string]core.Observation{
		"what":   {What: "tracker.labels", Exists: base.Exists, Items: base.Items},
		"exists": {What: base.What, Exists: false, Items: base.Items},
		"id": {What: base.What, Exists: true, Items: []core.Item{
			{ID: "2", Name: "Backlog", Fields: map[string]string{"position": "0"}}}},
		"name": {What: base.What, Exists: true, Items: []core.Item{
			{ID: "1", Name: "backlog", Fields: map[string]string{"position": "0"}}}},
		"field value": {What: base.What, Exists: true, Items: []core.Item{
			{ID: "1", Name: "Backlog", Fields: map[string]string{"position": "1"}}}},
		"field name": {What: base.What, Exists: true, Items: []core.Item{
			{ID: "1", Name: "Backlog", Fields: map[string]string{"order": "0"}}}},
		"extra field": {What: base.What, Exists: true, Items: []core.Item{
			{ID: "1", Name: "Backlog", Fields: map[string]string{"position": "0", "colour": "grey"}}}},
		"extra item": {What: base.What, Exists: true, Items: append(slices.Clone(base.Items),
			core.Item{ID: "2", Name: "Ready"})},
		"nothing at all": {What: base.What, Exists: true},
	} {
		if engine.HashObservation(changed) == engine.HashObservation(base) {
			t.Errorf("a different %s hashed the same", name)
		}
	}
}

// TestCanonicalCannotBeForged: a value that looks like the encoding must not
// be able to impersonate it. Without quoting, an item named with a newline
// could write a field of its own.
func TestCanonicalCannotBeForged(t *testing.T) {
	honest := core.Observation{What: "x", Exists: true, Items: []core.Item{
		{Name: "a", Fields: map[string]string{"f": "v"}},
	}}
	forged := core.Observation{What: "x", Exists: true, Items: []core.Item{
		{Name: "a\nfield \"f\" \"v\""},
	}}
	if engine.HashObservation(honest) == engine.HashObservation(forged) {
		t.Fatal("an item forged a field by putting the encoding in its name")
	}
	for _, line := range strings.Split(string(engine.CanonicalObservation(forged)), "\n") {
		if strings.HasPrefix(line, "field ") {
			t.Fatalf("the forged name escaped its record: %q", line)
		}
	}
}

// TestHashFormIsTheLockForm: the lock records "sha256:…" and nothing else
// (spec 03 §3).
func TestHashFormIsTheLockForm(t *testing.T) {
	hash := engine.HashObservation(core.Observation{What: "x"})
	if !strings.HasPrefix(hash, "sha256:") || len(hash) != len("sha256:")+64 {
		t.Errorf("hash %q is not the form the lock records", hash)
	}
	if hash != config.Hash(engine.CanonicalObservation(core.Observation{What: "x"})) {
		t.Error("the observation hash is not the hash of its canonical form")
	}
}

// TestDesiredItemHashIsPerItem: changing one item's contents changes that
// item's hash and the whole state's, and leaves the other items alone.
func TestDesiredItemHashIsPerItem(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	before := desired(t, cfg)

	cfg.Workflow.Columns[0].Name = "Icebox"
	after := desired(t, cfg)

	if before.Hash() == after.Hash() {
		t.Fatal("renaming a column did not change the desired state")
	}
	beforeOptions, _ := before.Find(engine.ItemTrackerStatusOptions)
	afterOptions, _ := after.Find(engine.ItemTrackerStatusOptions)
	if beforeOptions.Hash() == afterOptions.Hash() {
		t.Error("renaming a column did not change the status options")
	}
	beforeLabels, _ := before.Find(engine.ItemTrackerLabels)
	afterLabels, _ := after.Find(engine.ItemTrackerLabels)
	if beforeLabels.Hash() != afterLabels.Hash() {
		t.Error("renaming a column changed the labels")
	}
}

// TestDesiredFileHashesFollowTheTemplates: the proposal is part of what the
// plan promises, so a changed template is a changed plan.
func TestDesiredFileHashesFollowTheTemplates(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	item, _ := desired(t, cfg).Find(engine.ItemForgeFiles)
	changed := item
	changed.Files = slices.Clone(item.Files)
	changed.Files[0].Content = append(slices.Clone(changed.Files[0].Content), '\n')
	if item.Hash() == changed.Hash() {
		t.Fatal("a changed template left the desired files unchanged")
	}
	if strings.Contains(string(item.Canonical()), "runs-on") {
		t.Error("the canonical form carries the template's bytes rather than their hash")
	}
}

// TestDesiredTarget groups the items the way `flow plan --target` does.
func TestDesiredTarget(t *testing.T) {
	state := desired(t, preset(t, config.PresetLeanKanban))
	var counted int
	for _, target := range []string{engine.TargetTracker, engine.TargetForge, engine.TargetDeploys} {
		items := state.Target(target)
		if len(items) == 0 {
			t.Errorf("nothing belongs to %s", target)
		}
		for _, item := range items {
			if item.Target != target {
				t.Errorf("%s was returned for target %s", item.ID, target)
			}
		}
		counted += len(items)
	}
	if counted != len(state.Items) {
		t.Errorf("%d of %d items belong to a target", counted, len(state.Items))
	}
}
