// Package deploys holds the deployment sources: where flow learns that a
// commit reached an environment. flow never talks to a cluster or a deploy
// tool — it reads the records the forge holds about the application
// repository, written by whatever does the deploying (spec 13 §1). Like the
// forge and tracker adapters, it depends on core alone (spec 01 §2).
package deploys

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// GitHub deployment source constants (spec 10 §2).
const (
	// CapabilityAdapter names the declaration a deployment source is
	// described by, assets/capabilities/deploys.yaml. A deployment record
	// says the same thing whichever forge holds it, so the declaration is
	// shared rather than one per provider (spec 05 §1).
	CapabilityAdapter = "deploys"
	// gitHubAccept is the media type every REST call asks for.
	gitHubAccept = "application/vnd.github+json"
	// gitHubAPIVersion pins the REST API's dated version.
	gitHubAPIVersion = "2022-11-28"
	// gitHubPageSize is the largest page GitHub serves.
	gitHubPageSize = 100
)

// GitHubOptions configures a [GitHub] deployment source.
type GitHubOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner and Repo name the application repository whose records are
	// read. They are the repository the deployer wrote against, which is
	// the one holding the commits (spec 13 §1).
	Owner string
	Repo  string
	// Capabilities is the declaration loaded from
	// assets/capabilities/deploys.yaml.
	Capabilities core.CapabilitySet
}

// GitHub reads GitHub's own deployment records.
type GitHub struct {
	client *core.Client
	owner  string
	repo   string
	caps   core.CapabilitySet
}

// NewGitHub returns a deployment source for one repository.
func NewGitHub(o GitHubOptions) (*GitHub, error) {
	switch {
	case o.Client == nil:
		return nil, core.Errorf(core.ErrValidation, "build the github deployment source").
			WithWhy("no HTTP client was given")
	case o.Owner == "" || o.Repo == "":
		return nil, core.Errorf(core.ErrValidation, "build the github deployment source").
			WithWhy("the repository was given as %q/%q", o.Owner, o.Repo).
			WithFix("name the application repository as owner/name")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the github deployment source").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/%s.yaml and pass the declaration", CapabilityAdapter)
	}
	return &GitHub{client: o.Client, owner: o.Owner, repo: o.Repo, caps: o.Capabilities}, nil
}

// Kind is the source this is: the forge's own deployment records.
func (g *GitHub) Kind() string { return core.SourceForgeDeployments }

// Capabilities is what this source declares it can do.
func (g *GitHub) Capabilities() core.CapabilitySet { return g.caps }

// Slug is the repository the records are read from, as owner/name.
func (g *GitHub) Slug() string { return g.owner + "/" + g.repo }

// Deployments returns the records matching q, newest first.
//
// The environment and the commit are the provider's own filters, so a busy
// repository's other environments are never fetched to be thrown away. The
// window is not: GitHub's deployments endpoint has no "since", so the walk
// stops at the first record older than it, which the provider's newest-first
// order makes the last one worth reading (spec 10 §2).
func (g *GitHub) Deployments(ctx context.Context, q core.DeployQuery) ([]core.Deployment, error) {
	if q.Environment == "" {
		return nil, core.Errorf(core.ErrValidation, "read the deployments of %s", g.Slug()).
			WithWhy("no environment was named").
			WithFix("set deploys.environment in .flow.yaml")
	}
	query := url.Values{}
	query.Set("environment", q.Environment)
	query.Set("per_page", strconv.Itoa(gitHubPageSize))
	if q.SHA != "" {
		query.Set("sha", q.SHA)
	}

	var payloads []ghDeployment
	err := core.Paginate(ctx, g.client, g.decorate(core.Request{Path: g.path("deployments"), Query: query}),
		func(d ghDeployment) bool {
			if !q.Since.IsZero() && d.CreatedAt.Before(q.Since) {
				return false
			}
			payloads = append(payloads, d)
			return q.Limit <= 0 || len(payloads) < q.Limit
		})
	if err != nil {
		return nil, core.Explain(err, "read the %s deployments of %s", q.Environment, g.Slug())
	}

	records := make([]core.Deployment, 0, len(payloads))
	for _, payload := range payloads {
		record, err := g.state(ctx, payload)
		if err != nil {
			return nil, core.Explain(err, "read the state of the %s deployments of %s", q.Environment, g.Slug())
		}
		records = append(records, record)
	}
	// The provider serves them newest first; sorting makes that true of
	// what the caller gets whatever order a page arrived in.
	slices.SortStableFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return records, nil
}

// state fills in what became of one deployment. GitHub keeps the state beside
// the record rather than on it, as a history of statuses served newest first,
// so the state is the newest one that says something about the outcome.
func (g *GitHub) state(ctx context.Context, payload ghDeployment) (core.Deployment, error) {
	record := payload.toDeployment()
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(gitHubPageSize))
	var statuses []ghDeploymentStatus
	request := core.Request{
		Path:  g.path("deployments", strconv.FormatInt(payload.ID, 10), "statuses"),
		Query: query,
		Out:   &statuses,
	}
	if _, err := g.do(ctx, request); err != nil {
		return core.Deployment{}, err
	}
	for _, status := range statuses {
		// "inactive" is not an outcome: GitHub writes it on a record
		// that a later deployment superseded. Reading it as the state
		// would turn every past success into a non-success, and the
		// question "what was running at the time" needs those.
		if status.State == ghDeployInactive {
			continue
		}
		state, ok := deployState(status.State)
		if !ok {
			return core.Deployment{}, core.Errorf(core.ErrUnavailable, "read the state of deployment %s", record.ID).
				WithWhy("the provider called it %q, which flow does not understand", status.State).
				WithFix("this is a provider or flow bug; report it with the command you ran")
		}
		record.State = state
		record.UpdatedAt = status.CreatedAt
		record.Description = cmp.Or(status.Description, record.Description)
		record.URL = cmp.Or(status.LogURL, status.TargetURL, record.URL)
		return record, nil
	}
	// A deployment nothing has reported on has not started, which is
	// exactly what pending means (spec 13 §1).
	record.State = core.DeployPending
	return record, nil
}

// do sends one request with GitHub's media type and API version.
func (g *GitHub) do(ctx context.Context, r core.Request) (*core.Response, error) {
	return g.client.Do(ctx, g.decorate(r))
}

// decorate returns the request with GitHub's media type and API version on it.
func (g *GitHub) decorate(r core.Request) core.Request {
	header := http.Header{}
	for name, values := range r.Header {
		header[name] = slices.Clone(values)
	}
	header.Set("Accept", gitHubAccept)
	header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	r.Header = header
	return r
}

// path builds a path under this repository. Every segment below it is a
// literal or a decimal identifier, so only the repository's own name needs
// escaping.
func (g *GitHub) path(segments ...string) string {
	parts := append([]string{"repos", url.PathEscape(g.owner), url.PathEscape(g.repo)}, segments...)
	return strings.Join(parts, "/")
}

// GitHub is a deployment source.
var _ core.DeploySource = (*GitHub)(nil)
