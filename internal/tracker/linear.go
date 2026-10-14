package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/17twenty/flow/internal/core"
)

const (
	// LinearKind is the adapter's name, matching the declaration and the
	// tracker.type in configuration.
	LinearKind = "linear"
	// LinearAPI is where Linear answers. There is one workspace host for
	// everybody, so tracker.url names nothing for this adapter.
	LinearAPI = "https://api.linear.app"
	// LinearPath is the one endpoint a GraphQL API has.
	LinearPath = "graphql"
	// linearSite is where people read their issues.
	linearSite = "https://linear.app"
	// linearAccept is the media type every call asks for.
	linearAccept = "application/json"
	// linearOAuthPrefix is how Linear spells an OAuth access token, which
	// is sent as a Bearer token where a personal API key is not.
	linearOAuthPrefix = "lin_oauth_"
	// lnTimeLayout is how Linear writes a timestamp, which is RFC 3339
	// with milliseconds, and how a filter must be given one.
	lnTimeLayout = "2006-01-02T15:04:05.000Z"
	// lnPageSize is how many issues one page carries. Linear's default is
	// 50 and its complexity budget is spent on the fields each node
	// selects, so a larger page is not a cheaper read.
	lnPageSize = 50
)

// Linear's workflow state types, which are the vocabulary a column's category
// is read from and written in.
const (
	lnStateTriage    = "triage"
	lnStateBacklog   = "backlog"
	lnStateUnstarted = "unstarted"
	lnStateStarted   = "started"
	lnStateCompleted = "completed"
	lnStateCanceled  = "canceled"
)

// The fields an item of desired state carries.
const (
	lnFieldCategory    = "category"
	lnFieldColour      = "color"
	lnFieldDescription = "description"
)

// lnTokenTeamAdmin is Linear's permission vocabulary, as
// assets/capabilities/linear.yaml is written in it. A probe and a declaration
// that do not share a vocabulary can never agree (spec 05 §1).
const lnTokenTeamAdmin = "team_admin"

// LinearOptions builds the adapter.
type LinearOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Team is the team key, such as ENG. It is what tracker.project
	// resolves to, and it may be empty before setup has found one.
	Team string
	// Capabilities is the declaration loaded from
	// assets/capabilities/linear.yaml.
	Capabilities core.CapabilitySet
}

// Linear is the Linear tracker adapter.
//
// One team is the board: a Linear team owns the workflow states that are
// flow's columns and the issues that are its tickets, which is why
// `tracker.project` holds a team key (spec 11 §4).
type Linear struct {
	client *core.Client
	key    string
	caps   core.CapabilitySet

	// writing serialises the calls that change an issue. Linear applies
	// every edit to an issue through the same mutation, and two at once
	// buy nothing.
	writing sync.Mutex

	// mu guards everything read once a run.
	mu sync.Mutex
	// team is the configured team, resolved at most once.
	team *lnTeam
	// org is the workspace, read at most once: its urlKey is the only way
	// to build an address a person can open.
	org *lnOrganization
	// filed are the follow-ups filed in this process, so that a search run
	// before the workspace's index has caught up still finds one.
	filed []lnFiled
	// links maps a pull request's address to the issues Linear has
	// recorded it against, filled from every issue flow reads.
	links map[string][]string
	// cachedStates are the team's workflow states, read at most once.
	cachedStates []lnState
	// cachedLabels are the labels this team may use, read at most once.
	cachedLabels []lnLabel
}

// lnFiled is a follow-up this process filed.
type lnFiled struct {
	marker core.Marker
	ticket core.Ticket
}

// NewLinear returns an adapter for one Linear team.
func NewLinear(o LinearOptions) (*Linear, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the linear adapter").
			WithWhy("no HTTP client was given")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the linear adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/linear.yaml and pass the declaration")
	}
	return &Linear{
		client: o.Client,
		key:    strings.ToUpper(strings.TrimSpace(o.Team)),
		caps:   o.Capabilities,
		links:  map[string][]string{},
	}, nil
}

// Kind is the adapter's name.
func (l *Linear) Kind() string { return LinearKind }

// Capabilities is what this adapter can do, as declared.
func (l *Linear) Capabilities() core.CapabilitySet { return l.caps }

// Slug is the board, as a person would name it.
func (l *Linear) Slug() string {
	if l.key == "" {
		return LinearKind
	}
	return LinearKind + "/" + l.key
}

// Probe reports what the credential may do with this team (spec 05 §3).
//
// Linear publishes no per-permission endpoint, so the probe reads the two
// facts that decide whether flow may write the board: whether the credential
// belongs to an administrator of the workspace, and whether it owns the team.
// Either one may edit a team's workflow states, which is the only capability
// the declaration requires a permission for.
func (l *Linear) Probe(ctx context.Context) (core.Permissions, error) {
	var permissions core.Permissions
	me, err := l.viewer(ctx)
	if err != nil {
		return core.Permissions{}, core.Explain(err, "find out what the credential may do with %s", l.Slug())
	}
	permissions.Evidence = append(permissions.Evidence,
		fmt.Sprintf("the credential is %s of the %s workspace", me.Viewer.login(), me.Organization.Name))

	admin := me.Viewer.Admin
	if admin {
		permissions.Evidence = append(permissions.Evidence, "it administers the workspace")
	}
	if l.key != "" {
		team, err := l.lookUpTeam(ctx)
		switch {
		case errors.Is(err, core.ErrNotFound):
			permissions.Evidence = append(permissions.Evidence,
				fmt.Sprintf("it cannot see a team called %s", l.key))
		case err != nil:
			return core.Permissions{}, err
		case team.admin():
			admin = true
			permissions.Evidence = append(permissions.Evidence,
				fmt.Sprintf("it owns the %s team", team.Key))
		default:
			permissions.Evidence = append(permissions.Evidence,
				fmt.Sprintf("it is a member of the %s team", team.Key))
		}
	}
	if admin {
		permissions.Tokens = append(permissions.Tokens, lnTokenTeamAdmin)
	}
	return permissions, nil
}

// viewer reads who the credential is, at most once a run.
func (l *Linear) viewer(ctx context.Context) (lnViewer, error) {
	data, err := core.GraphQL[lnViewer](ctx, l.client, l.request(), core.GraphQLQuery{Query: lnViewerQuery}, lnSentinel)
	if err != nil {
		return lnViewer{}, err
	}
	l.mu.Lock()
	organization := data.Organization
	l.org = &organization
	l.mu.Unlock()
	return data, nil
}

// Discover offers the teams that could answer the tracker.project
// placeholder (spec 07 §3).
func (l *Linear) Discover(ctx context.Context, what string, hint map[string]string) ([]core.Candidate, error) {
	if what != core.ItemTrackerProject {
		return nil, nil
	}
	var (
		candidates []core.Candidate
		after      string
	)
	for {
		variables := map[string]any{}
		if after != "" {
			variables["after"] = after
		}
		data, err := core.GraphQL[lnTeamsResult](ctx, l.client, l.request(),
			core.GraphQLQuery{Query: lnTeamsQuery, Variables: variables}, lnSentinel)
		if err != nil {
			return nil, core.Explain(err, "list the teams flow could work")
		}
		for _, team := range data.Teams.Nodes {
			if lnHintMatches(team, hint) {
				candidates = append(candidates, team.toCandidate(data.Organization.URLKey))
			}
		}
		if !data.Teams.PageInfo.HasNextPage || data.Teams.PageInfo.EndCursor == "" {
			return candidates, nil
		}
		after = data.Teams.PageInfo.EndCursor
	}
}

// lnHintMatches reports whether a team answers what configuration already
// says. A hint flow has no field for is ignored rather than refused: a hint
// narrows the offer and never empties it.
func lnHintMatches(team lnTeam, hint map[string]string) bool {
	for field, want := range hint {
		if want == "" {
			continue
		}
		switch field {
		case "key", core.ItemTrackerProject:
			if !strings.EqualFold(team.Key, want) {
				return false
			}
		case "name":
			if !strings.Contains(strings.ToLower(team.Name), strings.ToLower(want)) {
				return false
			}
		}
	}
	return true
}

// ParseRef turns an address somebody pasted into a reference flow can record.
//
// Every Linear address begins with the workspace, so the team is the segment
// after `team`, and an issue's identifier carries the team key in front of
// its number. A bare key is taken as one, because that is what people type.
func (l *Linear) ParseRef(raw string) (core.Ref, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return core.Ref{}, lnNotARef(raw)
	}
	if !strings.Contains(text, "/") && !strings.Contains(text, " ") {
		if key, _, ok := lnSplitIdentifier(text); ok {
			return core.Ref{What: core.ItemTrackerProject, ID: key}, nil
		}
		if lnTeamKey(text) {
			return core.Ref{What: core.ItemTrackerProject, ID: strings.ToUpper(text)}, nil
		}
		return core.Ref{}, lnNotARef(raw)
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "https://"), "http://"), "/")
	host, rest, ok := strings.Cut(trimmed, "/")
	if !ok || !strings.EqualFold(host, strings.TrimPrefix(linearSite, "https://")) {
		return core.Ref{}, lnNotARef(raw)
	}
	segments := strings.Split(rest, "/")
	for at, segment := range segments {
		if at+1 >= len(segments) {
			break
		}
		switch segment {
		case "team", "project":
			if lnTeamKey(segments[at+1]) {
				return core.Ref{What: core.ItemTrackerProject, ID: strings.ToUpper(segments[at+1]), URL: text}, nil
			}
		case "issue":
			if key, _, ok := lnSplitIdentifier(segments[at+1]); ok {
				return core.Ref{What: core.ItemTrackerProject, ID: key, URL: text}, nil
			}
		}
	}
	return core.Ref{}, lnNotARef(raw)
}

// lnNotARef explains that an address names no Linear team.
func lnNotARef(raw string) error {
	return core.Errorf(core.ErrValidation, "read %q as a Linear team", raw).
		WithWhy("it is not a team key, an issue identifier or an address of one").
		WithFix("paste the team's board address, such as %s/acme/team/ENG/all", linearSite)
}

// lnSplitIdentifier splits an issue identifier into its team key and number.
func lnSplitIdentifier(text string) (string, string, bool) {
	key, number, ok := strings.Cut(text, "-")
	if !ok || key == "" || number == "" || !lnTeamKey(key) {
		return "", "", false
	}
	for _, r := range number {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return strings.ToUpper(key), number, true
}

// lnTeamKey reports whether a word could be a team key: letters and digits,
// which is what Linear allows, and at least one of them.
func lnTeamKey(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

// Observe returns the current state of one desired item (spec 06 §2).
func (l *Linear) Observe(ctx context.Context, what string) (core.Observation, error) {
	observation := core.Observation{What: what}
	switch what {
	case core.ItemTrackerProject:
		team, err := l.lookUpTeam(ctx)
		switch {
		case errors.Is(err, core.ErrNotFound):
			return observation.Canonical(), nil
		case err != nil:
			return core.Observation{}, err
		case l.key == "":
			return observation.Canonical(), nil
		}
		observation.Exists = true
		observation.Items = []core.Item{{ID: team.Key, Name: team.Name}}
		return observation.Canonical(), nil

	case core.ItemTrackerStatusOptions:
		states, err := l.states(ctx)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return observation.Canonical(), nil
			}
			return core.Observation{}, err
		}
		observation.Exists = len(states) > 0
		for _, state := range states {
			observation.Items = append(observation.Items, state.toItem())
		}
		return observation.Canonical(), nil

	case core.ItemTrackerLabels:
		labels, err := l.labels(ctx)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return observation.Canonical(), nil
			}
			return core.Observation{}, err
		}
		observation.Exists = len(labels) > 0
		for _, label := range labels {
			observation.Items = append(observation.Items, label.toItem())
		}
		return observation.Canonical(), nil

	case core.ItemTrackerWIP, core.ItemTrackerExpedite:
		// Linear enforces neither, and the declaration says so: both are
		// compensated by the policy gate (spec 05 §4).
		return observation.Canonical(), nil

	default:
		return core.Observation{}, lnNoSuchItem("observe", what)
	}
}

// lnNoSuchItem explains that a tracker has nothing of that name.
func lnNoSuchItem(verb, what string) error {
	return core.Errorf(core.ErrValidation, "%s %s", verb, what).
		WithWhy("a tracker has nothing called that").
		WithFix("ask the tracker about one of %s", strings.Join(lnTrackerItems(), ", "))
}

// lnTrackerItems is every item a tracker answers for, in the order spec 03
// declares them.
func lnTrackerItems() []string {
	return []string{
		core.ItemTrackerProject,
		core.ItemTrackerStatusOptions,
		core.ItemTrackerLabels,
		core.ItemTrackerWIP,
		core.ItemTrackerExpedite,
	}
}

// Apply makes one change to the board.
//
// A Linear team's workflow states and its labels are both writable, which is
// what the declaration says (assets/capabilities/linear.yaml). The team
// itself is not: flow does not create somebody's team, and neither a WIP
// limit nor an expedite view is a thing Linear has.
func (l *Linear) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if !slices.Contains(lnTrackerItems(), c.What) {
		return core.ApplyResult{}, lnNoSuchItem(string(c.Op), c.What)
	}
	switch c.What {
	case core.ItemTrackerStatusOptions:
		return l.editColumn(ctx, c)
	case core.ItemTrackerLabels:
		return l.editLabel(ctx, c)
	default:
		return core.ApplyResult{}, core.Errorf(core.ErrUnsupported, "%s %s", c.Op, c.What).
			WithWhy("Linear has nothing flow could write for %s", c.What).
			WithFix("the declaration says so too: the remediation is a follow-up or the policy gate")
	}
}

// lnUnknownOp explains an operation flow has no name for.
func lnUnknownOp(c core.Change) error {
	return core.Errorf(core.ErrValidation, "%s %q", c.Op, c.Item.Name).
		WithWhy("flow has no such operation").
		WithFix("use %s, %s or %s", core.ChangeCreate, core.ChangeUpdate, core.ChangeDelete)
}

// editColumn carries out one change to the team's workflow states.
func (l *Linear) editColumn(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if err := l.teamed("%s the column %q", c.Op, c.Item.Name); err != nil {
		return core.ApplyResult{}, err
	}
	switch c.Op {
	case core.ChangeCreate:
		return l.createColumn(ctx, c)
	case core.ChangeUpdate:
		return l.updateColumn(ctx, c)
	case core.ChangeDelete:
		return l.deleteColumn(ctx, c)
	default:
		return core.ApplyResult{}, lnUnknownOp(c)
	}
}

// createColumn adds a workflow state the team has not got.
func (l *Linear) createColumn(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	states, err := l.states(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if slices.ContainsFunc(states, func(s lnState) bool { return strings.EqualFold(s.Name, c.Item.Name) }) {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "create the column %q", c.Item.Name).
			WithWhy("%s already has a workflow state with that name", l.Slug()).
			WithFix("flow will update it instead")
	}
	team, err := l.lookUpTeam(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	input := map[string]any{
		"teamId": team.ID,
		"name":   c.Item.Name,
		"type":   lnStateTypeOf(c.Item.Field(lnFieldCategory)),
	}
	if colour := lnColourOf(c.Item); colour != "" {
		input["color"] = colour
	}
	if description := c.Item.Field(lnFieldDescription); description != "" {
		input["description"] = description
	}
	data, err := lnMutate[lnStateCreatePayload](ctx, l, lnStateCreateMutation, map[string]any{"input": input})
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "create the column %q in %s", c.Item.Name, l.Slug())
	}
	made, err := lnMade(data.Result.Success, data.Result.State, "create the column %q", c.Item.Name)
	if err != nil {
		return core.ApplyResult{}, err
	}
	l.forgetStates()
	return core.ApplyResult{
		Change: c,
		Item:   made.toItem(),
		Note:   fmt.Sprintf("added the column %q to %s", made.Name, l.Slug()),
	}, nil
}

// updateColumn edits a workflow state the team has, renaming it where the
// change carries a name the state it names has not got.
//
// A category is not among the things that can change. Linear's update input
// takes no type, so the only way to give a state another category would be to
// delete it and make a new one — which would take every issue standing in it
// off the board. flow says so rather than doing that (spec 06 §4).
func (l *Linear) updateColumn(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	current, err := l.columnOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if want := c.Item.Field(lnFieldCategory); want != "" && want != lnCategory(current.Type) {
		return core.ApplyResult{}, core.Errorf(core.ErrUnsupported, "move the column %q into %s", current.Name, want).
			WithWhy("Linear settles a workflow state's type when it is created and takes no type in an edit").
			WithFix("make a column in the %s category by hand and move the issues into it", want)
	}
	input := map[string]any{}
	if current.Name != c.Item.Name {
		input["name"] = c.Item.Name
	}
	if colour := lnColourOf(c.Item); colour != "" && !strings.EqualFold(colour, current.Color) {
		input["color"] = colour
	}
	if description := c.Item.Field(lnFieldDescription); description != "" && description != current.Description {
		input["description"] = description
	}
	if len(input) == 0 {
		return core.ApplyResult{
			Change: c,
			Item:   current.toItem(),
			Note:   fmt.Sprintf("the column %q of %s already says that", current.Name, l.Slug()),
		}, nil
	}
	data, err := lnMutate[lnStateUpdatePayload](ctx, l, lnStateUpdateMutation, map[string]any{"id": current.ID, "input": input})
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "update the column %q of %s", current.Name, l.Slug())
	}
	edited, err := lnMade(data.Result.Success, data.Result.State, "update the column %q", current.Name)
	if err != nil {
		return core.ApplyResult{}, err
	}
	l.forgetStates()
	note := fmt.Sprintf("updated the column %q of %s", edited.Name, l.Slug())
	if _, renamed := input["name"]; renamed {
		note = fmt.Sprintf("renamed the column %q of %s to %q", current.Name, l.Slug(), edited.Name)
	}
	return core.ApplyResult{Change: c, Item: edited.toItem(), Note: note}, nil
}

// deleteColumn removes a workflow state.
//
// Linear archives a state rather than deleting it, and refuses to archive one
// that issues still stand in. That refusal is the board protecting itself, so
// it is passed on as the retreat it is: somebody moves the work, and the next
// run takes the column away (spec 06 §4).
func (l *Linear) deleteColumn(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	current, err := l.columnOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	data, err := lnMutate[lnStateArchivePayload](ctx, l, lnStateArchiveMutation, map[string]any{"id": current.ID})
	if err != nil {
		if errors.Is(err, core.ErrValidation) || errors.Is(err, core.ErrConflict) {
			return core.ApplyResult{
				Change:   c,
				Item:     current.toItem(),
				FollowUp: fmt.Sprintf("Linear would not archive the column %q: %v", current.Name, err),
			}, nil
		}
		return core.ApplyResult{}, core.Explain(err, "delete the column %q of %s", current.Name, l.Slug())
	}
	if !data.Result.Success {
		return core.ApplyResult{
			Change:   c,
			Item:     current.toItem(),
			FollowUp: fmt.Sprintf("Linear would not archive the column %q", current.Name),
		}, nil
	}
	l.forgetStates()
	return core.ApplyResult{
		Change: c,
		Item:   current.toItem(),
		Note:   fmt.Sprintf("removed the column %q from %s", current.Name, l.Slug()),
	}, nil
}

// columnOf finds the workflow state a change is about: by id where the change
// carries one, because that survives a rename, and by name otherwise.
func (l *Linear) columnOf(ctx context.Context, want core.Item) (lnState, error) {
	states, err := l.states(ctx)
	if err != nil {
		return lnState{}, err
	}
	if want.ID != "" {
		if at := slices.IndexFunc(states, func(s lnState) bool { return s.ID == want.ID }); at >= 0 {
			return states[at], nil
		}
	}
	if at := slices.IndexFunc(states, func(s lnState) bool { return strings.EqualFold(s.Name, want.Name) }); at >= 0 {
		return states[at], nil
	}
	return lnState{}, core.Errorf(core.ErrNotFound, "find the column %q in %s", want.Name, l.Slug()).
		WithWhy("the team has no workflow state with that name").
		WithFix("run flow plan again: the column was there when the plan was made")
}

// editLabel carries out one change to the labels this team may use.
func (l *Linear) editLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if err := l.teamed("%s the label %q", c.Op, c.Item.Name); err != nil {
		return core.ApplyResult{}, err
	}
	switch c.Op {
	case core.ChangeCreate:
		return l.createLabel(ctx, c)
	case core.ChangeUpdate:
		return l.updateLabel(ctx, c)
	case core.ChangeDelete:
		return l.deleteLabel(ctx, c)
	default:
		return core.ApplyResult{}, lnUnknownOp(c)
	}
}

// createLabel adds a label the team has not got. A label is created on the
// team rather than on the workspace: the board is one team's, and a label
// every team in the workspace sees is a decision somebody else makes.
func (l *Linear) createLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	labels, err := l.labels(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if slices.ContainsFunc(labels, func(lb lnLabel) bool { return strings.EqualFold(lb.Name, c.Item.Name) }) {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "create the label %q", c.Item.Name).
			WithWhy("%s already has a label with that name", l.Slug()).
			WithFix("flow will update it instead")
	}
	team, err := l.lookUpTeam(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	input := map[string]any{"teamId": team.ID, "name": c.Item.Name}
	if colour := lnColourOf(c.Item); colour != "" {
		input["color"] = colour
	}
	if description := c.Item.Field(lnFieldDescription); description != "" {
		input["description"] = description
	}
	data, err := lnMutate[lnLabelCreatePayload](ctx, l, lnLabelCreateMutation, map[string]any{"input": input})
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "create the label %q in %s", c.Item.Name, l.Slug())
	}
	made, err := lnMade(data.Result.Success, data.Result.Label, "create the label %q", c.Item.Name)
	if err != nil {
		return core.ApplyResult{}, err
	}
	l.forgetLabels()
	return core.ApplyResult{
		Change: c,
		Item:   made.toItem(),
		Note:   fmt.Sprintf("added the label %q to %s", made.Name, l.Slug()),
	}, nil
}

// updateLabel edits a label the team has.
func (l *Linear) updateLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	current, err := l.labelOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	// Only what the change names is sent. flow has no opinion about a
	// label's colour or description, so an update that carries neither must
	// leave both as they are.
	input := map[string]any{}
	if current.Name != c.Item.Name {
		input["name"] = c.Item.Name
	}
	if colour := lnColourOf(c.Item); colour != "" && !strings.EqualFold(colour, current.Color) {
		input["color"] = colour
	}
	if description := c.Item.Field(lnFieldDescription); description != "" && description != current.Description {
		input["description"] = description
	}
	if len(input) == 0 {
		return core.ApplyResult{
			Change: c,
			Item:   current.toItem(),
			Note:   fmt.Sprintf("the label %q of %s already says that", current.Name, l.Slug()),
		}, nil
	}
	data, err := lnMutate[lnLabelUpdatePayload](ctx, l, lnLabelUpdateMutation, map[string]any{"id": current.ID, "input": input})
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "update the label %q of %s", current.Name, l.Slug())
	}
	edited, err := lnMade(data.Result.Success, data.Result.Label, "update the label %q", current.Name)
	if err != nil {
		return core.ApplyResult{}, err
	}
	l.forgetLabels()
	note := fmt.Sprintf("updated the label %q of %s", edited.Name, l.Slug())
	if _, renamed := input["name"]; renamed {
		note = fmt.Sprintf("renamed the label %q of %s to %q", current.Name, l.Slug(), edited.Name)
	}
	return core.ApplyResult{Change: c, Item: edited.toItem(), Note: note}, nil
}

// deleteLabel removes a label. Linear takes it off every issue that carried
// it, which is why only a label the lock records flow as having created is
// ever proposed for removal (spec 06 §4).
func (l *Linear) deleteLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	current, err := l.labelOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	data, err := lnMutate[lnLabelDeletePayload](ctx, l, lnLabelDeleteMutation, map[string]any{"id": current.ID})
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "delete the label %q from %s", current.Name, l.Slug())
	}
	if !data.Result.Success {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "delete the label %q from %s", current.Name, l.Slug()).
			WithWhy("Linear refused without saying why")
	}
	l.forgetLabels()
	return core.ApplyResult{
		Change: c,
		Item:   current.toItem(),
		Note:   fmt.Sprintf("removed the label %q from %s", current.Name, l.Slug()),
	}, nil
}

// labelOf finds the label a change is about.
func (l *Linear) labelOf(ctx context.Context, want core.Item) (lnLabel, error) {
	labels, err := l.labels(ctx)
	if err != nil {
		return lnLabel{}, err
	}
	if want.ID != "" {
		if at := slices.IndexFunc(labels, func(lb lnLabel) bool { return lb.ID == want.ID }); at >= 0 {
			return labels[at], nil
		}
	}
	if at := slices.IndexFunc(labels, func(lb lnLabel) bool { return strings.EqualFold(lb.Name, want.Name) }); at >= 0 {
		return labels[at], nil
	}
	return lnLabel{}, core.Errorf(core.ErrNotFound, "find the label %q in %s", want.Name, l.Slug()).
		WithWhy("the workspace has no label with that name this team may use").
		WithFix("run flow plan again: the label was there when the plan was made")
}

// lnMade is the thing a mutation says it made, or the refusal it was.
func lnMade[T any](success bool, made *T, format string, args ...any) (T, error) {
	var zero T
	if !success || made == nil {
		return zero, core.Errorf(core.ErrConflict, format, args...).
			WithWhy("Linear answered without the thing it was asked to write").
			WithFix("try again, and look at the team's settings if it happens twice")
	}
	return *made, nil
}

// Ticket returns one ticket by its identifier.
//
// A lookup answers for an archived issue, which a listing does not: the
// listing is the board and the lookup is the issue (spec 11 §1). An issue in
// the trash is gone as far as flow is concerned.
func (l *Linear) Ticket(ctx context.Context, key string) (core.Ticket, error) {
	issue, err := l.issue(ctx, key)
	if err != nil {
		return core.Ticket{}, err
	}
	return issue.toTicket(), nil
}

// issue reads one issue.
func (l *Linear) issue(ctx context.Context, key string) (lnIssue, error) {
	wanted := strings.ToUpper(strings.TrimSpace(key))
	if wanted == "" {
		return lnIssue{}, core.Errorf(core.ErrValidation, "read a ticket").
			WithWhy("no key was given")
	}
	data, err := core.GraphQL[lnIssueResult](ctx, l.client, l.request(),
		core.GraphQLQuery{Query: lnIssueQuery, Variables: map[string]any{"key": wanted}}, lnSentinel)
	switch {
	// A refusal Linear wrote keeps Linear's own words: only the silence of
	// a null issue is flow's to explain.
	case err != nil:
		return lnIssue{}, core.Explain(err, "read %s", wanted)
	case data.Issue == nil, data.Issue.Trashed != nil && *data.Issue.Trashed:
		return lnIssue{}, lnNoSuchTicket(wanted)
	}
	l.remember(*data.Issue)
	return *data.Issue, nil
}

// lnNoSuchTicket is a key the workspace does not hold.
func lnNoSuchTicket(key string) error {
	return core.Errorf(core.ErrNotFound, "read %s", key).
		WithWhy("Linear has no such issue, or the credential cannot see it").
		WithFix("check the identifier, and that the credential is a member of the team")
}

// Tickets returns the tickets matching a query.
//
// Keys are a question about issues and the rest is a question about the
// board, which is why a key answers for an archived issue and a listing
// leaves one out (spec 11 §1).
func (l *Linear) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	if len(q.Keys) > 0 {
		var tickets []core.Ticket
		for _, key := range q.Keys {
			issue, err := l.issue(ctx, key)
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			tickets = append(tickets, issue.toTicket())
		}
		return tickets, nil
	}
	if err := l.teamed("list the tickets of the board"); err != nil {
		return nil, err
	}
	filter := map[string]any{"team": map[string]any{"key": map[string]any{"eq": l.key}}}
	if !q.UpdatedSince.IsZero() {
		filter["updatedAt"] = map[string]any{"gte": q.UpdatedSince.UTC().Format(lnTimeLayout)}
	}
	if q.Column != "" {
		state, err := l.columnID(ctx, q.Column)
		if err != nil {
			return nil, err
		}
		filter["state"] = map[string]any{"id": map[string]any{"eq": state}}
	}
	issues, err := l.list(ctx, lnIssuesQuery, filter, q.Limit)
	if err != nil {
		return nil, core.Explain(err, "list the tickets of %s", l.Slug())
	}
	tickets := make([]core.Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, issue.toTicket())
	}
	return tickets, nil
}

// list walks every page of a listing, remembering the links each issue
// carries as it goes.
func (l *Linear) list(ctx context.Context, document string, filter map[string]any, limit int) ([]lnIssue, error) {
	var (
		issues []lnIssue
		after  string
	)
	for {
		first := lnPageSize
		if limit > 0 && limit-len(issues) < first {
			first = limit - len(issues)
		}
		variables := map[string]any{"filter": filter, "first": first}
		if after != "" {
			variables["after"] = after
		}
		data, err := core.GraphQL[lnIssuesResult](ctx, l.client, l.request(),
			core.GraphQLQuery{Query: document, Variables: variables}, lnSentinel)
		if err != nil {
			return nil, err
		}
		for _, issue := range data.Issues.Nodes {
			l.remember(issue)
			issues = append(issues, issue)
		}
		done := !data.Issues.PageInfo.HasNextPage || data.Issues.PageInfo.EndCursor == "" ||
			len(data.Issues.Nodes) == 0 || (limit > 0 && len(issues) >= limit)
		if done {
			return issues, nil
		}
		after = data.Issues.PageInfo.EndCursor
	}
}

// Move takes a ticket to another column (spec 04 §5).
func (l *Linear) Move(ctx context.Context, key string, to core.ColumnID) error {
	issue, err := l.issue(ctx, key)
	if err != nil {
		return err
	}
	if !issue.ArchivedAt.Time.IsZero() {
		return core.Errorf(core.ErrConflict, "move %s", issue.Identifier).
			WithWhy("it is archived, and Linear takes no change to an archived issue").
			WithFix("unarchive it in Linear if the work is live again")
	}
	state, err := l.columnID(ctx, to)
	if err != nil {
		return err
	}
	if issue.State != nil && issue.State.ID == state {
		return nil
	}
	l.writing.Lock()
	defer l.writing.Unlock()
	if _, err := lnMutate[lnIssueUpdatePayload](ctx, l, lnIssueUpdateMutation, map[string]any{"key": issue.Identifier, "input": map[string]any{"stateId": state}}); err != nil {
		return core.Explain(err, "move %s", issue.Identifier)
	}
	return nil
}

// columnID is the workflow state a column names, which is the tracker's own
// id or the name a human would recognise — the only name the configuration
// has (spec 04 §2).
func (l *Linear) columnID(ctx context.Context, to core.ColumnID) (string, error) {
	states, err := l.states(ctx)
	if err != nil {
		return "", err
	}
	want := string(to)
	if at := slices.IndexFunc(states, func(s lnState) bool { return s.ID == want }); at >= 0 {
		return states[at].ID, nil
	}
	if at := slices.IndexFunc(states, func(s lnState) bool { return strings.EqualFold(s.Name, want) }); at >= 0 {
		return states[at].ID, nil
	}
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.Name)
	}
	return "", core.Errorf(core.ErrValidation, "reach the column %q", want).
		WithWhy("%s has no such workflow state", l.Slug()).
		WithFix("name one of %s, or add the column to the team in Linear", strings.Join(names, ", "))
}

// CreateFollowUp files a ticket for work flow cannot do, or returns the one
// already filed (spec 07 §4).
func (l *Linear) CreateFollowUp(ctx context.Context, f core.FollowUp) (core.Ticket, error) {
	if f.Marker.IsZero() {
		return core.Ticket{}, core.Errorf(core.ErrValidation, "file a follow-up").
			WithWhy("it carries no marker, so flow could never find it again").
			WithFix("give the follow-up a marker")
	}
	if err := l.teamed("file the follow-up %q", f.Title); err != nil {
		return core.Ticket{}, err
	}
	if found, err := l.FindFollowUp(ctx, f.Marker); err != nil {
		return core.Ticket{}, err
	} else if found != nil {
		return *found, nil
	}
	team, err := l.lookUpTeam(ctx)
	if err != nil {
		return core.Ticket{}, err
	}
	input := map[string]any{
		"teamId":      team.ID,
		"title":       f.Title,
		"description": core.MarkBody(f.Body, f.Marker, true),
	}
	if ids, err := l.labelIDs(ctx, f.Labels); err != nil {
		return core.Ticket{}, err
	} else if len(ids) > 0 {
		input["labelIds"] = ids
	}
	if owner, err := l.assignee(ctx, f.Assignee); err != nil {
		return core.Ticket{}, err
	} else if owner != "" {
		input["assigneeId"] = owner
	}
	l.writing.Lock()
	defer l.writing.Unlock()
	data, err := lnMutate[lnIssuePayload](ctx, l, lnIssueCreateMutation, map[string]any{"input": input})
	if err != nil {
		return core.Ticket{}, core.Explain(err, "file the follow-up %q in %s", f.Title, l.Slug())
	}
	made, err := lnMade(data.Result.Success, data.Result.Issue, "file the follow-up %q", f.Title)
	if err != nil {
		return core.Ticket{}, err
	}
	ticket := made.toTicket()
	l.file(f.Marker, ticket)
	return ticket, nil
}

// labelIDs is the ids of the labels a follow-up asks for. A label the
// workspace has not got is left off rather than failing the filing: the
// ticket is the point, and `flow apply` is what makes the labels (spec 07 §4).
func (l *Linear) labelIDs(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	labels, err := l.labels(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, name := range names {
		if at := slices.IndexFunc(labels, func(lb lnLabel) bool { return strings.EqualFold(lb.Name, name) }); at >= 0 {
			ids = append(ids, labels[at].ID)
		}
	}
	return ids, nil
}

// assignee is the id of the person a follow-up is for, or empty where the
// configuration names nobody or names somebody Linear does not know. An owner
// who has left is not a reason to lose the ticket.
func (l *Linear) assignee(ctx context.Context, login string) (string, error) {
	if strings.TrimSpace(login) == "" {
		return "", nil
	}
	me, err := l.viewer(ctx)
	if err != nil {
		return "", err
	}
	for _, name := range []string{me.Viewer.DisplayName, me.Viewer.Name, me.Viewer.Email} {
		if name != "" && strings.EqualFold(name, login) {
			return me.Viewer.ID, nil
		}
	}
	return "", nil
}

// FindFollowUp returns the ticket carrying a marker, or nil (spec 11 §1).
//
// The search includes archived issues, because Linear archives an issue a
// while after it is closed and a follow-up flow filed last month is exactly
// the ticket it must not file again.
func (l *Linear) FindFollowUp(ctx context.Context, m core.Marker) (*core.Ticket, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find a follow-up").
			WithWhy("no marker was given, and a marker is the only thing that identifies one").
			WithFix("search for a marker such as %s", core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"))
	}
	if err := l.teamed("find the follow-up marked %s", m); err != nil {
		return nil, err
	}
	if found, ok := l.recall(m); ok {
		return &found, nil
	}
	filter := map[string]any{
		"team":        map[string]any{"key": map[string]any{"eq": l.key}},
		"description": map[string]any{"contains": core.MarkerPrefix + m.Kind},
	}
	issues, err := l.list(ctx, lnArchivedIssuesQuery, filter, 0)
	if err != nil {
		return nil, core.Explain(err, "find the follow-up marked %s", m)
	}
	for _, issue := range issues {
		if ticket := issue.toTicket(); core.HasMarker(ticket.Body, m) {
			return &ticket, nil
		}
	}
	return nil, nil
}

// file records a ticket flow filed, so that a search run before the
// workspace's index has caught up still finds it.
func (l *Linear) file(m core.Marker, ticket core.Ticket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.filed = append(l.filed, lnFiled{marker: m, ticket: ticket})
}

// recall returns a ticket flow filed in this process whose marker the search
// matches. The match is the marker's own, so a search naming fewer fields
// finds what a fuller marker filed (spec 11 §1).
func (l *Linear) recall(m core.Marker) (core.Ticket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, filed := range l.filed {
		if m.Matches(filed.marker) {
			return filed.ticket, true
		}
	}
	return core.Ticket{}, false
}

// UpdateFollowUp rewrites the title and body of a ticket flow opened, keeping
// its marker (spec 07 §4, spec 07 §10).
func (l *Linear) UpdateFollowUp(ctx context.Context, key string, m core.Marker, title, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "rewrite %s", key).
			WithWhy("no marker was given, and flow rewrites only the tickets it opened").
			WithFix("pass the marker the ticket was filed with")
	}
	ticket, err := l.Ticket(ctx, key)
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
	l.writing.Lock()
	defer l.writing.Unlock()
	_, err = lnMutate[lnIssueUpdatePayload](ctx, l, lnIssueUpdateMutation, map[string]any{
		"key":   ticket.Key,
		"input": map[string]any{"title": title, "description": marked},
	})
	if err != nil {
		return core.Explain(err, "rewrite %s", ticket.Key)
	}
	return nil
}

// Comment writes a marked comment on a ticket, editing flow's own rather than
// writing a second one (spec 07 §4).
func (l *Linear) Comment(ctx context.Context, key string, m core.Marker, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "comment on %s", key).
			WithWhy("no marker was given, and a comment flow cannot find again would be written twice").
			WithFix("pass a marker")
	}
	issue, err := l.issue(ctx, key)
	if err != nil {
		return err
	}
	existing, err := l.findComment(ctx, issue.Identifier, m)
	if err != nil {
		return err
	}
	marked := core.MarkBody(body, m, true)
	l.writing.Lock()
	defer l.writing.Unlock()
	if existing != nil {
		if existing.Body == marked {
			return nil
		}
		_, err = lnMutate[lnCommentUpdatePayload](ctx, l, lnCommentUpdateMutation, map[string]any{"id": existing.ID, "input": map[string]any{"body": marked}})
	} else {
		_, err = lnMutate[lnCommentCreatePayload](ctx, l, lnCommentCreateMutation, map[string]any{"input": map[string]any{"issueId": issue.ID, "body": marked}})
	}
	if err != nil {
		return core.Explain(err, "comment on %s", issue.Identifier)
	}
	return nil
}

// findComment returns flow's own comment carrying a marker, or nil.
func (l *Linear) findComment(ctx context.Context, key string, m core.Marker) (*lnComment, error) {
	after := ""
	for {
		variables := map[string]any{"key": key}
		if after != "" {
			variables["after"] = after
		}
		data, err := core.GraphQL[lnCommentsResult](ctx, l.client, l.request(),
			core.GraphQLQuery{Query: lnCommentsQuery, Variables: variables}, lnSentinel)
		if err != nil {
			return nil, core.Explain(err, "read the comments of %s", key)
		}
		if data.Issue == nil {
			return nil, lnNoSuchTicket(key)
		}
		for _, comment := range data.Issue.Comments.Nodes {
			if core.HasMarker(comment.Body, m) {
				return &comment, nil
			}
		}
		page := data.Issue.Comments.PageInfo
		if !page.HasNextPage || page.EndCursor == "" || len(data.Issue.Comments.Nodes) == 0 {
			return nil, nil
		}
		after = page.EndCursor
	}
}

// Close closes a ticket, recording why (spec 07 §4).
//
// A Linear issue has no closed flag: work is finished when it stands in a
// state Linear calls completed, and work that was dropped stands in one it
// calls canceled. The two are flow's two close reasons, which is why the
// reason chooses the column.
func (l *Linear) Close(ctx context.Context, key string, reason string) error {
	issue, err := l.issue(ctx, key)
	if err != nil {
		return err
	}
	if issue.State != nil && lnFinished(issue.State.Type) {
		return nil
	}
	wanted := lnStateCompleted
	if reason == core.ClosedNotPlanned {
		wanted = lnStateCanceled
	}
	state, err := l.stateOfType(ctx, wanted, lnStateCompleted, lnStateCanceled)
	if err != nil {
		return core.Explain(err, "close %s", issue.Identifier)
	}
	return l.setState(ctx, issue, state, "close %s")
}

// Reopen reopens a closed ticket (spec 07 §4).
func (l *Linear) Reopen(ctx context.Context, key string, reason string) error {
	issue, err := l.issue(ctx, key)
	if err != nil {
		return err
	}
	if issue.State == nil || !lnFinished(issue.State.Type) {
		return nil
	}
	state, err := l.stateOfType(ctx, lnStateUnstarted, lnStateBacklog, lnStateTriage)
	if err != nil {
		return core.Explain(err, "reopen %s", issue.Identifier)
	}
	return l.setState(ctx, issue, state, "reopen %s")
}

// setState puts an issue in a workflow state.
func (l *Linear) setState(ctx context.Context, issue lnIssue, state lnState, what string) error {
	l.writing.Lock()
	defer l.writing.Unlock()
	if _, err := lnMutate[lnIssueUpdatePayload](ctx, l, lnIssueUpdateMutation, map[string]any{"key": issue.Identifier, "input": map[string]any{"stateId": state.ID}}); err != nil {
		return core.Explain(err, what, issue.Identifier)
	}
	return nil
}

// stateOfType is the first workflow state of the first of those types the
// team has, in the order the team put its columns in: the leftmost completed
// column is the one work lands in. [Linear.states] is what settles that
// order, and asking a second time here would be a second answer to it.
func (l *Linear) stateOfType(ctx context.Context, types ...string) (lnState, error) {
	states, err := l.states(ctx)
	if err != nil {
		return lnState{}, err
	}
	for _, want := range types {
		if at := slices.IndexFunc(states, func(s lnState) bool {
			return strings.EqualFold(s.Type, want)
		}); at >= 0 {
			return states[at], nil
		}
	}
	return lnState{}, core.Errorf(core.ErrUnsupported, "find a %s column", types[0]).
		WithWhy("%s has no workflow state of that type", l.Slug()).
		WithFix("add one to the team in Linear")
}

// LinkedKeys returns the ticket keys a pull request is for.
//
// Linear is not the forge's tracker, so what a pull request says it closes is
// an issue of a repository and nothing here. Linear's own integration records
// a pull request as an attachment on the issue, and that is the first
// reading: flow asks the attachments of every issue it reads, so a pull
// request whose branch names nothing is still linked by what Linear itself
// recorded. The fallback is the identifier in the branch, the title or the
// header a squash will land, which is Linear's own documented convention and
// never a pattern somebody configured (P12, spec 04 §3).
func (l *Linear) LinkedKeys(pr core.PullRequest) []string {
	var keys []string
	for _, key := range l.linked(pr.URL) {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	for _, text := range []string{pr.HeadRef, pr.Title, pr.MergeHeader()} {
		for _, key := range lnKeysIn(l.key, text) {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// linked is the issues Linear has recorded against an address.
func (l *Linear) linked(url string) []string {
	if url == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.links[url])
}

// remember records what an issue is linked to, so that [Linear.LinkedKeys],
// which is handed a pull request and no way to ask anything, can answer from
// what flow has already read.
func (l *Linear) remember(issue lnIssue) {
	urls := issue.attachmentURLs()
	if len(urls) == 0 || issue.Identifier == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, url := range urls {
		if !slices.Contains(l.links[url], issue.Identifier) {
			l.links[url] = append(l.links[url], issue.Identifier)
		}
	}
}

// lookUpTeam resolves the configured team key into the team, at most once.
func (l *Linear) lookUpTeam(ctx context.Context) (lnTeam, error) {
	if l.key == "" {
		return lnTeam{}, nil
	}
	l.mu.Lock()
	cached := l.team
	l.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	data, err := core.GraphQL[lnTeamsResult](ctx, l.client, l.request(),
		core.GraphQLQuery{Query: lnTeamQuery, Variables: map[string]any{"key": l.key}}, lnSentinel)
	if err != nil {
		return lnTeam{}, core.Explain(err, "read the team %s", l.key)
	}
	if len(data.Teams.Nodes) == 0 {
		return lnTeam{}, core.Errorf(core.ErrNotFound, "read the team %s", l.key).
			WithWhy("the workspace has no team with that key, or the credential cannot see it").
			WithFix("run flow doctor --fix to pick one of the teams it offers")
	}
	team := data.Teams.Nodes[0]
	l.mu.Lock()
	l.team = &team
	l.mu.Unlock()
	return team, nil
}

// teamed refuses an act that needs a board when no team is configured. Setup
// runs before there is one, and saying which setting is missing is more use
// than a query against nothing.
func (l *Linear) teamed(format string, args ...any) error {
	if l.key != "" {
		return nil
	}
	return core.Errorf(core.ErrNeedsInput, format, args...).
		WithWhy("no Linear team is set, so flow does not know which board this is").
		WithFix("run flow doctor --fix, or set tracker.project to a team key such as ENG")
}

// states is the team's workflow states, read at most once a run and left in
// the order the team put them in.
func (l *Linear) states(ctx context.Context) ([]lnState, error) {
	if err := l.teamed("read the columns of the board"); err != nil {
		return nil, err
	}
	l.mu.Lock()
	cached := l.cachedStates
	l.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	team, err := l.lookUpTeam(ctx)
	if err != nil {
		return nil, err
	}
	var (
		states []lnState
		after  string
	)
	for {
		variables := map[string]any{"team": team.ID}
		if after != "" {
			variables["after"] = after
		}
		data, err := core.GraphQL[lnStatesResult](ctx, l.client, l.request(),
			core.GraphQLQuery{Query: lnStatesQuery, Variables: variables}, lnSentinel)
		if err != nil {
			return nil, core.Explain(err, "read the columns of %s", l.Slug())
		}
		if data.Team == nil {
			return nil, core.Errorf(core.ErrNotFound, "read the columns of %s", l.Slug()).
				WithWhy("the credential cannot see the team").
				WithFix("ask somebody to add it to the team in Linear")
		}
		states = append(states, data.Team.States.Nodes...)
		page := data.Team.States.PageInfo
		if !page.HasNextPage || page.EndCursor == "" || len(data.Team.States.Nodes) == 0 {
			break
		}
		after = page.EndCursor
	}
	slices.SortStableFunc(states, func(a, b lnState) int {
		switch {
		case a.Position < b.Position:
			return -1
		case a.Position > b.Position:
			return 1
		default:
			return strings.Compare(a.Name, b.Name)
		}
	})
	l.mu.Lock()
	l.cachedStates = states
	l.mu.Unlock()
	return states, nil
}

// forgetStates drops the cached columns after a write changed them.
func (l *Linear) forgetStates() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cachedStates = nil
}

// labels is every label this team may use: its own, and the workspace's,
// which every team may put on an issue. A group is a heading rather than a
// label and a retired one is no longer offered, so neither is one.
func (l *Linear) labels(ctx context.Context) ([]lnLabel, error) {
	l.mu.Lock()
	cached := l.cachedLabels
	l.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	team, err := l.lookUpTeam(ctx)
	if err != nil {
		return nil, err
	}
	var (
		labels []lnLabel
		after  string
	)
	for {
		variables := map[string]any{}
		if after != "" {
			variables["after"] = after
		}
		data, err := core.GraphQL[lnLabelsResult](ctx, l.client, l.request(),
			core.GraphQLQuery{Query: lnLabelsQuery, Variables: variables}, lnSentinel)
		if err != nil {
			return nil, core.Explain(err, "read the labels of %s", l.Slug())
		}
		for _, label := range data.IssueLabels.Nodes {
			if label.usable(team.ID) {
				labels = append(labels, label)
			}
		}
		page := data.IssueLabels.PageInfo
		if !page.HasNextPage || page.EndCursor == "" || len(data.IssueLabels.Nodes) == 0 {
			break
		}
		after = page.EndCursor
	}
	l.mu.Lock()
	l.cachedLabels = labels
	l.mu.Unlock()
	return labels, nil
}

// forgetLabels drops the cached labels after a write changed them.
func (l *Linear) forgetLabels() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cachedLabels = nil
}

// lnMutate sends one mutation and returns what it answered. It is a function
// rather than a method because what a mutation answers is its own type and Go
// gives a method no type parameters of its own.
func lnMutate[T any](ctx context.Context, l *Linear, document string, variables map[string]any) (T, error) {
	return core.GraphQL[T](ctx, l.client, l.request(),
		core.GraphQLQuery{Query: document, Variables: variables}, lnSentinel)
}

// request is the endpoint every call goes to. A GraphQL API has one.
func (l *Linear) request() core.Request {
	return core.Request{Path: LinearPath}
}

var _ core.Tracker = (*Linear)(nil)
