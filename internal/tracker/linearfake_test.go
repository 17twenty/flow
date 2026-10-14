package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// A fake Linear, in Linear's own shapes: one GraphQL endpoint, Relay cursor
// pages, and an Authorization header that is the credential and nothing else.
//
// It answers from the document it was sent rather than from its own world, so
// a field flow stops selecting stops arriving — a fake that answers what the
// query did not ask makes a missing field invisible (spec 14 §4).

const (
	// lnFakeTeam is the key of the team the fixture becomes, and
	// lnFakeKeyPrefix how its issues are spelled.
	lnFakeTeam      = "ENG"
	lnFakeKeyPrefix = lnFakeTeam + "-"
	// lnFakeToken is the personal API key the fake expects, assembled
	// rather than written out: a credential-shaped literal in the tree is
	// what push protection rejects (spec 15 §2).
	lnFakeToken = "lin_" + "api_" + "0123456789abcdef"
)

// lnFake is the fake workspace.
type lnFake struct {
	t        *testing.T
	team     lnTeam
	teams    []lnTeam
	org      lnOrganization
	admin    bool
	pageSize int

	mu       sync.Mutex
	now      time.Time
	next     int
	states   []lnState
	labels   []lnLabel
	issues   []*lnFakeIssue
	comments map[string][]lnComment
	calls    map[string]int
	refuse   map[string]lnFakeRefusal
}

// lnFakeIssue is one issue, with the two things Linear keeps beside it.
type lnFakeIssue struct {
	issue   lnIssue
	trashed bool
}

// lnFakeRefusal is an error the fake answers an operation with instead of
// doing it.
type lnFakeRefusal struct {
	Message string
	Code    string
	Type    string
	Once    bool
}

// newLinearServer stands a fake Linear up in front of the contract's fixture.
func newLinearServer(t *testing.T, fx *coretest.TrackerFixture) (*lnFake, *httptest.Server) {
	t.Helper()
	fx.KeyPrefix = lnFakeKeyPrefix
	linked := lnLinkedPullRequest()
	fx.LinkedPR = &linked

	fake := &lnFake{
		t:        t,
		org:      lnOrganization{ID: "org-1", Name: "Acme", URLKey: "acme"},
		admin:    true,
		pageSize: 2,
		now:      coretest.ContractNow,
		next:     1000,
		comments: map[string][]lnComment{},
		calls:    map[string]int{},
		refuse:   map[string]lnFakeRefusal{},
	}
	fake.seed(*fx)

	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	return fake, srv
}

// lnLinkedPullRequest is the pull request the linkage case reads. Linear is
// not the forge's tracker, so nothing the forge says it closes is a Linear
// identifier: what links the two is the identifier in the branch, which is
// Linear's own convention and what its integration reads.
func lnLinkedPullRequest() core.PullRequest {
	pr := coretest.ContractForge().PullRequests[1]
	pr.HeadRef = "dev/" + strings.ToLower(lnFakeKeyPrefix) + "11-the-last-event"
	pr.ClosingIssues = nil
	return pr
}

// seed fills the fake's world from the fixture.
func (f *lnFake) seed(fx coretest.TrackerFixture) {
	f.team = lnTeam{ID: "team-eng", Key: lnFakeTeam, Name: "Engineering", Membership: &lnMembership{Owner: false}}
	f.teams = append(f.teams, f.team)
	for _, candidate := range fx.Candidates[core.ItemTrackerProject] {
		f.teams = append(f.teams, lnTeam{
			ID:   "team-" + candidate.ID,
			Key:  lnFakeTeamKey(candidate.Name),
			Name: candidate.Name,
		})
	}

	position := 0.0
	for _, item := range fx.Observations[core.ItemTrackerStatusOptions].Items {
		position++
		f.states = append(f.states, lnState{
			ID:       item.ID,
			Name:     item.Name,
			Type:     lnFakeStateType(item.Name, item.Field("category")),
			Position: position,
			Color:    "#4cb782",
		})
	}
	// A column the fixture names and the observation has no option for is
	// still a state of the team's workflow: `workflow.columns` is written
	// by hand, and a board whose states flow never created is ordinary.
	for id, name := range fx.Columns {
		if slices.ContainsFunc(f.states, func(s lnState) bool { return s.ID == string(id) }) {
			continue
		}
		position++
		f.states = append(f.states, lnState{
			ID: string(id), Name: name, Type: lnFakeStateType(name, ""), Position: position, Color: "#4cb782",
		})
	}
	slices.SortStableFunc(f.states, func(a, b lnState) int { return strings.Compare(a.ID, b.ID) })

	for _, item := range fx.Observations[core.ItemTrackerLabels].Items {
		label := lnLabel{ID: item.ID, Name: item.Name, Team: &lnTeam{ID: f.team.ID, Key: f.team.Key}}
		if colour := item.Field("color"); colour != "" {
			label.Color = "#" + colour
		}
		f.labels = append(f.labels, label)
	}
	// A label group and a label of another team are both in the workspace
	// and neither is this board's.
	f.labels = append(f.labels,
		lnLabel{ID: "lbl-group", Name: "Priority", IsGroup: true},
		lnLabel{ID: "lbl-other", Name: "design", Color: "#b60205", Team: &lnTeam{ID: "team-PVT_1", Key: "DEL"}},
	)

	for _, ticket := range fx.Tickets {
		f.issues = append(f.issues, f.issueOf(fx, ticket))
	}
}

// lnFakeTeamKey is a team key made from a name, as somebody setting a team up
// would: the first letters, upper-cased.
func lnFakeTeamKey(name string) string {
	key := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, strings.ToUpper(name))
	if len(key) > 3 {
		key = key[:3]
	}
	return key
}

// lnFakeStateType is the Linear state type a column of the fixture is, from
// the category it declares or from its name where it declares none.
func lnFakeStateType(name, category string) string {
	switch category {
	case categoryTodo:
		return lnStateUnstarted
	case categoryInProgress:
		return lnStateStarted
	case categoryDone:
		return lnStateCompleted
	}
	switch strings.ToLower(name) {
	case "todo", "to do", "ready", "backlog":
		return lnStateUnstarted
	case "done":
		return lnStateCompleted
	default:
		return lnStateStarted
	}
}

// issueOf turns one of the fixture's tickets into an issue.
func (f *lnFake) issueOf(fx coretest.TrackerFixture, ticket core.Ticket) *lnFakeIssue {
	state := f.stateByID(string(ticket.ColumnID))
	issue := lnIssue{
		ID:          "issue-" + strings.TrimPrefix(ticket.Key, "#"),
		Identifier:  fx.Key(ticket.Key),
		Title:       ticket.Title,
		Description: ticket.Body,
		URL:         "https://linear.app/" + f.org.URLKey + "/issue/" + fx.Key(ticket.Key),
		BranchName:  strings.ToLower(fx.Key(ticket.Key)) + "-" + lnFakeSlug(ticket.Title),
		State:       state,
		Team:        &lnTeam{ID: f.team.ID, Key: f.team.Key, Name: f.team.Name},
		CreatedAt:   lnTime{Time: ticket.UpdatedAt},
		UpdatedAt:   lnTime{Time: ticket.UpdatedAt},
	}
	if number, err := strconv.Atoi(strings.TrimPrefix(ticket.Key, "#")); err == nil {
		issue.Number = float64(number)
	}
	if ticket.Parent != "" {
		issue.Parent = &lnIssue{ID: "issue-parent", Identifier: fx.Key(ticket.Parent)}
	}
	if len(ticket.Assignees) > 0 {
		issue.Assignee = &lnUser{ID: "user-dev", Name: ticket.Assignees[0], DisplayName: ticket.Assignees[0]}
	}
	if len(ticket.Labels) > 0 {
		issue.Labels = &struct {
			Nodes []lnLabel `json:"nodes"`
		}{}
		for _, name := range ticket.Labels {
			issue.Labels.Nodes = append(issue.Labels.Nodes, lnLabel{ID: "lbl-" + name, Name: name})
		}
	}
	if ticket.Archived {
		issue.ArchivedAt = lnTime{Time: ticket.UpdatedAt}
	}
	if ticket.Closed {
		if ticket.CloseReason == core.ClosedNotPlanned {
			issue.CanceledAt = lnTime{Time: ticket.ClosedAt}
		} else {
			issue.CompletedAt = lnTime{Time: ticket.ClosedAt}
		}
	}
	return &lnFakeIssue{issue: issue}
}

// lnFakeSlug is a title as a branch name holds one.
func lnFakeSlug(title string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, title)
	return strings.Trim(slug, "-")
}

// stateByID is the workflow state with that id, or nil.
func (f *lnFake) stateByID(id string) *lnState {
	if at := slices.IndexFunc(f.states, func(s lnState) bool { return s.ID == id }); at >= 0 {
		state := f.states[at]
		return &state
	}
	return nil
}

// serve answers one GraphQL call.
func (f *lnFake) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method != http.MethodPost:
		f.t.Errorf("Linear was asked %s %s; a GraphQL API takes POST", r.Method, r.URL.Path)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	case r.URL.Path != "/"+LinearPath:
		f.t.Errorf("Linear was asked for %s; it has one endpoint", r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Linear's personal API keys are the whole header. A scheme in front of
	// one is a credential Linear does not recognise.
	switch got := r.Header.Get("Authorization"); {
	case got == "":
		f.refusal(w, http.StatusUnauthorized, "Authentication required", "AUTHENTICATION_ERROR")
		return
	case got != lnFakeToken:
		f.t.Errorf("Authorization = %q, want the credential on its own", got)
		f.refusal(w, http.StatusUnauthorized, "Invalid credential", "AUTHENTICATION_ERROR")
		return
	}
	if accept := r.Header.Get("Accept"); !strings.Contains(accept, "application/json") {
		f.t.Errorf("Accept = %q, want application/json", accept)
	}
	if agent := r.Header.Get("User-Agent"); !strings.HasPrefix(agent, "flow/") {
		f.t.Errorf("User-Agent = %q, want flow to say who it is", agent)
	}

	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("the request body is not JSON: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	operation := lnFakeOperation(body.Query)
	if operation == "" {
		f.t.Errorf("the document names no operation:\n%s", body.Query)
	}

	f.mu.Lock()
	f.calls[operation]++
	refusal, refused := f.refuse[operation]
	if refused && refusal.Once {
		delete(f.refuse, operation)
	}
	f.mu.Unlock()
	if refused {
		f.error(w, refusal)
		return
	}

	data, err := f.answer(operation, body.Query, body.Variables)
	if err != nil {
		f.error(w, *err)
		return
	}
	f.write(w, map[string]any{"data": data})
}

// answer is the body of one operation.
func (f *lnFake) answer(operation, document string, variables map[string]any) (map[string]any, *lnFakeRefusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch operation {
	case "FlowViewer":
		return map[string]any{
			"viewer": map[string]any{
				"id": "user-flow", "name": "flow", "displayName": "flow",
				"email": "flow@acme.test", "admin": f.admin,
			},
			"organization": f.orgJSON(),
		}, nil
	case "FlowTeams":
		return f.teamsPage(variables), nil
	case "FlowTeam":
		return f.teamByKey(lnFakeString(variables["key"])), nil
	case "FlowStates":
		return f.statesPage(variables)
	case "FlowLabels":
		return f.labelsPage(variables), nil
	case "FlowIssues", "FlowArchivedIssues":
		return f.issuesPage(document, variables), nil
	case "FlowIssue":
		return f.issueByKey(document, lnFakeString(variables["key"])), nil
	case "FlowComments":
		return f.commentsPage(lnFakeString(variables["key"]), variables)
	case "FlowIssueCreate":
		return f.issueCreate(document, variables)
	case "FlowIssueUpdate":
		return f.issueUpdate(document, variables)
	case "FlowLabelCreate":
		return f.labelCreate(variables)
	case "FlowLabelUpdate":
		return f.labelUpdate(variables)
	case "FlowLabelDelete":
		return f.labelDelete(variables)
	case "FlowStateCreate":
		return f.stateCreate(variables)
	case "FlowStateUpdate":
		return f.stateUpdate(variables)
	case "FlowStateArchive":
		return f.stateArchive(variables)
	case "FlowCommentCreate":
		return f.commentCreate(variables)
	case "FlowCommentUpdate":
		return f.commentUpdate(variables)
	default:
		f.t.Errorf("Linear was sent an operation it has no answer for: %q", operation)
		return nil, &lnFakeRefusal{Message: "Unknown operation " + operation, Code: "INVALID_INPUT"}
	}
}

// orgJSON is the workspace.
func (f *lnFake) orgJSON() map[string]any {
	return map[string]any{"id": f.org.ID, "name": f.org.Name, "urlKey": f.org.URLKey}
}

// teamsPage is one page of teams.
func (f *lnFake) teamsPage(variables map[string]any) map[string]any {
	from := lnFakeCursor(variables)
	to := min(from+f.pageSize, len(f.teams))
	nodes := make([]any, 0, to-from)
	for _, team := range f.teams[from:to] {
		nodes = append(nodes, map[string]any{"id": team.ID, "key": team.Key, "name": team.Name})
	}
	return map[string]any{
		"teams":        map[string]any{"nodes": nodes, "pageInfo": lnFakePageInfo(to, len(f.teams))},
		"organization": f.orgJSON(),
	}
}

// teamByKey answers the lookup of one team.
func (f *lnFake) teamByKey(key string) map[string]any {
	var nodes []any
	for _, team := range f.teams {
		if !strings.EqualFold(team.Key, key) {
			continue
		}
		node := map[string]any{"id": team.ID, "key": team.Key, "name": team.Name}
		if team.Membership != nil {
			node["membership"] = map[string]any{"owner": team.Membership.Owner}
		}
		nodes = append(nodes, node)
	}
	return map[string]any{"teams": map[string]any{"nodes": nodes}}
}

// statesPage is one page of workflow states.
func (f *lnFake) statesPage(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	if id := lnFakeString(variables["team"]); id != f.team.ID {
		return map[string]any{"team": nil}, nil
	}
	from := lnFakeCursor(variables)
	to := min(from+f.pageSize, len(f.states))
	nodes := make([]any, 0, to-from)
	for _, state := range f.states[from:to] {
		nodes = append(nodes, map[string]any{
			"id": state.ID, "name": state.Name, "type": state.Type,
			"position": state.Position, "description": state.Description, "color": state.Color,
		})
	}
	return map[string]any{"team": map[string]any{
		"states": map[string]any{"nodes": nodes, "pageInfo": lnFakePageInfo(to, len(f.states))},
	}}, nil
}

// labelsPage is one page of labels.
func (f *lnFake) labelsPage(variables map[string]any) map[string]any {
	from := lnFakeCursor(variables)
	to := min(from+f.pageSize, len(f.labels))
	nodes := make([]any, 0, to-from)
	for _, label := range f.labels[from:to] {
		node := map[string]any{
			"id": label.ID, "name": label.Name, "color": label.Color,
			"description": label.Description, "isGroup": label.IsGroup,
		}
		if label.RetiredAt != nil {
			node["retiredAt"] = *label.RetiredAt
		}
		if label.Team != nil {
			node["team"] = map[string]any{"id": label.Team.ID, "key": label.Team.Key}
		}
		nodes = append(nodes, node)
	}
	return map[string]any{"issueLabels": map[string]any{
		"nodes": nodes, "pageInfo": lnFakePageInfo(to, len(f.labels)),
	}}
}

// issuesPage is one page of a listing.
//
// Whether archived issues are in it is read from the document rather than
// from the operation's name: `includeArchived` is an argument flow either
// sends or does not, and a fake that decides it for itself cannot tell the
// two queries apart.
func (f *lnFake) issuesPage(document string, variables map[string]any) map[string]any {
	archived := strings.Contains(document, "includeArchived: true")
	filter, _ := variables["filter"].(map[string]any)

	var matching []*lnFakeIssue
	for _, issue := range f.issues {
		switch {
		case issue.trashed:
			continue
		case !archived && !issue.issue.ArchivedAt.Time.IsZero():
			continue
		case !f.matches(issue.issue, filter):
			continue
		}
		matching = append(matching, issue)
	}
	slices.SortStableFunc(matching, func(a, b *lnFakeIssue) int {
		return b.issue.UpdatedAt.Time.Compare(a.issue.UpdatedAt.Time)
	})

	size := f.pageSize
	if first, ok := lnFakeInt(variables["first"]); ok && first < size {
		size = first
	}
	from := lnFakeCursor(variables)
	to := min(from+size, len(matching))
	nodes := make([]any, 0, max(to-from, 0))
	for _, issue := range matching[from:to] {
		nodes = append(nodes, f.issueJSON(document, issue.issue))
	}
	return map[string]any{"issues": map[string]any{
		"nodes": nodes, "pageInfo": lnFakePageInfo(to, len(matching)),
	}}
}

// matches reports whether an issue answers a filter. Only the shapes flow
// sends are understood: a filter the fake cannot read fails the test rather
// than quietly matching everything.
func (f *lnFake) matches(issue lnIssue, filter map[string]any) bool {
	for field, raw := range filter {
		clause, ok := raw.(map[string]any)
		if !ok {
			f.t.Errorf("the filter on %q is not a comparator: %#v", field, raw)
			return false
		}
		switch field {
		case "team":
			key, _ := clause["key"].(map[string]any)
			if issue.Team == nil || !strings.EqualFold(issue.Team.Key, lnFakeString(key["eq"])) {
				return false
			}
		case "state":
			id, _ := clause["id"].(map[string]any)
			if issue.State == nil || issue.State.ID != lnFakeString(id["eq"]) {
				return false
			}
		case "updatedAt":
			since, err := time.Parse(lnTimeLayout, lnFakeString(clause["gte"]))
			if err != nil {
				f.t.Errorf("updatedAt.gte = %q, which Linear cannot read: %v", clause["gte"], err)
				return false
			}
			if issue.UpdatedAt.Time.Before(since) {
				return false
			}
		case "description":
			if !strings.Contains(issue.Description, lnFakeString(clause["contains"])) {
				return false
			}
		default:
			f.t.Errorf("the fake does not know the filter %q", field)
			return false
		}
	}
	return true
}

// issueByKey answers the lookup of one issue. An archived issue answers here
// and nowhere else; one in the trash answers too, saying that it is in the
// trash — which is the whole reason Linear publishes the field.
func (f *lnFake) issueByKey(document, key string) map[string]any {
	issue := f.find(key)
	if issue == nil {
		return map[string]any{"issue": nil}
	}
	node := f.issueJSON(document, issue.issue)
	if _, selected := node["trashed"]; selected {
		node["trashed"] = issue.trashed
	}
	return map[string]any{"issue": node}
}

// find is the issue with that identifier, or nil.
func (f *lnFake) find(key string) *lnFakeIssue {
	for _, issue := range f.issues {
		if strings.EqualFold(issue.issue.Identifier, key) {
			return issue
		}
	}
	return nil
}

// issueJSON renders an issue, keeping only the fields the document selected.
func (f *lnFake) issueJSON(document string, issue lnIssue) map[string]any {
	node := map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"number":      issue.Number,
		"title":       issue.Title,
		"description": issue.Description,
		"url":         issue.URL,
		"branchName":  issue.BranchName,
		"trashed":     false,
		"archivedAt":  lnFakeTime(issue.ArchivedAt),
		"completedAt": lnFakeTime(issue.CompletedAt),
		"canceledAt":  lnFakeTime(issue.CanceledAt),
		"createdAt":   lnFakeTime(issue.CreatedAt),
		"updatedAt":   lnFakeTime(issue.UpdatedAt),
		"state":       nil,
		"team":        nil,
		"parent":      nil,
		"assignee":    nil,
		"labels":      map[string]any{"nodes": []any{}},
		"attachments": map[string]any{"nodes": []any{}},
	}
	if issue.State != nil {
		node["state"] = map[string]any{
			"id": issue.State.ID, "name": issue.State.Name,
			"type": issue.State.Type, "position": issue.State.Position,
		}
	}
	if issue.Team != nil {
		node["team"] = map[string]any{"id": issue.Team.ID, "key": issue.Team.Key, "name": issue.Team.Name}
	}
	if issue.Parent != nil {
		node["parent"] = map[string]any{"id": issue.Parent.ID, "identifier": issue.Parent.Identifier}
	}
	if issue.Assignee != nil {
		node["assignee"] = map[string]any{
			"id": issue.Assignee.ID, "name": issue.Assignee.Name, "displayName": issue.Assignee.DisplayName,
		}
	}
	if issue.Labels != nil {
		labels := make([]any, 0, len(issue.Labels.Nodes))
		for _, label := range issue.Labels.Nodes {
			labels = append(labels, map[string]any{"id": label.ID, "name": label.Name})
		}
		node["labels"] = map[string]any{"nodes": labels}
	}
	if issue.Attachments != nil {
		attachments := make([]any, 0, len(issue.Attachments.Nodes))
		for _, attachment := range issue.Attachments.Nodes {
			attachments = append(attachments, map[string]any{"id": attachment.ID, "url": attachment.URL})
		}
		node["attachments"] = map[string]any{"nodes": attachments}
	}
	for field := range node {
		if !lnFakeSelected(document, field) {
			delete(node, field)
		}
	}
	return node
}

// lnFakeSelected reports whether a document names a field. The name must
// stand on its own: a mutation that sends `stateId` has not selected `state`.
func lnFakeSelected(document, field string) bool {
	pattern := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(field) + `([^A-Za-z0-9_]|$)`)
	return pattern.MatchString(document)
}

// commentsPage is one page of an issue's comments.
func (f *lnFake) commentsPage(key string, variables map[string]any) (map[string]any, *lnFakeRefusal) {
	issue := f.find(key)
	if issue == nil || issue.trashed {
		return map[string]any{"issue": nil}, nil
	}
	comments := f.comments[issue.issue.ID]
	from := lnFakeCursor(variables)
	to := min(from+f.pageSize, len(comments))
	nodes := make([]any, 0, to-from)
	for _, comment := range comments[from:to] {
		nodes = append(nodes, f.commentJSON(comment))
	}
	return map[string]any{"issue": map[string]any{
		"id":       issue.issue.ID,
		"comments": map[string]any{"nodes": nodes, "pageInfo": lnFakePageInfo(to, len(comments))},
	}}, nil
}

// commentJSON renders one comment.
func (f *lnFake) commentJSON(comment lnComment) map[string]any {
	return map[string]any{
		"id": comment.ID, "body": comment.Body, "url": comment.URL,
		"createdAt": lnFakeTime(comment.CreatedAt), "updatedAt": lnFakeTime(comment.UpdatedAt),
		"user": map[string]any{"id": "user-flow", "name": "flow", "displayName": "flow"},
	}
}

// issueCreate files an issue.
func (f *lnFake) issueCreate(document string, variables map[string]any) (map[string]any, *lnFakeRefusal) {
	input, _ := variables["input"].(map[string]any)
	if lnFakeString(input["teamId"]) != f.team.ID {
		return nil, &lnFakeRefusal{Message: "Entity not found: Team", Code: "ENTITY_NOT_FOUND"}
	}
	f.next++
	number := f.next
	key := lnFakeKeyPrefix + strconv.Itoa(number)
	issue := lnIssue{
		ID:          "issue-" + strconv.Itoa(number),
		Identifier:  key,
		Number:      float64(number),
		Title:       lnFakeString(input["title"]),
		Description: lnFakeString(input["description"]),
		URL:         "https://linear.app/" + f.org.URLKey + "/issue/" + key,
		BranchName:  strings.ToLower(key) + "-" + lnFakeSlug(lnFakeString(input["title"])),
		State:       f.firstOfType(lnStateUnstarted, lnStateBacklog, lnStateTriage),
		Team:        &lnTeam{ID: f.team.ID, Key: f.team.Key, Name: f.team.Name},
		CreatedAt:   lnTime{Time: f.now},
		UpdatedAt:   lnTime{Time: f.now},
	}
	if ids, ok := input["labelIds"].([]any); ok && len(ids) > 0 {
		issue.Labels = &struct {
			Nodes []lnLabel `json:"nodes"`
		}{}
		for _, raw := range ids {
			id := lnFakeString(raw)
			at := slices.IndexFunc(f.labels, func(l lnLabel) bool { return l.ID == id })
			if at < 0 {
				return nil, &lnFakeRefusal{Message: "Entity not found: IssueLabel", Code: "ENTITY_NOT_FOUND"}
			}
			issue.Labels.Nodes = append(issue.Labels.Nodes, lnLabel{ID: id, Name: f.labels[at].Name})
		}
	}
	if owner := lnFakeString(input["assigneeId"]); owner != "" {
		issue.Assignee = &lnUser{ID: owner, Name: "flow", DisplayName: "flow"}
	}
	f.issues = append(f.issues, &lnFakeIssue{issue: issue})
	return map[string]any{"issueCreate": map[string]any{
		"success": true, "issue": f.issueJSON(document, issue),
	}}, nil
}

// issueUpdate edits an issue: its state, its title or its description.
func (f *lnFake) issueUpdate(document string, variables map[string]any) (map[string]any, *lnFakeRefusal) {
	issue := f.find(lnFakeString(variables["key"]))
	if issue == nil || issue.trashed {
		return nil, &lnFakeRefusal{Message: "Entity not found: Issue", Code: "ENTITY_NOT_FOUND"}
	}
	if !issue.issue.ArchivedAt.Time.IsZero() {
		return nil, &lnFakeRefusal{Message: "Entity is archived: Issue", Code: "ENTITY_ARCHIVED"}
	}
	// Linear writes at the moment of the write, which is after the moment
	// of the last one: a test cannot tell a date that moved from one that
	// did not if every write happens at the same instant.
	f.tick()
	input, _ := variables["input"].(map[string]any)
	if title, ok := input["title"]; ok {
		issue.issue.Title = lnFakeString(title)
	}
	if description, ok := input["description"]; ok {
		issue.issue.Description = lnFakeString(description)
	}
	if raw, ok := input["stateId"]; ok {
		state := f.stateByID(lnFakeString(raw))
		if state == nil {
			return nil, &lnFakeRefusal{Message: "Entity not found: WorkflowState", Code: "ENTITY_NOT_FOUND"}
		}
		issue.issue.State = state
		// Linear dates the two endings itself, which is what makes a
		// state of the right type a closed ticket.
		issue.issue.CompletedAt = lnTime{}
		issue.issue.CanceledAt = lnTime{}
		switch state.Type {
		case lnStateCompleted:
			issue.issue.CompletedAt = lnTime{Time: f.now}
		case lnStateCanceled:
			issue.issue.CanceledAt = lnTime{Time: f.now}
		}
	}
	issue.issue.UpdatedAt = lnTime{Time: f.now}
	return map[string]any{"issueUpdate": map[string]any{
		"success": true, "issue": f.issueJSON(document, issue.issue),
	}}, nil
}

// firstOfType is the first state of the first of those types, by position.
func (f *lnFake) firstOfType(types ...string) *lnState {
	for _, want := range types {
		for at, state := range f.states {
			if strings.EqualFold(state.Type, want) {
				found := f.states[at]
				return &found
			}
		}
	}
	return nil
}

// labelCreate adds a label to the team.
func (f *lnFake) labelCreate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	input, _ := variables["input"].(map[string]any)
	name := lnFakeString(input["name"])
	if slices.ContainsFunc(f.labels, func(l lnLabel) bool { return strings.EqualFold(l.Name, name) }) {
		return nil, &lnFakeRefusal{
			Message: "A label with this name already exists in the team",
			Code:    "INVALID_INPUT",
			Type:    "invalid input",
		}
	}
	f.next++
	label := lnLabel{
		ID:          "lbl-" + strconv.Itoa(f.next),
		Name:        name,
		Color:       lnFakeString(input["color"]),
		Description: lnFakeString(input["description"]),
		Team:        &lnTeam{ID: f.team.ID, Key: f.team.Key},
	}
	f.labels = append(f.labels, label)
	return map[string]any{"issueLabelCreate": map[string]any{
		"success": true, "issueLabel": f.labelJSON(label),
	}}, nil
}

// labelUpdate edits one.
func (f *lnFake) labelUpdate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	at := slices.IndexFunc(f.labels, func(l lnLabel) bool { return l.ID == lnFakeString(variables["id"]) })
	if at < 0 {
		return nil, &lnFakeRefusal{Message: "Entity not found: IssueLabel", Code: "ENTITY_NOT_FOUND"}
	}
	input, _ := variables["input"].(map[string]any)
	if name, ok := input["name"]; ok {
		f.labels[at].Name = lnFakeString(name)
	}
	if colour, ok := input["color"]; ok {
		f.labels[at].Color = lnFakeString(colour)
	}
	if description, ok := input["description"]; ok {
		f.labels[at].Description = lnFakeString(description)
	}
	return map[string]any{"issueLabelUpdate": map[string]any{
		"success": true, "issueLabel": f.labelJSON(f.labels[at]),
	}}, nil
}

// labelDelete removes one.
func (f *lnFake) labelDelete(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	at := slices.IndexFunc(f.labels, func(l lnLabel) bool { return l.ID == lnFakeString(variables["id"]) })
	if at < 0 {
		return nil, &lnFakeRefusal{Message: "Entity not found: IssueLabel", Code: "ENTITY_NOT_FOUND"}
	}
	f.labels = slices.Delete(f.labels, at, at+1)
	return map[string]any{"issueLabelDelete": map[string]any{"success": true}}, nil
}

// labelJSON renders one label.
func (f *lnFake) labelJSON(label lnLabel) map[string]any {
	node := map[string]any{
		"id": label.ID, "name": label.Name, "color": label.Color,
		"description": label.Description, "isGroup": label.IsGroup,
	}
	if label.Team != nil {
		node["team"] = map[string]any{"id": label.Team.ID, "key": label.Team.Key}
	}
	return node
}

// stateCreate adds a workflow state.
func (f *lnFake) stateCreate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	input, _ := variables["input"].(map[string]any)
	name := lnFakeString(input["name"])
	if slices.ContainsFunc(f.states, func(s lnState) bool { return strings.EqualFold(s.Name, name) }) {
		return nil, &lnFakeRefusal{
			Message: "A workflow state with this name already exists",
			Code:    "INVALID_INPUT",
			Type:    "invalid input",
		}
	}
	if !f.admin {
		return nil, &lnFakeRefusal{Message: "Not authorized to edit team settings", Code: "FEATURE_NOT_ACCESSIBLE"}
	}
	f.next++
	state := lnState{
		ID:          "state-" + strconv.Itoa(f.next),
		Name:        name,
		Type:        lnFakeString(input["type"]),
		Color:       lnFakeString(input["color"]),
		Description: lnFakeString(input["description"]),
		Position:    float64(len(f.states) + 1),
	}
	f.states = append(f.states, state)
	return map[string]any{"workflowStateCreate": map[string]any{
		"success": true, "workflowState": f.stateJSON(state),
	}}, nil
}

// stateUpdate edits one. Linear takes no type here, so a document that sent
// one would be a document Linear rejects.
func (f *lnFake) stateUpdate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	at := slices.IndexFunc(f.states, func(s lnState) bool { return s.ID == lnFakeString(variables["id"]) })
	if at < 0 {
		return nil, &lnFakeRefusal{Message: "Entity not found: WorkflowState", Code: "ENTITY_NOT_FOUND"}
	}
	input, _ := variables["input"].(map[string]any)
	if _, sent := input["type"]; sent {
		f.t.Error("flow sent a type to workflowStateUpdate, which Linear's input has no field for")
		return nil, &lnFakeRefusal{Message: `Unknown field "type"`, Code: "INVALID_INPUT"}
	}
	if name, ok := input["name"]; ok {
		f.states[at].Name = lnFakeString(name)
	}
	if colour, ok := input["color"]; ok {
		f.states[at].Color = lnFakeString(colour)
	}
	if description, ok := input["description"]; ok {
		f.states[at].Description = lnFakeString(description)
	}
	return map[string]any{"workflowStateUpdate": map[string]any{
		"success": true, "workflowState": f.stateJSON(f.states[at]),
	}}, nil
}

// stateArchive removes a workflow state, refusing while issues stand in it.
func (f *lnFake) stateArchive(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	id := lnFakeString(variables["id"])
	at := slices.IndexFunc(f.states, func(s lnState) bool { return s.ID == id })
	if at < 0 {
		return nil, &lnFakeRefusal{Message: "Entity not found: WorkflowState", Code: "ENTITY_NOT_FOUND"}
	}
	for _, issue := range f.issues {
		if issue.issue.State != nil && issue.issue.State.ID == id {
			return nil, &lnFakeRefusal{
				Message: "Cannot archive a workflow state that still has issues",
				Code:    "INVALID_INPUT",
				Type:    "invalid input",
			}
		}
	}
	state := f.states[at]
	f.states = slices.Delete(f.states, at, at+1)
	return map[string]any{"workflowStateArchive": map[string]any{
		"success": true, "entity": f.stateJSON(state),
	}}, nil
}

// stateJSON renders one workflow state.
func (f *lnFake) stateJSON(state lnState) map[string]any {
	return map[string]any{
		"id": state.ID, "name": state.Name, "type": state.Type,
		"position": state.Position, "description": state.Description, "color": state.Color,
	}
}

// commentCreate writes a comment.
func (f *lnFake) commentCreate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	input, _ := variables["input"].(map[string]any)
	id := lnFakeString(input["issueId"])
	at := slices.IndexFunc(f.issues, func(i *lnFakeIssue) bool { return i.issue.ID == id })
	if at < 0 {
		return nil, &lnFakeRefusal{Message: "Entity not found: Issue", Code: "ENTITY_NOT_FOUND"}
	}
	f.next++
	comment := lnComment{
		ID:        "comment-" + strconv.Itoa(f.next),
		Body:      lnFakeString(input["body"]),
		URL:       f.issues[at].issue.URL + "#comment-" + strconv.Itoa(f.next),
		CreatedAt: lnTime{Time: f.now},
		UpdatedAt: lnTime{Time: f.now},
	}
	f.comments[id] = append(f.comments[id], comment)
	return map[string]any{"commentCreate": map[string]any{
		"success": true, "comment": f.commentJSON(comment),
	}}, nil
}

// commentUpdate rewrites one.
func (f *lnFake) commentUpdate(variables map[string]any) (map[string]any, *lnFakeRefusal) {
	id := lnFakeString(variables["id"])
	input, _ := variables["input"].(map[string]any)
	for issue, comments := range f.comments {
		for at, comment := range comments {
			if comment.ID != id {
				continue
			}
			f.tick()
			comments[at].Body = lnFakeString(input["body"])
			comments[at].UpdatedAt = lnTime{Time: f.now}
			f.comments[issue] = comments
			return map[string]any{"commentUpdate": map[string]any{
				"success": true, "comment": f.commentJSON(comments[at]),
			}}, nil
		}
	}
	return nil, &lnFakeRefusal{Message: "Entity not found: Comment", Code: "ENTITY_NOT_FOUND"}
}

// tick moves the fake's clock on, so that a write that should have changed
// nothing can be told from one that did.
func (f *lnFake) tick() { f.now = f.now.Add(time.Minute) }

// error answers one GraphQL refusal, in Linear's own shape: HTTP 200, a null
// data, and the name of what went wrong under extensions.
func (f *lnFake) error(w http.ResponseWriter, refusal lnFakeRefusal) {
	f.write(w, map[string]any{
		"data": nil,
		"errors": []any{map[string]any{
			"message": refusal.Message,
			"path":    []any{"issue"},
			"extensions": map[string]any{
				"type":                   refusal.Type,
				"code":                   refusal.Code,
				"userError":              true,
				"userPresentableMessage": refusal.Message,
			},
		}},
	})
}

// refusal answers an HTTP-level refusal, which is what Linear answers a
// credential it will not read at all.
func (f *lnFake) refusal(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []any{map[string]any{
			"message":    message,
			"extensions": map[string]any{"code": code},
		}},
	})
}

// write sends one answer.
func (f *lnFake) write(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Requests-Limit", "1500")
	w.Header().Set("X-RateLimit-Requests-Remaining", "1499")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		f.t.Errorf("writing the answer: %v", err)
	}
}

// refuses makes the next call to an operation answer with a refusal.
func (f *lnFake) refuses(operation string, refusal lnFakeRefusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[operation] = refusal
}

// called is how many times an operation was asked for.
func (f *lnFake) called(operation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[operation]
}

// attach records a link against an issue, the way Linear's own GitHub
// integration does.
func (f *lnFake) attach(key, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.find(key)
	if issue == nil {
		f.t.Fatalf("no issue %s to attach %s to", key, url)
	}
	if issue.issue.Attachments == nil {
		issue.issue.Attachments = &struct {
			Nodes []lnAttachment `json:"nodes"`
		}{}
	}
	issue.issue.Attachments.Nodes = append(issue.issue.Attachments.Nodes,
		lnAttachment{ID: "attachment-" + strconv.Itoa(len(issue.issue.Attachments.Nodes)+1), URL: url})
}

// seedComment puts somebody else's comment on an issue.
func (f *lnFake) seedComment(key, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.find(key)
	if issue == nil {
		f.t.Fatalf("no issue %s to comment on", key)
	}
	f.next++
	f.comments[issue.issue.ID] = append(f.comments[issue.issue.ID], lnComment{
		ID:        "comment-" + strconv.Itoa(f.next),
		Body:      body,
		CreatedAt: lnTime{Time: f.now},
		UpdatedAt: lnTime{Time: f.now},
	})
}

// commentsOn is the comments the fake holds for an issue.
func (f *lnFake) commentsOn(key string) []lnComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.find(key)
	if issue == nil {
		f.t.Fatalf("no issue %s to read the comments of", key)
	}
	return slices.Clone(f.comments[issue.issue.ID])
}

// trash puts an issue in the trash, which is not the same as archiving it.
func (f *lnFake) trash(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.find(key)
	if issue == nil {
		f.t.Fatalf("no issue %s to trash", key)
	}
	issue.trashed = true
}

// lnFakeOperation is the name of the operation a document carries.
var lnFakeOperationRe = regexp.MustCompile(`(?m)^\s*(?:query|mutation)\s+(\w+)`)

func lnFakeOperation(document string) string {
	match := lnFakeOperationRe.FindStringSubmatch(document)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// lnFakeCursor is the index a page starts at.
func lnFakeCursor(variables map[string]any) int {
	at, _ := strconv.Atoi(lnFakeString(variables["after"]))
	return at
}

// lnFakePageInfo is the end of a page.
func lnFakePageInfo(to, total int) map[string]any {
	return map[string]any{"hasNextPage": to < total, "endCursor": strconv.Itoa(to)}
}

// lnFakeString is a variable as a string.
func lnFakeString(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

// lnFakeInt is a variable as a number, and whether it was one.
func lnFakeInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

// lnFakeTime renders a timestamp the way Linear does, or null.
func lnFakeTime(at lnTime) any {
	if at.Time.IsZero() {
		return nil
	}
	return at.Time.UTC().Format(lnTimeLayout)
}
