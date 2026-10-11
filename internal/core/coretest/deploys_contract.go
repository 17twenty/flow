package coretest

import (
	"slices"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// NewDeploys builds the deployment source under test from a fixture.
type NewDeploys func(t *testing.T, fx DeployFixture) core.DeploySource

// DeployContract is every behaviour the doctor's deployment checks are
// entitled to assume of a source: records for one environment, newest first,
// with their state intact (spec 13 §3).
func DeployContract(t *testing.T, newDeploys NewDeploys) {
	t.Helper()

	t.Run("identity", func(t *testing.T) {
		source := newDeploys(t, ContractDeploys())
		switch source.Kind() {
		case core.SourceForgeDeployments, core.SourceCommitStatus:
		default:
			t.Errorf("Kind() = %q, want one of the two sources spec 13 §1 defines", source.Kind())
		}
		capabilities := source.Capabilities()
		if capabilities.Kind != core.KindDeploys {
			t.Errorf("Capabilities().Kind = %q, want %q", capabilities.Kind, core.KindDeploys)
		}
		for _, name := range core.CapabilityNames(core.KindDeploys) {
			if _, ok := capabilities.Capabilities[name]; !ok {
				t.Errorf("the declaration says nothing about %q; silence is not a finding", name)
			}
		}
	})

	t.Run("one environment's records, newest first", func(t *testing.T) {
		source := newDeploys(t, ContractDeploys())
		records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
		if err != nil {
			t.Fatalf("Deployments: %v", err)
		}
		if len(records) != 3 {
			t.Fatalf("got %d production records, want 3 (the staging one is not production)", len(records))
		}
		for _, record := range records {
			if record.Environment != "production" {
				t.Errorf("a query for production returned a %s record", record.Environment)
			}
			if record.SHA == "" {
				t.Errorf("record %s names no commit, so nothing can be proved about it", record.ID)
			}
		}
		if !slices.IsSortedFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) }) {
			t.Error("records came back oldest first; the newest successful record is what deployed means")
		}
		if !records[0].Succeeded() || records[0].SHA != SHADeployed {
			t.Errorf("the newest record is %+v, want the successful deployment of %s", records[0], SHADeployed)
		}
	})

	t.Run("a failed deployment is reported, not hidden", func(t *testing.T) {
		source := newDeploys(t, ContractDeploys())
		records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
		if err != nil {
			t.Fatalf("Deployments: %v", err)
		}
		if !slices.ContainsFunc(records, func(d core.Deployment) bool { return !d.Succeeded() }) {
			t.Error("the failed deployment was dropped; doctor decides what a failure means, not the adapter")
		}
	})

	t.Run("a window and a commit narrow the query", func(t *testing.T) {
		source := newDeploys(t, ContractDeploys())
		recent, err := source.Deployments(t.Context(), core.DeployQuery{
			Environment: "production",
			Since:       ContractNow.Add(-24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("Deployments since: %v", err)
		}
		if len(recent) != 1 {
			t.Fatalf("got %d records in the last day, want 1", len(recent))
		}

		byCommit, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: SHAMerged})
		if err != nil {
			t.Fatalf("Deployments for a commit: %v", err)
		}
		for _, record := range byCommit {
			if record.SHA != SHAMerged {
				t.Errorf("a query for %s returned %s", SHAMerged, record.SHA)
			}
		}

		one, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
		if err != nil {
			t.Fatalf("Deployments with a limit: %v", err)
		}
		if len(one) != 1 {
			t.Errorf("a limit of 1 returned %d records", len(one))
		}
	})

	t.Run("an environment nobody deploys to", func(t *testing.T) {
		source := newDeploys(t, ContractDeploys())
		records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "nowhere"})
		if err != nil {
			t.Fatalf("Deployments for an unused environment: %v", err)
		}
		if len(records) != 0 {
			t.Errorf("got %d records for an environment nothing deploys to", len(records))
		}
	})
}
