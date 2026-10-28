package tracker

import (
	"crypto/sha256"
	"encoding/hex"
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

// jiraFake serves Jira Cloud's REST API over a [coretest.TrackerFixture], so
// that the adapter is asked the same questions about the same board as the
// in-memory fake (spec 14).
//
// It is deliberately literal about the three things a Jira client has to get
// right and an in-memory fake can never exercise: a search is paged by an
// opaque token and answers only the fields it was asked for, an archived
// issue answers by key and is absent from every search, and a column is a
// status a workflow transition has to reach.
type jiraFake struct {
	mu sync.Mutex

	// site is the fake's own address, used for the self links Jira writes.
	site string
	// key is the project the fixture's tickets belong to, and projects the
	// projects /project/search offers.
	key      string
	projects []jiraProject
	// statuses are the project's, in the order its workflow offers them.
	statuses []jiraStatus
	// issues are the project's issues, oldest first, which is the order
	// they were created in.
	issues []*jiraFakeIssue
	// resolutions is the instance's resolution scheme.
	resolutions []jiraResolution
	// users is who /user/search knows about.
	users []jiraUser
	// permissions is what /mypermissions grants.
	permissions map[string]bool

	// pageSize is how many issues one page of a search carries, kept small
	// so that an adapter which read the first page and stopped fails here.
	pageSize int
	// now is the fake's clock. Every write advances it, because a tracker
	// that did not move `updated` would make "rewriting it twice is no
	// work" a claim nothing could test.
	now time.Time
	// nextID is the id the next issue, comment or status gets.
	nextID int

	// jqls is every query a search was asked, in order.
	jqls []string
	// requests counts every call, by method and path.
	requests []string
	// refuseResolutions makes /resolution answer 403, which is an instance
	// whose credential may not read the scheme.
	refuseResolutions bool
	// noResolutionField makes every transition screen refuse a resolution.
	noResolutionField bool
	// frozenWorkflow makes every issue offer no transition at all, which
	// is a workflow that does not go where the configuration says.
	frozenWorkflow bool

	// asking is the call being served, which [jiraFake.write] reads to
	// answer a conditional request. Jira tags what it answers and flow
	// keeps the tags between runs, so a fake that never answered 304
	// could not tell a revalidated reading from a believed one.
	asking *http.Request
	// notModified counts the readings flow already had.
	notModified int
}

// jiraFakeIssue is one issue the fake holds.
type jiraFakeIssue struct {
	id       string
	key      string
	fields   jiraIssueFields
	archived bool
	comments []jiraComment
}

// newJiraServer stands a fake Jira up in front of a fixture and returns both.
// The fixture is told how this tracker spells a key, which is what lets the
// contract ask about FLOW-10 rather than about #10.
func newJiraServer(t *testing.T, fx *coretest.TrackerFixture) (*jiraFake, *httptest.Server) {
	t.Helper()
	fx.KeyPrefix = jiraFixtureKeyPrefix
	fx.ReadOnly = true
	linked := jiraLinkedPullRequest()
	fx.LinkedPR = &linked

	fake := &jiraFake{
		key:         jiraFixtureProject,
		pageSize:    2,
		now:         coretest.ContractNow,
		nextID:      1000,
		permissions: map[string]bool{},
		resolutions: []jiraResolution{
			{ID: "10000", Name: "Done"},
			{ID: "10001", Name: "Won't Do"},
		},
		users: []jiraUser{
			{AccountID: "acct-dev", DisplayName: "dev", EmailAddress: "dev@acme.test", Active: true},
			{AccountID: "acct-flow", DisplayName: "flow", EmailAddress: "flow@acme.test", Active: true},
		},
	}
	for _, permission := range jiraPermissionKeys {
		fake.permissions[permission] = permission != "ADMINISTER_PROJECTS"
	}
	fake.seed(*fx)

	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	fake.site = srv.URL
	return fake, srv
}

// jiraFixtureProject is the key of the project the fixture becomes, and
// jiraFixtureKeyPrefix how its issues are spelled.
const (
	jiraFixtureProject   = "FLOW"
	jiraFixtureKeyPrefix = jiraFixtureProject + "-"
)

// jiraLinkedPullRequest is the pull request the linkage case reads. Jira is
// not the forge's tracker, so nothing the forge says it closes is a Jira key:
// what links the two is the key in the branch, which is Jira's own
// convention and what its integrations read.
func jiraLinkedPullRequest() core.PullRequest {
	pr := coretest.ContractForge().PullRequests[1]
	pr.HeadRef = "fix/" + jiraFixtureKeyPrefix + "11-the-last-event"
	pr.ClosingIssues = nil
	return pr
}

// seed fills the fake's world from the fixture.
func (f *jiraFake) seed(fx coretest.TrackerFixture) {
	for _, candidate := range fx.Candidates[core.ItemTrackerProject] {
		f.projects = append(f.projects, jiraProject{
			ID:   "project-" + candidate.ID,
			Key:  jiraFakeKeyOf(candidate.Name),
			Name: candidate.Name,
		})
	}
	f.projects = append(f.projects, jiraProject{
		ID:   "project-flow",
		Key:  f.key,
		Name: "Flow",
		IssueTypes: []jiraIssueType{
			{ID: "5", Name: "Sub-task", Subtask: true},
			{ID: "10001", Name: "Task"},
		},
	})
	for _, item := range fx.Observations[core.ItemTrackerStatusOptions].Items {
		f.statuses = append(f.statuses, jiraStatus{
			ID:             item.ID,
			Name:           item.Name,
			StatusCategory: jiraCategoryOf(item.Field("category")),
		})
	}
	// A column the fixture names and the observation has no option for is
	// still a status of the workflow: `workflow.columns` is written by
	// hand, and a board whose statuses flow never created is the ordinary
	// case.
	for id, name := range fx.Columns {
		if slices.ContainsFunc(f.statuses, func(s jiraStatus) bool { return s.ID == string(id) }) {
			continue
		}
		f.statuses = append(f.statuses, jiraStatus{
			ID: string(id), Name: name, StatusCategory: jiraFakeCategoryOf(name),
		})
	}
	slices.SortStableFunc(f.statuses, func(a, b jiraStatus) int { return strings.Compare(a.ID, b.ID) })

	for _, ticket := range fx.Tickets {
		f.issues = append(f.issues, f.issueOf(fx, ticket))
	}
}

// jiraFakeCategoryOf is the category a column's name means, for the columns
// the fixture names without saying.
func jiraFakeCategoryOf(name string) string {
	switch strings.ToLower(name) {
	case "todo", "to do", "ready", "backlog":
		return jiraCategoryTodo
	case "done":
		return jiraCategoryDone
	default:
		return jiraCategoryInProgress
	}
}

// jiraFakeKeyOf is a project key made from a name, as somebody setting a
// project up would: the first letters, upper-cased.
func jiraFakeKeyOf(name string) string {
	key := strings.ToUpper(name)
	key = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, key)
	if len(key) > 3 {
		key = key[:3]
	}
	return key
}

// issueOf turns one of the fixture's tickets into an issue.
func (f *jiraFake) issueOf(fx coretest.TrackerFixture, ticket core.Ticket) *jiraFakeIssue {
	status := jiraIssueStatus{ID: string(ticket.ColumnID), Name: ticket.Status.Name}
	for _, have := range f.statuses {
		if have.ID == status.ID {
			status.Name = have.Name
			status.StatusCategory.Key = jiraFakeCategoryKey(have.StatusCategory)
			status.StatusCategory.Name = have.Name
		}
	}
	fields := jiraIssueFields{
		Summary:  ticket.Title,
		Status:   status,
		Labels:   slices.Clone(ticket.Labels),
		Updated:  jiraTime{Time: ticket.UpdatedAt},
		Created:  jiraTime{Time: ticket.UpdatedAt},
		Assignee: jiraFakeAssignee(ticket.Assignees),
	}
	if ticket.Body != "" {
		body := adfFromText(ticket.Body)
		fields.Description = &body
	}
	if ticket.Parent != "" {
		fields.Parent = &jiraIssue{Key: fx.Key(ticket.Parent)}
	}
	if ticket.Size > 0 {
		seconds := int64(ticket.Size / time.Second)
		fields.TimeEstimate = &seconds
	}
	if ticket.Closed {
		name := "Done"
		if ticket.CloseReason == core.ClosedNotPlanned {
			name = "Won't Do"
		}
		fields.Resolution = &jiraResolution{ID: "10000", Name: name}
		fields.ResolutionDate = jiraTime{Time: ticket.ClosedAt}
	}
	return &jiraFakeIssue{
		id:       "issue-" + strings.TrimPrefix(ticket.Key, "#"),
		key:      fx.Key(ticket.Key),
		fields:   fields,
		archived: ticket.Archived,
	}
}

// jiraFakeAssignee is the account a ticket's assignees name.
func jiraFakeAssignee(assignees []string) *jiraUser {
	if len(assignees) == 0 {
		return nil
	}
	return &jiraUser{AccountID: "acct-" + assignees[0], DisplayName: assignees[0]}
}

// jiraFakeCategoryKey is the key an issue's status category carries, which is
// the older vocabulary the status API's enum folds onto.
func jiraFakeCategoryKey(category string) string {
	switch category {
	case jiraCategoryTodo:
		return "new"
	case jiraCategoryDone:
		return "done"
	default:
		return "indeterminate"
	}
}

// serve answers one request.
func (f *jiraFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("Accept") != jiraAccept {
		f.refuse(w, http.StatusNotAcceptable, "every call must ask for "+jiraAccept)
		return
	}
	if r.Header.Get("Authorization") == "" {
		f.refuse(w, http.StatusUnauthorized, "no credential was sent")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, JiraAPIPath+"/")
	f.requests = append(f.requests, r.Method+" "+path)
	f.asking = r

	segments := strings.Split(path, "/")
	switch {
	case path == "myself" && r.Method == http.MethodGet:
		f.write(w, jiraUser{AccountID: "acct-flow", DisplayName: "flow", EmailAddress: "flow@acme.test"})
	case path == "mypermissions" && r.Method == http.MethodGet:
		f.myPermissions(w, r)
	case path == "project/search" && r.Method == http.MethodGet:
		f.projectSearch(w, r)
	case path == "resolution" && r.Method == http.MethodGet:
		if f.refuseResolutions {
			f.refuse(w, http.StatusForbidden, "the credential may not read the resolution scheme")
			return
		}
		f.write(w, f.resolutions)
	case path == "user/search" && r.Method == http.MethodGet:
		f.userSearch(w, r)
	case path == "search/jql" && r.Method == http.MethodPost:
		f.search(w, r)
	case path == "issue" && r.Method == http.MethodPost:
		f.createIssue(w, r)
	case len(segments) == 3 && segments[0] == "project" && segments[2] == "statuses" && r.Method == http.MethodGet:
		f.projectStatuses(w, segments[1])
	case len(segments) == 2 && segments[0] == "project" && r.Method == http.MethodGet:
		f.project(w, segments[1])
	case len(segments) == 3 && segments[0] == "issue" && segments[2] == "transitions":
		f.transitions(w, r, segments[1])
	case len(segments) >= 3 && segments[0] == "issue" && segments[2] == "comment":
		f.comments(w, r, segments)
	case len(segments) == 2 && segments[0] == "issue":
		f.issue(w, r, segments[1])
	default:
		f.refuse(w, http.StatusNotFound, "no endpoint answers "+r.Method+" "+path)
	}
}

// myPermissions answers what the credential may do. Jira refuses a call that
// names no permission, which is what makes flow name the ones it needs.
func (f *jiraFake) myPermissions(w http.ResponseWriter, r *http.Request) {
	wanted := strings.Split(r.URL.Query().Get("permissions"), ",")
	if len(wanted) == 0 || wanted[0] == "" {
		f.refuse(w, http.StatusBadRequest, "no permissions were specified")
		return
	}
	if key := r.URL.Query().Get("projectKey"); key != "" && !strings.EqualFold(key, f.key) {
		f.refuse(w, http.StatusNotFound, "no project could be found with key '"+key+"'")
		return
	}
	payload := jiraPermissions{Permissions: map[string]struct {
		Key            string `json:"key"`
		HavePermission bool   `json:"havePermission"`
	}{}}
	for _, name := range wanted {
		payload.Permissions[name] = struct {
			Key            string `json:"key"`
			HavePermission bool   `json:"havePermission"`
		}{Key: name, HavePermission: f.permissions[name]}
	}
	f.write(w, payload)
}

// projectSearch answers one page of the projects.
func (f *jiraFake) projectSearch(w http.ResponseWriter, r *http.Request) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	size := f.pageSize
	end := min(start+size, len(f.projects))
	if start > len(f.projects) {
		start = len(f.projects)
	}
	f.write(w, jiraProjectPage{
		Values:   f.projects[start:end],
		StartAt:  start,
		MaxItems: size,
		Total:    len(f.projects),
		IsLast:   end >= len(f.projects),
	})
}

// project answers one project.
func (f *jiraFake) project(w http.ResponseWriter, key string) {
	for _, project := range f.projects {
		if strings.EqualFold(project.Key, key) {
			f.write(w, project)
			return
		}
	}
	f.refuse(w, http.StatusNotFound, "No project could be found with key '"+key+"'.")
}

// projectStatuses answers the statuses of a project's workflows, which Jira
// groups by issue type.
func (f *jiraFake) projectStatuses(w http.ResponseWriter, key string) {
	if !strings.EqualFold(key, f.key) {
		f.refuse(w, http.StatusNotFound, "No project could be found with key '"+key+"'.")
		return
	}
	type issueTypeStatuses struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Statuses []jiraStatus `json:"statuses"`
	}
	// Two issue types sharing a workflow: the same status appears twice,
	// which is what an adapter that did not deduplicate would report as
	// two columns of the same name.
	f.write(w, []issueTypeStatuses{
		{ID: "10001", Name: "Task", Statuses: f.statuses},
		{ID: "10002", Name: "Bug", Statuses: f.statuses},
	})
}

// userSearch answers who the instance knows.
func (f *jiraFake) userSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(r.URL.Query().Get("query"))
	var found []jiraUser
	for _, user := range f.users {
		if strings.Contains(strings.ToLower(user.DisplayName), query) || strings.Contains(strings.ToLower(user.EmailAddress), query) {
			found = append(found, user)
		}
	}
	f.write(w, found)
}

// search answers one page of a JQL search.
//
// The query is read rather than ignored: a fake that answered every search
// with every issue would make a missing clause invisible, and the clause is
// the whole of what a listing by column or by window means.
func (f *jiraFake) search(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JQL           string   `json:"jql"`
		MaxResults    int      `json:"maxResults"`
		Fields        []string `json:"fields"`
		NextPageToken string   `json:"nextPageToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.refuse(w, http.StatusBadRequest, "the body is not JSON")
		return
	}
	f.jqls = append(f.jqls, body.JQL)
	matching, err := f.matching(body.JQL)
	if err != nil {
		f.refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	start := 0
	if body.NextPageToken != "" {
		if start, err = strconv.Atoi(body.NextPageToken); err != nil {
			f.refuse(w, http.StatusBadRequest, "the page token is not one of ours")
			return
		}
	}
	end := min(start+f.pageSize, len(matching))
	page := jiraSearchPage{IsLast: end >= len(matching)}
	for _, issue := range matching[start:end] {
		page.Issues = append(page.Issues, f.render(issue, body.Fields))
	}
	if !page.IsLast {
		page.NextPageToken = strconv.Itoa(end)
	}
	f.write(w, page)
}

// matching is the issues a JQL query selects, in the order it asks for.
//
// Only the shapes flow writes are understood, and anything else is a refusal:
// a fake that quietly ignored a clause it had not been taught would pass an
// adapter that asked the wrong question.
func (f *jiraFake) matching(jql string) ([]*jiraFakeIssue, error) {
	expression, order, _ := strings.Cut(jql, " ORDER BY ")
	var (
		project string
		status  string
		label   string
		since   time.Time
	)
	for _, clause := range strings.Split(expression, " AND ") {
		field, value, ok := jiraFakeClause(clause)
		if !ok {
			return nil, fmt.Errorf("the clause %q is not one flow writes", clause)
		}
		switch field {
		case "project":
			project = value
		case "status":
			status = value
		case "labels":
			label = value
		case "updated":
			when, err := time.Parse("2006-01-02 15:04", value)
			if err != nil {
				return nil, fmt.Errorf("%q is not a JQL date", value)
			}
			since = when.UTC()
		default:
			return nil, fmt.Errorf("the field %q is not one flow searches on", field)
		}
	}
	if !strings.EqualFold(project, f.key) {
		return nil, fmt.Errorf("no project could be found with key '%s'", project)
	}

	var matching []*jiraFakeIssue
	for _, issue := range f.issues {
		switch {
		// An archived issue is in no search at all. It is the one place
		// two readings of the same Jira disagree.
		case issue.archived:
			continue
		case status != "" && !strings.EqualFold(issue.fields.Status.Name, status):
			continue
		case label != "" && !slices.Contains(issue.fields.Labels, label):
			continue
		case !since.IsZero() && issue.fields.Updated.Time.Before(since):
			continue
		}
		matching = append(matching, issue)
	}
	switch order {
	case "updated DESC":
		slices.SortStableFunc(matching, func(a, b *jiraFakeIssue) int {
			return b.fields.Updated.Time.Compare(a.fields.Updated.Time)
		})
	case "created ASC", "":
		// The fake holds them oldest first already.
	case "created DESC":
		slices.Reverse(matching)
	default:
		return nil, fmt.Errorf("the ordering %q is not one flow asks for", order)
	}
	return matching, nil
}

// jiraFakeClause reads one `field <op> "value"` clause.
func jiraFakeClause(clause string) (field, value string, ok bool) {
	trimmed := strings.TrimSpace(clause)
	for _, operator := range []string{" >= ", " = "} {
		name, raw, found := strings.Cut(trimmed, operator)
		if !found {
			continue
		}
		unquoted, err := strconv.Unquote(raw)
		if err != nil {
			return "", "", false
		}
		return name, unquoted, true
	}
	return "", "", false
}

// render is one issue as a search answers it: the fields that were asked for
// and no others. An answer that carried everything would make a `fields` list
// missing a name invisible, and the list is how a search stays affordable.
func (f *jiraFake) render(issue *jiraFakeIssue, fields []string) jiraIssue {
	out := jiraIssue{ID: issue.id, Key: issue.key, Self: f.site + JiraAPIPath + "/issue/" + issue.key}
	if len(fields) == 0 {
		out.Fields = issue.fields
		return out
	}
	for _, field := range fields {
		switch field {
		case "summary":
			out.Fields.Summary = issue.fields.Summary
		case "description":
			out.Fields.Description = issue.fields.Description
		case "status":
			out.Fields.Status = issue.fields.Status
		case "labels":
			out.Fields.Labels = issue.fields.Labels
		case "assignee":
			out.Fields.Assignee = issue.fields.Assignee
		case "parent":
			out.Fields.Parent = issue.fields.Parent
		case "updated":
			out.Fields.Updated = issue.fields.Updated
		case "created":
			out.Fields.Created = issue.fields.Created
		case "resolution":
			out.Fields.Resolution = issue.fields.Resolution
		case "resolutiondate":
			out.Fields.ResolutionDate = issue.fields.ResolutionDate
		case "timeoriginalestimate":
			out.Fields.TimeEstimate = issue.fields.TimeEstimate
		case "key":
			// The key is not a field: it is on the issue itself, and
			// Jira accepts it in the list and answers nothing extra.
		}
	}
	return out
}

// issue answers or edits one issue.
func (f *jiraFake) issue(w http.ResponseWriter, r *http.Request, key string) {
	found := f.find(key)
	if found == nil {
		f.refuse(w, http.StatusNotFound, "Issue does not exist or you do not have permission to see it.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		var fields []string
		if list := r.URL.Query().Get("fields"); list != "" {
			fields = strings.Split(list, ",")
		}
		f.write(w, f.render(found, fields))
	case http.MethodPut:
		if found.archived {
			f.refuse(w, http.StatusBadRequest, "Issue is archived and cannot be edited.")
			return
		}
		var body struct {
			Fields struct {
				Summary     *string `json:"summary"`
				Description *adfDoc `json:"description"`
			} `json:"fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.refuse(w, http.StatusBadRequest, "the body is not JSON")
			return
		}
		if body.Fields.Summary != nil {
			found.fields.Summary = *body.Fields.Summary
		}
		if body.Fields.Description != nil {
			found.fields.Description = body.Fields.Description
		}
		f.tick()
		found.fields.Updated = jiraTime{Time: f.now}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.refuse(w, http.StatusMethodNotAllowed, "an issue takes GET and PUT")
	}
}

// createIssue files one.
func (f *jiraFake) createIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields struct {
			Project     struct{ Key string } `json:"project"`
			Summary     string               `json:"summary"`
			Description *adfDoc              `json:"description"`
			Labels      []string             `json:"labels"`
			IssueType   struct{ ID string }  `json:"issuetype"`
			Assignee    *struct {
				AccountID string `json:"accountId"`
			} `json:"assignee"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.refuse(w, http.StatusBadRequest, "the body is not JSON")
		return
	}
	switch {
	case !strings.EqualFold(body.Fields.Project.Key, f.key):
		f.refuse(w, http.StatusBadRequest, "project: no project could be found with key '"+body.Fields.Project.Key+"'")
		return
	case body.Fields.IssueType.ID == "":
		f.refuse(w, http.StatusBadRequest, "issuetype: issue type is required")
		return
	case body.Fields.Summary == "":
		f.refuse(w, http.StatusBadRequest, "summary: you must specify a summary of the issue")
		return
	}
	for _, label := range body.Fields.Labels {
		if strings.ContainsAny(label, " \t") {
			f.refuse(w, http.StatusBadRequest, "labels: labels may not contain spaces")
			return
		}
	}
	f.tick()
	f.nextID++
	issue := &jiraFakeIssue{
		id:  strconv.Itoa(f.nextID),
		key: f.key + "-" + strconv.Itoa(f.nextID),
		fields: jiraIssueFields{
			Summary:     body.Fields.Summary,
			Description: body.Fields.Description,
			Labels:      body.Fields.Labels,
			Status:      f.statusAt(0),
			Updated:     jiraTime{Time: f.now},
			Created:     jiraTime{Time: f.now},
		},
	}
	if body.Fields.Assignee != nil {
		for _, user := range f.users {
			if user.AccountID == body.Fields.Assignee.AccountID {
				assignee := user
				issue.fields.Assignee = &assignee
			}
		}
	}
	f.issues = append(f.issues, issue)
	w.WriteHeader(http.StatusCreated)
	f.write(w, jiraIssue{ID: issue.id, Key: issue.key, Self: f.site + JiraAPIPath + "/issue/" + issue.key})
}

// statusAt is the status an issue starts in: the first the workflow offers.
func (f *jiraFake) statusAt(index int) jiraIssueStatus {
	if index >= len(f.statuses) {
		return jiraIssueStatus{}
	}
	status := f.statuses[index]
	out := jiraIssueStatus{ID: status.ID, Name: status.Name}
	out.StatusCategory.Key = jiraFakeCategoryKey(status.StatusCategory)
	return out
}

// transitions lists or performs the moves an issue may make.
func (f *jiraFake) transitions(w http.ResponseWriter, r *http.Request, key string) {
	found := f.find(key)
	if found == nil {
		f.refuse(w, http.StatusNotFound, "Issue does not exist or you do not have permission to see it.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.write(w, jiraTransitions{Transitions: f.offer(found)})
	case http.MethodPost:
		var body struct {
			Transition struct{ ID string } `json:"transition"`
			Fields     map[string]any      `json:"fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.refuse(w, http.StatusBadRequest, "the body is not JSON")
			return
		}
		index := slices.IndexFunc(f.offer(found), func(t jiraTransition) bool { return t.ID == body.Transition.ID })
		if index < 0 {
			f.refuse(w, http.StatusBadRequest, "transition: it is not an allowed transition from the issue's status")
			return
		}
		chosen := f.offer(found)[index]
		if _, ok := body.Fields["resolution"]; ok && !chosen.takesResolution() {
			f.refuse(w, http.StatusBadRequest, "resolution: field cannot be set; it is not on the appropriate screen")
			return
		}
		found.fields.Status = chosen.To
		f.tick()
		found.fields.Updated = jiraTime{Time: f.now}
		if raw, ok := body.Fields["resolution"]; ok {
			switch value := raw.(type) {
			case nil:
				found.fields.Resolution = nil
				found.fields.ResolutionDate = jiraTime{}
			case map[string]any:
				name, _ := value["name"].(string)
				found.fields.Resolution = &jiraResolution{ID: "10000", Name: name}
				found.fields.ResolutionDate = jiraTime{Time: f.now}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.refuse(w, http.StatusMethodNotAllowed, "transitions take GET and POST")
	}
}

// offer is every transition an issue may make from where it is: one to each
// other status, which is a workflow with no restrictions on it. The screen of
// a transition into or out of the done category takes a resolution, as Jira's
// own default workflow does.
func (f *jiraFake) offer(issue *jiraFakeIssue) []jiraTransition {
	var offered []jiraTransition
	if f.frozenWorkflow {
		return nil
	}
	for index, status := range f.statuses {
		if status.ID == issue.fields.Status.ID {
			continue
		}
		to := jiraIssueStatus{ID: status.ID, Name: status.Name}
		to.StatusCategory.Key = jiraFakeCategoryKey(status.StatusCategory)
		transition := jiraTransition{
			ID:   strconv.Itoa(index + 11),
			Name: status.Name,
			To:   to,
		}
		resolving := status.StatusCategory == jiraCategoryDone ||
			jiraFakeCategoryKey(status.StatusCategory) != "done" && issue.fields.Resolution != nil
		if resolving && !f.noResolutionField {
			transition.Fields = map[string]struct {
				Required bool   `json:"required"`
				Name     string `json:"name"`
			}{"resolution": {Name: "Resolution"}}
		}
		offered = append(offered, transition)
	}
	return offered
}

// comments lists, writes and rewrites an issue's comments.
func (f *jiraFake) comments(w http.ResponseWriter, r *http.Request, segments []string) {
	found := f.find(segments[1])
	if found == nil {
		f.refuse(w, http.StatusNotFound, "Issue does not exist or you do not have permission to see it.")
		return
	}
	switch {
	case r.Method == http.MethodGet && len(segments) == 3:
		// Jira orders a page as it was asked to, and flow asks in two
		// different orders for two different questions.
		ordered := slices.Clone(found.comments)
		if r.URL.Query().Get("orderBy") == "-created" {
			slices.Reverse(ordered)
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		end := min(start+f.pageSize, len(ordered))
		if start > len(ordered) {
			start = len(ordered)
		}
		f.write(w, jiraCommentPage{
			Comments: ordered[start:end],
			StartAt:  start,
			MaxItems: f.pageSize,
			Total:    len(found.comments),
		})
	case r.Method == http.MethodPost && len(segments) == 3:
		body := f.commentBody(w, r)
		if body == nil {
			return
		}
		f.tick()
		f.nextID++
		found.comments = append(found.comments, jiraComment{
			ID: strconv.Itoa(f.nextID), Body: body,
			Author:  &jiraUser{AccountID: "acct-flow", DisplayName: "Flow Bot"},
			Created: jiraTime{Time: f.now}, Updated: jiraTime{Time: f.now},
		})
		w.WriteHeader(http.StatusCreated)
		f.write(w, found.comments[len(found.comments)-1])
	case r.Method == http.MethodPut && len(segments) == 4:
		body := f.commentBody(w, r)
		if body == nil {
			return
		}
		index := slices.IndexFunc(found.comments, func(c jiraComment) bool { return c.ID == segments[3] })
		if index < 0 {
			f.refuse(w, http.StatusNotFound, "Comment does not exist.")
			return
		}
		f.tick()
		found.comments[index].Body = body
		found.comments[index].Updated = jiraTime{Time: f.now}
		f.write(w, found.comments[index])
	default:
		f.refuse(w, http.StatusMethodNotAllowed, "comments take GET, POST and PUT")
	}
}

// commentBody reads the body of a comment, refusing one that is not a
// document: Jira's v3 API takes no strings.
func (f *jiraFake) commentBody(w http.ResponseWriter, r *http.Request) *adfDoc {
	var body struct {
		Body *adfDoc `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == nil {
		f.refuse(w, http.StatusBadRequest, "body: expected an Atlassian Document Format document")
		return nil
	}
	if body.Body.Type != adfDocType {
		f.refuse(w, http.StatusBadRequest, "body: expected an Atlassian Document Format document")
		return nil
	}
	return body.Body
}

// find is the issue with a key, archived or not.
func (f *jiraFake) find(key string) *jiraFakeIssue {
	for _, issue := range f.issues {
		if strings.EqualFold(issue.key, key) {
			return issue
		}
	}
	return nil
}

// tick advances the fake's clock, as a write does.
func (f *jiraFake) tick() { f.now = f.now.Add(time.Minute) }

// write answers with a payload, tagged so that a caller holding the same
// answer is told it already has it.
func (f *jiraFake) write(w http.ResponseWriter, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		f.refuse(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	if f.asking != nil && f.asking.Header.Get("If-None-Match") == etag {
		f.notModified++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(raw)
}

// jiraError is the shape Jira refuses in: a list of messages and sometimes a
// map of per-field ones. Flow reads it through [core.APIMessage] rather than
// here, so the fake owns the shape and the adapter owns nothing.
type jiraError struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// refuse answers the way Jira refuses: a status and a list of messages.
func (f *jiraFake) refuse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jiraError{ErrorMessages: []string{message}})
}

// asked reports how many times the fake was asked something matching a
// prefix, such as "GET issue/".
func (f *jiraFake) asked(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if strings.HasPrefix(request, prefix) {
			count++
		}
	}
	return count
}
