package tracker

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// The payloads Jira Cloud's REST API answers with, and the conversions into
// flow's own vocabulary. Nothing here talks to the network: a conversion that
// can be read on its own is a conversion that can be tested on its own
// (spec 11 §3).

// jiraTime is a Jira timestamp.
//
// Jira writes `2026-10-14T19:09:44.000+1100`, which is ISO 8601 and is not
// RFC 3339: the zone offset has no colon in it, so time.Time's own decoding
// refuses it. The zero time is what a null or an absent field means, because
// an issue that was never resolved has no resolution date and that is not an
// error.
type jiraTime struct{ time.Time }

// jiraTimeLayouts are the shapes a Jira timestamp comes in, in the order they
// are tried: its own first, then the two RFC 3339 spellings, so that a Jira
// Server or a proxy that normalises the offset is still read.
var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.999-0700",
	time.RFC3339Nano,
	"2006-01-02T15:04:05-0700",
}

// UnmarshalJSON reads a Jira timestamp, or nothing at all.
func (t *jiraTime) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" || text == `""` {
		t.Time = time.Time{}
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, layout := range jiraTimeLayouts {
		if when, err := time.Parse(layout, raw); err == nil {
			t.Time = when
			return nil
		}
	}
	return fmt.Errorf("%q is not a Jira timestamp", raw)
}

// MarshalJSON writes a Jira timestamp the way Jira does.
func (t jiraTime) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.Format(jiraTimeLayouts[0]))
}

// jiraUser is an account, as every payload carries it.
type jiraUser struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress,omitempty"`
	Active       bool   `json:"active,omitempty"`
}

// login is how flow names an account: the display name where there is one,
// because an account id is not what anybody wrote in `follow_ups.owner`.
func (u *jiraUser) login() string {
	switch {
	case u == nil:
		return ""
	case u.DisplayName != "":
		return u.DisplayName
	default:
		return u.AccountID
	}
}

// jiraPermissions is what /mypermissions answers.
type jiraPermissions struct {
	Permissions map[string]struct {
		Key            string `json:"key"`
		HavePermission bool   `json:"havePermission"`
	} `json:"permissions"`
}

// tokens is the permissions held, as the capability declaration spells them.
func (p jiraPermissions) tokens() []string {
	var held []string
	for name, permission := range p.Permissions {
		if permission.HavePermission {
			held = append(held, jiraToken(name))
		}
	}
	slices.Sort(held)
	return slices.Compact(held)
}

// jiraProject is a project, from /project and /project/search alike.
type jiraProject struct {
	ID         string          `json:"id"`
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Self       string          `json:"self"`
	IssueTypes []jiraIssueType `json:"issueTypes,omitempty"`
}

// jiraIssueType is one of a project's issue types.
type jiraIssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

// jiraProjectPage is one page of /project/search.
type jiraProjectPage struct {
	Values   []jiraProject `json:"values"`
	StartAt  int           `json:"startAt"`
	MaxItems int           `json:"maxResults"`
	Total    int           `json:"total"`
	IsLast   bool          `json:"isLast"`
}

// jiraStatusCategory is the three-way grouping Jira puts every status in. The
// status API spells it in capitals and an issue spells it in lower case with
// different words again, so both readings fold onto flow's own vocabulary.
const (
	jiraCategoryTodo       = "TODO"
	jiraCategoryInProgress = "IN_PROGRESS"
	jiraCategoryDone       = "DONE"
)

// jiraStatus is a status as the status API describes it, where the category
// is the enum a status is created with.
type jiraStatus struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	StatusCategory string     `json:"statusCategory"`
	Scope          *jiraScope `json:"scope,omitempty"`
}

// jiraScope is where a status or a field belongs.
type jiraScope struct {
	Type    string       `json:"type"`
	Project *jiraProject `json:"project,omitempty"`
}

// toItem is one status as a desired-state item. The id is the status's, which
// is what the lock records and what a move names.
func (s jiraStatus) toItem() core.Item {
	return core.Item{
		ID:     s.ID,
		Name:   s.Name,
		Fields: map[string]string{jiraFieldCategory: jiraCategory(s.StatusCategory)},
	}
}

// jiraFieldCategory is the field a status option carries its category in.
const jiraFieldCategory = "category"

// jiraCategory folds either of Jira's two spellings of a status category onto
// flow's (spec 04 §2). A category flow has no name for is no category: an
// empty field is read as "the tracker did not say", which is not the same as
// a wrong answer.
func jiraCategory(raw string) string {
	switch strings.ToLower(strings.ReplaceAll(raw, " ", "_")) {
	case "todo", "to_do", "new", "undefined":
		return categoryTodo
	case "in_progress", "indeterminate":
		return categoryInProgress
	case "done", "complete", "completed":
		return categoryDone
	default:
		return ""
	}
}

// jiraCategoryOf is the enum the status API takes for one of flow's
// categories. A column flow was not told the category of is created
// in-progress: Jira's own automations and its reporting fire on the two ends
// of the board — a status in the "to do" category reopens work and one in
// "done" resolves it — and the middle is the only category that cannot make
// the board say something untrue about work nobody has touched.
func jiraCategoryOf(category string) string {
	switch category {
	case categoryTodo:
		return jiraCategoryTodo
	case categoryDone:
		return jiraCategoryDone
	default:
		return jiraCategoryInProgress
	}
}

// jiraIssueStatus is a status as an issue carries it, where the category is an
// object and its key is the vocabulary Jira has used since before the status
// API existed: new, indeterminate, done.
type jiraIssueStatus struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	StatusCategory struct {
		ID   int    `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"statusCategory"`
}

// jiraResolution is why an issue was finished.
type jiraResolution struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// jiraIssue is one issue.
type jiraIssue struct {
	ID     string          `json:"id"`
	Key    string          `json:"key"`
	Self   string          `json:"self"`
	Fields jiraIssueFields `json:"fields"`
}

// jiraIssueFields is the part of an issue flow reads. The names are Jira's
// own field ids, which is what both the issue payload and a search's `fields`
// list are written in.
type jiraIssueFields struct {
	Summary        string          `json:"summary"`
	Description    *adfDoc         `json:"description"`
	Status         jiraIssueStatus `json:"status"`
	Labels         []string        `json:"labels"`
	Assignee       *jiraUser       `json:"assignee"`
	Parent         *jiraIssue      `json:"parent"`
	Updated        jiraTime        `json:"updated"`
	Created        jiraTime        `json:"created"`
	Resolution     *jiraResolution `json:"resolution"`
	ResolutionDate jiraTime        `json:"resolutiondate"`
	TimeEstimate   *int64          `json:"timeoriginalestimate"`
}

// jiraIssueFieldNames is what flow asks a search for. Asking for everything
// would carry every custom field of every issue over the wire for nothing.
var jiraIssueFieldNames = []string{
	"summary", "description", "status", "labels", "assignee",
	"parent", "updated", "created", "resolution", "resolutiondate",
	"timeoriginalestimate",
}

// toTicket is one issue as flow's ticket.
//
// A column is a status, not a board column (spec 11 §3), so the status's own
// id is the column id and a move names it. Closed is the resolution: Jira has
// no closed flag, and a status in the done category with no resolution is an
// issue somebody dragged to the end of the board rather than one the team
// finished — both read as done here, because the board is what the team means.
func (i jiraIssue) toTicket(site string) core.Ticket {
	fields := i.Fields
	ticket := core.Ticket{
		Key:       i.Key,
		ID:        i.ID,
		URL:       jiraBrowseURL(site, i.Key),
		Title:     fields.Summary,
		Body:      adfTextOf(fields.descriptionDoc()),
		Labels:    slices.Clone(fields.Labels),
		UpdatedAt: fields.Updated.Time,
		ClosedAt:  fields.ResolutionDate.Time,
	}
	if fields.Status.ID != "" {
		ticket.Status = core.Status{ID: fields.Status.ID, Name: fields.Status.Name}
		ticket.ColumnID = core.ColumnID(fields.Status.ID)
	}
	ticket.Closed = fields.Resolution != nil || jiraCategory(fields.Status.StatusCategory.Key) == categoryDone
	if ticket.Closed {
		ticket.CloseReason = jiraCloseReason(fields.Resolution)
	}
	if login := fields.Assignee.login(); login != "" {
		ticket.Assignees = []string{login}
	}
	if fields.Parent != nil && fields.Parent.Key != "" {
		ticket.Parent = fields.Parent.Key
	}
	if fields.TimeEstimate != nil && *fields.TimeEstimate > 0 {
		ticket.Size = time.Duration(*fields.TimeEstimate) * time.Second
	}
	return ticket
}

// descriptionDoc is the description, or the empty document when the issue has
// none. A description is null far more often than it is absent, and a nil
// pointer and an empty body must read the same way.
func (f jiraIssueFields) descriptionDoc() adfDoc {
	if f.Description == nil {
		return adfDoc{}
	}
	return *f.Description
}

// jiraCloseReason is the close reason a resolution means. Flow reads only
// whether the work was abandoned (spec 07 §6), so every resolution that is not
// one of Jira's own names for abandonment is work that was done.
func jiraCloseReason(resolution *jiraResolution) string {
	if resolution == nil {
		return core.ClosedCompleted
	}
	switch strings.ToLower(strings.TrimSpace(resolution.Name)) {
	case "won't do", "wont do", "won't fix", "wont fix", "duplicate",
		"cannot reproduce", "declined", "abandoned", "not a bug":
		return core.ClosedNotPlanned
	default:
		return core.ClosedCompleted
	}
}

// jiraResolutionNames are the resolutions flow asks for, in the order it will
// take them, for each of the two reasons it has. The first the instance holds
// is used: a resolution scheme is the team's and flow does not add to it.
func jiraResolutionNames(reason string) []string {
	if reason == core.ClosedNotPlanned {
		return []string{"Won't Do", "Won't Fix", "Declined", "Duplicate", "Cannot Reproduce"}
	}
	return []string{"Done", "Fixed", "Complete", "Completed", "Resolved"}
}

// jiraTransition is one move an issue may make from where it is.
type jiraTransition struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	To     jiraIssueStatus `json:"to"`
	Fields map[string]struct {
		Required bool   `json:"required"`
		Name     string `json:"name"`
	} `json:"fields,omitempty"`
}

// takesResolution reports whether this transition's screen accepts one.
// Setting a field a transition does not offer is a 400 from Jira, so flow
// asks first rather than failing and guessing why.
func (t jiraTransition) takesResolution() bool {
	_, ok := t.Fields["resolution"]
	return ok
}

// jiraTransitions is what the transitions endpoint answers.
type jiraTransitions struct {
	Transitions []jiraTransition `json:"transitions"`
}

// jiraSearchPage is one page of /search/jql. Jira's search moved to an opaque
// page token: there is no total and no last page, and an answer with no token
// is the end.
type jiraSearchPage struct {
	Issues        []jiraIssue `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        bool        `json:"isLast"`
}

// jiraComment is one comment.
type jiraComment struct {
	ID      string    `json:"id"`
	Body    *adfDoc   `json:"body"`
	Author  *jiraUser `json:"author,omitempty"`
	Created jiraTime  `json:"created,omitempty"`
	Updated jiraTime  `json:"updated,omitempty"`
}

// text is the comment's body as text.
func (c jiraComment) text() string {
	if c.Body == nil {
		return ""
	}
	return adfTextOf(*c.Body)
}

// jiraCommentPage is one page of an issue's comments.
type jiraCommentPage struct {
	Comments   []jiraComment `json:"comments"`
	StartAt    int           `json:"startAt"`
	MaxItems   int           `json:"maxResults"`
	Total      int           `json:"total"`
	IsLast     *bool         `json:"isLast,omitempty"`
	NextPageTk string        `json:"nextPageToken,omitempty"`
}

// jiraBrowseURL is where a person reads an issue.
func jiraBrowseURL(site, key string) string {
	if site == "" || key == "" {
		return ""
	}
	return strings.TrimSuffix(site, "/") + "/browse/" + key
}

// jiraProjectURL is where a person reads a project.
func jiraProjectURL(site, key string) string {
	if site == "" || key == "" {
		return ""
	}
	return strings.TrimSuffix(site, "/") + "/browse/" + key
}

// jiraQuote is one value in a JQL expression. JQL quotes with double quotes
// and escapes with a backslash, which is what strconv.Quote writes — except
// that it also escapes anything non-ASCII, and a summary or a label may hold
// it. So the quoting is written out, over the two characters JQL cares about.
func jiraQuote(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// jiraJQLTime is a moment as JQL writes it. JQL takes minutes and no zone, in
// the instance's own time, so the moment is sent as UTC and the query asks for
// a minute earlier than the window to make up for the rounding: a ticket on
// the boundary belongs in the window.
func jiraJQLTime(when time.Time) string {
	return when.UTC().Add(-time.Minute).Format("2006-01-02 15:04")
}

// jiraKeyPattern reports whether text is a Jira issue key of the given
// project: the project key, a hyphen, and a number. The match is exact and
// case-insensitive, because Jira itself upper-cases keys.
func jiraKeyPattern(project, text string) bool {
	rest, ok := strings.CutPrefix(strings.ToUpper(text), strings.ToUpper(project)+"-")
	if !ok || rest == "" {
		return false
	}
	number, err := strconv.Atoi(rest)
	return err == nil && number > 0
}

// jiraKeysIn finds every issue key of one project in a piece of text: a branch
// name, a pull request title, or the header a squash will land.
//
// This is Jira's own convention and nobody else's (spec 04 §3): flow never
// applies a pattern somebody configured. The text is split on everything that
// cannot be part of a key, so `feature/FLOW-12-the-filter-bar` yields FLOW-12
// and `FLOW-12a` yields nothing.
func jiraKeysIn(project, text string) []string {
	var found []string
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) {
		for _, candidate := range jiraKeyCandidates(word) {
			if jiraKeyPattern(project, candidate) {
				key := strings.ToUpper(candidate)
				if !slices.Contains(found, key) {
					found = append(found, key)
				}
			}
		}
	}
	return found
}

// jiraKeyCandidates is every prefix of a hyphenated word that could be a key.
// A branch is written FLOW-12-the-filter-bar, so the word carries the key and
// the title of the work after it.
func jiraKeyCandidates(word string) []string {
	parts := strings.Split(word, "-")
	if len(parts) < 2 {
		return nil
	}
	return []string{parts[0] + "-" + parts[1]}
}

// jiraToken is the permission token flow records for one of Jira's permission
// keys. The vocabulary is the capability declaration's (spec 05 §1): a probe
// and a declaration that do not share one can never agree.
func jiraToken(permission string) string {
	switch strings.ToUpper(permission) {
	case "ADMINISTER_PROJECTS":
		return jiraTokenProjectAdmin
	case "CREATE_ISSUES":
		return jiraTokenCreateIssues
	case "TRANSITION_ISSUES":
		return jiraTokenTransitionIssues
	case "EDIT_ISSUES":
		return jiraTokenEditIssues
	case "ADD_COMMENTS":
		return jiraTokenAddComments
	case "BROWSE_PROJECTS":
		return jiraTokenBrowse
	default:
		return strings.ToLower(permission)
	}
}
