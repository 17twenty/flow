package deploys

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// commitStatusWalk is how many trunk commits a query reads at most. A commit
// status is read one commit at a time, so a walk nothing bounds would be one
// request per commit of the repository's history. The hundred newest is the
// provider's own page, and a deployment that is not on one of them is older
// than anything doctor asks about (spec 13 §3).
const commitStatusWalk = 100

// CommitStatusOptions configures a [CommitStatus] deployment source.
type CommitStatusOptions struct {
	// Client is the HTTP client, already carrying the credential and the
	// API root.
	Client *core.Client
	// Owner and Repo name the application repository whose commits carry
	// the statuses (spec 13 §1).
	Owner string
	Repo  string
	// Environment is what the configured contexts stand for. A commit
	// status has no environment of its own, so this source speaks for one.
	Environment string
	// Contexts are the status contexts that mean "deployed". A trailing *
	// matches by prefix (spec 03 §1).
	Contexts []string
	// Branch is the trunk whose commits are walked. Empty asks the
	// repository for its default branch.
	Branch string
	// Capabilities is the declaration loaded from
	// assets/capabilities/deploys.yaml.
	Capabilities core.CapabilitySet
}

// CommitStatus reads deployments recorded as commit statuses, for forges with
// no deployments API and for deployers that report that way (spec 13 §1).
type CommitStatus struct {
	ghRepo
	environment string
	contexts    []string
	branch      string
	caps        core.CapabilitySet

	// mu guards trunk, which is the default branch once the repository has
	// been asked for it. Only an answer is remembered: a failed read is a
	// bad moment, not a fact about the repository.
	mu    sync.Mutex
	trunk string
}

// NewCommitStatus returns a commit status deployment source for one
// repository and one environment.
func NewCommitStatus(o CommitStatusOptions) (*CommitStatus, error) {
	repo := ghRepo{client: o.Client, owner: o.Owner, repo: o.Repo}
	if err := repo.validate("commit status deployment source"); err != nil {
		return nil, err
	}
	switch {
	case o.Environment == "":
		return nil, core.Errorf(core.ErrValidation, "build the commit status deployment source").
			WithWhy("no environment was named, and a commit status does not carry one").
			WithFix("set deploys.environment in .flow.yaml")
	case len(o.Contexts) == 0:
		return nil, core.Errorf(core.ErrValidation, "build the commit status deployment source").
			WithWhy("no status contexts were named, so nothing would mean deployed").
			WithFix("set deploys.contexts in .flow.yaml")
	case o.Capabilities.Adapter == "":
		return nil, core.Errorf(core.ErrValidation, "build the commit status deployment source").
			WithWhy("no capability declaration was given").
			WithFix("load assets/capabilities/%s.yaml and pass the declaration", CapabilityAdapter)
	}
	return &CommitStatus{
		ghRepo:      repo,
		environment: o.Environment,
		contexts:    slices.Clone(o.Contexts),
		branch:      o.Branch,
		caps:        o.Capabilities,
	}, nil
}

// Kind is the source this is: commit statuses standing for deployments.
func (c *CommitStatus) Kind() string { return core.SourceCommitStatus }

// Capabilities is what this source declares it can do.
func (c *CommitStatus) Capabilities() core.CapabilitySet { return c.caps }

// Slug is the repository the statuses are read from, as owner/name.
func (c *CommitStatus) Slug() string { return c.slug() }

// Deployments returns the records matching q, newest first.
//
// GitHub can be asked for the commits of a window but never for the statuses
// of one, so the walk is over the trunk's commits and the statuses hanging on
// them (spec 13 §3).
func (c *CommitStatus) Deployments(ctx context.Context, q core.DeployQuery) ([]core.Deployment, error) {
	if q.Environment == "" {
		return nil, core.Errorf(core.ErrValidation, "read the deployments of %s", c.Slug()).
			WithWhy("no environment was named").
			WithFix("set deploys.environment in .flow.yaml")
	}
	// The contexts are what stand for an environment here, and this source
	// was built for one. Another environment is not a question it holds any
	// records about, which is the same answer as an environment nothing
	// deploys to.
	if !strings.EqualFold(q.Environment, c.environment) {
		return nil, nil
	}

	commits, err := c.commits(ctx, q)
	if err != nil {
		return nil, core.Explain(err, "find the commits of %s that could carry a %s deployment", c.Slug(), c.environment)
	}

	var records []core.Deployment
	for _, sha := range commits {
		found, err := c.statuses(ctx, sha)
		if err != nil {
			return nil, core.Explain(err, "read the %s deployments of %s", c.environment, c.Slug())
		}
		records = append(records, found...)
		// The walk goes newest commit first, so a limit the caller only
		// wants the top of is already satisfied.
		if q.Limit > 0 && len(records) >= q.Limit {
			break
		}
	}
	slices.SortStableFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if q.Limit > 0 && len(records) > q.Limit {
		records = records[:q.Limit]
	}
	return records, nil
}

// commits returns the commits whose statuses could answer the query, newest
// first. A query naming a commit needs no walk at all: that commit is the
// only one whose statuses are about it.
func (c *CommitStatus) commits(ctx context.Context, q core.DeployQuery) ([]string, error) {
	if q.SHA != "" {
		return []string{q.SHA}, nil
	}
	trunk, err := c.trunkName(ctx)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("sha", trunk)
	query.Set("per_page", strconv.Itoa(gitHubPageSize))
	if !q.Since.IsZero() {
		query.Set("since", q.Since.UTC().Format(time.RFC3339))
	}

	var shas []string
	err = core.Paginate(ctx, c.client, c.decorate(core.Request{Path: c.path("commits"), Query: query}),
		func(commit ghTrunkCommit) bool {
			shas = append(shas, commit.SHA)
			return len(shas) < commitStatusWalk
		})
	if err != nil {
		return nil, err
	}
	return shas, nil
}

// statuses turns the statuses on one commit into the deployments they record:
// one per context the configuration asked for, each the newest word on it.
//
// A context's history is one deployment's progress and not several, so the
// pending status a deployer wrote before it finished is not a second record
// standing beside the success that replaced it.
func (c *CommitStatus) statuses(ctx context.Context, sha string) ([]core.Deployment, error) {
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(gitHubPageSize))

	var records []core.Deployment
	spoken := map[string]bool{}
	var unknown ghCommitStatus
	err := core.Paginate(ctx, c.client, c.decorate(core.Request{Path: c.path("commits", url.PathEscape(sha), "statuses"), Query: query}),
		func(status ghCommitStatus) bool {
			if !matchesContext(c.contexts, status.Context) || spoken[status.Context] {
				return true
			}
			spoken[status.Context] = true
			state, ok := statusState(status.State)
			if !ok {
				unknown = status
				return false
			}
			records = append(records, core.Deployment{
				ID:          strconv.FormatInt(status.ID, 10),
				Environment: c.environment,
				SHA:         sha,
				State:       state,
				CreatedAt:   status.CreatedAt,
				UpdatedAt:   cmp.Or(status.UpdatedAt, status.CreatedAt),
				URL:         status.TargetURL,
				Description: status.Description,
			})
			return true
		})
	if err != nil {
		return nil, err
	}
	if unknown.Context != "" {
		return nil, core.Errorf(core.ErrUnavailable, "read the state of the %s status on %s", unknown.Context, sha).
			WithWhy("the provider called it %q, which flow does not understand", unknown.State).
			WithFix("this is a provider or flow bug; report it with the command you ran")
	}
	return records, nil
}

// trunkName is the branch whose commits are walked. Configuration has no key
// for the trunk (spec 03 §1) because the repository already knows which
// branch it is, so an unnamed one is asked for once and remembered.
func (c *CommitStatus) trunkName(ctx context.Context) (string, error) {
	if c.branch != "" {
		return c.branch, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trunk != "" {
		return c.trunk, nil
	}
	var repo ghRepository
	if _, err := c.do(ctx, core.Request{Path: c.path(), Out: &repo}); err != nil {
		return "", err
	}
	if repo.DefaultBranch == "" {
		return "", core.Errorf(core.ErrUnavailable, "find the trunk of %s", c.Slug()).
			WithWhy("the provider named no default branch").
			WithFix("check that the repository exists and the credential can read it")
	}
	c.trunk = repo.DefaultBranch
	return c.trunk, nil
}

// matchesContext reports whether a status context is one the configuration
// asked for. A pattern ending in * matches by prefix; everywhere else a
// character is itself, a * that is not the last one included (spec 03 §1).
// The comparison is exact, because a status context is: `argocd/App-Prod` and
// `argocd/app-prod` are two checks to the forge, and flow is in no position
// to decide they are one.
func matchesContext(patterns []string, context string) bool {
	for _, pattern := range patterns {
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
			if strings.HasPrefix(context, prefix) {
				return true
			}
			continue
		}
		if context == pattern {
			return true
		}
	}
	return false
}

// CommitStatus is a deployment source.
var _ core.DeploySource = (*CommitStatus)(nil)
