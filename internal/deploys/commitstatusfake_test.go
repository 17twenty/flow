package deploys

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// The contexts the fake's deployer reports under. One environment's statuses
// are flow's business and the other's are not, which is what makes a fixture
// holding both able to tell matching from reading everything.
const (
	fakeProdContext    = "argocd/app-prod"
	fakeStagingContext = "argocd/app-staging"
	// fakeTrunk is the repository's default branch, which the source has
	// to ask for: configuration never names it.
	fakeTrunk = "main"
	// csCommitLead is how long before its deployment each commit was
	// written. A commit always predates the status hanging on it, and the
	// window is put to the provider against the commit.
	csCommitLead = 5 * time.Minute
)

// statusFake serves the three GitHub shapes a commit status deployment source
// reads — the repository, the trunk's commits and one commit's statuses —
// over a [coretest.DeployFixture] (spec 14).
//
// It paginates and filters by `since` the way the provider does, because the
// walk over commits and the stop at the window are the two things this
// adapter has to get right that an in-memory fake can never exercise.
type statusFake struct {
	pageSize int
	// commits are the trunk's commits, newest first.
	commits []ghTrunkCommit
	// statuses are each commit's statuses, newest first, in GitHub's own
	// vocabulary.
	statuses map[string][]map[string]any
	// branch is what the repository calls its trunk. Empty makes the
	// repository answer with no default branch at all.
	branch string

	mu       sync.Mutex
	requests []string
	queries  []string
}

// newStatusServer stands the fake up in front of a fixture, turning each of
// its deployment records into a commit status on the commit it deployed.
func newStatusServer(t *testing.T, fx coretest.DeployFixture) (*statusFake, *httptest.Server) {
	t.Helper()
	fake := &statusFake{pageSize: 2, statuses: map[string][]map[string]any{}, branch: fakeTrunk}

	records := slices.Clone(fx.Deployments)
	slices.SortStableFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	written := map[string]time.Time{}
	for _, record := range records {
		context := fakeProdContext
		if record.Environment != "production" {
			context = fakeStagingContext
		}
		fake.statuses[record.SHA] = append(fake.statuses[record.SHA],
			csStatusJSON(context, csStateOf(record.State), record.CreatedAt))
		// One commit may be deployed more than once; it was written
		// before the first of them.
		if at, seen := written[record.SHA]; !seen || record.CreatedAt.Before(at) {
			written[record.SHA] = record.CreatedAt
		}
	}
	for sha, at := range written {
		commit := ghTrunkCommit{SHA: sha}
		commit.Commit.Committer.Date = at.Add(-csCommitLead)
		fake.commits = append(fake.commits, commit)
	}
	slices.SortStableFunc(fake.commits, func(a, b ghTrunkCommit) int {
		return b.Commit.Committer.Date.Compare(a.Commit.Committer.Date)
	})

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv
}

// csStateOf is the GitHub commit status state a core state came from. There
// is no running state to come back to: a commit status has none.
func csStateOf(state string) string {
	switch state {
	case core.DeploySuccess:
		return ghStatusSuccess
	case core.DeployFailure:
		return ghStatusFailure
	default:
		return ghStatusPending
	}
}

// csStatusJSON renders a commit status as GitHub does.
func csStatusJSON(context, state string, at time.Time) map[string]any {
	return map[string]any{
		"id":          int64(len(context) + len(state)),
		"context":     context,
		"state":       state,
		"description": "the deployer said " + state,
		"target_url":  "https://ci.test/runs/" + state,
		"created_at":  at.Format(time.RFC3339),
		"updated_at":  at.Format(time.RFC3339),
	}
}

// push adds a status to a commit, as GitHub does: newest first, nothing
// replaced.
func (f *statusFake) push(sha, context, state string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[sha] = append([]map[string]any{csStatusJSON(context, state, at)}, f.statuses[sha]...)
}

// Requests is every path the adapter asked for, and Queries the raw query of
// each, so that a test can prove a filter was the provider's work.
func (f *statusFake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *statusFake) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

func (f *statusFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.EscapedPath())
	f.queries = append(f.queries, r.URL.RawQuery)
	f.mu.Unlock()

	if !readsLikeFlow(w, r) {
		return
	}
	prefix := "/repos/" + fakeOwner + "/" + fakeRepo
	path, under := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !under {
		failJSON(w, http.StatusNotFound, "Not Found")
		return
	}
	switch {
	case path == "":
		writeJSON(w, r, map[string]any{"default_branch": f.branch})
	case path == "/commits":
		f.listCommits(w, r)
	case strings.HasPrefix(path, "/commits/") && strings.HasSuffix(path, "/statuses"):
		f.listStatuses(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/commits/"), "/statuses"))
	default:
		failJSON(w, http.StatusNotFound, "Not Found")
	}
}

// listCommits serves the trunk's commits, filtered by `since` the way GitHub
// filters them.
func (f *statusFake) listCommits(w http.ResponseWriter, r *http.Request) {
	if branch := r.URL.Query().Get("sha"); branch != f.branch {
		failJSON(w, http.StatusNotFound, "No commit found for SHA: "+branch)
		return
	}
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			failJSON(w, http.StatusUnprocessableEntity, "since is not a timestamp")
			return
		}
		since = parsed
	}
	var items []any
	f.mu.Lock()
	for _, commit := range f.commits {
		if !since.IsZero() && commit.Commit.Committer.Date.Before(since) {
			continue
		}
		items = append(items, commit)
	}
	f.mu.Unlock()
	writePage(w, r, items, f.pageSize)
}

// listStatuses serves one commit's statuses.
func (f *statusFake) listStatuses(w http.ResponseWriter, r *http.Request, sha string) {
	f.mu.Lock()
	statuses := slices.Clone(f.statuses[sha])
	f.mu.Unlock()
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, status)
	}
	writePage(w, r, items, f.pageSize)
}
