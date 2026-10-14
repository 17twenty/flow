package tracker

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// The payloads Linear's GraphQL API answers with, the documents flow sends it,
// and the conversions into flow's own vocabulary. Nothing here talks to the
// network: a conversion that can be read on its own is a conversion that can
// be tested on its own (spec 11 §3).

// LinearHeader is what every call to Linear carries. GraphQL has one media
// type and no dated versions, so this is an Accept and nothing else.
func LinearHeader() http.Header {
	header := http.Header{}
	header.Set("Accept", linearAccept)
	return header
}

// LinearAuthorization is the Authorization header value for a Linear
// credential, which is the credential itself.
//
// A personal API key is sent with no scheme in front of it and an OAuth
// access token is sent as a Bearer token (spec 11 §4). Linear names the two
// kinds in the credential, so flow does not need a second setting to be told
// which one it was given — and a credential whose shape says nothing is sent
// the way the key people paste from the settings page is sent.
func LinearAuthorization(token string) string {
	if strings.HasPrefix(token, linearOAuthPrefix) {
		return "Bearer " + token
	}
	return token
}

// lnPageInfo is a Relay cursor page's end.
type lnPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// lnUser is an account, as every payload carries it.
type lnUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
}

// login is how flow names an account: the display name, which is what Linear
// shows and what somebody writing `follow_ups.owner` would have copied.
func (u *lnUser) login() string {
	switch {
	case u == nil:
		return ""
	case u.DisplayName != "":
		return u.DisplayName
	case u.Name != "":
		return u.Name
	default:
		return u.ID
	}
}

// lnOrganization is the workspace. Its urlKey is the first segment of every
// address people read issues at, and is the only way to build one.
type lnOrganization struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URLKey string `json:"urlKey"`
}

// lnViewer is what a probe reads: who the credential is, and the workspace it
// belongs to.
type lnViewer struct {
	Viewer       lnUser         `json:"viewer"`
	Organization lnOrganization `json:"organization"`
}

// lnMembership is the viewer's membership of one team.
type lnMembership struct {
	Owner bool `json:"owner"`
}

// lnTeam is a team, which is what flow's `tracker.project` names: a Linear
// team owns the workflow states that are the board's columns, and the issues
// that are its tickets.
type lnTeam struct {
	ID         string        `json:"id"`
	Key        string        `json:"key"`
	Name       string        `json:"name"`
	Membership *lnMembership `json:"membership,omitempty"`
}

// admin reports whether the viewer administers this team.
func (t lnTeam) admin() bool { return t.Membership != nil && t.Membership.Owner }

// url is where people read the team's board.
func (t lnTeam) url(urlKey string) string {
	if urlKey == "" || t.Key == "" {
		return ""
	}
	return linearSite + "/" + urlKey + "/team/" + t.Key + "/all"
}

// toCandidate offers a team as an answer to the `tracker.project`
// placeholder (spec 07 §3).
func (t lnTeam) toCandidate(urlKey string) core.Candidate {
	return core.Candidate{
		What:   core.ItemTrackerProject,
		ID:     t.Key,
		Name:   t.Name,
		URL:    t.url(urlKey),
		Detail: fmt.Sprintf("the %s team", t.Key),
	}
}

// lnTeamPage is a page of teams.
type lnTeamPage struct {
	Nodes    []lnTeam   `json:"nodes"`
	PageInfo lnPageInfo `json:"pageInfo"`
}

// lnTeamsResult is what the teams query answers.
type lnTeamsResult struct {
	Teams        lnTeamPage     `json:"teams"`
	Organization lnOrganization `json:"organization"`
}

// lnState is one workflow state: a column of the board.
type lnState struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Position    float64 `json:"position"`
	Description string  `json:"description,omitempty"`
	Color       string  `json:"color,omitempty"`
}

// toItem is the state as a desired-state item, carrying the category flow
// reads it as.
func (s lnState) toItem() core.Item {
	return core.Item{
		ID:     s.ID,
		Name:   s.Name,
		Fields: map[string]string{lnFieldCategory: lnCategory(s.Type)},
	}
}

// lnStatePage is a page of workflow states.
type lnStatePage struct {
	Nodes    []lnState  `json:"nodes"`
	PageInfo lnPageInfo `json:"pageInfo"`
}

// lnStatesResult is what the states query answers. A team the credential
// cannot see is null rather than an error.
type lnStatesResult struct {
	Team *struct {
		States lnStatePage `json:"states"`
	} `json:"team"`
}

// lnLabel is one label. A label belongs to a team or to the workspace, and
// the ones with no team are available to every team in it.
type lnLabel struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description string  `json:"description,omitempty"`
	IsGroup     bool    `json:"isGroup,omitempty"`
	RetiredAt   *string `json:"retiredAt,omitempty"`
	Team        *lnTeam `json:"team,omitempty"`
}

// usable reports whether a label can be put on an issue of this team. A group
// is a heading rather than a label, a retired one is no longer offered, and a
// label of another team belongs to that team.
func (l lnLabel) usable(teamID string) bool {
	switch {
	case l.IsGroup, l.RetiredAt != nil:
		return false
	case l.Team == nil:
		return true
	default:
		return l.Team.ID == teamID
	}
}

// toItem is the label as a desired-state item. The colour is recorded the way
// every other adapter records one — six hex digits, no hash — because the
// configuration is written once for whichever tracker is underneath it.
func (l lnLabel) toItem() core.Item {
	item := core.Item{ID: l.ID, Name: l.Name, Fields: map[string]string{}}
	if l.Color != "" {
		item.Fields[lnFieldColour] = lnColour(l.Color)
	}
	if l.Description != "" {
		item.Fields[lnFieldDescription] = l.Description
	}
	if len(item.Fields) == 0 {
		item.Fields = nil
	}
	return item
}

// lnLabelPage is a page of labels.
type lnLabelPage struct {
	Nodes    []lnLabel  `json:"nodes"`
	PageInfo lnPageInfo `json:"pageInfo"`
}

// lnLabelsResult is what the labels query answers.
type lnLabelsResult struct {
	IssueLabels lnLabelPage `json:"issueLabels"`
}

// lnAttachment is a link on an issue. Linear's own GitHub integration records
// a pull request as one, which is how flow learns that a pull request nobody
// named in a branch is still for this ticket.
type lnAttachment struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// lnComment is one comment.
type lnComment struct {
	ID        string  `json:"id"`
	Body      string  `json:"body"`
	URL       string  `json:"url,omitempty"`
	User      *lnUser `json:"user,omitempty"`
	CreatedAt lnTime  `json:"createdAt"`
	UpdatedAt lnTime  `json:"updatedAt"`
}

// lnTime is a Linear timestamp, which is RFC 3339 or absent. The zero time is
// what a null means: an issue that was never completed has no completion date
// and that is not an error.
type lnTime struct{ time.Time }

// UnmarshalJSON reads a timestamp, or nothing at all.
func (t *lnTime) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" || text == `""` {
		t.Time = time.Time{}
		return nil
	}
	return t.Time.UnmarshalJSON(data)
}

// lnIssue is one issue.
type lnIssue struct {
	ID          string   `json:"id"`
	Identifier  string   `json:"identifier"`
	Number      float64  `json:"number,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url,omitempty"`
	BranchName  string   `json:"branchName,omitempty"`
	Trashed     *bool    `json:"trashed,omitempty"`
	Estimate    *int     `json:"estimate,omitempty"`
	State       *lnState `json:"state,omitempty"`
	Team        *lnTeam  `json:"team,omitempty"`
	Parent      *lnIssue `json:"parent,omitempty"`
	Assignee    *lnUser  `json:"assignee,omitempty"`
	Labels      *struct {
		Nodes []lnLabel `json:"nodes"`
	} `json:"labels,omitempty"`
	Attachments *struct {
		Nodes []lnAttachment `json:"nodes"`
	} `json:"attachments,omitempty"`
	ArchivedAt  lnTime `json:"archivedAt"`
	CompletedAt lnTime `json:"completedAt"`
	CanceledAt  lnTime `json:"canceledAt"`
	CreatedAt   lnTime `json:"createdAt"`
	UpdatedAt   lnTime `json:"updatedAt"`
}

// labelNames is the names of the labels the issue carries.
func (i lnIssue) labelNames() []string {
	if i.Labels == nil {
		return nil
	}
	names := make([]string, 0, len(i.Labels.Nodes))
	for _, label := range i.Labels.Nodes {
		names = append(names, label.Name)
	}
	return names
}

// attachmentURLs is every link recorded on the issue.
func (i lnIssue) attachmentURLs() []string {
	if i.Attachments == nil {
		return nil
	}
	urls := make([]string, 0, len(i.Attachments.Nodes))
	for _, attachment := range i.Attachments.Nodes {
		if attachment.URL != "" {
			urls = append(urls, attachment.URL)
		}
	}
	return urls
}

// toTicket is the issue as flow's own vocabulary.
//
// A Linear issue has no open flag: it is finished when it stands in a state
// Linear calls completed or canceled, which is also what fills completedAt
// and canceledAt. A cancelled issue is work that was dropped, which is what
// [core.ClosedNotPlanned] means everywhere else.
func (i lnIssue) toTicket() core.Ticket {
	ticket := core.Ticket{
		Key:       i.Identifier,
		ID:        i.ID,
		URL:       i.URL,
		Title:     i.Title,
		Body:      i.Description,
		Labels:    i.labelNames(),
		Archived:  !i.ArchivedAt.Time.IsZero(),
		UpdatedAt: i.UpdatedAt.Time,
	}
	// The column is the closing and the date is when: the state's type says
	// whether the work is over and why, and Linear dates the two endings
	// itself. Two readings of the same fact would drift apart, so the
	// reason has one source and the date has the other.
	if i.State != nil && i.State.ID != "" {
		ticket.Status = core.Status{ID: i.State.ID, Name: i.State.Name}
		ticket.ColumnID = core.ColumnID(i.State.ID)
		if lnFinished(i.State.Type) {
			ticket.Closed = true
			ticket.CloseReason = lnCloseReason(i.State.Type)
		}
	}
	switch {
	case !i.CanceledAt.Time.IsZero():
		ticket.ClosedAt = i.CanceledAt.Time
	case !i.CompletedAt.Time.IsZero():
		ticket.ClosedAt = i.CompletedAt.Time
	}
	if login := i.Assignee.login(); login != "" {
		ticket.Assignees = []string{login}
	}
	if i.Parent != nil && i.Parent.Identifier != "" {
		ticket.Parent = i.Parent.Identifier
	}
	return ticket
}

// lnIssuePage is a page of issues.
type lnIssuePage struct {
	Nodes    []lnIssue  `json:"nodes"`
	PageInfo lnPageInfo `json:"pageInfo"`
}

// lnIssuesResult is what a listing answers.
type lnIssuesResult struct {
	Issues lnIssuePage `json:"issues"`
}

// lnIssueResult is what a lookup by key answers. An issue the credential
// cannot see is null rather than an error.
type lnIssueResult struct {
	Issue *lnIssue `json:"issue"`
}

// lnCommentsResult is what an issue's comments answer.
type lnCommentsResult struct {
	Issue *struct {
		ID       string `json:"id"`
		Comments struct {
			Nodes    []lnComment `json:"nodes"`
			PageInfo lnPageInfo  `json:"pageInfo"`
		} `json:"comments"`
	} `json:"issue"`
}

// The mutation answers. Linear says whether each one worked and hands back
// the thing it made, which is what the lock records.

type lnIssuePayload struct {
	Result struct {
		Success bool     `json:"success"`
		Issue   *lnIssue `json:"issue"`
	} `json:"issueCreate"`
}

type lnIssueUpdatePayload struct {
	Result struct {
		Success bool     `json:"success"`
		Issue   *lnIssue `json:"issue"`
	} `json:"issueUpdate"`
}

type lnLabelCreatePayload struct {
	Result struct {
		Success bool     `json:"success"`
		Label   *lnLabel `json:"issueLabel"`
	} `json:"issueLabelCreate"`
}

type lnLabelUpdatePayload struct {
	Result struct {
		Success bool     `json:"success"`
		Label   *lnLabel `json:"issueLabel"`
	} `json:"issueLabelUpdate"`
}

type lnLabelDeletePayload struct {
	Result struct {
		Success bool `json:"success"`
	} `json:"issueLabelDelete"`
}

type lnStateCreatePayload struct {
	Result struct {
		Success bool     `json:"success"`
		State   *lnState `json:"workflowState"`
	} `json:"workflowStateCreate"`
}

type lnStateUpdatePayload struct {
	Result struct {
		Success bool     `json:"success"`
		State   *lnState `json:"workflowState"`
	} `json:"workflowStateUpdate"`
}

type lnStateArchivePayload struct {
	Result struct {
		Success bool     `json:"success"`
		Entity  *lnState `json:"entity"`
	} `json:"workflowStateArchive"`
}

type lnCommentCreatePayload struct {
	Result struct {
		Success bool       `json:"success"`
		Comment *lnComment `json:"comment"`
	} `json:"commentCreate"`
}

type lnCommentUpdatePayload struct {
	Result struct {
		Success bool       `json:"success"`
		Comment *lnComment `json:"comment"`
	} `json:"commentUpdate"`
}

// lnCategory is the column category one of Linear's workflow state types
// falls in (spec 04 §2).
//
// Linear has six and flow has three. Triage and backlog and unstarted are all
// work nobody has begun; started is work in hand; completed and canceled are
// both work that is over, because a column flow calls done is a column work
// leaves the board from, and a cancelled issue has left it.
func lnCategory(stateType string) string {
	switch strings.ToLower(stateType) {
	case lnStateTriage, lnStateBacklog, lnStateUnstarted:
		return categoryTodo
	case lnStateStarted:
		return categoryInProgress
	case lnStateCompleted, lnStateCanceled:
		return categoryDone
	default:
		return ""
	}
}

// lnStateTypeOf is the Linear state type flow creates a column of one of its
// categories as.
//
// A column flow was not told the category of is created started, for the
// reason Jira's is created in-progress: Linear's own automations fire at the
// two ends of the board — an issue in a backlog state is work not begun and
// one in a completed state is work finished — and the middle is the only
// category that cannot make the board say something untrue about work nobody
// has touched.
func lnStateTypeOf(category string) string {
	switch category {
	case categoryTodo:
		return lnStateUnstarted
	case categoryDone:
		return lnStateCompleted
	default:
		return lnStateStarted
	}
}

// lnFinished reports whether work in a state of that type is over.
func lnFinished(stateType string) bool {
	switch strings.ToLower(stateType) {
	case lnStateCompleted, lnStateCanceled:
		return true
	default:
		return false
	}
}

// lnCloseReason is why work in a state of that type is over.
func lnCloseReason(stateType string) string {
	if strings.EqualFold(stateType, lnStateCanceled) {
		return core.ClosedNotPlanned
	}
	return core.ClosedCompleted
}

// lnColour is a colour as flow records one: six hex digits and no hash.
// Linear writes `#rrggbb` and the configuration is written for whichever
// tracker is underneath it, so the hash is the adapter's business.
func lnColour(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
}

// lnColourOf is the colour a change asks for, as Linear takes one, or empty
// where the change asks for no colour at all.
func lnColourOf(item core.Item) string {
	colour := lnColour(item.Field(lnFieldColour))
	if colour == "" {
		return ""
	}
	return "#" + colour
}

// lnKeysIn finds every issue identifier of one team in a piece of text: a
// branch name, a pull request title, or the header a squash will land.
//
// This is Linear's own convention and nobody else's (spec 04 §3): flow never
// applies a pattern somebody configured. Linear writes a branch name as
// `name/eng-12-the-filter-bar`, in lower case, so the comparison is folded
// and the identifier is returned the way Linear displays it.
func lnKeysIn(team, text string) []string {
	if team == "" {
		return nil
	}
	var found []string
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) {
		parts := strings.Split(word, "-")
		if len(parts) < 2 {
			continue
		}
		candidate := parts[0] + "-" + parts[1]
		if !lnKeyPattern(team, candidate) {
			continue
		}
		if key := strings.ToUpper(candidate); !slices.Contains(found, key) {
			found = append(found, key)
		}
	}
	return found
}

// lnKeyPattern reports whether a word is an identifier of that team.
func lnKeyPattern(team, text string) bool {
	rest, ok := strings.CutPrefix(strings.ToUpper(text), strings.ToUpper(team)+"-")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return rest[0] != '0'
}

// lnSentinel maps Linear's own name for a failure onto one of flow's.
//
// Linear puts the name in the error's extensions rather than beside the
// message, and [core.GraphQLError.Kind] is what hands this function whichever
// of the two a provider used. The codes are the precise ones and the types
// are the coarse ones underneath them, so both are answered: an adapter that
// knew only the codes would read a refusal it has no code for as an outage
// and retry it.
func lnSentinel(errType string) error {
	switch strings.ToUpper(strings.ReplaceAll(errType, " ", "_")) {
	case "ENTITY_NOT_FOUND", "ENTITY_ARCHIVED":
		return core.ErrNotFound
	case "AUTHENTICATION_ERROR", "AUTHENTICATION_FAILED":
		return core.ErrPermission
	case "FEATURE_NOT_ACCESSIBLE", "FORBIDDEN", "USAGE_LIMIT_EXCEEDED":
		return core.ErrPermission
	case "INVALID_INPUT", "USER_ERROR", "GRAPHQL_ERROR", "BAD_USER_INPUT":
		return core.ErrValidation
	case "RATELIMITED", "RATE_LIMITED":
		return core.ErrRateLimited
	default:
		return nil
	}
}

// The documents flow sends. Every one is named, so that what a query is for
// can be read in a log, in a test and in Linear's own audit trail.

const (
	// lnViewerQuery is the probe: who the credential is, and where.
	lnViewerQuery = `query FlowViewer {
  viewer { id name displayName email admin }
  organization { id name urlKey }
}`

	// lnTeamsQuery lists the teams a placeholder could be answered with.
	lnTeamsQuery = `query FlowTeams($after: String) {
  teams(first: 50, after: $after) {
    nodes { id key name }
    pageInfo { hasNextPage endCursor }
  }
  organization { id name urlKey }
}`

	// lnTeamQuery finds one team by the key people write.
	lnTeamQuery = `query FlowTeam($key: String!) {
  teams(filter: { key: { eq: $key } }, first: 2) {
    nodes { id key name membership { owner } }
  }
}`

	// lnStatesQuery reads a team's workflow states: the board's columns.
	lnStatesQuery = `query FlowStates($team: String!, $after: String) {
  team(id: $team) {
    states(first: 100, after: $after) {
      nodes { id name type position description color }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

	// lnLabelsQuery reads every label the workspace offers, which is more
	// than the team's own: a label with no team belongs to the workspace and
	// every team may use it.
	lnLabelsQuery = `query FlowLabels($after: String) {
  issueLabels(first: 100, after: $after) {
    nodes { id name color description isGroup retiredAt team { id key } }
    pageInfo { hasNextPage endCursor }
  }
}`

	// lnIssuesQuery lists the issues of a board. Archived issues are left
	// out, because the board does not show them (spec 11 §1).
	lnIssuesQuery = `query FlowIssues($filter: IssueFilter, $after: String, $first: Int) {
  issues(filter: $filter, first: $first, after: $after, orderBy: updatedAt) {
    nodes { ` + lnIssueSelection + ` }
    pageInfo { hasNextPage endCursor }
  }
}`

	// lnArchivedIssuesQuery lists issues including the archived ones, which
	// is how flow finds a follow-up Linear archived after it was closed.
	lnArchivedIssuesQuery = `query FlowArchivedIssues($filter: IssueFilter, $after: String, $first: Int) {
  issues(filter: $filter, first: $first, after: $after, orderBy: updatedAt, includeArchived: true) {
    nodes { ` + lnIssueSelection + ` }
    pageInfo { hasNextPage endCursor }
  }
}`

	// lnIssueQuery reads one issue by the identifier people write. A lookup
	// answers for an archived issue; a listing does not.
	lnIssueQuery = `query FlowIssue($key: String!) {
  issue(id: $key) { ` + lnIssueSelection + ` }
}`

	// lnCommentsQuery reads an issue's comments.
	lnCommentsQuery = `query FlowComments($key: String!, $after: String) {
  issue(id: $key) {
    id
    comments(first: 50, after: $after) {
      nodes { id body url createdAt updatedAt user { id name displayName } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

	// lnIssueCreateMutation files an issue.
	lnIssueCreateMutation = `mutation FlowIssueCreate($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { ` + lnIssueSelection + ` } }
}`

	// lnIssueUpdateMutation edits one: its state, its title or its body.
	lnIssueUpdateMutation = `mutation FlowIssueUpdate($key: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $key, input: $input) { success issue { ` + lnIssueSelection + ` } }
}`

	// lnLabelCreateMutation adds a label to the team.
	lnLabelCreateMutation = `mutation FlowLabelCreate($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel { id name color description isGroup team { id key } }
  }
}`

	// lnLabelUpdateMutation edits a label.
	lnLabelUpdateMutation = `mutation FlowLabelUpdate($id: String!, $input: IssueLabelUpdateInput!) {
  issueLabelUpdate(id: $id, input: $input) {
    success
    issueLabel { id name color description isGroup team { id key } }
  }
}`

	// lnLabelDeleteMutation removes one.
	lnLabelDeleteMutation = `mutation FlowLabelDelete($id: String!) {
  issueLabelDelete(id: $id) { success }
}`

	// lnStateCreateMutation adds a column to the board.
	lnStateCreateMutation = `mutation FlowStateCreate($input: WorkflowStateCreateInput!) {
  workflowStateCreate(input: $input) {
    success
    workflowState { id name type position description color }
  }
}`

	// lnStateUpdateMutation edits one. Linear takes no type here, so a
	// column's category is settled when it is created.
	lnStateUpdateMutation = `mutation FlowStateUpdate($id: String!, $input: WorkflowStateUpdateInput!) {
  workflowStateUpdate(id: $id, input: $input) {
    success
    workflowState { id name type position description color }
  }
}`

	// lnStateArchiveMutation removes a column. Linear archives a state
	// rather than deleting it, and refuses while issues still stand in it.
	lnStateArchiveMutation = `mutation FlowStateArchive($id: String!) {
  workflowStateArchive(id: $id) {
    success
    entity { id name type position }
  }
}`

	// lnCommentCreateMutation writes a comment.
	lnCommentCreateMutation = `mutation FlowCommentCreate($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id body url createdAt updatedAt }
  }
}`

	// lnCommentUpdateMutation rewrites one flow wrote.
	lnCommentUpdateMutation = `mutation FlowCommentUpdate($id: String!, $input: CommentUpdateInput!) {
  commentUpdate(id: $id, input: $input) {
    success
    comment { id body url createdAt updatedAt }
  }
}`
)

// lnIssueSelection is every field flow reads of an issue, in one place: two
// queries that read an issue differently are two tickets that can disagree.
const lnIssueSelection = `id identifier number title description url branchName trashed
    archivedAt completedAt canceledAt createdAt updatedAt
    state { id name type position }
    team { id key name }
    parent { id identifier }
    assignee { id name displayName }
    labels(first: 50) { nodes { id name } }
    attachments(first: 20) { nodes { id url } }`
