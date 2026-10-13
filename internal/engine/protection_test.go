package engine_test

import (
	"context"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// Whether a branch has to be up to date with the trunk before it may merge
// (spec 10 §2). A status is about the commit it was posted on, so a gate that
// passed on a branch which has since fallen behind was passed on a tree that
// is not the one landing — two pull requests, each green against the trunk it
// left, can still be red together.

// protectionItem is the one item flow wants of the trunk.
func protectionItem(t *testing.T, cfg *config.Config) core.Item {
	t.Helper()
	wanted, ok := desired(t, cfg).Find(core.ItemForgeProtection)
	if !ok {
		t.Fatal("nothing in the desired state protects the trunk")
	}
	if len(wanted.Items) != 1 {
		t.Fatalf("protection is %d items, want the one", len(wanted.Items))
	}
	return wanted.Items[0]
}

// TestTheTrunkIsWantedUpToDateWhereverFlowRequiresACheck states the rule and
// its limit in one place. Currency is a property of a required check: where
// flow requires one it wants the branch current, and where it requires none
// there is nothing for currency to be about, so it asks for nothing.
func TestTheTrunkIsWantedUpToDateWhereverFlowRequiresACheck(t *testing.T) {
	gated := preset(t, config.PresetLeanKanban)
	if gated.Policy.StatusContext == "" {
		t.Fatal("the lean-kanban preset requires no check, so this proves nothing")
	}
	if got := protectionItem(t, gated).Field(engine.FieldStrictChecks); got != "true" {
		t.Errorf("%s = %q with %q required, want the branch up to date",
			engine.FieldStrictChecks, got, gated.Policy.StatusContext)
	}

	ungated := preset(t, config.PresetLeanKanban)
	ungated.Policy.StatusContext = ""
	if got := protectionItem(t, ungated).Field(engine.FieldStrictChecks); got != "false" {
		t.Errorf("%s = %q with no check required, want flow to ask for nothing",
			engine.FieldStrictChecks, got)
	}
}

// TestWhatIsWrittenIsWhatWasWanted: the desired item is read back into the
// protection flow hands the forge. Without this the field would be in the
// plan a person approves and in nothing that reaches the provider.
func TestWhatIsWrittenIsWhatWasWanted(t *testing.T) {
	wanted := engine.ProtectionFor("main",
		core.Item{Name: engine.NameProtection, Fields: map[string]string{
			engine.FieldEnabled:      "true",
			engine.FieldStrictChecks: "true",
		}},
		[]core.Item{{Name: "flow/policy"}})
	if !wanted.StrictChecks {
		t.Error("the protection flow would write lets a branch merge behind the trunk")
	}

	unwanted := engine.ProtectionFor("main",
		core.Item{Name: engine.NameProtection, Fields: map[string]string{engine.FieldEnabled: "true"}},
		[]core.Item{{Name: "flow/policy"}})
	if unwanted.StrictChecks {
		t.Error("flow would make a branch current without having asked to")
	}
}

// TestAForgeThatLetsABranchMergeBehindIsTheOnlyWorkLeft is the whole of it
// against a provider. Both trunks are everything else flow wants — protected,
// unrewritable, linear, already requiring flow's own check — so currency is
// the only thing left that could give flow something to do, and it is what
// decides whether it has any.
func TestAForgeThatLetsABranchMergeBehindIsTheOnlyWorkLeft(t *testing.T) {
	for _, tc := range []struct {
		name    string
		strict  bool
		changes bool
	}{
		{"a trunk a stale branch may merge into", false, true},
		{"a trunk that is already current", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := forgeFixture(t)
			fixture.Protections = map[string]core.Protection{"main": {
				Enabled:              true,
				RequiredChecks:       []string{"flow/policy"},
				RequireLinearHistory: true,
				StrictChecks:         tc.strict,
				RequiredApprovals:    1,
			}}
			w := githubProjectsWorld(t)
			w.forge = coretest.NewFakeForge(fixture)

			item, ok := planFor(t, w).Find(core.ItemForgeProtection)
			if !ok {
				t.Fatal("the plan says nothing about the trunk's protection")
			}
			if got := len(item.Changes) > 0; got != tc.changes {
				t.Errorf("the plan has %d changes for the trunk, want changes=%v: %+v",
					len(item.Changes), tc.changes, item.Changes)
			}
			if !tc.changes {
				return
			}
			if item.Changes[0].Item.Fields[engine.FieldStrictChecks] != "true" {
				t.Errorf("the change flow proposes does not ask for currency: %+v", item.Changes[0].Item)
			}
		})
	}
}

// TestTheForgeIsAskedWhetherABranchMayMergeBehind: the field has to come back
// off the provider as well as go to it, or flow would propose the same change
// every run and `flow doctor` would never see it settled.
func TestTheForgeIsAskedWhetherABranchMayMergeBehind(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	fixture := forgeFixture(t)
	fixture.Protections = map[string]core.Protection{
		"main": {Enabled: true, RequiredChecks: []string{"flow/policy"}, StrictChecks: true},
	}
	w := githubProjectsWorld(t)
	w.forge = coretest.NewFakeForge(fixture)

	observed, err := engine.Observe(context.Background(), desired(t, cfg), w.adapters())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	seen, ok := observed[core.ItemForgeProtection]
	if !ok || len(seen.Items) != 1 {
		t.Fatalf("the trunk's protection was observed as %+v", seen)
	}
	if got := seen.Items[0].Field(engine.FieldStrictChecks); got != "true" {
		t.Errorf("%s was observed as %q on a trunk that requires it, want %q",
			engine.FieldStrictChecks, got, "true")
	}
}
