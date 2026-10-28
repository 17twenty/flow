package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/17twenty/flow/internal/core"
)

// Jira Cloud adapter constants (spec 11 §3).
const (
	// JiraCloudKind is the adapter name, matching
	// assets/capabilities/jira-cloud.yaml and tracker.type in
	// configuration.
	JiraCloudKind = "jira-cloud"
	// JiraAPIPath is where a Jira Cloud site answers. Every site is
	// somebody's own address, so tracker.url is not optional for this
	// adapter.
	JiraAPIPath = "/rest/api/3"
	// jiraAccept is the media type every call asks for.
	jiraAccept = "application/json"
	// jiraPageSize is how many results one page carries. Jira's search
	// caps a page at 100 and quietly reduces anything larger.
	jiraPageSize = 100
)

// The column categories, which are flow's own mapping of a column onto the
// three states work can be in (spec 04 §2). They are re-declared here rather
// than imported: an adapter may not depend on `internal/config` (spec 01 §2),
// and these three strings are the configuration's vocabulary.
const (
	categoryTodo       = "todo"
	categoryInProgress = "in_progress"
	categoryDone       = "done"
)

// Jira's permission vocabulary, as assets/capabilities/jira-cloud.yaml is
// written in it. A probe and a declaration that do not share a vocabulary can
// never agree (spec 05 §1).
const (
	jiraTokenBrowse           = "browse"
	jiraTokenCreateIssues     = "create_issues"
	jiraTokenEditIssues       = "edit_issues"
	jiraTokenTransitionIssues = "transition_issues"
	jiraTokenAddComments      = "add_comments"
	jiraTokenProjectAdmin     = "project_admin"
)

// jiraPermissionKeys are the permissions flow asks about. Jira's
// /mypermissions refuses a call that names none, so the list is flow's own
// and is the same list the declaration's `requires` is drawn from.
var jiraPermissionKeys = []string{
	"BROWSE_PROJECTS", "CREATE_ISSUES", "EDIT_ISSUES",
	"TRANSITION_ISSUES", "ADD_COMMENTS", "ADMINISTER_PROJECTS",
}

// JiraHeader returns the headers every Jira call carries. The root passes it
// to [core.ClientOptions], as it does GitHub's.
func JiraHeader() http.Header {
	h := http.Header{}
	h.Set("Accept", jiraAccept)
	return h
}

// JiraCloudOptions configures a [JiraCloud] adapter.
type JiraCloudOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Site is the web address of the instance, such as
	// https://acme.atlassian.net, which is where people read its issues.
	Site string
	// Project is the project key, such as FLOW. It is what
	// tracker.project resolves to, and it may be empty before setup has
	// found one.
	Project string
	// Capabilities is the declaration loaded from
	// assets/capabilities/jira-cloud.yaml.
	Capabilities core.CapabilitySet
	// FollowUpLabels is followups.labels: what every follow-up and the
	// health issue are filed with, and so what the search for one is
	// narrowed by (issue #296). Empty means the whole project is read.
	FollowUpLabels []string
}

// JiraCloud is the Jira Cloud tracker adapter.
//
// One site and one project: a Jira project is the board flow works, and the
// site is its owner. A column is a status rather than a board column
// (spec 11 §3), because a board column is several statuses and flow's
// configuration names one thing per column.
type JiraCloud struct {
	client *core.Client
	site   string
	key    string
	caps   core.CapabilitySet
	// followUpLabel is the label the follow-up search is narrowed by, or
	// empty when none is configured that Jira would accept.
	followUpLabel string

	// writing serialises the calls that change an issue: Jira applies a
	// transition and an edit through the same workflow, and two at once
	// buy nothing.
	writing sync.Mutex

	// mu guards everything read once a run.
	mu sync.Mutex
	// live is the set of keys a full listing of the project returned,
	// which is how an archived issue is told from a live one: Jira
	// publishes no archived flag anywhere, and an archived issue is
	// exactly one that answers by key and is absent from a full search
	// (spec 11 §3).
	live map[string]bool
	// filed are the follow-ups filed in this process, so that a search run
	// before the project has caught up still finds one.
	filed []jiraFiled
	// resolutions is the instance's resolution scheme, read at most once.
	resolutions []jiraResolution
	// project is the configured project, read at most once.
	project *jiraProject
}

// jiraFiled is a follow-up this process filed.
type jiraFiled struct {
	marker core.Marker
	ticket core.Ticket
}

// NewJiraCloud returns an adapter for one Jira project.
func NewJiraCloud(o JiraCloudOptions) (*JiraCloud, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the jira-cloud adapter").
			WithWhy("no HTTP client was given")
	case o.Site == "":
		return nil, core.Errorf(core.ErrValidation, "build the jira-cloud adapter").
			WithWhy("no site was given, and a Jira answers wherever it was bought").
			WithFix("set tracker.url to the site, such as https://acme.atlassian.net")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the jira-cloud adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/jira-cloud.yaml and pass the declaration")
	}
	return &JiraCloud{
		client: o.Client,
		site:   strings.TrimSuffix(o.Site, "/"),
		key:    strings.ToUpper(strings.TrimSpace(o.Project)),
		caps:   o.Capabilities,
		// Any one label will do: CreateFollowUp applies every usable
		// label to every follow-up, so each of them names the same set.
		followUpLabel: jiraFollowUpLabel(o.FollowUpLabels),
	}, nil
}

// jiraFollowUpLabel is the first label CreateFollowUp would apply, or empty.
// A label with a space is left off the filing, so narrowing by it would
// find nothing at all.
func jiraFollowUpLabel(labels []string) string {
	for _, label := range labels {
		if jiraLabelUsable(label) {
			return label
		}
	}
	return ""
}

// jiraLabelUsable is whether Jira takes a label: it holds no spaces.
func jiraLabelUsable(label string) bool {
	return label != "" && !strings.ContainsAny(label, " \t")
}

// Kind is the adapter's name.
func (j *JiraCloud) Kind() string { return JiraCloudKind }

// Capabilities is what this adapter declares it can do.
func (j *JiraCloud) Capabilities() core.CapabilitySet { return j.caps }

// Slug is the board as people write it: the site and the project key.
func (j *JiraCloud) Slug() string {
	host := strings.TrimPrefix(strings.TrimPrefix(j.site, "https://"), "http://")
	if j.key == "" {
		return host
	}
	return host + "/" + j.key
}

// Probe reports what the credential may do in this project (spec 05 §3).
func (j *JiraCloud) Probe(ctx context.Context) (core.Permissions, error) {
	var me jiraUser
	if _, err := j.do(ctx, core.Request{Path: "myself", Out: &me}); err != nil {
		return core.Permissions{}, core.Explain(err, "ask %s who the credential belongs to", j.Slug())
	}
	permissions := core.Permissions{Evidence: []string{"myself: " + me.login()}}

	query := url.Values{"permissions": []string{strings.Join(jiraPermissionKeys, ",")}}
	if j.key != "" {
		query.Set("projectKey", j.key)
	}
	var held jiraPermissions
	if _, err := j.do(ctx, core.Request{Path: "mypermissions", Query: query, Out: &held}); err != nil {
		// A project nobody has chosen yet, or one the credential cannot
		// see, is not a failed probe: it is what discovery is for, and
		// the identity above is already a permission worth reporting.
		if !errors.Is(err, core.ErrNotFound) {
			return core.Permissions{}, core.Explain(err, "ask %s what the credential may do", j.Slug())
		}
		// Jira answers 404 for a project that is gone and for one the
		// account may not browse alike, and both leave the credential
		// blind to the board it was given (issue #292). Only a request
		// naming a project can come back 404, so one is always named.
		permissions.Unreachable = "the project " + j.key
		return permissions, nil
	}
	permissions.Tokens = held.tokens()
	permissions.Evidence = append(permissions.Evidence,
		fmt.Sprintf("mypermissions: %s", strings.Join(permissions.Tokens, ", ")))
	return permissions, nil
}

// Discover offers the projects that could answer a placeholder (spec 07 §3).
func (j *JiraCloud) Discover(ctx context.Context, what string, hint map[string]string) ([]core.Candidate, error) {
	if what != core.ItemTrackerProject {
		return nil, nil
	}
	var candidates []core.Candidate
	start := 0
	for {
		var page jiraProjectPage
		query := url.Values{
			"startAt":    []string{fmt.Sprint(start)},
			"maxResults": []string{fmt.Sprint(jiraPageSize)},
			"orderBy":    []string{"key"},
			"expand":     []string{"description"},
			"typeKey":    []string{"software,business"},
			"status":     []string{"live"},
			"action":     []string{"browse"},
		}
		if _, err := j.do(ctx, core.Request{Path: "project/search", Query: query, Out: &page}); err != nil {
			return nil, core.Explain(err, "ask %s which projects there are", j.Slug())
		}
		for _, project := range page.Values {
			if !jiraHintMatches(project, hint) {
				continue
			}
			candidates = append(candidates, core.Candidate{
				ID:     project.Key,
				Name:   project.Name,
				URL:    jiraProjectURL(j.site, project.Key),
				Detail: "project " + project.Key,
			})
		}
		start += len(page.Values)
		if page.IsLast || len(page.Values) == 0 {
			return candidates, nil
		}
	}
}

// jiraHintMatches reports whether a project answers what configuration
// already knows. A hint narrows and never reorders (spec 07 §3).
func jiraHintMatches(project jiraProject, hint map[string]string) bool {
	for field, want := range hint {
		if want == "" {
			continue
		}
		switch field {
		case "name":
			if !strings.EqualFold(project.Name, want) {
				return false
			}
		case "key", "project":
			if !strings.EqualFold(project.Key, want) {
				return false
			}
		}
	}
	return true
}

// ParseRef turns an address somebody pasted into the project it names.
//
// Jira writes a project's address four ways — /browse/KEY, /projects/KEY,
// /jira/software/projects/KEY/boards/1 and the REST self link — and all four
// carry the key in a segment after a word flow knows. Any project is read,
// not only the configured one: this is what answers the question of which
// project to configure.
func (j *JiraCloud) ParseRef(raw string) (core.Ref, error) {
	refuse := func(why string, args ...any) (core.Ref, error) {
		return core.Ref{}, core.Errorf(core.ErrValidation, "read %q as a project", raw).
			WithWhy(why, args...).
			WithFix("paste the project's web address, e.g. %s", jiraProjectURL(j.site, "FLOW"))
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return refuse("it is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return refuse("it is not a web address")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, segment := range segments {
		if i+1 >= len(segments) {
			break
		}
		switch segment {
		case "browse", "projects", "project":
			key := strings.ToUpper(jiraKeyOf(segments[i+1]))
			if key == "" {
				return refuse("%q is not a project key", segments[i+1])
			}
			return core.Ref{
				What: core.ItemTrackerProject,
				ID:   key,
				URL:  jiraProjectURL(j.site, key),
			}, nil
		}
	}
	return refuse("its path names no project: flow looks for /browse/<key> or /projects/<key>")
}

// jiraKeyOf is the project key a segment carries: the whole segment, or the
// part in front of the issue number when somebody pasted an issue's address
// rather than the project's.
func jiraKeyOf(segment string) string {
	key, _, found := strings.Cut(segment, "-")
	if !found {
		key = segment
	}
	for _, r := range key {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ""
		}
	}
	return key
}

// Observe returns the current state of one desired item, canonically
// (spec 06 §2).
//
// Only two of the five are observable here, and the declaration says so. A
// Jira project's labels are free-form — a label exists the moment an issue
// carries one and there is no registry to look in — and neither a board's
// work-in-progress limits nor an expedite view can be read for a project that
// may have several boards. Saying so is a finding; answering with an empty
// list would be a lie that reads as drift for ever (spec 05 §1).
func (j *JiraCloud) Observe(ctx context.Context, what string) (core.Observation, error) {
	observation := core.Observation{What: what}
	switch what {
	case core.ItemTrackerProject:
		project, err := j.lookUpProject(ctx)
		switch {
		case errors.Is(err, core.ErrNotFound):
			return observation.Canonical(), nil
		case err != nil:
			return core.Observation{}, err
		case j.key == "":
			return observation.Canonical(), nil
		}
		observation.Exists = true
		observation.Items = []core.Item{{ID: project.ID, Name: project.Name}}
		return observation.Canonical(), nil

	case core.ItemTrackerStatusOptions:
		statuses, err := j.statuses(ctx)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return observation.Canonical(), nil
			}
			return core.Observation{}, err
		}
		observation.Exists = len(statuses) > 0
		for _, status := range statuses {
			observation.Items = append(observation.Items, status.toItem())
		}
		return observation.Canonical(), nil

	case core.ItemTrackerLabels, core.ItemTrackerWIP, core.ItemTrackerExpedite:
		return observation.Canonical(), nil

	default:
		return core.Observation{}, jiraNoSuchItem("observe", what)
	}
}

// jiraNoSuchItem explains that a tracker has nothing of that name.
func jiraNoSuchItem(verb, what string) error {
	return core.Errorf(core.ErrValidation, "%s %s", verb, what).
		WithWhy("a tracker has nothing called that").
		WithFix("ask the tracker about one of %s", strings.Join(jiraTrackerItems(), ", "))
}

// jiraTrackerItems is every item a tracker answers for, in the order spec 03
// declares them.
func jiraTrackerItems() []string {
	return []string{
		core.ItemTrackerProject,
		core.ItemTrackerStatusOptions,
		core.ItemTrackerLabels,
		core.ItemTrackerWIP,
		core.ItemTrackerExpedite,
	}
}

// Apply makes one change — which, for a Jira project, is none of them.
//
// A status can be created through the status API, and creating one would not
// make a column: which statuses a project's workflow offers is the workflow's
// business, and which column of the board a status appears in is the board's,
// and neither is writable in the public Cloud API (assets/research). A flow
// that created a status nobody could reach would report a column the team
// cannot see. So every change is refused here and the declaration carries the
// remediation that gets it done: a follow-up for somebody who administers the
// project (spec 05 §4).
func (j *JiraCloud) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if !slices.Contains(jiraTrackerItems(), c.What) {
		return core.ApplyResult{}, jiraNoSuchItem(string(c.Op), c.What)
	}
	return core.ApplyResult{}, core.Errorf(core.ErrUnsupported, "%s %s %q", c.Op, c.What, c.Item.Name).
		WithWhy("%s is configured by its project and board administrators, and Jira Cloud's API publishes no way to change it", j.Slug()).
		WithFix("ask somebody who administers the project to make the change; flow files a follow-up for it")
}

// Ticket returns one ticket by key.
func (j *JiraCloud) Ticket(ctx context.Context, key string) (core.Ticket, error) {
	issue, err := j.issue(ctx, key)
	if err != nil {
		return core.Ticket{}, err
	}
	ticket := issue.toTicket(j.site)
	archived, err := j.archived(ctx, issue.Key)
	if err != nil {
		return core.Ticket{}, err
	}
	ticket.Archived = archived
	return ticket, nil
}

// issue reads one issue by key.
func (j *JiraCloud) issue(ctx context.Context, key string) (jiraIssue, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return jiraIssue{}, core.Errorf(core.ErrValidation, "read a ticket").
			WithWhy("no key was given").
			WithFix("name an issue, such as %s-12", cmpOr(j.key, "FLOW"))
	}
	var issue jiraIssue
	query := url.Values{"fields": []string{strings.Join(jiraIssueFieldNames, ",")}}
	_, err := j.do(ctx, core.Request{Path: "issue/" + url.PathEscape(trimmed), Query: query, Out: &issue})
	if err != nil {
		return jiraIssue{}, core.Explain(err, "read %s", trimmed)
	}
	return issue, nil
}

// cmpOr is the first value that is not empty.
func cmpOr(first, second string) string {
	if first != "" {
		return first
	}
	return second
}

// Tickets returns the tickets matching a query.
//
// A query by key is a question about issues and a query by anything else is a
// question about the board, and Jira answers the two differently: a search
// never returns an archived issue, and a lookup by key always does (spec 11
// §3). So keys are read one at a time and everything else is one search.
func (j *JiraCloud) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	if len(q.Keys) > 0 {
		return j.ticketsByKey(ctx, q.Keys)
	}
	jql, err := j.jql(ctx, q)
	if err != nil {
		return nil, err
	}
	issues, err := j.search(ctx, jql, jiraIssueFieldNames)
	if err != nil {
		return nil, err
	}
	tickets := make([]core.Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, issue.toTicket(j.site))
	}
	return tickets, nil
}

// ticketsByKey reads each key in turn. A key the project does not hold is
// left out rather than failing the listing: a caller asking about five
// tickets wants the four that are there.
func (j *JiraCloud) ticketsByKey(ctx context.Context, keys []string) ([]core.Ticket, error) {
	tickets := make([]core.Ticket, 0, len(keys))
	for _, key := range keys {
		ticket, err := j.Ticket(ctx, key)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return tickets, nil
}

// jql writes the query a listing asks.
func (j *JiraCloud) jql(ctx context.Context, q core.TicketQuery) (string, error) {
	if j.key == "" {
		return "", j.unnumbered("list the tickets")
	}
	clauses := []string{"project = " + jiraQuote(j.key)}
	if q.Column != "" {
		status, err := j.statusOf(ctx, q.Column)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, "status = "+jiraQuote(status.Name))
	}
	if !q.UpdatedSince.IsZero() {
		clauses = append(clauses, "updated >= "+jiraQuote(jiraJQLTime(q.UpdatedSince)))
	}
	return strings.Join(clauses, " AND ") + " ORDER BY updated DESC", nil
}

// search runs one JQL query and returns every page of it.
func (j *JiraCloud) search(ctx context.Context, jql string, fields []string) ([]jiraIssue, error) {
	var issues []jiraIssue
	token := ""
	for {
		body := map[string]any{
			"jql":        jql,
			"maxResults": jiraPageSize,
			"fields":     fields,
		}
		if token != "" {
			body["nextPageToken"] = token
		}
		var page jiraSearchPage
		_, err := j.do(ctx, core.Request{
			Method: http.MethodPost, Path: "search/jql", Body: body, Out: &page,
		})
		if err != nil {
			return nil, core.Explain(err, "search %s", j.Slug())
		}
		issues = append(issues, page.Issues...)
		if page.NextPageToken == "" || len(page.Issues) == 0 {
			return issues, nil
		}
		token = page.NextPageToken
	}
}

// archived reports whether an issue is archived, which Jira says nowhere.
//
// An archived issue is read-only, keeps its status, and drops out of every
// search; it still answers by key in full. So the two readings are compared:
// present by key and absent from a full listing of the project is the only
// evidence there is (assets/research, spec 11 §3). The listing is read once a
// run, because archiving is not something that happens while flow is looking.
//
// The key is the one Jira answered with and never the one flow was handed:
// a Jira key is case-insensitive to ask with and canonical to read back, so
// both sides of this comparison come from the provider.
func (j *JiraCloud) archived(ctx context.Context, key string) (bool, error) {
	if j.key == "" {
		return false, nil
	}
	live, err := j.liveKeys(ctx)
	if err != nil {
		return false, err
	}
	return !live[key], nil
}

// liveKeys is every key a full listing of the project returns.
func (j *JiraCloud) liveKeys(ctx context.Context) (map[string]bool, error) {
	j.mu.Lock()
	cached := j.live
	j.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	// Deliberately the whole project and nothing else: absence from a
	// narrowed listing means the filter, not the archive.
	issues, err := j.search(ctx, "project = "+jiraQuote(j.key)+" ORDER BY created ASC", []string{"key"})
	if err != nil {
		return nil, core.Explain(err, "list %s to tell an archived issue from a live one", j.Slug())
	}
	live := make(map[string]bool, len(issues))
	for _, issue := range issues {
		live[issue.Key] = true
	}
	j.mu.Lock()
	j.live = live
	j.mu.Unlock()
	return live, nil
}

// Move takes a ticket to another column, which in Jira is a transition to
// another status (spec 04 §5).
func (j *JiraCloud) Move(ctx context.Context, key string, to core.ColumnID) error {
	j.writing.Lock()
	defer j.writing.Unlock()

	target, err := j.statusOf(ctx, to)
	if err != nil {
		return err
	}
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return err
	}
	switch {
	case ticket.Archived:
		return core.Errorf(core.ErrConflict, "move %s to %q", ticket.Key, target.Name).
			WithWhy("%s is archived, and an archived issue is read-only", ticket.Key).
			WithFix("restore the issue in Jira if the work is live")
	case ticket.Status.ID == target.ID:
		return nil
	}
	return j.transition(ctx, ticket, func(t jiraTransition) bool { return t.To.ID == target.ID },
		nil, fmt.Sprintf("move %s to %q", ticket.Key, target.Name),
		fmt.Sprintf("the workflow of %s offers no transition from %q to %q", ticket.Key, ticket.Status.Name, target.Name),
		"ask somebody who administers the workflow to allow it, or configure the columns the workflow has")
}

// transition performs the first transition the predicate accepts, with
// whatever fields the caller wants set on the way through.
func (j *JiraCloud) transition(ctx context.Context, ticket core.Ticket, want func(jiraTransition) bool, fields func(jiraTransition) map[string]any, what, why, fix string) error {
	var available jiraTransitions
	query := url.Values{"expand": []string{"transitions.fields"}}
	path := "issue/" + url.PathEscape(ticket.Key) + "/transitions"
	if _, err := j.do(ctx, core.Request{Path: path, Query: query, Out: &available}); err != nil {
		return core.Explain(err, "ask what %s may do next", ticket.Key)
	}
	index := slices.IndexFunc(available.Transitions, want)
	if index < 0 {
		return core.Errorf(core.ErrConflict, "%s", what).WithWhy("%s", why).WithFix("%s", fix)
	}
	chosen := available.Transitions[index]
	body := map[string]any{"transition": map[string]string{"id": chosen.ID}}
	if fields != nil {
		if set := fields(chosen); len(set) > 0 {
			body["fields"] = set
		}
	}
	if _, err := j.do(ctx, core.Request{Method: http.MethodPost, Path: path, Body: body}); err != nil {
		return core.Explain(err, "%s", what)
	}
	return nil
}

// statusOf resolves a column onto the project's statuses, by the tracker's own
// id first and then by the name the configuration uses — which is the only
// name flow has for a column it did not create (spec 04 §2).
func (j *JiraCloud) statusOf(ctx context.Context, to core.ColumnID) (jiraStatus, error) {
	wanted := strings.TrimSpace(string(to))
	if wanted == "" {
		return jiraStatus{}, core.Errorf(core.ErrValidation, "move a ticket").
			WithWhy("no column was named")
	}
	statuses, err := j.statuses(ctx)
	if err != nil {
		return jiraStatus{}, err
	}
	for _, status := range statuses {
		if status.ID == wanted {
			return status, nil
		}
	}
	for _, status := range statuses {
		if strings.EqualFold(status.Name, wanted) {
			return status, nil
		}
	}
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		names = append(names, status.Name)
	}
	slices.Sort(names)
	return jiraStatus{}, core.Errorf(core.ErrValidation, "move a ticket to %q", wanted).
		WithWhy("%s has no such status", j.Slug()).
		WithFix("name one of %s, or ask somebody who administers the workflow to add it", strings.Join(names, ", "))
}

// statuses is every status the project's workflows offer, deduplicated and in
// the order Jira gave them.
func (j *JiraCloud) statuses(ctx context.Context) ([]jiraStatus, error) {
	if j.key == "" {
		return nil, j.unnumbered("read the columns")
	}
	var byType []struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Statuses []jiraStatus `json:"statuses"`
	}
	path := "project/" + url.PathEscape(j.key) + "/statuses"
	if _, err := j.do(ctx, core.Request{Path: path, Out: &byType}); err != nil {
		return nil, core.Explain(err, "read the statuses of %s", j.Slug())
	}
	var statuses []jiraStatus
	seen := map[string]bool{}
	for _, issueType := range byType {
		for _, status := range issueType.Statuses {
			if status.ID == "" || seen[status.ID] {
				continue
			}
			seen[status.ID] = true
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

// unnumbered explains that nobody has chosen a project yet.
func (j *JiraCloud) unnumbered(what string) error {
	return core.Errorf(core.ErrNeedsInput, "%s", what).
		WithWhy("no Jira project is configured").
		WithFix("run flow doctor --fix to choose one, or set tracker.project to the project key")
}

// lookUpProject reads the configured project, at most once a run.
func (j *JiraCloud) lookUpProject(ctx context.Context) (jiraProject, error) {
	if j.key == "" {
		return jiraProject{}, nil
	}
	j.mu.Lock()
	cached := j.project
	j.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	var project jiraProject
	path := "project/" + url.PathEscape(j.key)
	if _, err := j.do(ctx, core.Request{Path: path, Out: &project}); err != nil {
		return jiraProject{}, core.Explain(err, "read the project %s", j.Slug())
	}
	j.mu.Lock()
	j.project = &project
	j.mu.Unlock()
	return project, nil
}

// CreateFollowUp files a ticket for work flow cannot do, or returns the one
// already filed (spec 07 §4).
func (j *JiraCloud) CreateFollowUp(ctx context.Context, f core.FollowUp) (core.Ticket, error) {
	if f.Marker.IsZero() {
		return core.Ticket{}, core.Errorf(core.ErrValidation, "file a follow-up").
			WithWhy("it carries no marker, so flow could never find it again").
			WithFix("give the follow-up a marker")
	}
	if j.key == "" {
		return core.Ticket{}, j.unnumbered(fmt.Sprintf("file the follow-up %q", f.Title))
	}
	if found, err := j.FindFollowUp(ctx, f.Marker); err != nil {
		return core.Ticket{}, err
	} else if found != nil {
		return *found, nil
	}

	project, err := j.lookUpProject(ctx)
	if err != nil {
		return core.Ticket{}, err
	}
	issueType, err := j.issueType(ctx, project)
	if err != nil {
		return core.Ticket{}, err
	}
	fields := map[string]any{
		"project":     map[string]string{"key": j.key},
		"summary":     f.Title,
		"description": adfFromText(core.MarkBody(f.Body, f.Marker, true)),
		"issuetype":   map[string]string{"id": issueType},
	}
	if len(f.Labels) > 0 {
		// Jira labels hold no spaces. A label that cannot be applied is
		// left off rather than failing the filing: the labels are how
		// people find flow's follow-ups, not what makes one true.
		labels := make([]string, 0, len(f.Labels))
		for _, label := range f.Labels {
			if jiraLabelUsable(label) {
				labels = append(labels, label)
			}
		}
		if len(labels) > 0 {
			fields["labels"] = labels
		}
	}
	if account, err := j.accountOf(ctx, f.Assignee); err != nil {
		return core.Ticket{}, err
	} else if account != "" {
		fields["assignee"] = map[string]string{"accountId": account}
	}

	j.writing.Lock()
	var filed jiraIssue
	_, err = j.do(ctx, core.Request{
		Method: http.MethodPost, Path: "issue",
		Body: map[string]any{"fields": fields}, Out: &filed,
	})
	j.writing.Unlock()
	if err != nil {
		return core.Ticket{}, core.Explain(err, "file the follow-up %q in %s", f.Title, j.Slug())
	}
	if filed.Key == "" {
		return core.Ticket{}, core.Errorf(core.ErrUnavailable, "file the follow-up %q", f.Title).
			WithWhy("%s accepted the issue but did not say which one it is", j.Slug())
	}
	// Jira answers a filing with the key and nothing else, and the caller
	// is owed a ticket: a follow-up is pinned, rewritten and closed by
	// what comes back from here.
	ticket, err := j.Ticket(ctx, filed.Key)
	if err != nil {
		return core.Ticket{}, err
	}
	j.remember(f.Marker, ticket)
	return ticket, nil
}

// issueType is the type a follow-up is filed as: the one called Task where
// there is one, and otherwise the first type that is not a sub-task — a
// sub-task cannot be filed without a parent, and flow's follow-ups have none.
func (j *JiraCloud) issueType(ctx context.Context, project jiraProject) (string, error) {
	var fallback string
	for _, issueType := range project.IssueTypes {
		if issueType.Subtask || issueType.ID == "" {
			continue
		}
		if strings.EqualFold(issueType.Name, "Task") {
			return issueType.ID, nil
		}
		if fallback == "" {
			fallback = issueType.ID
		}
	}
	if fallback == "" {
		return "", core.Errorf(core.ErrUnsupported, "file a follow-up in %s", j.Slug()).
			WithWhy("the project offers no issue type that is not a sub-task").
			WithFix("add a Task issue type to the project")
	}
	return fallback, nil
}

// accountOf turns whatever `follow_ups.owner` holds into an account id. A
// person Jira does not know is left off: the owner comes from configuration
// (spec 07 §4) and a typo there must not stop the follow-up being filed.
func (j *JiraCloud) accountOf(ctx context.Context, owner string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(owner), "@")
	if trimmed == "" {
		return "", nil
	}
	var found []jiraUser
	query := url.Values{"query": []string{trimmed}, "maxResults": []string{"5"}}
	if _, err := j.do(ctx, core.Request{Path: "user/search", Query: query, Out: &found}); err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermission) {
			return "", nil
		}
		return "", core.Explain(err, "find out who %s is", owner)
	}
	for _, user := range found {
		if strings.EqualFold(user.EmailAddress, trimmed) || strings.EqualFold(user.DisplayName, trimmed) || user.AccountID == trimmed {
			return user.AccountID, nil
		}
	}
	if len(found) == 1 {
		return found[0].AccountID, nil
	}
	return "", nil
}

// FindFollowUp returns the ticket carrying a marker, or nil when there is none
// (spec 11 §1).
//
// The project is read rather than searched. Jira's text search tokenises, and
// a marker is punctuation: `description ~ "flow:followup"` is a question
// about words that happen to be next to each other, which would find
// somebody's bug report and miss flow's own ticket. So the issues are walked
// newest first — flow's are the recent ones — and the marker is matched
// against the body, which is the only thing that decides (spec 11 §3).
//
// Read whole, that is every issue in the project on every run, and a large
// project spends its burst limit on it (issue #296). So when followups.labels
// names a label, only the issues carrying it are read: flow puts it on every
// follow-up it files, and labels is an exact match, not a text search. The
// marker still decides. A follow-up whose label somebody removed is not
// found, and a fresh, labelled one is filed in its place; reading the whole
// project again on every miss would cost exactly what the label saves.
func (j *JiraCloud) FindFollowUp(ctx context.Context, m core.Marker) (*core.Ticket, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find a follow-up").
			WithWhy("no marker was given, and a marker is the only thing that identifies one").
			WithFix("search for a marker such as %s", core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"))
	}
	if j.key == "" {
		return nil, j.unnumbered(fmt.Sprintf("find the follow-up marked %s", m))
	}
	if found, ok := j.recall(m); ok {
		return &found, nil
	}
	jql := "project = " + jiraQuote(j.key)
	if j.followUpLabel != "" {
		jql += " AND labels = " + jiraQuote(j.followUpLabel)
	}
	issues, err := j.search(ctx, jql+" ORDER BY created DESC",
		[]string{"summary", "description", "status", "labels", "updated", "created", "resolution", "resolutiondate", "parent", "assignee"})
	if err != nil {
		return nil, core.Explain(err, "find the follow-up marked %s", m)
	}
	for _, issue := range issues {
		ticket := issue.toTicket(j.site)
		if core.HasMarker(ticket.Body, m) {
			return &ticket, nil
		}
	}
	return nil, nil
}

// remember records a ticket flow filed, so that a search run before the
// project's index has caught up still finds it.
func (j *JiraCloud) remember(m core.Marker, ticket core.Ticket) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.filed = append(j.filed, jiraFiled{marker: m, ticket: ticket})
}

// recall returns a ticket flow filed in this process whose marker the search
// matches. The match is the marker's own, so a search naming fewer fields
// finds what a fuller marker filed (spec 11 §1).
func (j *JiraCloud) recall(m core.Marker) (core.Ticket, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, filed := range j.filed {
		if m.Matches(filed.marker) {
			return filed.ticket, true
		}
	}
	return core.Ticket{}, false
}

// UpdateFollowUp rewrites the title and body of a ticket flow opened, keeping
// its marker (spec 07 §4, spec 07 §10).
//
// The ticket is read first and the marker checked against what is there, not
// against what flow filed earlier: a person can edit a description, and the
// one thing worse than failing to publish a report is publishing it over
// somebody's bug report. A ticket that already says this is left alone, so
// that a daily report does not look like news every morning.
func (j *JiraCloud) UpdateFollowUp(ctx context.Context, key string, m core.Marker, title, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "rewrite %s", key).
			WithWhy("no marker was given, and flow rewrites only the tickets it opened").
			WithFix("pass the marker the ticket was filed with")
	}
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return err
	}
	if !core.HasMarker(ticket.Body, m) {
		return core.Errorf(core.ErrValidation, "rewrite %s", ticket.Key).
			WithWhy("its description does not carry the marker %s, so it is not flow's to rewrite", m).
			WithFix("file a new follow-up instead of rewriting somebody else's ticket")
	}
	marked := core.MarkBody(body, m, true)
	if title == ticket.Title && marked == ticket.Body {
		return nil
	}
	j.writing.Lock()
	defer j.writing.Unlock()
	_, err = j.do(ctx, core.Request{
		Method: http.MethodPut,
		Path:   "issue/" + url.PathEscape(ticket.Key),
		Body: map[string]any{"fields": map[string]any{
			"summary":     title,
			"description": adfFromText(marked),
		}},
	})
	if err != nil {
		return core.Explain(err, "rewrite %s", ticket.Key)
	}
	return nil
}

// Comment writes a marked comment on a ticket, editing flow's own rather than
// writing a second one (spec 07 §4).
func (j *JiraCloud) Comment(ctx context.Context, key string, m core.Marker, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "comment on %s", key).
			WithWhy("no marker was given, and a comment flow cannot find again would be written twice").
			WithFix("pass a marker")
	}
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return err
	}
	existing, err := j.findComment(ctx, ticket.Key, m)
	if err != nil {
		return err
	}
	marked := core.MarkBody(body, m, true)
	path := "issue/" + url.PathEscape(ticket.Key) + "/comment"
	request := core.Request{
		Method: http.MethodPost, Path: path,
		Body: map[string]any{"body": adfFromText(marked)},
	}
	if existing != nil {
		if existing.text() == marked {
			return nil
		}
		request.Method = http.MethodPut
		request.Path = path + "/" + url.PathEscape(existing.ID)
	}
	j.writing.Lock()
	defer j.writing.Unlock()
	if _, err := j.do(ctx, request); err != nil {
		return core.Explain(err, "comment on %s", ticket.Key)
	}
	return nil
}

// walkComments hands every comment on an issue to fn, stopping when fn returns
// false. The order is the caller's: flow's own comment is the newest thing on
// a ticket it has been nudging, and a person's explanation is read oldest
// first because that is the order a person reads a ticket in.
func (j *JiraCloud) walkComments(ctx context.Context, key, order string, fn func(jiraComment) bool) error {
	path := "issue/" + url.PathEscape(key) + "/comment"
	start := 0
	for {
		var page jiraCommentPage
		query := url.Values{
			"startAt":    []string{fmt.Sprint(start)},
			"maxResults": []string{fmt.Sprint(jiraPageSize)},
			"orderBy":    []string{order},
		}
		if _, err := j.do(ctx, core.Request{Path: path, Query: query, Out: &page}); err != nil {
			return core.Explain(err, "read the comments of %s", key)
		}
		for _, comment := range page.Comments {
			if !fn(comment) {
				return nil
			}
		}
		start += len(page.Comments)
		if len(page.Comments) == 0 || start >= page.Total {
			return nil
		}
	}
}

// findComment returns flow's own comment carrying a marker, or nil.
func (j *JiraCloud) findComment(ctx context.Context, key string, m core.Marker) (*jiraComment, error) {
	var found *jiraComment
	err := j.walkComments(ctx, key, "-created", func(comment jiraComment) bool {
		if !core.HasMarker(comment.text(), m) {
			return true
		}
		found = &comment
		return false
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Comments returns every comment on a ticket, oldest first.
//
// The issue is read first so that a key nobody has is [core.ErrNotFound]
// rather than an empty list: Jira answers the comment endpoint of an issue
// that is not there with a 404 whose message names the endpoint, and a board
// check that cannot tell "nothing was said" from "no such ticket" would read
// a mistyped key as a clean bill of health.
func (j *JiraCloud) Comments(ctx context.Context, key string) ([]core.TicketComment, error) {
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []core.TicketComment
	if err := j.walkComments(ctx, ticket.Key, "created", func(comment jiraComment) bool {
		out = append(out, core.TicketComment{
			Author:    comment.Author.login(),
			Body:      comment.text(),
			CreatedAt: comment.Created.Time,
		})
		return true
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Close closes a ticket, recording why (spec 07 §4).
//
// Jira has no closed flag: work is finished when it has a resolution, and the
// resolution is the reason. The transition is to whichever status the
// workflow offers in the done category, and the resolution is set only when
// that transition's screen takes one — setting a field a transition does not
// offer is a refusal, and flow would not know why.
func (j *JiraCloud) Close(ctx context.Context, key string, reason string) error {
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return err
	}
	if ticket.Closed {
		return nil
	}
	resolution, err := j.resolutionFor(ctx, reason)
	if err != nil {
		return err
	}
	j.writing.Lock()
	defer j.writing.Unlock()
	return j.transition(ctx, ticket,
		func(t jiraTransition) bool { return jiraCategory(t.To.StatusCategory.Key) == categoryDone },
		func(t jiraTransition) map[string]any {
			if resolution == "" || !t.takesResolution() {
				return nil
			}
			return map[string]any{"resolution": map[string]string{"name": resolution}}
		},
		fmt.Sprintf("close %s", ticket.Key),
		fmt.Sprintf("the workflow of %s offers no transition from %q to anything the board calls done", ticket.Key, ticket.Status.Name),
		"ask somebody who administers the workflow to allow it")
}

// Reopen reopens a closed ticket (spec 07 §4).
func (j *JiraCloud) Reopen(ctx context.Context, key string, reason string) error {
	ticket, err := j.Ticket(ctx, key)
	if err != nil {
		return err
	}
	if !ticket.Closed {
		return nil
	}
	j.writing.Lock()
	defer j.writing.Unlock()
	return j.transition(ctx, ticket,
		func(t jiraTransition) bool { return jiraCategory(t.To.StatusCategory.Key) == categoryTodo },
		func(t jiraTransition) map[string]any {
			if !t.takesResolution() {
				return nil
			}
			// Clearing the resolution is what makes the work open
			// again: a Jira issue with a resolution is finished
			// whatever column it is in.
			return map[string]any{"resolution": nil}
		},
		fmt.Sprintf("reopen %s", ticket.Key),
		fmt.Sprintf("the workflow of %s offers no transition from %q back to anything the board calls to do", ticket.Key, ticket.Status.Name),
		"ask somebody who administers the workflow to allow it")
}

// resolutionFor is the resolution flow will record for one of its two
// reasons: the first of its preferred names the instance holds. A resolution
// scheme is the team's and flow does not add to it, so an instance holding
// none of them closes the work with no resolution rather than failing.
func (j *JiraCloud) resolutionFor(ctx context.Context, reason string) (string, error) {
	available, err := j.allResolutions(ctx)
	if err != nil {
		return "", err
	}
	for _, want := range jiraResolutionNames(reason) {
		for _, have := range available {
			if strings.EqualFold(have.Name, want) {
				return have.Name, nil
			}
		}
	}
	return "", nil
}

// allResolutions is the instance's resolution scheme, read at most once.
func (j *JiraCloud) allResolutions(ctx context.Context) ([]jiraResolution, error) {
	j.mu.Lock()
	cached := j.resolutions
	j.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var resolutions []jiraResolution
	if _, err := j.do(ctx, core.Request{Path: "resolution", Out: &resolutions}); err != nil {
		if errors.Is(err, core.ErrPermission) || errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return nil, core.Explain(err, "read the resolutions of %s", j.Slug())
	}
	if resolutions == nil {
		resolutions = []jiraResolution{}
	}
	j.mu.Lock()
	j.resolutions = resolutions
	j.mu.Unlock()
	return resolutions, nil
}

// LinkedKeys returns the ticket keys a pull request is for.
//
// Jira is not the forge's tracker, so the forge's structured link names
// issues of its own and never Jira keys: what one of its pull requests says
// it closes is #11 of a repository, which is nothing here. Jira's own
// convention is the key in the branch name, which is what its integrations
// read and what its documentation tells people to do, so that is what flow
// reads — along with the title and the header a squash will land. It is never
// a pattern somebody configured (P12, spec 04 §3).
func (j *JiraCloud) LinkedKeys(pr core.PullRequest) []string {
	if j.key == "" {
		return nil
	}
	var keys []string
	for _, text := range []string{pr.HeadRef, pr.Title, pr.MergeHeader()} {
		for _, key := range jiraKeysIn(j.key, text) {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// do sends one request with Jira's media type.
func (j *JiraCloud) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return j.client.Do(ctx, j.decorate(r))
}

// decorate returns the request with Jira's media type on it, leaving an Accept
// the caller chose alone.
func (j *JiraCloud) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", jiraAccept)
	}
	r.Header = header
	return r
}

var _ core.Tracker = (*JiraCloud)(nil)
