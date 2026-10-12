// Package tracker holds the ticket-system adapters: GitHub Projects v2 in v1,
// Jira Cloud and Linear in phase 7. Each depends on core alone, so that
// nothing about a provider leaks into the engine (spec 01 §2, spec 11).
package tracker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// GitHub Projects adapter constants (spec 11 §2).
const (
	// GitHubProjectsKind is the adapter name, matching
	// assets/capabilities/github-projects.yaml and tracker.type in
	// configuration.
	GitHubProjectsKind = "github-projects"
	// GitHubAPI is the API root of github.com. GitHub Enterprise Server
	// uses https://<host>/api/v3.
	GitHubAPI = "https://api.github.com"
	// OwnerUser and OwnerOrg are the two kinds of project owner. GitHub
	// addresses a person's projects and an organisation's through
	// different fields, so tracker.owner_type decides which is asked
	// (spec 03 §1.1). They are configuration's own words, repeated here
	// because an adapter may not import config (spec 01 §2).
	OwnerUser = "user"
	OwnerOrg  = "org"
	// GitHubProjectsSite is where a project's web address lives. It is
	// derived from the API root for Enterprise Server, where the API and
	// the site are different hosts.
	GitHubProjectsSite = "https://github.com"
	// ghAccept is the media type every call asks for, and ghAPIVersion
	// pins the dated REST version. GraphQL ignores both; sending them
	// costs nothing and means one decoration covers every request.
	ghAccept     = "application/vnd.github+json"
	ghAPIVersion = "2022-11-28"
	// ghProjectPageSize is how many projects one page carries. Spec 11 §2
	// asks for small queries against a shared points budget.
	ghProjectPageSize = 100
	// ghScopeHeader is the header a classic token's scopes come back in.
	// Nothing else sends it, which is how flow tells a classic token from
	// a fine-grained one or a GitHub App (spec 05 §3).
	ghScopeHeader = "X-OAuth-Scopes"
	// The classic scopes spec 05 §3 names. `project` grants reading and
	// writing a project, and `repo` is what reaches the issues a project
	// holds. There is no scope for reading a project here, because reading
	// one is not inferred: the trial read either worked or it did not.
	ghScopeProject = "project"
	ghScopeRepo    = "repo"
	// The permission tokens assets/capabilities/github-projects.yaml
	// requires. A probe and a declaration that do not share a vocabulary
	// can never agree (spec 05 §1).
	ghTokenProjectRead  = "project_read"
	ghTokenProjectWrite = "project_write"
	ghTokenIssuesWrite  = "issues_write"
)

// GitHubProjectsOptions configures a [GitHubProjects] adapter.
type GitHubProjectsOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner is the login the project belongs to, and OwnerType says
	// whether that login is a person or an organisation.
	Owner     string
	OwnerType string
	// Number is the project's number, as it appears in its web address.
	// It is zero before setup has resolved one, which is not an error:
	// discovery and parsing a pasted reference are exactly what runs then.
	Number int
	// Repo is the repository whose issues the project holds, as
	// owner/name. Follow-ups and labels are written there (item 3.11).
	Repo string
	// Capabilities is the declaration loaded from
	// assets/capabilities/github-projects.yaml. The adapter never invents
	// it: what flow believes it can do comes from the declaration, not
	// from the code (spec 05 §1).
	Capabilities core.CapabilitySet
}

// GitHubProjects is the GitHub Projects v2 tracker adapter.
type GitHubProjects struct {
	client    *core.Client
	owner     string
	ownerType string
	number    int
	repo      string
	caps      core.CapabilitySet
	// graphQL is the endpoint every query goes to and site the web host
	// the project's address is on, both worked out once from the API root
	// the client was built with.
	graphQL string
	site    string
}

// NewGitHubProjects returns an adapter for one project.
func NewGitHubProjects(o GitHubProjectsOptions) (*GitHubProjects, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the github-projects adapter").
			WithWhy("no HTTP client was given")
	case o.Owner == "":
		return nil, core.Errorf(core.ErrValidation, "build the github-projects adapter").
			WithWhy("no project owner was given").
			WithFix("set tracker.owner to the user or organisation the project belongs to")
	case o.OwnerType != OwnerUser && o.OwnerType != OwnerOrg:
		return nil, core.Errorf(core.ErrValidation, "build the github-projects adapter").
			WithWhy("the owner type is %q", o.OwnerType).
			WithFix("set tracker.owner_type to %s or %s: GitHub addresses a person's projects and an organisation's differently", OwnerUser, OwnerOrg)
	case o.Number < 0:
		return nil, core.Errorf(core.ErrValidation, "build the github-projects adapter").
			WithWhy("the project number is %d", o.Number).
			WithFix("set tracker.project to the number in the project's web address")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the github-projects adapter").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/github-projects.yaml and pass the declaration")
	}
	return &GitHubProjects{
		client:    o.Client,
		owner:     o.Owner,
		ownerType: o.OwnerType,
		number:    o.Number,
		repo:      o.Repo,
		caps:      o.Capabilities,
		graphQL:   ghGraphQLEndpoint(o.Client.BaseURL()),
		site:      ghSite(o.Client.BaseURL()),
	}, nil
}

// Kind is the adapter's name.
func (g *GitHubProjects) Kind() string { return GitHubProjectsKind }

// Capabilities is what this adapter declares it can do.
func (g *GitHubProjects) Capabilities() core.CapabilitySet { return g.caps }

// Slug is the project as people write it: the owner and the number, which
// together are what its web address is built from.
func (g *GitHubProjects) Slug() string {
	return g.owner + "/projects/" + strconv.Itoa(g.number)
}

// Probe reports what the credential may do with this project, in two reads:
// the scopes a classic token announces in a header, and a trial read of the
// project itself (spec 05 §3).
//
// The trial read is the only honest answer to "may this credential see the
// board": a fine-grained token or a GitHub App announces no scopes at all,
// and a classic token's `project` scope says what the token may be used for
// rather than which projects the account can reach. A project the credential
// cannot read is a finding and not a failure — that is what doctor is for —
// so it costs the read token rather than refusing.
func (g *GitHubProjects) Probe(ctx context.Context) (core.Permissions, error) {
	var permissions core.Permissions

	scopes, classic, err := g.scopes(ctx)
	if err != nil {
		return core.Permissions{}, err
	}
	if classic {
		permissions.Evidence = append(permissions.Evidence, ghScopeHeader+": "+strings.Join(scopes, ", "))
		permissions.Tokens = append(permissions.Tokens, scopes...)
	} else {
		permissions.Evidence = append(permissions.Evidence,
			"the credential announced no scopes, so it is limited by its grants alone")
	}

	reachable, why, err := g.reachable(ctx)
	if err != nil {
		return core.Permissions{}, err
	}
	permissions.Evidence = append(permissions.Evidence, why)
	if reachable {
		permissions.Tokens = append(permissions.Tokens, ghTokenProjectRead)
		if !classic || ghWrites(scopes, ghScopeProject) {
			permissions.Tokens = append(permissions.Tokens, ghTokenProjectWrite)
		}
	}
	if !classic || ghWrites(scopes, ghScopeRepo) {
		permissions.Tokens = append(permissions.Tokens, ghTokenIssuesWrite)
	}
	return permissions, nil
}

// scopes returns the classic token scopes the provider reported, and whether
// it reported any header at all. An empty header is a classic token with no
// scopes, which is not the same thing as no header.
func (g *GitHubProjects) scopes(ctx context.Context) ([]string, bool, error) {
	res, err := g.do(ctx, core.Request{Path: "user"})
	if err != nil && !errors.Is(err, core.ErrNotFound) && !errors.Is(err, core.ErrPermission) {
		return nil, false, core.Explain(err, "find out what the credential may do with %s", g.Slug())
	}
	if res == nil {
		return nil, false, nil
	}
	raw, ok := res.Header[http.CanonicalHeaderKey(ghScopeHeader)]
	if !ok {
		return nil, false, nil
	}
	var scopes []string
	for _, scope := range strings.Split(strings.Join(raw, ","), ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes, true, nil
}

// reachable reports whether the credential can read the project, and says what
// flow saw. A project nobody set a number for yet cannot be tried, and saying
// so is more use than a refusal during setup.
func (g *GitHubProjects) reachable(ctx context.Context) (bool, string, error) {
	if g.number == 0 {
		return false, "no project number is set yet, so none was tried", nil
	}
	project, err := g.project(ctx)
	switch {
	case err == nil && project.Exists:
		return true, "read the project " + g.Slug(), nil
	case err == nil:
		return false, "the project " + g.Slug() + " is not there", nil
	case errors.Is(err, core.ErrNotFound), errors.Is(err, core.ErrPermission):
		return false, "the credential cannot read the project " + g.Slug(), nil
	default:
		return false, "", core.Explain(err, "find out what the credential may do with %s", g.Slug())
	}
}

// ghWrites reports whether a classic token's scopes contain one that grants
// what is being asked for.
func ghWrites(scopes []string, want string) bool { return slices.Contains(scopes, want) }

// Discover offers the projects that could answer a placeholder (spec 07 §3).
// Only the board itself is discoverable: everything else flow resolves is a
// part of a board it has already been pointed at.
func (g *GitHubProjects) Discover(ctx context.Context, what string, hint map[string]string) ([]core.Candidate, error) {
	if what != core.ItemTrackerProject {
		return nil, nil
	}
	variables := map[string]any{"login": g.owner, "first": ghProjectPageSize}
	var candidates []core.Candidate
	for page := 0; page < core.MaxPages; page++ {
		data, err := core.GraphQL[ghProjectList](ctx, g.client, g.request(), core.GraphQLQuery{
			Query:     ghProjectsQuery(g.ownerType),
			Variables: variables,
		}, ghSentinel)
		if err != nil {
			return nil, core.Explain(err, "list the projects of %s", g.owner)
		}
		list := data.owner().ProjectsV2
		for _, node := range list.Nodes {
			if candidate, ok := node.toCandidate(g.site, g.owner, g.ownerType); ok && ghHintMatches(node, hint) {
				candidates = append(candidates, candidate)
			}
		}
		if !list.PageInfo.HasNextPage || list.PageInfo.EndCursor == "" {
			return candidates, nil
		}
		variables["after"] = list.PageInfo.EndCursor
	}
	return nil, core.Errorf(core.ErrUnavailable, "list the projects of %s", g.owner).
		WithWhy("the provider offered more than %d pages of projects", core.MaxPages).
		WithFix("name the project in configuration rather than discovering it")
}

// ghHintMatches reports whether a project answers the hint it was given. A
// hint names a project exactly — by title or by number — rather than
// searching for one: GitHub's own project search is fuzzy, and a setup step
// that silently offers a near miss is worse than one that offers nothing.
func ghHintMatches(node ghProjectNode, hint map[string]string) bool {
	for key, value := range hint {
		if value == "" {
			continue
		}
		switch key {
		case "name", "title":
			if !strings.EqualFold(node.Title, value) {
				return false
			}
		case "number":
			if strconv.Itoa(node.Number) != strings.TrimSpace(value) {
				return false
			}
		}
	}
	return true
}

// ParseRef turns a project's web address into a reference flow can record.
//
// The reference's id is the project's number, which is what configuration
// holds and what a human can check against the address bar; the opaque node
// id the API works in is resolved from it when a query needs one. The owner
// in the address must be the owner flow is configured for, because a number
// means a different board under a different login and recording one against
// the other would be silent and wrong.
func (g *GitHubProjects) ParseRef(raw string) (core.Ref, error) {
	refuse := func(why string, args ...any) (core.Ref, error) {
		return core.Ref{}, core.Errorf(core.ErrValidation, "read %q as a project", raw).
			WithWhy(why, args...).
			WithFix("paste the project's web address, e.g. %s", ghProjectURL(g.site, g.owner, g.ownerType, 3))
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
	if len(segments) < 4 || segments[2] != "projects" {
		return refuse("its path is not /<users|orgs>/<login>/projects/<number>")
	}
	ownerType, ok := ghOwnerType(segments[0])
	if !ok {
		return refuse("%q names neither a user nor an organisation", segments[0])
	}
	if ownerType != g.ownerType || !strings.EqualFold(segments[1], g.owner) {
		return refuse("it is the project of %s %q, and flow is configured for %s %q",
			ownerType, segments[1], g.ownerType, g.owner)
	}
	number, err := strconv.Atoi(segments[3])
	if err != nil || number <= 0 {
		return refuse("%q is not a project number", segments[3])
	}
	return core.Ref{
		What: core.ItemTrackerProject,
		ID:   strconv.Itoa(number),
		URL:  ghProjectURL(g.site, g.owner, g.ownerType, number),
	}, nil
}

// Observe returns the current state of one desired item, canonically (spec 06
// §2).
func (g *GitHubProjects) Observe(ctx context.Context, what string) (core.Observation, error) {
	observation := core.Observation{What: what}
	switch what {
	case core.ItemTrackerProject:
		project, err := g.project(ctx)
		if err != nil {
			return core.Observation{}, core.Explain(err, "read the project %s", g.Slug())
		}
		if !project.Exists {
			return observation, nil
		}
		observation.Exists = true
		observation.Items = []core.Item{project.item()}
	case core.ItemTrackerStatusOptions:
		project, err := g.project(ctx)
		if err != nil {
			return core.Observation{}, core.Explain(err, "read the columns of %s", g.Slug())
		}
		if !project.Exists || project.Status == nil {
			return observation, nil
		}
		observation.Exists = true
		observation.Items = project.Status.items()
	case core.ItemTrackerWIP, core.ItemTrackerExpedite:
		// A column's limit and the expedite view are per-view settings
		// the API does not report at all, which the declaration says by
		// refusing to verify them. An observation that does not exist
		// is the truthful answer, and the resolution turns it into the
		// compensation or the attestation the declaration asks for
		// (spec 05 §4).
		return observation, nil
	default:
		return core.Observation{}, notYet("observe %s", what)
	}
	return observation.Canonical(), nil
}

// project reads the board and its Status field in one query.
func (g *GitHubProjects) project(ctx context.Context) (ghProject, error) {
	if err := g.numbered("read the project of %s", g.owner); err != nil {
		return ghProject{}, err
	}
	data, err := core.GraphQL[ghProjectResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghProjectQuery(g.ownerType),
		Variables: map[string]any{"login": g.owner, "number": g.number},
	}, ghSentinel)
	if err != nil {
		return ghProject{}, err
	}
	project := data.owner().ProjectV2
	if project == nil {
		return ghProject{}, nil
	}
	project.Exists = true
	return *project, nil
}

// Apply makes one change to the board. Plan item 3.10.
func (g *GitHubProjects) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	return core.ApplyResult{}, notYet("change %s", c.What)
}

// Ticket returns one ticket by key.
//
// A ticket is an issue, and its column is where this board holds it. An issue
// nobody has put on the board is still a ticket — it has a key, a title and
// acceptance criteria a check can read — and simply sits in no column, which
// is what spec 04 §7 has to be able to say about one.
func (g *GitHubProjects) Ticket(ctx context.Context, key string) (core.Ticket, error) {
	ticket, _, err := g.ticket(ctx, key)
	return ticket, err
}

// ticket reads one issue and the item holding it on this board, in one query:
// the issue→item lookup a move needs is the same document the ticket is built
// from, so reading a ticket costs one query however it is used.
func (g *GitHubProjects) ticket(ctx context.Context, key string) (core.Ticket, ghItemFields, error) {
	owner, name, number, err := ghParseKey(key, g.repo)
	if err != nil {
		return core.Ticket{}, ghItemFields{}, err
	}
	data, err := core.GraphQL[ghIssueResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghIssueItemQuery,
		Variables: map[string]any{"owner": owner, "name": name, "number": number},
	}, ghSentinel)
	if err != nil {
		return core.Ticket{}, ghItemFields{}, core.Explain(err, "read the ticket %s", key)
	}
	issue := data.issue()
	if issue == nil {
		return core.Ticket{}, ghItemFields{}, core.Errorf(core.ErrNotFound, "read the ticket %s", key).
			WithWhy("%s/%s has no issue %d", owner, name, number)
	}
	item, _ := issue.on(g.number)
	return ghTicket(issue.ghIssue, item, g.repo), item, nil
}

// Tickets returns the tickets matching a query.
//
// Keys are read one at a time, because an issue is addressed by its own
// repository and number and two keys need not share a repository at all. A key
// nobody holds is left out rather than failing the query: a caller asking
// about several tickets is asking what is there, and one that has been deleted
// is an answer rather than an error.
//
// Everything else is the board, read a page at a time. The column and the
// window are applied here rather than asked of the provider: Projects v2 has
// no server-side filter for either, and a filter flow applied to a page it had
// already paid for costs nothing more.
func (g *GitHubProjects) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	if len(q.Keys) > 0 {
		tickets := make([]core.Ticket, 0, len(q.Keys))
		for _, key := range q.Keys {
			ticket, err := g.Ticket(ctx, key)
			switch {
			case errors.Is(err, core.ErrNotFound):
				continue
			case err != nil:
				return nil, err
			}
			tickets = append(tickets, ticket)
		}
		return tickets, nil
	}
	if err := g.numbered("read the tickets of %s", g.owner); err != nil {
		return nil, err
	}
	variables := map[string]any{"login": g.owner, "number": g.number, "first": ghItemPageSize}
	var tickets []core.Ticket
	for page := 0; page < core.MaxPages; page++ {
		data, err := core.GraphQL[ghItemsResult](ctx, g.client, g.request(), core.GraphQLQuery{
			Query:     ghItemsQuery(g.ownerType),
			Variables: variables,
		}, ghSentinel)
		if err != nil {
			return nil, core.Explain(err, "read the tickets of %s", g.Slug())
		}
		items := data.owner().items()
		for _, item := range items.Nodes {
			// A draft item or a pull request on the board is not a
			// ticket: it has no key to refer to it by and nothing
			// a check can read.
			if item.Content == nil || item.Content.Number <= 0 {
				continue
			}
			ticket := ghTicket(*item.Content, item.ghItemFields, g.repo)
			if q.Column != "" && ticket.ColumnID != q.Column {
				continue
			}
			if !q.UpdatedSince.IsZero() && ticket.UpdatedAt.Before(q.UpdatedSince) {
				continue
			}
			tickets = append(tickets, ticket)
		}
		if !items.PageInfo.HasNextPage || items.PageInfo.EndCursor == "" {
			slices.SortStableFunc(tickets, func(a, b core.Ticket) int {
				return b.UpdatedAt.Compare(a.UpdatedAt)
			})
			if q.Limit > 0 && len(tickets) > q.Limit {
				tickets = tickets[:q.Limit]
			}
			return tickets, nil
		}
		variables["after"] = items.PageInfo.EndCursor
	}
	return nil, core.Errorf(core.ErrUnavailable, "read the tickets of %s", g.Slug()).
		WithWhy("the board offered more than %d pages of items", core.MaxPages).
		WithFix("split the board: flow reads every item of it on every run")
}

// Move takes a ticket to another column.
//
// It reads before it writes, and a ticket already in that column is left
// alone: a move that sent the mutation anyway would touch the item every time
// doctor ran, and spec 06 §4 counts a write that changed nothing as a change
// that happened.
//
// An issue that is not on the board is not moved onto it. Adding an issue to a
// board is a different act with a different consequence — the board's
// automations run on it — and a transition is not the place to decide that it
// should happen.
func (g *GitHubProjects) Move(ctx context.Context, key string, to core.ColumnID) error {
	if to == "" {
		return core.Errorf(core.ErrValidation, "move %s", key).
			WithWhy("no column was named").
			WithFix("name the column to move the ticket to")
	}
	if err := g.numbered("move %s", key); err != nil {
		return err
	}
	project, err := g.project(ctx)
	if err != nil {
		return core.Explain(err, "move %s to %s", key, to)
	}
	switch {
	case !project.Exists:
		return core.Errorf(core.ErrNotFound, "move %s to %s", key, to).
			WithWhy("%s is not there", g.Slug()).
			WithFix("set tracker.project to the number in the project's web address")
	case project.Status == nil:
		return core.Errorf(core.ErrNotFound, "move %s to %s", key, to).
			WithWhy("%s has no %s field, so it has no columns", g.Slug(), ghStatusFieldName)
	}
	if !project.Status.has(string(to)) {
		return core.Errorf(core.ErrValidation, "move %s to %s", key, to).
			WithWhy("%s has no such column", g.Slug()).
			WithFix("the columns of %s are: %s", g.Slug(), strings.Join(project.Status.names(), ", "))
	}
	_, item, err := g.ticket(ctx, key)
	if err != nil {
		return err
	}
	if item.ID == "" {
		return core.Errorf(core.ErrNotFound, "move %s to %s", key, to).
			WithWhy("%s is not on %s", key, g.Slug()).
			WithFix("add the issue to the project, which flow will not do on a transition's behalf")
	}
	if item.optionID() == string(to) {
		return nil
	}
	if _, err := core.GraphQL[ghMoveResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query: ghMoveMutation,
		Variables: map[string]any{
			"project": project.ID,
			"item":    item.ID,
			"field":   project.Status.ID,
			"option":  string(to),
		},
	}, ghSentinel); err != nil {
		return core.Explain(err, "move %s to %s", key, to)
	}
	return nil
}

// numbered refuses an act that needs a board when no board number is set.
// Setup runs before there is one, and saying which setting is missing is more
// use than a query against project zero.
func (g *GitHubProjects) numbered(format string, args ...any) error {
	if g.number != 0 {
		return nil
	}
	return core.Errorf(core.ErrValidation, format, args...).
		WithWhy("no project number is set").
		WithFix("set tracker.project to the number in the project's web address")
}

// CreateFollowUp files a ticket for work flow cannot do. Plan item 3.11.
func (g *GitHubProjects) CreateFollowUp(ctx context.Context, f core.FollowUp) (core.Ticket, error) {
	return core.Ticket{}, notYet("file the follow-up %q", f.Title)
}

// FindFollowUp returns the ticket carrying a marker. Plan item 3.11.
func (g *GitHubProjects) FindFollowUp(ctx context.Context, m core.Marker) (*core.Ticket, error) {
	return nil, notYet("find the follow-up marked %s", m)
}

// Comment writes a marked comment on a ticket. Plan item 3.11.
func (g *GitHubProjects) Comment(ctx context.Context, key string, m core.Marker, body string) error {
	return notYet("comment on %s", key)
}

// Close closes a ticket, recording why. Plan item 3.11.
func (g *GitHubProjects) Close(ctx context.Context, key string, reason string) error {
	return notYet("close %s", key)
}

// Reopen reopens a closed ticket, recording why. Plan item 3.11.
func (g *GitHubProjects) Reopen(ctx context.Context, key string, reason string) error {
	return notYet("reopen %s", key)
}

// LinkedKeys returns the ticket keys a pull request is for.
//
// GitHub has a structured link of its own — closingIssuesReferences, which the
// forge adapter reads — so there is nothing for the tracker to infer. A pull
// request that names no issue links to none: flow never applies a pattern to a
// branch name or a title to guess one (P12, spec 04 §3).
func (g *GitHubProjects) LinkedKeys(pr core.PullRequest) []string {
	return slices.Clone(pr.ClosingIssues)
}

// request is the GraphQL request every query starts from: the endpoint, and
// the headers the adapter puts on everything it sends.
func (g *GitHubProjects) request() core.Request {
	return g.decorate(core.Request{Path: g.graphQL})
}

// do sends one request with GitHub's media type and API version on it.
func (g *GitHubProjects) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return g.client.Do(ctx, g.decorate(r))
}

// decorate returns the request with GitHub's media type and API version on it.
func (g *GitHubProjects) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	header.Set("Accept", ghAccept)
	header.Set("X-GitHub-Api-Version", ghAPIVersion)
	r.Header = header
	return r
}

// notYet is the refusal of a method whose plan item has not landed. It names
// itself so that a caller reaching it says what it wanted rather than failing
// blankly.
func notYet(format string, args ...any) error {
	return core.Errorf(core.ErrUnsupported, format, args...).
		WithWhy("the github-projects adapter does not do that yet").
		WithFix("this is a gap in flow, not in your configuration; see the plan")
}

// GitHubProjects is a tracker.
var _ core.Tracker = (*GitHubProjects)(nil)
