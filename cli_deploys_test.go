package main

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/forge"
	"github.com/17twenty/flow/internal/tracker"
)

// deploysFor builds the live adapters for a configuration that differs from
// the shipped preset only in its deploys block and its forge URL, and returns
// the deployment source it chose.
func deploysFor(t *testing.T, block config.Deploys, forgeURL ...string) (core.DeploySource, error) {
	t.Helper()
	return deploysOn(t, "github", block, forgeURL...)
}

// deploysOn is deploysFor on a forge of the kind named.
func deploysOn(t *testing.T, kind string, block config.Deploys, forgeURL ...string) (core.DeploySource, error) {
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
	url := cmp.Or(forgeURL...)
	s := &session{
		ConfigPath: ".flow.yaml",
		Registry:   registry,
		Config: &config.Config{
			Forge:   config.Forge{Type: kind, Repo: "acme/widget", URL: url, Auth: auth},
			Tracker: config.Tracker{Type: "github-projects", Owner: "acme", OwnerType: config.OwnerOrg, Auth: auth},
			Deploys: block,
		},
		Settings: approving(url),
	}
	if host := hostOf(url); host != "" {
		s.Settings.Auth[kind] = config.AuthSource{Hosts: []string{host}}
	}
	adapters, err := liveAdapters(hostContext(t, CLI{}), s)
	if err != nil {
		return nil, err
	}
	return adapters.Deploys, nil
}

// approving is the personal configuration of somebody who has agreed to send
// their credential to the forge this test stands up. A host flow does not
// ship is nobody's to approve but the person running it (spec 15 §1), and a
// test server is never that host, so without this every build below would
// stop at the question instead of reaching the thing it means to prove.
func approving(forgeURL string) config.Settings {
	host := hostOf(forgeURL)
	if host == "" {
		return config.Settings{}
	}
	source := config.AuthSource{Hosts: []string{host}}
	return config.Settings{Auth: map[string]config.AuthSource{
		forge.GitHubKind:           source,
		tracker.GitHubProjectsKind: source,
	}}
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

// trackerFor builds the live adapters for a configuration whose tracker is
// the one named, and returns the tracker it chose.
func trackerFor(t *testing.T, block config.Tracker) (core.Tracker, error) {
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
	settings := approving("")
	settings.Auth = map[string]config.AuthSource{
		forge.GitHubKind:           {Hosts: []string{"api.github.com"}},
		tracker.GitHubProjectsKind: {Hosts: []string{"api.github.com"}},
		tracker.JiraCloudKind:      {Hosts: []string{hostOf(block.URL)}},
	}
	block.Auth = config.Auth{TokenEnv: auth.TokenEnv, EmailEnv: block.Auth.EmailEnv}
	s := &session{
		ConfigPath: ".flow.yaml",
		Registry:   registry,
		Config: &config.Config{
			Forge:   config.Forge{Type: "github", Repo: "acme/widget", Auth: auth},
			Tracker: block,
			Deploys: config.Deploys{Source: config.DeploySourceNone},
		},
		Settings: settings,
	}
	adapters, err := liveAdapters(hostContext(t, CLI{}), s)
	if err != nil {
		return nil, err
	}
	return adapters.Tracker, nil
}

// TestTheConfiguredTrackerIsTheOneThatGetsBuilt: `tracker.type` chooses the
// adapter, and what the configuration names reaches the wire — the site it
// is sent to, the project key it asks about, and the credential paired with
// the email that Jira Cloud's Basic authentication is.
func TestTheConfiguredTrackerIsTheOneThatGetsBuilt(t *testing.T) {
	t.Setenv("JIRA_EMAIL", "dev@acme.test")
	var sent *http.Request
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessages":["nothing here"],"errors":{}}`))
	}))
	defer server.Close()

	built, err := trackerFor(t, config.Tracker{
		Type:    config.TrackerJiraCloud,
		URL:     server.URL,
		Project: config.Pending{Text: "FLOW"},
		Auth:    config.Auth{EmailEnv: "JIRA_EMAIL"},
	})
	if err != nil {
		t.Fatalf("liveAdapters: %v", err)
	}
	if built == nil {
		t.Fatal("no tracker was built")
	}
	if built.Kind() != tracker.JiraCloudKind {
		t.Fatalf("Kind() = %q, want %q", built.Kind(), tracker.JiraCloudKind)
	}

	if _, err := built.Tickets(t.Context(), core.TicketQuery{}); err == nil {
		t.Fatal("the server refused and the adapter did not say so")
	}
	if sent == nil {
		t.Fatal("nothing was asked of the site the configuration named")
	}
	if want := tracker.JiraAPIPath + "/search/jql"; sent.URL.Path != want {
		t.Errorf("the adapter asked %q, want %q", sent.URL.Path, want)
	}
	if !strings.Contains(string(body), "FLOW") {
		t.Errorf("the query is %q, want the project the configuration named", body)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("dev@acme.test:test-token"))
	if got := sent.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %q, want the email and the token encoded together", got)
	}
}

// TestAJiraBuildWantsTheEmailTheTokenBelongsTo: the pairing that makes Jira's
// Basic authentication happens while the adapters are being built, so a
// configuration naming an email variable nobody exported stops there — rather
// than at the first call, authenticated as half an account.
func TestAJiraBuildWantsTheEmailTheTokenBelongsTo(t *testing.T) {
	t.Setenv("JIRA_EMAIL", "")
	_, err := trackerFor(t, config.Tracker{
		Type:    config.TrackerJiraCloud,
		URL:     "https://acme.atlassian.net",
		Project: config.Pending{Text: "FLOW"},
		Auth:    config.Auth{EmailEnv: "JIRA_EMAIL"},
	})
	if err == nil {
		t.Fatal("the adapters were built without the email the token belongs to")
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "JIRA_EMAIL") {
		t.Errorf("the refusal does not name the variable to export: %+v", err)
	}
}

// TestNoSourceIsBuiltForRecordsTheForgeDoesNotKeep is issue #295. The only
// reader of forge deployments is GitHub's, and building it against GitLab or
// Gitea made every observation of it an error that failed every check doctor
// planned. No source is built for them; a commit status source still is,
// because every forge keeps commit statuses.
func TestNoSourceIsBuiltForRecordsTheForgeDoesNotKeep(t *testing.T) {
	for _, kind := range []string{"gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			url := "https://" + kind + ".example.test"
			source, err := deploysOn(t, kind, config.Deploys{Source: config.DeploySourceForge, Environment: "production"}, url)
			if err != nil {
				t.Fatalf("liveAdapters: %v", err)
			}
			if source != nil {
				t.Errorf("a %q source was built on %s, which keeps no deployment records", source.Kind(), kind)
			}

			source, err = deploysOn(t, kind, config.Deploys{
				Source:      config.DeploySourceCommitStatus,
				Environment: "production",
				Contexts:    []string{"deploy/production"},
			}, url)
			if err != nil {
				t.Fatalf("liveAdapters: %v", err)
			}
			if source == nil || source.Kind() != core.SourceCommitStatus {
				t.Errorf("source = %v on %s, want a commit status source", source, kind)
			}
		})
	}
}
