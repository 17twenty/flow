package tracker

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// This file is the wire: the GraphQL documents flow sends to GitHub, the
// shapes their answers arrive in, and the translation of those shapes into
// flow's own vocabulary. Projects v2 has no REST API at all, so everything
// the adapter reads about a board is a query; the repository's own labels,
// at the end of this file, are the one exception (spec 11 §2).

// ghGraphQLEndpoint is the GraphQL endpoint beside an API root. github.com
// serves it under the API host; Enterprise Server serves it beside /api/v3
// rather than under it.
func ghGraphQLEndpoint(base string) string {
	trimmed := strings.TrimSuffix(base, "/")
	if host, ok := strings.CutSuffix(trimmed, "/api/v3"); ok {
		return host + "/api/graphql"
	}
	return trimmed + "/graphql"
}

// ghSite is the web host the API root belongs to: where a project's address
// is, which is a different host from the API on github.com and the same one
// on Enterprise Server.
func ghSite(base string) string {
	trimmed := strings.TrimSuffix(base, "/")
	if host, ok := strings.CutSuffix(trimmed, "/api/v3"); ok {
		return host
	}
	if host, ok := strings.CutPrefix(trimmed, "https://api."); ok {
		return "https://" + host
	}
	return GitHubProjectsSite
}

// ghOwnerType reads the segment a project's address names its owner kind with.
func ghOwnerType(segment string) (string, bool) {
	switch strings.ToLower(segment) {
	case "users":
		return OwnerUser, true
	case "orgs", "organizations":
		return OwnerOrg, true
	default:
		return "", false
	}
}

// ghOwnerSegment is the inverse: the segment an owner type appears as.
func ghOwnerSegment(ownerType string) string {
	if ownerType == OwnerOrg {
		return "orgs"
	}
	return "users"
}

// ghProjectURL is a project's web address, built rather than quoted from the
// provider. Discovery must only ever offer an address [GitHubProjects.ParseRef]
// accepts, and the one flow builds is the one it parses.
func ghProjectURL(site, owner, ownerType string, number int) string {
	return site + "/" + ghOwnerSegment(ownerType) + "/" + owner + "/projects/" + strconv.Itoa(number)
}

// ghOwnerField is the query field an owner type is reached through. GitHub
// addresses a person's projects and an organisation's through different
// fields of the same schema, which is why tracker.owner_type exists.
func ghOwnerField(ownerType string) string {
	if ownerType == OwnerOrg {
		return "organization"
	}
	return "user"
}

// ghSentinel is the core error a GitHub GraphQL error type means. Anything
// GitHub has no documented type for is the provider refusing for a reason
// flow has no name for, which is unavailable rather than invalid input,
// because the query is flow's and was not the user's to get wrong.
//
// It repeats the forge adapter's mapping because an adapter may not import
// another (spec 01 §2), and two adapters that happen to speak to the same
// company are not thereby one adapter.
func ghSentinel(errType string) error {
	switch errType {
	case "NOT_FOUND":
		return core.ErrNotFound
	case "FORBIDDEN", "INSUFFICIENT_SCOPES", "UNAUTHORIZED":
		return core.ErrPermission
	case "RATE_LIMITED":
		return core.ErrRateLimited
	default:
		return core.ErrUnavailable
	}
}

// ghProjectsQuery lists an owner's projects, one page at a time.
func ghProjectsQuery(ownerType string) string {
	return "query($login:String!,$first:Int!,$after:String){" +
		ghOwnerField(ownerType) + "(login:$login){" +
		"projectsV2(first:$first,after:$after){" +
		"nodes{id number title shortDescription closed items{totalCount}}" +
		"pageInfo{hasNextPage endCursor}}}}"
}

// ghProjectQuery reads one project and its Status field together. The columns
// are the single-select options of that field, so asking for them separately
// would be a second query for one board.
//
// The option's colour and description are read although no observation
// records them: GitHub's option input makes both mandatory, so rewriting the
// option set means sending every option flow is not changing back exactly as
// it stands. The item count comes along for the same reason — it is what says
// whether the rewrite is one flow can put right afterwards (spec 06 §4).
func ghProjectQuery(ownerType string) string {
	return "query($login:String!,$number:Int!){" +
		ghOwnerField(ownerType) + "(login:$login){" +
		"projectV2(number:$number){id number title shortDescription closed items{totalCount} " +
		`field(name:"Status"){...on ProjectV2SingleSelectField{id name options{id name color description}}}` +
		"}}}"
}

// ghPageInfo is GraphQL's cursor pagination.
type ghPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// ghProjectList is the answer to [ghProjectsQuery]. Only one of the two owner
// fields is ever present: the query names the one the owner type decided.
type ghProjectList struct {
	User         *ghProjects `json:"user"`
	Organization *ghProjects `json:"organization"`
}

// owner returns whichever owner the query asked for. An owner that is not
// there answers with no projects rather than with an error, which is what a
// login nobody has registered truthfully means.
func (l ghProjectList) owner() ghProjects {
	switch {
	case l.User != nil:
		return *l.User
	case l.Organization != nil:
		return *l.Organization
	default:
		return ghProjects{}
	}
}

// ghProjects is an owner's project connection.
type ghProjects struct {
	ProjectsV2 ghProjectConnection `json:"projectsV2"`
}

// ghProjectConnection is one page of projects.
type ghProjectConnection struct {
	Nodes    []ghProjectNode `json:"nodes"`
	PageInfo ghPageInfo      `json:"pageInfo"`
}

// ghProjectNode is one project as discovery sees it.
type ghProjectNode struct {
	ID               string      `json:"id"`
	Number           int         `json:"number"`
	Title            string      `json:"title"`
	ShortDescription string      `json:"shortDescription"`
	Closed           bool        `json:"closed"`
	Items            ghItemCount `json:"items"`
}

// ghItemCount is how many items a board holds.
type ghItemCount struct {
	TotalCount int `json:"totalCount"`
}

// toCandidate offers the project as an answer to the tracker.project
// placeholder, and reports whether it is one at all.
//
// A closed project is not offered. Taking the only candidate automatically is
// what discovery is for (spec 07 §3), and a board the team has finished with
// is the one answer that must never be chosen on their behalf.
func (n ghProjectNode) toCandidate(site, owner, ownerType string) (core.Candidate, bool) {
	if n.Closed || n.Number <= 0 || n.Title == "" {
		return core.Candidate{}, false
	}
	detail := strings.TrimSpace(n.ShortDescription)
	if detail == "" {
		detail = strconv.Itoa(n.Items.TotalCount) + " items"
	}
	return core.Candidate{
		What:   core.ItemTrackerProject,
		ID:     strconv.Itoa(n.Number),
		Name:   n.Title,
		URL:    ghProjectURL(site, owner, ownerType, n.Number),
		Detail: detail,
	}, true
}

// ghProjectResult is the answer to [ghProjectQuery].
type ghProjectResult struct {
	User         *ghProjectHolder `json:"user"`
	Organization *ghProjectHolder `json:"organization"`
}

// owner returns whichever owner the query asked for.
func (r ghProjectResult) owner() ghProjectHolder {
	switch {
	case r.User != nil:
		return *r.User
	case r.Organization != nil:
		return *r.Organization
	default:
		return ghProjectHolder{}
	}
}

// ghProjectHolder is the owner's one project, nil when there is no project
// with that number.
type ghProjectHolder struct {
	ProjectV2 *ghProject `json:"projectV2"`
}

// ghProject is one board and the Status field whose options are its columns.
type ghProject struct {
	// Exists is the adapter's own, set when the query returned a project.
	// GraphQL answers a missing project with a null rather than an error,
	// and a zero project and a missing one must not read alike.
	Exists bool `json:"-"`

	ID               string         `json:"id"`
	Number           int            `json:"number"`
	Title            string         `json:"title"`
	ShortDescription string         `json:"shortDescription"`
	Closed           bool           `json:"closed"`
	Items            ghItemCount    `json:"items"`
	Status           *ghStatusField `json:"field"`
}

// item is the board as one observed item. A project is identified by its
// number everywhere flow records it — in configuration, in the lock and in
// the address bar — so the number is the id here too, and the opaque node id
// stays a detail of the query that needs one.
func (p ghProject) item() core.Item {
	return core.Item{ID: strconv.Itoa(p.Number), Name: p.Title}
}

// ghStatusField is the single-select field GitHub Projects calls Status, whose
// options are the board's columns.
type ghStatusField struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Options []ghStatusOption `json:"options"`
}

// items is the columns, as observed items.
//
// An option is its display name and the id the provider gave it, and nothing
// else. Its colour and its description are the provider's own; recording them
// would make a hash of the observation change when somebody recoloured a
// column, and spec 06 §1 writes the desired option as a name alone.
func (f ghStatusField) items() []core.Item {
	items := make([]core.Item, 0, len(f.Options))
	for _, option := range f.Options {
		items = append(items, core.Item{ID: option.ID, Name: option.Name})
	}
	return items
}

// ghStatusOption is one column.
//
// The colour and the description are the provider's own and no observation
// records them, but a rewrite of the option set has to send them back: see
// [ghOptionInput].
type ghStatusOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// column is the option a transition is asking for. A target is either the
// tracker's own option id, which an observation carries, or the column's
// display name, which is all a configuration holds: `workflow.columns` is
// written by hand (spec 04 §2) and the lock records no id for a column flow
// never created. Names are folded because [engine.ColumnOf] reads a column
// back by a folded name, and a move flow could not read back would not be
// idempotent.
func (f ghStatusField) column(to core.ColumnID) (ghStatusOption, bool) {
	for _, option := range f.Options {
		if option.ID == string(to) {
			return option, true
		}
	}
	for _, option := range f.Options {
		if strings.EqualFold(option.Name, string(to)) {
			return option, true
		}
	}
	return ghStatusOption{}, false
}

// find returns the option with that display name. A board cannot hold two
// columns of one name, so a name identifies an option as surely as an id —
// which matters because a rewrite gives every option it touches a new id.
func (f ghStatusField) find(name string) (ghStatusOption, bool) {
	for _, option := range f.Options {
		if option.Name == name {
			return option, true
		}
	}
	return ghStatusOption{}, false
}

// identify returns the option a change is about: the one with its id, or the
// one with its name when the change carries no id. A change made from an
// observation carries the id; one made by hand need not.
func (f ghStatusField) identify(item core.Item) (ghStatusOption, bool) {
	if item.ID != "" {
		for _, option := range f.Options {
			if option.ID == item.ID {
				return option, true
			}
		}
		return ghStatusOption{}, false
	}
	return f.find(item.Name)
}

// names is the columns as a human would list them, for an error that has to
// say what the board does have.
func (f ghStatusField) names() []string {
	names := make([]string, 0, len(f.Options))
	for _, option := range f.Options {
		names = append(names, option.Name)
	}
	return names
}

// The rest of this file is the ticket side of the board: the items it holds,
// the issues behind them, and the one mutation that moves an item between
// columns (spec 11 §2, item 3.9).

// Page sizes for the ticket queries. The items connection is paged at the 100
// spec 11 §2 asks for, against a shared points budget. The connections inside
// one issue are bounded rather than paged: a ticket with more than twenty
// labels or five assignees is not one flow can say anything useful about, and
// an issue is on a handful of boards at most.
const ghItemPageSize = 100

// GitHub's own words for the state of an issue, and why it was closed.
const (
	ghIssueClosed       = "CLOSED"
	ghReasonCompleted   = "COMPLETED"
	ghReasonNotPlanned  = "NOT_PLANNED"
	ghStatusFieldName   = "Status"
	ghUpdateItemFieldOp = "updateProjectV2ItemFieldValue"
)

// ghIssueFields is the part of an Issue every ticket is built from. It is one
// string because the two queries that read an issue — the board's items and
// one issue by number — must build the same ticket from it, and a field
// present in one and missing from the other would make a ticket mean two
// things depending on how it was asked for.
const ghIssueFields = "id number title body url state stateReason closedAt updatedAt " +
	"repository{nameWithOwner} " +
	"labels(first:" + ghLabelPage + "){nodes{name}} " +
	"assignees(first:" + ghAssigneePage + "){nodes{login}} " +
	"linkedBranches(first:" + ghBranchPage + "){nodes{ref{name}}} " +
	"parent{number repository{nameWithOwner}}"

// The inner page sizes, as they appear in the document. They are literals
// rather than variables because they are flow's own bounds and never a
// caller's, and a literal is what makes [ghIssueFields] one constant string.
const (
	ghLabelPage     = "20"
	ghAssigneePage  = "5"
	ghBranchPage    = "10"
	ghIssueItemPage = "10"
)

// ghStatusValue is an item's value of the Status field. fieldValueByName
// answers with an empty object for a field of another type, which reads as an
// item with no column — the truthful answer for a board whose Status is not a
// single select.
const ghStatusValue = `fieldValueByName(name:"` + ghStatusFieldName + `"){` +
	`...on ProjectV2ItemFieldSingleSelectValue{optionId name}}`

// ghItemSelection is what both queries that read a board item ask of it. It is
// one string for the same reason [ghIssueFields] is: the two must build the
// same item, and a field present in one and missing from the other would make
// an item mean two things depending on how it was asked for.
//
// `isArchived` is the field that makes the point. `ProjectV2.items` leaves
// archived items out of the listing and `Issue.projectItems` returns them, so
// without asking, the same item is on the board or off it depending on which
// question flow happened to put.
const ghItemSelection = "id isArchived updatedAt " + ghStatusValue

// ghItemsQuery reads one page of the board's items, each with the issue behind
// it and the column it sits in.
func ghItemsQuery(ownerType string) string {
	return "query($login:String!,$number:Int!,$first:Int!,$after:String){" +
		ghOwnerField(ownerType) + "(login:$login){projectV2(number:$number){" +
		"items(first:$first,after:$after){" +
		"nodes{" + ghItemSelection + " content{...on Issue{" + ghIssueFields + "}}}" +
		"pageInfo{hasNextPage endCursor}}}}}"
}

// ghIssueItemQuery reads one issue and the board items holding it: the
// issue→item lookup a move needs, answered in the same document as the ticket
// itself so that reading a ticket costs one query however it is used.
const ghIssueItemQuery = "query($owner:String!,$name:String!,$number:Int!){" +
	"repository(owner:$owner,name:$name){issue(number:$number){" + ghIssueFields +
	" projectItems(first:" + ghIssueItemPage + "){" +
	"nodes{" + ghItemSelection + " project{number}}}}}}"

// ghMoveMutation sets one item's Status to one option.
const ghMoveMutation = "mutation($project:ID!,$item:ID!,$field:ID!,$option:String!){" +
	ghUpdateItemFieldOp + "(input:{projectId:$project,itemId:$item,fieldId:$field," +
	"value:{singleSelectOptionId:$option}}){projectV2Item{id}}}"

// ghItemFields is what both queries report about a board item: which item it
// is, whether the board still shows it, when it last changed, and the column
// it is in.
type ghItemFields struct {
	ID         string        `json:"id"`
	IsArchived bool          `json:"isArchived"`
	UpdatedAt  time.Time     `json:"updatedAt"`
	Status     *ghItemStatus `json:"fieldValueByName"`
}

// optionID is the column the item sits in, empty when its Status is unset.
func (f ghItemFields) optionID() string {
	if f.Status == nil {
		return ""
	}
	return f.Status.OptionID
}

// ghItemStatus is an item's single-select Status value.
type ghItemStatus struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
}

// ghItem is one item of the board, with whatever it holds.
type ghItem struct {
	ghItemFields
	// Content is the issue, pull request or draft the item is of. Only an
	// issue is a ticket; anything else decodes with no number.
	Content *ghIssue `json:"content"`
}

// ghIssueItem is one item as the issue→item lookup reports it: the same
// fields, and which board it is on.
type ghIssueItem struct {
	ghItemFields
	Project *ghItemProject `json:"project"`
}

// ghItemProject is the board an item belongs to, by the number flow records.
type ghItemProject struct {
	Number int `json:"number"`
}

// ghIssue is an issue as flow reads it.
type ghIssue struct {
	ID          string    `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	URL         string    `json:"url"`
	State       string    `json:"state"`
	StateReason string    `json:"stateReason"`
	ClosedAt    time.Time `json:"closedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Repository  ghRepoRef `json:"repository"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	LinkedBranches struct {
		Nodes []struct {
			Ref *struct {
				Name string `json:"name"`
			} `json:"ref"`
		} `json:"nodes"`
	} `json:"linkedBranches"`
	Parent *ghIssueRef `json:"parent"`
}

// ghRepoRef is a repository as owner/name.
type ghRepoRef struct {
	NameWithOwner string `json:"nameWithOwner"`
}

// ghIssueRef is another issue referred to from this one: the sub-issue parent
// the Definition of Ready reads (spec 04 §7).
type ghIssueRef struct {
	Number     int       `json:"number"`
	Repository ghRepoRef `json:"repository"`
}

// ghIssueResult is the answer to [ghIssueItemQuery]. Both the repository and
// the issue are null when there is no such thing, which is how GraphQL says
// not found for a field that is allowed to be null.
type ghIssueResult struct {
	Repository *struct {
		Issue *ghIssueWithItems `json:"issue"`
	} `json:"repository"`
}

// issue returns the issue the lookup found, or nil.
func (r ghIssueResult) issue() *ghIssueWithItems {
	if r.Repository == nil || r.Repository.Issue == nil || r.Repository.Issue.Number <= 0 {
		return nil
	}
	return r.Repository.Issue
}

// ghIssueWithItems is one issue and the board items holding it. The issue is
// embedded rather than repeated: the lookup and the board's own items query
// must build the same ticket out of the same fields.
type ghIssueWithItems struct {
	ghIssue
	ProjectItems struct {
		Nodes []ghIssueItem `json:"nodes"`
	} `json:"projectItems"`
}

// on returns the item holding this issue on the board with that number, and
// whether there is one. An issue may be on many boards; only one of them is
// the board flow was pointed at.
func (w ghIssueWithItems) on(number int) (ghItemFields, bool) {
	for _, item := range w.ProjectItems.Nodes {
		if item.Project != nil && item.Project.Number == number && item.ID != "" {
			return item.ghItemFields, true
		}
	}
	return ghItemFields{}, false
}

// ghItemsResult is the answer to [ghItemsQuery].
type ghItemsResult struct {
	User         *ghItemsHolder `json:"user"`
	Organization *ghItemsHolder `json:"organization"`
}

// owner returns whichever owner the query asked for.
func (r ghItemsResult) owner() ghItemsHolder {
	switch {
	case r.User != nil:
		return *r.User
	case r.Organization != nil:
		return *r.Organization
	default:
		return ghItemsHolder{}
	}
}

// ghItemsHolder is the owner's one board, as the items query sees it.
type ghItemsHolder struct {
	ProjectV2 *struct {
		Items ghItemConnection `json:"items"`
	} `json:"projectV2"`
}

// items returns the page of items, empty when there is no such board.
func (h ghItemsHolder) items() ghItemConnection {
	if h.ProjectV2 == nil {
		return ghItemConnection{}
	}
	return h.ProjectV2.Items
}

// ghItemConnection is one page of a board's items.
type ghItemConnection struct {
	Nodes    []ghItem   `json:"nodes"`
	PageInfo ghPageInfo `json:"pageInfo"`
}

// ghTicket builds a ticket from an issue and the board item holding it, where
// here is the repository flow is configured for.
//
// The ticket's id is the issue's own node id. The item's id identifies where
// the issue is pinned rather than what it is: it changes when somebody removes
// the issue from the board and adds it again, and an issue on no board has
// none at all. What the mutations need is resolved when they run.
func ghTicket(issue ghIssue, item ghItemFields, here string) core.Ticket {
	ticket := core.Ticket{
		Key:       ghKey(issue.Repository.NameWithOwner, here, issue.Number),
		Repo:      issue.Repository.NameWithOwner,
		ID:        issue.ID,
		URL:       issue.URL,
		Title:     issue.Title,
		Body:      issue.Body,
		Closed:    strings.EqualFold(issue.State, ghIssueClosed),
		Archived:  item.IsArchived,
		ClosedAt:  issue.ClosedAt,
		UpdatedAt: issue.UpdatedAt,
	}
	if ticket.Closed {
		ticket.CloseReason = ghCloseReason(issue.StateReason)
	}
	// The ticket last changed when either half of it did: editing the
	// issue does not touch the item, and moving the item does not touch
	// the issue, and a window that missed either would miss work.
	if item.UpdatedAt.After(ticket.UpdatedAt) {
		ticket.UpdatedAt = item.UpdatedAt
	}
	if option := item.optionID(); option != "" {
		ticket.ColumnID = core.ColumnID(option)
		ticket.Status = core.Status{ID: option, Name: item.Status.Name}
	}
	for _, label := range issue.Labels.Nodes {
		if label.Name != "" {
			ticket.Labels = append(ticket.Labels, label.Name)
		}
	}
	for _, assignee := range issue.Assignees.Nodes {
		if assignee.Login != "" {
			ticket.Assignees = append(ticket.Assignees, assignee.Login)
		}
	}
	// A linked branch whose ref is null is one somebody has since deleted:
	// GitHub keeps the link and drops the reference. The work it stands for
	// is over, and a branch flow cannot name is not a branch it can report.
	for _, linked := range issue.LinkedBranches.Nodes {
		if linked.Ref != nil && linked.Ref.Name != "" {
			ticket.Branches = append(ticket.Branches, linked.Ref.Name)
		}
	}
	if issue.Parent != nil && issue.Parent.Number > 0 {
		ticket.Parent = ghKey(issue.Parent.Repository.NameWithOwner, here, issue.Parent.Number)
	}
	return ticket
}

// ghCloseReason is the close reason GitHub's state reason means. A reason flow
// has no name for is no reason: spec 07 §6 reads only whether the work was
// abandoned, and inventing a third answer would make a check say something the
// tracker did not.
func ghCloseReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case ghReasonCompleted:
		return core.ClosedCompleted
	case ghReasonNotPlanned:
		return core.ClosedNotPlanned
	default:
		return ""
	}
}

// ghKey is the ticket key of an issue: "#7" in the repository flow is
// configured for, and "owner/repo#7" anywhere else — the form GitHub itself
// qualifies a reference with, and the only one the tracker can resolve later
// without guessing which repository was meant (spec 11 §2).
func ghKey(slug, here string, number int) string {
	key := "#" + strconv.Itoa(number)
	if slug != "" && !strings.EqualFold(slug, here) {
		key = slug + key
	}
	return key
}

// ghParseKey splits a ticket key into the repository holding it and the issue
// number, with here standing for the repository flow is configured for.
//
// Only the two forms [ghKey] writes are keys. A bare number is not: a tracker
// that accepted one would resolve a key from somewhere else — a Jira key, a
// commit subject, a pull request number — against its own board, and the
// mistake would be silent.
func ghParseKey(key, here string) (owner, name string, number int, err error) {
	refuse := func(why string, args ...any) (string, string, int, error) {
		return "", "", 0, core.Errorf(core.ErrValidation, "read %q as a ticket", key).
			WithWhy(why, args...).
			WithFix("write a ticket as #7, or as owner/repo#7 for an issue in another repository")
	}
	trimmed := strings.TrimSpace(key)
	at := strings.Index(trimmed, "#")
	if at < 0 || trimmed == "" {
		return refuse("it has no issue number")
	}
	slug := trimmed[:at]
	parsed, convErr := strconv.Atoi(trimmed[at+1:])
	if convErr != nil || parsed <= 0 {
		return refuse("%q is not an issue number", trimmed[at+1:])
	}
	if slug == "" {
		if here == "" {
			return refuse("it names no repository, and flow has not been told which one holds it")
		}
		slug = here
	}
	repoOwner, repoName, ok := strings.Cut(slug, "/")
	if !ok || repoOwner == "" || repoName == "" || strings.Contains(repoName, "/") {
		return refuse("%q does not name a repository as owner/name", slug)
	}
	return repoOwner, repoName, parsed, nil
}

// ghMoveResult is the answer to [ghMoveMutation]. The item it returns is read,
// because "the mutation either happened or came back as a GraphQL error" is
// not true: asked to set the Status of an archived item, GitHub answers
// `{"updateProjectV2ItemFieldValue": null}` with no errors at all and changes
// nothing. The id coming back is the only evidence in the response that the
// write landed.
type ghMoveResult struct {
	UpdateProjectV2ItemFieldValue struct {
		ProjectV2Item struct {
			ID string `json:"id"`
		} `json:"projectV2Item"`
	} `json:"updateProjectV2ItemFieldValue"`
}

// The column rewrite (spec 06 §4, spec 11 §2, item 3.10).

const (
	// ghUpdateFieldOp is the mutation that edits a field. There is no
	// mutation that adds one option to a single select: the input carries
	// the option set the field is to end up with, and GitHub replaces
	// what is there with it.
	ghUpdateFieldOp = "updateProjectV2Field"
	// ghOptionEditLimit is the largest board whose columns flow will
	// rewrite. The rewrite is only safe because flow reads every item
	// value first and puts back whatever the provider cleared, and a
	// board bigger than this is one flow cannot promise to restore in one
	// pass — so the edit becomes a human's (spec 06 §4).
	ghOptionEditLimit = 500
	// ghDefaultOptionColour is the colour a column flow adds is given.
	// GitHub's input makes the colour mandatory and a flow configuration
	// has no opinion about it, so a new column is grey; whoever wants
	// another one picks it in the UI, and flow sends it back unchanged
	// ever after.
	ghDefaultOptionColour = "GRAY"
)

// ghOptionsMutation rewrites a single-select field's option set.
const ghOptionsMutation = "mutation($field:ID!,$options:[ProjectV2SingleSelectFieldOptionInput!]!){" +
	ghUpdateFieldOp + "(input:{fieldId:$field,singleSelectOptions:$options}){" +
	"projectV2Field{...on ProjectV2SingleSelectField{id}}}}"

// ghOptionInput is one option as the rewrite sends it.
//
// The id is what makes the edit an edit: GitHub keeps an option's identity,
// and with it every item's value, when the option comes back with its own id.
// A provider that does not know the field — an older GitHub Enterprise Server
// — refuses the document before performing any of it, which is why flow sends
// the id rather than hoping: a rewrite that recreated the options would also
// break the board's own automations, which name the option they move an item
// to by id. Name, colour and description are mandatory, so an option flow is
// not changing is sent back exactly as it stands, and a description nobody
// wrote is sent as the empty string rather than left out.
type ghOptionInput struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// ghOptionInputs is an option set as the rewrite sends it.
func ghOptionInputs(options []ghStatusOption) []ghOptionInput {
	inputs := make([]ghOptionInput, 0, len(options))
	for _, option := range options {
		inputs = append(inputs, ghOptionInput{
			ID:          option.ID,
			Name:        option.Name,
			Color:       ghOr(option.Color, ghDefaultOptionColour),
			Description: option.Description,
		})
	}
	return inputs
}

// ghFieldResult is the answer to [ghOptionsMutation]. The field is read back
// from the board afterwards rather than out of this, because the rewrite has
// to be verified against what the board says it is now.
type ghFieldResult struct {
	UpdateProjectV2Field struct {
		ProjectV2Field struct {
			ID string `json:"id"`
		} `json:"projectV2Field"`
	} `json:"updateProjectV2Field"`
}

// ghItemValuesQuery reads one page of the board's items and nothing but the
// column each sits in.
//
// It is the snapshot a rewrite is restored from, so it covers every item of
// the board rather than every ticket: a draft and a pull request hold a
// column too, and losing one of those values would be as much a loss as
// losing an issue's.
func ghItemValuesQuery(ownerType string) string {
	return "query($login:String!,$number:Int!,$first:Int!,$after:String){" +
		ghOwnerField(ownerType) + "(login:$login){projectV2(number:$number){" +
		"items(first:$first,after:$after){nodes{id " + ghStatusValue + "}" +
		"pageInfo{hasNextPage endCursor}}}}}"
}

// ghItemValue is one item of the board and the column it was in, by name.
//
// By name, because the rewrite is exactly what makes the id meaningless: the
// option the item pointed at is gone, and the column it belongs in is the one
// called what that one was called.
type ghItemValue struct {
	ID   string
	Name string
}

// ghOr is the first of two strings that is set.
func ghOr(first, second string) string {
	if first != "" {
		return first
	}
	return second
}

// Labels (spec 11 §2, item 3.11).
//
// Labels are the one thing this adapter reads over REST. They belong to the
// repository rather than to the board — a project has no labels of its own —
// and GitHub's REST labels endpoints are a complete set where GraphQL's are
// not: there is no mutation to delete a label by name, and `createLabel`
// needed a preview media type for most of its life. A tracker whose tickets
// are issues is entitled to the issues' own API.

const (
	// ghLabelPageSize is how many labels one page carries. GitHub's
	// maximum is 100 and a repository with more than that is ordinary, so
	// the reader pages.
	ghLabelPageSize = 100
	// ghDefaultLabelColour is the colour a label flow adds is given, as
	// six hex digits without the leading hash GitHub does not accept. A
	// flow configuration names labels and says nothing about how they
	// look (spec 03 §1), so whoever wants another colour picks it and
	// flow leaves it alone ever after.
	ghDefaultLabelColour = "ededed"
	// ghLabelColourField and ghLabelDescriptionField are what a label's
	// colour and description are called in an observed item's fields.
	ghLabelColourField      = "color"
	ghLabelDescriptionField = "description"
)

// ghLabel is one repository label as REST reports it.
type ghLabel struct {
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// item is the label as flow observes one: its name, the id the lock can
// record, and what GitHub knows about it that flow did not ask for.
func (l ghLabel) item() core.Item {
	item := core.Item{ID: l.NodeID, Name: l.Name, Fields: map[string]string{}}
	if l.Color != "" {
		item.Fields[ghLabelColourField] = strings.ToLower(strings.TrimPrefix(l.Color, "#"))
	}
	if l.Description != "" {
		item.Fields[ghLabelDescriptionField] = l.Description
	}
	return item
}

// ghLabelBody is the body a label is created or edited with. The name is sent
// as `new_name` on an edit, which is the only way GitHub renames one, so the
// two bodies differ and are built separately.
type ghLabelBody struct {
	Name        string  `json:"name,omitempty"`
	NewName     string  `json:"new_name,omitempty"`
	Color       string  `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
}

// Follow-ups, comments and an issue's state (spec 07 §4, spec 11 §2, item
// 3.11). These are all about issues rather than about the board, so they are
// GraphQL against the repository — with one call to `addProjectV2ItemById` at
// the end of filing, which is the only time flow puts an issue on the board.

const (
	// ghSearchPage is how many issues one marker search asks for. A
	// marker identifies one ticket, so anything past the first page would
	// be a repository with duplicates in it, which the search cannot fix.
	ghSearchPage = 20
	// ghCommentPage is how many comments one page of an issue carries.
	ghCommentPage = 50
	// The state reasons GitHub records a closure with.
	ghReasonCompletedInput  = "COMPLETED"
	ghReasonNotPlannedInput = "NOT_PLANNED"
)

// ghSearchQuery finds issues by a search expression. The search only narrows:
// every candidate's own body is read before it counts (spec 11 §2).
const ghSearchQuery = "query($query:String!,$first:Int!){" +
	"search(query:$query,type:ISSUE,first:$first){nodes{...on Issue{" + ghIssueFields + "}}}}"

// ghSearchResult is the answer to [ghSearchQuery].
type ghSearchResult struct {
	Search struct {
		Nodes []ghIssue `json:"nodes"`
	} `json:"search"`
}

// ghRepoLabelPage is how many of a repository's labels the filing query reads,
// to turn the names a follow-up asks for into the ids createIssue takes. It is
// a literal, like the other inner page sizes, because it is flow's own bound
// and never a caller's.
const ghRepoLabelPage = "100"

// ghRepoQuery reads what filing an issue needs: the repository's own id, and
// the ids of the labels a follow-up asks for by name.
const ghRepoQuery = "query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id " +
	"labels(first:" + ghRepoLabelPage + "){nodes{id name}}}}"

// ghRepoResult is the answer to [ghRepoQuery].
type ghRepoResult struct {
	Repository *struct {
		ID     string `json:"id"`
		Labels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
	} `json:"repository"`
}

// ghUserQuery reads one login's id, for assigning a follow-up to its owner.
const ghUserQuery = "query($login:String!){user(login:$login){id}}"

// ghUserResult is the answer to [ghUserQuery].
type ghUserResult struct {
	User *struct {
		ID string `json:"id"`
	} `json:"user"`
}

// ghCreateIssueMutation files an issue and reads back the ticket it became.
const ghCreateIssueMutation = "mutation($repo:ID!,$title:String!,$body:String!,$labels:[ID!],$assignees:[ID!]){" +
	"createIssue(input:{repositoryId:$repo,title:$title,body:$body,labelIds:$labels,assigneeIds:$assignees}){" +
	"issue{" + ghIssueFields + "}}}"

// ghCreateIssueResult is the answer to [ghCreateIssueMutation].
type ghCreateIssueResult struct {
	CreateIssue struct {
		Issue *ghIssue `json:"issue"`
	} `json:"createIssue"`
}

// ghAddItemMutation puts an issue on the board. It is idempotent at GitHub's
// end: adding content the project already holds answers with the item that
// was already there.
const ghAddItemMutation = "mutation($project:ID!,$content:ID!){" +
	"addProjectV2ItemById(input:{projectId:$project,contentId:$content}){item{id}}}"

// ghAddItemResult is the answer to [ghAddItemMutation].
type ghAddItemResult struct {
	Add struct {
		Item *struct {
			ID string `json:"id"`
		} `json:"item"`
	} `json:"addProjectV2ItemById"`
}

// ghCommentsQuery reads one page of an issue's comments, with the issue's own
// id so that a comment can be added without a second lookup.
const ghCommentsQuery = "query($owner:String!,$name:String!,$number:Int!,$first:Int!,$after:String){" +
	"repository(owner:$owner,name:$name){issue(number:$number){id " +
	"comments(first:$first,after:$after){nodes{id body createdAt author{login}}" +
	"pageInfo{hasNextPage endCursor}}}}}"

// ghComment is one comment of [ghCommentsQuery]. An author is a nullable
// actor: GitHub answers null for a comment whose account has gone.
type ghComment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Author    *struct {
		Login string `json:"login"`
	} `json:"author"`
}

// login is who wrote it, empty where the account is gone.
func (c ghComment) login() string {
	if c.Author == nil {
		return ""
	}
	return c.Author.Login
}

// ghCommentsResult is the answer to [ghCommentsQuery].
type ghCommentsResult struct {
	Repository *struct {
		Issue *struct {
			ID       string `json:"id"`
			Comments struct {
				Nodes    []ghComment `json:"nodes"`
				PageInfo ghPageInfo  `json:"pageInfo"`
			} `json:"comments"`
		} `json:"issue"`
	} `json:"repository"`
}

// The comment mutations: one to write a new comment, one to edit the comment
// already carrying flow's marker.
const (
	ghAddCommentMutation = "mutation($subject:ID!,$body:String!){" +
		"addComment(input:{subjectId:$subject,body:$body}){clientMutationId}}"
	ghUpdateCommentMutation = "mutation($comment:ID!,$body:String!){" +
		"updateIssueComment(input:{id:$comment,body:$body}){clientMutationId}}"
)

// The state mutations. GitHub records why an issue was closed and nothing at
// all about why one was reopened, so a reopening's reason is a comment the
// engine writes separately (spec 07 §4).
const (
	ghCloseIssueMutation = "mutation($issue:ID!,$reason:IssueClosedStateReason!){" +
		"closeIssue(input:{issueId:$issue,stateReason:$reason}){issue{id}}}"
	ghReopenIssueMutation = "mutation($issue:ID!){reopenIssue(input:{issueId:$issue}){issue{id}}}"
	ghUpdateIssueMutation = "mutation($issue:ID!,$title:String!,$body:String!){" +
		"updateIssue(input:{id:$issue,title:$title,body:$body}){issue{id}}}"
)

// ghMutationResult is the answer to a mutation whose result flow does not
// read. The document still asks for a field, because GraphQL has no empty
// selection set.
type ghMutationResult struct{}

// ghStateReason is the state reason GitHub takes for one of flow's. Anything
// flow has no name for is work that was not done: closing a follow-up flow
// could not verify as completed would be a lie recorded in the tracker.
func ghStateReason(reason string) string {
	if reason == core.ClosedCompleted {
		return ghReasonCompletedInput
	}
	return ghReasonNotPlannedInput
}

// ghSearchExpression is the search that narrows a marker hunt to the issues
// that could carry it: this repository's, and the marker's own text.
//
// Each field is its own quoted phrase rather than the whole marker being one,
// because a marker naming fewer fields must still find a ticket filed with
// more (spec 11 §1) and a marker renders its fields in alphabetical order —
// so `check=…` and `repo=…` are adjacent in one marker and separated in
// another, and a phrase search for the pair would miss. GitHub requires every
// term, which narrows; what actually decides is the body.
func ghSearchExpression(repo string, m core.Marker) string {
	parts := []string{"repo:" + repo, "is:issue", "in:body", strconv.Quote(core.MarkerPrefix + m.Kind)}
	for _, field := range slices.Sorted(maps.Keys(m.Fields)) {
		parts = append(parts, strconv.Quote(ghMarkerField(m.Kind, field, m.Fields[field])))
	}
	return strings.Join(parts, " ")
}

// ghMarkerField renders one of a marker's fields exactly as the marker itself
// renders it, quoting included, so that the phrase searched for is the text
// the body carries. It asks the marker rather than reimplementing the quoting,
// because a search built from a second rendering would drift from the first.
func ghMarkerField(kind, key, value string) string {
	return strings.TrimPrefix(core.NewMarker(kind, key, value).String(), core.NewMarker(kind).String()+" ")
}
