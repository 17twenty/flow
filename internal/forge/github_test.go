package forge

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// gitHubCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func gitHubCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(GitHubKind)
	if err != nil {
		t.Fatalf("Of(github): %v", err)
	}
	return set
}

// newGitHubAdapter builds an adapter talking to srv.
func newGitHubAdapter(t *testing.T, srv *httptest.Server, fx coretest.ForgeFixture, adjust func(*core.ClientOptions)) *GitHub {
	t.Helper()
	opts := core.ClientOptions{
		BaseURL: srv.URL,
		Token:   "test-token",
		Version: "v1.0.0",
		Clock:   coretest.NewClock(coretest.ContractNow),
		Backoff: func(int, core.RateLimit) time.Duration { return time.Millisecond },
	}
	if adjust != nil {
		adjust(&opts)
	}
	client, err := core.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	adapter, err := NewGitHub(GitHubOptions{
		Client:       client,
		Owner:        fx.Repo.Owner,
		Repo:         fx.Repo.Name,
		Capabilities: gitHubCapabilities(t),
	})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return adapter
}

// contractGitHub stands a fake GitHub up in front of the fixture.
func contractGitHub(t *testing.T, fx coretest.ForgeFixture) core.Forge {
	t.Helper()
	_, srv := newGitHubServer(t, fx)
	return newGitHubAdapter(t, srv, fx, nil)
}

func TestGitHubMeetsTheReadHalfOfTheContract(t *testing.T) {
	coretest.ForgeContractSubset(t, contractGitHub,
		"identity",
		"repo",
		"one pull request, with everything the gate reads",
		"a pull request that is not there",
		"pull requests in a window, newest first",
		"commits on the trunk, newest first",
		"ancestry proves a merge reached an environment",
		"files on the trunk",
	)
}

func TestNewGitHubRefusesAnIncompleteAdapter(t *testing.T) {
	caps := gitHubCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: GitHubAPI})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct {
		name string
		opts GitHubOptions
	}{
		{"no client", GitHubOptions{Owner: "a", Repo: "b", Capabilities: caps}},
		{"no repository", GitHubOptions{Client: client, Owner: "a", Capabilities: caps}},
		{"no declaration", GitHubOptions{Client: client, Owner: "a", Repo: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewGitHub(tc.opts); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

func TestGitHubReadsMergeSettingsInGitHubsOwnVocabulary(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Repo.Merge = core.MergeSettings{
		AllowSquash:         true,
		AllowMerge:          false,
		AllowRebase:         false,
		SquashTitle:         core.SquashTitlePR,
		SquashMessage:       core.SquashMessagePRBody,
		DeleteBranchOnMerge: true,
	}
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	repo, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if repo.Merge != fx.Repo.Merge {
		t.Errorf("Merge = %+v, want %+v", repo.Merge, fx.Repo.Merge)
	}
	if repo.URL != fx.Repo.URL {
		t.Errorf("URL = %q, want %q", repo.URL, fx.Repo.URL)
	}
}

func TestGitHubMapsAnUnknownSquashSettingToNothing(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Repo.Merge.SquashTitle = "something-else"
	fx.Repo.Merge.SquashMessage = "something-else"
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	repo, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if repo.Merge.SquashTitle != "" || repo.Merge.SquashMessage != "" {
		t.Errorf("Merge = %+v, want a setting flow does not recognise reported as unset", repo.Merge)
	}
}

func TestGitHubFollowsTheLinkHeaderInsteadOfCountingPages(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.pageSize = 1
	adapter := newGitHubAdapter(t, srv, fx, nil)

	prs, err := adapter.PullRequests(t.Context(), core.PRQuery{})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(prs) != len(fx.PullRequests) {
		t.Fatalf("got %d pull requests, want all %d across the pages", len(prs), len(fx.PullRequests))
	}
	var listed int
	for _, path := range fake.Requests() {
		if strings.HasSuffix(path, "/pulls") {
			listed++
		}
	}
	if listed != 3 {
		t.Errorf("the list was fetched %d times, want one request per page", listed)
	}
}

func TestGitHubStopsPagingAsSoonAsTheWindowCloses(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.pageSize = 1
	adapter := newGitHubAdapter(t, srv, fx, nil)

	prs, err := adapter.PullRequests(t.Context(), core.PRQuery{Limit: 1})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d pull requests, want 1", len(prs))
	}
	var listed int
	for _, path := range fake.Requests() {
		if strings.HasSuffix(path, "/pulls") {
			listed++
		}
	}
	if listed != 1 {
		t.Errorf("the list was fetched %d times for a limit of one", listed)
	}
}

func TestGitHubFiltersByStateGitHubCannotExpress(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	closed, err := adapter.PullRequests(t.Context(), core.PRQuery{State: core.PRStateClosed})
	if err != nil {
		t.Fatalf("PullRequests(closed): %v", err)
	}
	if len(closed) != 1 || closed[0].Number != 3 {
		t.Fatalf("closed = %v, want only #3: a merged pull request is closed to GitHub but not to flow", numbers(closed))
	}

	open, err := adapter.PullRequests(t.Context(), core.PRQuery{State: core.PRStateOpen})
	if err != nil {
		t.Fatalf("PullRequests(open): %v", err)
	}
	if len(open) != 1 || open[0].Number != 1 {
		t.Errorf("open = %v, want only #1", numbers(open))
	}
}

func TestGitHubReadsOnePullRequestWholeAndDropsAReviewNobodySubmitted(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	want := fx.PullRequests[1]
	if pr.Title != want.Title || pr.Author != want.Author || pr.HeadRef != want.HeadRef || pr.BaseRef != want.BaseRef {
		t.Errorf("pull request = %+v, want the fixture's", pr)
	}
	if pr.State != core.PRStateMerged || pr.MergeSHA != want.MergeSHA {
		t.Errorf("state/merge = %q/%q, want merged/%s", pr.State, pr.MergeSHA, want.MergeSHA)
	}
	if !slices.Equal(pr.Labels, want.Labels) {
		t.Errorf("Labels = %v, want %v", pr.Labels, want.Labels)
	}
	if len(pr.Reviews) != len(want.Reviews) {
		t.Fatalf("got %d reviews, want %d: a review nobody submitted is not a verdict", len(pr.Reviews), len(want.Reviews))
	}
	if pr.Reviews[0].State != core.ReviewChangesRequested || pr.Reviews[1].State != core.ReviewApproved {
		t.Errorf("reviews = %+v, want them oldest first", pr.Reviews)
	}
	if pr.Files.Changed != want.Files.Changed || pr.Files.Additions != want.Files.Additions ||
		pr.Files.Deletions != want.Files.Deletions || !slices.Equal(pr.Files.Paths, want.Files.Paths) {
		t.Errorf("Files = %+v, want %+v (paths sorted, counts from the pull request)", pr.Files, want.Files)
	}
	if !pr.MergedAt.Equal(want.MergedAt) || !pr.ClosedAt.Equal(want.ClosedAt) {
		t.Errorf("timeline = %v/%v, want %v/%v", pr.MergedAt, pr.ClosedAt, want.MergedAt, want.ClosedAt)
	}
}

func TestGitHubReportsAnOpenPullRequestAsOpen(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.State != core.PRStateOpen || !pr.Draft {
		t.Errorf("state/draft = %q/%v, want open/true", pr.State, pr.Draft)
	}
	if !pr.MergedAt.IsZero() || !pr.ClosedAt.IsZero() {
		t.Errorf("an open pull request has a timeline: merged %v, closed %v", pr.MergedAt, pr.ClosedAt)
	}
	if len(pr.Reviews) != 0 {
		t.Errorf("Reviews = %+v, want none", pr.Reviews)
	}
}

func TestGitHubBoundsCommitsByTheWindow(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	commits, err := adapter.Commits(t.Context(), core.CommitQuery{
		Ref:   "main",
		Since: coretest.ContractNow.Add(-50 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits in the window, want 2", len(commits))
	}
	if commits[0].SHA != coretest.SHADeployed || commits[1].SHA != coretest.SHAMerged {
		t.Errorf("commits = %v, want the two newest", []string{commits[0].SHA, commits[1].SHA})
	}
	if commits[0].Author != "dev" {
		t.Errorf("Author = %q, want the login", commits[0].Author)
	}
}

func TestGitHubRefusesAComparisonItCannotRead(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	if _, err := adapter.Compare(t.Context(), "", coretest.SHAMerged); err == nil {
		t.Error("comparing against nothing was accepted")
	}
	if _, err := adapter.Compare(t.Context(), "nope", "also-nope"); err == nil {
		t.Error("comparing two commits that are not there was accepted")
	}
}

func TestGitHubStopsWhenAProviderLoopsItsOwnLinkHeader(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.loop = true
	adapter := newGitHubAdapter(t, srv, fx, nil)

	_, err := adapter.PullRequests(t.Context(), core.PRQuery{})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("PullRequests = %v, want it to give up on a provider that never ends", err)
	}
	if got := len(fake.Requests()); got != maxPages {
		t.Errorf("the walk made %d requests, want it bounded at %d", got, maxPages)
	}
}

func TestGitHubWritesARefWithSlashesAsOneRef(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	// The comparison cannot succeed — the fixture has no such branch. What
	// is being read here is the URL: a slash inside a ref is the URL's own
	// separator, and escaping it would make GitHub look for a branch
	// literally called "release%2F1.0".
	if _, err := adapter.Compare(t.Context(), "release/1.0", coretest.SHADeployed); err == nil {
		t.Fatal("comparing against a branch that is not there was accepted")
	}
	want := "/repos/" + fx.Repo.Owner + "/" + fx.Repo.Name + "/compare/release/1.0..." + coretest.SHADeployed
	if got := fake.Requests(); len(got) != 1 || got[0] != want {
		t.Errorf("the provider was asked for %v, want %q", got, want)
	}
}

func TestGitHubRefusesAFileWithNoPath(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	if _, err := adapter.File(t.Context(), "", "main"); err == nil {
		t.Error("reading a file with no path was accepted")
	}
}

func TestGitHubValidatesWithETagsOnTheSecondRun(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	cache := core.NewMemoryCache()
	adapter := newGitHubAdapter(t, srv, fx, func(o *core.ClientOptions) { o.Cache = cache })

	first, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	second, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo again: %v", err)
	}
	if second != first {
		t.Errorf("the cached answer differs: %+v vs %+v", second, first)
	}
	if fake.NotModified() != 1 {
		t.Errorf("%d conditional requests were answered 304, want 1", fake.NotModified())
	}
}

func TestGitHubIdentifiesItselfOnEveryRequest(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	if _, err := adapter.PullRequest(t.Context(), 1); err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.userAgents) != 1 {
		t.Fatalf("user agents = %v, want exactly one", fake.userAgents)
	}
	for agent := range fake.userAgents {
		if agent != core.UserAgent("v1.0.0") {
			t.Errorf("User-Agent = %q, want %q", agent, core.UserAgent("v1.0.0"))
		}
	}
}

func TestGitHubRefusesWhatItsPlanItemHasNotLanded(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	ctx := t.Context()
	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")

	calls := map[string]func() error{
		"Probe":            func() error { _, err := adapter.Probe(ctx); return err },
		"Protection":       func() error { _, err := adapter.Protection(ctx, "main"); return err },
		"SetProtection":    func() error { return adapter.SetProtection(ctx, "main", core.Protection{}) },
		"SetMergeSettings": func() error { return adapter.SetMergeSettings(ctx, core.MergeSettings{}) },
		"CommitStatuses":   func() error { _, err := adapter.CommitStatuses(ctx, "abc"); return err },
		"SetCommitStatus":  func() error { return adapter.SetCommitStatus(ctx, "abc", core.CommitStatus{}) },
		"UpsertComment":    func() error { return adapter.UpsertComment(ctx, 1, marker, "x") },
		"Comment":          func() error { _, err := adapter.Comment(ctx, 1, marker); return err },
		"DeleteComment":    func() error { return adapter.DeleteComment(ctx, 1, marker) },
		"ProposeFiles":     func() error { _, err := adapter.ProposeFiles(ctx, core.FileProposal{}); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if core.ExitCode(err) != core.ExitFail {
				t.Fatalf("%s = %v, want a refusal flow can exit on", name, err)
			}
			var user *core.UserError
			if !errors.As(err, &user) || user.What == "" || user.Fix == "" {
				t.Errorf("%s refused without explaining itself: %v", name, err)
			}
		})
	}
}

// numbers renders the pull request numbers for a message.
func numbers(prs []core.PullRequest) []int {
	out := make([]int, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pr.Number)
	}
	return out
}
