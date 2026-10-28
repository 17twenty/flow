package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/tracker"
)

// TestTheJiraAdapterIsToldTheFollowUpLabels: the Jira follow-up search is
// narrowed by followups.labels (issue #296), and only the build can tell the
// adapter what they are. A build that dropped them would quietly read the
// whole project on every run, with every adapter test still green.
func TestTheJiraAdapterIsToldTheFollowUpLabels(t *testing.T) {
	t.Setenv("FLOW_TEST_JIRA_TOKEN", "test-token")
	t.Setenv("FLOW_TEST_JIRA_EMAIL", "dev@acme.test")

	var (
		mu   sync.Mutex
		jqls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/search/jql") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			JQL string `json:"jql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "not JSON", http.StatusBadRequest)
			return
		}
		mu.Lock()
		jqls = append(jqls, body.JQL)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	t.Cleanup(srv.Close)

	declarations, err := assets.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	registry, err := core.LoadRegistry(declarations)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	s := &session{
		ConfigPath: ".flow.yaml",
		Registry:   registry,
		Config: &config.Config{
			Forge: config.Forge{Type: "github", Repo: "acme/widget", Auth: config.Auth{TokenEnv: "FLOW_TEST_JIRA_TOKEN"}},
			Tracker: config.Tracker{
				Type: tracker.JiraCloudKind, URL: srv.URL, Project: config.Pending{Text: "FLOW"},
				Auth: config.Auth{TokenEnv: "FLOW_TEST_JIRA_TOKEN", EmailEnv: "FLOW_TEST_JIRA_EMAIL"},
			},
			FollowUps: config.FollowUps{Labels: []string{"flow-followup"}},
		},
		Settings: config.Settings{Auth: map[string]config.AuthSource{
			tracker.JiraCloudKind: {Hosts: []string{hostOf(srv.URL)}},
		}},
	}
	adapters, err := liveAdapters(hostContext(t, CLI{}), s)
	if err != nil {
		t.Fatalf("liveAdapters: %v", err)
	}
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
	if _, err := adapters.Tracker.FindFollowUp(t.Context(), marker); err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(jqls) == 0 || !strings.Contains(jqls[0], `labels = "flow-followup"`) {
		t.Errorf("the follow-up search was not narrowed by followups.labels: %q", jqls)
	}
}
