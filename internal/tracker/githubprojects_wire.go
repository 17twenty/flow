package tracker

import (
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// This file is the wire: the GraphQL documents flow sends to GitHub, the
// shapes their answers arrive in, and the translation of those shapes into
// flow's own vocabulary. Projects v2 has no REST API at all, so everything
// the adapter reads about a board is a query (spec 11 §2).

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
func ghProjectQuery(ownerType string) string {
	return "query($login:String!,$number:Int!){" +
		ghOwnerField(ownerType) + "(login:$login){" +
		"projectV2(number:$number){id number title shortDescription closed " +
		`field(name:"Status"){...on ProjectV2SingleSelectField{id name options{id name}}}` +
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
	ID               string `json:"id"`
	Number           int    `json:"number"`
	Title            string `json:"title"`
	ShortDescription string `json:"shortDescription"`
	Closed           bool   `json:"closed"`
	Items            struct {
		TotalCount int `json:"totalCount"`
	} `json:"items"`
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
type ghStatusOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
