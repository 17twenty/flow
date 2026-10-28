//go:build flowtest

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// Acceptance scenarios A05, A06 and A07 (spec 19): the three things `flow
// doctor` does that no other command does — resolve what the configuration
// does not know, file and settle the work flow is not allowed to do itself,
// and notice that something which used to hold has stopped holding.
//
// They are written against the in-memory providers of fakes_flowtest.go and
// run the real command whole: kong, configuration, lock, plan, report and exit
// code. The lock is the thing to assert against rather than the printed
// report, because the lock is what the next run reads.

// reportDoc is the shape of a `flow doctor --json` document that a test reads.
// Only the fields a scenario asserts on are named.
type reportDoc struct {
	Window string     `json:"window"`
	Checks []checkDoc `json:"checks"`
}

// checkDoc is one entry of that report.
type checkDoc struct {
	ID        string `json:"id"`
	BlockedBy string `json:"blocked_by"`
	Result    struct {
		State    string `json:"state"`
		Message  string `json:"message"`
		Evidence []struct {
			Text string `json:"text"`
		} `json:"evidence"`
		Remediation struct {
			Kind    string   `json:"kind"`
			Command string   `json:"command"`
			Summary string   `json:"summary"`
			Steps   []string `json:"steps"`
		} `json:"remediation"`
		Counts *struct {
			Pass  int `json:"pass"`
			Total int `json:"total"`
		} `json:"counts"`
	} `json:"result"`
}

// doctorRun runs `flow doctor --json`, parses the one document it printed, and
// hands back what it said on the side as well: a report is not the whole of
// what a run tells somebody.
func doctorRun(t *testing.T, dir string, args ...string) (reportDoc, int, string) {
	t.Helper()
	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "doctor"}, args...)...)
	var doc reportDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return doc, code, stderr
}

// doctorJSON is doctorRun for the tests that are only about the report.
func doctorJSON(t *testing.T, dir string, args ...string) (reportDoc, int) {
	t.Helper()
	doc, code, _ := doctorRun(t, dir, args...)
	return doc, code
}

// checkState is what the report said about one check.
func (d reportDoc) checkState(t *testing.T, id string) string {
	t.Helper()
	for _, checked := range d.Checks {
		if checked.ID == id {
			return checked.Result.State
		}
	}
	var ids []string
	for _, checked := range d.Checks {
		ids = append(ids, checked.ID)
	}
	t.Fatalf("the report has no %s; it has %v", id, ids)
	return ""
}

// check is one entry of the report, for the assertions that are about more
// than the state.
func (d reportDoc) check(t *testing.T, id string) checkDoc {
	t.Helper()
	for _, checked := range d.Checks {
		if checked.ID == id {
			return checked
		}
	}
	t.Fatalf("the report has no %s", id)
	panic("unreachable")
}

// lockText is `.flow.lock` as written, for asserting on what flow remembered.
func lockText(t *testing.T, dir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, ".flow.lock"))
	if err != nil {
		t.Fatalf("no lock was written: %v", err)
	}
	return string(content)
}

// boards seeds the fake tracker with the projects a Discover would offer.
func boards(dir string, candidates ...core.Candidate) {
	seedFakeWorld(dir, func(w *fakeWorld) {
		w.tracker.SetCandidates(core.ItemTrackerProject, candidates...)
	})
}

// owners answers the four follow-up owner placeholders, which are not
// discoverable and would otherwise fail `followups.health` in every scenario
// that is not about them.
func owners() []string {
	return []string{
		"--set", "followups.owners.default=@ops",
		"--set", "followups.owners.tracker_admin=@ops",
		"--set", "followups.owners.forge_admin=@ops",
		"--set", "followups.owners.platform=@ops",
	}
}

// TestScenario_A05_DoctorTakesTheOnlyBoardThereIs is the first half of scenario A05: where
// a placeholder has exactly one possible answer, `--fix` takes it and writes
// it down. Nothing is guessed — one candidate is not a choice — and the next
// run reads the answer out of the lock rather than discovering it again.
func TestScenario_A05_DoctorTakesTheOnlyBoardThereIs(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	t.Setenv(fakeEnv, "1")
	boards(dir, core.Candidate{ID: "2", Name: "flow", URL: "https://github.test/users/acme/projects/2"})

	doc, _ := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)

	if got := doc.checkState(t, core.ItemTrackerProject); got == "input" {
		t.Fatalf("%s is still %q: the only board there is was not taken", core.ItemTrackerProject, got)
	}
	lock := lockText(t, dir)
	if !strings.Contains(lock, core.ItemTrackerProject) || !strings.Contains(lock, "flow") {
		t.Errorf("the lock does not record which board was chosen:\n%s", lock)
	}

	// The point of writing it down is that the next run does not have to ask.
	// A second doctor with no --fix and no --set must still know the board.
	again, _ := doctorJSON(t, dir)
	if got := again.checkState(t, core.ItemTrackerProject); got == "input" {
		t.Errorf("the second run asks again: the lock is not being read")
	}
}

// TestDoctorUsesTheAnswerItJustFound is what the first live run of doctor on
// flow's own repository caught, and it is the one thing scenario A05 does not
// say out loud: the run that finds the board's number has to be able to use
// it. The tracker is built from what flow knew when it connected, so a board
// discovered afterwards leaves the adapter pointing at nothing, and the whole
// plan is then unobservable — on the one run that had the answer.
//
// The property is that discovery is invisible in the result: a board flow was
// told about and a board flow worked out must produce the same report. That
// is worth more than naming the states, because the states of an unapplied
// repository are a different scenario's business (A03) and would make this
// test fail for a change it is not about.
func TestDoctorUsesTheAnswerItJustFound(t *testing.T) {
	told := fakeRepo(t)
	worked := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", worked, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	boards(worked, core.Candidate{ID: "2", Name: "flow", URL: "https://github.test/users/acme/projects/2"})

	fix := append([]string{"--fix"}, owners()...)
	discovered, discoveredCode := doctorJSON(t, worked, fix...)
	configured, configuredCode := doctorJSON(t, told, fix...)

	if discoveredCode != configuredCode {
		t.Errorf("doctor exited %d where the board was discovered and %d where it was configured",
			discoveredCode, configuredCode)
	}
	for _, checked := range configured.Checks {
		got := discovered.checkState(t, checked.ID)
		if got != checked.Result.State {
			t.Errorf("%s is %q on the run that discovered the board and %q on the run that was told it: "+
				"the providers were not rebuilt around the answer", checked.ID, got, checked.Result.State)
		}
	}
}

// TestDoctorWillNotCallAnUnseenProviderHealthy is the other half of that
// finding, and the reason it went unnoticed for a run: a doctor that cannot
// observe anything has an empty plan, and an empty plan is indistinguishable
// from a plan that wants nothing — which is a skip, and a report full of
// skips exits 0. "This configuration does not ask for it", said about twenty
// things flow never looked at, is worse than no report at all.
//
// Project zero is how that state is reached here, as it is in production:
// `GitHubProjects` refuses to query a board whose number is not set rather
// than asking about project zero, and nothing is pending that would make the
// refusal a question somebody still has to answer.
func TestDoctorWillNotCallAnUnseenProviderHealthy(t *testing.T) {
	dir := fakeRepo(t)
	writeConfig(t, dir, strings.Replace(configOf(t, dir), `project: "2"`, `project: "0"`, 1))

	doc, code := doctorJSON(t, dir, owners()...)

	if code == core.ExitOK {
		t.Fatalf("doctor exited 0 having observed nothing at all")
	}
	// The two checks that read the plan first say what went wrong, and
	// everything behind them is not asked. What none of them may be is
	// skipped: a check that does not apply and a check flow could not make
	// up its mind about are opposite answers.
	for _, id := range []string{engine.CheckForgeRepo, core.ItemTrackerProject} {
		checked := doc.check(t, id)
		if checked.Result.State != "error" {
			t.Errorf("%s is %q, want error: flow never found out either way", id, checked.Result.State)
		}
		if !strings.Contains(checked.Result.Message, "no project number is set") {
			t.Errorf("%s does not say why there was no plan: %q", id, checked.Result.Message)
		}
	}
	for _, id := range []string{core.ItemForgeProtection, core.ItemTrackerStatusOptions, core.ItemTrackerLabels} {
		if got := doc.checkState(t, id); got != "blocked" {
			t.Errorf("%s is %q, want blocked: it stands behind a question flow could not answer", id, got)
		}
	}
}

// TestScenario_A05_DoctorAsksWhichBoardWhenThereAreTwo is the second half of A05. Two
// candidates is a question only a person can settle, so flow does not choose:
// it reports `input`, says exactly how to answer, blocks everything that
// depends on the answer rather than failing it, and exits 2.
func TestScenario_A05_DoctorAsksWhichBoardWhenThereAreTwo(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	t.Setenv(fakeEnv, "1")
	boards(dir,
		core.Candidate{ID: "2", Name: "flow", URL: "https://github.test/users/acme/projects/2"},
		core.Candidate{ID: "7", Name: "platform", URL: "https://github.test/users/acme/projects/7"},
	)

	doc, code := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)

	if code != core.ExitActionRequired {
		t.Fatalf("doctor exited %d, want %d: a question is pending", code, core.ExitActionRequired)
	}
	project := doc.check(t, core.ItemTrackerProject)
	if project.Result.State != "input" {
		t.Fatalf("%s is %q, want input: flow chose between two boards by itself",
			core.ItemTrackerProject, project.Result.State)
	}
	hint := project.Result.Remediation.Command + " " + strings.Join(project.Result.Remediation.Steps, " ")
	if !strings.Contains(hint, "--set") || !strings.Contains(hint, core.ItemTrackerProject) {
		t.Errorf("the remedy does not say how to answer it: %+v", project.Result.Remediation)
	}

	for _, id := range []string{core.ItemTrackerStatusOptions, core.ItemTrackerLabels} {
		checked := doc.check(t, id)
		if checked.Result.State != "blocked" {
			t.Errorf("%s is %q, want blocked: it stands behind an unanswered question", id, checked.Result.State)
		}
		if checked.BlockedBy != core.ItemTrackerProject {
			t.Errorf("%s says it was blocked by %q, want %s", id, checked.BlockedBy, core.ItemTrackerProject)
		}
	}
	// The forge's items are asked for by the configuration too, and were
	// not planned only because the question is open. Saying "this
	// configuration does not ask for it" about them is false (issue #263).
	if got := doc.checkState(t, engine.CheckForgeRepo); got != "pass" {
		t.Errorf("%s is %q, want pass: the repository answered, and the open question is the tracker's", engine.CheckForgeRepo, got)
	}
	for _, id := range []string{core.ItemForgeProtection, core.ItemForgeRequiredStatus, core.ItemForgeFiles} {
		checked := doc.check(t, id)
		if checked.Result.State != "blocked" || !strings.Contains(checked.Result.Message, core.ItemTrackerProject+" is still to find out") {
			t.Errorf("%s is %q %q, want it not asked until %s is answered", id, checked.Result.State, checked.Result.Message, core.ItemTrackerProject)
		}
	}

	// Nothing was written down, because nothing was decided.
	if content, err := os.ReadFile(filepath.Join(dir, ".flow.lock")); err == nil {
		if strings.Contains(string(content), "projects/7") {
			t.Error("flow recorded a board it was never told to use")
		}
	}

	// And --set is the answer: the same run, told which one, settles it.
	answered, _ := doctorJSON(t, dir, append([]string{"--fix", "--set", core.ItemTrackerProject + "=7"}, owners()...)...)
	if got := answered.checkState(t, core.ItemTrackerProject); got == "input" {
		t.Errorf("--set did not answer the question: %s is still input", core.ItemTrackerProject)
	}
}

// writeAccess is a forge credential the provider accepts and which still
// cannot administer the repository: the commonest shape of A06, because a
// repository's own `GITHUB_TOKEN` is exactly this.
var writeAccess = core.Permissions{
	Tokens:   []string{"statuses_write", "contents_write", "pulls_write"},
	Evidence: []string{"repository permission: write"},
}

// adminAccess is the credential the person who can do something about it
// holds.
var adminAccess = core.Permissions{
	Tokens:   []string{"admin", "statuses_write", "contents_write", "pulls_write"},
	Evidence: []string{"repository permission: admin"},
}

// TestScenario_A06_DoctorFilesARulesetFollowUpAndSettlesIt is scenario A06 forwards: a
// token that cannot protect the trunk makes `forge.protection` somebody
// else's work rather than a red mark, the ticket is filed once however many
// times doctor runs, and when that somebody has done it flow says so on the
// ticket and closes it.
func TestScenario_A06_DoctorFilesARulesetFollowUpAndSettlesIt(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(writeAccess) })

	first, code := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if code != core.ExitActionRequired {
		t.Fatalf("doctor exited %d, want %d: a follow-up is outstanding", code, core.ExitActionRequired)
	}
	if got := first.checkState(t, core.ItemForgeProtection); got != "external" {
		t.Fatalf("%s is %q, want external: a filed follow-up is waiting on somebody, not broken",
			core.ItemForgeProtection, got)
	}

	ctx := t.Context()
	marker := engine.FollowUpMarker(core.ItemForgeProtection, "acme/api")
	ticket, err := world().tracker.FindFollowUp(ctx, marker)
	if err != nil || ticket == nil {
		t.Fatalf("no ticket carries %v: %v", marker, err)
	}
	if !strings.Contains(ticket.Body, core.ItemForgeProtection) {
		t.Errorf("the ticket does not say which check it is about:\n%s", ticket.Body)
	}
	if !strings.Contains(lockText(t, dir), ticket.Key) {
		t.Errorf("the lock does not record follow-up %s", ticket.Key)
	}

	// The marker is what makes a daily run safe: running again must adopt
	// the ticket that is there, not open a second one about the same thing.
	before := len(ticketsOf(t, world()))
	doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if after := len(ticketsOf(t, world())); after != before {
		t.Errorf("a second run filed more tickets: %d became %d", before, after)
	}

	// Somebody with the access does the work. Flow sees the trunk protected
	// on the next run and settles its own ticket.
	world().forge.SetPermissions(adminAccess)
	if code, _, stderr := execAt(t, dir, "apply", "--auto-approve"); code == core.ExitFail {
		t.Fatalf("the administrator's apply failed: %s", stderr)
	}
	world().forge.SetPermissions(writeAccess)

	settled, _ := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if got := settled.checkState(t, core.ItemForgeProtection); got != "pass" {
		t.Fatalf("%s is %q, want pass: the trunk is protected now", core.ItemForgeProtection, got)
	}
	done, err := world().tracker.Ticket(ctx, ticket.Key)
	if err != nil {
		t.Fatalf("read %s back: %v", ticket.Key, err)
	}
	if !done.Closed {
		t.Errorf("%s is still open after flow saw the work done", ticket.Key)
	}
	if done.CloseReason != core.ClosedCompleted {
		t.Errorf("%s was closed as %q, want %q", ticket.Key, done.CloseReason, core.ClosedCompleted)
	}
	if comments := world().tracker.Written(ticket.Key); len(comments) == 0 {
		t.Error("flow closed the ticket without saying why")
	} else if !strings.Contains(comments[len(comments)-1].Body, core.ItemForgeProtection) {
		t.Errorf("the closing comment does not name the check:\n%s", comments[len(comments)-1].Body)
	}
}

// TestScenario_A06_DoctorReopensAFollowUpClosedWithTheWorkUndone is the rest of A06.
// Closing the ticket is not a way to make the check pass: flow reopens it and
// says what it still sees, because a workflow whose checks can be silenced by
// closing a ticket proves nothing.
func TestScenario_A06_DoctorReopensAFollowUpClosedWithTheWorkUndone(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(writeAccess) })
	doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)

	ctx := t.Context()
	ticket, err := world().tracker.FindFollowUp(ctx, engine.FollowUpMarker(core.ItemForgeProtection, "acme/api"))
	if err != nil || ticket == nil {
		t.Fatalf("no follow-up was filed: %v", err)
	}
	if err := world().tracker.Close(ctx, ticket.Key, core.ClosedCompleted); err != nil {
		t.Fatalf("close %s: %v", ticket.Key, err)
	}

	doc, code := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if code != core.ExitActionRequired {
		t.Fatalf("doctor exited %d, want %d: the trunk is still unprotected", code, core.ExitActionRequired)
	}
	if got := doc.checkState(t, core.ItemForgeProtection); got != "external" {
		t.Errorf("%s is %q, want external: the work is still somebody's", core.ItemForgeProtection, got)
	}
	again, err := world().tracker.Ticket(ctx, ticket.Key)
	if err != nil {
		t.Fatalf("read %s back: %v", ticket.Key, err)
	}
	if again.Closed {
		t.Fatalf("%s is still closed: closing it silenced the check", ticket.Key)
	}
	if reason := world().tracker.ReopenReason(ticket.Key); !strings.Contains(reason, core.ItemForgeProtection) {
		t.Errorf("flow reopened %s without saying why: %q", ticket.Key, reason)
	}
	comments := world().tracker.Written(ticket.Key)
	if len(comments) == 0 {
		t.Fatal("flow reopened the ticket silently")
	}
	if body := comments[len(comments)-1].Body; !strings.Contains(body, "admin") {
		t.Errorf("the reopening comment carries no evidence of what flow saw:\n%s", body)
	}
}

// TestScenario_A07_DoctorCallsARenamedColumnDrift is scenario A07. Something that passed,
// looks different and no longer holds is `drift` rather than a plain failure:
// the distinction is between a workflow nobody finished setting up and one
// somebody changed behind flow's back, and only the second is news.
func TestScenario_A07_DoctorCallsARenamedColumnDrift(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) {
		w.tracker.SetCandidates(core.ItemTrackerProject,
			core.Candidate{ID: "2", Name: "flow", URL: "https://github.test/users/acme/projects/2"})
	})
	// Resolve the board, converge it, then let doctor see it hold: drift is
	// measured against what flow remembers, so there has to be a memory.
	doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if code, _, stderr := execAt(t, dir, "apply", "--auto-approve"); code == core.ExitFail {
		t.Fatalf("apply failed: %s", stderr)
	}
	held, _ := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	if got := held.checkState(t, core.ItemTrackerStatusOptions); got != "pass" {
		t.Fatalf("%s is %q after an apply, want pass: there is nothing for it to drift from",
			core.ItemTrackerStatusOptions, got)
	}

	// Somebody renames a column on the board itself.
	ctx := t.Context()
	observed, err := world().tracker.Observe(ctx, core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("read the board back: %v", err)
	}
	renamed := false
	for i := range observed.Items {
		if observed.Items[i].Name == "In review" {
			observed.Items[i].Name, renamed = "Reviewing", true
		}
	}
	if !renamed {
		t.Fatalf("the board has no In review column to rename: %+v", observed.Items)
	}
	world().tracker.SetObservation(core.ItemTrackerStatusOptions, observed)

	drifted, code := doctorJSON(t, dir, owners()...)
	if code != core.ExitActionRequired {
		t.Fatalf("doctor exited %d, want %d", code, core.ExitActionRequired)
	}
	options := drifted.check(t, core.ItemTrackerStatusOptions)
	if options.Result.State != "drift" {
		t.Fatalf("%s is %q, want drift: it held before and does not now",
			core.ItemTrackerStatusOptions, options.Result.State)
	}
	if !strings.Contains(options.Result.Message, "has changed since") {
		t.Errorf("the message does not say it used to hold: %q", options.Result.Message)
	}
	if options.Result.Remediation.Command == "" {
		t.Errorf("drift was reported with nothing to do about it: %+v", options.Result.Remediation)
	}
}

// TestDoctorStampsTheLockWithTheConfigurationItRead is spec 03 §3 from the
// command's end. What flow writes down is only true of the workflow it was
// written down about, so the lock carries that workflow's hash — and when the
// workflow is edited, the next run says on `lock.consistent` that what it
// remembers was about something else. Without the stamp every promise in the
// lock would survive a rewrite of the thing it was promised about.
func TestDoctorStampsTheLockWithTheConfigurationItRead(t *testing.T) {
	dir, _ := gateRepo(t, func(*fakeWorld) {})

	doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)

	path := filepath.Join(dir, ".flow.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the configuration back: %v", err)
	}
	if want := config.Hash(source); !strings.Contains(lockText(t, dir), want) {
		t.Fatalf("the lock does not record the configuration it was written about (%s):\n%s",
			want, lockText(t, dir))
	}

	// Editing the workflow is ordinary rather than a fault, so the check
	// still passes — but it has lapsed what was promised about the old file,
	// and the run has to say so or the lapse is invisible.
	if err := os.WriteFile(path, append(source, "\n# a later edit\n"...), 0o644); err != nil {
		t.Fatalf("edit the configuration: %v", err)
	}

	doc, _ := doctorJSON(t, dir, owners()...)
	consistent := doc.check(t, engine.CheckLockConsistent)
	if consistent.Result.State != "pass" {
		t.Errorf("%s is %q, want pass: editing the workflow is not a fault",
			engine.CheckLockConsistent, consistent.Result.State)
	}
	if !saw(consistent, "the configuration has changed since the lock was written") {
		t.Errorf("%s does not say the lock predates this configuration: %+v",
			engine.CheckLockConsistent, consistent.Result)
	}
}

// TestDoctorReportsABrokenConfigurationRatherThanRefusingToStart is the whole
// reason doctor builds its session differently from every other command
// (spec 07 §5). A workflow that does not parse, or that names a column it does
// not have, is exactly the moment somebody needs the report most — and a
// command that will not start cannot tell them which part is wrong. What stands
// behind the broken part is blocked rather than failed twenty times over.
func TestDoctorReportsABrokenConfigurationRatherThanRefusingToStart(t *testing.T) {
	t.Run("semantics", func(t *testing.T) {
		dir, _ := gateRepo(t, func(*fakeWorld) {})
		source := configOf(t, dir)
		broken := strings.Replace(source, "{ to: done,        owner: flow   }", "{ to: shipped,     owner: flow   }", 1)
		if broken == source {
			t.Fatal("the preset no longer has the transition this test breaks")
		}
		writeConfig(t, dir, broken)

		doc, code, stderr := doctorRun(t, dir, owners()...)

		if code == core.ExitOK {
			t.Error("doctor exited 0 on a workflow that does not hang together")
		}
		// And it does not go on to blame the providers for it. Planning a
		// workflow that was just found incoherent means asking a tracker
		// flow never connected to, and the warning that produces points at
		// the wrong thing entirely.
		if strings.Contains(stderr, "providers") {
			t.Errorf("doctor blamed the providers for a local mistake: %s", stderr)
		}
		if got := doc.checkState(t, engine.CheckConfigSyntax); got != "pass" {
			t.Errorf("%s is %q, want pass: the file parses, it just does not add up",
				engine.CheckConfigSyntax, got)
		}
		if got := doc.checkState(t, engine.CheckConfigSemantics); got != "fail" {
			t.Fatalf("%s is %q, want fail", engine.CheckConfigSemantics, got)
		}
		if !saw(doc.check(t, engine.CheckConfigSemantics), "shipped") {
			t.Errorf("the failure does not say what was wrong: %+v",
				doc.check(t, engine.CheckConfigSemantics).Result)
		}
		blocked := doc.check(t, engine.CheckLocalGit)
		if blocked.Result.State != "blocked" || blocked.BlockedBy != engine.CheckConfigSemantics {
			t.Errorf("%s is %q blocked by %q, want blocked by %s",
				engine.CheckLocalGit, blocked.Result.State, blocked.BlockedBy, engine.CheckConfigSemantics)
		}
	})

	t.Run("syntax", func(t *testing.T) {
		dir, _ := gateRepo(t, func(*fakeWorld) {})
		writeConfig(t, dir, "version: 1\nforge: [unclosed\n")

		doc, code := doctorJSON(t, dir, owners()...)

		if code == core.ExitOK {
			t.Error("doctor exited 0 on a file it could not read")
		}
		if got := doc.checkState(t, engine.CheckConfigSyntax); got != "fail" {
			t.Fatalf("%s is %q, want fail", engine.CheckConfigSyntax, got)
		}
		if got := doc.checkState(t, engine.CheckConfigSemantics); got != "blocked" {
			t.Errorf("%s is %q, want blocked: there is nothing to judge",
				engine.CheckConfigSemantics, got)
		}
	})
}

// configOf is `.flow.yaml` as it stands, and writeConfig puts one back.
func configOf(t *testing.T, dir string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(dir, ".flow.yaml"))
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}
	return string(source)
}

func writeConfig(t *testing.T, dir, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".flow.yaml"), []byte(source), 0o644); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
}

// saw reports whether a check's evidence mentions something.
func saw(checked checkDoc, want string) bool {
	for _, evidence := range checked.Result.Evidence {
		if strings.Contains(evidence.Text, want) {
			return true
		}
	}
	return false
}

// ticketsOf is every ticket the fake tracker holds.
func ticketsOf(t *testing.T, world *fakeWorld) []core.Ticket {
	t.Helper()
	tickets, err := world.tracker.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("read the tickets: %v", err)
	}
	return tickets
}

// A fortnight of merges compared afresh every morning is the same question
// put to the forge every day for answers that cannot change: a commit's
// parents are settled for all time (spec 13 §3). So the daily audit remembers
// its comparisons, in the place and by the name `flow check deploy` keeps
// them, rather than in a second cache of its own that would halve the value
// of both.
func TestDoctorRemembersWhatItComparedWhereCheckDeployKeepsIt(t *testing.T) {
	dir := fakeRepo(t)

	doctorJSON(t, dir, owners()...)

	// The cache directory is the repository's own, because hookRepo isolates
	// every test from the machine's.
	if _, err := os.Stat(filepath.Join(os.Getenv(core.CacheDirEnv), core.AncestryDir)); err != nil {
		t.Fatalf("the audit kept nothing it compared: %v", err)
	}
}

// A report nobody reads twice is a run nobody reads at all. `--report` writes
// the markdown to a file for a CI job to attach, and a `--fix` run keeps the
// same markdown in one pinned issue that it rewrites every morning (spec 07
// §10) — which is what makes a fortnight of green days one page somebody can
// open rather than fourteen notifications they have muted.
func TestDoctorWritesTheReportWhereItWasAsked(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })
	path := filepath.Join(t.TempDir(), "health.md")

	code, _, stderr := execAt(t, dir, append([]string{"doctor", "--report", path}, owners()...)...)
	if code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("--report wrote nothing to %s: %v", path, err)
	}
	// Markdown, not the terminal rendering: the file is for a web page.
	for _, want := range []string{"## flow doctor", "| check | found |", "`config.syntax`"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("the report does not contain %q:\n%s", want, written)
		}
	}
}

// Without --fix flow writes to the file it was given and to nothing else: the
// tracker is somebody's project, and a preview that opened an issue would be
// a preview that changed the world (spec 02 §2).
func TestDoctorPreviewsTheReportWithoutFilingIt(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })
	path := filepath.Join(t.TempDir(), "health.md")

	execAt(t, dir, append([]string{"doctor", "--report", path}, owners()...)...)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("--report wrote nothing: %v", err)
	}
	found, err := world().tracker.FindFollowUp(t.Context(), engine.HealthMarker("acme/api"))
	if err != nil {
		t.Fatalf("look for the health issue: %v", err)
	}
	if found != nil {
		t.Errorf("a run without --fix opened %s", found.Key)
	}
}

// The pinned issue is one issue. A daily run that filed a new one every
// morning would bury the record it exists to keep.
func TestDoctorKeepsOneHealthIssueAndRewritesIt(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })

	execAt(t, dir, append([]string{"doctor", "--fix"}, owners()...)...)
	marker := engine.HealthMarker("acme/api")
	ticket, err := world().tracker.FindFollowUp(t.Context(), marker)
	if err != nil || ticket == nil {
		t.Fatalf("no issue carries %v: %v", marker, err)
	}
	if !strings.Contains(ticket.Body, "### Daily runs") {
		t.Errorf("the issue carries no run log:\n%s", ticket.Body)
	}
	if !strings.Contains(ticket.Body, "| check | found |") {
		t.Errorf("the issue does not carry the report itself:\n%s", ticket.Body)
	}

	before := len(ticketsOf(t, world()))
	execAt(t, dir, append([]string{"doctor", "--fix"}, owners()...)...)
	if after := len(ticketsOf(t, world())); after != before {
		t.Errorf("a second morning filed another issue: %d became %d", before, after)
	}
	again, err := world().tracker.FindFollowUp(t.Context(), marker)
	if err != nil || again == nil {
		t.Fatalf("the issue is no longer findable by its marker: %v", err)
	}
	if again.Key != ticket.Key {
		t.Errorf("the second run wrote to %s, the first to %s", again.Key, ticket.Key)
	}
}

// A tracker that will not take the report is weather, and the run exits on
// what it found. A run that ran out of its own time did not finish: on the
// day flow's own daily job hit its deadline filing the report, it went green
// and the health issue quietly stopped being rewritten.
func TestDoctorThatRanOutOfTimePublishingDidNotFinish(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		err    error
		want   int
	}{
		{"weather", "FindFollowUp", core.Errorf(core.ErrUnavailable, "look for the report"), core.ExitActionRequired},
		{"deadline", "FindFollowUp", fmt.Errorf("look for the report: %w", context.DeadlineExceeded), core.ExitUnavailable},
		{"lock deadline", "ProposeFiles", fmt.Errorf("propose: %w", context.DeadlineExceeded), core.ExitUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := gateRepo(t, func(w *fakeWorld) {
				w.forge.SetPermissions(adminAccess)
				w.tracker.Fail(tc.method, tc.err)
				w.forge.Fail(tc.method, tc.err)
			})
			code, _, stderr := execAt(t, dir, append([]string{"doctor", "--ci", "--fix"}, owners()...)...)
			if code != tc.want {
				t.Errorf("doctor exited %d, want %d: %s", code, tc.want, stderr)
			}
			if tc.want == core.ExitUnavailable && !strings.Contains(stderr, "finish within") {
				t.Errorf("the run does not say it ran out of time: %s", stderr)
			}
		})
	}
}

// A run asked only about `config.*` saw a fraction of the workflow.
// Publishing it would overwrite a whole morning's report with a partial one
// and record a day in the streak that nobody audited.
func TestDoctorDoesNotPublishARunThatOnlyLookedAtSomeOfIt(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })

	execAt(t, dir, append([]string{"doctor", "--fix", "--check", "config.*"}, owners()...)...)
	found, err := world().tracker.FindFollowUp(t.Context(), engine.HealthMarker("acme/api"))
	if err != nil {
		t.Fatalf("look for the health issue: %v", err)
	}
	if found != nil {
		t.Errorf("a narrowed run published %s as if it were the morning report", found.Key)
	}
}

// What a CI job is told (item 5.8, spec 02 §2). A scheduled run is read by
// somebody who was not there, through an interface that holds a few lines, so
// `--ci` is about what survives the job: the annotations on the run, the
// document still being a document, and the lock going somewhere that is not
// a checkout about to be deleted.

// TestDoctorAnnotatesTheJobItIsRunningIn: a finding a person must act on
// becomes a line the runner lifts out of the log; a finding that held does
// not, because an interface full of green says nothing when something is red.
func TestDoctorAnnotatesTheJobItIsRunningIn(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(writeAccess) })

	code, stdout, stderr := execAt(t, dir, append([]string{"doctor", "--ci"}, owners()...)...)
	if code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	if !strings.Contains(stderr, "::warning title="+core.ItemForgeProtection+"::") {
		t.Errorf("the job was not told about %s:\n%s", core.ItemForgeProtection, stderr)
	}
	if strings.Contains(stderr, "title=config.syntax") {
		t.Errorf("a check that held was annotated:\n%s", stderr)
	}
	if strings.Contains(stdout, "::") {
		t.Errorf("the annotations were written into the report:\n%s", stdout)
	}
	if !strings.Contains(stdout, core.ItemForgeProtection) {
		t.Errorf("the report itself was not printed:\n%s", stdout)
	}
}

// TestDoctorInCIStillPrintsOneDocument: the annotations are a second reader's
// stream, so `--json --ci` must still hand a machine exactly one document.
func TestDoctorInCIStillPrintsOneDocument(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(writeAccess) })

	code, stdout, stderr := execAt(t, dir, append([]string{"--json", "doctor", "--ci"}, owners()...)...)
	if code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	var doc reportDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json --ci is not one JSON document (%v):\n%s", err, stdout)
	}
	if !strings.Contains(stderr, "::warning title=") {
		t.Errorf("a JSON run annotated nothing:\n%s", stderr)
	}
}

// TestDoctorInCIProposesTheLockItWouldHaveWritten: a lock written into a CI
// checkout is deleted with the runner, so in `--ci` the refresh becomes a
// pull request instead — and one pull request, however many mornings it
// takes somebody to merge it.
func TestDoctorInCIProposesTheLockItWouldHaveWritten(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })

	args := append([]string{"doctor", "--ci", "--fix"}, owners()...)
	if code, _, stderr := execAt(t, dir, args...); code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, config.LockPath)); err == nil {
		t.Fatalf("a CI run wrote %s into a checkout nobody will read again", config.LockPath)
	}
	proposed := proposalOf(t, world())
	if proposed == nil {
		t.Fatal("nothing was proposed, so the refreshed lock went nowhere")
	}
	if !slices.Contains(proposed.Files.Paths, config.LockPath) {
		t.Errorf("the proposal does not carry %s: %v", config.LockPath, proposed.Files.Paths)
	}
	title := commits.Parse(proposed.Title)
	if broken := commits.Errors(commits.Lint(title, commits.LintOptions{Settings: commits.DefaultSettings()})); len(broken) > 0 {
		t.Errorf("the pull request's title breaks the convention it asks of everybody else: %v", broken)
	}
	// And the convention this repository asks of everybody else, which is the
	// one its own gate will judge the proposal by (issue #152).
	if _, unlinked := engine.LockProposal.Title(configIn(t, dir)); unlinked != nil {
		t.Errorf("flow proposed a lock its own gate refuses: %v", unlinked)
	}

	execAt(t, dir, args...)
	if got := proposals(t, world()); len(got) != 1 {
		t.Errorf("a second morning opened another pull request: %d in all", len(got))
	}
}

// TestDoctorProposesNothingItsOwnGateWouldRefuse: the lock proposal is judged
// by flow's own gate, so a configuration that leaves flow no landable title
// gets no pull request at all. One that nobody can merge is a red cross on
// the trunk's gate every morning, on the one change nobody can act on, and
// the only useful thing flow can say about it is which key said no (issue
// #152).
func TestDoctorProposesNothingItsOwnGateWouldRefuse(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })
	// Nothing flow could honestly call its own bookkeeping may merge here
	// without a ticket, and flow has no ticket for keeping its own lock.
	rewriteConfig(t, dir, "unlinked_types: [ci, build, chore, docs]", "unlinked_types: [feat]")

	args := append([]string{"doctor", "--ci", "--fix"}, owners()...)
	code, _, stderr := execAt(t, dir, args...)
	if code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	if got := proposals(t, world()); len(got) != 0 {
		t.Errorf("flow opened a pull request its own gate refuses: %q", got[0].Title)
	}
	if !strings.Contains(stderr, "commits.unlinked_types") {
		t.Errorf("nothing said which key refused the proposal:\n%s", stderr)
	}
}

// configIn reads a repository's own configuration, so that a test can ask
// what flow's gate would make of what flow wrote.
func configIn(t *testing.T, dir string) *config.Config {
	t.Helper()
	path := filepath.Join(dir, config.DefaultPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw, path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// rewriteConfig edits one line of a repository's configuration, failing when
// the line it was told to replace is not there: a fixture that silently stops
// shaping anything is a test that silently stops testing.
func rewriteConfig(t *testing.T, dir, from, to string) {
	t.Helper()
	path := filepath.Join(dir, config.DefaultPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), from) {
		t.Fatalf("%s does not hold %q", path, from)
	}
	edited := strings.Replace(string(raw), from, to, 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorProposesNothingWhenTheLockIsAlreadyRight: the refresh is a
// proposal only when there is something to propose. A pull request a day
// against a lock nobody has changed is how a team learns to ignore them.
func TestDoctorProposesNothingWhenTheLockIsAlreadyRight(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(adminAccess) })

	if code, _, stderr := execAt(t, dir, append([]string{"doctor", "--fix"}, owners()...)...); code == core.ExitFail {
		t.Fatalf("doctor failed: %s", stderr)
	}
	before := lockText(t, dir)

	if code, _, stderr := execAt(t, dir, append([]string{"doctor", "--ci", "--fix"}, owners()...)...); code == core.ExitFail {
		t.Fatalf("doctor --ci failed: %s", stderr)
	}
	if got := proposals(t, world()); len(got) != 0 {
		t.Errorf("a lock that was already right was proposed anyway: %v", got[0].Title)
	}
	if after := lockText(t, dir); after != before {
		t.Errorf("the CI run rewrote the lock:\n%s\nwas\n%s", after, before)
	}
}

// TestDoctorLooksBackAsFarAsItWasAsked: the live-health audit's window is a
// flag because a scheduled job and a person checking by hand want different
// fortnights out of the same repository (spec 02 §2).
func TestDoctorLooksBackAsFarAsItWasAsked(t *testing.T) {
	now := time.Now()
	dir, _ := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPermissions(adminAccess)
		w.forge.SetPullRequest(mergedWithoutATicket(41, now.Add(-2*24*time.Hour)))
		w.forge.SetPullRequest(mergedWithoutATicket(42, now.Add(-20*24*time.Hour)))
	})

	fortnight, _ := doctorJSON(t, dir, append([]string{"--window", "14d"}, owners()...)...)
	if got := fortnight.check(t, engine.CheckLivePRsLinked).Result.Counts.Total; got != 1 {
		t.Errorf("a fortnight read %d merges, want 1: the older one is outside it", got)
	}
	if got := fortnight.Window; got != "14d" {
		t.Errorf("the report says the window was %q, want 14d", got)
	}

	month, _ := doctorJSON(t, dir, append([]string{"--window", "720h"}, owners()...)...)
	if got := month.check(t, engine.CheckLivePRsLinked).Result.Counts.Total; got != 2 {
		t.Errorf("a month read %d merges, want 2", got)
	}
}

// mergedWithoutATicket is a pull request that merged into the trunk naming no
// ticket, which is what `live.prs.linked` counts.
func mergedWithoutATicket(number int, at time.Time) core.PullRequest {
	pr := gatePR(number, "feat(engine): add x")
	pr.State = core.PRStateMerged
	pr.MergedAt = at
	pr.UpdatedAt = at
	pr.MergeSHA = fmt.Sprintf("merge-%d", number)
	return pr
}

// proposals are the pull requests flow opened for itself.
func proposals(t *testing.T, world *fakeWorld) []core.PullRequest {
	t.Helper()
	open, err := world.forge.PullRequests(t.Context(), core.PRQuery{State: core.PRStateOpen})
	if err != nil {
		t.Fatalf("read the pull requests: %v", err)
	}
	var mine []core.PullRequest
	for _, pr := range open {
		if pr.HeadRef != (core.FileProposal{ID: "lock"}).Branch() {
			continue
		}
		// A listing does not carry the files, so ask for the pull request
		// itself — which is what anything judging one has to do.
		full, err := world.forge.PullRequest(t.Context(), pr.Number)
		if err != nil {
			t.Fatalf("read pull request #%d: %v", pr.Number, err)
		}
		mine = append(mine, full)
	}
	return mine
}

// proposalOf is the one proposal flow made, or nil.
func proposalOf(t *testing.T, world *fakeWorld) *core.PullRequest {
	t.Helper()
	found := proposals(t, world)
	if len(found) == 0 {
		return nil
	}
	return &found[0]
}

// Where a commit message's trailers end and its body begins is a question
// only git answers consistently (spec 08 §2), so flow hands the message to
// the working copy's own git rather than matching lines that look like
// trailers. The difference is not academic: a `Flag-Added:` line with prose
// after it is prose, and a regex would file a cleanup ticket for a flag
// nobody added.
func TestDoctorReadsTrailersWithGitItself(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPermissions(adminAccess)
		w.forge.SetCommits(
			onTrunk("c0ffee1", "feat(checkout): the new basket",
				"It goes to ten per cent on Monday.", "Flag-Added: new_basket"),
			onTrunk("decafb2", "feat(search): faster suggestions",
				"Flag-Added: never_added", "In the end we did not need a flag for this."),
		)
	})

	doc, _ := doctorJSON(t, dir, append([]string{"--fix"}, owners()...)...)
	checked := doc.check(t, engine.CheckLiveFlagsCleanup)
	if checked.Result.State != "pass" {
		t.Fatalf("the flag audit says %q: %s", checked.Result.State, checked.Result.Message)
	}
	if got := checked.Result.Counts.Total; got != 1 {
		t.Errorf("git found %d flags in two commits, want 1: the second one's is prose", got)
	}

	for name, want := range map[string]bool{"new_basket": true, "never_added": false} {
		found, err := world().tracker.FindFollowUp(t.Context(), engine.FlagMarker(name, "acme/api"))
		if err != nil {
			t.Fatalf("look for the cleanup ticket for %s: %v", name, err)
		}
		if (found != nil) != want {
			t.Errorf("a cleanup ticket for %s exists = %v, want %v", name, found != nil, want)
		}
	}
}

// onTrunk is a commit on the fake forge's default branch, recent enough for
// the audit's window to hold it, whose message is the header followed by each
// paragraph in turn.
func onTrunk(sha, header string, paragraphs ...string) core.Commit {
	at := time.Now().Add(-2 * 24 * time.Hour)
	message := header
	for _, paragraph := range paragraphs {
		message += "\n\n" + paragraph
	}
	return core.Commit{
		SHA:         sha,
		Message:     message,
		AuthoredAt:  at,
		CommittedAt: at,
		URL:         "https://github.test/acme/api/commit/" + sha,
	}
}

// TestDoctorDoesNotAskARunnerForAGitHook: the hook is a thing in a working
// copy, and a runner's working copy lasts as long as the job. The scheduled
// audit of spec 07 §10 is what makes it matter — a check that cannot hold in
// CI is a check that keeps every daily run from ever being green — so the
// same repository has to answer differently to the two runs.
func TestDoctorDoesNotAskARunnerForAGitHook(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) { w.forge.SetPermissions(writeAccess) })
	if err := os.Remove(filepath.Join(dir, ".git", "hooks", "commit-msg")); err != nil {
		t.Fatalf("this repository had no hook to take away: %v", err)
	}

	local, _ := doctorJSON(t, dir, owners()...)
	if got := local.checkState(t, engine.CheckLocalHook); got != "fail" {
		t.Fatalf("a working copy with no hook answered %q, want fail", got)
	}

	ci, _ := doctorJSON(t, dir, append([]string{"--ci"}, owners()...)...)
	if got := ci.checkState(t, engine.CheckLocalHook); got != "skip" {
		t.Errorf("the same checkout in CI answered %q, want skip", got)
	}
	if message := ci.check(t, engine.CheckLocalHook).Result.Message; !strings.Contains(message, "thrown away") {
		t.Errorf("the skip says %q, which does not say why a runner needs no hook", message)
	}
}
