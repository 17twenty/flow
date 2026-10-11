package forge

import (
	"errors"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
		"the tickets a pull request closes",
		"a pull request that is not there",
		"pull requests in a window, newest first",
		"commits on the trunk, newest first",
		"ancestry proves a merge reached an environment",
		"files on the trunk",
		"a commit status replaces the last verdict from the same context",
		"a comment is edited in place, found by its marker, and deleted once",
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

func TestGitHubQualifiesALinkedTicketOnlyWhenItIsSomewhereElse(t *testing.T) {
	fx := coretest.ContractForge()
	fx.PullRequests[1].ClosingIssues = []string{"#11", "acme/other#7", "ACME/WIDGET#12"}
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	want := []string{"#11", "acme/other#7", "#12"}
	if !slices.Equal(pr.ClosingIssues, want) {
		t.Errorf("ClosingIssues = %v, want %v: the repository being spoken for is not named, and its name is not case-sensitive", pr.ClosingIssues, want)
	}
}

func TestGitHubFollowsTheCursorPastTheFirstPageOfLinkedTickets(t *testing.T) {
	fx := coretest.ContractForge()
	want := []string{"#11", "#12", "#13", "acme/other#7", "#15"}
	fx.PullRequests[1].ClosingIssues = want
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if !slices.Equal(pr.ClosingIssues, want) {
		t.Errorf("ClosingIssues = %v, want %v: a page size is not a ceiling", pr.ClosingIssues, want)
	}
	queries := 0
	for _, path := range fake.Requests() {
		if path == "/graphql" {
			queries++
		}
	}
	if queries < 2 {
		t.Errorf("%d GraphQL queries, want the walk to follow the cursor", queries)
	}
}

func TestGitHubReadsAGraphQLRefusalOutOfASuccessfulResponse(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	cases := map[string]struct {
		kind string
		want error
	}{
		"a pull request nobody can see": {kind: "NOT_FOUND", want: core.ErrNotFound},
		"a token without the scope":     {kind: "FORBIDDEN", want: core.ErrPermission},
		"a reason flow has no name for": {kind: "SOMETHING_NEW", want: core.ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake.graphQLRefusal(tc.kind, "the provider said no")
			_, err := adapter.PullRequest(t.Context(), 2)
			if !errors.Is(err, tc.want) {
				t.Fatalf("PullRequest = %v, want it to wrap %v", err, tc.want)
			}
			var user *core.UserError
			if !errors.As(err, &user) || !strings.Contains(user.What, "tickets") {
				t.Errorf("the refusal does not say what flow was reading: %v", err)
			}
			if !strings.Contains(user.Why, "the provider said no") {
				t.Errorf("Why = %q, want the provider's own message", user.Why)
			}
		})
	}
}

func TestGitHubDoesNotAskForLinksItsDeclarationCannotVerify(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	caps := gitHubCapabilities(t)
	caps.Capabilities = maps.Clone(caps.Capabilities)
	blind := caps.Capability(core.CapLinkedIssuesRead)
	blind.Verify = false
	caps.Capabilities[core.CapLinkedIssuesRead] = blind

	client, err := core.NewClient(core.ClientOptions{BaseURL: srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	adapter, err := NewGitHub(GitHubOptions{Client: client, Owner: fx.Repo.Owner, Repo: fx.Repo.Name, Capabilities: caps})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if len(pr.ClosingIssues) != 0 {
		t.Errorf("ClosingIssues = %v, want none: an adapter that cannot read the link says so instead of guessing", pr.ClosingIssues)
	}
	if slices.Contains(fake.Requests(), "/graphql") {
		t.Error("the adapter asked GraphQL for a link its declaration says it cannot read")
	}
}

func TestGitHubFindsTheGraphQLEndpointBesideTheRESTOne(t *testing.T) {
	cases := map[string]string{
		GitHubAPI:                         GitHubAPI + "/graphql",
		GitHubAPI + "/":                   GitHubAPI + "/graphql",
		"https://ghe.example.com/api/v3":  "https://ghe.example.com/api/graphql",
		"https://ghe.example.com/api/v3/": "https://ghe.example.com/api/graphql",
	}
	for base, want := range cases {
		if got := gitHubGraphQL(base); got != want {
			t.Errorf("gitHubGraphQL(%q) = %q, want %q", base, got, want)
		}
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

	calls := map[string]func() error{
		"Probe":            func() error { _, err := adapter.Probe(ctx); return err },
		"Protection":       func() error { _, err := adapter.Protection(ctx, "main"); return err },
		"SetProtection":    func() error { return adapter.SetProtection(ctx, "main", core.Protection{}) },
		"SetMergeSettings": func() error { return adapter.SetMergeSettings(ctx, core.MergeSettings{}) },
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

func TestGitHubDoesNotPublishAVerdictTheCommitAlreadyCarries(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	status := core.CommitStatus{Context: "flow/policy", State: core.StatusSuccess, Description: "✓ all rules pass"}

	for range 3 {
		if err := adapter.SetCommitStatus(t.Context(), coretest.SHADeployed, status); err != nil {
			t.Fatalf("SetCommitStatus: %v", err)
		}
	}
	if history := fake.statusHistory(coretest.SHADeployed); len(history) != 1 {
		t.Errorf("the commit carries %d statuses, want the one verdict said three times", len(history))
	}

	status.Description = "✗ 1 error — ticket.ready"
	status.State = core.StatusFailure
	if err := adapter.SetCommitStatus(t.Context(), coretest.SHADeployed, status); err != nil {
		t.Fatalf("SetCommitStatus(changed): %v", err)
	}
	history := fake.statusHistory(coretest.SHADeployed)
	if len(history) != 2 {
		t.Fatalf("the commit carries %d statuses, want the verdict to have changed", len(history))
	}

	statuses, err := adapter.CommitStatuses(t.Context(), coretest.SHADeployed)
	if err != nil {
		t.Fatalf("CommitStatuses: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("CommitStatuses = %+v, want the latest verdict per context, not the history", statuses)
	}
	if statuses[0].State != core.StatusFailure {
		t.Errorf("the verdict is %q, want the newest", statuses[0].State)
	}
}

func TestGitHubKeepsEveryContextsLatestVerdict(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.pageSize = 1
	adapter := newGitHubAdapter(t, srv, fx, nil)

	for _, status := range []core.CommitStatus{
		{Context: "ci/test", State: core.StatusFailure, Description: "one test failed"},
		{Context: "flow/policy", State: core.StatusPending, Description: "draft — not enforced"},
		{Context: "ci/test", State: core.StatusSuccess, Description: "all tests pass"},
	} {
		if err := adapter.SetCommitStatus(t.Context(), coretest.SHAHead, status); err != nil {
			t.Fatalf("SetCommitStatus(%s): %v", status.Context, err)
		}
	}
	statuses, err := adapter.CommitStatuses(t.Context(), coretest.SHAHead)
	if err != nil {
		t.Fatalf("CommitStatuses: %v", err)
	}
	got := map[string]string{}
	for _, status := range statuses {
		if _, repeated := got[status.Context]; repeated {
			t.Fatalf("CommitStatuses = %+v, want one verdict per context", statuses)
		}
		got[status.Context] = status.State
	}
	if got["ci/test"] != core.StatusSuccess || got["flow/policy"] != core.StatusPending {
		t.Errorf("statuses = %v, want ci/test's newest verdict and flow/policy's only one", got)
	}
}

func TestGitHubShortensADescriptionGitHubWouldRefuse(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	long := strings.Repeat("é", 400)
	if err := adapter.SetCommitStatus(t.Context(), coretest.SHADeployed, core.CommitStatus{
		Context: "flow/policy", State: core.StatusFailure, Description: long,
	}); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
	history := fake.statusHistory(coretest.SHADeployed)
	if len(history) != 1 {
		t.Fatalf("got %d statuses, want the long one published", len(history))
	}
	if got := len([]rune(history[0].Description)); got != 140 {
		t.Errorf("the description is %d characters, want it cut to GitHub's 140", got)
	}
	if !strings.HasSuffix(history[0].Description, "…") {
		t.Errorf("the description is %q, want it to show that it was cut", history[0].Description)
	}
}

func TestGitHubRefusesAStatusNoForgeCouldPublish(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	cases := map[string]core.CommitStatus{
		"no context":           {State: core.StatusSuccess},
		"no state":             {Context: "flow/policy"},
		"a state nobody knows": {Context: "flow/policy", State: "probably-fine"},
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			err := adapter.SetCommitStatus(t.Context(), coretest.SHAHead, status)
			if !errors.Is(err, core.ErrValidation) {
				t.Fatalf("SetCommitStatus = %v, want a refusal flow explains itself", err)
			}
		})
	}
	if got := len(fake.Requests()); got != 0 {
		t.Errorf("the provider was asked %d times about a status flow should never send", got)
	}
}

func TestGitHubLeavesCommentsItDidNotWriteAlone(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")

	fake.addComment(1, "dev", "I think this is fine.")
	fake.addComment(1, "flow", core.MarkBody("a follow-up", core.NewMarker(core.MarkerFollowUp, "check", "x"), true))

	if err := adapter.UpsertComment(t.Context(), 1, marker, "two rules failed"); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}
	if err := adapter.UpsertComment(t.Context(), 1, marker, "one rule failed"); err != nil {
		t.Fatalf("UpsertComment again: %v", err)
	}
	comments := fake.commentsOn(1)
	if len(comments) != 3 {
		t.Fatalf("the pull request has %d comments, want the two it had and one of flow's", len(comments))
	}
	if comments[0].Body != "I think this is fine." {
		t.Errorf("a person's comment was rewritten to %q", comments[0].Body)
	}
	if !strings.Contains(comments[1].Body, "a follow-up") {
		t.Errorf("flow's comment about something else was rewritten to %q", comments[1].Body)
	}
	if !strings.Contains(comments[2].Body, "one rule failed") {
		t.Errorf("the policy comment says %q, want the second body", comments[2].Body)
	}
	if !strings.Contains(comments[2].Body, marker.HTML()) {
		t.Errorf("the marker is not an HTML comment: %q", comments[2].Body)
	}

	if err := adapter.DeleteComment(t.Context(), 1, marker); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if left := fake.commentsOn(1); len(left) != 2 {
		t.Errorf("deleting flow's comment left %d comments, want the other two untouched", len(left))
	}
}

func TestGitHubDoesNotEditACommentThatAlreadySaysThis(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")

	for range 3 {
		if err := adapter.UpsertComment(t.Context(), 1, marker, "two rules failed"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
	}
	var written int
	for _, method := range fake.Methods() {
		if method == "POST" || method == "PATCH" {
			written++
		}
	}
	if written != 1 {
		t.Errorf("the comment was written %d times, want once: saying the same thing again is not an edit", written)
	}
}

func TestGitHubRefusesCommentsItCouldNotFindAgain(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	if err := adapter.UpsertComment(t.Context(), 1, core.Marker{}, "a body"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("UpsertComment with no marker = %v, want a refusal", err)
	}
	if _, err := adapter.Comment(t.Context(), 1, core.Marker{}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Comment with no marker = %v, want a refusal", err)
	}
}

func TestGitHubReportsAMissingPullRequestWhenCommentingOnIt(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "4040")

	if _, err := adapter.Comment(t.Context(), 4040, marker); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Comment on a pull request that is not there = %v, want ErrNotFound", err)
	}
	if err := adapter.DeleteComment(t.Context(), 4040, marker); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("DeleteComment on a pull request that is not there = %v, want ErrNotFound", err)
	}
}

func TestGitHubChangesOneRepositoryOneCallAtATime(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := "run " + strconv.Itoa(i)
			if err := adapter.UpsertComment(t.Context(), 1, marker, body); err != nil {
				t.Errorf("UpsertComment: %v", err)
			}
			status := core.CommitStatus{
				Context:     "flow/policy",
				State:       core.StatusFailure,
				Description: body,
			}
			if err := adapter.SetCommitStatus(t.Context(), coretest.SHADeployed, status); err != nil {
				t.Errorf("SetCommitStatus: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := fake.MostWriting(); got != 1 {
		t.Errorf("%d changes to the repository were in flight at once, want them serialised", got)
	}
	if comments := fake.commentsOn(1); len(comments) != 1 {
		t.Errorf("six concurrent runs left %d comments, want the one they all meant", len(comments))
	}
}
