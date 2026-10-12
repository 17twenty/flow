package coretest

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// TrackerFixture is the world a tracker presents (spec 14).
type TrackerFixture struct {
	// Kind is the adapter name the tracker reports.
	Kind string
	// Capabilities is what it declares it can do.
	Capabilities core.CapabilitySet
	// Permissions is what Probe reports.
	Permissions core.Permissions
	// Candidates are the answers Discover offers, keyed by placeholder.
	Candidates map[string][]core.Candidate
	// Observations are what Observe finds, keyed by desired item id.
	Observations map[string]core.Observation
	// Tickets are the items on the board.
	Tickets []core.Ticket
	// Columns map a column id to the name the tracker displays, so that a
	// move sets a status a human would recognise.
	Columns map[core.ColumnID]string
	// Clock decides what "now" is for anything the tracker writes.
	Clock core.Clock
}

// FakeTracker is an in-memory tracker that behaves: the reference every real
// tracker adapter is measured against. Failures are injected through its
// embedded [Faults].
type FakeTracker struct {
	Faults

	mu           sync.Mutex
	kind         string
	caps         core.CapabilitySet
	permissions  core.Permissions
	clock        core.Clock
	candidates   map[string][]core.Candidate
	observations map[string]core.Observation
	tickets      []core.Ticket
	columns      map[core.ColumnID]string
	comments     map[string][]core.Comment
	reopens      map[string]string
	nextTicket   int
	nextID       int
}

// NewFakeTracker builds a tracker holding the fixture's world.
func NewFakeTracker(fx TrackerFixture) *FakeTracker {
	if fx.Kind == "" {
		fx.Kind = "fake-tracker"
	}
	if fx.Capabilities.Adapter == "" {
		fx.Capabilities = FullCapabilities(fx.Kind, core.KindTracker)
	}
	if fx.Clock == nil {
		fx.Clock = NewClock(time.Time{})
	}
	t := &FakeTracker{
		kind:         fx.Kind,
		caps:         fx.Capabilities,
		permissions:  fx.Permissions,
		clock:        fx.Clock,
		candidates:   map[string][]core.Candidate{},
		observations: map[string]core.Observation{},
		tickets:      slices.Clone(fx.Tickets),
		columns:      maps.Clone(fx.Columns),
		comments:     map[string][]core.Comment{},
		reopens:      map[string]string{},
	}
	for what, candidates := range fx.Candidates {
		t.candidates[what] = slices.Clone(candidates)
	}
	for what, observation := range fx.Observations {
		observation.What = what
		t.observations[what] = observation.Canonical()
	}
	for _, ticket := range t.tickets {
		if n, err := strconv.Atoi(strings.TrimPrefix(ticket.Key, "#")); err == nil {
			t.nextTicket = max(t.nextTicket, n)
		}
	}
	return t
}

// Kind is the adapter's name.
func (t *FakeTracker) Kind() string { return t.kind }

// Capabilities is what the fake declares it can do.
func (t *FakeTracker) Capabilities() core.CapabilitySet { return t.caps }

// Probe reports the permissions the fixture granted.
func (t *FakeTracker) Probe(ctx context.Context) (core.Permissions, error) {
	if err := t.enter(ctx, "Probe"); err != nil {
		return core.Permissions{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.permissions, nil
}

// Discover offers the candidates that could answer a placeholder.
func (t *FakeTracker) Discover(ctx context.Context, what string, hint map[string]string) ([]core.Candidate, error) {
	if err := t.enter(ctx, "Discover"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	candidates := slices.Clone(t.candidates[what])
	for i := range candidates {
		candidates[i].What = what
	}
	if name := hint["name"]; name != "" {
		candidates = slices.DeleteFunc(candidates, func(c core.Candidate) bool { return c.Name != name })
	}
	return candidates, nil
}

// ParseRef turns one of the fake's own URLs back into a reference.
func (t *FakeTracker) ParseRef(raw string) (core.Ref, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for what, candidates := range t.candidates {
		for _, candidate := range candidates {
			if candidate.URL != "" && candidate.URL == raw {
				candidate.What = what
				return candidate.Ref(), nil
			}
		}
	}
	return core.Ref{}, core.Errorf(core.ErrValidation, "read the reference %q", raw).
		WithWhy("it is not an address this tracker recognises").
		WithFix("paste the URL from the tracker's own page")
}

// Observe returns the current state of one desired item, canonically.
func (t *FakeTracker) Observe(ctx context.Context, what string) (core.Observation, error) {
	if err := t.enter(ctx, "Observe"); err != nil {
		return core.Observation{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	observation, ok := t.observations[what]
	if !ok {
		return core.Observation{What: what}, nil
	}
	cloned := observation.Canonical()
	cloned.Items = slices.Clone(cloned.Items)
	for i, item := range cloned.Items {
		cloned.Items[i].Fields = maps.Clone(item.Fields)
	}
	return cloned, nil
}

// Apply makes one change to what the tracker holds.
func (t *FakeTracker) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if err := t.enter(ctx, "Apply"); err != nil {
		return core.ApplyResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	observation, ok := t.observations[c.What]
	if !ok {
		observation = core.Observation{What: c.What, Exists: true}
	}
	at := slices.IndexFunc(observation.Items, func(item core.Item) bool {
		return (c.Item.ID != "" && item.ID == c.Item.ID) || item.Name == c.Item.Name
	})

	result := core.ApplyResult{Change: c}
	switch c.Op {
	case core.ChangeCreate:
		if at >= 0 {
			return core.ApplyResult{}, core.Errorf(core.ErrConflict, "create %s %q", c.What, c.Item.Name).
				WithWhy("the tracker already has one by that name").
				WithFix("update it instead of creating it")
		}
		t.nextID++
		item := c.Item
		item.ID = c.What + "-" + strconv.Itoa(t.nextID)
		observation.Exists = true
		observation.Items = append(observation.Items, item)
		result.Item, result.Note = item, "created "+item.Name
	case core.ChangeUpdate:
		if at < 0 {
			return core.ApplyResult{}, notFound(c.What, c.Item.Name)
		}
		item := c.Item
		if item.ID == "" {
			item.ID = observation.Items[at].ID
		}
		observation.Items[at] = item
		result.Item, result.Note = item, "updated "+item.Name
	case core.ChangeDelete:
		if at < 0 {
			return core.ApplyResult{}, notFound(c.What, c.Item.Name)
		}
		result.Item, result.Note = observation.Items[at], "deleted "+observation.Items[at].Name
		observation.Items = slices.Delete(observation.Items, at, at+1)
	default:
		return core.ApplyResult{}, core.Errorf(core.ErrValidation, "apply a change to %s", c.What).
			WithWhy("%q is not a change this tracker knows", c.Op).
			WithFix("use create, update or delete")
	}
	t.observations[c.What] = observation.Canonical()
	return result, nil
}

// Ticket returns one ticket by key.
func (t *FakeTracker) Ticket(ctx context.Context, key string) (core.Ticket, error) {
	if err := t.enter(ctx, "Ticket"); err != nil {
		return core.Ticket{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return core.Ticket{}, notFound("ticket", key)
	}
	return t.tickets[at], nil
}

// Tickets returns the tickets matching q.
func (t *FakeTracker) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	if err := t.enter(ctx, "Tickets"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []core.Ticket
	if len(q.Keys) > 0 {
		for _, key := range q.Keys {
			if at := t.indexOf(key); at >= 0 {
				out = append(out, t.tickets[at])
			}
		}
		return out, nil
	}
	for _, ticket := range t.tickets {
		if q.Column != "" && ticket.ColumnID != q.Column {
			continue
		}
		if !q.UpdatedSince.IsZero() && ticket.UpdatedAt.Before(q.UpdatedSince) {
			continue
		}
		out = append(out, ticket)
	}
	slices.SortStableFunc(out, func(a, b core.Ticket) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Move takes a ticket to another column, and does nothing when it is already
// there.
func (t *FakeTracker) Move(ctx context.Context, key string, to core.ColumnID) error {
	if err := t.enter(ctx, "Move"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return notFound("ticket", key)
	}
	if t.tickets[at].ColumnID == to {
		return nil
	}
	name := t.columns[to]
	if name == "" {
		name = string(to)
	}
	t.tickets[at].ColumnID = to
	t.tickets[at].Status = core.Status{ID: string(to), Name: name}
	t.tickets[at].UpdatedAt = t.clock.Now()
	return nil
}

// CreateFollowUp files a ticket, or returns the one the marker already names.
func (t *FakeTracker) CreateFollowUp(ctx context.Context, f core.FollowUp) (core.Ticket, error) {
	if err := t.enter(ctx, "CreateFollowUp"); err != nil {
		return core.Ticket{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if f.Marker.IsZero() {
		return core.Ticket{}, core.Errorf(core.ErrValidation, "file a follow-up").
			WithWhy("it carries no marker, so flow could never find it again").
			WithFix("give the follow-up a marker")
	}
	if at := t.findMarked(f.Marker); at >= 0 {
		return t.tickets[at], nil
	}
	t.nextTicket++
	now := t.clock.Now()
	ticket := core.Ticket{
		Key:       "#" + strconv.Itoa(t.nextTicket),
		ID:        "ticket-" + strconv.Itoa(t.nextTicket),
		URL:       "https://tracker.test/issues/" + strconv.Itoa(t.nextTicket),
		Title:     f.Title,
		Body:      core.MarkBody(f.Body, f.Marker, true),
		Labels:    slices.Clone(f.Labels),
		UpdatedAt: now,
	}
	if f.Assignee != "" {
		ticket.Assignees = []string{f.Assignee}
	}
	t.tickets = append(t.tickets, ticket)
	return ticket, nil
}

// FindFollowUp returns the ticket carrying a marker, or nil.
func (t *FakeTracker) FindFollowUp(ctx context.Context, m core.Marker) (*core.Ticket, error) {
	if err := t.enter(ctx, "FindFollowUp"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.findMarked(m)
	if at < 0 {
		return nil, nil
	}
	found := t.tickets[at]
	return &found, nil
}

// UpdateFollowUp rewrites the title and body of a ticket flow opened, keeping
// its marker.
func (t *FakeTracker) UpdateFollowUp(ctx context.Context, key string, m core.Marker, title, body string) error {
	if err := t.enter(ctx, "UpdateFollowUp"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return notFound("ticket", key)
	}
	if !core.HasMarker(t.tickets[at].Body, m) {
		return core.Errorf(core.ErrValidation, "rewrite %s", key).
			WithWhy("it does not carry %s, so it is not flow's to rewrite", m).
			WithFix("leave it alone, or give flow its own ticket to keep")
	}
	marked := core.MarkBody(body, m, true)
	if marked == t.tickets[at].Body && title == t.tickets[at].Title {
		return nil
	}
	t.tickets[at].Title = title
	t.tickets[at].Body = marked
	t.tickets[at].UpdatedAt = t.clock.Now()
	return nil
}

// Comment writes a marked comment on a ticket, editing the one already
// carrying that marker.
func (t *FakeTracker) Comment(ctx context.Context, key string, m core.Marker, body string) error {
	if err := t.enter(ctx, "Comment"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return notFound("ticket", key)
	}
	key = t.tickets[at].Key
	marked := core.MarkBody(body, m, true)
	now := t.clock.Now()
	for i, comment := range t.comments[key] {
		if m.Matches(comment.Marker) {
			comment.Body, comment.UpdatedAt = marked, now
			t.comments[key][i] = comment
			return nil
		}
	}
	t.nextID++
	t.comments[key] = append(t.comments[key], core.Comment{
		ID:        "comment-" + strconv.Itoa(t.nextID),
		Author:    "flow",
		Body:      marked,
		Marker:    m,
		CreatedAt: now,
		UpdatedAt: now,
	})
	return nil
}

// Close closes a ticket, recording why.
func (t *FakeTracker) Close(ctx context.Context, key string, reason string) error {
	if err := t.enter(ctx, "Close"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return notFound("ticket", key)
	}
	if t.tickets[at].Closed {
		return nil
	}
	if reason == "" {
		reason = core.ClosedCompleted
	}
	now := t.clock.Now()
	t.tickets[at].Closed = true
	t.tickets[at].CloseReason = reason
	t.tickets[at].ClosedAt = now
	t.tickets[at].UpdatedAt = now
	return nil
}

// Reopen reopens a closed ticket, recording why.
func (t *FakeTracker) Reopen(ctx context.Context, key string, reason string) error {
	if err := t.enter(ctx, "Reopen"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.indexOf(key)
	if at < 0 {
		return notFound("ticket", key)
	}
	t.tickets[at].Closed = false
	t.tickets[at].CloseReason = ""
	t.tickets[at].ClosedAt = time.Time{}
	t.tickets[at].UpdatedAt = t.clock.Now()
	t.reopens[t.tickets[at].Key] = reason
	return nil
}

// LinkedKeys returns the tickets the forge itself says a pull request closes.
func (t *FakeTracker) LinkedKeys(pr core.PullRequest) []string {
	return slices.Clone(pr.ClosingIssues)
}

// Comments returns the comments on a ticket, for a test to inspect.
func (t *FakeTracker) Comments(key string) []core.Comment {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.comments[key])
}

// ReopenReason returns the reason a ticket was last reopened with.
func (t *FakeTracker) ReopenReason(key string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reopens[key]
}

// SetTicket adds or replaces a ticket, so that a test can change the world
// between calls.
func (t *FakeTracker) SetTicket(ticket core.Ticket) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if at := t.indexOf(ticket.Key); at >= 0 {
		t.tickets[at] = ticket
		return
	}
	t.tickets = append(t.tickets, ticket)
}

// SetObservation adds or replaces what Observe finds for one desired item,
// keyed by the item's own id, so that a test can move the board behind flow's
// back: a status option renamed after a check passed is what drift is
// (spec 07 §7), and it cannot be staged any other way.
func (t *FakeTracker) SetObservation(what string, observation core.Observation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.observations == nil {
		t.observations = map[string]core.Observation{}
	}
	observation.What = what
	t.observations[what] = observation
}

// SetCandidates replaces what Discover offers for one placeholder. How many
// there are is the whole of spec 07 §3: one is an answer flow may take, and
// more than one is a question only a person can settle.
func (t *FakeTracker) SetCandidates(what string, candidates ...core.Candidate) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.candidates == nil {
		t.candidates = map[string][]core.Candidate{}
	}
	t.candidates[what] = slices.Clone(candidates)
}

// SetPermissions replaces what Probe reports, so that a test can hold a
// credential that is accepted and still too small.
func (t *FakeTracker) SetPermissions(permissions core.Permissions) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.permissions = permissions
}

// indexOf finds a ticket by key, ignoring a leading #. The caller holds the
// lock.
func (t *FakeTracker) indexOf(key string) int {
	want := strings.TrimPrefix(key, "#")
	return slices.IndexFunc(t.tickets, func(have core.Ticket) bool {
		return strings.TrimPrefix(have.Key, "#") == want
	})
}

// findMarked finds the ticket whose body carries a matching marker. The
// caller holds the lock.
func (t *FakeTracker) findMarked(m core.Marker) int {
	return slices.IndexFunc(t.tickets, func(have core.Ticket) bool {
		return core.HasMarker(have.Body, m)
	})
}

// FakeTracker is the reference implementation of the interface.
var _ core.Tracker = (*FakeTracker)(nil)
