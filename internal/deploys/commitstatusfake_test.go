package deploys

import (
	"context"
	"slices"
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
	// window is put to the forge against the commit.
	csCommitLead = 5 * time.Minute
)

// statusForge is the forge a commit status deployment source reads: a
// [coretest.FakeForge] holding a [coretest.DeployFixture] as commit statuses,
// and a record of everything the source asked it.
//
// The source speaks to a forge rather than to a provider's API, so what it
// asks for is the thing worth watching. How a question about a window or a
// page becomes an HTTP request is the adapter's business and is proved
// against each adapter's own fake (spec 14).
type statusForge struct {
	*coretest.FakeForge

	mu       sync.Mutex
	repos    int
	commits  []core.CommitQuery
	statuses []string
}

// newStatusForge turns each of a fixture's deployment records into a commit
// status on the commit it deployed, on a trunk whose commits are dated just
// before them.
func newStatusForge(t *testing.T, fx coretest.DeployFixture) *statusForge {
	t.Helper()
	return newStatusForgeOn(t, fx, fakeTrunk)
}

// newStatusForgeOn is the same forge with the trunk named. An empty name is a
// repository that will not say which branch its trunk is.
func newStatusForgeOn(t *testing.T, fx coretest.DeployFixture, trunk string) *statusForge {
	t.Helper()

	records := slices.Clone(fx.Deployments)
	slices.SortStableFunc(records, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })

	statuses := map[string][]core.CommitStatus{}
	written := map[string]time.Time{}
	for _, record := range records {
		context := fakeProdContext
		if record.Environment != "production" {
			context = fakeStagingContext
		}
		// A forge answers with the latest verdict per context, so a
		// second record under the same context on the same commit
		// replaces the first, exactly as publishing it would.
		kept := slices.DeleteFunc(statuses[record.SHA], func(have core.CommitStatus) bool {
			return have.Context == context
		})
		statuses[record.SHA] = append(kept, csStatus(context, csStateOf(record.State), record.CreatedAt))
		// One commit may be deployed more than once; it was written
		// before the first of them.
		if at, seen := written[record.SHA]; !seen || record.CreatedAt.Before(at) {
			written[record.SHA] = record.CreatedAt
		}
	}

	var commits []core.Commit
	for sha, at := range written {
		commits = append(commits, core.Commit{SHA: sha, CommittedAt: at.Add(-csCommitLead), AuthoredAt: at.Add(-csCommitLead)})
	}
	slices.SortStableFunc(commits, func(a, b core.Commit) int { return b.CommittedAt.Compare(a.CommittedAt) })

	return &statusForge{FakeForge: coretest.NewFakeForge(coretest.ForgeFixture{
		Repo:     core.RepoInfo{Owner: fakeOwner, Name: fakeRepo, DefaultBranch: trunk},
		Commits:  commits,
		Statuses: statuses,
		Clock:    coretest.NewClock(coretest.ContractNow),
	})}
}

// csStateOf is the commit status state a deployment state came from. There is
// no running state to come back to: a commit status has none.
func csStateOf(state string) string {
	switch state {
	case core.DeploySuccess:
		return core.StatusSuccess
	case core.DeployFailure:
		return core.StatusFailure
	default:
		return core.StatusPending
	}
}

// csStatus is one deployer's verdict, with the line and the address a report
// shows.
func csStatus(context, state string, at time.Time) core.CommitStatus {
	return core.CommitStatus{
		Context:     context,
		State:       state,
		Description: "the deployer said " + state,
		TargetURL:   "https://ci.test/runs/" + state,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

// push records a deployer's verdict on a commit, replacing that context's
// last word as publishing it does.
func (f *statusForge) push(t *testing.T, sha, context, state string, at time.Time) {
	t.Helper()
	if err := f.FakeForge.SetCommitStatus(t.Context(), sha, csStatus(context, state, at)); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
}

func (f *statusForge) Repo(ctx context.Context) (core.RepoInfo, error) {
	f.mu.Lock()
	f.repos++
	f.mu.Unlock()
	return f.FakeForge.Repo(ctx)
}

func (f *statusForge) Commits(ctx context.Context, q core.CommitQuery) ([]core.Commit, error) {
	f.mu.Lock()
	f.commits = append(f.commits, q)
	f.mu.Unlock()
	return f.FakeForge.Commits(ctx, q)
}

func (f *statusForge) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	f.mu.Lock()
	f.statuses = append(f.statuses, sha)
	f.mu.Unlock()
	return f.FakeForge.CommitStatuses(ctx, sha)
}

// Repos is how many times the repository itself was asked for, Walks every
// walk of the trunk the source asked for, and Statuses every commit whose
// statuses it read.
func (f *statusForge) Repos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos
}

func (f *statusForge) Walks() []core.CommitQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commits)
}

func (f *statusForge) Statuses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.statuses)
}
