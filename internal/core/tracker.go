package core

import (
	"cmp"
	"maps"
	"slices"
	"time"
)

// ColumnID is the tracker's own identifier for a board column: a GitHub
// Projects Status option id, a Linear workflow state id, a Jira board column.
// flow's configured column ids are mapped onto these in the lock, so that
// renaming a column in the tracker does not change the configuration (spec 04
// §2).
type ColumnID string

// Status is a ticket's status as the tracker records it. For most trackers the
// status is the column; for Jira a column may gather several statuses, which is
// why a ticket carries both.
type Status struct {
	// ID is the tracker's identifier for the status.
	ID string
	// Name is what the tracker displays.
	Name string
}

// Ticket is one item of work, as every tracker flow speaks to can describe it
// (spec 11 §1).
type Ticket struct {
	// Key is how people refer to it: #12 for GitHub, PROJ-12 for Jira.
	Key string
	// ID is the tracker's internal identifier, where it differs from the key.
	ID string
	// URL is its web address.
	URL string
	// Title is its one-line summary.
	Title string
	// Body is its description, in markdown.
	Body string
	// Status is the tracker's status for it.
	Status Status
	// ColumnID is the board column that status falls in.
	ColumnID ColumnID
	// Labels are its labels.
	Labels []string
	// Assignees are the logins it is assigned to.
	Assignees []string
	// Parent is the key of the epic, sub-issue parent or project above it,
	// empty when it has none. The Definition of Ready reads it (spec 04 §7).
	Parent string
	// Blocked reports a ticket the team has marked as unable to move.
	Blocked bool
	// Closed reports a ticket the tracker considers finished.
	Closed bool
	// CloseReason is why it was closed, where the tracker records one:
	// [ClosedCompleted] or [ClosedNotPlanned].
	CloseReason string
	// Size is the tracker's estimate for it, zero when the tracker has no
	// size field or none is set.
	Size time.Duration
	// UpdatedAt is when it last changed, and ClosedAt when it was closed.
	UpdatedAt time.Time
	ClosedAt  time.Time
}

// Close reasons, where the tracker distinguishes them. A ticket closed as not
// planned is not expected to have a merged pull request behind it (spec 07 §6).
const (
	// ClosedCompleted means the work was done.
	ClosedCompleted = "completed"
	// ClosedNotPlanned means it was abandoned.
	ClosedNotPlanned = "not_planned"
)

// HasLabel reports whether the ticket carries that label, case-insensitively,
// since trackers differ on whether labels are case-sensitive.
func (t Ticket) HasLabel(label string) bool {
	return slices.ContainsFunc(t.Labels, func(have string) bool {
		return equalFold(have, label)
	})
}

// TicketQuery selects tickets: by key, by the column they sit in, or by when
// they last changed (spec 01 §3).
type TicketQuery struct {
	// Keys names particular tickets. When set, the other fields are ignored.
	Keys []string
	// Column limits the query to one board column.
	Column ColumnID
	// UpdatedSince bounds the window; a zero time means no bound.
	UpdatedSince time.Time
	// Limit caps how many to return. Zero means no cap.
	Limit int
}

// Candidate is one answer to a placeholder: a board flow might mean, offered
// to a human to choose from or taken automatically when it is the only one
// (spec 07 §3).
type Candidate struct {
	// What names the placeholder this answers, e.g. tracker.project.
	What string
	// ID is the tracker's identifier for it, which is what goes in the lock.
	ID string
	// Name is what the tracker displays.
	Name string
	// URL is its web address.
	URL string
	// Detail is one line distinguishing it from the other candidates.
	Detail string
}

// Ref is the candidate as a reference: what a human pasted, resolved to
// something flow can record.
func (c Candidate) Ref() Ref {
	return Ref{What: c.What, ID: c.ID, Name: c.Name, URL: c.URL}
}

// Ref identifies one tracker object. It is what [Candidate] resolves to and
// what a tracker's ParseRef makes of a URL somebody pasted.
type Ref struct {
	// What names the kind of object, e.g. tracker.project.
	What string
	// ID is the tracker's identifier for it.
	ID string
	// Name is what the tracker displays, where the reference carried one.
	Name string
	// URL is the address it came from.
	URL string
}

// Item is one thing a provider holds: a status option, a label, a column's
// limit. An observation and a desired state are both lists of these, so they
// can be diffed without the planner knowing what they describe (spec 06 §2).
type Item struct {
	// ID is the provider's identifier, empty for something that does not
	// exist yet.
	ID string
	// Name is what a human sees, and what two items are matched on.
	Name string
	// Fields carry everything else, as strings.
	Fields map[string]string
}

// Field returns a field's value, or the empty string.
func (i Item) Field(name string) string { return i.Fields[name] }

// WithField returns a copy of the item with that field set.
func (i Item) WithField(name, value string) Item {
	fields := maps.Clone(i.Fields)
	if fields == nil {
		fields = map[string]string{}
	}
	fields[name] = value
	i.Fields = fields
	return i
}

// Equal reports whether two items say the same thing. An item with no id
// matches one that has one, because a desired item does not know the id the
// provider will give it.
func (i Item) Equal(other Item) bool {
	if i.Name != other.Name {
		return false
	}
	if i.ID != "" && other.ID != "" && i.ID != other.ID {
		return false
	}
	return maps.Equal(i.Fields, other.Fields)
}

// Observation is what an adapter found when it looked, in a canonical form:
// the same board observed twice gives the same observation, whatever order the
// provider returned things in, so that a hash of it means something (spec 06
// §2).
type Observation struct {
	// What names what was observed, e.g. tracker.status_options.
	What string
	// Exists reports whether the thing itself is there at all. An observation
	// of a project that does not exist has no items and is not an error.
	Exists bool
	// Items are what it holds, in canonical order.
	Items []Item
}

// Canonical returns the observation with its items in a fixed order: by name,
// then by id. Adapters return observations through this, so that two of them
// describing the same board are equal.
func (o Observation) Canonical() Observation {
	o.Items = slices.Clone(o.Items)
	slices.SortStableFunc(o.Items, func(a, b Item) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return o
}

// Find returns the item with that name.
func (o Observation) Find(name string) (Item, bool) {
	for _, item := range o.Items {
		if item.Name == name {
			return item, true
		}
	}
	return Item{}, false
}

// Change operations. A plan is a list of these, and apply hands them to the
// adapter one at a time (spec 06 §3).
const (
	// ChangeCreate adds something the provider does not have.
	ChangeCreate = "create"
	// ChangeUpdate alters something it does.
	ChangeUpdate = "update"
	// ChangeDelete removes something flow created and no longer wants.
	ChangeDelete = "delete"
)

// Change is one mutation an adapter is asked to make.
type Change struct {
	// What names what is being changed, e.g. tracker.status_options.
	What string
	// Op is one of the change operations above.
	Op string
	// Item is the thing to create, the new value to update to, or the thing
	// to delete. Its ID is set for an update or a delete.
	Item Item
}

// ApplyResult is what became of a [Change].
type ApplyResult struct {
	// Change is the change that was made.
	Change Change
	// Item is the thing as it stands afterwards, with the id the provider
	// gave it.
	Item Item
	// Note is one line describing what the provider did, for the log.
	Note string
}

// FollowUp is a ticket flow files for work it cannot do itself: what is
// missing, why, and how flow will know when it is fixed (spec 07 §4). The
// marker is what makes filing it twice impossible.
type FollowUp struct {
	// Marker identifies the ticket as flow's, and which check filed it.
	Marker Marker
	// Title is the ticket's title.
	Title string
	// Body is the ticket's description, in markdown. The adapter appends the
	// marker to it in whichever rendering the tracker preserves.
	Body string
	// Labels are applied to the ticket.
	Labels []string
	// Assignee is who it is given to, empty for nobody.
	Assignee string
}

// equalFold reports whether two ASCII strings are equal ignoring case. Labels
// are ASCII in every tracker researched, and a Unicode fold would make two
// visually distinct labels the same one.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
