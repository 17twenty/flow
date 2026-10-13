package coretest

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// ForgeFixture is the world a forge presents. The contract suite builds one
// and hands it to whatever is under test, so that the in-memory fake and a
// real adapter in front of an httptest server are asked the same questions
// about the same repository (spec 14).
type ForgeFixture struct {
	// Kind is the adapter name the forge reports.
	Kind string
	// Capabilities is what it declares it can do.
	Capabilities core.CapabilitySet
	// Permissions is what Probe reports.
	Permissions core.Permissions
	// Repo is the repository itself.
	Repo core.RepoInfo
	// PullRequests are its pull requests.
	PullRequests []core.PullRequest
	// Commits are the commits on the default branch, newest first. Ancestry
	// is derived from this order when Comparisons says nothing.
	Commits []core.Commit
	// Statuses are the commit statuses, keyed by SHA.
	Statuses map[string][]core.CommitStatus
	// Protections are the protected branches, keyed by branch.
	Protections map[string]core.Protection
	// Comparisons override derived ancestry, keyed "base...head".
	Comparisons map[string]core.Comparison
	// Files are the files on the default branch, keyed by path.
	Files map[string][]byte
	// Clock decides what "now" is for anything the forge writes.
	Clock core.Clock
}

// FakeForge is an in-memory forge that behaves: it is the reference every
// real forge adapter is measured against, and the one the engine's tests run
// against. Failures are injected through its embedded [Faults].
type FakeForge struct {
	Faults

	mu          sync.Mutex
	kind        string
	caps        core.CapabilitySet
	permissions core.Permissions
	clock       core.Clock
	repo        core.RepoInfo
	prs         []core.PullRequest
	commits     []core.Commit
	statuses    map[string][]core.CommitStatus
	protections map[string]core.Protection
	comparisons map[string]core.Comparison
	files       map[string][]byte
	comments    map[int][]core.Comment
	nextPR      int
	nextID      int
}

// NewFakeForge builds a forge holding the fixture's world.
func NewFakeForge(fx ForgeFixture) *FakeForge {
	if fx.Kind == "" {
		fx.Kind = "fake-forge"
	}
	if fx.Capabilities.Adapter == "" {
		fx.Capabilities = FullCapabilities(fx.Kind, core.KindForge)
	}
	if fx.Clock == nil {
		fx.Clock = NewClock(time.Time{})
	}
	f := &FakeForge{
		kind:        fx.Kind,
		caps:        fx.Capabilities,
		permissions: fx.Permissions,
		clock:       fx.Clock,
		repo:        fx.Repo,
		prs:         slices.Clone(fx.PullRequests),
		commits:     slices.Clone(fx.Commits),
		statuses:    map[string][]core.CommitStatus{},
		protections: map[string]core.Protection{},
		comparisons: maps.Clone(fx.Comparisons),
		files:       maps.Clone(fx.Files),
		comments:    map[int][]core.Comment{},
	}
	for sha, statuses := range fx.Statuses {
		f.statuses[sha] = slices.Clone(statuses)
	}
	for branch, protection := range fx.Protections {
		protection.Branch = branch
		f.protections[branch] = protection
	}
	for _, pr := range f.prs {
		f.nextPR = max(f.nextPR, pr.Number)
	}
	return f
}

// Kind is the adapter's name.
func (f *FakeForge) Kind() string { return f.kind }

// Capabilities is what the fake declares it can do.
func (f *FakeForge) Capabilities() core.CapabilitySet { return f.caps }

// Probe reports the permissions the fixture granted.
func (f *FakeForge) Probe(ctx context.Context) (core.Permissions, error) {
	if err := f.enter(ctx, "Probe"); err != nil {
		return core.Permissions{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.permissions, nil
}

// Repo describes the repository.
func (f *FakeForge) Repo(ctx context.Context) (core.RepoInfo, error) {
	if err := f.enter(ctx, "Repo"); err != nil {
		return core.RepoInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repo, nil
}

// PullRequest returns one pull request.
func (f *FakeForge) PullRequest(ctx context.Context, number int) (core.PullRequest, error) {
	if err := f.enter(ctx, "PullRequest"); err != nil {
		return core.PullRequest{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pr := range f.prs {
		if pr.Number == number {
			return pr, nil
		}
	}
	return core.PullRequest{}, notFound("pull request", "#"+strconv.Itoa(number))
}

// PullRequests returns the pull requests matching q, most recently updated
// first.
func (f *FakeForge) PullRequests(ctx context.Context, q core.PRQuery) ([]core.PullRequest, error) {
	if err := f.enter(ctx, "PullRequests"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []core.PullRequest
	for _, pr := range f.prs {
		if q.State != "" && pr.State != q.State {
			continue
		}
		if q.Base != "" && pr.BaseRef != q.Base {
			continue
		}
		if !q.UpdatedSince.IsZero() && pr.UpdatedAt.Before(q.UpdatedSince) {
			continue
		}
		out = append(out, summarised(pr))
	}
	slices.SortStableFunc(out, func(a, b core.PullRequest) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// summarised is one pull request as a listing describes it. Reviews and
// closing issues are separate reads on every forge flow speaks to (spec 10
// §2), so a fake that handed them out with the list would let a caller read
// what no adapter would ever have given it — which is exactly how the live
// audit came to judge approvals it had never fetched.
func summarised(pr core.PullRequest) core.PullRequest {
	pr.Reviews = nil
	pr.ClosingIssues = nil
	pr.Files.Paths = nil
	return pr
}

// Commits returns commits on a branch, newest first.
func (f *FakeForge) Commits(ctx context.Context, q core.CommitQuery) ([]core.Commit, error) {
	if err := f.enter(ctx, "Commits"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []core.Commit
	for _, commit := range f.commits {
		if !q.Since.IsZero() && commit.CommittedAt.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && commit.CommittedAt.After(q.Until) {
			continue
		}
		out = append(out, commit)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Compare reports the ancestry between two commits. The fixture's commit
// order is the history: a commit listed later is an ancestor of one listed
// earlier.
func (f *FakeForge) Compare(ctx context.Context, base, head string) (core.Comparison, error) {
	if err := f.enter(ctx, "Compare"); err != nil {
		return core.Comparison{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	comparison := core.Comparison{Base: base, Head: head}
	if override, ok := f.comparisons[base+"..."+head]; ok {
		override.Base, override.Head = base, head
		return override, nil
	}
	if base == head {
		comparison.Status = core.CompareIdentical
		return comparison, nil
	}
	baseAt, headAt := f.indexOf(base), f.indexOf(head)
	if baseAt < 0 || headAt < 0 {
		return core.Comparison{}, notFound("commit", base+"..."+head)
	}
	switch {
	case headAt < baseAt:
		comparison.Status, comparison.AheadBy = core.CompareAhead, baseAt-headAt
	default:
		comparison.Status, comparison.BehindBy = core.CompareBehind, headAt-baseAt
	}
	return comparison, nil
}

// indexOf returns a commit's position in the fixture's history, newest first.
func (f *FakeForge) indexOf(sha string) int {
	return slices.IndexFunc(f.commits, func(c core.Commit) bool { return c.SHA == sha })
}

// File returns a file's content on the default branch.
func (f *FakeForge) File(ctx context.Context, path, ref string) ([]byte, error) {
	if err := f.enter(ctx, "File"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[path]
	if !ok {
		return nil, notFound("file", path+"@"+ref)
	}
	return slices.Clone(content), nil
}

// Protection returns a branch's effective protection. An unprotected branch
// is not an error: it is protection that is not enabled.
func (f *FakeForge) Protection(ctx context.Context, branch string) (core.Protection, error) {
	if err := f.enter(ctx, "Protection"); err != nil {
		return core.Protection{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if protection, ok := f.protections[branch]; ok {
		return protection, nil
	}
	return core.Protection{Branch: branch}, nil
}

// SetProtection applies protection additively.
func (f *FakeForge) SetProtection(ctx context.Context, branch string, p core.Protection) error {
	if err := f.enter(ctx, "SetProtection"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.protections[branch]
	if !ok {
		current = core.Protection{Branch: branch}
	}
	merged := current.Merge(p)
	merged.Branch = branch
	f.protections[branch] = merged
	return nil
}

// SetMergeSettings sets the repository's merge strategies.
func (f *FakeForge) SetMergeSettings(ctx context.Context, s core.MergeSettings) error {
	if err := f.enter(ctx, "SetMergeSettings"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo.Merge = s
	return nil
}

// CommitStatuses returns the statuses on a commit, newest first.
func (f *FakeForge) CommitStatuses(ctx context.Context, sha string) ([]core.CommitStatus, error) {
	if err := f.enter(ctx, "CommitStatuses"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	statuses := slices.Clone(f.statuses[sha])
	slices.SortStableFunc(statuses, func(a, b core.CommitStatus) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return statuses, nil
}

// SetCommitStatus publishes a status, replacing an earlier verdict from the
// same context.
func (f *FakeForge) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	if err := f.enter(ctx, "SetCommitStatus"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = f.clock.Now()
	}
	statuses := slices.DeleteFunc(f.statuses[sha], func(have core.CommitStatus) bool {
		return have.Context == s.Context
	})
	f.statuses[sha] = append(statuses, s)
	return nil
}

// UpsertComment writes flow's comment, editing the one carrying that marker.
func (f *FakeForge) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	if err := f.enter(ctx, "UpsertComment"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.pullRequest(number); err != nil {
		return err
	}
	marked := core.MarkBody(body, m, true)
	now := f.clock.Now()
	for i, comment := range f.comments[number] {
		if m.Matches(comment.Marker) {
			comment.Body, comment.Marker, comment.UpdatedAt = marked, m, now
			f.comments[number][i] = comment
			return nil
		}
	}
	f.nextID++
	f.comments[number] = append(f.comments[number], core.Comment{
		ID:        "comment-" + strconv.Itoa(f.nextID),
		Author:    "flow",
		Body:      marked,
		Marker:    m,
		CreatedAt: now,
		UpdatedAt: now,
		URL:       f.repo.URL + "/pull/" + strconv.Itoa(number) + "#comment-" + strconv.Itoa(f.nextID),
	})
	return nil
}

// Comment returns flow's comment carrying that marker, or nil.
func (f *FakeForge) Comment(ctx context.Context, number int, m core.Marker) (*core.Comment, error) {
	if err := f.enter(ctx, "Comment"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.pullRequest(number); err != nil {
		return nil, err
	}
	for _, comment := range f.comments[number] {
		if m.Matches(comment.Marker) {
			found := comment
			return &found, nil
		}
	}
	return nil, nil
}

// DeleteComment removes the comment carrying that marker, if there is one.
func (f *FakeForge) DeleteComment(ctx context.Context, number int, m core.Marker) error {
	if err := f.enter(ctx, "DeleteComment"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.pullRequest(number); err != nil {
		return err
	}
	f.comments[number] = slices.DeleteFunc(f.comments[number], func(have core.Comment) bool {
		return m.Matches(have.Marker)
	})
	return nil
}

// ProposeFiles offers files as a pull request from the proposal's own branch,
// updating the open one when the same proposal is made again.
func (f *FakeForge) ProposeFiles(ctx context.Context, p core.FileProposal) (core.PullRequest, error) {
	if err := f.enter(ctx, "ProposeFiles"); err != nil {
		return core.PullRequest{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if p.ID == "" {
		return core.PullRequest{}, core.Errorf(core.ErrValidation, "propose files").
			WithWhy("the proposal has no id, so it has no branch").
			WithFix("give the proposal an id")
	}
	now := f.clock.Now()
	paths := make([]string, 0, len(p.Files))
	for _, file := range p.Files {
		paths = append(paths, file.Path)
	}
	slices.Sort(paths)

	for i, pr := range f.prs {
		if pr.HeadRef != p.Branch() || pr.State != core.PRStateOpen {
			continue
		}
		pr.Title, pr.Body, pr.UpdatedAt = p.Title, p.Body, now
		pr.Files = core.FileSummary{Changed: len(paths), Paths: paths}
		f.prs[i] = pr
		return pr, nil
	}

	f.nextPR++
	pr := core.PullRequest{
		Number:    f.nextPR,
		Title:     p.Title,
		Body:      p.Body,
		Author:    "flow",
		State:     core.PRStateOpen,
		HeadRef:   p.Branch(),
		BaseRef:   f.repo.DefaultBranch,
		HeadSHA:   "sha-" + p.ID,
		Files:     core.FileSummary{Changed: len(paths), Paths: paths},
		CreatedAt: now,
		UpdatedAt: now,
		URL:       f.repo.URL + "/pull/" + strconv.Itoa(f.nextPR),
	}
	f.prs = append(f.prs, pr)
	return pr, nil
}

// pullRequest finds a pull request. The caller holds the lock.
func (f *FakeForge) pullRequest(number int) (core.PullRequest, error) {
	for _, pr := range f.prs {
		if pr.Number == number {
			return pr, nil
		}
	}
	return core.PullRequest{}, notFound("pull request", "#"+strconv.Itoa(number))
}

// Comments returns every comment on a pull request, for a test to inspect.
func (f *FakeForge) Comments(number int) []core.Comment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[number])
}

// SetPullRequest adds or replaces a pull request, so that a test can change
// the world between calls.
func (f *FakeForge) SetPullRequest(pr core.PullRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, have := range f.prs {
		if have.Number == pr.Number {
			f.prs[i] = pr
			return
		}
	}
	f.prs = append(f.prs, pr)
	f.nextPR = max(f.nextPR, pr.Number)
}

// SetPermissions replaces what Probe reports, so that a test can hold the
// common unhappy case: a credential the forge accepts which still cannot
// administer the repository (scenario A06).
func (f *FakeForge) SetPermissions(permissions core.Permissions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.permissions = permissions
}

// FakeForge is the reference implementation of the interface.
var _ core.Forge = (*FakeForge)(nil)
