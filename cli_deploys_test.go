package main

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// deploysFor builds the live adapters for a configuration that differs from
// the shipped preset only in its deploys block and its forge URL, and returns
// the deployment source it chose.
func deploysFor(t *testing.T, block config.Deploys, forgeURL ...string) (core.DeploySource, error) {
	t.Helper()
	t.Setenv("FLOW_TEST_FORGE_TOKEN", "test-token")

	declarations, err := assets.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	registry, err := core.LoadRegistry(declarations)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	auth := config.Auth{TokenEnv: "FLOW_TEST_FORGE_TOKEN"}
	s := &session{
		ConfigPath: ".flow.yaml",
		Registry:   registry,
		Config: &config.Config{
			Forge:   config.Forge{Type: "github", Repo: "acme/widget", URL: cmp.Or(forgeURL...), Auth: auth},
			Tracker: config.Tracker{Type: "github-projects", Owner: "acme", OwnerType: config.OwnerOrg, Auth: auth},
			Deploys: block,
		},
	}
	adapters, err := liveAdapters(&Context{Ctx: context.Background()}, s)
	if err != nil {
		return nil, err
	}
	return adapters.Deploys, nil
}

// TestTheConfiguredSourceIsTheOneThatGetsBuilt: `deploys.source` is the only
// thing that decides where flow looks for a deployment, and a build that
// ignored it would read an empty deployments API on a forge whose deployer
// reports with commit statuses — and report that nothing had shipped.
func TestTheConfiguredSourceIsTheOneThatGetsBuilt(t *testing.T) {
	cases := []struct {
		name  string
		block config.Deploys
		want  string
	}{
		{"forge deployments", config.Deploys{Source: config.DeploySourceForge, Environment: "production"}, core.SourceForgeDeployments},
		{"commit statuses", config.Deploys{
			Source:      config.DeploySourceCommitStatus,
			Environment: "production",
			Contexts:    []string{"argocd/app-prod"},
		}, core.SourceCommitStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := deploysFor(t, tc.block)
			if err != nil {
				t.Fatalf("liveAdapters: %v", err)
			}
			if source == nil {
				t.Fatal("no deployment source was built")
			}
			if source.Kind() != tc.want {
				t.Errorf("Kind() = %q, want %q", source.Kind(), tc.want)
			}
		})
	}
}

// TestNoDeploySourceIsNoAdapter: `none` means flow reads no deployments at
// all, so there is nothing to build and nothing to ask.
func TestNoDeploySourceIsNoAdapter(t *testing.T) {
	source, err := deploysFor(t, config.Deploys{Source: config.DeploySourceNone})
	if err != nil {
		t.Fatalf("liveAdapters: %v", err)
	}
	if source != nil {
		t.Errorf("a %s source was built: %q", config.DeploySourceNone, source.Kind())
	}
}

// TestACommitStatusSourceIsBuiltForTheConfiguredEnvironment: the contexts
// stand for an environment, and the source has to be told which, because a
// commit status does not carry one.
func TestACommitStatusSourceIsBuiltForTheConfiguredEnvironment(t *testing.T) {
	_, err := deploysFor(t, config.Deploys{
		Source:   config.DeploySourceCommitStatus,
		Contexts: []string{"argocd/app-prod"},
	})
	if err == nil {
		t.Fatal("a commit status source was built with no environment to stand for")
	}
	if _, ok := err.(*core.UserError); !ok {
		t.Errorf("err = %#v, want a core.UserError the person can act on", err)
	}
}

// TestTheConfiguredContextsAreTheOnesRead: `deploys.contexts` is what tells a
// commit status source which status means deployed, and a build that dropped
// them would read a repository full of green checks and find no deployment in
// it. The source is built the way a run builds it and then asked.
func TestTheConfiguredContextsAreTheOnesRead(t *testing.T) {
	const sha = "c0ffee00"
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widget/commits/"+sha+"/statuses" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 2, "context": "ci/test", "state": "success", "created_at": at.Format(time.RFC3339)},
			{"id": 1, "context": "argocd/app-prod", "state": "success", "created_at": at.Format(time.RFC3339)},
		})
	}))
	t.Cleanup(srv.Close)

	source, err := deploysFor(t, config.Deploys{
		Source:      config.DeploySourceCommitStatus,
		Environment: "production",
		Contexts:    []string{"argocd/app-prod"},
	}, srv.URL+"/api/v3")
	if err != nil {
		t.Fatalf("liveAdapters: %v", err)
	}

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: sha})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want only the one the configured context named: %+v", len(records), records)
	}
	if records[0].SHA != sha || !records[0].Succeeded() {
		t.Errorf("record = %+v, want the successful argocd/app-prod status on %s", records[0], sha)
	}
}
