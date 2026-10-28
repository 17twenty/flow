package engine_test

import (
	"errors"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// forgeOfKind is a fake forge answering with the shipped declaration of kind.
func forgeOfKind(t *testing.T, kind string) core.Forge {
	t.Helper()
	fixture := coretest.ContractForge()
	fixture.Kind = kind
	fixture.Capabilities = testRegistry(t)[kind]
	return coretest.NewFakeForge(fixture)
}

// TestDeploysUnreadableIsTheForgesDeclaration (issue #295): GitLab and Gitea
// declare no deployment records flow can read, GitHub does, and no forge at
// all is the auth check's business rather than a forge without records.
func TestDeploysUnreadableIsTheForgesDeclaration(t *testing.T) {
	for kind, want := range map[string]bool{"github": false, "gitlab": true, "gitea": true} {
		if got := engine.DeploysUnreadable(forgeOfKind(t, kind)); got != want {
			t.Errorf("DeploysUnreadable(%s) = %v, want %v", kind, got, want)
		}
	}
	if engine.DeploysUnreadable(nil) {
		t.Error("no forge at all was called one without deployment records")
	}
}

// TestObservingDeploymentsAForgeCannotKeepIsNotAnError: the deploys item of a
// forge with no records and no source is left unobserved, so it cannot take
// every other item's observation down with it (issue #295). With no forge to
// explain the missing source, a missing source is still refused.
func TestObservingDeploymentsAForgeCannotKeepIsNotAnError(t *testing.T) {
	desired := engine.Desired{Items: []engine.DesiredItem{{
		ID:         core.ItemDeploysRecords,
		Capability: core.CapDeployRecords,
		Target:     engine.TargetDeploys,
		Items:      []core.Item{{Name: "production"}},
	}}}

	observed, err := engine.Observe(t.Context(), desired, engine.Adapters{Forge: forgeOfKind(t, "gitlab")})
	if err != nil {
		t.Fatalf("Observe on gitlab with no source: %v", err)
	}
	if observed[core.ItemDeploysRecords].Exists {
		t.Error("a deployment record was reported that nothing read")
	}

	for name, forge := range map[string]core.Forge{"no forge": nil, "github": forgeOfKind(t, "github")} {
		if _, err := engine.Observe(t.Context(), desired, engine.Adapters{Forge: forge}); !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("%s with no source: err = %v, want %v", name, err, core.ErrUnsupported)
		}
	}
}
