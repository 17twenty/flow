package tracker

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	// workflows is the Workflows panel: the built-in automations the board
	// has configured, and whether each is switched on. A name with no entry
	// is one GitHub leaves out of the connection altogether, which is what
	// a workflow nobody ever touched looks like.
	workflows map[string]bool

	// totalItems overrides how many items the board reports holding, for
	// a test about a board larger than one can be built in memory.
	totalItems int
	// wipesIDs makes the fake ignore the option ids a rewrite sends, so
	// that every option is recreated and every item value cleared: the
	// hazard spec 11 §2 names, and what an older server does.
	wipesIDs bool
	// deafToMoves makes setting an item's value answer without doing it,
	// which is the one thing a restore has to notice.
	deafToMoves bool
	// nullOnMoves makes setting an item's value answer with no item at
	// all, which is what GitHub does for an item archived between the read
	// and the write: accepted, no errors, nothing written.
	nullOnMoves bool
	// optionsMade counts the options the fake has issued ids for, so that
	// every option it creates is a new one.
	optionsMade int

	// labelPageSize is how many labels one page of the REST endpoint
	// carries, kept small so that an adapter which read the first page
	// and stopped fails here.
	labelPageSize int
	// commentPageSize is the same for an issue's comments. blind makes the
	// search index answer with nothing at all, which is the index as it is
	// for the minutes after an issue is filed; loose makes it answer with
	// every issue in the repository, which is the index being an index
	// rather than a matcher.
	commentPageSize int
	blind           bool
	loose           bool

	mu      sync.Mutex
	queries []string
	paths   []string
	// agents counts the User-Agent of every request, so that a call going
	// out anonymously is visible.
	agents map[string]int
	// labels are the repository's labels, in the order REST returns them,
	// and labelsMade counts the ids issued so that every label created is
	// a new one.
	labels     []ghLabel
	labelsMade int
	// comments are each issue's comments by issue number, and issuesMade
	// and commentsMade count what the fake has created, so that every id
	// it issues is a new one.
	comments     map[int][]*ghFakeComment
	issuesMade   int
	commentsMade int
	// items are the board's items, and now the clock a mutation moves. An
	// item's updatedAt changes when its field value does, which is how a
	// move that should not have happened is caught.
	items []*ghFakeItem
	now   time.Time
}

// ghFakeComment is one comment on an issue.
type ghFakeComment struct {
	id        string
	body      string
	author    string
	createdAt time.Time
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
	// something that is not an issue at all, and offBoard an issue the
	// repository has that the project does not hold — which is what an
	// issue looks like between being filed and being added.
	// archived is an item the board has put out of sight: GitHub leaves it
	// out of ProjectV2.items, returns it from Issue.projectItems, and
	// silently declines to write to it.
	// goneBranches are branches the issue is still linked to and nobody can
	// reach any more: GitHub keeps the link and answers `ref: null`.
	repo         string
	parentRepo   string
	goneBranches []string
	draft        bool
	offBoard     bool
	archived     bool
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
		owner:           ghFixtureOwner,
		ownerType:       OwnerUser,
		options:         map[int][]ghStatusOption{},
		columns:         map[string]string{},
		comments:        map[int][]*ghFakeComment{},
		agents:          map[string]int{},
		repo:            ghFixtureOwner + "/widget",
		pageSize:        2,
		labelPageSize:   1,
		commentPageSize: 1,
		scopes:          ghScopesOf(fx.Permissions),
		now:             coretest.ContractNow,
		// What GitHub switches on for a new project, and nothing more:
		// "Code changes requested" is one of the four a person has to
		// turn on themselves, and a fake that had it on would make the
		// commonest board in the world unreachable from the tests.
		workflows: map[string]bool{
			"Item closed":         true,
			"Pull request merged": true,
		},
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
	for i, item := range fx.Observations[core.ItemTrackerStatusOptions].Items {
		// A real option has a colour and may have a description, and
		// neither is in the fixture: an observation does not record
		// them. The fake gives them anyway, because a rewrite that
		// dropped them would be invisible against a board where every
		// option was grey and silent.
		option := ghStatusOption{
			ID:    item.ID,
			Name:  item.Name,
			Color: ghFakeColours[i%len(ghFakeColours)],
		}
		if i%2 == 0 {
			option.Description = "what " + item.Name + " means"
		}
		fake.options[1] = append(fake.options[1], option)
	}
	for _, item := range fx.Observations[core.ItemTrackerLabels].Items {
		fake.labels = append(fake.labels, ghLabel{
			NodeID:      item.ID,
			Name:        item.Name,
			Color:       item.Field("color"),
			Description: item.Field("description"),
		})
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
			archived:  ticket.Archived,
			ticket:    ticket,
		})
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// ghFakeColours are the colours the fake's columns come in, so that a rewrite
// which did not send an option's own colour back shows up as a recolour.
var ghFakeColours = []string{"GREEN", "YELLOW", "PURPLE", "GRAY"}

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
	f.agents[r.Header.Get("User-Agent")]++
	f.mu.Unlock()
	if f.scopes != nil {
		w.Header().Set(ghScopeHeader, *f.scopes)
	}
	// GitHub is asked for a dated API and a media type on everything,
	// GraphQL included, and a request that pins neither is served by
	// whatever GitHub happens to mean today. The fake refuses it here so
	// that a call built without the headers fails every test it is in,
	// rather than only the one test that remembered to look.
	if r.Header.Get("X-GitHub-Api-Version") != ghAPIVersion {
		f.fail(w, http.StatusBadRequest, "no API version was pinned")
		return
	}
	if accept := r.Header.Get("Accept"); accept != ghAccept {
		f.fail(w, http.StatusNotAcceptable, "unexpected Accept "+accept)
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	switch {
	case path == "graphql":
		f.graphQL(w, r)
	case path == "user":
		f.writeJSON(w, http.StatusOK, map[string]any{"login": f.owner})
	case path == "repos/"+f.repo+"/labels":
		f.labelsEndpoint(w, r)
	case strings.HasPrefix(path, "repos/"+f.repo+"/labels/"):
		f.labelEndpoint(w, r, strings.TrimPrefix(path, "repos/"+f.repo+"/labels/"))
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
	if strings.Contains(query.Query, "search(query:") {
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"search": f.search(query.Query, query.Variables)},
		})
		return
	}
	if strings.Contains(query.Query, "repository(owner:") {
		// Three documents ask about a repository, and what tells them
		// apart is what they ask for: the issue's board items, its
		// comments, or the repository's own id.
		var answer any
		switch {
		case strings.Contains(query.Query, "projectItems("):
			answer = f.issueLookup(query.Query, query.Variables)
		case strings.Contains(query.Query, "comments(first:"):
			answer = f.commentPage(query.Variables)
		default:
			answer = f.repoLookup(query.Variables)
		}
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"repository": answer},
		})
		return
	}
	if strings.Contains(query.Query, "user(login:$login){id}") {
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"user": f.userLookup(query.Variables)},
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
			"data": map[string]any{field: map[string]any{"projectV2": f.itemPage(query.Query, query.Variables)}},
		})
	case strings.Contains(query.Query, "projectsV2("):
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{field: map[string]any{"projectsV2": f.page(query.Variables)}},
		})
	case strings.Contains(query.Query, "projectV2("):
		f.writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{field: map[string]any{"projectV2": f.project(query.Query, query.Variables)}},
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
func (f *ghProjectsFake) project(document string, variables map[string]any) any {
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
		doc["items"] = map[string]any{"totalCount": f.count()}
		// Only what the document asked for: a fake that volunteers the
		// workflows connection answers a query that forgot to select it,
		// and the adapter would then read every board as fully automated.
		if strings.Contains(document, "workflows(") {
			nodes := make([]any, 0, len(f.workflows))
			for _, name := range slices.Sorted(maps.Keys(f.workflows)) {
				nodes = append(nodes, map[string]any{"name": name, "enabled": f.workflows[name]})
			}
			doc["workflows"] = map[string]any{"nodes": nodes}
		}
		doc["field"] = nil
		if options, ok := f.options[number]; ok {
			values := make([]any, 0, len(options))
			for _, option := range options {
				values = append(values, map[string]any{
					"id":          option.ID,
					"name":        option.Name,
					"color":       option.Color,
					"description": option.Description,
				})
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

// count is how many items the board says it holds: the ones the fake has that
// the project holds, unless a test has claimed a board too big to hold in one.
func (f *ghProjectsFake) count() int {
	if f.totalItems > 0 {
		return f.totalItems
	}
	held := 0
	for _, item := range f.items {
		if !item.offBoard && !item.archived {
			held++
		}
	}
	return held
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
func (f *ghProjectsFake) itemPage(document string, variables map[string]any) any {
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
	// Only what the project holds. An issue the repository has and the
	// board has not is not an item at all, which is what a follow-up
	// looks like until it is added.
	board := make([]*ghFakeItem, 0, len(f.items))
	for _, item := range f.items {
		if item.offBoard || item.archived {
			continue
		}
		board = append(board, item)
	}
	rest := board[min(from, len(board)):]
	more := len(rest) > f.pageSize
	if more {
		rest = rest[:f.pageSize]
	}
	nodes := make([]any, 0, len(rest))
	for _, item := range rest {
		doc := f.itemJSON(document, item)
		doc["content"] = f.issueJSON(document, item)
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
func (f *ghProjectsFake) issueLookup(document string, variables map[string]any) any {
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
		doc := f.issueJSON(document, item)
		if item.offBoard {
			// The repository has the issue; no project holds it.
			doc["projectItems"] = map[string]any{"nodes": []any{}}
			return map[string]any{"issue": doc}
		}
		node := f.itemJSON(document, item)
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

// mutate performs the two mutations flow sends: setting an item's Status, and
// rewriting the options of the Status field itself.
func (f *ghProjectsFake) mutate(w http.ResponseWriter, query core.GraphQLQuery) {
	switch {
	case strings.Contains(query.Query, ghUpdateItemFieldOp):
		f.setItemValue(w, query)
	case strings.Contains(query.Query, ghUpdateFieldOp):
		f.rewriteOptions(w, query)
	case strings.Contains(query.Query, "createIssue(input:"):
		f.createIssue(w, query)
	case strings.Contains(query.Query, "addProjectV2ItemById(input:"):
		f.addItem(w, query)
	case strings.Contains(query.Query, "addComment(input:"):
		f.addComment(w, query)
	case strings.Contains(query.Query, "updateIssueComment(input:"):
		f.updateComment(w, query)
	case strings.Contains(query.Query, "updateIssue(input:"):
		f.updateIssue(w, query)
	case strings.Contains(query.Query, "closeIssue(input:"), strings.Contains(query.Query, "reopenIssue(input:"):
		f.setIssueState(w, query)
	default:
		f.refuse(w, "NOT_IMPLEMENTED", "the fake knows no such mutation")
	}
}

// rewriteOptions replaces the Status field's option set, the way GitHub does.
//
// An option sent back with its own id keeps it, and one sent without gets a
// new one. Whatever is no longer there takes every item value pointing at it
// with it: that is the hazard, and it is the fake's job to inflict it.
func (f *ghProjectsFake) rewriteOptions(w http.ResponseWriter, query core.GraphQLQuery) {
	field, _ := query.Variables["field"].(string)
	payload, err := json.Marshal(query.Variables["options"])
	if err != nil {
		f.refuse(w, "INVALID", "the options did not encode: "+err.Error())
		return
	}
	var sent []ghOptionInput
	if err := json.Unmarshal(payload, &sent); err != nil {
		f.refuse(w, "INVALID", "the options are not options: "+err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if field != ghFakeStatusFieldID {
		f.refuse(w, "NOT_FOUND", "no field "+field)
		return
	}
	if len(sent) == 0 {
		// GitHub leaves the options alone when none are sent, which
		// flow must never rely on: it is the one input that means
		// something other than what it says.
		f.refuse(w, "INVALID", "singleSelectOptions was empty")
		return
	}
	next := make([]ghStatusOption, 0, len(sent))
	for _, option := range sent {
		if option.Name == "" || option.Color == "" {
			f.refuse(w, "INVALID", "every option needs a name and a colour")
			return
		}
		id := option.ID
		if id == "" || f.wipesIDs {
			f.optionsMade++
			id = "opt-made-" + strconv.Itoa(f.optionsMade)
		}
		next = append(next, ghStatusOption{
			ID:          id,
			Name:        option.Name,
			Color:       option.Color,
			Description: option.Description,
		})
	}
	had := f.options[1]
	f.options[1] = next
	for _, item := range f.items {
		// Only a value pointing at an option the field actually had is
		// cleared. An item sitting in a column the Status field does
		// not have at all is the fixture's board that somebody moved
		// on from, and a rewrite of the options says nothing about it.
		was := func(o ghStatusOption) bool { return o.ID == item.optionID }
		if item.optionID == "" || !slices.ContainsFunc(had, was) || slices.ContainsFunc(next, was) {
			continue
		}
		f.now = f.now.Add(time.Minute)
		item.optionID = ""
		item.updatedAt = f.now
	}
	f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		ghUpdateFieldOp: map[string]any{"projectV2Field": map[string]any{"id": field}},
	}})
}

// setItemValue puts one item in one column.
func (f *ghProjectsFake) setItemValue(w http.ResponseWriter, query core.GraphQLQuery) {
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
		if item.archived || f.nullOnMoves {
			// Exactly what GitHub does with an archived item: the
			// mutation is accepted, no error is reported, a null item
			// comes back and nothing is written.
			f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				ghUpdateItemFieldOp: map[string]any{"projectV2Item": nil},
			}})
			return
		}
		if f.deafToMoves {
			f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				ghUpdateItemFieldOp: map[string]any{"projectV2Item": map[string]any{"id": item.id}},
			}})
			return
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

// itemJSON renders the part of an item both queries ask for, and only the part
// they ask for. The caller adds whichever of content and project its own
// document named.
//
// Honouring the selection is the whole point of rendering it from the document
// rather than from the fake's own idea of an item: GraphQL answers with the
// fields that were asked for, so a fake that volunteers one lets the adapter
// read a field it never selected, and no test can tell. That is exactly how
// `isArchived` could have been left out of both queries with every test still
// green.
func (f *ghProjectsFake) itemJSON(document string, item *ghFakeItem) map[string]any {
	doc := map[string]any{
		"id":               item.id,
		"isArchived":       item.archived,
		"updatedAt":        item.updatedAt.Format(time.RFC3339),
		"fieldValueByName": nil,
	}
	if item.optionID != "" {
		doc["fieldValueByName"] = map[string]any{
			"optionId": item.optionID,
			"name":     f.optionName(item.optionID),
		}
	}
	selection := ghItemSelectionOf(document)
	for field := range doc {
		if !strings.Contains(selection, field) {
			delete(doc, field)
		}
	}
	return doc
}

// ghItemSelectionOf is the fields a document asks of a board item: everything
// between the `nodes{` of the one item list it reads and the first nested
// object, which in both queries is the issue or the project behind the item.
// It is a reader for two known shapes rather than a GraphQL parser, so a query
// that stops matching it reports every field as unasked-for and fails loudly.
func ghItemSelectionOf(document string) string {
	const list = "nodes{"
	at := strings.Index(document, list)
	if at < 0 {
		return ""
	}
	rest := document[at+len(list):]
	for _, nested := range []string{"content{", "project{"} {
		if at := strings.Index(rest, nested); at >= 0 {
			rest = rest[:at]
		}
	}
	return rest
}

// optionName is what the column with that id is called: the board's own
// options first, since a rewrite gives them new ids, and then the fixture's
// column names, which cover a column the Status field no longer has.
func (f *ghProjectsFake) optionName(id string) string {
	for _, option := range f.options[1] {
		if option.ID == id {
			return option.Name
		}
	}
	return f.columns[id]
}

// issueJSON renders the issue behind an item.
func (f *ghProjectsFake) issueJSON(document string, item *ghFakeItem) map[string]any {
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
	branches := make([]any, 0, len(ticket.Branches)+len(item.goneBranches))
	for _, name := range ticket.Branches {
		branches = append(branches, map[string]any{"ref": map[string]any{"name": name}})
	}
	for range item.goneBranches {
		branches = append(branches, map[string]any{"ref": nil})
	}
	doc["linkedBranches"] = map[string]any{"nodes": branches}
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
	selection := ghIssueSelectionOf(document)
	for field := range doc {
		if !strings.Contains(selection, " "+field+" ") {
			delete(doc, field)
		}
	}
	return doc
}

// ghIssueSelectionOf is the fields a document asks of an issue, with every
// nested selection taken out, so that what is left is the names of the issue's
// own fields and nothing a label or a parent happens to be called. The answer
// is padded with spaces at both ends so that a caller can ask for a whole
// word.
//
// Like [ghItemSelectionOf] it reads two known shapes rather than parsing
// GraphQL: a query that stops matching it reports every field as unasked-for
// and fails loudly, which is the direction an unreadable document should fail
// in.
func ghIssueSelectionOf(document string) string {
	for _, open := range []string{"...on Issue{", "issue(number:$number){", "issue{"} {
		at := strings.Index(document, open)
		if at < 0 {
			continue
		}
		return " " + ghFlatten(document[at+len(open):]) + " "
	}
	return ""
}

// ghFlatten returns the top level of one brace-delimited selection: every
// nested `{...}` and every field's `(...)` arguments are replaced by a space,
// and the text stops at the brace that closes the selection itself. What is
// left is the field names, separated by spaces.
func ghFlatten(rest string) string {
	var out strings.Builder
	depth, args := 0, 0
	for _, r := range rest {
		switch {
		case r == '(':
			args++
		case r == ')':
			args--
			out.WriteRune(' ')
		case args > 0:
		case r == '{':
			depth++
		case r == '}' && depth == 0:
			return out.String()
		case r == '}':
			depth--
			out.WriteRune(' ')
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return out.String()
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

// item is the fake's item for a ticket key, for a test that wants to change
// something about an issue the fixture already holds.
func (f *ghProjectsFake) item(t *testing.T, key string) *ghFakeItem {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item.ticket.Key == key {
			return item
		}
	}
	t.Fatalf("the fake holds no %s", key)
	return nil
}

// sent returns the GraphQL documents the fake has been asked, in order.
func (f *ghProjectsFake) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// columnOf is what the item holding that issue is in, by column name, for a
// test that asks the fake rather than the adapter where a card ended up.
func (f *ghProjectsFake) columnOf(number int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.items {
		if item.number == number {
			return f.optionName(item.optionID)
		}
	}
	return ""
}

// statusOptions is the board's Status options as they now stand.
func (f *ghProjectsFake) statusOptions() []ghStatusOption {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.options[1])
}

// mutations counts how many documents carrying that operation were sent.
func (f *ghProjectsFake) mutations(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, query := range f.queries {
		if strings.HasPrefix(query, "mutation") && strings.Contains(query, op) {
			count++
		}
	}
	return count
}

// labelsEndpoint is REST's collection of a repository's labels: GET lists
// them a page at a time, POST adds one.
func (f *ghProjectsFake) labelsEndpoint(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.listLabels(w, r)
	case http.MethodPost:
		f.createLabel(w, r)
	default:
		f.fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
	}
}

// listLabels answers one page, with a Link header when there is another. It
// pages at the fake's own small size whatever per_page asked for, because a
// server is entitled to answer with fewer than it was asked for and an
// adapter that assumed otherwise would stop early against a real one.
func (f *ghProjectsFake) listLabels(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	all := slices.Clone(f.labels)
	f.mu.Unlock()

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size := f.labelPageSize
	from := (page - 1) * size
	if from > len(all) {
		from = len(all)
	}
	to := min(from+size, len(all))
	if to < len(all) {
		next := *r.URL
		query := next.Query()
		query.Set("page", strconv.Itoa(page+1))
		next.RawQuery = query.Encode()
		next.Scheme, next.Host = "http", r.Host
		w.Header().Set("Link", `<`+next.String()+`>; rel="next"`)
	}
	f.writeJSON(w, http.StatusOK, all[from:to])
}

// createLabel adds a label, or refuses the way GitHub refuses a duplicate:
// 422, with the per-error code that says it is already there.
func (f *ghProjectsFake) createLabel(w http.ResponseWriter, r *http.Request) {
	var body ghLabelBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "the body is not JSON")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.ContainsFunc(f.labels, func(l ghLabel) bool { return strings.EqualFold(l.Name, body.Name) }) {
		f.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"message": "Validation Failed",
			"errors":  []any{map[string]any{"resource": "Label", "code": "already_exists", "field": "name"}},
		})
		return
	}
	f.labelsMade++
	made := ghLabel{
		NodeID: "LA_made_" + strconv.Itoa(f.labelsMade),
		Name:   body.Name,
		Color:  body.Color,
	}
	if body.Description != nil {
		made.Description = *body.Description
	}
	f.labels = append(f.labels, made)
	f.writeJSON(w, http.StatusCreated, made)
}

// labelEndpoint is one label by name: PATCH edits or renames it, DELETE
// removes it, and a name the repository has not got is 404.
func (f *ghProjectsFake) labelEndpoint(w http.ResponseWriter, r *http.Request, raw string) {
	name, err := url.PathUnescape(raw)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "the label name is not a path segment")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.labels, func(l ghLabel) bool { return l.Name == name })
	if at < 0 {
		f.fail(w, http.StatusNotFound, "Not Found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var body ghLabelBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.fail(w, http.StatusBadRequest, "the body is not JSON")
			return
		}
		if body.NewName != "" {
			f.labels[at].Name = body.NewName
		}
		if body.Color != "" {
			f.labels[at].Color = body.Color
		}
		if body.Description != nil {
			f.labels[at].Description = *body.Description
		}
		f.writeJSON(w, http.StatusOK, f.labels[at])
	case http.MethodDelete:
		f.labels = slices.Delete(f.labels, at, at+1)
		w.WriteHeader(http.StatusNoContent)
	default:
		f.fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
	}
}

// ghFakeRepoID is the node id of the repository the board's issues live in,
// and ghFakeUserID the id of a login GitHub knows.
const (
	ghFakeRepoID = "R_widget"
	ghFakeUserID = "U_maintainer"
)

// repoLookup answers the query that filing an issue begins with: the
// repository's own id, and the labels a follow-up names by name.
func (f *ghProjectsFake) repoLookup(variables map[string]any) any {
	owner, _ := variables["owner"].(string)
	name, _ := variables["name"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.EqualFold(owner+"/"+name, f.repo) {
		return nil
	}
	nodes := make([]any, 0, len(f.labels))
	for _, label := range f.labels {
		nodes = append(nodes, map[string]any{"id": label.NodeID, "name": label.Name})
	}
	return map[string]any{"id": ghFakeRepoID, "labels": map[string]any{"nodes": nodes}}
}

// userLookup answers the assignee query. Only the board's owner and a
// maintainer are people here; anybody else is a null user, which is what
// GitHub answers for a login nobody has.
func (f *ghProjectsFake) userLookup(variables map[string]any) any {
	login, _ := variables["login"].(string)
	if login != f.owner && login != "maintainer" {
		return nil
	}
	return map[string]any{"id": ghFakeUserID}
}

// search answers the marker hunt. It honours the repo: filter and requires
// every quoted phrase to be in the issue's body, which is roughly what
// GitHub's index does — and then answers with the whole issue, because the
// adapter is entitled to check the body itself.
//
// A blind fake answers with nothing at all, which is the index as it is for
// the minutes after an issue is filed.
func (f *ghProjectsFake) search(document string, variables map[string]any) any {
	expression, _ := variables["query"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	nodes := []any{}
	if f.blind {
		return map[string]any{"nodes": nodes}
	}
	phrases := ghQuotedPhrases(expression)
	for _, item := range f.items {
		if item.draft || !strings.Contains(expression, "repo:"+item.in(f.repo)+" ") {
			continue
		}
		if f.loose || ghContainsAll(item.ticket.Body, phrases) {
			nodes = append(nodes, f.issueJSON(document, item))
		}
	}
	return map[string]any{"nodes": nodes}
}

// ghContainsAll reports whether text carries every phrase.
func ghContainsAll(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if !strings.Contains(text, phrase) {
			return false
		}
	}
	return true
}

// ghQuotedPhrases pulls the quoted phrases out of a search expression.
func ghQuotedPhrases(expression string) []string {
	var phrases []string
	rest := expression
	for {
		_, after, ok := strings.Cut(rest, `"`)
		if !ok {
			return phrases
		}
		phrase, remainder, closed := strings.Cut(after, `"`)
		if !closed {
			return phrases
		}
		if unquoted, err := strconv.Unquote(`"` + phrase + `"`); err == nil {
			phrase = unquoted
		}
		phrases = append(phrases, phrase)
		rest = remainder
	}
}

// createIssue files an issue in the repository. It is not on the board: a
// project holds an issue only once something adds it, which is the next
// mutation flow sends.
func (f *ghProjectsFake) createIssue(w http.ResponseWriter, query core.GraphQLQuery) {
	repo, _ := query.Variables["repo"].(string)
	title, _ := query.Variables["title"].(string)
	body, _ := query.Variables["body"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo != ghFakeRepoID {
		f.refuse(w, "NOT_FOUND", "no repository "+repo)
		return
	}
	f.issuesMade++
	number := 100 + f.issuesMade
	item := &ghFakeItem{
		id:        "PVTI_" + strconv.Itoa(number),
		number:    number,
		updatedAt: f.now,
		offBoard:  true,
		ticket: core.Ticket{
			Key:       "#" + strconv.Itoa(number),
			ID:        "I_" + strconv.Itoa(number),
			URL:       "https://github.com/" + f.repo + "/issues/" + strconv.Itoa(number),
			Title:     title,
			Body:      body,
			Labels:    f.labelNames(query.Variables["labels"]),
			Assignees: f.assigneeLogins(query.Variables["assignees"]),
			UpdatedAt: f.now,
		},
	}
	f.items = append(f.items, item)
	f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"createIssue": map[string]any{"issue": f.issueJSON(query.Query, item)},
	}})
}

// labelNames turns the label ids a filing sent into the names they stand for.
func (f *ghProjectsFake) labelNames(raw any) []string {
	ids, _ := raw.([]any)
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		for _, label := range f.labels {
			if label.NodeID == id {
				names = append(names, label.Name)
			}
		}
	}
	return names
}

// assigneeLogins turns the assignee ids a filing sent into logins.
func (f *ghProjectsFake) assigneeLogins(raw any) []string {
	ids, _ := raw.([]any)
	logins := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == ghFakeUserID {
			logins = append(logins, "maintainer")
		}
	}
	return logins
}

// addItem puts an issue on the board. Adding content the project already
// holds answers with the item that is already there, as GitHub does.
func (f *ghProjectsFake) addItem(w http.ResponseWriter, query core.GraphQLQuery) {
	project, _ := query.Variables["project"].(string)
	content, _ := query.Variables["content"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.projects) == 0 || project != f.projects[0].ID {
		f.refuse(w, "NOT_FOUND", "no project "+project)
		return
	}
	for _, item := range f.items {
		if item.ticket.ID != content {
			continue
		}
		item.offBoard = false
		f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"addProjectV2ItemById": map[string]any{"item": map[string]any{"id": item.id}},
		}})
		return
	}
	f.refuse(w, "NOT_FOUND", "no content "+content)
}

// commentPage answers one page of an issue's comments, with the issue's id.
func (f *ghProjectsFake) commentPage(variables map[string]any) any {
	owner, _ := variables["owner"].(string)
	name, _ := variables["name"].(string)
	raw, ok := variables["number"].(float64)
	if !ok {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.serves(owner + "/" + name) {
		return nil
	}
	at := slices.IndexFunc(f.items, func(i *ghFakeItem) bool {
		return i.number == int(raw) && strings.EqualFold(i.in(f.repo), owner+"/"+name)
	})
	if at < 0 {
		return map[string]any{"issue": nil}
	}
	from := 0
	if after, ok := variables["after"].(string); ok {
		if n, err := strconv.Atoi(after); err == nil {
			from = n
		}
	}
	all := f.comments[int(raw)]
	rest := all[min(from, len(all)):]
	more := len(rest) > f.commentPageSize
	if more {
		rest = rest[:f.commentPageSize]
	}
	nodes := make([]any, 0, len(rest))
	for _, comment := range rest {
		node := map[string]any{
			"id": comment.id, "body": comment.body,
			"createdAt": comment.createdAt.Format(time.RFC3339),
			"author":    nil,
		}
		if comment.author != "" {
			node["author"] = map[string]any{"login": comment.author}
		}
		nodes = append(nodes, node)
	}
	return map[string]any{"issue": map[string]any{
		"id": f.items[at].ticket.ID,
		"comments": map[string]any{
			"nodes": nodes,
			"pageInfo": map[string]any{
				"hasNextPage": more,
				"endCursor":   strconv.Itoa(from + len(rest)),
			},
		},
	}}
}

// addComment writes a new comment on an issue.
func (f *ghProjectsFake) addComment(w http.ResponseWriter, query core.GraphQLQuery) {
	subject, _ := query.Variables["subject"].(string)
	body, _ := query.Variables["body"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.items, func(i *ghFakeItem) bool { return i.ticket.ID == subject })
	if at < 0 {
		f.refuse(w, "NOT_FOUND", "no issue "+subject)
		return
	}
	f.commentsMade++
	f.now = f.now.Add(time.Minute)
	number := f.items[at].number
	f.comments[number] = append(f.comments[number], &ghFakeComment{
		id:        "IC_" + strconv.Itoa(f.commentsMade),
		body:      body,
		author:    "flow-bot",
		createdAt: f.now,
	})
	f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"addComment": map[string]any{}}})
}

// updateComment edits a comment in place.
func (f *ghProjectsFake) updateComment(w http.ResponseWriter, query core.GraphQLQuery) {
	id, _ := query.Variables["comment"].(string)
	body, _ := query.Variables["body"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, comments := range f.comments {
		for _, comment := range comments {
			if comment.id == id {
				comment.body = body
				f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"updateIssueComment": map[string]any{}}})
				return
			}
		}
	}
	f.refuse(w, "NOT_FOUND", "no comment "+id)
}

// updateIssue rewrites an issue's body, the way GitHub does, and moves the
// moment it last changed. That movement is the whole hazard a rewrite of
// identical bytes would cause, so the fake inflicts it.
func (f *ghProjectsFake) updateIssue(w http.ResponseWriter, query core.GraphQLQuery) {
	id, _ := query.Variables["issue"].(string)
	title, _ := query.Variables["title"].(string)
	body, _ := query.Variables["body"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.items, func(i *ghFakeItem) bool { return i.ticket.ID == id })
	if at < 0 {
		f.refuse(w, "NOT_FOUND", "no issue "+id)
		return
	}
	f.now = f.now.Add(time.Minute)
	f.items[at].ticket.Title = title
	f.items[at].ticket.Body = body
	f.items[at].ticket.UpdatedAt = f.now
	f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"updateIssue": map[string]any{"issue": map[string]any{"id": id}},
	}})
}

// setIssueState closes or reopens an issue, recording when and why. The clock
// moves so that a second closure of a closed issue shows up as a date that
// moved — which is the thing Close must not do.
func (f *ghProjectsFake) setIssueState(w http.ResponseWriter, query core.GraphQLQuery) {
	id, _ := query.Variables["issue"].(string)
	reason, _ := query.Variables["reason"].(string)
	closing := strings.Contains(query.Query, "closeIssue(input:")
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.items, func(i *ghFakeItem) bool { return i.ticket.ID == id })
	if at < 0 {
		f.refuse(w, "NOT_FOUND", "no issue "+id)
		return
	}
	f.now = f.now.Add(time.Minute)
	ticket := &f.items[at].ticket
	ticket.Closed = closing
	ticket.UpdatedAt = f.now
	if closing {
		ticket.ClosedAt = f.now
		ticket.CloseReason = core.ClosedCompleted
		if reason == ghReasonNotPlannedInput {
			ticket.CloseReason = core.ClosedNotPlanned
		}
	} else {
		ticket.ClosedAt = time.Time{}
		ticket.CloseReason = ""
	}
	op := "reopenIssue"
	if closing {
		op = "closeIssue"
	}
	f.writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		op: map[string]any{"issue": map[string]any{"id": id}},
	}})
}

// commentBodies is what an issue's comments now say, for a test that asks the
// fake rather than the adapter.
func (f *ghProjectsFake) commentBodies(number int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	bodies := make([]string, 0, len(f.comments[number]))
	for _, comment := range f.comments[number] {
		bodies = append(bodies, comment.body)
	}
	return bodies
}

// onBoard reports whether the project holds the issue with that number.
func (f *ghProjectsFake) onBoard(number int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	at := slices.IndexFunc(f.items, func(i *ghFakeItem) bool { return i.number == number })
	return at >= 0 && !f.items[at].offBoard
}
