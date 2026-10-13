// Package tracker holds the ticket-system adapters: GitHub Projects v2 in v1,
// Jira Cloud and Linear in phase 7. Each depends on core alone, so that
// nothing about a provider leaks into the engine (spec 01 §2, spec 11).
package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

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

	// mu guards filed, which is what flow has put in the tracker during
	// this process. GitHub's search index lags behind its writes by
	// seconds to minutes, and a doctor run that filed a follow-up and
	// then asked whether one existed would otherwise file a second
	// (spec 07 §4).
	mu    sync.Mutex
	filed []ghFiled
}

// ghFiled is one follow-up this process filed: the marker it carries and the
// ticket it became.
type ghFiled struct {
	marker core.Marker
	ticket core.Ticket
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
	case core.ItemTrackerLabels:
		labels, err := g.labels(ctx)
		if err != nil {
			return core.Observation{}, core.Explain(err, "read the labels of %s", g.repo)
		}
		// A repository always has labels — GitHub gives a new one a
		// starter set — so the observation exists even when the slice
		// is empty, which is a repository somebody has emptied rather
		// than one flow could not read.
		observation.Exists = true
		observation.Items = make([]core.Item, 0, len(labels))
		for _, label := range labels {
			observation.Items = append(observation.Items, label.item())
		}
	case core.ItemTrackerWIP, core.ItemTrackerExpedite:
		// A column's limit and the expedite view are per-view settings
		// the API does not report at all, which the declaration says by
		// refusing to verify them. An observation that does not exist
		// is the truthful answer, and the resolution turns it into the
		// compensation or the attestation the declaration asks for
		// (spec 05 §4).
		return observation, nil
	default:
		if strings.HasPrefix(what, core.NativeItemPrefix) {
			// Whether a built-in workflow is on is not something the
			// Projects API will say, so at setup there is nothing to
			// observe and a person's word is what settles it. What
			// actually happened to the cards is the live-health
			// audit's question, not this one (spec 07 §6).
			return observation, nil
		}
		return core.Observation{}, ghNoSuchItem("observe", what)
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

// Apply makes one change to the board, or to the labels of the repository
// whose issues it holds.
//
// The only thing flow changes about a board itself is its columns, and GitHub
// has no way to change one. The options of a single-select field are written
// whole: the mutation takes the set the field should end up with, and an
// option it did not recognise is a new option with a new id, which every item
// pointing at the old one loses. So the whole dance lives behind this one
// call — read what column every item is in, rewrite the set, put back
// whatever the provider cleared, and read the board again to prove it
// (spec 06 §4).
func (g *GitHubProjects) Apply(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	switch c.What {
	case core.ItemTrackerStatusOptions:
		return g.editColumns(ctx, c)
	case core.ItemTrackerLabels:
		return g.editLabel(ctx, c)
	default:
		return core.ApplyResult{}, ghNoSuchItem("change", c.What)
	}
}

// ghNoSuchItem refuses an item this adapter does not own. A tracker is asked
// only about tracker items, so reaching this is a caller's mistake rather
// than a gap in the adapter: every item the declaration claims is answered
// above, including the two it answers by declining to verify.
func ghNoSuchItem(verb, what string) error {
	return core.Errorf(core.ErrValidation, "%s %s", verb, what).
		WithWhy("a tracker has nothing called that").
		WithFix("ask the tracker about one of %s", strings.Join(ghTrackerItems(), ", "))
}

// ghTrackerItems is every item a tracker answers for, in the order spec 03
// declares them.
func ghTrackerItems() []string {
	return []string{
		core.ItemTrackerProject,
		core.ItemTrackerStatusOptions,
		core.ItemTrackerLabels,
		core.ItemTrackerWIP,
		core.ItemTrackerExpedite,
	}
}

// editColumns carries out one change to the board's columns.
func (g *GitHubProjects) editColumns(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if err := g.numbered("%s the column %q", c.Op, c.Item.Name); err != nil {
		return core.ApplyResult{}, err
	}
	project, err := g.project(ctx)
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "%s the column %q of %s", c.Op, c.Item.Name, g.Slug())
	}
	switch {
	case !project.Exists:
		return core.ApplyResult{}, core.Errorf(core.ErrNotFound, "%s the column %q", c.Op, c.Item.Name).
			WithWhy("%s is not there", g.Slug()).
			WithFix("set tracker.project to the number in the project's web address")
	case project.Status == nil:
		return core.ApplyResult{}, core.Errorf(core.ErrNotFound, "%s the column %q", c.Op, c.Item.Name).
			WithWhy("%s has no %s field, so it has no columns", g.Slug(), ghStatusFieldName).
			WithFix("add a %s field to the board: a board's fields are the team's own and flow does not create them", ghStatusFieldName)
	}

	edit, err := ghColumnEditOf(*project.Status, c, g.Slug())
	if err != nil {
		return core.ApplyResult{}, err
	}
	if !edit.rewrites {
		return core.ApplyResult{Change: c, Item: edit.standing(), Note: edit.note}, nil
	}
	// The retreat spec 06 §4 asks for, and it is taken here rather than
	// when the plan was made: how big the board is has nothing to do with
	// what the configuration asks for, and a plan that changed with the
	// number of cards on a board would not be the same plan twice.
	if project.Items.TotalCount > ghOptionEditLimit {
		return core.ApplyResult{Change: c, FollowUp: fmt.Sprintf(
			"%s holds %d items; rewriting a board's columns clears the column of every item on it, and flow puts back at most %d",
			g.Slug(), project.Items.TotalCount, ghOptionEditLimit)}, nil
	}

	before, err := g.columnValues(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if held := ghHolders(before, edit.removing()); held > 0 {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "remove the column %q from %s", edit.subject.Name, g.Slug()).
			WithWhy("%d items are in it, and nothing flow can do afterwards would put them back", held).
			WithFix("move them to another column first")
	}

	if _, err := core.GraphQL[ghFieldResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghOptionsMutation,
		Variables: map[string]any{"field": project.Status.ID, "options": ghOptionInputs(edit.options)},
	}, ghSentinel); err != nil {
		return core.ApplyResult{}, core.Explain(err, "rewrite the columns of %s", g.Slug())
	}

	after, err := g.project(ctx)
	if err != nil {
		return core.ApplyResult{}, core.Explain(err, "read back the columns of %s", g.Slug())
	}
	if !after.Exists || after.Status == nil {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "rewrite the columns of %s", g.Slug()).
			WithWhy("the board has no %s field after the rewrite", ghStatusFieldName)
	}
	item, err := edit.verify(*after.Status)
	if err != nil {
		return core.ApplyResult{}, err
	}
	restored, err := g.restore(ctx, after, before, edit.renames)
	if err != nil {
		return core.ApplyResult{}, err
	}
	return core.ApplyResult{Change: c, Item: item, Note: edit.note + ghRestoreNote(restored, len(before))}, nil
}

// columnValues reads every item of the board and the column it is in.
//
// This is the snapshot the rewrite is put back from, and it is taken over
// every item rather than every ticket: a draft and a pull request sit in a
// column too, and clearing one of those is as much a loss as clearing an
// issue's. An item in no column is left out — there is nothing to put back.
func (g *GitHubProjects) columnValues(ctx context.Context) ([]ghItemValue, error) {
	variables := map[string]any{"login": g.owner, "number": g.number, "first": ghItemPageSize}
	var values []ghItemValue
	for page := 0; page < core.MaxPages; page++ {
		data, err := core.GraphQL[ghItemsResult](ctx, g.client, g.request(), core.GraphQLQuery{
			Query:     ghItemValuesQuery(g.ownerType),
			Variables: variables,
		}, ghSentinel)
		if err != nil {
			return nil, core.Explain(err, "read what column each item of %s is in", g.Slug())
		}
		items := data.owner().items()
		for _, item := range items.Nodes {
			if item.ID == "" || item.Status == nil || item.Status.Name == "" {
				continue
			}
			values = append(values, ghItemValue{ID: item.ID, Name: item.Status.Name})
		}
		if !items.PageInfo.HasNextPage || items.PageInfo.EndCursor == "" {
			return values, nil
		}
		variables["after"] = items.PageInfo.EndCursor
	}
	return nil, core.Errorf(core.ErrUnavailable, "read what column each item of %s is in", g.Slug()).
		WithWhy("the board offered more than %d pages of items", core.MaxPages).
		WithFix("split the board: flow reads every item of it before it touches a column")
}

// restore puts every item back in the column it was in, and returns how many
// had to be put back.
//
// It reads before it writes, because the provider may well have kept the
// values: an option sent back with its own id keeps its identity, and
// rewriting 500 items that never moved would touch every card on the board
// every time flow added a column. What was put back is then read again, so
// that "restored" is something flow saw rather than something it attempted.
func (g *GitHubProjects) restore(ctx context.Context, project ghProject, before []ghItemValue, renames map[string]string) (int, error) {
	now, err := g.columnValues(ctx)
	if err != nil {
		return 0, err
	}
	held := ghValueIndex(now)
	cleared := 0
	for _, was := range before {
		want := ghOr(renames[was.Name], was.Name)
		if held[was.ID] == want {
			continue
		}
		cleared++
		option, ok := project.Status.find(want)
		if !ok {
			// Nothing to put it back into. The verification below
			// is what reports it, so that one answer covers every
			// way an item can fail to come back.
			continue
		}
		if err := g.setColumn(ctx, project, was.ID, option.ID); err != nil {
			return 0, core.Explain(err, "put an item of %s back in %q", g.Slug(), want)
		}
	}
	if cleared == 0 {
		return 0, nil
	}

	now, err = g.columnValues(ctx)
	if err != nil {
		return 0, err
	}
	held = ghValueIndex(now)
	lost := 0
	for _, was := range before {
		if held[was.ID] != ghOr(renames[was.Name], was.Name) {
			lost++
		}
	}
	if lost > 0 {
		return cleared, core.Errorf(core.ErrConflict, "put the items of %s back in their columns", g.Slug()).
			WithWhy("the rewrite cleared %d item columns and %d of them are still empty", cleared, lost).
			WithFix("the board needs a person: flow made the column change, but %d items no longer say which column they were in", lost)
	}
	return cleared, nil
}

// setColumn puts one item in one column.
func (g *GitHubProjects) setColumn(ctx context.Context, project ghProject, item, option string) error {
	_, err := core.GraphQL[ghMoveResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query: ghMoveMutation,
		Variables: map[string]any{
			"project": project.ID,
			"item":    item,
			"field":   project.Status.ID,
			"option":  option,
		},
	}, ghSentinel)
	return err
}

// ghColumnEdit is what one change does to a board's columns: the option set
// the field is to end up with, and what a snapshot taken before the rewrite
// means afterwards.
type ghColumnEdit struct {
	// options is the whole set, as the field should stand afterwards.
	options []ghStatusOption
	// subject is the option the change is about, as it stood before. It
	// is empty for a column being added, which stood nowhere.
	subject ghStatusOption
	// want is what the option is called afterwards, empty for a removal.
	want string
	// renames maps a column's name before the rewrite to its name after,
	// which is how an item whose column was renamed is put back in it.
	renames map[string]string
	// note says what happened, for the log.
	note string
	// rewrites says whether the option set has to be written at all. A
	// change asking for what the board already says does not.
	rewrites bool
}

// ghColumnEditOf works out the rewrite one change asks for.
//
// Adding a column that is there and changing or removing one that is not are
// the tracker contract's own answers — ErrConflict and ErrNotFound — because
// the planner only ever proposes a change against what it observed, and a
// board that no longer looks like that is news rather than a no-op.
func ghColumnEditOf(field ghStatusField, c core.Change, slug string) (ghColumnEdit, error) {
	refuse := func(sentinel error, why string, args ...any) (ghColumnEdit, error) {
		return ghColumnEdit{}, core.Errorf(sentinel, "%s the column %q of %s", c.Op, c.Item.Name, slug).
			WithWhy(why, args...)
	}
	switch c.Op {
	case core.ChangeCreate:
		if c.Item.Name == "" {
			return refuse(core.ErrValidation, "a column has to be called something")
		}
		if _, taken := field.find(c.Item.Name); taken {
			return refuse(core.ErrConflict, "%s already has a column called %q", slug, c.Item.Name)
		}
		added := ghStatusOption{Name: c.Item.Name, Color: ghDefaultOptionColour}
		return ghColumnEdit{
			options:  append(slices.Clone(field.Options), added),
			want:     added.Name,
			note:     fmt.Sprintf("added the column %q", added.Name),
			rewrites: true,
		}, nil

	case core.ChangeUpdate:
		standing, ok := field.identify(c.Item)
		if !ok {
			return refuse(core.ErrNotFound, "%s has no such column", slug)
		}
		if c.Item.Name == "" {
			return refuse(core.ErrValidation, "a column has to be called something")
		}
		if standing.Name == c.Item.Name {
			return ghColumnEdit{
				subject: standing,
				want:    standing.Name,
				note:    fmt.Sprintf("the column %q is already as it should be", standing.Name),
			}, nil
		}
		if _, taken := field.find(c.Item.Name); taken {
			return refuse(core.ErrConflict, "%s already has a column called %q", slug, c.Item.Name)
		}
		options := slices.Clone(field.Options)
		for i := range options {
			if options[i].ID == standing.ID {
				options[i].Name = c.Item.Name
			}
		}
		return ghColumnEdit{
			options:  options,
			subject:  standing,
			want:     c.Item.Name,
			renames:  map[string]string{standing.Name: c.Item.Name},
			note:     fmt.Sprintf("renamed the column %q to %q", standing.Name, c.Item.Name),
			rewrites: true,
		}, nil

	case core.ChangeDelete:
		standing, ok := field.identify(c.Item)
		if !ok {
			return refuse(core.ErrNotFound, "%s has no such column", slug)
		}
		options := slices.DeleteFunc(slices.Clone(field.Options), func(o ghStatusOption) bool {
			return o.ID == standing.ID
		})
		return ghColumnEdit{
			options:  options,
			subject:  standing,
			note:     fmt.Sprintf("removed the column %q", standing.Name),
			rewrites: true,
		}, nil

	default:
		return refuse(core.ErrValidation, "%q is not something flow does to a column", c.Op)
	}
}

// removing is the name of the column the edit takes away, empty when it takes
// none away.
func (e ghColumnEdit) removing() string {
	if e.want != "" {
		return ""
	}
	return e.subject.Name
}

// standing is the option a change that needed no rewrite left alone.
func (e ghColumnEdit) standing() core.Item {
	return core.Item{ID: e.subject.ID, Name: e.subject.Name}
}

// verify reads the rewrite back off the board: the column the change asked
// for is there, under the name it asked for, or the one it took away is gone.
// What comes back is the item the lock records, with the id the board gave it
// — which for every option of a rewritten field is a new one.
func (e ghColumnEdit) verify(field ghStatusField) (core.Item, error) {
	if e.want == "" {
		if _, still := field.find(e.subject.Name); still {
			return core.Item{}, core.Errorf(core.ErrConflict, "remove the column %q", e.subject.Name).
				WithWhy("the board still has it after the rewrite")
		}
		return core.Item{ID: e.subject.ID, Name: e.subject.Name}, nil
	}
	option, ok := field.find(e.want)
	if !ok {
		return core.Item{}, core.Errorf(core.ErrConflict, "set the column %q", e.want).
			WithWhy("the board has no such column after the rewrite").
			WithFix("the rewrite was accepted and did something else: look at the board before running flow again")
	}
	return core.Item{ID: option.ID, Name: option.Name}, nil
}

// ghHolders counts the items in a column, by name. A name nobody asked about
// holds nothing.
func ghHolders(values []ghItemValue, name string) int {
	if name == "" {
		return 0
	}
	held := 0
	for _, value := range values {
		if value.Name == name {
			held++
		}
	}
	return held
}

// ghValueIndex is the column each item is in, by item id.
func ghValueIndex(values []ghItemValue) map[string]string {
	index := make(map[string]string, len(values))
	for _, value := range values {
		index[value.ID] = value.Name
	}
	return index
}

// ghRestoreNote says what the rewrite cost the items on the board, which is
// the half of the log line a person actually worries about.
func ghRestoreNote(restored, held int) string {
	if restored == 0 {
		return fmt.Sprintf("; every one of the %d item columns survived it", held)
	}
	return fmt.Sprintf("; %d of %d item columns were cleared by it and put back", restored, held)
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
	target, found := project.Status.column(to)
	if !found {
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
	if item.optionID() == target.ID {
		return nil
	}
	if err := g.setColumn(ctx, project, item.ID, target.ID); err != nil {
		return core.Explain(err, "move %s to %s", key, to)
	}
	return nil
}

// repoed refuses an act that needs a repository when none is configured.
// Labels and follow-ups are written to a repository, not to a board: a
// project can hold issues from many, so there is no one repository to infer
// from the board itself (spec 11 §2).
func (g *GitHubProjects) repoed(format string, args ...any) error {
	if g.repo != "" {
		return nil
	}
	return core.Errorf(core.ErrValidation, format, args...).
		WithWhy("no repository is set, and a label belongs to a repository rather than to a board").
		WithFix("set repo to the owner/name whose issues the project holds")
}

// labels reads every label the repository has, a page at a time.
func (g *GitHubProjects) labels(ctx context.Context) ([]ghLabel, error) {
	if err := g.repoed("read the labels of %s", g.Slug()); err != nil {
		return nil, err
	}
	var all []ghLabel
	next := core.Request{
		Path:  "repos/" + g.repo + "/labels",
		Query: url.Values{"per_page": {strconv.Itoa(ghLabelPageSize)}},
	}
	for {
		var page []ghLabel
		next.Out = &page
		res, err := g.do(ctx, next)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		link, ok := res.Links["next"]
		if !ok || len(page) == 0 {
			return all, nil
		}
		next = core.Request{Path: link}
	}
}

// editLabel carries out one change to the repository's labels.
//
// GitHub identifies a label by its name in the path, so a rename is the one
// edit that has to say both: the name it is reaching for and the name it
// wants. The node id comes back on every answer and is what the lock records,
// because a name is what a rename changes.
func (g *GitHubProjects) editLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	if err := g.repoed("%s the label %q", c.Op, c.Item.Name); err != nil {
		return core.ApplyResult{}, err
	}
	switch c.Op {
	case core.ChangeCreate:
		return g.createLabel(ctx, c)
	case core.ChangeUpdate:
		return g.updateLabel(ctx, c)
	case core.ChangeDelete:
		return g.deleteLabel(ctx, c)
	default:
		return core.ApplyResult{}, core.Errorf(core.ErrValidation, "%s the label %q", c.Op, c.Item.Name).
			WithWhy("flow has no such operation").
			WithFix("use %s, %s or %s", core.ChangeCreate, core.ChangeUpdate, core.ChangeDelete)
	}
}

// createLabel adds a label the repository has not got.
//
// It reads the labels first. GitHub answers a duplicate with 422, the same
// status it uses for a name it will not take, and the two are a conflict and
// a validation failure respectively — but the per-error code that tells them
// apart is in a body the client does not hand back with the error. Reading
// first is what makes the conflict apply relies on (spec 06 §4) reliable, and
// it is what this adapter already does before every other write.
func (g *GitHubProjects) createLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	all, err := g.labels(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if slices.ContainsFunc(all, func(l ghLabel) bool { return strings.EqualFold(l.Name, c.Item.Name) }) {
		return core.ApplyResult{}, core.Errorf(core.ErrConflict, "create the label %q", c.Item.Name).
			WithWhy("%s already has a label with that name", g.repo).
			WithFix("flow will update it instead")
	}
	var made ghLabel
	if _, err := g.do(ctx, core.Request{
		Method: http.MethodPost,
		Path:   "repos/" + g.repo + "/labels",
		Body: ghLabelBody{
			Name:        c.Item.Name,
			Color:       ghLabelColourOf(c.Item),
			Description: ghLabelDescriptionOf(c.Item),
		},
		Out: &made,
	}); err != nil {
		return core.ApplyResult{}, core.Explain(err, "create the label %q in %s", c.Item.Name, g.repo)
	}
	return core.ApplyResult{
		Change: c,
		Item:   made.item(),
		Note:   fmt.Sprintf("added the label %q to %s", made.Name, g.repo),
	}, nil
}

// updateLabel edits a label the repository has, renaming it when the change
// carries a name the label it names has not got.
func (g *GitHubProjects) updateLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	// The change names the label it wants to exist; which label that is
	// comes from the id the plan copied off the observation, because the
	// name may be the very thing being changed.
	current, err := g.labelOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	// Only what the change names is sent. flow has no opinion about a
	// label's colour or description, so an update that carries neither must
	// leave both as they are: a rename that quietly repainted a label the
	// team had chosen the colour of would be a change nobody asked for.
	body := ghLabelBody{
		Color:       ghLabelColour(c.Item),
		Description: ghLabelDescriptionOf(c.Item),
	}
	if current.Name != c.Item.Name {
		body.NewName = c.Item.Name
	}
	var edited ghLabel
	if _, err := g.do(ctx, core.Request{
		Method: http.MethodPatch,
		Path:   "repos/" + g.repo + "/labels/" + url.PathEscape(current.Name),
		Body:   body,
		Out:    &edited,
	}); err != nil {
		return core.ApplyResult{}, core.Explain(err, "update the label %q in %s", current.Name, g.repo)
	}
	note := fmt.Sprintf("updated the label %q of %s", edited.Name, g.repo)
	if body.NewName != "" {
		note = fmt.Sprintf("renamed the label %q of %s to %q", current.Name, g.repo, edited.Name)
	}
	return core.ApplyResult{Change: c, Item: edited.item(), Note: note}, nil
}

// deleteLabel removes a label. GitHub takes it off every issue that carried
// it, which is why only a label the lock records flow as having created is
// ever proposed for removal (spec 06 §4).
func (g *GitHubProjects) deleteLabel(ctx context.Context, c core.Change) (core.ApplyResult, error) {
	current, err := g.labelOf(ctx, c.Item)
	if err != nil {
		return core.ApplyResult{}, err
	}
	if _, err := g.do(ctx, core.Request{
		Method: http.MethodDelete,
		Path:   "repos/" + g.repo + "/labels/" + url.PathEscape(current.Name),
	}); err != nil {
		return core.ApplyResult{}, core.Explain(err, "delete the label %q from %s", current.Name, g.repo)
	}
	return core.ApplyResult{
		Change: c,
		Item:   current.item(),
		Note:   fmt.Sprintf("removed the label %q from %s", current.Name, g.repo),
	}, nil
}

// labelOf finds the label a change is about: by node id when the change
// carries one, because that survives a rename, and by name otherwise.
func (g *GitHubProjects) labelOf(ctx context.Context, want core.Item) (ghLabel, error) {
	all, err := g.labels(ctx)
	if err != nil {
		return ghLabel{}, err
	}
	if want.ID != "" {
		if at := slices.IndexFunc(all, func(l ghLabel) bool { return l.NodeID == want.ID }); at >= 0 {
			return all[at], nil
		}
	}
	if at := slices.IndexFunc(all, func(l ghLabel) bool { return l.Name == want.Name }); at >= 0 {
		return all[at], nil
	}
	return ghLabel{}, core.Errorf(core.ErrNotFound, "find the label %q in %s", want.Name, g.repo).
		WithWhy("the repository has no such label").
		WithFix("run flow plan again: the label was there when the plan was made")
}

// ghLabelColour is the colour a change asks for, as GitHub writes one, or
// empty when the change asks for no colour at all.
func ghLabelColour(item core.Item) string {
	return strings.ToLower(strings.TrimPrefix(item.Field(ghLabelColourField), "#"))
}

// ghLabelColourOf is the colour a change asks for, or flow's default, which
// is what a label GitHub's own UI makes gets.
func ghLabelColourOf(item core.Item) string {
	if colour := ghLabelColour(item); colour != "" {
		return colour
	}
	return ghDefaultLabelColour
}

// ghLabelDescriptionOf is the description a change asks for, or nil when it
// says nothing about one. Nil leaves whatever is there: a configuration that
// names a label is not thereby asking for its description to be erased.
func ghLabelDescriptionOf(item core.Item) *string {
	if text := item.Field(ghLabelDescriptionField); text != "" {
		return &text
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

// CreateFollowUp files a ticket for work flow cannot do, or returns the one
// it filed before.
//
// The marker search runs first, every time: a doctor run that filed a second
// ticket for the same check every day would be worse than the thing it was
// reporting (spec 07 §4). The ticket is then put on the board, which is the
// one place flow adds an issue to a project — a follow-up nobody can see on
// the board is a follow-up nobody will do.
func (g *GitHubProjects) CreateFollowUp(ctx context.Context, f core.FollowUp) (core.Ticket, error) {
	if f.Marker.IsZero() {
		return core.Ticket{}, core.Errorf(core.ErrValidation, "file a follow-up").
			WithWhy("it carries no marker, so flow could never find it again").
			WithFix("give the follow-up a marker")
	}
	if err := g.repoed("file the follow-up %q", f.Title); err != nil {
		return core.Ticket{}, err
	}
	if found, err := g.FindFollowUp(ctx, f.Marker); err != nil {
		return core.Ticket{}, err
	} else if found != nil {
		return *found, nil
	}

	owner, name, ok := strings.Cut(g.repo, "/")
	if !ok {
		return core.Ticket{}, core.Errorf(core.ErrValidation, "file the follow-up %q", f.Title).
			WithWhy("%q is not an owner/name repository", g.repo).
			WithFix("set repo to the owner/name whose issues the project holds")
	}
	repo, err := core.GraphQL[ghRepoResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghRepoQuery,
		Variables: map[string]any{"owner": owner, "name": name},
	}, ghSentinel)
	if err != nil {
		return core.Ticket{}, core.Explain(err, "read %s to file the follow-up %q", g.repo, f.Title)
	}
	if repo.Repository == nil {
		return core.Ticket{}, core.Errorf(core.ErrNotFound, "file the follow-up %q", f.Title).
			WithWhy("%s is not there, or the credential cannot see it", g.repo).
			WithFix("check repo and that the credential may read it")
	}

	// A label the repository has not got is left off rather than failing
	// the filing. The labels are how people find flow's follow-ups, not
	// what makes one true, and the item that creates them is a separate
	// change that apply may not have reached yet (spec 06 §3).
	labels := make([]string, 0, len(f.Labels))
	for _, want := range f.Labels {
		for _, have := range repo.Repository.Labels.Nodes {
			if strings.EqualFold(have.Name, want) {
				labels = append(labels, have.ID)
				break
			}
		}
	}
	assignees, err := g.assignee(ctx, f.Assignee)
	if err != nil {
		return core.Ticket{}, err
	}

	filed, err := core.GraphQL[ghCreateIssueResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query: ghCreateIssueMutation,
		Variables: map[string]any{
			"repo":      repo.Repository.ID,
			"title":     f.Title,
			"body":      core.MarkBody(f.Body, f.Marker, true),
			"labels":    labels,
			"assignees": assignees,
		},
	}, ghSentinel)
	if err != nil {
		return core.Ticket{}, core.Explain(err, "file the follow-up %q in %s", f.Title, g.repo)
	}
	if filed.CreateIssue.Issue == nil {
		return core.Ticket{}, core.Errorf(core.ErrUnavailable, "file the follow-up %q", f.Title).
			WithWhy("%s accepted the issue but did not say which one it is", g.Slug())
	}
	ticket := ghTicket(*filed.CreateIssue.Issue, ghItemFields{}, g.repo)
	if err := g.addToBoard(ctx, ticket); err != nil {
		return core.Ticket{}, err
	}
	g.remember(f.Marker, ticket)
	return ticket, nil
}

// assignee turns a login into the id list createIssue takes. A login GitHub
// does not know is left unassigned: the owner comes from configuration
// (spec 07 §4), and a typo there must not stop the follow-up being filed —
// an unassigned ticket somebody can see beats no ticket at all.
func (g *GitHubProjects) assignee(ctx context.Context, login string) ([]string, error) {
	if login == "" {
		return nil, nil
	}
	user, err := core.GraphQL[ghUserResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghUserQuery,
		Variables: map[string]any{"login": strings.TrimPrefix(login, "@")},
	}, ghSentinel)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return nil, core.Explain(err, "find out who %s is", login)
	}
	if user.User == nil {
		return nil, nil
	}
	return []string{user.User.ID}, nil
}

// addToBoard puts a filed follow-up on the project. GitHub answers with the
// item that was already there when the content is already on the board, so
// this is safe to repeat; a board flow has no number for is skipped, because
// setup has not chosen one yet.
func (g *GitHubProjects) addToBoard(ctx context.Context, ticket core.Ticket) error {
	if g.number == 0 {
		return nil
	}
	project, err := g.project(ctx)
	if err != nil {
		return core.Explain(err, "put %s on %s", ticket.Key, g.Slug())
	}
	if !project.Exists {
		return core.Errorf(core.ErrNotFound, "put %s on %s", ticket.Key, g.Slug()).
			WithWhy("the project is not there").
			WithFix("set tracker.project to the number in the project's web address")
	}
	if _, err := core.GraphQL[ghAddItemResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghAddItemMutation,
		Variables: map[string]any{"project": project.ID, "content": ticket.ID},
	}, ghSentinel); err != nil {
		return core.Explain(err, "put %s on %s", ticket.Key, g.Slug())
	}
	return nil
}

// FindFollowUp returns the ticket carrying a marker, or nil.
//
// The search only narrows. GitHub's index decides which issues are worth
// looking at, and then every candidate's own body is read and matched, so an
// index that answers with too much is harmless; the body is the truth and the
// query is a filter over it.
//
// What the index cannot do is answer about an issue filed a moment ago —
// indexing lags by seconds to minutes — so flow also remembers what it filed
// in this process. That is exactly the window in which a second filing would
// happen: one doctor run that files a follow-up and then asks again.
func (g *GitHubProjects) FindFollowUp(ctx context.Context, m core.Marker) (*core.Ticket, error) {
	if m.IsZero() {
		return nil, core.Errorf(core.ErrValidation, "find a follow-up").
			WithWhy("no marker was given, and a marker is the only thing that identifies one").
			WithFix("search for a marker such as %s", core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"))
	}
	if err := g.repoed("find the follow-up marked %s", m); err != nil {
		return nil, err
	}
	if found, ok := g.recall(m); ok {
		return &found, nil
	}
	data, err := core.GraphQL[ghSearchResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghSearchQuery,
		Variables: map[string]any{"query": ghSearchExpression(g.repo, m), "first": ghSearchPage},
	}, ghSentinel)
	if err != nil {
		return nil, core.Explain(err, "find the follow-up marked %s", m)
	}
	for _, issue := range data.Search.Nodes {
		if core.HasMarker(issue.Body, m) {
			ticket := ghTicket(issue, ghItemFields{}, g.repo)
			return &ticket, nil
		}
	}
	return nil, nil
}

// remember records a ticket flow filed, so that a search run before GitHub's
// index has caught up still finds it.
func (g *GitHubProjects) remember(m core.Marker, ticket core.Ticket) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.filed = append(g.filed, ghFiled{marker: m, ticket: ticket})
}

// recall returns a ticket flow filed in this process whose marker the search
// matches. The match is the marker's own, so a search naming fewer fields
// finds what a fuller marker filed (spec 11 §1).
func (g *GitHubProjects) recall(m core.Marker) (core.Ticket, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, filed := range g.filed {
		if m.Matches(filed.marker) {
			return filed.ticket, true
		}
	}
	return core.Ticket{}, false
}

// UpdateFollowUp rewrites the title and body of a ticket flow opened, keeping
// its marker.
//
// The ticket is read first and the marker checked against what is actually
// there, not against what flow filed earlier: an issue number can be reused
// in a fork, a person can edit a body, and the one thing worse than failing
// to publish a report is publishing it over somebody's bug report.
//
// A body that already says this is left alone. GitHub moves `updatedAt` on
// every edit, and a daily report that rewrote itself with identical bytes
// would make the pinned issue look like it had news every morning.
func (g *GitHubProjects) UpdateFollowUp(ctx context.Context, key string, m core.Marker, title, body string) error {
	if m.IsZero() {
		return core.Errorf(core.ErrValidation, "rewrite %s", key).
			WithWhy("no marker was given, and the marker is what says the ticket is flow's").
			WithFix("pass the marker the ticket was filed with")
	}
	ticket, _, err := g.ticket(ctx, key)
	if err != nil {
		return err
	}
	if !core.HasMarker(ticket.Body, m) {
		return core.Errorf(core.ErrValidation, "rewrite %s", key).
			WithWhy("it does not carry %s, so it is not flow's to rewrite", m).
			WithFix("leave it alone, or let flow open its own ticket to keep")
	}
	marked := core.MarkBody(body, m, true)
	if marked == ticket.Body && title == ticket.Title {
		return nil
	}
	if _, err := core.GraphQL[ghMutationResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghUpdateIssueMutation,
		Variables: map[string]any{"issue": ticket.ID, "title": title, "body": marked},
	}, ghSentinel); err != nil {
		return core.Explain(err, "rewrite %s", key)
	}
	return nil
}

// Comment writes a marked comment on a ticket, editing the one already
// carrying that marker rather than adding another.
//
// Six weeks of weekly nudging is one comment that keeps changing, not six
// comments (spec 07 §4), so the comments are read first and the marker
// decides which — if any — is flow's.
func (g *GitHubProjects) Comment(ctx context.Context, key string, m core.Marker, body string) error {
	owner, name, number, err := ghParseKey(key, g.repo)
	if err != nil {
		return err
	}
	issueID, existing, err := g.findComment(ctx, owner, name, number, key, m)
	if err != nil {
		return err
	}
	marked := core.MarkBody(body, m, true)
	if existing != "" {
		if _, err := core.GraphQL[ghMutationResult](ctx, g.client, g.request(), core.GraphQLQuery{
			Query:     ghUpdateCommentMutation,
			Variables: map[string]any{"comment": existing, "body": marked},
		}, ghSentinel); err != nil {
			return core.Explain(err, "edit flow's comment on %s", key)
		}
		return nil
	}
	if _, err := core.GraphQL[ghMutationResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghAddCommentMutation,
		Variables: map[string]any{"subject": issueID, "body": marked},
	}, ghSentinel); err != nil {
		return core.Explain(err, "comment on %s", key)
	}
	return nil
}

// findComment returns the issue's id and the id of the comment carrying the
// marker, empty when there is none. It reads every page: flow's comment is
// the oldest one on a ticket that has been nudged for weeks.
func (g *GitHubProjects) findComment(ctx context.Context, owner, name string, number int, key string, m core.Marker) (string, string, error) {
	var after any
	issueID := ""
	for {
		data, err := core.GraphQL[ghCommentsResult](ctx, g.client, g.request(), core.GraphQLQuery{
			Query: ghCommentsQuery,
			Variables: map[string]any{
				"owner": owner, "name": name, "number": number,
				"first": ghCommentPage, "after": after,
			},
		}, ghSentinel)
		if err != nil {
			return "", "", core.Explain(err, "read the comments on %s", key)
		}
		if data.Repository == nil || data.Repository.Issue == nil {
			return "", "", core.Errorf(core.ErrNotFound, "comment on %s", key).
				WithWhy("%s/%s has no issue %d", owner, name, number)
		}
		issue := data.Repository.Issue
		issueID = issue.ID
		for _, comment := range issue.Comments.Nodes {
			if core.HasMarker(comment.Body, m) {
				return issueID, comment.ID, nil
			}
		}
		if !issue.Comments.PageInfo.HasNextPage || issue.Comments.PageInfo.EndCursor == "" {
			return issueID, "", nil
		}
		after = issue.Comments.PageInfo.EndCursor
	}
}

// Close closes a ticket, recording why.
//
// A ticket already closed is left exactly as it is. Doctor runs daily, and
// closing a closed ticket again would move the moment the work was finished —
// which is what spec 07 §6 reads to decide whether a follow-up is overdue.
func (g *GitHubProjects) Close(ctx context.Context, key string, reason string) error {
	ticket, _, err := g.ticket(ctx, key)
	if err != nil {
		return err
	}
	if ticket.Closed {
		return nil
	}
	if _, err := core.GraphQL[ghMutationResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghCloseIssueMutation,
		Variables: map[string]any{"issue": ticket.ID, "reason": ghStateReason(reason)},
	}, ghSentinel); err != nil {
		return core.Explain(err, "close %s", key)
	}
	return nil
}

// Reopen reopens a closed ticket.
//
// GitHub records nothing about why an issue was reopened, so the reason is
// not sent: the engine writes it as a comment of its own, which is evidence
// somebody can read rather than a field nobody can (spec 07 §4).
func (g *GitHubProjects) Reopen(ctx context.Context, key string, reason string) error {
	ticket, _, err := g.ticket(ctx, key)
	if err != nil {
		return err
	}
	if !ticket.Closed {
		return nil
	}
	if _, err := core.GraphQL[ghMutationResult](ctx, g.client, g.request(), core.GraphQLQuery{
		Query:     ghReopenIssueMutation,
		Variables: map[string]any{"issue": ticket.ID},
	}, ghSentinel); err != nil {
		return core.Explain(err, "reopen %s", key)
	}
	return nil
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

// GitHubProjects is a tracker.
var _ core.Tracker = (*GitHubProjects)(nil)
