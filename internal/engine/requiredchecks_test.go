package engine_test

import (
	"slices"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// What a merge must pass (spec 03 §1, spec 06 §4). Flow's own gate is flow's
// business; the build is the team's, and forge.required_checks is where they
// say so. The item is additive, so naming a check here asks for it where it
// is missing and never takes anybody else's away.

// requiredContexts is the contexts the desired state wants of the trunk, in
// the order it wants them.
func requiredContexts(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	wanted, ok := desired(t, cfg).Find(core.ItemForgeRequiredStatus)
	if !ok {
		return nil
	}
	contexts := make([]string, 0, len(wanted.Items))
	for _, item := range wanted.Items {
		contexts = append(contexts, item.Name)
	}
	return contexts
}

// TestTheTrunkRequiresTheGateAndWhatTheTeamNamedBesideIt: both halves, and
// the same plan however they were written down. The desired state
// canonicalises its items, so the order in the file is not a difference a
// hash can see.
func TestTheTrunkRequiresTheGateAndWhatTheTeamNamedBesideIt(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Policy.StatusContext = "flow/policy"
	cfg.Forge.RequiredChecks = []string{"test", "lint"}

	want := []string{"flow/policy", "lint", "test"}
	if got := requiredContexts(t, cfg); !slices.Equal(got, want) {
		t.Errorf("the trunk requires %q, want %q", got, want)
	}

	rewritten := preset(t, config.PresetLeanKanban)
	rewritten.Policy.StatusContext = "flow/policy"
	rewritten.Forge.RequiredChecks = []string{"lint", "test"}
	if got := requiredContexts(t, rewritten); !slices.Equal(got, want) {
		t.Errorf("written the other way round the trunk requires %q, want %q", got, want)
	}
}

// TestATeamMayRequireABuildWithoutRunningTheGate: the two halves are
// independent. A repository that publishes no status of flow's own still gets
// the checks it named, and still wants branches current — currency is a
// property of a required check, whoever required it.
func TestATeamMayRequireABuildWithoutRunningTheGate(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Policy.StatusContext = ""
	cfg.Forge.RequiredChecks = []string{"test"}

	want := []string{"test"}
	if got := requiredContexts(t, cfg); !slices.Equal(got, want) {
		t.Errorf("the trunk requires %q, want %q", got, want)
	}
	if got := protectionItem(t, cfg).Field(engine.FieldStrictChecks); got != "true" {
		t.Errorf("%s = %q with %q required by the team, want the branch up to date",
			engine.FieldStrictChecks, got, "test")
	}
}

// TestNamingTheGatesOwnContextAgainIsNotASecondCheck: a repetition is
// dropped, wherever it came from. A forge asked to require the same context
// twice is being asked something nobody meant.
func TestNamingTheGatesOwnContextAgainIsNotASecondCheck(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Policy.StatusContext = "flow/policy"
	cfg.Forge.RequiredChecks = []string{"flow/policy", "test"}

	want := []string{"flow/policy", "test"}
	if got := requiredContexts(t, cfg); !slices.Equal(got, want) {
		t.Errorf("the trunk requires %q, want %q", got, want)
	}
}

// TestARepositoryThatRequiresNothingIsAskedForNothing: with no gate and no
// named check there is no item at all, and nothing for currency to be a
// property of.
func TestARepositoryThatRequiresNothingIsAskedForNothing(t *testing.T) {
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Policy.StatusContext = ""
	cfg.Forge.RequiredChecks = nil

	if got := requiredContexts(t, cfg); len(got) != 0 {
		t.Errorf("the trunk requires %q, want nothing", got)
	}
	if got := protectionItem(t, cfg).Field(engine.FieldStrictChecks); got != "false" {
		t.Errorf("%s = %q with nothing required, want flow to ask for nothing",
			engine.FieldStrictChecks, got)
	}
}

// TestTheCheckTheTeamNamedReachesTheForge: the contexts are not only in the
// plan. Required checks and protection are two desired items and one setting
// at the forge, so a context that stopped at the plan would be a promise the
// provider never heard.
func TestTheCheckTheTeamNamedReachesTheForge(t *testing.T) {
	w := githubProjectsWorld(t)
	cfg := preset(t, config.PresetLeanKanban)
	cfg.Policy.StatusContext = "flow/policy"
	cfg.Forge.RequiredChecks = []string{"test"}
	state := desired(t, cfg)

	applied, _ := applyTo(t, w, engine.ApplyInput{
		Plan:    planWith(t, w, cfg),
		Desired: state,
	})
	if checks := item(t, applied, core.ItemForgeRequiredStatus); !checks.Done() {
		t.Fatalf("the required checks were not applied: %v", checks.Err)
	}

	protection, err := w.forge.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("read back the protection: %v", err)
	}
	for _, context := range []string{"flow/policy", "test"} {
		if !slices.Contains(protection.RequiredChecks, context) {
			t.Errorf("the trunk requires %q, want %q among them", protection.RequiredChecks, context)
		}
	}
}
