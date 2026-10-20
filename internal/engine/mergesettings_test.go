package engine_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// A merge setting is written whole: the forge takes every field of it in one
// call, whatever flow meant to change. So the question each of these asks is
// not "did flow write what it planned" but "what became of the fields the
// plan never mentioned" (spec 06 §4).

// mergeItems is what the plan says about the repository's merge settings for
// a configuration that squashes, which is both items filled in.
func mergeItems(t *testing.T, cfg *config.Config) (strategies, text core.Item) {
	t.Helper()
	state := desired(t, cfg)
	wanted, ok := state.Find(core.ItemForgeMergeSettings)
	if !ok || len(wanted.Items) == 0 {
		t.Fatalf("the desired state asks nothing of %s", core.ItemForgeMergeSettings)
	}
	squash, ok := state.Find(core.ItemForgeSquashText)
	if !ok || len(squash.Items) == 0 {
		t.Fatalf("the desired state asks nothing of %s", core.ItemForgeSquashText)
	}
	return wanted.Items[0], squash.Items[0]
}

// TestApplyKeepsAMergeSettingThePlanNeverMentioned is the defect itself: a
// repository that deletes the branch after a merge, a drift in a field that
// has nothing to do with that, and an apply that writes the setting whole.
// Nothing in the plan said the branches would start piling up.
func TestApplyKeepsAMergeSettingThePlanNeverMentioned(t *testing.T) {
	w := githubProjectsWorld(t)
	fixture := forgeFixture(t)
	fixture.Repo.Merge.DeleteBranchOnMerge = true
	w.forge = coretest.NewFakeForge(fixture)

	applied, _ := applyTo(t, w, engine.ApplyInput{})
	if merge := item(t, applied, core.ItemForgeMergeSettings); !merge.Done() {
		t.Fatalf("the merge settings were not written: %v %s", merge.Err, merge.Skipped)
	}

	repo, err := w.forge.Repo(context.Background())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	if repo.Merge.AllowMerge {
		t.Fatal("the drift flow came for is still there, so this proves nothing about the write")
	}
	if !repo.Merge.DeleteBranchOnMerge {
		t.Error("flow turned off deleting the branch after a merge, which no plan ever mentioned")
	}
}

// TestEveryMergeSettingIsWrittenFromThePlanOrKept is the rule rather than the
// instance: every field of the setting is either one a desired item names, or
// one flow leaves exactly as it found it. A field added to the struct and to
// nothing else fails here, where it is cheap, rather than by being switched
// off in somebody's repository.
//
// It asks mechanically which of the two a field is, by writing the same plan
// over two different repositories: a field the plan names comes out the same
// both times, and a field it does not comes out as each repository had it.
func TestEveryMergeSettingIsWrittenFromThePlanOrKept(t *testing.T) {
	// The fields flow has no opinion about, with why. Everything else must
	// be written from the plan.
	kept := map[string]string{
		"DeleteBranchOnMerge": "no desired item asks for it, and a team's branch housekeeping is its own",
		"AllowAutoMerge":      "a configuration that does not ask for it has not asked for its absence either (issue #167)",
	}

	strategies, text := mergeItems(t, preset(t, config.PresetLeanKanban))
	kind := reflect.TypeOf(core.MergeSettings{})
	for i := range kind.NumField() {
		field := kind.Field(i)
		var other core.MergeSettings
		value := reflect.ValueOf(&other).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.String:
			value.SetString("something-else")
		case reflect.Bool:
			value.SetBool(true)
		default:
			t.Fatalf("MergeSettings.%s is a %s, which this test cannot vary", field.Name, field.Type.Kind())
		}

		from := engine.MergeSettingsFor(core.MergeSettings{}, strategies, text)
		carried := engine.MergeSettingsFor(other, strategies, text)
		decided := reflect.ValueOf(from).Field(i).Equal(reflect.ValueOf(carried).Field(i))

		why, exempt := kept[field.Name]
		switch {
		case exempt && decided:
			t.Errorf("MergeSettings.%s is listed as kept (%s) but the plan decides it; take it off the list", field.Name, why)
		case !exempt && !decided:
			t.Errorf("MergeSettings.%s is written by flow and named by no desired item, so a write made for some other reason sets it to %v", field.Name, reflect.ValueOf(from).Field(i))
		}
	}
}

// TestAMergeSettingTheItemNamesIsWrittenEvenWhenItIsNo states the other half:
// a field is kept because the plan is silent about it, not because the plan
// happens to want nothing of it. "Allow merge commits: no" is an answer.
func TestAMergeSettingTheItemNamesIsWrittenEvenWhenItIsNo(t *testing.T) {
	strategies, text := mergeItems(t, preset(t, config.PresetLeanKanban))
	current := core.MergeSettings{AllowSquash: true, AllowMerge: true, AllowRebase: true}

	got := engine.MergeSettingsFor(current, strategies, text)
	if !got.AllowSquash {
		t.Error("the strategy the configuration asks for was not allowed")
	}
	if got.AllowMerge || got.AllowRebase {
		t.Errorf("merge commits (%v) and rebase (%v) survived a plan that says no to both", got.AllowMerge, got.AllowRebase)
	}
	if got.SquashTitle != core.SquashTitlePR || got.SquashMessage != core.SquashMessagePRBody {
		t.Errorf("the squash text is %q/%q, want it taken from the pull request", got.SquashTitle, got.SquashMessage)
	}
}

// TestAStrategyTheItemLeavesOutIsTheRepositorysOwn is what makes a setting
// askable for additively: an item that names one field of the setting and not
// the others is an opinion about that field alone. Without it the only way to
// want nothing of a field would be to ask every forge for a "no", including
// the ones with no switch to say it with.
func TestAStrategyTheItemLeavesOutIsTheRepositorysOwn(t *testing.T) {
	only := core.Item{Name: "merge", Fields: map[string]string{engine.FieldAllowSquash: "true"}}
	current := core.MergeSettings{AllowMerge: true, AllowRebase: true}

	got := engine.MergeSettingsFor(current, only, core.Item{})
	if !got.AllowSquash {
		t.Error("the one field the item named was not written")
	}
	if !got.AllowMerge || !got.AllowRebase {
		t.Errorf("merge commits (%v) and rebase (%v) were answered by an item that said nothing about either", got.AllowMerge, got.AllowRebase)
	}
}

// TestAForgeThatComposesItsOwnSquashKeepsTheTextItHas covers the item that is
// not there at all. A configuration that does not squash asks nothing about
// where a squash commit's words come from, and asking nothing is not the same
// as asking for nothing.
func TestAForgeThatComposesItsOwnSquashKeepsTheTextItHas(t *testing.T) {
	strategies, _ := mergeItems(t, preset(t, config.PresetLeanKanban))
	current := core.MergeSettings{SquashTitle: core.SquashTitleCommit, SquashMessage: core.SquashMessageCommits}

	got := engine.MergeSettingsFor(current, strategies, core.Item{})
	if got.SquashTitle != core.SquashTitleCommit || got.SquashMessage != core.SquashMessageCommits {
		t.Errorf("the squash text became %q/%q, want it left as the repository had it", got.SquashTitle, got.SquashMessage)
	}
}

// TestAutoMergeIsKeptUntilTheConfigurationAsksForIt is the other half of the
// exemption above. `forge.auto_merge` is the one field of the setting flow
// names conditionally: a team that wants it gets it written, and a team that
// says nothing keeps whatever the repository had, because two of the three
// providers have no switch to say "no" with (spec 06 §4). Exempting it from
// the rule without this would exempt it from being written at all.
func TestAutoMergeIsKeptUntilTheConfigurationAsksForIt(t *testing.T) {
	permitted := core.MergeSettings{AllowAutoMerge: true}

	silent, text := mergeItems(t, preset(t, config.PresetLeanKanban))
	if got := engine.MergeSettingsFor(permitted, silent, text); !got.AllowAutoMerge {
		t.Error("a configuration that says nothing about auto-merge withdrew it")
	}

	cfg := preset(t, config.PresetLeanKanban)
	cfg.Forge.AutoMerge = true
	asking, text := mergeItems(t, cfg)
	if got := engine.MergeSettingsFor(core.MergeSettings{}, asking, text); !got.AllowAutoMerge {
		t.Error("a configuration that asks for auto-merge did not get it written")
	}
}
