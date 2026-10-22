package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// What the gate says it did, as against what it worked out it should do
// (issue #192). A run that keeps one field for both will sooner or later
// report an act it never performed, and this repository's own gate did:
// it printed a ticket moving every time it decided one should, whether the
// board had taken the move or not.

// TestAMoveNobodyMadeIsNotReportedAsAMoveThatHappened: the credential was
// refused the status, so the board is left alone — and the run has to go on
// saying where the ticket would have gone without claiming it went there.
func TestAMoveNobodyMadeIsNotReportedAsAMoveThatHappened(t *testing.T) {
	world := gateFixture(t)
	world.forge.Fail("SetCommitStatus", core.Errorf(core.ErrPermission, "set a status").WithWhy("read-only"))

	gate := world.run(t)
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Fatalf("a read-only run made %d moves", moves)
	}
	move := onlyMove(t, gate)
	if !move.Moves() {
		t.Fatalf("the gate decided %+v, want the move it would have made", move)
	}
	if move.Outcome != engine.GateReadOnly {
		t.Errorf("the move's outcome is %q, want %q", move.Outcome, engine.GateReadOnly)
	}
	text := gate.Text()
	if !strings.Contains(text, "not made: this credential may not write to the board") {
		t.Errorf("the run says %q, and nowhere in it that the move did not happen", text)
	}
	if strings.Contains(text, "→ #12") {
		t.Errorf("the run says %q, which is the arrow it prints for a move the board took", text)
	}
}

// TestAMoveTheBoardRefusedIsReportedAsRefused: the other way round. The forge
// let flow write and the tracker would not, which is a credential holding one
// of the two it needs — and the only way anybody learns that is the run.
func TestAMoveTheBoardRefusedIsReportedAsRefused(t *testing.T) {
	world := gateFixture(t)
	world.tracker.Fail("Move", core.Errorf(core.ErrPermission, "move a card").WithWhy("read-only"))

	gate := world.run(t)
	if gate.StatusOutcome != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q", gate.StatusOutcome, engine.GatePosted)
	}
	if move := onlyMove(t, gate); move.Outcome != engine.GateReadOnly {
		t.Errorf("the move's outcome is %q, want %q", move.Outcome, engine.GateReadOnly)
	}
	if text := gate.Text(); strings.Contains(text, "→ #12") {
		t.Errorf("the run says %q, which claims a move the board refused", text)
	}
}

// TestADryRunSaysWhichMoveItDidNotMake: --dry-run is the one case where
// reporting a decision is the whole point, and it still may not be reported
// as an act.
func TestADryRunSaysWhichMoveItDidNotMake(t *testing.T) {
	world := gateFixture(t)

	gate := world.run(t, func(in *engine.GateInput) { in.DryRun = true })
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Fatalf("a dry run made %d moves", moves)
	}
	if move := onlyMove(t, gate); move.Outcome != engine.GateDryRun {
		t.Errorf("the move's outcome is %q, want %q", move.Outcome, engine.GateDryRun)
	}
	text := gate.Text()
	if !strings.Contains(text, "ready → in_review (pr_ready_for_review) not made: dry run") {
		t.Errorf("the run says %q, and nowhere in it the move it decided on and did not make", text)
	}
}

// TestACommentRefusedDoesNotDisownTheStatus is the shape that hid the arming
// permission: a late write refused, an early one reported as refused with it.
// The comment is the one that cannot be written here, and the status went out.
func TestACommentRefusedDoesNotDisownTheStatus(t *testing.T) {
	world := gateFixture(t)
	world.tracker.SetTicket(unreadyTicket("#12", "Ready")) // the gate fails, and so wants a comment
	world.forge.Fail("UpsertComment", core.Errorf(core.ErrPermission, "comment").WithWhy("read-only"))

	text := world.run(t).Text()
	if !strings.Contains(text, "comment not written: this credential may not comment here") {
		t.Errorf("the run says %q, and nowhere in it that the comment was refused", text)
	}
	if strings.Contains(text, "status not posted") {
		t.Errorf("the run says %q, which is a report of something that did not happen", text)
	}
}

// TestTheJSONDocumentCarriesWhatBecameOfEachMove: a machine reading the gate
// gets the same two facts a person does, and in one field each.
func TestTheJSONDocumentCarriesWhatBecameOfEachMove(t *testing.T) {
	world := gateFixture(t)

	raw, err := world.run(t).JSON()
	if err != nil {
		t.Fatalf("render the document: %v", err)
	}
	var doc struct {
		Moves []struct {
			Ticket  string `json:"ticket"`
			To      string `json:"to"`
			Outcome string `json:"outcome"`
		} `json:"moves"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("read the document back: %v", err)
	}
	if len(doc.Moves) != 1 {
		t.Fatalf("the document holds %d moves, want the one the gate made: %s", len(doc.Moves), raw)
	}
	if doc.Moves[0].To == "" || doc.Moves[0].Outcome != engine.GatePosted {
		t.Errorf("the document says %+v, want the decision and %q beside it", doc.Moves[0], engine.GatePosted)
	}
}

// onlyMove is the single transition the fixture's one linked ticket implies.
func onlyMove(t *testing.T, gate engine.Gate) engine.GateMove {
	t.Helper()
	if len(gate.Moves) != 1 {
		t.Fatalf("the gate considered %d tickets, want the one the pull request closes", len(gate.Moves))
	}
	return gate.Moves[0]
}
