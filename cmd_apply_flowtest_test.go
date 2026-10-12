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

// appliedDoc is the shape of a `flow apply --json` document a test reads.
type appliedDoc struct {
	Schema      string        `json:"schema"`
	Items       []appliedItem `json:"items"`
	Outstanding int           `json:"outstanding"`
}

// appliedItem is one entry of that document.
type appliedItem struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	Changes []struct {
		Op      string `json:"op"`
		Item    string `json:"item"`
		Verb    string `json:"verb"`
		Note    string `json:"note,omitempty"`
		Refused string `json:"refused,omitempty"`
		Error   string `json:"error,omitempty"`
	} `json:"changes,omitempty"`
}

// applyJSON runs `flow apply --json` and parses the one document it printed.
func applyJSON(t *testing.T, dir string, args ...string) (appliedDoc, int) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "apply"}, args...)...)
	var doc appliedDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("apply --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return doc, code
}

// mutations is every change this apply actually made through a provider.
func (d appliedDoc) mutations() []string {
	var made []string
	for _, item := range d.Items {
		for _, change := range item.Changes {
			if change.Refused == "" && change.Error == "" {
				made = append(made, item.ID+" "+change.Op+" "+change.Item)
			}
		}
	}
	return made
}

// TestApplyConvergesAndStaysThere is scenario A04. The first apply makes the
// changes it planned; the second, against the world the first one left,
// plans nothing to make and makes nothing.
//
// The fakes are a process-wide world keyed by working tree, so the second
// run here genuinely sees what the first one wrote. That is the whole point:
// an apply rebuilt from a fixture would converge for ever and prove nothing.
func TestApplyConvergesAndStaysThere(t *testing.T) {
	dir := fakeRepo(t)

	first, code := applyJSON(t, dir, "--auto-approve")
	if code != core.ExitActionRequired {
		t.Fatalf("the first apply exited %d, want %d: attestations are still pending", code, core.ExitActionRequired)
	}
	if first.Schema != "flow.apply/v1" {
		t.Errorf("schema is %q, want flow.apply/v1 (spec 02 §3)", first.Schema)
	}
	if len(first.mutations()) == 0 {
		t.Fatal("the first apply changed nothing")
	}
	for _, item := range first.Items {
		if item.Error != "" {
			t.Errorf("%s failed to apply: %s", item.ID, item.Error)
		}
	}

	// Having applied it, flow must now see it. A plan that still wants the
	// same work is the bug this scenario exists to catch: it means apply
	// wrote something observe cannot read back.
	plan, planCode := planJSON(t, dir)
	for _, it := range plan.Items {
		if it.Outcome == "native" {
			t.Errorf("%s is still planned as native after an apply; it should be noop", it.ID)
		}
	}
	if planCode != core.ExitActionRequired {
		t.Errorf("plan exited %d, want %d: the attestations are still outstanding", planCode, core.ExitActionRequired)
	}

	second, code := applyJSON(t, dir, "--auto-approve")
	if code != core.ExitActionRequired {
		t.Fatalf("the second apply exited %d, want %d", code, core.ExitActionRequired)
	}
	if made := second.mutations(); len(made) != 0 {
		t.Errorf("the second apply was not idempotent; it changed %v", made)
	}
}

// Non-interactively, an apply with changes to make must refuse rather than
// guess at consent (spec 02 §2, spec 06 §3). The test's streams are buffers,
// so this run is exactly the CI invocation the rule is written for.
func TestApplyRefusesWithoutApprovalWhenNobodyCanAnswer(t *testing.T) {
	dir := fakeRepo(t)

	code, stdout, stderr := execAt(t, dir, "apply")
	if code != core.ExitCode(core.ErrValidation) {
		t.Fatalf("exited %d, want %d (stderr: %s)", code, core.ExitCode(core.ErrValidation), stderr)
	}
	if strings.Contains(stdout, "✓") {
		t.Errorf("a refused apply reported work:\n%s", stdout)
	}
	if !strings.Contains(stderr, "--auto-approve") {
		t.Errorf("the refusal does not say how to proceed:\n%s", stderr)
	}

	// Nothing may have been written on the way to refusing.
	if second, _ := applyJSON(t, dir, "--auto-approve"); len(second.mutations()) == 0 {
		t.Error("the refused apply had already made the changes")
	}
}

// A saved plan may be applied, and is refused once the world has moved on
// (spec 02 §2, spec 06 §3).
func TestApplyUsesASavedPlan(t *testing.T) {
	dir := fakeRepo(t)
	path := filepath.Join(t.TempDir(), "plan.json")

	if _, code := planOut(t, dir, path); code != core.ExitActionRequired {
		t.Fatalf("plan --out exited %d", code)
	}
	doc, code := applyJSON(t, dir, "--plan", path, "--auto-approve")
	if code != core.ExitActionRequired {
		t.Fatalf("apply --plan exited %d, want %d", code, core.ExitActionRequired)
	}
	if len(doc.mutations()) == 0 {
		t.Error("apply --plan made no changes")
	}
}

// A plan that no longer describes what flow would decide now is refused: the
// approval it carries was given for a different world.
func TestApplyRefusesAStalePlan(t *testing.T) {
	dir := fakeRepo(t)
	path := filepath.Join(t.TempDir(), "plan.json")

	if _, code := planOut(t, dir, path); code != core.ExitActionRequired {
		t.Fatalf("plan --out exited %d", code)
	}
	// Editing the configuration is what moves the world: the plan is pinned
	// to the hash of the file it was decided from (spec 06 §3).
	configPath := filepath.Join(dir, ".flow.yaml")
	source, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	const wip = "wip: { limit: 3 }"
	if !strings.Contains(string(source), wip) {
		t.Fatalf("the value this test edits is no longer in the preset:\n%s", source)
	}
	edited := strings.Replace(string(source), wip, "wip: { limit: 4 }", 1)
	if err := os.WriteFile(configPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := execAt(t, dir, "apply", "--plan", path, "--auto-approve")
	if code == core.ExitOK {
		t.Fatalf("a stale plan was applied:\n%s", stdout)
	}
	if !strings.Contains(stderr, path) {
		t.Errorf("the refusal does not name the plan it refused:\n%s", stderr)
	}
}

// A plan file that is not there is a clear error, not a panic.
func TestApplyRefusesAMissingPlan(t *testing.T) {
	dir := fakeRepo(t)

	path := filepath.Join(t.TempDir(), "nosuchplan.json")
	code, _, stderr := execAt(t, dir, "apply", "--plan", path, "--auto-approve")
	if code != core.ExitCode(core.ErrNotFound) {
		t.Fatalf("exited %d, want %d (stderr: %s)", code, core.ExitCode(core.ErrNotFound), stderr)
	}
	if !strings.Contains(stderr, "flow plan --out") {
		t.Errorf("the error does not say how to make one:\n%s", stderr)
	}
}

// Applying writes the lock, which is how the next run knows what flow itself
// created and may therefore prune (spec 06 §4).
func TestApplyWritesTheLock(t *testing.T) {
	dir := fakeRepo(t)

	if _, code := applyJSON(t, dir, "--auto-approve"); code != core.ExitActionRequired {
		t.Fatalf("apply exited %d", code)
	}
	lock, err := os.ReadFile(filepath.Join(dir, ".flow.lock"))
	if err != nil {
		t.Fatalf("apply wrote no lock: %v", err)
	}
	for _, want := range []string{"Backlog", "In progress"} {
		if !strings.Contains(string(lock), want) {
			t.Errorf("the lock does not record the %q flow created:\n%s", want, lock)
		}
	}
}

// A file the forge's trunk does not hold is offered as a pull request, never
// written to the trunk itself (spec 06 §4). Running again updates that same
// proposal rather than opening a second one: the proposal's id is its
// identity, and an apply on a timer must not fill the repository with pull
// requests nobody asked for.
func TestApplyProposesMissingFilesOnce(t *testing.T) {
	dir := fakeRepo(t)
	// The fake forge's trunk is what the working tree holds, so removing the
	// workflow here is a repository whose CI never had it.
	if err := os.Remove(filepath.Join(dir, ".github", "workflows", "flow.yml")); err != nil {
		t.Fatal(err)
	}

	first, code := applyJSON(t, dir, "--auto-approve")
	if code != core.ExitActionRequired {
		t.Fatalf("apply exited %d, want %d", code, core.ExitActionRequired)
	}
	note := changeNote(t, first, "forge.files")
	if !strings.Contains(note, ".github/workflows/flow.yml") {
		t.Errorf("the proposal does not name the missing file: %q", note)
	}
	if !strings.Contains(note, "#1") {
		t.Errorf("the proposal does not say which pull request carries it: %q", note)
	}

	if _, code := applyJSON(t, dir, "--auto-approve"); code != core.ExitActionRequired {
		t.Fatalf("the second apply exited %d", code)
	}
	again := changeNote(t, mustApply(t, dir), "forge.files")
	if !strings.Contains(again, "#1") {
		t.Errorf("applying again opened another pull request instead of updating #1: %q", again)
	}
}

// mustApply applies and fails the test if the apply itself could not be read.
func mustApply(t *testing.T, dir string) appliedDoc {
	t.Helper()
	doc, _ := applyJSON(t, dir, "--auto-approve")
	return doc
}

// changeNote is the provider's own words about the first change to an item.
func changeNote(t *testing.T, doc appliedDoc, id string) string {
	t.Helper()
	for _, item := range doc.Items {
		if item.ID != id {
			continue
		}
		if len(item.Changes) == 0 {
			t.Fatalf("%s applied no changes", id)
		}
		return item.Changes[0].Note
	}
	t.Fatalf("the apply has no %s item", id)
	return ""
}
