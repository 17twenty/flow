package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

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

	mu      sync.Mutex
	queries []string
	paths   []string
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
		pageSize:  2,
		scopes:    ghScopesOf(fx.Permissions),
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
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// ghFixtureOwner is the login the fixture's board belongs to. A tracker
// fixture names no owner — it is about boards and tickets — so the GitHub
// fake supplies one.
const ghFixtureOwner = "acme"

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
