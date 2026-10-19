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
	// Archived reports a board item the tracker has put out of sight. It is
	// not the same as closed: an archived item keeps its column and its
	// issue, but the board no longer lists it and the tracker will not accept
	// a change to it, so flow neither counts it nor moves it.
	Archived bool
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

// TicketComment is one comment on a ticket, by anybody: flow's own marked
// writing and a person's explanation look the same to a tracker, and telling
// them apart is [Marked]'s job rather than the adapter's.
type TicketComment struct {
	// Author is the login that wrote it, where the tracker says.
	Author string
	// Body is what it says, in the tracker's own markup.
	Body string
	// CreatedAt is when it was written.
	CreatedAt time.Time
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
	ID string `json:"id,omitempty"`
	// Name is what a human sees, and what two items are matched on.
	Name string `json:"name"`
	// Fields carry everything else, as strings.
	Fields map[string]string `json:"fields,omitempty"`
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

// Matches reports whether i is at least what want asks for: the same name, a
// compatible id, and every field want names, with the same value.
//
// It is how an observation is compared against a desired item. A provider
// knows things about an item flow never asked about — a label's colour, an
// option's description — and holding it to [Item.Equal] would call every one
// of them a difference flow could never resolve (spec 06 §2).
func (i Item) Matches(want Item) bool {
	if i.Name != want.Name {
		return false
	}
	if i.ID != "" && want.ID != "" && i.ID != want.ID {
		return false
	}
	return len(i.Differences(want)) == 0
}

// Differences names the fields of want that i does not already agree with,
// sorted. It is the reason [Item.Matches] said no, and the two are one
// mechanism so that they cannot come to disagree.
//
// Something has to say which fields these are, because an instruction to a
// person has to be about the part that is wrong. "Protect the trunk, with
// allow_deletions=false, allow_force_push=false, enabled=true,
// require_linear_history=true, strict_checks=true" sends somebody to check
// five settings, four of which were already as flow asked — and buries the
// one that was not (spec 06 §2).
//
// The name and the id are not fields and are not answered here: an item with
// another name is not this item at all, which is a question Matches asks
// before it asks this one.
func (i Item) Differences(want Item) []string {
	var differs []string
	for field, value := range want.Fields {
		if i.Fields[field] != value {
			differs = append(differs, field)
		}
	}
	slices.Sort(differs)
	return differs
}

// Observation is what an adapter found when it looked, in a canonical form:
// the same board observed twice gives the same observation, whatever order the
// provider returned things in, so that a hash of it means something (spec 06
// §2).
type Observation struct {
	// What names what was observed, e.g. tracker.status_options.
	What string `json:"what"`
	// Exists reports whether the thing itself is there at all. An observation
	// of a project that does not exist has no items and is not an error.
	Exists bool `json:"exists"`
	// Items are what it holds, in canonical order.
	Items []Item `json:"items,omitempty"`
	// Notes are things the provider said about this item that flow does not
	// compare it on: a mechanism that is configured but switched off, a
	// setting enforced somewhere flow cannot write. They are deliberately
	// outside the canonical encoding, so a note can never read as drift —
	// they change what a report says to do, never what the plan decides.
	Notes []Note `json:"notes,omitempty"`
}

// Note is one thing a provider volunteered about an observation.
type Note struct {
	// Text is the fact, in one line.
	Text string `json:"text"`
	// URL is where to see it, empty when it is not a thing with a page.
	URL string `json:"url,omitempty"`
	// Fix is the step the fact implies, empty when it implies none. The
	// adapter writes it because the mechanism is the adapter's own: only
	// the GitHub adapter knows that a ruleset has an enforcement switch.
	Fix string `json:"fix,omitempty"`
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
	What string `json:"what"`
	// Op is one of the change operations above.
	Op string `json:"op"`
	// Item is the thing to create, the new value to update to, or the thing
	// to delete. Its ID is set for an update or a delete.
	//
	// It is whole, even for an update: an adapter writing one of these
	// settings writes them all, so sending only the part that moved would
	// clear the rest.
	Item Item `json:"item"`
	// Differs names the fields of Item the provider does not already agree
	// with. It is empty unless there is something there to disagree with,
	// which is to say for an update; a create differs in all of itself and
	// a delete in none of it.
	//
	// What it is for is the telling. The adapter is handed the whole item
	// either way, but a plan and a follow-up ticket are read by somebody
	// deciding what to go and do, and a list that includes four settings
	// already as asked is a list that wastes their time and hides the
	// fifth (spec 06 §2).
	Differs []string `json:"differs,omitempty"`
}

// Fields names the fields worth telling somebody about, sorted: the ones
// that differ when the provider has something to differ from, and all of
// them when it does not. An adapter never calls this — it is handed
// [Change.Item] whole — and every rendering of a change does, so that a plan
// and the follow-up ticket made from it cannot describe it differently.
func (c Change) Fields() []string {
	if len(c.Differs) > 0 {
		return c.Differs
	}
	return slices.Sorted(maps.Keys(c.Item.Fields))
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
	// FollowUp says the change was not made and a human has to make it,
	// with the reason. It is how an adapter retreats from a change it
	// could only make unsafely — GitHub Projects will not rewrite the
	// columns of a board too large to put back afterwards (spec 06 §4).
	// Nothing happened: an adapter that has already written something
	// returns the error it hit instead.
	FollowUp string
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
