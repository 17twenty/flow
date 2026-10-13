//go:build flowtest

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// fakeRepo is a configured repository whose providers are the in-memory
// fakes: a GitHub origin, `flow init` run over it, and the switch of
// fakes_flowtest.go set. It is scenario A03's starting point — a repository
// somebody has just configured and nobody has applied.
//
// The board's number is given, because A03 starts from a project flow can
// read. A repository that does not know its board yet is a different
// scenario — A05 — and the fakes model it faithfully enough that a plan
// cannot be made there at all, so the tests about it call `init` themselves.
func fakeRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--project", "2", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	t.Setenv(fakeEnv, "1")
	return dir
}

// planDoc is the shape of a `flow plan --json` document that a test reads.
// Only the fields a scenario asserts on are named: the schema's own contract
// is held by the engine's golden files, and repeating it here would make this
// test fail for a rendering change it is not about.
type planDoc struct {
	Schema string     `json:"schema"`
	Items  []planItem `json:"items"`
}

// planItem is one entry of that document.
type planItem struct {
	ID      string   `json:"id"`
	Target  string   `json:"target"`
	Outcome string   `json:"outcome"`
	Hazards []string `json:"hazards,omitempty"`
	Changes []struct {
		Op   string `json:"op"`
		Item struct {
			Name string `json:"name"`
		} `json:"item"`
	} `json:"changes,omitempty"`
}

// planJSON runs `flow plan --json` and parses the one document it printed.
func planJSON(t *testing.T, dir string, args ...string) (planDoc, int) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "plan"}, args...)...)
	var doc planDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("plan --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return doc, code
}

// execAt runs the CLI against one repository.
func execAt(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return exec(t, append([]string{"-C", dir}, args...)...)
}

// item finds a planned item by id.
func (d planDoc) item(id string) (planItem, bool) {
	for _, it := range d.Items {
		if it.ID == id {
			return it, true
		}
	}
	return planItem{}, false
}

// TestScenario_A03_PlanOnFreshProject is scenario A03: a board holding the three statuses
// GitHub creates a project with becomes a plan that says, for every part of
// the workflow, whether the provider can be made to do it — and exits 2
// because work is pending.
func TestScenario_A03_PlanOnFreshProject(t *testing.T) {
	dir := fakeRepo(t)

	doc, code := planJSON(t, dir)
	if code != core.ExitActionRequired {
		t.Fatalf("plan exited %d, want %d: a fresh repository has changes pending", code, core.ExitActionRequired)
	}
	if doc.Schema != "flow.plan/v1" {
		t.Errorf("schema is %q, want flow.plan/v1 (spec 02 §3)", doc.Schema)
	}

	// Every outcome spec 06 §2 can reach is reached by this one repository,
	// which is what makes it the scenario worth holding: a plan that only
	// ever said "native" would pass a weaker test for ever.
	want := map[string]string{
		"tracker.status_options":           "native",
		"tracker.labels":                   "native",
		"forge.protection":                 "native",
		"forge.merge_settings":             "native",
		"tracker.native.pr_merged":         "attest",
		"tracker.native.changes_requested": "attest",
		"tracker.native.issue_closed":      "attest",
		"tracker.wip":                      "compensated",
		"deploys.records":                  "follow_up",
		"tracker.project":                  "noop",
	}
	for id, outcome := range want {
		it, ok := doc.item(id)
		if !ok {
			t.Errorf("the plan has no %s item", id)
			continue
		}
		if it.Outcome != outcome {
			t.Errorf("%s is planned as %q, want %q", id, it.Outcome, outcome)
		}
	}

	// The status options are the dangerous change, and the scenario asks for
	// the hazard to travel with it: a reader who is told flow can do this
	// natively must be told in the same breath what it costs.
	options, ok := doc.item("tracker.status_options")
	if !ok {
		t.Fatal("the plan has no tracker.status_options item")
	}
	if len(options.Hazards) == 0 {
		t.Error("the status option change carries no hazard note (scenario A03)")
	}
	var created []string
	for _, change := range options.Changes {
		if change.Op == "create" {
			created = append(created, change.Item.Name)
		}
	}
	for _, status := range []string{"Backlog", "Ready", "In progress", "In review"} {
		if !slicesContains(created, status) {
			t.Errorf("the plan does not create the %q status; it creates %v", status, created)
		}
	}
}

// slicesContains is used rather than slices.Contains so this file holds no
// import the assertions do not need.
func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A filter that names nothing is a typo, and must be refused rather than
// reported as a converged workflow (spec 02 §2).
func TestPlanRefusesAFilterThatNamesNothing(t *testing.T) {
	dir := fakeRepo(t)

	for _, tc := range []struct{ name, flag, value string }{
		{"unknown capability", "--only", "nosuchthing"},
		{"unknown target", "--target", "database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := execAt(t, dir, "plan", tc.flag, tc.value)
			if code != core.ExitCode(core.ErrValidation) {
				t.Errorf("exited %d, want %d (stderr: %s)", code, core.ExitCode(core.ErrValidation), stderr)
			}
			if stdout != "" {
				t.Errorf("a refused plan printed a plan:\n%s", stdout)
			}
			if !strings.Contains(stderr, "fix:") {
				t.Errorf("the refusal does not say what to do instead:\n%s", stderr)
			}
		})
	}
}

// --only narrows the plan to the capability that was asked for, and nothing
// else. It names a capability rather than a plan item: the question a person
// has is "what about labels", and which target answers it is flow's problem.
func TestPlanOnlyNarrowsTheWork(t *testing.T) {
	dir := fakeRepo(t)

	doc, code := planJSON(t, dir, "--only", "labels")
	if code != core.ExitActionRequired {
		t.Fatalf("plan exited %d, want %d", code, core.ExitActionRequired)
	}
	if len(doc.Items) != 1 || doc.Items[0].ID != "tracker.labels" {
		var names []string
		for _, it := range doc.Items {
			names = append(names, it.ID)
		}
		t.Errorf("--only labels planned %v, want just tracker.labels", names)
	}
}

// --target narrows to one provider, which is the flag a person reaches for
// when the other one's credential is the thing that is broken.
func TestPlanTargetNarrowsToOneProvider(t *testing.T) {
	dir := fakeRepo(t)

	doc, _ := planJSON(t, dir, "--target", "forge")
	if len(doc.Items) == 0 {
		t.Fatal("--target forge planned nothing")
	}
	for _, it := range doc.Items {
		if it.Target != "forge" {
			t.Errorf("--target forge planned the %s item %s", it.Target, it.ID)
		}
	}
}

// --tracker-override answers from the two declarations alone, with no
// provider built and no credential resolved (spec 05 §6). The absence of
// FLOW_TEST_FAKES is the assertion: a comparison that reached for a provider
// would need a token this run does not have.
func TestPlanComparesTrackersOffline(t *testing.T) {
	dir := fakeRepo(t)
	t.Setenv(fakeEnv, "")

	code, stdout, stderr := execAt(t, dir, "plan", "--tracker-override", "jira-cloud")
	if code != core.ExitOK && code != core.ExitActionRequired {
		t.Fatalf("comparison exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "jira-cloud") {
		t.Errorf("the comparison does not name the tracker it compared against:\n%s", stdout)
	}
}

// A tracker flow ships no declaration for is refused by name, not by a
// nil-pointer panic somewhere downstream.
func TestPlanRefusesAnUnknownTrackerOverride(t *testing.T) {
	dir := fakeRepo(t)

	code, _, stderr := execAt(t, dir, "plan", "--tracker-override", "trello")
	if code != core.ExitCode(core.ErrValidation) {
		t.Fatalf("exited %d, want %d (stderr: %s)", code, core.ExitCode(core.ErrValidation), stderr)
	}
	if !strings.Contains(stderr, "trello") {
		t.Errorf("the refusal does not name what was asked for:\n%s", stderr)
	}
}

// --out writes the plan where a human can read it and `flow apply --plan`
// can read it back.
func TestPlanWritesTheFileApplyReadsBack(t *testing.T) {
	dir := fakeRepo(t)
	path := filepath.Join(t.TempDir(), "plan.json")

	if _, code := planOut(t, dir, path); code != core.ExitActionRequired {
		t.Fatalf("plan --out exited %d, want %d", code, core.ExitActionRequired)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("plan --out wrote no file: %v", err)
	}
	var doc planDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the saved plan is not JSON: %v\n%s", err, data)
	}
	if doc.Schema != "flow.plan/v1" {
		t.Errorf("the saved plan has schema %q, want flow.plan/v1", doc.Schema)
	}
}

// planOut runs `flow plan --out path`.
func planOut(t *testing.T, dir, path string) (string, int) {
	t.Helper()
	code, stdout, _ := execAt(t, dir, "plan", "--out", path)
	return stdout, code
}
