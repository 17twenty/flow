//go:build flowtest

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// `flow check deploy` run whole: kong, the configuration `flow init` wrote,
// the fake providers, the engine and the rendering. What each test asserts is
// what somebody watching the board would see after a release workflow ran.

// deployedSHA is the commit the environment is running in these tests.
const deployedSHA = "c0ffee0000000000000000000000000000000c0f"

// deployRepo is a repository configured to finish work on deployment, holding
// one merged pull request, the ticket it closes, and a successful deployment
// of its merge.
func deployRepo(t *testing.T, seed func(*fakeWorld)) (string, func() *fakeWorld) {
	t.Helper()
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--preset", "deploy-done", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	t.Setenv(fakeEnv, "1")

	var world *fakeWorld
	seedFakeWorld(dir, func(w *fakeWorld) {
		world = w
		w.forge.SetPullRequest(core.PullRequest{
			Number:        7,
			Title:         "feat(api): ship it",
			Author:        "nick",
			State:         core.PRStateMerged,
			MergeSHA:      deployedSHA,
			BaseRef:       "main",
			ClosingIssues: []string{"#42"},
			UpdatedAt:     time.Now(),
			MergedAt:      time.Now(),
		})
		w.tracker.SetTicket(core.Ticket{
			Key:      "#42",
			Title:    "ship it",
			Body:     "## Acceptance criteria\n\n- it is running\n",
			Parent:   "#1",
			Status:   core.Status{ID: "In review", Name: "In review"},
			ColumnID: core.ColumnID("In review"),
		})
		w.deploys.Record(core.Deployment{
			ID:          "9",
			Environment: "production",
			SHA:         deployedSHA,
			State:       core.DeploySuccess,
			UpdatedAt:   time.Now(),
		})
		if seed != nil {
			seed(w)
		}
	})
	return dir, func() *fakeWorld {
		t.Helper()
		if world == nil {
			t.Fatal("no command has built this repository's providers yet")
		}
		return world
	}
}

// deployDocument is the shape of a `flow check deploy --json` document.
type deployDocument struct {
	Schema      string `json:"schema"`
	SHA         string `json:"sha"`
	Environment string `json:"environment"`
	Watched     bool   `json:"watched"`
	Confirmed   bool   `json:"confirmed"`
	Acted       bool   `json:"acted"`
	Why         string `json:"why"`
	DryRun      bool   `json:"dry_run"`
	PRs         []struct {
		Number int `json:"pr"`
	} `json:"prs"`
	Moves []struct {
		Ticket string `json:"ticket"`
		From   string `json:"from"`
		To     string `json:"to"`
		Event  string `json:"event"`
	} `json:"moves"`
}

// deployJSON runs `flow check deploy --json` and parses the one document it
// printed.
func deployJSON(t *testing.T, dir string, args ...string) (deployDocument, int) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "check", "deploy"}, args...)...)
	var doc deployDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("check deploy --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return doc, code
}

// columnOf is where the board has a ticket now.
func columnOf(t *testing.T, world *fakeWorld, key string) string {
	t.Helper()
	ticket, err := world.tracker.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("read ticket %s: %v", key, err)
	}
	return ticket.Status.Name
}

// TestCheckDeployFinishesTheWork is the command doing its one job: the ticket
// whose merge is running reaches the done column, and the run says so.
func TestCheckDeployFinishesTheWork(t *testing.T) {
	dir, world := deployRepo(t, nil)

	doc, code := deployJSON(t, dir, "--sha", deployedSHA)
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if !doc.Watched || !doc.Confirmed || !doc.Acted {
		t.Fatalf("doc = %+v, want a run that confirmed the record and acted", doc)
	}
	if doc.Environment != "production" || doc.SHA != deployedSHA {
		t.Fatalf("doc = %+v, want it to name the deployment", doc)
	}
	if len(doc.PRs) != 1 || doc.PRs[0].Number != 7 {
		t.Fatalf("prs = %+v, want the merge production is running", doc.PRs)
	}
	if len(doc.Moves) != 1 || doc.Moves[0].To != "done" || doc.Moves[0].Event != "deployed" {
		t.Fatalf("moves = %+v, want #42 moved to done on deployed", doc.Moves)
	}
	if got := columnOf(t, world(), "#42"); got != "Done" {
		t.Fatalf("#42 is in %q, want Done", got)
	}
}

// TestCheckDeployIsIdempotent is spec 04 §5 rule 5 through the command: a
// release workflow that reruns, or two deployments of the same commit, must
// not keep writing to the board.
func TestCheckDeployIsIdempotent(t *testing.T) {
	dir, world := deployRepo(t, nil)

	if _, code := deployJSON(t, dir, "--sha", deployedSHA); code != core.ExitOK {
		t.Fatalf("the first check deploy exited %d", code)
	}
	moves := world().tracker.Calls("Move")

	doc, code := deployJSON(t, dir, "--sha", deployedSHA)
	if code != core.ExitOK {
		t.Fatalf("the second check deploy exited %d", code)
	}
	if len(doc.Moves) != 1 || doc.Moves[0].To != "" {
		t.Fatalf("moves = %+v, want the second run to decide on no move", doc.Moves)
	}
	if again := world().tracker.Calls("Move"); again != moves {
		t.Fatalf("Move called %d times, want the %d of the first run", again, moves)
	}
	if got := columnOf(t, world(), "#42"); got != "Done" {
		t.Fatalf("#42 is in %q, want it left in Done", got)
	}
}

// TestCheckDeployDryRunMovesNothing reports the decision and leaves the board
// exactly as it was.
func TestCheckDeployDryRunMovesNothing(t *testing.T) {
	dir, world := deployRepo(t, nil)

	doc, code := deployJSON(t, dir, "--sha", deployedSHA, "--dry-run")
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if !doc.DryRun || len(doc.Moves) != 1 || doc.Moves[0].To != "done" {
		t.Fatalf("doc = %+v, want the move it would have made", doc)
	}
	if n := world().tracker.Calls("Move"); n != 0 {
		t.Fatalf("Move called %d times, want the board untouched", n)
	}
	if got := columnOf(t, world(), "#42"); got != "In review" {
		t.Fatalf("#42 is in %q, want it where it was", got)
	}
}

// TestCheckDeployTrustsTheRecord is spec 13 §1 through the command: `--sha`
// is a claim with nothing behind it, and a commit no deployment record
// mentions finishes nothing.
func TestCheckDeployTrustsTheRecord(t *testing.T) {
	dir, world := deployRepo(t, nil)

	doc, code := deployJSON(t, dir, "--sha", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if doc.Confirmed || doc.Acted {
		t.Fatalf("doc = %+v, want an unconfirmed deployment to do nothing", doc)
	}
	if got := columnOf(t, world(), "#42"); got != "In review" {
		t.Fatalf("#42 is in %q, want it left alone", got)
	}
}

// TestCheckDeployReadsTheEventPayload is how it runs in CI: GitHub writes the
// `deployment_status` body to a file and names it in the environment, and the
// commit and environment come from there rather than from flags.
func TestCheckDeployReadsTheEventPayload(t *testing.T) {
	dir, world := deployRepo(t, nil)
	t.Setenv("GITHUB_EVENT_PATH", writeEvent(t, map[string]any{
		"deployment":        map[string]any{"sha": deployedSHA, "environment": "production"},
		"deployment_status": map[string]any{"environment": "production", "state": "success"},
	}))

	doc, code := deployJSON(t, dir)
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if doc.SHA != deployedSHA || doc.Environment != "production" || !doc.Acted {
		t.Fatalf("doc = %+v, want the deployment the payload named", doc)
	}
	if got := columnOf(t, world(), "#42"); got != "Done" {
		t.Fatalf("#42 is in %q, want Done", got)
	}
}

// TestCheckDeployIgnoresAnotherEnvironmentsPayload keeps a review app out of
// the board even when the event arrives on the same workflow.
func TestCheckDeployIgnoresAnotherEnvironmentsPayload(t *testing.T) {
	dir, world := deployRepo(t, nil)
	t.Setenv("GITHUB_EVENT_PATH", writeEvent(t, map[string]any{
		"deployment":        map[string]any{"sha": deployedSHA, "environment": "staging"},
		"deployment_status": map[string]any{"environment": "staging", "state": "success"},
	}))

	doc, code := deployJSON(t, dir)
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if doc.Acted || doc.Environment != "staging" {
		t.Fatalf("doc = %+v, want staging left alone", doc)
	}
	if got := columnOf(t, world(), "#42"); got != "In review" {
		t.Fatalf("#42 is in %q, want it left alone", got)
	}
}

// TestCheckDeployPrefersTheFlagsToThePayload is somebody re-running the job
// by hand. The payload is whatever the last event happened to be, and a flag
// was typed on purpose, so the flag is the one believed — otherwise a stale
// event in the environment would quietly decide what a person had just said.
func TestCheckDeployPrefersTheFlagsToThePayload(t *testing.T) {
	dir, world := deployRepo(t, nil)
	t.Setenv("GITHUB_EVENT_PATH", writeEvent(t, map[string]any{
		"deployment":        map[string]any{"sha": "0000000000000000000000000000000000000000", "environment": "staging"},
		"deployment_status": map[string]any{"environment": "staging", "state": "success"},
	}))

	doc, code := deployJSON(t, dir, "--sha", deployedSHA, "--environment", "production")
	if code != core.ExitOK {
		t.Fatalf("check deploy exited %d, want %d", code, core.ExitOK)
	}
	if doc.SHA != deployedSHA || doc.Environment != "production" || !doc.Acted {
		t.Fatalf("doc = %+v, want the deployment the flags named", doc)
	}
	if got := columnOf(t, world(), "#42"); got != "Done" {
		t.Fatalf("#42 is in %q, want Done", got)
	}
}

// TestCheckDeployNeedsToKnowWhatWasDeployed refuses to guess, and says how to
// tell it (spec 02 §4: needs input is exit 2).
func TestCheckDeployNeedsToKnowWhatWasDeployed(t *testing.T) {
	dir, _ := deployRepo(t, nil)
	t.Setenv("GITHUB_EVENT_PATH", "")

	code, stdout, stderr := execAt(t, dir, "check", "deploy")
	if want := core.ExitCode(core.ErrNeedsInput); code != want {
		t.Fatalf("check deploy exited %d, want %d (stderr: %s)", code, want, stderr)
	}
	if stdout != "" {
		t.Fatalf("a failing command wrote to stdout: %q", stdout)
	}
	if !containsAll(stderr, "error:", "fix:", "--sha") {
		t.Fatalf("stderr = %q, want the what/why/fix shape naming --sha", stderr)
	}
}

// containsAll reports whether a string holds every one of the fragments.
func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

// writeEvent stores a CI event payload and returns its path.
func writeEvent(t *testing.T, event map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode the event: %v", err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write the event: %v", err)
	}
	return path
}
