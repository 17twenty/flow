package coretest

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// NewTracker builds the tracker under test from a fixture.
type NewTracker func(t *testing.T, fx TrackerFixture) core.Tracker

// TrackerContract is every behaviour the engine is entitled to assume of a
// tracker: probing, discovery, canonical observation, idempotent moves, and
// follow-ups that are filed once (spec 11 §5).
func TrackerContract(t *testing.T, newTracker NewTracker) {
	t.Helper()

	t.Run("identity", func(t *testing.T) {
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
	})

	t.Run("probe reports what the credential may do", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		permissions, err := tracker.Probe(t.Context())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if len(permissions.Tokens) == 0 {
			t.Error("Probe reported no permissions for a credential the fixture grants everything to")
		}
	})

	t.Run("what is discovered can be parsed back", func(t *testing.T) {
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
	})

	t.Run("a hint narrows discovery", func(t *testing.T) {
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
	})

	t.Run("nonsense is not a reference", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		if _, err := tracker.ParseRef("the delivery board, you know the one"); !errors.Is(err, core.ErrValidation) {
			t.Errorf("ParseRef of nonsense = %v, want it to wrap ErrValidation", err)
		}
	})

	t.Run("an observation is canonical and stable", func(t *testing.T) {
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
	})

	t.Run("observing something that is not there is not an error", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		observation, err := tracker.Observe(t.Context(), "tracker.expedite")
		if err != nil {
			t.Fatalf("Observe of an unconfigured thing: %v", err)
		}
		if observation.Exists {
			t.Error("the tracker claims to hold something the fixture never gave it")
		}
	})

	t.Run("applying a change is visible in the next observation", func(t *testing.T) {
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
	})

	t.Run("creating the same thing twice is a conflict", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		change := core.Change{
			What: "tracker.labels",
			Op:   core.ChangeCreate,
			Item: core.Item{Name: "blocked", Fields: map[string]string{"color": "b60205"}},
		}
		if _, err := tracker.Apply(t.Context(), change); !errors.Is(err, core.ErrConflict) {
			t.Errorf("creating a label that is already there = %v, want ErrConflict", err)
		}
	})

	t.Run("a thing is updated and then deleted", func(t *testing.T) {
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
	})

	t.Run("tickets by key, by column, and all of them", func(t *testing.T) {
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
	})

	t.Run("a ticket that is not there", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		if _, err := tracker.Ticket(t.Context(), "#4040"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Ticket(#4040) = %v, want it to wrap ErrNotFound", err)
		}
		if err := tracker.Move(t.Context(), "#4040", ColumnDone); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Move(#4040) = %v, want it to wrap ErrNotFound", err)
		}
	})

	t.Run("a move is idempotent", func(t *testing.T) {
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
	})

	t.Run("a follow-up is filed once and found by its marker", func(t *testing.T) {
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
	})

	t.Run("a new follow-up carries its marker", func(t *testing.T) {
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
	})

	t.Run("a comment is written and edited in place", func(t *testing.T) {
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
	})

	t.Run("a closed ticket is reopened", func(t *testing.T) {
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
	})

	t.Run("linkage uses the forge's own link", func(t *testing.T) {
		tracker := newTracker(t, ContractTracker())
		pr := ContractForge().PullRequests[1]
		if keys := tracker.LinkedKeys(pr); !slices.Contains(keys, "#11") {
			t.Errorf("LinkedKeys = %v, want the issue the forge says it closes", keys)
		}
		if keys := tracker.LinkedKeys(core.PullRequest{Number: 99}); len(keys) != 0 {
			t.Errorf("LinkedKeys of an unlinked pull request = %v, want none: flow never guesses", keys)
		}
	})
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
