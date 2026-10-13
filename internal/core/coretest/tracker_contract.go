package coretest

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// NewTracker builds the tracker under test from a fixture.
type NewTracker func(t *testing.T, fx TrackerFixture) core.Tracker

// TrackerContractCase is one named behaviour in the tracker contract. The
// cases are named and addressable so that an adapter still being built can be
// held to the part of the contract it already claims: a plan item lands a few
// methods at a time, and a suite that can only be run whole cannot be run at
// all until the last one (spec 14).
type TrackerContractCase struct {
	// Name is the subtest's name, and how a subset asks for the case.
	Name string
	// Run is the case itself.
	Run func(t *testing.T, newTracker NewTracker)
}

// TrackerContractCases returns every case of the tracker contract, in order.
func TrackerContractCases() []TrackerContractCase {
	return []TrackerContractCase{
		{Name: "identity", Run: trackerIdentity},
		{Name: "probe reports what the credential may do", Run: trackerProbe},
		{Name: "what is discovered can be parsed back", Run: trackerDiscover},
		{Name: "a hint narrows discovery", Run: trackerDiscoverHint},
		{Name: "nonsense is not a reference", Run: trackerParseRefNonsense},
		{Name: "an observation is canonical and stable", Run: trackerObserve},
		{Name: "observing something that is not there is not an error", Run: trackerObserveMissing},
		{Name: "applying a change is visible in the next observation", Run: trackerApplyCreate},
		{Name: "creating the same thing twice is a conflict", Run: trackerApplyConflict},
		{Name: "a thing is updated and then deleted", Run: trackerApplyUpdateDelete},
		{Name: "tickets by key, by column, and all of them", Run: trackerTickets},
		{Name: "a ticket that is not there", Run: trackerMissingTicket},
		{Name: "a move is idempotent", Run: trackerMove},
		{Name: "a move names a column the way the configuration does", Run: trackerMoveByName},
		{Name: "a follow-up is filed once and found by its marker", Run: trackerFollowUp},
		{Name: "a new follow-up carries its marker", Run: trackerNewFollowUp},
		{Name: "flow rewrites its own ticket and nobody else's", Run: trackerUpdateFollowUp},
		{Name: "a comment is written and edited in place", Run: trackerComment},
		{Name: "a ticket is closed, and closing it again is no work", Run: trackerClose},
		{Name: "a closed ticket is reopened", Run: trackerReopen},
		{Name: "linkage uses the forge's own link", Run: trackerLinkedKeys},
	}
}

// TrackerContract is every behaviour the engine is entitled to assume of a
// tracker: probing, discovery, canonical observation, idempotent moves, and
// follow-ups that are filed once (spec 11 §5).
func TrackerContract(t *testing.T, newTracker NewTracker) {
	t.Helper()
	for _, c := range TrackerContractCases() {
		t.Run(c.Name, func(t *testing.T) { c.Run(t, newTracker) })
	}
}

// TrackerContractSubset runs the named cases only. An unknown name fails the
// test rather than passing quietly: a subset naming a case that no longer
// exists would otherwise shrink to nothing without anybody noticing.
func TrackerContractSubset(t *testing.T, newTracker NewTracker, names ...string) {
	t.Helper()
	all := map[string]TrackerContractCase{}
	for _, c := range TrackerContractCases() {
		all[c.Name] = c
	}
	for _, name := range names {
		c, ok := all[name]
		if !ok {
			t.Fatalf("no tracker contract case is named %q", name)
		}
		t.Run(c.Name, func(t *testing.T) { c.Run(t, newTracker) })
	}
}

// trackerIdentity: a tracker names itself and declares every capability its
// kind has, because silence is not a finding (spec 05 §1).
func trackerIdentity(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	if tracker.Kind() == "" {
		t.Error("the tracker has no kind, so no capability declaration can be found for it")
	}
	capabilities := tracker.Capabilities()
	if capabilities.Kind != core.KindTracker {
		t.Errorf("Capabilities().Kind = %q, want %q", capabilities.Kind, core.KindTracker)
	}
	for _, name := range core.CapabilityNames(core.KindTracker) {
		if _, ok := capabilities.Capabilities[name]; !ok {
			t.Errorf("the declaration says nothing about %q; silence is not a finding", name)
		}
	}
}

// trackerProbe: a credential the fixture grants everything to comes back with
// something, so a resolution has tokens to compare against (spec 05 §4).
func trackerProbe(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	permissions, err := tracker.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(permissions.Tokens) == 0 {
		t.Error("Probe reported no permissions for a credential the fixture grants everything to")
	}
}

// trackerDiscover: everything discovery offers can be recorded, shown, and —
// where it has an address — parsed back into the same id (spec 07 §3).
func trackerDiscover(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	candidates, err := tracker.Discover(t.Context(), "tracker.project", nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("got %d candidates, want the fixture's two projects", len(candidates))
	}
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.Name == "" {
			t.Errorf("candidate %+v cannot be recorded or shown", candidate)
		}
		if candidate.URL == "" {
			continue
		}
		ref, err := tracker.ParseRef(candidate.URL)
		if err != nil {
			t.Errorf("ParseRef(%q) failed for an address the tracker itself offered: %v", candidate.URL, err)
			continue
		}
		if ref.ID != candidate.ID {
			t.Errorf("ParseRef(%q).ID = %q, want %q", candidate.URL, ref.ID, candidate.ID)
		}
	}
}

// trackerDiscoverHint: a hint narrows the candidates rather than reordering
// them, so setup can be driven from what configuration already knows.
func trackerDiscoverHint(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	candidates, err := tracker.Discover(t.Context(), "tracker.project", map[string]string{"name": "Delivery"})
	if err != nil {
		t.Fatalf("Discover with a hint: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.Name != "Delivery" {
			t.Errorf("a hint of Delivery still offered %q", candidate.Name)
		}
	}
}

// trackerParseRefNonsense: a reference flow cannot read is the user's to fix,
// so it is ErrValidation and not a provider failure.
func trackerParseRefNonsense(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	if _, err := tracker.ParseRef("the delivery board, you know the one"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("ParseRef of nonsense = %v, want it to wrap ErrValidation", err)
	}
}

// trackerObserve: an observation is canonical and stable, because spec 06 §2
// diffs and hashes it and the provider's own order is nobody's contract.
func trackerObserve(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	first, err := tracker.Observe(t.Context(), "tracker.status_options")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !first.Exists || len(first.Items) == 0 {
		t.Fatalf("Observe(status_options) = %+v, want the fixture's columns", first)
	}
	if !slices.IsSortedFunc(first.Items, func(a, b core.Item) int { return compareItems(a, b) }) {
		t.Errorf("the items came back in the provider's order, not a canonical one: %+v", first.Items)
	}
	second, err := tracker.Observe(t.Context(), "tracker.status_options")
	if err != nil {
		t.Fatalf("Observe again: %v", err)
	}
	if !sameObservation(first, second) {
		t.Errorf("observing twice gave different answers:\n%+v\n%+v", first, second)
	}
	if _, ok := first.Find("In review"); !ok {
		t.Error("the observation has no In review column")
	}
}

// trackerObserveMissing: something the tracker does not hold is an observation
// that does not exist, not an error — that is the plan's "create" case.
func trackerObserveMissing(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	observation, err := tracker.Observe(t.Context(), "tracker.expedite")
	if err != nil {
		t.Fatalf("Observe of an unconfigured thing: %v", err)
	}
	if observation.Exists {
		t.Error("the tracker claims to hold something the fixture never gave it")
	}
}

// trackerApplyCreate: a change is visible in the next observation, carries an
// id the lock can record, and says what it did.
func trackerApplyCreate(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	change := core.Change{
		What: "tracker.labels",
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "incident", Fields: map[string]string{"color": "d93f0b"}},
	}
	result, err := tracker.Apply(t.Context(), change)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Item.ID == "" {
		t.Error("the created item has no id, so the lock cannot record it")
	}
	if result.Note == "" {
		t.Error("the result explains nothing, so the log cannot say what happened")
	}

	observation, err := tracker.Observe(t.Context(), "tracker.labels")
	if err != nil {
		t.Fatalf("Observe after Apply: %v", err)
	}
	item, ok := observation.Find("incident")
	if !ok {
		t.Fatalf("the created label is not there: %+v", observation.Items)
	}
	if item.Field("color") != "d93f0b" {
		t.Errorf("the created label is %+v, want the colour it was given", item)
	}
}

// trackerApplyConflict: creating something that is already there is
// ErrConflict, which is what tells apply to update instead.
func trackerApplyConflict(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	change := core.Change{
		What: "tracker.labels",
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "blocked", Fields: map[string]string{"color": "b60205"}},
	}
	if _, err := tracker.Apply(t.Context(), change); !errors.Is(err, core.ErrConflict) {
		t.Errorf("creating a label that is already there = %v, want ErrConflict", err)
	}
}

// trackerApplyUpdateDelete: a thing is updated in place and then deleted, and
// deleting it twice is ErrNotFound.
func trackerApplyUpdateDelete(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	before, err := tracker.Observe(t.Context(), "tracker.labels")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	existing, ok := before.Find("blocked")
	if !ok {
		t.Fatal("the fixture's blocked label is missing")
	}

	updated := existing.WithField("color", "000000")
	if _, err := tracker.Apply(t.Context(), core.Change{What: "tracker.labels", Op: core.ChangeUpdate, Item: updated}); err != nil {
		t.Fatalf("Apply(update): %v", err)
	}
	after, err := tracker.Observe(t.Context(), "tracker.labels")
	if err != nil {
		t.Fatalf("Observe after the update: %v", err)
	}
	if item, _ := after.Find("blocked"); item.Field("color") != "000000" {
		t.Errorf("the label is %+v, want the colour it was updated to", item)
	}

	if _, err := tracker.Apply(t.Context(), core.Change{What: "tracker.labels", Op: core.ChangeDelete, Item: updated}); err != nil {
		t.Fatalf("Apply(delete): %v", err)
	}
	gone, err := tracker.Observe(t.Context(), "tracker.labels")
	if err != nil {
		t.Fatalf("Observe after the delete: %v", err)
	}
	if _, ok := gone.Find("blocked"); ok {
		t.Error("the label survived being deleted")
	}

	if _, err := tracker.Apply(t.Context(), core.Change{What: "tracker.labels", Op: core.ChangeDelete, Item: updated}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleting it again = %v, want ErrNotFound", err)
	}
}

// trackerTickets: tickets by key, by column, in a window, and all of them —
// which for a real adapter means every page of them.
func trackerTickets(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	ticket, err := tracker.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket(#10): %v", err)
	}
	if ticket.Title == "" || ticket.ColumnID != ColumnInProgress {
		t.Errorf("Ticket(#10) = %+v, want the in-progress ticket", ticket)
	}
	if ticket.Parent == "" {
		t.Error("the ticket has no parent, and the Definition of Ready needs one")
	}

	byKey, err := tracker.Tickets(t.Context(), core.TicketQuery{Keys: []string{"#10", "#12"}})
	if err != nil {
		t.Fatalf("Tickets by key: %v", err)
	}
	if len(byKey) != 2 {
		t.Errorf("got %d tickets for two keys", len(byKey))
	}

	byColumn, err := tracker.Tickets(t.Context(), core.TicketQuery{Column: ColumnTodo})
	if err != nil {
		t.Fatalf("Tickets by column: %v", err)
	}
	if len(byColumn) == 0 {
		t.Fatal("no tickets in Todo, but the fixture puts two there")
	}
	for _, ticket := range byColumn {
		if ticket.ColumnID != ColumnTodo {
			t.Errorf("a query for Todo returned %s, which is in %s", ticket.Key, ticket.ColumnID)
		}
	}

	// Every ticket, which for a real adapter means every page of them.
	all, err := tracker.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(all) != len(ContractTracker().Tickets) {
		t.Errorf("got %d tickets, want all %d: a page was dropped", len(all), len(ContractTracker().Tickets))
	}

	window, err := tracker.Tickets(t.Context(), core.TicketQuery{UpdatedSince: ContractNow.Add(-24*time.Hour - time.Minute)})
	if err != nil {
		t.Fatalf("Tickets in a window: %v", err)
	}
	for _, ticket := range window {
		if ticket.UpdatedAt.Before(ContractNow.Add(-25 * time.Hour)) {
			t.Errorf("%s was updated at %s, outside the window asked for", ticket.Key, ticket.UpdatedAt)
		}
	}
}

// trackerMissingTicket: a key the board does not hold is ErrNotFound, whether
// it is read or moved.
func trackerMissingTicket(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	if _, err := tracker.Ticket(t.Context(), "#4040"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Ticket(#4040) = %v, want it to wrap ErrNotFound", err)
	}
	if err := tracker.Move(t.Context(), "#4040", ColumnDone); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Move(#4040) = %v, want it to wrap ErrNotFound", err)
	}
}

// trackerMove: a move lands the ticket in the column with a name a human would
// recognise, and moving it where it already is changes nothing at all.
func trackerMove(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	if err := tracker.Move(t.Context(), "#10", ColumnInReview); err != nil {
		t.Fatalf("Move: %v", err)
	}
	moved, err := tracker.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket after Move: %v", err)
	}
	if moved.ColumnID != ColumnInReview {
		t.Fatalf("the ticket is in %s, want %s", moved.ColumnID, ColumnInReview)
	}
	if moved.Status.Name == "" {
		t.Error("the ticket's status has no name after a move")
	}

	if err := tracker.Move(t.Context(), "#10", ColumnInReview); err != nil {
		t.Fatalf("moving a ticket to the column it is already in failed: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket after the second Move: %v", err)
	}
	if again.ColumnID != moved.ColumnID || !again.UpdatedAt.Equal(moved.UpdatedAt) {
		t.Errorf("the second move changed the ticket: %+v then %+v", moved, again)
	}
}

// trackerMoveByName: a transition names its target the way the configuration
// does — by the column's display name — because that is the only name flow
// has. `workflow.columns` is written by hand (spec 04 §2) and a column flow
// never created has no tracker id anywhere in the lock, so an adapter that
// understood only its own ids would make every configured transition
// unperformable. The ticket still comes back carrying the tracker's id.
func trackerMoveByName(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	name := ContractTracker().Columns[ColumnInReview]
	if err := tracker.Move(t.Context(), "#10", core.ColumnID(name)); err != nil {
		t.Fatalf("Move(%q): %v", name, err)
	}
	moved, err := tracker.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket after Move: %v", err)
	}
	if moved.ColumnID != ColumnInReview {
		t.Fatalf("moving to %q left the ticket in %s, want %s", name, moved.ColumnID, ColumnInReview)
	}
	if err := tracker.Move(t.Context(), "#10", core.ColumnID(name)); err != nil {
		t.Fatalf("moving by name to the column it is already in failed: %v", err)
	}

	err = tracker.Move(t.Context(), "#10", "Pending triage")
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("Move to a column the board does not have = %v, want it to wrap ErrValidation", err)
	}
}

// trackerFollowUp: a follow-up is filed once and found by its marker, and a
// search naming fewer fields still finds it (spec 11 §1).
func trackerFollowUp(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	marker := ContractFollowUpMarker()

	found, err := tracker.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil {
		t.Fatal("FindFollowUp missed the ticket the fixture marked")
	}

	// A search naming fewer fields must still find it (spec 11 §1).
	broad, err := tracker.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"))
	if err != nil {
		t.Fatalf("FindFollowUp with fewer fields: %v", err)
	}
	if broad == nil || broad.Key != found.Key {
		t.Errorf("a broader search found %v, want %s", broad, found.Key)
	}

	again, err := tracker.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: marker,
		Title:  "flow: set a WIP limit on In review (acme/widget)",
		Body:   "The tracker cannot enforce it.",
		Labels: []string{"flow-followup"},
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	if again.Key != found.Key {
		t.Errorf("filing the same follow-up again opened %s as well as %s", again.Key, found.Key)
	}

	missing, err := tracker.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "nothing.at.all"))
	if err != nil {
		t.Fatalf("FindFollowUp for a check that filed nothing: %v", err)
	}
	if missing != nil {
		t.Errorf("FindFollowUp invented %s", missing.Key)
	}
}

// trackerNewFollowUp: a ticket flow has just filed can be referred to and
// found again, which is what stops the next run filing a second one.
func trackerNewFollowUp(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget")
	filed, err := tracker.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: marker,
		Title:  "flow: add an expedite view (acme/widget)",
		Body:   "Why, and how flow will check.",
		Labels: []string{"flow-followup"},
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	if filed.Key == "" || filed.URL == "" {
		t.Errorf("the filed ticket cannot be referred to: %+v", filed)
	}
	found, err := tracker.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil || found.Key != filed.Key {
		t.Errorf("flow could not find the ticket it had just filed")
	}
}

// trackerUpdateFollowUp: the body of flow's own ticket is rewritten in place,
// the marker survives the rewrite, and a ticket a person wrote is refused.
func trackerUpdateFollowUp(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	marker := ContractFollowUpMarker()

	if err := tracker.UpdateFollowUp(t.Context(), "#13", marker,
		"flow: the trunk is still unprotected", "Still not enforced, three days on."); err != nil {
		t.Fatalf("UpdateFollowUp: %v", err)
	}
	after, err := tracker.Ticket(t.Context(), "#13")
	if err != nil {
		t.Fatalf("Ticket after the rewrite: %v", err)
	}
	if !strings.Contains(after.Body, "three days on") {
		t.Errorf("the rewritten body is %q, which does not say what flow wrote", after.Body)
	}
	// The title travels with the body. A follow-up filed before flow could
	// say what the work was is titled after a summary that has since
	// improved, and a stale title is what a person reads in the issue list.
	if after.Title != "flow: the trunk is still unprotected" {
		t.Errorf("the title is %q, which is not what flow rewrote it to", after.Title)
	}
	// Without the marker the next run would file a second ticket instead of
	// finding this one (spec 11 §1).
	if !core.HasMarker(after.Body, marker) {
		t.Errorf("the rewrite dropped the marker: %q", after.Body)
	}

	// Rewriting it to what it already says is no work. The pinned health
	// report is rewritten every morning (spec 07 §10), and a report that
	// sent identical bytes would move the moment the issue last changed and
	// make a quiet fortnight look like news every day.
	if err := tracker.UpdateFollowUp(t.Context(), "#13", marker,
		"flow: the trunk is still unprotected", "Still not enforced, three days on."); err != nil {
		t.Errorf("UpdateFollowUp with nothing to change: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), "#13")
	if err != nil {
		t.Fatalf("Ticket after the second rewrite: %v", err)
	}
	if !again.UpdatedAt.Equal(after.UpdatedAt) {
		t.Errorf("updated at %v then %v: rewriting a body to what it already says moved the date",
			after.UpdatedAt, again.UpdatedAt)
	}

	// The title is not a thing the body decides. A summary can improve
	// without a word of the body changing, and the title is the whole of
	// what a person reads in the issue list.
	if err := tracker.UpdateFollowUp(t.Context(), "#13", marker,
		"flow: the trunk takes anything anyone pushes", "Still not enforced, three days on."); err != nil {
		t.Fatalf("UpdateFollowUp with only the title to change: %v", err)
	}
	retitled, err := tracker.Ticket(t.Context(), "#13")
	if err != nil {
		t.Fatalf("Ticket after the retitle: %v", err)
	}
	if retitled.Title != "flow: the trunk takes anything anyone pushes" {
		t.Errorf("the title is %q: a rewrite that only changed the title did nothing", retitled.Title)
	}

	// #12 is a person's ticket. flow edits only its own writing.
	if err := tracker.UpdateFollowUp(t.Context(), "#12", marker, "flow took over", "flow was here"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("rewriting a ticket flow did not open = %v, want ErrValidation", err)
	}
	theirs, err := tracker.Ticket(t.Context(), "#12")
	if err != nil {
		t.Fatalf("Ticket #12: %v", err)
	}
	if strings.Contains(theirs.Body, "flow was here") {
		t.Errorf("flow rewrote somebody else's ticket anyway: %q", theirs.Body)
	}
	if theirs.Title == "flow took over" {
		t.Errorf("flow retitled somebody else's ticket anyway: %q", theirs.Title)
	}

	if err := tracker.UpdateFollowUp(t.Context(), "#4040", marker, "nobody", "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("rewriting a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerComment: a marked comment is written and then edited in place, and a
// ticket that is not there is ErrNotFound.
func trackerComment(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
	if err := tracker.Comment(t.Context(), "#13", marker, "still not done"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := tracker.Comment(t.Context(), "#13", marker, "still not done, a week on"); err != nil {
		t.Fatalf("Comment again: %v", err)
	}
	if err := tracker.Comment(t.Context(), "#4040", marker, "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("commenting on a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerClose: a ticket is closed with a reason, and closing it again does not
// move the moment the work was finished.
func trackerClose(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	if err := tracker.Close(t.Context(), "#13", core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := tracker.Ticket(t.Context(), "#13")
	if err != nil {
		t.Fatalf("Ticket after Close: %v", err)
	}
	if !closed.Closed {
		t.Fatal("the ticket is still open after being closed")
	}
	if closed.CloseReason != core.ClosedCompleted {
		t.Errorf("close reason = %q, want %q", closed.CloseReason, core.ClosedCompleted)
	}
	if closed.ClosedAt.IsZero() {
		t.Error("a closed ticket has no closed-at time")
	}
	// Idempotent: doctor runs daily, and the second run must not move the
	// moment the work was finished.
	if err := tracker.Close(t.Context(), "#13", core.ClosedCompleted); err != nil {
		t.Fatalf("Close again: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), "#13")
	if err != nil {
		t.Fatalf("Ticket after the second Close: %v", err)
	}
	if !again.ClosedAt.Equal(closed.ClosedAt) {
		t.Errorf("closed at %v then %v: closing a closed ticket moved the date", closed.ClosedAt, again.ClosedAt)
	}
	if err := tracker.Close(t.Context(), "#4040", core.ClosedCompleted); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("closing a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerReopen: a closed ticket is reopened, and reopening one that is not
// there is ErrNotFound.
func trackerReopen(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	before, err := tracker.Ticket(t.Context(), "#11")
	if err != nil {
		t.Fatalf("Ticket(#11): %v", err)
	}
	if !before.Closed {
		t.Fatal("the fixture's #11 should be closed")
	}
	if err := tracker.Reopen(t.Context(), "#11", "flow could not verify it was done"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	after, err := tracker.Ticket(t.Context(), "#11")
	if err != nil {
		t.Fatalf("Ticket after Reopen: %v", err)
	}
	if after.Closed {
		t.Error("the ticket is still closed after being reopened")
	}
	if err := tracker.Reopen(t.Context(), "#4040", "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("reopening a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerLinkedKeys: linkage comes from the forge's own structured link, and a
// pull request that names nothing links to nothing (P12, spec 04 §3).
func trackerLinkedKeys(t *testing.T, newTracker NewTracker) {
	tracker := newTracker(t, ContractTracker())
	pr := ContractForge().PullRequests[1]
	if keys := tracker.LinkedKeys(pr); !slices.Contains(keys, "#11") {
		t.Errorf("LinkedKeys = %v, want the issue the forge says it closes", keys)
	}
	if keys := tracker.LinkedKeys(core.PullRequest{Number: 99}); len(keys) != 0 {
		t.Errorf("LinkedKeys of an unlinked pull request = %v, want none: flow never guesses", keys)
	}
}

// compareItems is the canonical order of items: by name, then by id.
func compareItems(a, b core.Item) int {
	if a.Name != b.Name {
		if a.Name < b.Name {
			return -1
		}
		return 1
	}
	switch {
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}

// sameObservation reports whether two observations say the same thing.
func sameObservation(a, b core.Observation) bool {
	if a.What != b.What || a.Exists != b.Exists || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if !a.Items[i].Equal(b.Items[i]) || a.Items[i].ID != b.Items[i].ID {
			return false
		}
	}
	return true
}
