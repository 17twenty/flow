package engine_test

import (
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// applyFollowUpWorld is an apply's half of the lifecycle: a tracker with
// nothing on it, a lock with no memory, and a clock that stands still.
type applyFollowUpWorld struct {
	tracker *coretest.FakeTracker
	lock    *config.Lock
	cfg     *config.Config
	clock   *coretest.FakeClock
}

func newApplyFollowUpWorld(t *testing.T) applyFollowUpWorld {
	t.Helper()
	fixture := coretest.ContractTracker()
	fixture.Tickets = nil
	clock := coretest.NewClock(coretest.ContractNow)
	fixture.Clock = clock
	return applyFollowUpWorld{
		tracker: coretest.NewFakeTracker(fixture),
		lock:    config.NewLock("v0.3.0"),
		cfg:     followUpConfig(t),
		clock:   clock,
	}
}

// account runs one round of apply's follow-up accounting over a single item.
func (w applyFollowUpWorld) account(t *testing.T, planned engine.PlanItem, item engine.AppliedItem) engine.FollowUpOutcome {
	t.Helper()
	outcomes := engine.ApplyFollowUps(t.Context(), engine.ApplyFollowUpInput{
		Applied: engine.Applied{Items: []engine.AppliedItem{item}},
		Plan:    engine.Plan{Items: []engine.PlanItem{planned}},
		Config:  w.cfg,
		Lock:    w.lock,
		Tracker: w.tracker,
		Repo:    "acme/widget",
		Clock:   w.clock,
		File:    true,
	})
	if len(outcomes) == 0 {
		return engine.FollowUpOutcome{Action: engine.FollowUpNone}
	}
	if len(outcomes) != 1 {
		t.Fatalf("one item accounted for %d follow-ups: %+v", len(outcomes), outcomes)
	}
	if outcomes[0].Err != nil {
		t.Fatalf("account for %s: %v", planned.ID, outcomes[0].Err)
	}
	return outcomes[0]
}

// refusedByTheForge is the plan's word on an item flow is not allowed to
// make true: the trunk's protection on a repository whose credential has
// push and not admin, which is this repository's own situation.
func refusedByTheForge() engine.PlanItem {
	return engine.PlanItem{
		ID:      core.ItemForgeProtection,
		Target:  engine.TargetForge,
		Outcome: engine.OutcomeFollowUp,
		Owner:   engine.OwnerForgeAdmin,
		Missing: []string{"admin"},
		Why:     "the credential is missing admin, so a person with those permissions has to do it",
		Changes: []core.Change{{
			Op:   core.ChangeUpdate,
			What: "forge.protection",
			Item: core.Item{ID: core.ItemForgeProtection, Name: "protection"},
		}},
	}
}

// neverAttempted is what apply records for an item the plan did not give it:
// nothing was tried, so nothing is known about whether the work is done.
func neverAttempted(planned engine.PlanItem) engine.AppliedItem {
	return engine.AppliedItem{
		ID:      planned.ID,
		Outcome: planned.Outcome,
		Skipped: "the plan does not have flow making this change: " + string(planned.Outcome),
	}
}

// TestWorkApplyNeverAttemptedIsNotWorkApplyVerified is the regression. An
// item the plan hands to a person is one apply steps over: it attempts
// nothing, so it records no changes. Reading that empty list as "there is
// nothing left to do" closed this repository's own `forge.protection` and
// `forge.required_status` tickets as *verified* while the trunk sat
// unprotected — a ticket closed by the one thing that promised to verify it.
func TestWorkApplyNeverAttemptedIsNotWorkApplyVerified(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	planned := refusedByTheForge()

	filed := w.account(t, planned, neverAttempted(planned))
	if filed.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want %q: an item nobody did is work somebody owes", filed.Action, engine.FollowUpFiled)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if ticket.Closed {
		t.Error("flow filed the ticket closed")
	}

	// The second run is the one that did the damage: the ticket is there,
	// and apply steps over the item again.
	again := w.account(t, planned, neverAttempted(planned))
	if again.Action == engine.FollowUpVerified {
		t.Fatal("apply verified work it never attempted")
	}
	after, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if after.Closed {
		t.Error("apply closed a ticket for work that is still owed")
	}
}

// The ticket apply files has to say what is owed. Apply made no changes, so
// the only account of the work is the plan's.
func TestASkippedItemsTicketCarriesThePlansChanges(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	planned := refusedByTheForge()

	filed := w.account(t, planned, neverAttempted(planned))
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if !strings.Contains(ticket.Body, `update forge.protection "protection"`) {
		t.Errorf("the ticket does not say what is owed:\n%s", ticket.Body)
	}
	if !strings.Contains(ticket.Body, "admin") {
		t.Errorf("the ticket does not say what access is short:\n%s", ticket.Body)
	}
}

// An item that is already true is also skipped, and that skip really does
// mean there is nothing left to do. Refusing to close those would leave a
// ticket open for ever, which is the failure the lifecycle exists to stop —
// so the fix must tell the two skips apart rather than distrust both.
func TestAnItemThatIsAlreadyTrueClosesItsTicket(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	owed := refusedByTheForge()
	filed := w.account(t, owed, neverAttempted(owed))
	if filed.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want %q", filed.Action, engine.FollowUpFiled)
	}

	settled := owed
	settled.Outcome = engine.OutcomeNoop
	settled.Changes = nil
	settled.Why = "the provider already matches what was asked for"

	out := w.account(t, settled, neverAttempted(settled))
	if out.Action != engine.FollowUpVerified {
		t.Fatalf("action = %q, want %q: the work is done and the ticket should close", out.Action, engine.FollowUpVerified)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if !ticket.Closed {
		t.Error("the ticket is still open for work that is now done")
	}
}

// A compensated item is enforced another way and an attested one is waiting
// on a person's word, not on a ticket. Neither is a thing apply owes anybody,
// so neither keeps a follow-up open.
func TestTheOtherSkipsAreSettledWork(t *testing.T) {
	t.Parallel()

	for _, outcome := range []engine.Outcome{
		engine.OutcomeCompensated,
		engine.OutcomeAttest,
		engine.OutcomeUnsupported,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			w := newApplyFollowUpWorld(t)
			planned := refusedByTheForge()
			planned.Outcome = outcome

			out := w.account(t, planned, neverAttempted(planned))
			if out.Action == engine.FollowUpFiled {
				t.Errorf("apply filed a ticket for %s work: %+v", outcome, out.Ticket)
			}
		})
	}
}

// An item apply carried out is done, and its ticket closes. This is the path
// the regression broke by accident: the test above makes the lifecycle
// distrust a skip, and this one holds it to still believing an apply.
func TestWorkApplyDidClosesItsTicket(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	owed := refusedByTheForge()
	filed := w.account(t, owed, neverAttempted(owed))

	done := owed
	done.Outcome = engine.OutcomeNative
	out := w.account(t, done, engine.AppliedItem{
		ID:      done.ID,
		Outcome: engine.OutcomeNative,
		Changes: []engine.AppliedChange{{Change: done.Changes[0], Note: "protected main"}},
	})
	if out.Action != engine.FollowUpVerified {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpVerified)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if !ticket.Closed {
		t.Error("the ticket is still open for work apply did")
	}
}

// A provider that refused a change apply attempted still owes it, and that
// is apply's own account rather than the plan's.
func TestAProviderRefusalKeepsTheTicketOpen(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	planned := refusedByTheForge()
	planned.Outcome = engine.OutcomeNative

	out := w.account(t, planned, engine.AppliedItem{
		ID:      planned.ID,
		Outcome: engine.OutcomeNative,
		Changes: []engine.AppliedChange{{
			Change:   planned.Changes[0],
			FollowUp: "the API will not set this on a repository with rulesets",
		}},
	})
	if out.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpFiled)
	}
	ticket, err := w.tracker.Ticket(t.Context(), out.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if !strings.Contains(ticket.Body, "will not set this on a repository with rulesets") {
		t.Errorf("the ticket does not carry the provider's reason:\n%s", ticket.Body)
	}
}

// A refused item flow could not even describe is still refused. The plan
// knows nothing to list — a provider that would not say what it holds leaves
// no diff — and a ticket with an empty "what to do" is a ticket that reads
// as nothing to do.
func TestARefusalWithNothingToListIsStillOwed(t *testing.T) {
	t.Parallel()

	w := newApplyFollowUpWorld(t)
	planned := refusedByTheForge()
	planned.Changes = nil

	out := w.account(t, planned, neverAttempted(planned))
	if out.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpFiled)
	}
	ticket, err := w.tracker.Ticket(t.Context(), out.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if strings.Contains(ticket.Body, "no steps to offer") {
		t.Errorf("the ticket asks for nothing:\n%s", ticket.Body)
	}
	if !strings.Contains(ticket.Body, core.ItemForgeProtection) {
		t.Errorf("the ticket does not even name the item:\n%s", ticket.Body)
	}
}
