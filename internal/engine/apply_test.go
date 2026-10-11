package engine_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// saver remembers what the lock said each time it was written, so that a test
// can ask what an interrupted apply had committed to at every point rather
// than only what it finished with.
type saver struct {
	writes [][]string
}

func (s *saver) save(lock *config.Lock) error {
	created := make([]string, 0, len(lock.Created))
	for _, creation := range lock.Created {
		created = append(created, creation.Kind+" "+creation.Name)
	}
	s.writes = append(s.writes, created)
	return nil
}

// last is the lock as it was last written.
func (s *saver) last() []string {
	if len(s.writes) == 0 {
		return nil
	}
	return s.writes[len(s.writes)-1]
}

// applyTo carries out a world's own plan against it.
func applyTo(t *testing.T, w world, in engine.ApplyInput) (engine.Applied, *saver) {
	t.Helper()
	records := &saver{}
	if in.Plan.Version == "" {
		in.Plan = planFor(t, w)
	}
	if in.Desired.Items == nil {
		in.Desired = desired(t, preset(t, config.PresetLeanKanban))
	}
	if in.Adapters.Tracker == nil && in.Adapters.Forge == nil {
		in.Adapters = w.adapters()
	}
	if in.Lock == nil {
		in.Lock = w.lock
	}
	if in.Clock == nil {
		in.Clock = coretest.NewClock(coretest.ContractNow)
	}
	if in.Save == nil {
		in.Save = records.save
	}
	applied, err := engine.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return applied, records
}

// item is one item's outcome, or a failure when the apply never reached it.
func item(t *testing.T, applied engine.Applied, id string) engine.AppliedItem {
	t.Helper()
	at := slices.IndexFunc(applied.Items, func(i engine.AppliedItem) bool { return i.ID == id })
	if at < 0 {
		t.Fatalf("%s is not among the items apply attempted", id)
	}
	return applied.Items[at]
}

// TestApplyMakesTheProviderAgree is the whole point: run the plan, and the
// next plan has nothing left to do.
func TestApplyMakesTheProviderAgree(t *testing.T) {
	w := githubProjectsWorld(t)
	applied, records := applyTo(t, w, engine.ApplyInput{Prune: true})

	if err := applied.Err(); err != nil {
		t.Fatalf("apply reported a failure: %v", err)
	}
	done, skipped, failed := applied.Counts()
	if failed != 0 {
		t.Fatalf("%d items failed", failed)
	}
	if done != 6 || skipped != 8 {
		t.Errorf("%d done and %d skipped, want 6 and 8", done, skipped)
	}

	// The board now holds what was asked of it, and no longer holds the
	// column flow created and the configuration dropped.
	after := planFor(t, w)
	for _, id := range []string{
		engine.ItemTrackerStatusOptions,
		engine.ItemTrackerLabels,
		engine.ItemForgeProtection,
		engine.ItemForgeRequiredStatus,
		engine.ItemForgeMergeSettings,
	} {
		planned, ok := after.Find(id)
		if !ok {
			t.Fatalf("%s is no longer planned at all", id)
		}
		if planned.Outcome != engine.OutcomeNoop {
			t.Errorf("%s is %s after apply, want noop: %s", id, planned.Outcome, planned.Why)
		}
	}

	// The files are proposed, not written, so they are still wanted: a pull
	// request nobody has merged has changed nothing on the trunk.
	files := item(t, applied, engine.ItemForgeFiles)
	if !files.Done() {
		t.Fatalf("the file proposal did not land: %v %s", files.Err, files.Skipped)
	}
	if note := files.Changes[0].Note; !strings.Contains(note, "pull_request_template.md") {
		t.Errorf("the proposal note is %q, want it to name the files", note)
	}

	// Every creation is in the lock, and the pruned option is out of it.
	created := records.last()
	for _, want := range []string{"tracker.status_option Ready", "tracker.label agent", "tracker.label ui"} {
		if !slices.Contains(created, want) {
			t.Errorf("the lock does not record %q: %v", want, created)
		}
	}
	if slices.Contains(created, "tracker.status_option Icebox") {
		t.Errorf("the lock still records the pruned option: %v", created)
	}
}

// TestApplyWritesTheWholeForgeSetting checks that the trunk ends up with both
// halves of its protection — the flags and the required contexts — and that
// what the repository already required survives.
func TestApplyWritesTheWholeForgeSetting(t *testing.T) {
	w := githubProjectsWorld(t)
	applyTo(t, w, engine.ApplyInput{})

	repo, err := w.forge.Repo(context.Background())
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	protection, err := w.forge.Protection(context.Background(), repo.DefaultBranch)
	if err != nil {
		t.Fatalf("protection: %v", err)
	}
	if !protection.Enabled || !protection.RequireLinearHistory {
		t.Errorf("the trunk is %+v, want it protected with a linear history", protection)
	}
	if protection.AllowForcePush || protection.AllowDeletions {
		t.Errorf("the trunk can still be rewritten or deleted: %+v", protection)
	}
	if !protection.RequiresCheck("flow/policy") {
		t.Errorf("the gate is not required: %v", protection.RequiredChecks)
	}
	if !protection.RequiresCheck("ci/test") {
		t.Errorf("the repository's own check was dropped: %v", protection.RequiredChecks)
	}
	if !repo.Merge.AllowSquash || repo.Merge.AllowMerge || repo.Merge.AllowRebase {
		t.Errorf("the merge strategies are %+v, want squash only", repo.Merge)
	}
}

// TestApplyWithoutPruneKeepsWhatItWouldRemove is spec 02 §2: a removal is
// reported and not made unless somebody asked for it.
func TestApplyWithoutPruneKeepsWhatItWouldRemove(t *testing.T) {
	w := githubProjectsWorld(t)
	applied, records := applyTo(t, w, engine.ApplyInput{})

	options := item(t, applied, engine.ItemTrackerStatusOptions)
	refusals := refused(options)
	if len(refusals) != 1 {
		t.Fatalf("%d refusals, want 1: %+v", len(refusals), options.Changes)
	}
	if !strings.Contains(refusals[0], "--prune") {
		t.Errorf("the refusal is %q, want it to say what would remove it", refusals[0])
	}
	if !options.Done() {
		t.Errorf("the item failed, but a refusal is not a failure: %v", options.Err)
	}

	observation, err := w.tracker.Observe(context.Background(), engine.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !slices.ContainsFunc(observation.Items, func(i core.Item) bool { return i.Name == "Icebox" }) {
		t.Errorf("the column was removed anyway: %v", observation.Items)
	}
	if !slices.Contains(records.last(), "tracker.status_option Icebox") {
		t.Errorf("the lock stopped recording a creation that is still there: %v", records.last())
	}
}

// TestPruneRefusesAnOptionInUse is spec 06 §4's refusal. Deleting a column
// somebody has work in takes that work's status with it, and no flag is
// consent to that.
func TestPruneRefusesAnOptionInUse(t *testing.T) {
	w := githubProjectsWorld(t)
	w.tracker.SetTicket(core.Ticket{
		Key:      "#20",
		Title:    "Someday, maybe",
		Status:   core.Status{ID: "opt-icebox", Name: "Icebox"},
		ColumnID: coretest.ColumnTodo,
	})

	applied, _ := applyTo(t, w, engine.ApplyInput{Prune: true})
	options := item(t, applied, engine.ItemTrackerStatusOptions)

	refusals := refused(options)
	if len(refusals) != 1 {
		t.Fatalf("%d refusals, want 1: %+v", len(refusals), options.Changes)
	}
	if !strings.Contains(refusals[0], "1 ticket") {
		t.Errorf("the refusal is %q, want it to say how much work is in there", refusals[0])
	}

	observation, err := w.tracker.Observe(context.Background(), engine.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !slices.ContainsFunc(observation.Items, func(i core.Item) bool { return i.Name == "Icebox" }) {
		t.Errorf("the column was removed anyway: %v", observation.Items)
	}
	// Everything else the item asked for still happened.
	if !slices.ContainsFunc(observation.Items, func(i core.Item) bool { return i.Name == "Ready" }) {
		t.Errorf("one refusal stopped the rest of the item: %v", observation.Items)
	}
}

// TestPruneRefusesWhatTheLockDoesNotOwn covers a plan that was made when the
// lock still claimed the column, and applied after it stopped. The lock is
// read again at apply time precisely so that the window between the two does
// not become a deletion.
func TestPruneRefusesWhatTheLockDoesNotOwn(t *testing.T) {
	w := githubProjectsWorld(t)
	plan := planFor(t, w)
	w.lock.Created = nil

	applied, _ := applyTo(t, w, engine.ApplyInput{Plan: plan, Prune: true})
	refusals := refused(item(t, applied, engine.ItemTrackerStatusOptions))
	if len(refusals) != 1 || !strings.Contains(refusals[0], "lock") {
		t.Fatalf("refusals are %v, want one naming the lock", refusals)
	}
}

// TestApplyStopsAtAFailureAndLeavesTheLockTrue is the first half of the
// acceptance: an apply that is interrupted has recorded exactly what it did,
// and claimed nothing it did not.
func TestApplyStopsAtAFailureAndLeavesTheLockTrue(t *testing.T) {
	w := githubProjectsWorld(t)
	plan := planFor(t, w)

	halt := errors.New("the project field was changed by somebody else")
	tracker := &haltingTracker{FakeTracker: w.tracker, failAt: 2, err: halt}
	adapters := w.adapters()
	adapters.Tracker = tracker

	applied, records := applyTo(t, w, engine.ApplyInput{Plan: plan, Adapters: adapters, Prune: true})

	options := item(t, applied, engine.ItemTrackerStatusOptions)
	if !errors.Is(options.Err, halt) {
		t.Fatalf("the item's failure is %v, want the one injected", options.Err)
	}
	if len(options.Changes) != 2 {
		t.Fatalf("%d changes were attempted, want 2: %+v", len(options.Changes), options.Changes)
	}
	if !options.Changes[0].Done() {
		t.Errorf("the change that landed is not recorded as done: %+v", options.Changes[0])
	}

	// The lock was written for the change that landed, before the one that
	// did not was attempted, and says nothing about the one that failed.
	if len(records.writes) < 2 {
		t.Fatalf("the lock was written %d times, want one per mutation and one at the end", len(records.writes))
	}
	created := records.last()
	if !slices.Contains(created, "tracker.status_option Ready") {
		t.Errorf("the lock does not record the column that was created: %v", created)
	}
	if !slices.Contains(created, "tracker.status_option Icebox") {
		t.Errorf("the lock disowned a column that is still on the board: %v", created)
	}

	// Everything the failure did not depend on still happened.
	labels := item(t, applied, engine.ItemTrackerLabels)
	if !labels.Done() {
		t.Errorf("one failure stopped an unrelated item: %v %s", labels.Err, labels.Skipped)
	}
}

// TestApplyBlocksOnlyDependants is the other half of spec 06 §3: a failure
// stops what was waiting on it, and nothing else.
func TestApplyBlocksOnlyDependants(t *testing.T) {
	w := githubProjectsWorld(t)
	refusal := errors.New("the credential may not protect branches")
	w.forge.Fail("SetProtection", refusal)

	applied, _ := applyTo(t, w, engine.ApplyInput{})

	protection := item(t, applied, engine.ItemForgeProtection)
	if !errors.Is(protection.Err, refusal) {
		t.Fatalf("protection failed with %v, want the injected refusal", protection.Err)
	}

	checks := item(t, applied, engine.ItemForgeRequiredStatus)
	if checks.Err != nil {
		t.Errorf("the dependant failed rather than being left alone: %v", checks.Err)
	}
	if checks.BlockedBy != engine.ItemForgeProtection {
		t.Errorf("the dependant was blocked by %q, want %s", checks.BlockedBy, engine.ItemForgeProtection)
	}
	if w.forge.Calls("SetProtection") != 1 {
		t.Errorf("the forge was asked %d times, want once: a blocked item is not attempted", w.forge.Calls("SetProtection"))
	}

	merge := item(t, applied, engine.ItemForgeMergeSettings)
	if !merge.Done() {
		t.Errorf("an item that depends on nothing was stopped: %v %s", merge.Err, merge.Skipped)
	}
	if applied.Err() == nil {
		t.Error("apply reported no failure at all")
	}
}

// TestApplySkipsWhatIsNotFlowsToDo states the rule that keeps apply honest:
// only a native outcome is carried out. A compensated item is enforced
// elsewhere, an attested one is a human's word, and an unsupported one is
// nobody's to make true.
func TestApplySkipsWhatIsNotFlowsToDo(t *testing.T) {
	w := githubProjectsWorld(t)
	applied, _ := applyTo(t, w, engine.ApplyInput{})

	for _, id := range []string{
		engine.ItemTrackerWIP,
		engine.ItemTrackerExpedite,
		engine.NativeItem("issue_closed"),
		engine.ItemDeploysRecords,
	} {
		skipped := item(t, applied, id)
		if skipped.Skipped == "" {
			t.Errorf("%s was carried out, and it is not flow's to carry out", id)
		}
		if len(skipped.Changes) != 0 {
			t.Errorf("%s changed something: %+v", id, skipped.Changes)
		}
	}
	if w.tracker.Calls("Move") != 0 {
		t.Error("apply moved a ticket")
	}
}

// TestStaleRefusesAPlanTheWorldMovedPast is spec 06 §3: a saved plan is a
// promise about a world, and applying it once that world has moved would make
// a change nobody approved.
func TestStaleRefusesAPlanTheWorldMovedPast(t *testing.T) {
	w := githubProjectsWorld(t)
	plan := planFor(t, w)

	if err := engine.Stale(plan, planFor(t, w)); err != nil {
		t.Fatalf("an unchanged world is stale: %v", err)
	}

	if _, err := w.tracker.Apply(context.Background(), core.Change{
		What: engine.ItemTrackerStatusOptions,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "Parked"},
	}); err != nil {
		t.Fatalf("change the board: %v", err)
	}

	err := engine.Stale(plan, planFor(t, w))
	if !errors.Is(err, engine.ErrStale) {
		t.Fatalf("the plan is %v, want it stale", err)
	}
	if !strings.Contains(err.Error(), engine.ItemTrackerStatusOptions) {
		t.Errorf("the refusal is %q, want it to name what moved", err)
	}
}

// TestApplyNeedsALockAndAClock refuses a run that could not tell the truth
// about what it did.
func TestApplyNeedsALockAndAClock(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   engine.ApplyInput
	}{
		{name: "no lock", in: engine.ApplyInput{Clock: coretest.NewClock(coretest.ContractNow)}},
		{name: "no clock", in: engine.ApplyInput{Lock: config.NewLock("v0.3.0")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := engine.Apply(context.Background(), tc.in); err == nil {
				t.Fatal("apply ran without being told what it needed")
			}
		})
	}
}

// refused is the reasons an item's changes were declined.
func refused(applied engine.AppliedItem) []string {
	var reasons []string
	for _, change := range applied.Changes {
		if change.Refused != "" {
			reasons = append(reasons, change.Refused)
		}
	}
	return reasons
}

// haltingTracker fails one call to Apply, so that a test can interrupt an
// apply part way through an item and ask what the lock says afterwards.
type haltingTracker struct {
	*coretest.FakeTracker
	failAt int
	calls  int
	err    error
}

func (t *haltingTracker) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	t.calls++
	if t.calls == t.failAt {
		return core.ApplyResult{}, t.err
	}
	return t.FakeTracker.Apply(ctx, c)
}
