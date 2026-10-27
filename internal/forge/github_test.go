package forge

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

// The whole forge contract now passes against GitHub's own shapes: every
// method it describes has landed (plan items 3.1 to 3.7).
func TestGitHubMeetsTheForgeContract(t *testing.T) {
	coretest.ForgeContract(t, contractGitHub)
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
	if pr.Files.Changed != want.Files.Changed || !slices.Equal(pr.Files.Paths, want.Files.Paths) {
		t.Errorf("Files = %+v, want %+v (paths sorted, counts from the pull request)", pr.Files, want.Files)
	}
	if !pr.MergedAt.Equal(want.MergedAt) || !pr.ClosedAt.Equal(want.ClosedAt) {
		t.Errorf("timeline = %v/%v, want %v/%v", pr.MergedAt, pr.ClosedAt, want.MergedAt, want.ClosedAt)
	}
}

// TestGitHubSaysTheNumberWillBeAppendedToTheTitle: GitHub serves no field for
// the header a squash would land, so the adapter composes the one the merge
// box offers under squash_merge_commit_title: PR_TITLE. The forge contract
// only asks that a header be reported and begin with the title, because
// GitLab appends nothing; what GitHub appends is GitHub's to assert.
func TestGitHubSaysTheNumberWillBeAppendedToTheTitle(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if want := fmt.Sprintf("%s (#%d)", pr.Title, pr.Number); pr.SquashHeader != want {
		t.Errorf("SquashHeader = %q, want %q: the number lands with the title and counts against the header length", pr.SquashHeader, want)
	}

	listed, err := adapter.PullRequests(t.Context(), core.PRQuery{State: core.PRStateMerged})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(listed) == 0 || listed[0].SquashHeader != pr.SquashHeader {
		t.Errorf("the listing says %+v and the pull request says %q", listed, pr.SquashHeader)
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
	if got := len(fake.Requests()); got != core.MaxPages {
		t.Errorf("the walk made %d requests, want it bounded at %d", got, core.MaxPages)
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

// numbers renders the pull request numbers for a message.
func numbers(prs []core.PullRequest) []int {
	out := make([]int, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pr.Number)
	}
	return out
}

func TestGitHubReadsBothProtectionMechanismsAsOne(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRule("main", map[string]any{
		"type": "required_status_checks",
		"parameters": map[string]any{
			"strict_required_status_checks_policy": true,
			"required_status_checks":               []any{map[string]any{"context": "ci/lint"}},
		},
	})
	fake.addRule("main", map[string]any{
		"type": "pull_request",
		"parameters": map[string]any{
			"required_approving_review_count":   2,
			"required_review_thread_resolution": true,
		},
	})
	fake.addRule("main", map[string]any{"type": "non_fast_forward"})
	fake.addRule("main", map[string]any{"type": "deletion"})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if !protection.RequiresCheck("ci/test") || !protection.RequiresCheck("ci/lint") {
		t.Errorf("RequiredChecks = %v, want both mechanisms' checks", protection.RequiredChecks)
	}
	if protection.RequiredApprovals != 2 {
		t.Errorf("RequiredApprovals = %d, want 2: the stricter of the two is what the branch really demands", protection.RequiredApprovals)
	}
	if !protection.StrictChecks || !protection.RequireConversationResolution {
		t.Errorf("protection = %+v, want the ruleset's demands carried", protection)
	}
	if protection.AllowForcePush || protection.AllowDeletions {
		t.Error("a branch a ruleset forbids force-pushing or deleting came back permitting it")
	}
}

func TestGitHubReportsABranchProtectedOnlyByARulesetAsProtected(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRule("release/1.0", map[string]any{"type": "required_linear_history"})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	protection, err := adapter.Protection(t.Context(), "release/1.0")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if !protection.Enabled || !protection.RequireLinearHistory {
		t.Errorf("Protection(release/1.0) = %+v, want the ruleset alone to count as protection", protection)
	}
	if !protection.AllowForcePush || !protection.AllowDeletions {
		t.Error("a rule that says nothing about force-pushing was read as forbidding it")
	}
	if !slices.Contains(fake.Requests(), "/repos/acme/widget/rules/branches/release/1.0") {
		t.Errorf("the branch was not asked about as one ref: %v", fake.Requests())
	}
}

func TestGitHubKeepsTheProtectionItWasNotAskedAbout(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Protections["main"] = core.Protection{
		Enabled:             true,
		RequiredChecks:      []string{"ci/test"},
		RequiredApprovals:   2,
		DismissStaleReviews: true,
		EnforceAdmins:       true,
	}
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	want := core.Protection{Enabled: true, RequiredChecks: []string{"flow/policy"}, RequireLinearHistory: true}
	if err := adapter.SetProtection(t.Context(), "main", want); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	after, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if !after.RequiresCheck("ci/test") || !after.RequiresCheck("flow/policy") {
		t.Errorf("RequiredChecks = %v, want both: GitHub's PUT replaces, so the adapter merges", after.RequiredChecks)
	}
	if after.RequiredApprovals != 2 || !after.DismissStaleReviews || !after.EnforceAdmins {
		t.Errorf("protection = %+v, want the settings flow never mentioned kept", after)
	}
	if !after.RequireLinearHistory {
		t.Error("SetProtection did not apply what it was asked for")
	}
}

// A ruleset that is written and never switched on is the one state both
// protection endpoints are honestly silent about: GitHub creates every
// ruleset `disabled`, and a disabled one is in nobody's effective rules, so
// the branch reads exactly like a branch nobody has thought about. None of it
// is protection and none of it is reported as any — but the remedy is a
// switch rather than a day's work, and a report that cannot tell the two
// apart sends somebody off to build what they already have.
func TestGitHubReportsARuleWrittenAndNeverSwitchedOn(t *testing.T) {
	fx := coretest.ContractForge()
	delete(fx.Protections, "main")
	fake, srv := newGitHubServer(t, fx)
	fake.addRuleset(ghFakeRuleset{
		name:        "Flow",
		target:      "branch",
		enforcement: "disabled",
		include:     []string{ghRefDefaultBranch},
	})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if protection.Enabled {
		t.Errorf("a ruleset nobody enabled was read as protection: %+v", protection)
	}
	want := []core.DormantRule{{
		Name:  "Flow",
		State: "disabled",
		URL:   "https://github.com/acme/widget/rules/1",
	}}
	if !slices.Equal(protection.Dormant, want) {
		t.Errorf("Dormant = %+v, want %+v", protection.Dormant, want)
	}
}

// `evaluate` is GitHub's dry run: the ruleset reports what it would have
// blocked and blocks nothing. For flow's purposes that is off, because the
// question flow asks is whether the trunk can be pushed to, not whether
// anybody would hear about it.
func TestGitHubCountsAnEvaluatedRuleAsNotInForce(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRuleset(ghFakeRuleset{
		name:        "Flow",
		target:      "branch",
		enforcement: "evaluate",
		include:     []string{"refs/heads/ma*"},
	})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if len(protection.Dormant) != 1 || protection.Dormant[0].State != "evaluate" {
		t.Errorf("Dormant = %+v, want the evaluated rule, in the forge's own word for it", protection.Dormant)
	}
}

// A ruleset in force is already in the branch's effective rules and has
// already been folded into the protection. Naming it again as something
// somebody ought to switch on would be advice to flick a switch that is
// already flicked, and the second request it would cost is one every
// repository would pay on every run.
func TestGitHubDoesNotCallARuleInForceDormant(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRuleset(ghFakeRuleset{
		name:        "Flow",
		target:      "branch",
		enforcement: "active",
		include:     []string{ghRefAll},
	})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if len(protection.Dormant) != 0 {
		t.Errorf("Dormant = %+v, want nothing: the rule is in force", protection.Dormant)
	}
	if slices.Contains(fake.Requests(), "/repos/acme/widget/rulesets/1") {
		t.Error("a rule already in force was fetched in full for nothing")
	}
}

// Whether a ruleset covers this branch is in the ruleset and not in the list
// of them, so flow has to ask. One that would never apply to the trunk is not
// an answer to a question about the trunk.
func TestGitHubIgnoresARuleThatWouldNotCoverTheBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ruleset ghFakeRuleset
	}{
		{"another branch", ghFakeRuleset{target: "branch", include: []string{"refs/heads/release/*"}}},
		{"everything but this one", ghFakeRuleset{
			target:  "branch",
			include: []string{ghRefAll},
			exclude: []string{"refs/heads/main"},
		}},
		{"tags", ghFakeRuleset{target: "tag", include: []string{ghRefAll}}},
		{"nothing at all", ghFakeRuleset{target: "branch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := coretest.ContractForge()
			fake, srv := newGitHubServer(t, fx)
			tc.ruleset.name, tc.ruleset.enforcement = "Flow", "disabled"
			fake.addRuleset(tc.ruleset)
			adapter := newGitHubAdapter(t, srv, fx, nil)

			protection, err := adapter.Protection(t.Context(), "main")
			if err != nil {
				t.Fatalf("Protection: %v", err)
			}
			if len(protection.Dormant) != 0 {
				t.Errorf("Dormant = %+v, want nothing: the rule does not cover main", protection.Dormant)
			}
		})
	}
}

// Reading the rules of a repository is a permission of its own, and a
// credential without it can still read the protection flow actually decides
// on. Sharpening a remedy is worth one request and no run at all.
func TestGitHubStillReportsProtectionWhenItMayNotReadTheRules(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fx := coretest.ContractForge()
			fake, srv := newGitHubServer(t, fx)
			fake.rulesetStatus = status
			adapter := newGitHubAdapter(t, srv, fx, nil)

			protection, err := adapter.Protection(t.Context(), "main")
			if err != nil {
				t.Fatalf("Protection: %v", err)
			}
			if !protection.Enabled || !protection.RequiresCheck("ci/test") {
				t.Errorf("Protection = %+v, want the protection that was readable", protection)
			}
		})
	}
}

// The patterns are GitHub's own, and flow reads them rather than guessing: a
// remedy pointing at a ruleset that would not have applied anyway is worse
// than no remedy, because it is somebody's afternoon.
func TestRulesetCoverageReadsGitHubsOwnPatterns(t *testing.T) {
	for _, tc := range []struct {
		name             string
		target           string
		include, exclude []string
		branch           string
		want             bool
	}{
		{name: "every ref", target: "branch", include: []string{ghRefAll}, branch: "main", want: true},
		{name: "a target the forge did not name", target: "", include: []string{ghRefAll}, branch: "main", want: true},
		{name: "the default branch", target: "branch", include: []string{ghRefDefaultBranch}, branch: "main", want: true},
		{name: "the default branch, asked about another", target: "branch", include: []string{ghRefDefaultBranch}, branch: "release/1.0", want: false},
		{name: "by name", target: "branch", include: []string{"refs/heads/main"}, branch: "main", want: true},
		{name: "by name, another branch", target: "branch", include: []string{"refs/heads/main"}, branch: "mane", want: false},
		{name: "a star inside a segment", target: "branch", include: []string{"refs/heads/rel*"}, branch: "release", want: true},
		{name: "a star does not cross a slash", target: "branch", include: []string{"refs/heads/release/*"}, branch: "release/1.0/hotfix", want: false},
		{name: "two stars are more than flow reads", target: "branch", include: []string{"refs/heads/release/**"}, branch: "release/1.0", want: false},
		{name: "a pattern that is not a branch", target: "branch", include: []string{"refs/tags/v*"}, branch: "v1", want: false},
		{name: "an exclusion beats an inclusion", target: "branch", include: []string{ghRefAll}, exclude: []string{"refs/heads/main"}, branch: "main", want: false},
		{name: "an exclusion of something else", target: "branch", include: []string{ghRefAll}, exclude: []string{"refs/heads/release/*"}, branch: "main", want: true},
		{name: "tags have nothing to say about a trunk", target: "tag", include: []string{ghRefAll}, branch: "main", want: false},
		{name: "no condition at all", target: "branch", include: nil, branch: "main", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ruleset ghRuleset
			ruleset.Target = tc.target
			ruleset.Conditions.RefName.Include = tc.include
			ruleset.Conditions.RefName.Exclude = tc.exclude

			if got := ruleset.covers(tc.branch, "main"); got != tc.want {
				t.Errorf("covers(%q) = %v, want %v", tc.branch, got, tc.want)
			}
		})
	}
}

func TestGitHubProtectsNothingTheBranchAlreadyProtects(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRule("main", map[string]any{
		"type": "required_status_checks",
		"parameters": map[string]any{
			"required_status_checks": []any{map[string]any{"context": "flow/policy"}},
		},
	})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	want := core.Protection{Enabled: true, RequiredChecks: []string{"flow/policy", "ci/test"}}
	if err := adapter.SetProtection(t.Context(), "main", want); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	if slices.Contains(fake.Methods(), "PUT") {
		t.Error("the adapter rewrote protection a ruleset already provides; a check required either way is required")
	}
}

// A rule nobody enabled changes nothing about what the branch requires, so it
// must not provoke a write. Protection is read, merged with what flow wants
// and written only if the merge differs — and a difference flow invented by
// carrying its own reading into the comparison would rewrite the trunk's
// protection every morning for nothing.
func TestGitHubDoesNotRewriteProtectionOverARuleNobodyEnabled(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.addRuleset(ghFakeRuleset{
		name:        "Flow",
		target:      "branch",
		enforcement: "disabled",
		include:     []string{ghRefDefaultBranch},
	})
	adapter := newGitHubAdapter(t, srv, fx, nil)

	want := core.Protection{Enabled: true, RequiredChecks: []string{"ci/test"}, RequiredApprovals: 1}
	if err := adapter.SetProtection(t.Context(), "main", want); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	if slices.Contains(fake.Methods(), "PUT") {
		t.Error("the trunk's protection was rewritten to say what it already said")
	}
}

func TestGitHubRefusesToTakeProtectionOff(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	err := adapter.SetProtection(t.Context(), "main", core.Protection{RequiredChecks: []string{"flow/policy"}})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("SetProtection(disabled) = %v, want it refused as validation", err)
	}
	if slices.Contains(fake.Methods(), "PUT") {
		t.Error("flow wrote protection while being asked to remove it")
	}
}

func TestGitHubRefusesMergeSettingsThatLeaveNoWayToMerge(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	err := adapter.SetMergeSettings(t.Context(), core.MergeSettings{DeleteBranchOnMerge: true})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("SetMergeSettings = %v, want it refused before anything was sent", err)
	}
	if slices.Contains(fake.Methods(), "PATCH") {
		t.Error("flow asked GitHub for a repository nobody could merge into")
	}
}

func TestGitHubReportsMergeSettingsTheProviderDidNotKeep(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.fixedMerge = true
	adapter := newGitHubAdapter(t, srv, fx, nil)

	err := adapter.SetMergeSettings(t.Context(), core.MergeSettings{AllowSquash: true, SquashTitle: core.SquashTitlePR})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("SetMergeSettings = %v, want a conflict: a write the provider ignored is not a write", err)
	}
}

// TestGitHubLeavesAnAutoMergeSettingNobodyAskedAbout.
//
// Every other merge setting is stated both ways round, because leaving one on
// is a way for a commit of the wrong shape to land. Auto-merge is the one
// flow only ever turns on: a configuration without `forge.auto_merge` asks
// for nothing (spec 06 §4), and a repository that permitted its pull requests
// to land themselves before flow ever saw it goes on permitting it. The write
// used to carry the field unconditionally, which made every `flow apply`
// quietly switch it off.
func TestGitHubLeavesAnAutoMergeSettingNobodyAskedAbout(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Repo.Merge.AllowAutoMerge = true
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	want := fx.Repo.Merge
	want.AllowAutoMerge = false // the team never mentioned it
	if err := adapter.SetMergeSettings(t.Context(), want); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}
	repo, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if !repo.Merge.AllowAutoMerge {
		t.Error("flow turned auto-merge off on a repository whose team had never asked it to")
	}
}

func TestSameProtectionComparesEveryFieldProtectionHas(t *testing.T) {
	kind := reflect.TypeOf(core.Protection{})
	for i := range kind.NumField() {
		field := kind.Field(i)
		changed := core.Protection{}
		value := reflect.ValueOf(&changed).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.String:
			value.SetString("x")
		case reflect.Bool:
			value.SetBool(true)
		case reflect.Int:
			value.SetInt(1)
		case reflect.Slice:
			// One non-zero element of whatever the slice holds. The point is
			// that the field differs, not what it differs by.
			element := reflect.New(field.Type.Elem()).Elem()
			switch element.Kind() {
			case reflect.String:
				element.SetString("x")
			case reflect.Struct:
				element.Field(0).SetString("x")
			default:
				t.Fatalf("Protection.%s holds %s, which this test cannot vary", field.Name, element.Kind())
			}
			value.Set(reflect.Append(value, element))
		default:
			t.Fatalf("Protection.%s is a %s, which sameProtection has no way to compare", field.Name, field.Type.Kind())
		}
		if sameProtection(core.Protection{}, changed) {
			t.Errorf("sameProtection ignores Protection.%s, so a difference in it would never be written", field.Name)
		}
	}
}

func TestGitHubProposesOnItsOwnBranchAndNeverTheTrunk(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	workflow := ".github/workflows/flow.yml"
	proposal := core.FileProposal{
		ID:    "files",
		Title: "chore(flow): add the policy workflow",
		Body:  "flow proposes this.",
		Files: []core.ProposedFile{{Path: workflow, Content: []byte("name: flow\non: [push]\n")}},
	}

	pr, err := adapter.ProposeFiles(t.Context(), proposal)
	if err != nil {
		t.Fatalf("ProposeFiles: %v", err)
	}
	if pr.HeadRef != "flow/setup-files" || pr.BaseRef != fx.Repo.DefaultBranch {
		t.Errorf("the proposal is %s → %s, want flow/setup-files → %s", pr.HeadRef, pr.BaseRef, fx.Repo.DefaultBranch)
	}
	if got := fake.fileOn(fx.Repo.DefaultBranch, workflow); !bytes.Equal(got, fx.Files[workflow]) {
		t.Errorf("the trunk now holds %q; flow never writes the default branch", got)
	}
	if got := fake.fileOn("flow/setup-files", workflow); !bytes.Equal(got, proposal.Files[0].Content) {
		t.Errorf("the branch holds %q, want the proposed content", got)
	}
}

func TestGitHubCommitsNothingWhenTheBranchAlreadyHoldsTheProposal(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	proposal := core.FileProposal{
		ID:    "files",
		Title: "chore(flow): add the policy workflow",
		Files: []core.ProposedFile{{Path: "docs/flow.md", Content: []byte("# flow\n")}},
	}
	if _, err := adapter.ProposeFiles(t.Context(), proposal); err != nil {
		t.Fatalf("ProposeFiles: %v", err)
	}

	before := len(fake.Methods())
	if _, err := adapter.ProposeFiles(t.Context(), proposal); err != nil {
		t.Fatalf("ProposeFiles again: %v", err)
	}
	for i, method := range fake.Methods()[before:] {
		if method == http.MethodPut {
			t.Errorf("the second proposal rewrote %s; the branch already held it", fake.Requests()[before+i])
		}
		if method == http.MethodPost {
			t.Errorf("the second proposal posted to %s; the branch and the pull request were already there", fake.Requests()[before+i])
		}
	}
}

func TestGitHubRefusesAProposalThatCouldNotBeOffered(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	file := core.ProposedFile{Path: "a.txt", Content: []byte("a")}

	cases := map[string]core.FileProposal{
		"no id":    {Title: "chore(flow): x", Files: []core.ProposedFile{file}},
		"no title": {ID: "files", Files: []core.ProposedFile{file}},
		"no files": {ID: "files", Title: "chore(flow): x"},
		"no path":  {ID: "files", Title: "chore(flow): x", Files: []core.ProposedFile{{Content: []byte("a")}}},
	}
	for name, proposal := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := adapter.ProposeFiles(t.Context(), proposal); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("ProposeFiles = %v, want it refused as validation", err)
			}
		})
	}
	if len(fake.Requests()) != 0 {
		t.Errorf("a proposal flow could not make was sent anyway: %v", fake.Requests())
	}
}

// TestGitHubLeavesAStatusWithNoIdentityWithout: a deployment record built out
// of a commit status points at it by its identity (spec 13 §1), and an
// identity of "0" points at nothing. A provider that did not give one has to
// read as not having given one.
// A GitHub Actions job reports as a check run, not a status, so a merge whose
// build ran is invisible to a reader of statuses alone (spec 20 §6). Every
// page is read, and each run reads as the verdict it amounts to.
func TestGitHubReadsTheCheckRunsOnACommit(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	fake.checkRuns = map[string][]ghCheckRun{"merge1": {
		{ID: 1, Name: "test", Status: "completed", Conclusion: "success", StartedAt: at, CompletedAt: at.Add(time.Minute)},
		{ID: 2, Name: "lint", Status: "completed", Conclusion: "failure", StartedAt: at},
		{ID: 3, Name: "release", Status: "in_progress", StartedAt: at},
		{ID: 4, Name: "docs", Status: "completed", Conclusion: "skipped", StartedAt: at},
		{ID: 5, Name: "deploy", Status: "completed", Conclusion: "cancelled", StartedAt: at},
	}}

	runs, err := adapter.CheckRuns(t.Context(), "merge1")
	if err != nil {
		t.Fatalf("CheckRuns: %v", err)
	}
	want := map[string]string{"test": core.StatusSuccess, "lint": core.StatusFailure, "release": core.StatusPending, "docs": core.StatusSuccess, "deploy": core.StatusFailure}
	if len(runs) != len(want) {
		t.Fatalf("read %d runs across the pages, want %d: %+v", len(runs), len(want), runs)
	}
	for _, run := range runs {
		if run.State != want[run.Context] {
			t.Errorf("%s reads as %q, want %q", run.Context, run.State, want[run.Context])
		}
	}
	if none, err := adapter.CheckRuns(t.Context(), "nothing-ran"); err != nil || len(none) != 0 {
		t.Errorf("a commit nothing ran on reads as %+v, %v; want none and no error", none, err)
	}
	var _ core.CheckRunReader = adapter
}

func TestGitHubLeavesAStatusWithNoIdentityWithout(t *testing.T) {
	if got := (ghStatus{ID: 4321, Context: "ci/deploy"}).toCommitStatus().ID; got != "4321" {
		t.Errorf("the identity is %q, want the provider's own", got)
	}
	if got := (ghStatus{Context: "ci/deploy"}).toCommitStatus().ID; got != "" {
		t.Errorf("a status the provider did not identify came back as %q, want nothing", got)
	}
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

// probeWith stands a fake up with one role, one set of classic scopes — nil
// for a credential that reports none — and one visibility, and probes it.
func probeWith(t *testing.T, role ghRepoPermissions, scopes *string, public bool) core.Permissions {
	t.Helper()
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.role, fake.scopes, fake.public = role, scopes, public
	permissions, err := newGitHubAdapter(t, srv, fx, nil).Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return permissions
}

func scopeHeader(scopes string) *string { return &scopes }

// The roles GitHub reports, as it reports them: the whole implied set.
var (
	roleAdmin    = ghRepoPermissions{Admin: true, Maintain: true, Push: true, Triage: true, Pull: true}
	roleWriter   = ghRepoPermissions{Push: true, Triage: true, Pull: true}
	roleReadOnly = ghRepoPermissions{Pull: true}
)

func TestGitHubProbeSpeaksTheVocabularyTheDeclarationRequires(t *testing.T) {
	permissions := probeWith(t, roleAdmin, scopeHeader("repo, project"), false)
	for name, capability := range gitHubCapabilities(t).Capabilities {
		if missing := permissions.Missing(capability.Requires); len(missing) > 0 {
			t.Errorf("an admin's probe is missing %v, which %s requires; a probe and a declaration that do not share a vocabulary can never agree", missing, name)
		}
	}
}

func TestGitHubProbeCostsOneRequest(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	if _, err := newGitHubAdapter(t, srv, fx, nil).Probe(t.Context()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := len(fake.Requests()); got > 5 {
		t.Errorf("Probe made %d requests (%v), want at most the five spec 05 §3 allows", got, fake.Requests())
	}
}

func TestGitHubProbeReportsTheRoleItWasGiven(t *testing.T) {
	cases := []struct {
		name  string
		role  ghRepoPermissions
		want  []string
		wont  []string
		scope *string
	}{
		{
			name:  "an admin",
			role:  roleAdmin,
			want:  []string{"admin", "maintain", "push", "statuses_write", "contents_write", "pulls_write"},
			scope: scopeHeader("repo"),
		},
		{
			name:  "a writer",
			role:  roleWriter,
			want:  []string{"push", "triage", "pull", "statuses_write", "pulls_write"},
			wont:  []string{"admin", "maintain"},
			scope: scopeHeader("repo"),
		},
		{
			// An older GitHub Enterprise Server sends only the
			// roles it knew about, so the implication an admin
			// carries has to be flow's as well as GitHub's.
			name:  "an admin on a server that reports nothing else",
			role:  ghRepoPermissions{Admin: true},
			want:  []string{"admin", "maintain", "push", "triage", "pull", "statuses_write"},
			scope: scopeHeader("repo"),
		},
		{
			name:  "a reader",
			role:  roleReadOnly,
			want:  []string{"pull"},
			wont:  []string{"admin", "maintain", "push", "statuses_write", "contents_write", "pulls_write"},
			scope: scopeHeader("repo"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			permissions := probeWith(t, tc.role, tc.scope, false)
			if missing := permissions.Missing(tc.want); len(missing) > 0 {
				t.Errorf("%s was not granted %v; probe reported %s", tc.name, missing, permissions)
			}
			for _, token := range tc.wont {
				if permissions.Has(token) {
					t.Errorf("%s was granted %q", tc.name, token)
				}
			}
		})
	}
}

func TestGitHubProbeTakesTheScopeAndTheRoleTogether(t *testing.T) {
	cases := []struct {
		name   string
		scopes *string
		public bool
		writes bool
	}{
		{name: "a classic token scoped for every repository", scopes: scopeHeader("repo, project"), writes: true},
		{name: "a classic token scoped for public repositories only", scopes: scopeHeader("public_repo"), public: true, writes: true},
		{name: "that same token against a private repository", scopes: scopeHeader("public_repo"), writes: false},
		{name: "a classic token that may only read", scopes: scopeHeader("read:org, read:project"), writes: false},
		{name: "a classic token with no scopes at all", scopes: scopeHeader(""), writes: false},
		{name: "a credential that reports no scopes", scopes: nil, writes: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			permissions := probeWith(t, roleAdmin, tc.scopes, tc.public)
			if !permissions.Has("admin") {
				t.Error("the role was lost; a scope limits what the role may be used for, it does not replace it")
			}
			if got := permissions.Has("statuses_write"); got != tc.writes {
				t.Errorf("%s may write: %v, want %v (reported: %s)", tc.name, got, tc.writes, permissions)
			}
		})
	}
}

func TestGitHubProbeSaysWhatItRead(t *testing.T) {
	permissions := probeWith(t, roleAdmin, scopeHeader("repo, project"), false)
	joined := strings.Join(permissions.Evidence, "\n")
	for _, want := range []string{"repository permissions: admin", "X-OAuth-Scopes: repo, project"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the evidence is %q, want it to carry %q so a refusal can say what flow saw", joined, want)
		}
	}

	quiet := probeWith(t, roleAdmin, nil, false)
	if slices.ContainsFunc(quiet.Evidence, func(line string) bool { return strings.Contains(line, "X-OAuth-Scopes") }) {
		t.Errorf("a credential that sent no scope header was given one as evidence: %v", quiet.Evidence)
	}
}

func TestGitHubProbeReportsTheScopesThemselves(t *testing.T) {
	permissions := probeWith(t, roleAdmin, scopeHeader("repo, read:project"), false)
	for _, scope := range []string{"repo", "read:project"} {
		if !permissions.Has(scope) {
			t.Errorf("the probe did not report the %q scope, which a declaration may require by name (spec 05 §3)", scope)
		}
	}
}

func TestGitHubProbeExplainsARepositoryItCannotRead(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)
	adapter.repo = "missing"
	_, err := adapter.Probe(t.Context())
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Probe of a repository that is not there = %v, want ErrNotFound", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.What, "acme/missing") {
		t.Errorf("the refusal is %q, want it to name the repository it could not read", err)
	}
}

// An internal repository on an enterprise is neither private nor public, and
// GitHub says so in `visibility` while `private` can only say false. A
// `public_repo` scope does not cover it, so the newer field has to win.
func TestGitHubProbeReadsVisibilityRatherThanPrivacy(t *testing.T) {
	cases := []struct {
		name  string
		probe ghProbe
		want  bool
	}{
		{name: "a public repository", probe: ghProbe{Visibility: "public"}, want: true},
		{name: "a private repository", probe: ghProbe{Private: true, Visibility: "private"}},
		{name: "an internal repository", probe: ghProbe{Visibility: "internal"}},
		{name: "a server too old to report visibility", probe: ghProbe{}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.probe.public(); got != tc.want {
				t.Errorf("%s counts as public: %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestGitHubReadsABranchThatOnlyFellBehindAsExactlyThat: GitHub says a pull
// request whose base has moved is still `mergeable`, because nothing
// conflicts — it is the strict required status checks that will not take it
// until it is level. An adapter reading the bool and ignoring the state would
// call it clean; one reading the state as a conflict would send somebody to
// resolve one that is not there.
func TestGitHubReadsABranchThatOnlyFellBehindAsExactlyThat(t *testing.T) {
	fx := coretest.ContractForge()
	fx.PullRequests[0].Mergeability = core.MergeabilityBehind
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Mergeability != core.MergeabilityBehind {
		t.Fatalf("Mergeability = %q, want %q", pr.Mergeability, core.MergeabilityBehind)
	}
	why, stuck := pr.Stuck()
	if !stuck || !strings.Contains(why, "level") {
		t.Errorf("Stuck() = %q/%v, want it to say the branch has to be level with its base", why, stuck)
	}
}

// TestGitHubDoesNotReadSilenceAsApproval: GitHub computes mergeability in the
// background and answers null until it has. An adapter that read that as
// "nothing is in the way" would make the check that asks fail hardest on the
// pull request somebody opened a moment ago.
func TestGitHubDoesNotReadSilenceAsApproval(t *testing.T) {
	fx := coretest.ContractForge()
	fx.PullRequests[0].Mergeability = core.MergeabilityUnknown
	_, srv := newGitHubServer(t, fx)
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Mergeability != core.MergeabilityUnknown {
		t.Errorf("Mergeability = %q, want nothing at all: GitHub said null", pr.Mergeability)
	}
	if _, stuck := pr.Stuck(); stuck {
		t.Error("a pull request GitHub has not finished looking at reads as stuck")
	}
}

// TestGitHubTrustsTheBoolOverAStateItHasNoNameFor: `mergeable_state` grows
// values with GitHub's features, and one flow has never heard of alongside
// `mergeable: false` is still a refusal as far as anybody acting on it is
// concerned.
func TestGitHubTrustsTheBoolOverAStateItHasNoNameFor(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.mergeableState = "some_new_thing"
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Mergeability != core.MergeabilityConflicted {
		t.Errorf("Mergeability = %q, want %q: GitHub said it does not merge", pr.Mergeability, core.MergeabilityConflicted)
	}
}

// TestGitHubDoesNotReadThroughANullItWasHandedAnyway: GitHub sends
// `mergeable: null` while it works the answer out, and does not promise the
// state beside it is `unknown` while it does. A mapping that reached for the
// bool because the state was a word it did not recognise would panic on a
// payload the provider is entitled to send.
func TestGitHubDoesNotReadThroughANullItWasHandedAnyway(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.mergeableState, fake.mergeableNull = "some_new_thing", true
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Mergeability != core.MergeabilityUnknown {
		t.Errorf("Mergeability = %q, want nothing at all: GitHub answered null", pr.Mergeability)
	}
}

// TestGitHubBelievesAConflictEvenWhileItIsStillThinking: `dirty` is a
// definite answer, and GitHub sends it next to a null bool while it recomputes
// after a push to the base. Reading the pair as "no answer yet" would hide the
// conflict for exactly as long as it takes somebody to stop looking.
func TestGitHubBelievesAConflictEvenWhileItIsStillThinking(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitHubServer(t, fx)
	fake.mergeableState, fake.mergeableNull = "dirty", true
	adapter := newGitHubAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Mergeability != core.MergeabilityConflicted {
		t.Errorf("Mergeability = %q, want %q", pr.Mergeability, core.MergeabilityConflicted)
	}
}

// TestGitHubReadsBeforeItArms: the gate runs on every push, and an arming is
// a mutation. GitHub answers the pending request in the same round trip as
// the node id the mutation needs, so flow already knows before it asks — and
// a second arming that cost nothing would still be a write in the audit log
// of every repository flow watches.
func TestGitHubReadsBeforeItArms(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Repo.Merge.AllowAutoMerge = true
	fx.PullRequests[0].Draft = false
	fake, srv := newGitHubServer(t, fx)
	github := newGitHubAdapter(t, srv, fx, nil)

	for range 3 {
		if err := github.ArmAutoMerge(t.Context(), 1, core.MergeMethodSquash); err != nil {
			t.Fatalf("ArmAutoMerge: %v", err)
		}
	}
	if fake.armings != 1 {
		t.Errorf("flow armed #1 %d times, want 1", fake.armings)
	}
}

// TestGitHubArmsWithTheStrategyItWasAsked: the arming says how the pull
// request is to land, and GitHub holds it until the checks pass. A promise
// made with the wrong strategy is a commit of the wrong shape on the trunk,
// published by nobody, hours after the last person looked — which is the one
// failure auto-merge could introduce that a person merging never would.
func TestGitHubArmsWithTheStrategyItWasAsked(t *testing.T) {
	for _, tc := range []struct{ asked, sent string }{
		{core.MergeMethodSquash, "SQUASH"},
		{core.MergeMethodMerge, "MERGE"},
		{core.MergeMethodRebase, "REBASE"},
	} {
		t.Run(tc.asked, func(t *testing.T) {
			fx := coretest.ContractForge()
			fx.Repo.Merge.AllowAutoMerge = true
			fx.PullRequests[0].Draft = false
			fake, srv := newGitHubServer(t, fx)
			github := newGitHubAdapter(t, srv, fx, nil)

			if err := github.ArmAutoMerge(t.Context(), 1, tc.asked); err != nil {
				t.Fatalf("ArmAutoMerge: %v", err)
			}
			if got := fake.armed[1]; got != tc.sent {
				t.Errorf("flow promised to land #1 as %q, want %q: GitHub takes the strategy with the arming and uses it when nobody is watching", got, tc.sent)
			}
		})
	}
}

// TestGitHubReportsARefusalOnlyGitHubCouldMake: every condition that stops an
// arming comes back as one error type, and flow reads it as the request being
// wrong rather than the provider being unwell. A caller that could not tell
// the two apart would either fail a gate over a repository setting or retry a
// draft for ever.
func TestGitHubReportsARefusalOnlyGitHubCouldMake(t *testing.T) {
	fx := coretest.ContractForge() // auto-merge is off on the repository
	fx.PullRequests[0].Draft = false
	_, srv := newGitHubServer(t, fx)
	github := newGitHubAdapter(t, srv, fx, nil)

	err := github.ArmAutoMerge(t.Context(), 1, core.MergeMethodSquash)
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("ArmAutoMerge = %v, want %v", err, core.ErrValidation)
	}
	if !strings.Contains(err.Error(), "not allowed for this repository") {
		t.Errorf("the refusal drops what GitHub said about it:\n%v", err)
	}
	if err := github.ArmAutoMerge(t.Context(), 404, core.MergeMethodSquash); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("arming a pull request GitHub does not have = %v, want %v", err, core.ErrNotFound)
	}
}
