//go:build flowtest

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// `flow attest` end to end (spec 02 §2, spec 07 §8): kong, configuration,
// plan, lock and exit code. The command's whole job is to be hard to misuse,
// so most of what is asserted here is what it refuses.

// attestDoc is the shape of a `flow attest --json` document a test reads.
type attestDoc struct {
	Schema    string `json:"schema"`
	Check     string `json:"check"`
	By        string `json:"by"`
	Note      string `json:"note"`
	ExpiresIn string `json:"expires_in"`
	Inputs    string `json:"inputs"`
}

// attestJSON runs the command and parses the one document it printed.
func attestJSON(t *testing.T, dir string, args ...string) (attestDoc, int, string) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "attest"}, args...)...)
	var doc attestDoc
	if stdout != "" {
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("attest --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}
	}
	return doc, code, stderr
}

// attestAs runs the command with a particular user configuration behind it,
// which is where the default for --by comes from.
func attestAs(t *testing.T, dir, userConfig string, args ...string) (attestDoc, int, string) {
	t.Helper()
	return attestJSON(t, dir, append([]string{"--user-config", userConfig}, args...)...)
}

// lockAttestations reads back what the lock remembers, which is the thing
// that matters: the next run reads the lock, not the printed page.
func lockAttestations(t *testing.T, dir string) []config.Attestation {
	t.Helper()
	lock, err := config.LoadLock(filepath.Join(dir, ".flow.lock"))
	if err != nil {
		t.Fatalf("the lock is not readable: %v", err)
	}
	return lock.Attestations
}

// A human's word about something no API answers is recorded, and the lock the
// next run reads is what holds it.
func TestAttestRecordsAHumanWord(t *testing.T) {
	dir := fakeRepo(t)
	boards(dir, core.Candidate{ID: "2", Name: "flow", URL: "https://github.test/users/acme/projects/2"})

	doc, code, stderr := attestJSON(t, dir, "tracker.native.pr_merged",
		"--by", "@17twenty", "--note", "watched a PR merge and the card move to Done")

	if code != core.ExitOK {
		t.Fatalf("attest exited %d, want %d: %s", code, core.ExitOK, stderr)
	}
	if doc.Schema != "flow.attest/v1" {
		t.Errorf("schema = %q, want flow.attest/v1 (spec 02 §3)", doc.Schema)
	}
	if doc.ExpiresIn != "30d" {
		t.Errorf("expires_in = %q, want 30d, the default of spec 02 §2", doc.ExpiresIn)
	}

	recorded := lockAttestations(t, dir)
	if len(recorded) != 1 {
		t.Fatalf("the lock holds %d attestations, want one: %+v", len(recorded), recorded)
	}
	if recorded[0].Check != "tracker.native.pr_merged" || recorded[0].By != "@17twenty" {
		t.Errorf("the lock records %+v, want the check and who vouched for it", recorded[0])
	}
	if recorded[0].Inputs == "" {
		t.Error("the lock does not record what the promise covered, so nothing will ever retire it")
	}

	// The promise must settle the check it was made about: a doctor run that
	// still asks for it means attest and doctor disagree about one board.
	report, doctorCode := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if got := report.checkState(t, "tracker.native.pr_merged"); got != "pass" {
		t.Errorf("doctor says %s is %q, want pass: the attestation settles nothing", "tracker.native.pr_merged", got)
	}
	if doctorCode != core.ExitOK && doctorCode != core.ExitActionRequired {
		t.Errorf("doctor exited %d", doctorCode)
	}

	// And what doctor says about a check nobody has vouched for yet must be
	// the command that would settle it, with the same expiry attest writes:
	// a remediation promising a different number is one nobody can check.
	waiting := report.check(t, core.ItemTrackerExpedite)
	if !strings.HasPrefix(waiting.Result.Remediation.Command, "flow attest "+core.ItemTrackerExpedite) {
		t.Errorf("doctor says to run %q, want it to start flow attest %s", waiting.Result.Remediation.Command, core.ItemTrackerExpedite)
	}
	if steps := strings.Join(waiting.Result.Remediation.Steps, " "); !strings.Contains(steps, "30d") {
		t.Errorf("doctor does not say how long the promise would stand: %q", steps)
	}
}

// The refusal that makes the mechanism worth anything: a check flow verifies
// through an API cannot be sworn away. Exit 64, and the lock is untouched.
func TestAttestRefusesWhatFlowCanCheckItself(t *testing.T) {
	dir := fakeRepo(t)

	_, code, stderr := attestJSON(t, dir, "forge.protection", "--by", "@17twenty", "--note", "it is fine, honestly")

	if code != core.ExitUsage {
		t.Fatalf("attest exited %d, want %d (spec 02 §2): %s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "flow apply") {
		t.Errorf("the refusal does not say what to do instead:\n%s", stderr)
	}
	if _, err := os.ReadFile(filepath.Join(dir, ".flow.lock")); err == nil {
		if recorded := lockAttestations(t, dir); len(recorded) != 0 {
			t.Errorf("a refused attestation was written down anyway: %+v", recorded)
		}
	}
}

// An id that is not in this workflow is a typo, and the fix is the ids that
// are.
func TestAttestOffersTheIdsThereAre(t *testing.T) {
	dir := fakeRepo(t)

	_, code, stderr := attestJSON(t, dir, "tracker.native.pr_merge", "--by", "@17twenty", "--note", "typo")

	if code != core.ExitUsage {
		t.Fatalf("attest exited %d, want %d: %s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "tracker.native.pr_merged") {
		t.Errorf("the refusal does not offer the ids that exist:\n%s", stderr)
	}
}

// Who vouched defaults to whoever this machine is configured as (spec 03 §4),
// and when nothing says, the run refuses rather than signing it anonymously.
func TestAttestTakesTheHandleFromUserConfig(t *testing.T) {
	dir := fakeRepo(t)
	user := filepath.Join(t.TempDir(), "flow.toml")
	if err := os.WriteFile(user, []byte("[user]\nhandle = \"@priya\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, code, stderr := attestAs(t, dir, user,
		"tracker.expedite", "--note", "the lane is empty and the team agreed to keep it so")
	if code != core.ExitOK {
		t.Fatalf("attest exited %d, want %d: %s", code, core.ExitOK, stderr)
	}
	if doc.By != "@priya" {
		t.Errorf("by = %q, want the handle from the user config", doc.By)
	}

	empty := filepath.Join(t.TempDir(), "flow.toml")
	if err := os.WriteFile(empty, []byte("[ui]\ntheme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code, stderr = attestAs(t, dir, empty,
		"tracker.native.issue_closed", "--note", "nobody is signing this")
	if code != core.ExitUsage {
		t.Errorf("attest exited %d with nobody to sign it, want %d: %s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--by") {
		t.Errorf("the refusal does not say how to sign it:\n%s", stderr)
	}
}

// `--expires` is a duration a person writes, and one that has already run out
// is refused: a promise that expires before it is read vouches for nothing.
func TestAttestRefusesAPromiseThatStandsForNoTime(t *testing.T) {
	dir := fakeRepo(t)

	doc, code, stderr := attestJSON(t, dir, "tracker.expedite",
		"--by", "@17twenty", "--note", "checked the lane", "--expires", "2w")
	if code != core.ExitOK {
		t.Fatalf("attest exited %d, want %d: %s", code, core.ExitOK, stderr)
	}
	if doc.ExpiresIn != "14d" {
		t.Errorf("expires_in = %q, want 14d", doc.ExpiresIn)
	}

	for _, expires := range []string{"soon", "0", "-1d"} {
		_, code, stderr := attestJSON(t, dir, "tracker.expedite",
			"--by", "@17twenty", "--note", "checked the lane", "--expires", expires)
		if code != core.ExitUsage {
			t.Errorf("--expires %q exited %d, want %d: %s", expires, code, core.ExitUsage, stderr)
		}
	}
}

// Scenario A04's second half (spec 19): an applied workflow whose outstanding
// attestations have been made has nothing left to report, and the run that
// says so exits 0.
//
// The environment has been deployed to, because scenario A04's exit 0 is the
// exit code of the whole run and `deploys.records` is the one item in this
// configuration nobody may switch on — a deployment is the deployer's event
// and flow only has to be able to see one (spec 13 §3). A repository where
// nothing has ever been deployed has a genuine follow-up outstanding, which
// is A06's business; this scenario is about what is left when there is none.
func TestScenario_A04_AnAppliedAndAttestedWorkflowIsDone(t *testing.T) {
	dir := fakeRepo(t)
	seedFakeWorld(dir, func(w *fakeWorld) {
		w.deploys.Record(core.Deployment{
			ID:          "1",
			Environment: "production",
			SHA:         strings.Repeat("a", 40),
			State:       core.DeploySuccess,
			CreatedAt:   time.Now(),
		})
	})

	if _, code := applyJSON(t, dir, "--auto-approve"); code != core.ExitActionRequired {
		t.Fatalf("the first apply exited %d, want %d: attestations are still pending", code, core.ExitActionRequired)
	}

	plan, _ := planJSON(t, dir)
	var waiting []string
	for _, item := range plan.Items {
		if item.Outcome == "attest" {
			waiting = append(waiting, item.ID)
		}
	}
	if len(waiting) == 0 {
		t.Fatal("nothing in this workflow waits on a human's word, so the scenario proves nothing")
	}
	for _, id := range waiting {
		if _, code, stderr := attestJSON(t, dir, id, "--by", "@17twenty", "--note", "checked the board by hand"); code != core.ExitOK {
			t.Fatalf("attesting %s exited %d: %s", id, code, stderr)
		}
	}

	after, code := planJSON(t, dir)
	if code != core.ExitOK {
		t.Errorf("plan exited %d, want %d: everything outstanding has been attested", code, core.ExitOK)
	}
	for _, item := range after.Items {
		if item.Outcome == "attest" {
			t.Errorf("%s still waits on a human's word after it was attested", item.ID)
		}
	}

	// The scenario's own words: the second apply has no native changes and
	// exits 0. A plan agreeing is not the same statement — apply is the
	// command somebody runs twice, and it is apply that must come back
	// saying there was nothing to do.
	second, code := applyJSON(t, dir, "--auto-approve")
	if code != core.ExitOK {
		t.Errorf("the second apply exited %d, want %d: everything it wanted is done and vouched for", code, core.ExitOK)
	}
	if made := second.mutations(); len(made) != 0 {
		t.Errorf("the second apply was not idempotent; it changed %v", made)
	}
}

// Vouching for one check must not quietly revive every other promise that had
// lapsed with the configuration.
//
// Attestations written before flow recorded what each one covered have no
// inputs hash, so the only thing that can retire them is the lock's own
// `config_hash` moving away from the file's (spec 03 §3). Stamping that hash
// here — which is what a run that saved the lock wholesale would do — would
// make one person's word about one check re-validate a stranger's word about
// everything else. Doctor stamps it, because doctor has looked at all of them.
func TestAttestingOneCheckDoesNotReviveTheRest(t *testing.T) {
	dir := fakeRepo(t)
	path := filepath.Join(dir, ".flow.lock")

	// A promise from before, about something else, made against a
	// configuration that is no longer this one.
	stale := config.NewLock("v0.0.1")
	stale.ConfigHash = "sha256:" + strings.Repeat("ab", 32)
	stale.Attestations = append(stale.Attestations, config.Attestation{
		Check:     core.ItemTrackerExpedite,
		By:        "@someone-who-left",
		Note:      "the Expedite view was on the board a year ago",
		At:        time.Now().Add(-365 * 24 * time.Hour),
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	})
	if err := config.WriteLock(path, stale); err != nil {
		t.Fatal(err)
	}
	before, _ := planJSON(t, dir)
	if item, ok := before.item(core.ItemTrackerExpedite); !ok || item.Outcome != "attest" {
		t.Fatalf("%s plans as %q before the run, want attest: the lapsed promise is not lapsed, so this proves nothing", core.ItemTrackerExpedite, item.Outcome)
	}

	if _, code, stderr := attestJSON(t, dir, "tracker.native.pr_merged",
		"--by", "@17twenty", "--note", "watched a PR merge and the card move to Done"); code != core.ExitOK {
		t.Fatalf("attest exited %d: %s", code, stderr)
	}

	after, err := config.LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.ConfigHash != stale.ConfigHash {
		t.Errorf("attest moved the lock's config_hash to %q; only a run that looked at every check may do that", after.ConfigHash)
	}
	again, _ := planJSON(t, dir)
	if item, ok := again.item(core.ItemTrackerExpedite); !ok || item.Outcome != "attest" {
		t.Errorf("%s plans as %q after vouching for a different check, want attest", core.ItemTrackerExpedite, item.Outcome)
	}
}
