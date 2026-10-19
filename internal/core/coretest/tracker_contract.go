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
type NewTracker func(t *testing.T, fx *TrackerFixture) core.Tracker

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
		{Name: "an archived item is off the board and takes no change", Run: trackerArchived},
		{Name: "a move is idempotent", Run: trackerMove},
		{Name: "a move names a column the way the configuration does", Run: trackerMoveByName},
		{Name: "a follow-up is filed once and found by its marker", Run: trackerFollowUp},
		{Name: "a new follow-up carries its marker", Run: trackerNewFollowUp},
		{Name: "flow rewrites its own ticket and nobody else's", Run: trackerUpdateFollowUp},
		{Name: "a comment is written and edited in place", Run: trackerComment},
		{Name: "every comment on a ticket can be read back", Run: trackerComments},
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if _, err := tracker.ParseRef("the delivery board, you know the one"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("ParseRef of nonsense = %v, want it to wrap ErrValidation", err)
	}
}

// trackerObserve: an observation is canonical and stable, because spec 06 §2
// diffs and hashes it and the provider's own order is nobody's contract.
func trackerObserve(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if fx.ReadOnly {
		trackerRefusesEveryChange(t, tracker)
		return
	}
	write := fx.Write()
	change := core.Change{What: write.What, Op: core.ChangeCreate, Item: write.Create}
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

	observation, err := tracker.Observe(t.Context(), write.What)
	if err != nil {
		t.Fatalf("Observe after Apply: %v", err)
	}
	item, ok := observation.Find(write.Create.Name)
	if !ok {
		t.Fatalf("the created %s is not there: %+v", write.What, observation.Items)
	}
	for field, want := range write.Create.Fields {
		if item.Field(field) != want {
			t.Errorf("the created item is %+v, want %s = %q", item, field, want)
		}
	}
}

// trackerApplyConflict: creating something that is already there is
// ErrConflict, which is what tells apply to update instead.
func trackerApplyConflict(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if fx.ReadOnly {
		return
	}
	write := fx.Write()
	existing, err := tracker.Observe(t.Context(), write.What)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	already, ok := existing.Find(write.Existing)
	if !ok {
		t.Fatalf("the fixture has no %s called %q", write.What, write.Existing)
	}
	change := core.Change{What: write.What, Op: core.ChangeCreate, Item: core.Item{Name: already.Name, Fields: already.Fields}}
	if _, err := tracker.Apply(t.Context(), change); !errors.Is(err, core.ErrConflict) {
		t.Errorf("creating a %s that is already there = %v, want ErrConflict", write.What, err)
	}
}

// trackerApplyUpdateDelete: a thing is updated in place and then deleted, and
// deleting it twice is ErrNotFound.
func trackerApplyUpdateDelete(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if fx.ReadOnly {
		return
	}
	write := fx.Write()
	before, err := tracker.Observe(t.Context(), write.What)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	existing, ok := before.Find(write.Existing)
	if !ok {
		t.Fatalf("the fixture's %q is missing from %s", write.Existing, write.What)
	}

	updated := existing.WithField(write.Field, write.Value)
	if _, err := tracker.Apply(t.Context(), core.Change{What: write.What, Op: core.ChangeUpdate, Item: updated}); err != nil {
		t.Fatalf("Apply(update): %v", err)
	}
	after, err := tracker.Observe(t.Context(), write.What)
	if err != nil {
		t.Fatalf("Observe after the update: %v", err)
	}
	if item, _ := after.Find(write.Existing); item.Field(write.Field) != write.Value {
		t.Errorf("the item is %+v, want %s = %q", item, write.Field, write.Value)
	}

	if _, err := tracker.Apply(t.Context(), core.Change{What: write.What, Op: core.ChangeDelete, Item: updated}); err != nil {
		t.Fatalf("Apply(delete): %v", err)
	}
	gone, err := tracker.Observe(t.Context(), write.What)
	if err != nil {
		t.Fatalf("Observe after the delete: %v", err)
	}
	if _, ok := gone.Find(write.Existing); ok {
		t.Errorf("the %s survived being deleted", write.Existing)
	}

	if _, err := tracker.Apply(t.Context(), core.Change{What: write.What, Op: core.ChangeDelete, Item: updated}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleting it again = %v, want ErrNotFound", err)
	}
}

// trackerRefusesEveryChange holds a tracker that sets no desired state to
// refusing one, and to saying so in its declaration. A tracker whose board is
// the team's to configure is a finding and not a silence: the engine reads the
// declaration to decide whether to ask at all (spec 05 §1, §4), and a
// declaration that claims a write the adapter refuses would make a plan
// propose work that can never land.
func trackerRefusesEveryChange(t *testing.T, tracker core.Tracker) {
	t.Helper()
	capabilities := tracker.Capabilities()
	for item, capability := range map[string]string{
		core.ItemTrackerProject:       core.CapProject,
		core.ItemTrackerStatusOptions: core.CapStatusOptions,
		core.ItemTrackerLabels:        core.CapLabels,
	} {
		if capabilities.Can(capability) {
			t.Errorf("the declaration says %s can be set, and the adapter refuses to set it", capability)
		}
		change := core.Change{
			What: item,
			Op:   core.ChangeCreate,
			Item: core.Item{Name: "whatever the configuration wants"},
		}
		if _, err := tracker.Apply(t.Context(), change); !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("Apply(create %s) = %v, want ErrUnsupported", item, err)
		}
	}
}

// trackerTickets: tickets by key, by column, in a window, and all of them —
// which for a real adapter means every page of them.
func trackerTickets(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	ticket, err := tracker.Ticket(t.Context(), fx.Key("#10"))
	if err != nil {
		t.Fatalf("Ticket(#10): %v", err)
	}
	if ticket.Title == "" || ticket.ColumnID != ColumnInProgress {
		t.Errorf("Ticket(#10) = %+v, want the in-progress ticket", ticket)
	}
	if ticket.Parent == "" {
		t.Error("the ticket has no parent, and the Definition of Ready needs one")
	}

	byKey, err := tracker.Tickets(t.Context(), core.TicketQuery{Keys: []string{fx.Key("#10"), fx.Key("#12")}})
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

	// Every ticket, which for a real adapter means every page of them —
	// except the archived one, which is not on the board at all.
	all, err := tracker.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if want := len(onTheBoard()); len(all) != want {
		t.Errorf("got %d tickets, want all %d: a page was dropped", len(all), want)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if _, err := tracker.Ticket(t.Context(), fx.Key("#4040")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Ticket(#4040) = %v, want it to wrap ErrNotFound", err)
	}
	if err := tracker.Move(t.Context(), fx.Key("#4040"), ColumnDone); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Move(#4040) = %v, want it to wrap ErrNotFound", err)
	}
}

// trackerArchived: an archived item is off the board, still reachable by key,
// and refuses every change.
//
// Archiving is how a board stops showing work without closing it or throwing
// it away, and the two readings of the same item are meant to disagree: the
// listing is the board, so it leaves the item out, while the issue still has
// the item behind it and reports the column it was archived in. A move is
// refused rather than quietly dropped, because a tracker that accepted one
// would have flow report a move the board never made (spec 11 §5).
func trackerArchived(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	archived, err := tracker.Ticket(t.Context(), fx.Key(ContractArchivedKey))
	if err != nil {
		t.Fatalf("Ticket(%s): %v", ContractArchivedKey, err)
	}
	if !archived.Archived {
		t.Errorf("Ticket(%s).Archived is false, so nothing downstream can tell it is off the board", ContractArchivedKey)
	}
	if archived.ColumnID != ColumnInReview {
		t.Errorf("Ticket(%s).ColumnID = %q, want %q: archiving does not take an item out of its column",
			ContractArchivedKey, archived.ColumnID, ColumnInReview)
	}

	byKey, err := tracker.Tickets(t.Context(), core.TicketQuery{Keys: []string{fx.Key(ContractArchivedKey)}})
	if err != nil {
		t.Fatalf("Tickets by key: %v", err)
	}
	if len(byKey) != 1 {
		t.Errorf("asking for %s by key returned %d tickets, want the archived one: a key is a question about an issue, not about the board",
			ContractArchivedKey, len(byKey))
	}

	for _, q := range []core.TicketQuery{{}, {Column: ColumnInReview}} {
		listing, err := tracker.Tickets(t.Context(), q)
		if err != nil {
			t.Fatalf("Tickets(%+v): %v", q, err)
		}
		for _, ticket := range listing {
			if sameKey(ticket.Key, fx.Key(ContractArchivedKey)) {
				t.Errorf("Tickets(%+v) reported %s, which is archived and not on the board", q, ticket.Key)
			}
		}
	}

	err = tracker.Move(t.Context(), fx.Key(ContractArchivedKey), ColumnDone)
	if !errors.Is(err, core.ErrConflict) {
		t.Errorf("Move of an archived item = %v, want it to wrap ErrConflict: the item takes no change", err)
	}
	var user *core.UserError
	if errors.As(err, &user) && !strings.Contains(user.Why, "archiv") {
		t.Errorf("the refusal explains itself as %q, want it to name the archiving: a person reading it has one thing to undo", user.Why)
	}
	after, err := tracker.Ticket(t.Context(), fx.Key(ContractArchivedKey))
	if err != nil {
		t.Fatalf("Ticket after a refused Move: %v", err)
	}
	if after.ColumnID != ColumnInReview {
		t.Errorf("the archived item is now in %q: a refused move wrote to the board anyway", after.ColumnID)
	}
}

// sameKey compares two ticket keys the way people write them, which is
// sometimes with the # and sometimes without.
func sameKey(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(a, "#"), strings.TrimPrefix(b, "#"))
}

// trackerMove: a move lands the ticket in the column with a name a human would
// recognise, and moving it where it already is changes nothing at all.
func trackerMove(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if err := tracker.Move(t.Context(), fx.Key("#10"), ColumnInReview); err != nil {
		t.Fatalf("Move: %v", err)
	}
	moved, err := tracker.Ticket(t.Context(), fx.Key("#10"))
	if err != nil {
		t.Fatalf("Ticket after Move: %v", err)
	}
	if moved.ColumnID != ColumnInReview {
		t.Fatalf("the ticket is in %s, want %s", moved.ColumnID, ColumnInReview)
	}
	if moved.Status.Name == "" {
		t.Error("the ticket's status has no name after a move")
	}

	if err := tracker.Move(t.Context(), fx.Key("#10"), ColumnInReview); err != nil {
		t.Fatalf("moving a ticket to the column it is already in failed: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), fx.Key("#10"))
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	name := ContractTracker().Columns[ColumnInReview]
	if err := tracker.Move(t.Context(), fx.Key("#10"), core.ColumnID(name)); err != nil {
		t.Fatalf("Move(%q): %v", name, err)
	}
	moved, err := tracker.Ticket(t.Context(), fx.Key("#10"))
	if err != nil {
		t.Fatalf("Ticket after Move: %v", err)
	}
	if moved.ColumnID != ColumnInReview {
		t.Fatalf("moving to %q left the ticket in %s, want %s", name, moved.ColumnID, ColumnInReview)
	}
	if err := tracker.Move(t.Context(), fx.Key("#10"), core.ColumnID(name)); err != nil {
		t.Fatalf("moving by name to the column it is already in failed: %v", err)
	}

	err = tracker.Move(t.Context(), fx.Key("#10"), "Pending triage")
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("Move to a column the board does not have = %v, want it to wrap ErrValidation", err)
	}
}

// trackerFollowUp: a follow-up is filed once and found by its marker, and a
// search naming fewer fields still finds it (spec 11 §1).
func trackerFollowUp(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
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
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	marker := ContractFollowUpMarker()

	if err := tracker.UpdateFollowUp(t.Context(), fx.Key("#13"), marker,
		"flow: the trunk is still unprotected", "Still not enforced, three days on."); err != nil {
		t.Fatalf("UpdateFollowUp: %v", err)
	}
	after, err := tracker.Ticket(t.Context(), fx.Key("#13"))
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
	if err := tracker.UpdateFollowUp(t.Context(), fx.Key("#13"), marker,
		"flow: the trunk is still unprotected", "Still not enforced, three days on."); err != nil {
		t.Errorf("UpdateFollowUp with nothing to change: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), fx.Key("#13"))
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
	if err := tracker.UpdateFollowUp(t.Context(), fx.Key("#13"), marker,
		"flow: the trunk takes anything anyone pushes", "Still not enforced, three days on."); err != nil {
		t.Fatalf("UpdateFollowUp with only the title to change: %v", err)
	}
	retitled, err := tracker.Ticket(t.Context(), fx.Key("#13"))
	if err != nil {
		t.Fatalf("Ticket after the retitle: %v", err)
	}
	if retitled.Title != "flow: the trunk takes anything anyone pushes" {
		t.Errorf("the title is %q: a rewrite that only changed the title did nothing", retitled.Title)
	}

	// #12 is a person's ticket. flow edits only its own writing.
	if err := tracker.UpdateFollowUp(t.Context(), fx.Key("#12"), marker, "flow took over", "flow was here"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("rewriting a ticket flow did not open = %v, want ErrValidation", err)
	}
	theirs, err := tracker.Ticket(t.Context(), fx.Key("#12"))
	if err != nil {
		t.Fatalf("Ticket #12: %v", err)
	}
	if strings.Contains(theirs.Body, "flow was here") {
		t.Errorf("flow rewrote somebody else's ticket anyway: %q", theirs.Body)
	}
	if theirs.Title == "flow took over" {
		t.Errorf("flow retitled somebody else's ticket anyway: %q", theirs.Title)
	}

	if err := tracker.UpdateFollowUp(t.Context(), fx.Key("#4040"), marker, "nobody", "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("rewriting a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerComment: a marked comment is written and then edited in place, and a
// ticket that is not there is ErrNotFound.
func trackerComment(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
	if err := tracker.Comment(t.Context(), fx.Key("#13"), marker, "still not done"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := tracker.Comment(t.Context(), fx.Key("#13"), marker, "still not done, a week on"); err != nil {
		t.Fatalf("Comment again: %v", err)
	}
	if err := tracker.Comment(t.Context(), fx.Key("#4040"), marker, "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("commenting on a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerComments: everything written on a ticket comes back, oldest first,
// with flow's own writing still recognisable as flow's. The blocked-work audit
// asks whether anybody has explained a ticket, and a reading that lost the
// marker would count flow's own nudge as the explanation (spec 07 §6).
func trackerComments(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	key := fx.Key("#13")
	first := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")
	second := core.NewMarker(core.MarkerFollowUp, "check", "tracker.labels", "repo", "acme/widget")
	if err := tracker.Comment(t.Context(), key, first, "the older of the two"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := tracker.Comment(t.Context(), key, second, "the newer of the two"); err != nil {
		t.Fatalf("Comment again: %v", err)
	}
	comments, err := tracker.Comments(t.Context(), key)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("Comments returned %d comments, want the 2 that were written: %+v", len(comments), comments)
	}
	if !strings.Contains(comments[0].Body, "the older of the two") {
		t.Errorf("the first comment back is %q, want the older one: comments come back oldest first", comments[0].Body)
	}
	if !strings.Contains(comments[1].Body, "the newer of the two") {
		t.Errorf("the second comment back is %q, want the newer one", comments[1].Body)
	}
	for _, comment := range comments {
		if !core.Marked(comment.Body) {
			t.Errorf("a comment flow wrote came back as %q, with no marker: flow cannot tell its own writing from an explanation", comment.Body)
		}
		if comment.Author == "" {
			t.Error("a comment came back with no author, though flow's own credential wrote it")
		}
		if comment.CreatedAt.IsZero() {
			t.Error("a comment came back with no date, so nothing can be said about when it was written")
		}
	}
	missing := fx.Key("#4040")
	_, err = tracker.Comments(t.Context(), missing)
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("reading the comments of a ticket that is not there = %v, want ErrNotFound", err)
	}
	// The ticket is what the caller asked about, so the ticket is what the
	// refusal has to name: a provider that answers the comment endpoint of a
	// missing issue with a 404 about the endpoint leaves whoever reads the
	// report looking for a fault in flow.
	if err != nil && !strings.Contains(err.Error(), missing) {
		t.Errorf("reading the comments of %s failed with %q, which does not name the ticket", missing, err)
	}
}

// trackerClose: a ticket is closed with a reason, and closing it again does not
// move the moment the work was finished.
func trackerClose(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	if err := tracker.Close(t.Context(), fx.Key("#13"), core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := tracker.Ticket(t.Context(), fx.Key("#13"))
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
	if err := tracker.Close(t.Context(), fx.Key("#13"), core.ClosedCompleted); err != nil {
		t.Fatalf("Close again: %v", err)
	}
	again, err := tracker.Ticket(t.Context(), fx.Key("#13"))
	if err != nil {
		t.Fatalf("Ticket after the second Close: %v", err)
	}
	if !again.ClosedAt.Equal(closed.ClosedAt) {
		t.Errorf("closed at %v then %v: closing a closed ticket moved the date", closed.ClosedAt, again.ClosedAt)
	}
	if err := tracker.Close(t.Context(), fx.Key("#4040"), core.ClosedCompleted); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("closing a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerReopen: a closed ticket is reopened, and reopening one that is not
// there is ErrNotFound.
func trackerReopen(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	before, err := tracker.Ticket(t.Context(), fx.Key("#11"))
	if err != nil {
		t.Fatalf("Ticket(#11): %v", err)
	}
	if !before.Closed {
		t.Fatal("the fixture's #11 should be closed")
	}
	if err := tracker.Reopen(t.Context(), fx.Key("#11"), "flow could not verify it was done"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	after, err := tracker.Ticket(t.Context(), fx.Key("#11"))
	if err != nil {
		t.Fatalf("Ticket after Reopen: %v", err)
	}
	if after.Closed {
		t.Error("the ticket is still closed after being reopened")
	}
	if err := tracker.Reopen(t.Context(), fx.Key("#4040"), "nobody is listening"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("reopening a ticket that is not there = %v, want ErrNotFound", err)
	}
}

// trackerLinkedKeys: linkage comes from the forge's own structured link, and a
// pull request that names nothing links to nothing (P12, spec 04 §3).
func trackerLinkedKeys(t *testing.T, newTracker NewTracker) {
	fx := ContractTracker()
	tracker := newTracker(t, &fx)
	pr := fx.Linked()
	if keys := tracker.LinkedKeys(pr); !slices.Contains(keys, fx.Key("#11")) {
		t.Errorf("LinkedKeys = %v, want the ticket the pull request is for", keys)
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
