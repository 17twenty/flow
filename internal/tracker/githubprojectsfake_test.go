package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// ghProjectsFake serves GitHub's GraphQL schema over a
// [coretest.TrackerFixture], so that the adapter is asked the same questions
// about the same board as the in-memory fake (spec 14).
//
// Projects v2 has no REST API, so almost everything here is one endpoint
// answering several documents. It pages at its own small size and answers for
// one owner of one kind, which are the two things a GraphQL client has to get
// right and an in-memory fake can never exercise.
type ghProjectsFake struct {
	owner     string
	ownerType string
	// projects are the owner's boards, in the order the connection returns
	// them, and options the Status field of each, keyed by project number.
	// A project with no entry has no Status field at all, which is what a
	// board somebody deleted the field from looks like.
	projects []ghProjectNode
	options  map[int][]ghStatusOption
	pageSize int
	// scopes is what X-OAuth-Scopes reports, nil for a credential that is
	// not a classic token and sends no such header.
	scopes *string
	// refusalType and refusalMessage, when set, make every query fail the
	// way GraphQL fails: 200, no data, and a reason.
	refusalType    string
	refusalMessage string

	// repo is the repository the board's issues live in, and columns the
	// display name of every column the fixture names — including those the
	// Status field has no option for, which is a board somebody has moved
	// on from rather than a board that cannot answer.
	repo    string
	columns map[string]string

	mu      sync.Mutex
	queries []string
	paths   []string
	// items are the board's items, and now the clock a mutation moves. An
	// item's updatedAt changes when its field value does, which is how a
	// move that should not have happened is caught.
	items []*ghFakeItem
	now   time.Time
}

// ghFakeItem is one item of the board: an issue, where it currently sits, and
// when that last changed.
type ghFakeItem struct {
	id        string
	number    int
	optionID  string
	updatedAt time.Time
	ticket    core.Ticket
	// repo is the repository the issue is in, empty for the board's own,
	// and parentRepo the same for its parent. draft is an item holding
	// something that is not an issue at all.
	repo       string
	parentRepo string
	draft      bool
}

// in returns the repository an item's issue is in, which is the board's own
// unless the item says otherwise.
func (i *ghFakeItem) in(here string) string {
	if i.repo != "" {
		return i.repo
	}
	return here
}

// newGitHubProjectsServer stands the fake up over a fixture and returns it
// with its server. The fixture's candidates become the owner's projects,
// numbered in the order they are given, and its status_options observation
// becomes the Status field of the first of them.
func newGitHubProjectsServer(t *testing.T, fx coretest.TrackerFixture) (*ghProjectsFake, *httptest.Server) {
	t.Helper()
	fake := &ghProjectsFake{
		owner:     ghFixtureOwner,
		ownerType: OwnerUser,
		options:   map[int][]ghStatusOption{},
		columns:   map[string]string{},
		repo:      ghFixtureOwner + "/widget",
		pageSize:  2,
		scopes:    ghScopesOf(fx.Permissions),
		now:       coretest.ContractNow,
	}
	for i, candidate := range fx.Candidates[core.ItemTrackerProject] {
		node := ghProjectNode{
			ID:               candidate.ID,
			Number:           i + 1,
			Title:            candidate.Name,
			ShortDescription: candidate.Detail,
		}
		node.Items.TotalCount = 10 + i
		fake.projects = append(fake.projects, node)
	}
	for _, item := range fx.Observations[core.ItemTrackerStatusOptions].Items {
		fake.options[1] = append(fake.options[1], ghStatusOption{ID: item.ID, Name: item.Name})
	}
	for id, name := range fx.Columns {
		fake.columns[string(id)] = name
	}
	for _, ticket := range fx.Tickets {
		number := ghFixtureNumber(t, ticket.Key)
		fake.items = append(fake.items, &ghFakeItem{
			id:        "PVTI_" + strconv.Itoa(number),
			number:    number,
			optionID:  string(ticket.ColumnID),
			updatedAt: ticket.UpdatedAt,
			ticket:    ticket,
		})
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// ghFixtureOwner is the login the fixture's board belongs to. A tracker
// fixture names no owner — it is about boards and tickets — so the GitHub
// fake supplies one.
const ghFixtureOwner = "acme"

// ghFixtureNumber is the issue number a fixture ticket's key names. A fixture
// key that is not one GitHub could have issued is the fixture's bug, not the
// adapter's, so it fails the test rather than being skipped.
func ghFixtureNumber(t *testing.T, key string) int {
	t.Helper()
	number, err := strconv.Atoi(strings.TrimPrefix(key, "#"))
	if err != nil || number <= 0 {
		t.Fatalf("the fixture ticket %q is not a GitHub issue key", key)
	}
	return number
}

// ghScopesOf reads the classic token scopes out of a fixture's permission
// evidence, or nil when the credential announced none.
func ghScopesOf(p core.Permissions) *string {
	for _, line := range p.Evidence {
		if rest, ok := strings.CutPrefix(line, ghScopeHeader+": "); ok {
			return &rest
		}
	}
	return nil
}

func (f *ghProjectsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	if f.scopes != nil {
		w.Header().Set(ghScopeHeader, *f.scopes)
	}
	switch strings.Trim(r.URL.Path, "/") {
	case "graphql":
		f.graphQL(w, r)
	case "user":
		f.writeJSON(w, http.StatusOK, map[string]any{"login": f.owner})
	default:
		f.fail(w, http.StatusNotFound, "Not Found")
	}
}

// graphQL answers whichever of flow's documents was sent.
func (f *ghProjectsFake) graphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		f.fail(w, http.StatusMethodNotAllowed, "a GraphQL query is sent with POST")
		return
	}
	var query core.GraphQLQuery
	if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
		f.fail(w, http.StatusBadRequest, "the query did not decode: "+err.Error())
		return
	}
	f.mu.Lock()
	f.queries = append(f.queries, query.Query)
	refusal, message := f.refusalType, f.refusalMessage
	f.mu.Unlock()
	if refusal != "" {
		f.refuse(w, refusal, message)
		return
	}

	if strings.HasPrefix(query.Query, "mutation") {
		f.mutate(w, query)
		return
	}
	if strings.Contains(query.Query, "repository(owner:") {
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"repository": f.issueLookup(query.Variables)},
		})
		return
	}

	field := "user"
	if strings.Contains(query.Query, "organization(login") {
		field = "organization"
	}
	// An owner of the other kind, or a login nobody has, is a null owner
	// rather than an error: that is what GitHub answers.
	if field != ghOwnerField(f.ownerType) || query.Variables["login"] != f.owner {
		f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{field: nil}})
		return
	}
	switch {
	case strings.Contains(query.Query, "items(first:"):
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{field: map[string]any{"projectV2": f.itemPage(query.Variables)}},
		})
	case strings.Contains(query.Query, "projectsV2("):
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{field: map[string]any{"projectsV2": f.page(query.Variables)}},
		})
	case strings.Contains(query.Query, "projectV2("):
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{field: map[string]any{"projectV2": f.project(query.Variables)}},
		})
	default:
		f.refuse(w, "NOT_IMPLEMENTED", "the fake knows no such query")
	}
}

// page renders one page of the owner's projects connection. A connection may
// hand back fewer nodes than were asked for, so the fake pages at its own
// small size: an adapter that reads the first page and stops would pass
// against one that always answered in full.
func (f *ghProjectsFake) page(variables map[string]any) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	from := 0
	if after, ok := variables["after"].(string); ok {
		if n, err := strconv.Atoi(after); err == nil {
			from = n
		}
	}
	rest := f.projects[min(from, len(f.projects)):]
	more := len(rest) > f.pageSize
	if more {
		rest = rest[:f.pageSize]
	}
	nodes := make([]any, 0, len(rest))
	for _, node := range rest {
		nodes = append(nodes, ghNodeJSON(node))
	}
	return map[string]any{
		"nodes": nodes,
		"pageInfo": map[string]any{
			"hasNextPage": more,
			"endCursor":   strconv.Itoa(from + len(rest)),
		},
	}
}

// project renders one board and its Status field, or null when the owner has
// no project with that number.
func (f *ghProjectsFake) project(variables map[string]any) any {
	raw, ok := variables["number"].(float64)
	if !ok {
		return nil
	}
	number := int(raw)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, node := range f.projects {
		if node.Number != number {
			continue
		}
		doc := ghNodeJSON(node)
		delete(doc, "items")
		doc["field"] = nil
		if options, ok := f.options[number]; ok {
			values := make([]any, 0, len(options))
			for _, option := range options {
				values = append(values, map[string]any{"id": option.ID, "name": option.Name})
			}
			doc["field"] = map[string]any{
				"id":      "PVTSSF_status",
				"name":    "Status",
				"options": values,
			}
		}
		return doc
	}
	return nil
}

// ghNodeJSON renders one project as GitHub's connection does.
func ghNodeJSON(node ghProjectNode) map[string]any {
	return map[string]any{
		"id":               node.ID,
		"number":           node.Number,
		"title":            node.Title,
		"shortDescription": node.ShortDescription,
		"closed":           node.Closed,
		"items":            map[string]any{"totalCount": node.Items.TotalCount},
	}
}

// refuse answers the way GraphQL refuses: 200, with the reason beside the
// data rather than in the status line.
func (f *ghProjectsFake) refuse(w http.ResponseWriter, errType, message string) {
	f.writeJSON(w, http.StatusOK, map[string]any{
		"data":   nil,
		"errors": []any{map[string]any{"type": errType, "message": message}},
	})
}

func (f *ghProjectsFake) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":%q}`, message)
}

func (f *ghProjectsFake) writeJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		f.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// asked reports how many GraphQL documents have been sent.
func (f *ghProjectsFake) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

// visited reports how many times a path was requested.
func (f *ghProjectsFake) visited(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, seen := range f.paths {
		if seen == path {
			count++
		}
	}
	return count
}

// itemPage renders one page of the board's items, each with the issue behind
// it. It pages at the fake's own small size: an adapter that read the first
// page and stopped would pass against a server that always answered in full.
func (f *ghProjectsFake) itemPage(variables map[string]any) any {
	if number, ok := variables["number"].(float64); !ok || int(number) != 1 {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	from := 0
	if after, ok := variables["after"].(string); ok {
		if n, err := strconv.Atoi(after); err == nil {
			from = n
		}
	}
	rest := f.items[min(from, len(f.items)):]
	more := len(rest) > f.pageSize
	if more {
		rest = rest[:f.pageSize]
	}
	nodes := make([]any, 0, len(rest))
	for _, item := range rest {
		doc := f.itemJSON(item)
		doc["content"] = f.issueJSON(item)
		nodes = append(nodes, doc)
	}
	return map[string]any{"items": map[string]any{
		"nodes": nodes,
		"pageInfo": map[string]any{
			"hasNextPage": more,
			"endCursor":   strconv.Itoa(from + len(rest)),
		},
	}}
}

// issueLookup answers the issue→item query: one issue of one repository, and
// the board items holding it. A repository the fake does not serve and an
// issue nobody filed are both null, which is how GraphQL says not found.
func (f *ghProjectsFake) issueLookup(variables map[string]any) any {
	owner, _ := variables["owner"].(string)
	name, _ := variables["name"].(string)
	raw, ok := variables["number"].(float64)
	if !ok {
		return nil
	}
	slug := owner + "/" + name
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.serves(slug) {
		return nil
	}
	for _, item := range f.items {
		if item.number != int(raw) || !strings.EqualFold(item.in(f.repo), slug) {
			continue
		}
		doc := f.issueJSON(item)
		node := f.itemJSON(item)
		node["project"] = map[string]any{"number": 1}
		// The issue is on another board too, so that an adapter taking
		// the first item it is handed is caught.
		other := map[string]any{"id": "PVTI_other", "updatedAt": f.now.Format(time.RFC3339), "project": map[string]any{"number": 99}}
		other["fieldValueByName"] = map[string]any{"optionId": "opt-elsewhere", "name": "Elsewhere"}
		doc["projectItems"] = map[string]any{"nodes": []any{other, node}}
		return map[string]any{"issue": doc}
	}
	return map[string]any{"issue": nil}
}

// serves reports whether the fake has a repository of that name at all. One it
// has never heard of is a null repository, which is how GraphQL says that the
// name in the query is nobody's.
func (f *ghProjectsFake) serves(slug string) bool {
	if strings.EqualFold(slug, f.repo) {
		return true
	}
	return slices.ContainsFunc(f.items, func(i *ghFakeItem) bool {
		return i.repo != "" && strings.EqualFold(i.repo, slug)
	})
}

// mutate performs the one mutation flow sends: setting an item's Status.
func (f *ghProjectsFake) mutate(w http.ResponseWriter, query core.GraphQLQuery) {
	if !strings.Contains(query.Query, ghUpdateItemFieldOp) {
		f.refuse(w, "NOT_IMPLEMENTED", "the fake knows no such mutation")
		return
	}
	itemID, _ := query.Variables["item"].(string)
	option, _ := query.Variables["option"].(string)
	field, _ := query.Variables["field"].(string)
	project, _ := query.Variables["project"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	if field != ghFakeStatusFieldID {
		f.refuse(w, "NOT_FOUND", "no field "+field)
		return
	}
	if len(f.projects) == 0 || project != f.projects[0].ID {
		f.refuse(w, "NOT_FOUND", "no project "+project)
		return
	}
	if !slices.ContainsFunc(f.options[1], func(o ghStatusOption) bool { return o.ID == option }) {
		f.refuse(w, "NOT_FOUND", "no option "+option)
		return
	}
	for _, item := range f.items {
		if item.id != itemID {
			continue
		}
		f.now = f.now.Add(time.Minute)
		item.optionID = option
		item.updatedAt = f.now
		f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			ghUpdateItemFieldOp: map[string]any{"projectV2Item": map[string]any{"id": item.id}},
		}})
		return
	}
	f.refuse(w, "NOT_FOUND", "no item "+itemID)
}

// ghFakeStatusFieldID is the id of the board's Status field, which the project
// query reports and a move has to send back.
const ghFakeStatusFieldID = "PVTSSF_status"

// itemJSON renders the part of an item both queries ask for. The caller adds
// whichever of content and project its own document named.
func (f *ghProjectsFake) itemJSON(item *ghFakeItem) map[string]any {
	doc := map[string]any{
		"id":               item.id,
		"updatedAt":        item.updatedAt.Format(time.RFC3339),
		"fieldValueByName": nil,
	}
	if item.optionID != "" {
		doc["fieldValueByName"] = map[string]any{
			"optionId": item.optionID,
			"name":     f.columns[item.optionID],
		}
	}
	return doc
}

// issueJSON renders the issue behind an item.
func (f *ghProjectsFake) issueJSON(item *ghFakeItem) map[string]any {
	if item.draft {
		// An item holding a draft or a pull request matches no inline
		// fragment, and GraphQL answers with an empty object rather
		// than with null.
		return map[string]any{}
	}
	ticket := item.ticket
	doc := map[string]any{
		"id":          ticket.ID,
		"number":      item.number,
		"title":       ticket.Title,
		"body":        ticket.Body,
		"url":         ticket.URL,
		"state":       "OPEN",
		"stateReason": nil,
		"closedAt":    nil,
		"updatedAt":   ticket.UpdatedAt.Format(time.RFC3339),
		"repository":  map[string]any{"nameWithOwner": item.in(f.repo)},
		"labels":      ghNameNodes("name", ticket.Labels),
		"assignees":   ghNameNodes("login", ticket.Assignees),
		"parent":      nil,
	}
	if ticket.Closed {
		doc["state"] = "CLOSED"
		doc["closedAt"] = ticket.ClosedAt.Format(time.RFC3339)
		switch ticket.CloseReason {
		case core.ClosedNotPlanned:
			doc["stateReason"] = "NOT_PLANNED"
		default:
			doc["stateReason"] = "COMPLETED"
		}
	}
	if parent := strings.TrimPrefix(ticket.Parent, "#"); parent != "" {
		if number, err := strconv.Atoi(parent); err == nil {
			doc["parent"] = map[string]any{
				"number":     number,
				"repository": map[string]any{"nameWithOwner": ghOr(item.parentRepo, f.repo)},
			}
		}
	}
	return doc
}

// ghNameNodes renders a list of strings as a GraphQL connection of one field.
func ghNameNodes(field string, values []string) map[string]any {
	nodes := make([]any, 0, len(values))
	for _, value := range values {
		nodes = append(nodes, map[string]any{field: value})
	}
	return map[string]any{"nodes": nodes}
}

// itemIn returns the column an item is in, for a test to check the fake's own
// state rather than the adapter's report of it.
func (f *ghProjectsFake) itemIn(number int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item.number == number {
			return item.optionID
		}
	}
	return ""
}

// ghOr is the first of two names that is set.
func ghOr(first, second string) string {
	if first != "" {
		return first
	}
	return second
}

// sent returns the GraphQL documents the fake has been asked, in order.
func (f *ghProjectsFake) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}
