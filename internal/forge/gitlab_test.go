package forge

import (
	"errors"
	"net/http"
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

// gitLabCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func gitLabCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(GitLabKind)
	if err != nil {
		t.Fatalf("Of(gitlab): %v", err)
	}
	return set
}

// newGitLabAdapter builds an adapter talking to srv.
//
// The base URL carries [GitLabAPIPath], because that is what the root hands
// the client: a GitLab lives wherever somebody put it, and the API root is
// the instance's URL with that path on the end.
func newGitLabAdapter(t *testing.T, srv *httptest.Server, fx coretest.ForgeFixture, adjust func(*core.ClientOptions)) *GitLab {
	t.Helper()
	opts := core.ClientOptions{
		BaseURL: srv.URL + GitLabAPIPath,
		Token:   gitLabTestToken,
		Version: "v1.0.0",
		Header:  GitLabHeader(),
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
	adapter, err := NewGitLab(GitLabOptions{
		Client:       client,
		Owner:        fx.Repo.Owner,
		Repo:         fx.Repo.Name,
		Capabilities: gitLabCapabilities(t),
	})
	if err != nil {
		t.Fatalf("NewGitLab: %v", err)
	}
	return adapter
}

// contractGitLab stands a fake GitLab up in front of the fixture.
func contractGitLab(t *testing.T, fx coretest.ForgeFixture) core.Forge {
	t.Helper()
	_, srv := newGitLabServer(t, fx)
	return newGitLabAdapter(t, srv, fx, nil)
}

// gitLabFixture stands the contract's world up and hands back both ends of
// it, for the tests that are about GitLab in particular.
func gitLabFixture(t *testing.T) (*gitLabFake, *GitLab) {
	t.Helper()
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)
	return fake, newGitLabAdapter(t, srv, fx, nil)
}

// countGitLabRequests is how many times a path was asked for.
func countGitLabRequests(fake *gitLabFake, path string) int {
	var n int
	for _, asked := range fake.Requests() {
		if asked == path {
			n++
		}
	}
	return n
}

// countGitLabCalls is how many times a path was asked for with a method.
func countGitLabCalls(fake *gitLabFake, method, path string) int {
	var n int
	requests, methods := fake.Requests(), fake.Methods()
	for i, asked := range requests {
		if asked == path && methods[i] == method {
			n++
		}
	}
	return n
}

// The whole forge contract against GitLab's own shapes. An adapter that
// passes it can be swapped for GitHub's or Gitea's, which is the point of
// there being an interface (spec 14).
func TestGitLabMeetsTheForgeContract(t *testing.T) {
	coretest.ForgeContract(t, contractGitLab)
}

func TestNewGitLabRefusesAnIncompleteAdapter(t *testing.T) {
	caps := gitLabCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: GitLabHost + GitLabAPIPath})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct {
		name string
		opts GitLabOptions
	}{
		{"no client", GitLabOptions{Owner: "a", Repo: "b", Capabilities: caps}},
		{"no namespace", GitLabOptions{Client: client, Repo: "b", Capabilities: caps}},
		{"no project", GitLabOptions{Client: client, Owner: "a", Capabilities: caps}},
		{"no declaration", GitLabOptions{Client: client, Owner: "a", Repo: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewGitLab(tc.opts); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

// A project is one path segment with the slash encoded, not two. Addressing
// it as two would read some other project, or none: GitLab's namespaces
// nest, and `acme/widget` is a name rather than a pair.
func TestGitLabAddressesAProjectByItsEncodedPath(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	if _, err := gitlab.Repo(t.Context()); err != nil {
		t.Fatalf("Repo: %v", err)
	}
	const want = "/api/v4/projects/acme%2Fwidget"
	if got := fake.Requests(); !slices.Contains(got, want) {
		t.Errorf("the project was read as %v, want %s with the slash encoded", got, want)
	}
}

// A call that does not say who it is must be refused, and the adapter must
// never make one. A fake that answered anybody would prove nothing about what
// flow sends.
func TestGitLabSaysWhoItIs(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)

	anonymous := newGitLabAdapter(t, srv, fx, func(o *core.ClientOptions) { o.Token = "" })
	if _, err := anonymous.Repo(t.Context()); err == nil {
		t.Fatal("a call carrying no credential was answered")
	}
	if fake.Anonymous() != 1 {
		t.Errorf("the fake refused %d anonymous calls, want 1", fake.Anonymous())
	}

	// And one that asks for something other than JSON is refused too, so
	// that the Accept header the adapter sends is load-bearing.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+GitLabAPIPath+"/projects/acme%2Fwidget", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+gitLabTestToken)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "flow-test-probe")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotAcceptable {
		t.Errorf("a call asking for text/html was answered %d, want 406", res.StatusCode)
	}

	credentialled := newGitLabAdapter(t, srv, fx, nil)
	if _, err := credentialled.Repo(t.Context()); err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if fake.Anonymous() != 1 || fake.Unacceptable() != 1 {
		t.Errorf("the adapter's own call was refused: %d anonymous, %d unacceptable", fake.Anonymous(), fake.Unacceptable())
	}
	for agent := range fake.Agents() {
		if agent != "" && !strings.Contains(agent, "flow") {
			t.Errorf("a call identified itself as %q, which does not name flow", agent)
		}
	}
}

// GitLab pages with `page` and `per_page` and links the next page in the Link
// header. A reader of only the first page would quietly report two of three
// merge requests, and an audit would be wrong rather than short.
func TestGitLabWalksEveryPage(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	if fake.pageSize >= len(coretest.ContractForge().PullRequests) {
		t.Fatalf("the fake pages %d at a time, which is not smaller than the fixture", fake.pageSize)
	}

	prs, err := gitlab.PullRequests(t.Context(), core.PRQuery{})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d merge requests, want all 3: the walk stopped at a page boundary", len(prs))
	}
	const listing = "/api/v4/projects/acme%2Fwidget/merge_requests"
	if got := countGitLabRequests(fake, listing); got != 2 {
		t.Errorf("the listing was asked for %d times, want 2 pages for 3 merge requests", got)
	}

	commits, err := gitlab.Commits(t.Context(), core.CommitQuery{Ref: "main"})
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if len(commits) != 3 {
		t.Errorf("got %d commits, want all 3", len(commits))
	}

	// A limit stops the walk rather than filtering what it read: asking
	// for one merge request must not cost a second page.
	before := countGitLabRequests(fake, listing)
	one, err := gitlab.PullRequests(t.Context(), core.PRQuery{Limit: 1})
	if err != nil {
		t.Fatalf("PullRequests(limit 1): %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("a limit of 1 returned %d merge requests", len(one))
	}
	if got := countGitLabRequests(fake, listing) - before; got != 1 {
		t.Errorf("a limit of 1 read %d pages, want 1", got)
	}
}

// The listing is answered by the query, not by the adapter: GitLab filters by
// state, target branch and window itself, and flow must send all three.
func TestGitLabListingIsAnsweredByTheQuery(t *testing.T) {
	_, gitlab := gitLabFixture(t)
	since := coretest.ContractNow.Add(-14 * 24 * time.Hour)

	merged, err := gitlab.PullRequests(t.Context(), core.PRQuery{State: core.PRStateMerged, Base: "main", UpdatedSince: since})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if len(merged) != 1 || merged[0].Number != 2 {
		t.Fatalf("got %v, want the one merged merge request in the window", gitLabNumbers(merged))
	}
	if merged[0].MergeSHA != coretest.SHAMerged {
		t.Errorf("the merged merge request landed %q, want %q", merged[0].MergeSHA, coretest.SHAMerged)
	}

	elsewhere, err := gitlab.PullRequests(t.Context(), core.PRQuery{Base: "release"})
	if err != nil {
		t.Fatalf("PullRequests(base release): %v", err)
	}
	if len(elsewhere) != 0 {
		t.Errorf("a query for merge requests into release returned %v", gitLabNumbers(elsewhere))
	}
}

// gitLabNumbers names the merge requests in a listing.
func gitLabNumbers(prs []core.PullRequest) []int {
	out := make([]int, 0, len(prs))
	for _, pr := range prs {
		out = append(out, pr.Number)
	}
	return out
}

// The query is where the question is asked, not the answer. GitLab filters a
// listing itself, pages it 100 at a time at most, and orders it; an adapter
// that left any of that out would read every merge request the project ever
// had and filter them in flow.
func TestGitLabAsksTheQuestionInTheQuery(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	since := coretest.ContractNow.Add(-14 * 24 * time.Hour)
	if _, err := gitlab.PullRequests(t.Context(), core.PRQuery{
		State:        core.PRStateMerged,
		Base:         "main",
		UpdatedSince: since,
	}); err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	query, asked := fake.Query("/api/v4/projects/acme%2Fwidget/merge_requests", 0)
	if !asked {
		t.Fatal("the listing was never read")
	}
	want := map[string]string{
		"state":         "merged",
		"target_branch": "main",
		"updated_after": since.UTC().Format(time.RFC3339),
		"order_by":      "updated_at",
		"sort":          "desc",
		"per_page":      "100",
	}
	for name, value := range want {
		if got := query.Get(name); got != value {
			t.Errorf("the listing asked %s=%q, want %q", name, got, value)
		}
	}

	// And one merge request is read once, not once per thing read off it.
	const one = "/api/v4/projects/acme%2Fwidget/merge_requests/2"
	if _, err := gitlab.PullRequest(t.Context(), 2); err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	if got := countGitLabRequests(fake, one); got != 1 {
		t.Errorf("the merge request was read %d times for one reading of it, want 1", got)
	}
}

// The tickets a merge request closes come from GitLab's own endpoint for
// them, and from nowhere else. A ticket in the project being spoken for is
// written as everybody writes it; one anywhere else is qualified, because
// `#7` there and `#7` here are different tickets (P12, spec 04 §3).
func TestGitLabReadsTheLinkTheForgeKeeps(t *testing.T) {
	fx := coretest.ContractForge()
	// A description that names a ticket the forge does not link. flow must
	// not read it: a guess is worse than the tracker fallback it silences.
	fx.PullRequests[1].Body = "Closes #99\n\nand mentions #98."
	fake, srv := newGitLabServer(t, fx)
	gitlab := newGitLabAdapter(t, srv, fx, nil)

	pr, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	if !slices.Equal(pr.ClosingIssues, []string{"#11", "acme/other#7"}) {
		t.Errorf("ClosingIssues = %v, want [#11 acme/other#7]", pr.ClosingIssues)
	}
	const closes = "/api/v4/projects/acme%2Fwidget/merge_requests/2/closes_issues"
	if got := countGitLabRequests(fake, closes); got != 1 {
		t.Errorf("closes_issues was read %d times for one merge request, want 1", got)
	}
}

// An approval is not a review. GitLab keeps one row per reviewer with their
// latest verdict and a separate list of approvers, and somebody can approve
// without ever having been asked to review. Reading one and not the other
// would lose an approval the gate is counting.
func TestGitLabCountsApproversWhoWereNeverReviewers(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)
	fake.approvers[2] = []string{"release-bot", "dev"}
	gitlab := newGitLabAdapter(t, srv, fx, nil)

	pr, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	approvals := pr.Approvals()
	slices.Sort(approvals)
	if !slices.Equal(approvals, []string{"dev", "release-bot"}) {
		t.Errorf("Approvals() = %v, want [dev release-bot]", approvals)
	}
	var reviews int
	for _, review := range pr.Reviews {
		if review.Author == "dev" {
			reviews++
		}
	}
	if reviews != 1 {
		t.Errorf("dev reviewed once and approved once and came back %d times; an approver already seen is the same person", reviews)
	}
	for _, review := range pr.Reviews {
		if review.SubmittedAt.IsZero() {
			t.Errorf("%s's review says nothing about when it was submitted", review.Author)
		}
	}
	if !slices.IsSortedFunc(pr.Reviews, func(a, b core.Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) }) {
		t.Error("the reviews came back out of order; a later verdict replaces an earlier one")
	}
}

// A merge request says the title will be the squashed commit's header, with
// nothing appended. GitHub and Gitea append the number; GitLab's default
// squash template is the title alone, and a gate told otherwise would judge a
// header that never lands.
func TestGitLabSquashHeaderIsTheTitleAlone(t *testing.T) {
	_, gitlab := gitLabFixture(t)
	pr, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	if pr.SquashHeader != pr.Title {
		t.Errorf("SquashHeader = %q, want the title alone, %q", pr.SquashHeader, pr.Title)
	}
	if pr.MergeHeader() != pr.Title {
		t.Errorf("MergeHeader() = %q, want %q", pr.MergeHeader(), pr.Title)
	}
}

// A branch's protection is read from three places at once: the protected
// branch, the approval rules and the external status checks. Two of the three
// are paid features, and a project without them is still protected.
func TestGitLabReadsProtectionFromEveryPlaceItIsKept(t *testing.T) {
	fake, gitlab := gitLabFixture(t)

	main, err := gitlab.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection(main): %v", err)
	}
	if !main.Enabled {
		t.Fatal("main is protected and Protection says it is not")
	}
	if !slices.Equal(main.RequiredChecks, []string{"ci/test"}) {
		t.Errorf("RequiredChecks = %v, want [ci/test] from the external status checks", main.RequiredChecks)
	}
	if main.RequiredApprovals != 1 {
		t.Errorf("RequiredApprovals = %d, want 1 from the approval rules", main.RequiredApprovals)
	}
	if main.StrictChecks {
		t.Error("the project does not insist a pipeline succeeds and Protection says it does")
	}
	if main.RequireLinearHistory {
		t.Error("the project merges with a merge commit, so the history is not linear")
	}

	// The project's own two settings are properties of the project and
	// not of the branch, which is why they are reported for a branch that
	// is not protected at all.
	fake.mu.Lock()
	fake.project.PipelineMustSucceed = true
	fake.project.MergeMethod = gitLabMergeFastForward
	fake.mu.Unlock()

	loose, err := gitlab.Protection(t.Context(), "some-branch")
	if err != nil {
		t.Fatalf("Protection(some-branch): %v", err)
	}
	if loose.Enabled {
		t.Error("an unprotected branch came back protected")
	}
	if !loose.StrictChecks || !loose.RequireLinearHistory {
		t.Errorf("Protection(some-branch) = %+v, want the project's own pipeline gate and linear history", loose)
	}
}

// Protecting a branch that is not protected creates the protection, and asks
// for the approvals in a rule of flow's own so that a later run edits that
// rule rather than adding another beside it.
func TestGitLabProtectsABranchThatWasNot(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	if err := gitlab.SetProtection(t.Context(), "release", core.Protection{
		Enabled:           true,
		RequiredApprovals: 2,
	}); err != nil {
		t.Fatalf("SetProtection(release): %v", err)
	}

	fake.mu.Lock()
	branch, protected := fake.protected["release"]
	rules := slices.Clone(fake.rules)
	fake.mu.Unlock()
	if !protected {
		t.Fatal("release is still unprotected")
	}
	if branch.PushAccessLevels[0].AccessLevel != gitLabNobody {
		t.Errorf("push access is %d, want nobody: a protected branch is written through a merge request",
			branch.PushAccessLevels[0].AccessLevel)
	}
	if branch.MergeAccessLevels[0].AccessLevel != gitLabDeveloper {
		t.Errorf("merge access is %d, want developer", branch.MergeAccessLevels[0].AccessLevel)
	}
	var flows int
	for _, rule := range rules {
		if rule.Name != gitLabApprovalRule {
			continue
		}
		flows++
		if rule.ApprovalsRequired != 2 {
			t.Errorf("flow's rule asks for %d approvals, want 2", rule.ApprovalsRequired)
		}
		if rule.RuleType != gitLabAnyApprover {
			t.Errorf("flow's rule is %q, want %q: flow names no approver", rule.RuleType, gitLabAnyApprover)
		}
	}
	if flows != 1 {
		t.Fatalf("there are %d rules named %q, want 1", flows, gitLabApprovalRule)
	}

	// Asking again changes nothing, and adds no second rule.
	if err := gitlab.SetProtection(t.Context(), "release", core.Protection{
		Enabled:           true,
		RequiredApprovals: 2,
	}); err != nil {
		t.Fatalf("SetProtection(release) again: %v", err)
	}
	fake.mu.Lock()
	again := len(fake.rules)
	fake.mu.Unlock()
	if again != len(rules) {
		t.Errorf("asking for the same protection again left %d rules, want %d", again, len(rules))
	}

	after, err := gitlab.Protection(t.Context(), "release")
	if err != nil {
		t.Fatalf("Protection(release): %v", err)
	}
	if !after.Enabled || after.RequiredApprovals != 2 {
		t.Errorf("Protection(release) = %+v, want it protected and asking for 2 approvals", after)
	}
}

// Approval rules are a paid feature. An instance that refuses them is not an
// instance that has none: the reading degrades to what can be read, because a
// refusal here would stop every protection reading flow does.
func TestGitLabWithoutApprovalRulesStillReadsProtection(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	fake.mu.Lock()
	fake.hasApprovals = false
	fake.mu.Unlock()

	main, err := gitlab.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection(main): %v", err)
	}
	if !main.Enabled {
		t.Error("the branch is protected and a refused approvals endpoint hid it")
	}
	if main.RequiredApprovals != 0 {
		t.Errorf("RequiredApprovals = %d, want 0: nothing could be read", main.RequiredApprovals)
	}
	if !slices.Equal(main.RequiredChecks, []string{"ci/test"}) {
		t.Errorf("RequiredChecks = %v, want [ci/test] still", main.RequiredChecks)
	}
}

// External status checks are where a named verdict is required, and only
// Ultimate has them. Asked to require one on an instance without them, flow
// says so rather than reporting a check it never wrote.
func TestGitLabWithoutExternalChecksCannotRequireOne(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	fake.mu.Lock()
	fake.hasChecks = false
	fake.mu.Unlock()

	main, err := gitlab.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection(main): %v", err)
	}
	if len(main.RequiredChecks) != 0 {
		t.Errorf("RequiredChecks = %v, want none: this instance has no external status checks", main.RequiredChecks)
	}

	err = gitlab.SetProtection(t.Context(), "main", core.Protection{
		Enabled:        true,
		RequiredChecks: []string{"flow/policy"},
	})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("SetProtection = %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "flow/policy") {
		t.Errorf("the refusal does not name the check it could not require: %v", err)
	}
}

// Requiring a named check writes an external status check against the
// protected branch it belongs to, and leaves the one that was there.
func TestGitLabRequiresACheckAdditively(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	if err := gitlab.SetProtection(t.Context(), "main", core.Protection{
		Enabled:        true,
		RequiredChecks: []string{"flow/policy"},
	}); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	fake.mu.Lock()
	checks := slices.Clone(fake.checks)
	fake.mu.Unlock()

	var names []string
	for _, check := range checks {
		names = append(names, check.Name)
		if check.Name != "flow/policy" {
			continue
		}
		if check.ExternalURL == "" {
			t.Error("the check flow wrote has no external URL, and GitLab insists on one")
		}
		if len(check.ProtectedBranches) != 1 || check.ProtectedBranches[0].Name != "main" {
			t.Errorf("the check covers %v, want main alone", check.ProtectedBranches)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"ci/test", "flow/policy"}) {
		t.Errorf("the checks are %v, want the one that was there and the one flow added", names)
	}
}

// Ancestry is asked straight, both ways round, because a merge base would
// answer a different question: flow wants to know whether what was merged is
// in what was deployed (spec 13 §3).
func TestGitLabComparesStraightAndBothWays(t *testing.T) {
	_, gitlab := gitLabFixture(t)

	ahead, err := gitlab.Compare(t.Context(), coretest.SHAOld, coretest.SHADeployed)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if ahead.Status != core.CompareAhead || ahead.AheadBy != 2 || ahead.BehindBy != 0 {
		t.Errorf("Compare(old, deployed) = %+v, want ahead by 2", ahead)
	}

	same, err := gitlab.Compare(t.Context(), "main", "main")
	if err != nil {
		t.Fatalf("Compare(main, main): %v", err)
	}
	if same.Status != core.CompareIdentical {
		t.Errorf("Compare of a branch with itself = %q, want identical", same.Status)
	}

	// The open merge request's head left the trunk before its tip, so
	// neither contains the other.
	diverged, err := gitlab.Compare(t.Context(), coretest.SHADeployed, coretest.SHAHead)
	if err != nil {
		t.Fatalf("Compare(deployed, head): %v", err)
	}
	if diverged.Status != core.CompareDiverged {
		t.Errorf("Compare(deployed, head) = %+v, want diverged", diverged)
	}
	if diverged.Contains() {
		t.Error("a diverged comparison says the head contains the base")
	}

	if _, err := gitlab.Compare(t.Context(), "main", "no-such-ref"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Compare with a ref that is not there = %v, want it to wrap ErrNotFound", err)
	}
	if _, err := gitlab.Compare(t.Context(), "", "main"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Compare with no base = %v, want it to wrap ErrValidation", err)
	}
}

// A GitLab commit status is a row in a history: publishing never replaces
// anything. flow reads the newest row per name, and does not write a row that
// would say what the newest one already says — a status published every run
// would otherwise bury the history under identical verdicts.
func TestGitLabPublishesAStatusOnlyWhenTheVerdictChanges(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	const publish = "/api/v4/projects/acme%2Fwidget/statuses/" + coretest.SHAHead

	verdict := core.CommitStatus{Context: "flow/policy", State: core.StatusSuccess, Description: "✓ all rules pass"}
	if err := gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, verdict); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
	if err := gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, verdict); err != nil {
		t.Fatalf("SetCommitStatus again: %v", err)
	}
	if got := countGitLabCalls(fake, http.MethodPost, publish); got != 1 {
		t.Errorf("the same verdict was published %d times, want 1", got)
	}

	changed := core.CommitStatus{Context: "flow/policy", State: core.StatusFailure, Description: "✗ 1 error"}
	if err := gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, changed); err != nil {
		t.Fatalf("SetCommitStatus(changed): %v", err)
	}
	if got := countGitLabCalls(fake, http.MethodPost, publish); got != 2 {
		t.Errorf("a changed verdict was published %d times in all, want 2", got)
	}

	history := fake.statusHistory(coretest.SHAHead)
	if len(history) != 3 {
		t.Errorf("the history holds %d rows, want the fixture's one and flow's two: GitLab replaces nothing", len(history))
	}

	statuses, err := gitlab.CommitStatuses(t.Context(), coretest.SHAHead)
	if err != nil {
		t.Fatalf("CommitStatuses: %v", err)
	}
	var ours []core.CommitStatus
	for _, status := range statuses {
		if status.Context == "flow/policy" {
			ours = append(ours, status)
		}
	}
	if len(ours) != 1 {
		t.Fatalf("got %d flow/policy statuses, want the newest row only", len(ours))
	}
	if ours[0].State != core.StatusFailure {
		t.Errorf("the surviving status is %q, want the newest verdict", ours[0].State)
	}
	if ours[0].ID == "" || ours[0].UpdatedAt.IsZero() {
		t.Errorf("the status has no identity or no date: %+v", ours[0])
	}
}

// GitLab has no state of its own for a check that could not run, so an error
// lands as a failure and comes back as one. Writing a state GitLab has never
// heard of is refused before the call rather than by it.
func TestGitLabWritesTheStatesItHas(t *testing.T) {
	_, gitlab := gitLabFixture(t)
	if err := gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, core.CommitStatus{
		Context: "flow/policy",
		State:   core.StatusError,
	}); err != nil {
		t.Fatalf("SetCommitStatus(error): %v", err)
	}
	statuses, err := gitlab.CommitStatuses(t.Context(), coretest.SHAHead)
	if err != nil {
		t.Fatalf("CommitStatuses: %v", err)
	}
	var found bool
	for _, status := range statuses {
		if status.Context != "flow/policy" {
			continue
		}
		found = true
		if status.State != core.StatusFailure {
			t.Errorf("an error landed as %q; GitLab has only failed", status.State)
		}
	}
	if !found {
		t.Fatal("the status was not published at all")
	}

	err = gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, core.CommitStatus{Context: "flow/policy", State: "nonsense"})
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("publishing a state GitLab has not got = %v, want it to wrap ErrValidation", err)
	}
	err = gitlab.SetCommitStatus(t.Context(), coretest.SHAHead, core.CommitStatus{State: core.StatusSuccess})
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("publishing a status with no context = %v, want it to wrap ErrValidation", err)
	}
}

// GitLab writes notes of its own on every merge request. flow reads only the
// comments it wrote, which is what the marker is for: a system note carrying
// the same marker is still not flow's, and editing one would be editing the
// forge's own record.
func TestGitLabLeavesTheForgesOwnNotesAlone(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")
	fake.systemNote = core.MarkBody("assigned to @dev", marker, true)
	gitlab := newGitLabAdapter(t, srv, fx, nil)

	comment, err := gitlab.Comment(t.Context(), 1, marker)
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if comment != nil {
		t.Fatalf("flow claimed a system note as its own: %q", comment.Body)
	}

	if err := gitlab.UpsertComment(t.Context(), 1, marker, "two rules failed"); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}
	comment, err = gitlab.Comment(t.Context(), 1, marker)
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if comment == nil || !strings.Contains(comment.Body, "two rules failed") {
		t.Fatalf("Comment = %+v, want flow's own", comment)
	}
	if comment.Author != "flow-bot" {
		t.Errorf("the comment is by %q, want the credential that wrote it", comment.Author)
	}
	if !strings.Contains(comment.URL, "#note_"+comment.ID) {
		t.Errorf("the comment's URL is %q, which does not point at note %s", comment.URL, comment.ID)
	}
}

// An archived project takes no writes. One field is the whole of what flow
// needs to refuse a run before the first item rather than one item at a time
// (spec 10 §2).
func TestGitLabReportsAnArchivedProject(t *testing.T) {
	fx := coretest.ContractForge()
	fx.Repo.Archived = true
	_, srv := newGitLabServer(t, fx)
	repo, err := newGitLabAdapter(t, srv, fx, nil).Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if !repo.Archived {
		t.Error("the project is archived and Repo does not say so")
	}
	if repo.Slug() != "acme/widget" || repo.DefaultBranch != "main" {
		t.Errorf("Repo = %+v, want the project it was asked about", repo)
	}
}

// What GitLab refuses, flow names. A 404 is not there, a 403 is not allowed,
// and both carry what the provider said so that a refusal can be read.
func TestGitLabNamesWhatTheProviderRefused(t *testing.T) {
	const project = "/api/v4/projects/acme%2Fwidget"
	cases := []struct {
		name   string
		path   string
		status int
		want   error
		call   func(*GitLab) error
	}{
		{
			name: "a merge request that is not there", status: http.StatusNotFound, want: core.ErrNotFound,
			call: func(g *GitLab) error { _, err := g.PullRequest(t.Context(), 4040); return err },
		},
		{
			name: "a file that is not there", status: http.StatusNotFound, want: core.ErrNotFound,
			call: func(g *GitLab) error { _, err := g.File(t.Context(), "nothing/here.yml", "main"); return err },
		},
		{
			name: "a project the credential may not read", path: project,
			status: http.StatusForbidden, want: core.ErrPermission,
			call: func(g *GitLab) error { _, err := g.Repo(t.Context()); return err },
		},
		{
			name: "a project that is not there", path: project,
			status: http.StatusNotFound, want: core.ErrNotFound,
			call: func(g *GitLab) error { _, err := g.Probe(t.Context()); return err },
		},
		{
			name: "a listing the credential may not read", path: project + "/merge_requests",
			status: http.StatusForbidden, want: core.ErrPermission,
			call: func(g *GitLab) error { _, err := g.PullRequests(t.Context(), core.PRQuery{}); return err },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, gitlab := gitLabFixture(t)
			if tc.path != "" {
				fake.refuse(tc.path, tc.status)
			}
			err := tc.call(gitlab)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want it to wrap %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "acme/widget") {
				t.Errorf("the refusal does not say what flow was doing: %v", err)
			}
		})
	}
}

// A file is read from the raw endpoint at the ref asked for, and a path that
// is not there is not an error flow dresses up as something else.
func TestGitLabReadsAFileAtARef(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	content, err := gitlab.File(t.Context(), ".github/workflows/flow.yml", "main")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if !strings.Contains(string(content), "flow check pr") {
		t.Errorf("File returned %q", content)
	}
	const raw = "/api/v4/projects/acme%2Fwidget/repository/files/.github%2Fworkflows%2Fflow.yml/raw"
	if got := countGitLabRequests(fake, raw); got != 1 {
		t.Errorf("the file was read as %v, want the path encoded into one segment", fake.Requests())
	}
	if _, err := gitlab.File(t.Context(), "nothing/here.yml", "main"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("File of a missing path = %v, want it to wrap ErrNotFound", err)
	}
	if _, err := gitlab.File(t.Context(), "", "main"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("File of no path = %v, want it to wrap ErrValidation", err)
	}
}

// Proposing files branches off the trunk, writes each file there and offers
// one merge request, which a second proposal updates rather than repeats.
// flow never writes the trunk (spec 11 §2).
func TestGitLabProposesFilesOnABranchOfItsOwn(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	proposal := core.FileProposal{
		ID:    "ci",
		Title: "chore(flow): add the policy workflow",
		Body:  "flow proposes this.",
		Files: []core.ProposedFile{{Path: "ci/flow.yml", Content: []byte("name: flow\n")}},
	}
	first, err := gitlab.ProposeFiles(t.Context(), proposal)
	if err != nil {
		t.Fatalf("ProposeFiles: %v", err)
	}
	if first.HeadRef != proposal.Branch() || first.BaseRef != "main" {
		t.Errorf("the proposal is %s into %s, want %s into main", first.HeadRef, first.BaseRef, proposal.Branch())
	}
	fake.mu.Lock()
	written := string(fake.branches[proposal.Branch()]["ci/flow.yml"])
	trunk, onTrunk := fake.branches["main"]["ci/flow.yml"]
	fake.mu.Unlock()
	if written != "name: flow\n" {
		t.Errorf("the file on the branch is %q, want the proposal's content", written)
	}
	if onTrunk {
		t.Errorf("flow wrote %q to the trunk", trunk)
	}

	const write = "/api/v4/projects/acme%2Fwidget/repository/files/ci%2Fflow.yml"
	if got := countGitLabCalls(fake, http.MethodPost, write); got != 1 {
		t.Errorf("the file was created %d times, want 1", got)
	}

	proposal.Body = "flow proposes this, again."
	second, err := gitlab.ProposeFiles(t.Context(), proposal)
	if err != nil {
		t.Fatalf("ProposeFiles again: %v", err)
	}
	if second.Number != first.Number {
		t.Errorf("the second proposal opened !%d as well as !%d", second.Number, first.Number)
	}
	if second.Body != proposal.Body {
		t.Errorf("the second proposal did not update the description: %q", second.Body)
	}
	if got := countGitLabCalls(fake, http.MethodPut, write); got != 0 {
		t.Errorf("a file that had not changed was written %d times", got)
	}
}

// Merge settings are written to the project and read back from the answer,
// and where a squash takes its words from is a template GitLab keeps beside
// them. A project that cannot be told refuses rather than reporting a setting
// it does not have.
func TestGitLabWritesTheSquashTemplate(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	want := core.MergeSettings{
		AllowSquash:         true,
		SquashTitle:         core.SquashTitlePR,
		SquashMessage:       core.SquashMessagePRBody,
		DeleteBranchOnMerge: true,
	}
	if err := gitlab.SetMergeSettings(t.Context(), want); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}
	fake.mu.Lock()
	template, option, method := fake.project.SquashCommitTemplate, fake.project.SquashOption, fake.project.MergeMethod
	fake.mu.Unlock()
	if template != gitLabTemplateDescription {
		t.Errorf("squash_commit_template = %q, want %q", template, gitLabTemplateDescription)
	}
	if option != gitLabSquashAlways {
		t.Errorf("squash_option = %q, want %q: nothing else is allowed to land", option, gitLabSquashAlways)
	}
	if method != gitLabMergeFastForward {
		t.Errorf("merge_method = %q, want %q", method, gitLabMergeFastForward)
	}

	repo, err := gitlab.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if repo.Merge != want {
		t.Errorf("Repo().Merge = %+v, want %+v", repo.Merge, want)
	}

	if err := gitlab.SetMergeSettings(t.Context(), core.MergeSettings{}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("leaving no way to merge = %v, want it to wrap ErrValidation", err)
	}
}

// A project whose merge settings are fixed elsewhere — a group policy, an
// administrator — keeps what it had, and flow says the setting did not take
// rather than reporting what it asked for.
func TestGitLabSaysWhenTheProjectKeptItsSettings(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)
	gitlab := newGitLabAdapter(t, srv, fx, nil)

	fake.mu.Lock()
	fake.fixedMerge = true
	fake.mu.Unlock()

	err := gitlab.SetMergeSettings(t.Context(), core.MergeSettings{
		AllowSquash:   true,
		SquashTitle:   core.SquashTitlePR,
		SquashMessage: core.SquashMessagePRBody,
	})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("SetMergeSettings against a fixed project = %v, want it to wrap ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "acme/widget") {
		t.Errorf("the refusal does not name the project: %v", err)
	}
}

// Approval rules and external status checks are paid features, and a project
// on the tier most people have answers neither. Asking for them behind a
// branch nobody protected buys a refusal per plan item for a credential that
// was never going to be allowed, and tells flow nothing: there is no rule
// without a protected branch to hang it on.
func TestGitLabDoesNotLookBehindAnUnprotectedBranch(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	if _, err := gitlab.Protection(t.Context(), "some-branch"); err != nil {
		t.Fatalf("Protection(some-branch): %v", err)
	}
	for _, path := range []string{
		"/api/v4/projects/acme%2Fwidget/approval_rules",
		"/api/v4/projects/acme%2Fwidget/external_status_checks",
	} {
		if got := countGitLabRequests(fake, path); got != 0 {
			t.Errorf("%s was read %d times behind a branch nobody protected, want 0", path, got)
		}
	}
}

// A project's rules and checks are the project's, and each says which
// branches it covers. The trunk's protection is the strictest of the ones
// that cover the trunk, and none of the ones that do not: counting a rule
// written for a release branch would report an approval nobody will be asked
// for, and taking the last rule read rather than the strictest would report
// fewer than GitLab will insist on.
func TestGitLabTakesTheStrictestRuleThatCoversTheBranch(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	fake.mu.Lock()
	fake.rules = append(fake.rules,
		// A rule that names a branch and applies to every protected one
		// anyway. GitLab's flag wins over its own list.
		glApprovalRule{ID: 90, Name: "every protected branch", RuleType: gitLabAnyApprover, ApprovalsRequired: 5,
			AppliesToAllProtectedBranches: true, ProtectedBranches: gitLabBranchNames("release")},
		glApprovalRule{ID: 91, Name: "the trunk", RuleType: gitLabAnyApprover, ApprovalsRequired: 3,
			ProtectedBranches: gitLabBranchNames("main")},
		glApprovalRule{ID: 92, Name: "the release train", RuleType: gitLabAnyApprover, ApprovalsRequired: 9,
			ProtectedBranches: gitLabBranchNames("release")},
	)
	fake.checks = append(fake.checks,
		glExternalCheck{ID: 93, Name: "release/sign-off", ExternalURL: "https://gitlab.test",
			ProtectedBranches: gitLabBranchNames("release")},
		// A check belonging to no branch in particular belongs to every
		// merge request the project has.
		glExternalCheck{ID: 94, Name: "pipeline/green", ExternalURL: "https://gitlab.test"},
	)
	fake.mu.Unlock()

	main, err := gitlab.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection(main): %v", err)
	}
	if main.RequiredApprovals != 5 {
		t.Errorf("RequiredApprovals = %d, want 5: the strictest rule covering main", main.RequiredApprovals)
	}
	want := []string{"ci/test", "pipeline/green"}
	if !slices.Equal(main.RequiredChecks, want) {
		t.Errorf("RequiredChecks = %v, want %v: the release branch's check is not main's", main.RequiredChecks, want)
	}
}

// gitLabBranchNames builds the `protected_branches` of a rule or a check.
func gitLabBranchNames(names ...string) []struct {
	Name string `json:"name"`
} {
	out := make([]struct {
		Name string `json:"name"`
	}, 0, len(names))
	for _, name := range names {
		out = append(out, struct {
			Name string `json:"name"`
		}{Name: name})
	}
	return out
}

// A squashed merge request has two commits and only one of them is on the
// trunk. `flow check deploy` proves a deployment contains what landed, and
// the merge commit of a squashed merge request is not what landed.
func TestGitLabLandsTheCommitTheSquashMade(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	squashed, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	if squashed.MergeSHA != coretest.SHAMerged {
		t.Errorf("MergeSHA = %q, want the squash commit %q", squashed.MergeSHA, coretest.SHAMerged)
	}

	// A merge request that was not squashed has only the merge commit,
	// and that is the one that landed.
	fake.mu.Lock()
	fake.squashCommit = false
	fake.mu.Unlock()
	merged, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2) unsquashed: %v", err)
	}
	if merged.MergeSHA != coretest.SHAMerged {
		t.Errorf("MergeSHA = %q, want the merge commit %q", merged.MergeSHA, coretest.SHAMerged)
	}
}

// GitLab caps `changes_count` at a thousand and writes the cap as "1000+".
// A summary that says a thousand files is not improved by saying none, which
// is what reading the text as a number gives.
func TestGitLabReadsACappedChangeCountAsTheNumberItCaps(t *testing.T) {
	fx := coretest.ContractForge()
	fx.PullRequests[0].Files.Changed = 4000
	fake, srv := newGitLabServer(t, fx)
	gitlab := newGitLabAdapter(t, srv, fx, nil)
	fake.mu.Lock()
	fake.paths[1] = nil
	fake.mu.Unlock()

	pr, err := gitlab.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest(1): %v", err)
	}
	if pr.Files.Changed != 1000 {
		t.Errorf("Files.Changed = %d, want 1000: the cap is the number it qualifies", pr.Files.Changed)
	}
}

// The files of a merge request are a summary somebody reads, and GitLab sends
// them in the order the diff was built. Flow sorts them so that two readings
// of the same merge request say the same thing.
func TestGitLabSortsTheFilesOfAMergeRequest(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGitLabServer(t, fx)
	gitlab := newGitLabAdapter(t, srv, fx, nil)
	fake.mu.Lock()
	fake.paths[1] = []string{"ui/zoom.go", "core/events.go"}
	fake.mu.Unlock()

	pr, err := gitlab.PullRequest(t.Context(), 1)
	if err != nil {
		t.Fatalf("PullRequest(1): %v", err)
	}
	want := []string{"core/events.go", "ui/zoom.go"}
	if !slices.Equal(pr.Files.Paths, want) {
		t.Errorf("Files.Paths = %v, want %v", pr.Files.Paths, want)
	}
}

// A project wired to an external issue tracker answers closes_issues with an
// id and a title and no reference of any kind. There is no key in that, and
// inventing one would send the tracker looking for a ticket called nothing;
// leaving the link empty is what lets the tracker fallback do its work.
func TestGitLabNamesNoTicketAnExternalTrackerKeeps(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	fake.mu.Lock()
	fake.external[2] = true
	fake.mu.Unlock()

	pr, err := gitlab.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest(2): %v", err)
	}
	if len(pr.ClosingIssues) != 0 {
		t.Errorf("ClosingIssues = %v, want none: an external issue has no key flow can write", pr.ClosingIssues)
	}
}

// Where a squash commit takes its words from is one template, and settings
// that do not say leave it alone. Writing flow's own default over a template
// somebody chose would make the next plan propose putting it back.
func TestGitLabLeavesASquashTemplateItCannotSay(t *testing.T) {
	fake, gitlab := gitLabFixture(t)
	fake.mu.Lock()
	before := fake.project.SquashCommitTemplate
	fake.mu.Unlock()
	if before != gitLabTemplateFirstCommit {
		t.Fatalf("the project starts with %q, want %q", before, gitLabTemplateFirstCommit)
	}

	if err := gitlab.SetMergeSettings(t.Context(), core.MergeSettings{AllowSquash: true, AllowMerge: true}); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}
	fake.mu.Lock()
	after := fake.project.SquashCommitTemplate
	fake.mu.Unlock()
	if after != before {
		t.Errorf("squash_commit_template = %q, want %q: the settings said nothing about it", after, before)
	}
}

// Each of flow's three states is a state GitLab's listing understands, and
// the mapping is the whole of the filtering: flow asks GitLab the question
// rather than reading every merge request and judging them itself.
func TestGitLabAsksForTheStateItWasAskedFor(t *testing.T) {
	for _, row := range []struct{ state, want string }{
		{core.PRStateOpen, "opened"},
		{core.PRStateMerged, "merged"},
		{core.PRStateClosed, "closed"},
		{"", "all"},
	} {
		t.Run(row.want, func(t *testing.T) {
			fake, gitlab := gitLabFixture(t)
			prs, err := gitlab.PullRequests(t.Context(), core.PRQuery{State: row.state})
			if err != nil {
				t.Fatalf("PullRequests(%q): %v", row.state, err)
			}
			query, asked := fake.Query("/api/v4/projects/acme%2Fwidget/merge_requests", 0)
			if !asked {
				t.Fatal("the listing was never read")
			}
			if got := query.Get("state"); got != row.want {
				t.Fatalf("the listing asked state=%q, want %q", got, row.want)
			}
			for _, pr := range prs {
				if row.state != "" && pr.State != row.state {
					t.Errorf("merge request !%d is %s in a listing of %s", pr.Number, pr.State, row.state)
				}
			}
		})
	}
}

// TestGitLabReadsAMergeRequestThatNeedsRebasingAsBehind: a project whose
// merge method is fast-forward or semi-linear needs the branch rebased
// whenever the target moves, and GitLab says so in its own word. It is a
// push, not a resolution, and the two readings have to stay apart.
func TestGitLabReadsAMergeRequestThatNeedsRebasingAsBehind(t *testing.T) {
	fx := coretest.ContractForge()
	fx.PullRequests[0].Mergeability = core.MergeabilityBehind
	_, srv := newGitLabServer(t, fx)
	adapter := newGitLabAdapter(t, srv, fx, nil)

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

// TestGitLabReadsEveryMergeStatusItIsAnsweredWith is the vocabulary itself.
//
// GitLab reports the *first* failing merge check rather than all of them, so
// most of these values mean "blocked on a person" and only three mean
// "blocked on the branch". An installation older than 15.6 answers only the
// coarse field, which cannot tell a conflict from a rebase and so never says
// `behind` — a forge that has said something coarse has still said more than
// nothing.
func TestGitLabReadsEveryMergeStatusItIsAnsweredWith(t *testing.T) {
	for _, row := range []struct {
		name     string
		detailed string
		coarse   string
		want     string
	}{
		{"every check passing", glMergeMergeable, glMergeStatusCan, core.MergeabilityClean},
		{"the branches conflict", glMergeConflict, glMergeStatusCannot, core.MergeabilityConflicted},
		{"a conflict GitLab could not describe", glMergeBroken, glMergeStatusCannot, core.MergeabilityConflicted},
		{"the merge method wants a rebase", glMergeNeedRebase, glMergeStatusCan, core.MergeabilityBehind},
		{"GitLab is still working it out", "checking", "checking", core.MergeabilityUnknown},
		{"GitLab has not started", "unchecked", "", core.MergeabilityUnknown},
		{"waiting on a person, not on a push", "not_approved", glMergeStatusCan, core.MergeabilityClean},
		{"a blocker flow has no name for", "jira_association_missing", glMergeStatusCan, core.MergeabilityClean},
		{"an old installation that can merge it", "", glMergeStatusCan, core.MergeabilityClean},
		{"an old installation that cannot", "", glMergeStatusCannot, core.MergeabilityConflicted},
		{"an old installation still checking", "", "checking", core.MergeabilityUnknown},
		{"an installation that said nothing", "", "", core.MergeabilityUnknown},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := mergeabilityFromGitLab(row.detailed, row.coarse); got != row.want {
				t.Errorf("mergeabilityFromGitLab(%q, %q) = %q, want %q",
					row.detailed, row.coarse, got, row.want)
			}
		})
	}
}
