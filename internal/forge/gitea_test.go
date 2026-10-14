package forge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// giteaCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func giteaCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(GiteaKind)
	if err != nil {
		t.Fatalf("Of(gitea): %v", err)
	}
	return set
}

// newGiteaAdapter builds an adapter talking to srv.
//
// The base URL carries [GiteaAPIPath], because that is what the root hands
// the client: a Gitea lives wherever somebody put it, and the API root is the
// instance's URL with that path on the end.
func newGiteaAdapter(t *testing.T, srv *httptest.Server, fx coretest.ForgeFixture, adjust func(*core.ClientOptions)) *Gitea {
	t.Helper()
	opts := core.ClientOptions{
		BaseURL: srv.URL + GiteaAPIPath,
		Token:   "test-token",
		Version: "v1.0.0",
		Header:  GiteaHeader(),
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
	adapter, err := NewGitea(GiteaOptions{
		Client:       client,
		Owner:        fx.Repo.Owner,
		Repo:         fx.Repo.Name,
		Capabilities: giteaCapabilities(t),
	})
	if err != nil {
		t.Fatalf("NewGitea: %v", err)
	}
	return adapter
}

// contractGitea stands a fake Gitea up in front of the fixture.
func contractGitea(t *testing.T, fx coretest.ForgeFixture) core.Forge {
	t.Helper()
	_, srv := newGiteaServer(t, fx)
	return newGiteaAdapter(t, srv, fx, nil)
}

// The whole forge contract against Gitea's own shapes. An adapter that passes
// it can be swapped for GitHub's, which is the point of there being an
// interface (spec 14).
func TestGiteaMeetsTheForgeContract(t *testing.T) {
	coretest.ForgeContract(t, contractGitea)
}

func TestNewGiteaRefusesAnIncompleteAdapter(t *testing.T) {
	caps := giteaCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: "https://codeberg.org" + GiteaAPIPath})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct {
		name string
		opts GiteaOptions
	}{
		{"no client", GiteaOptions{Owner: "a", Repo: "b", Capabilities: caps}},
		{"no repository", GiteaOptions{Client: client, Owner: "a", Capabilities: caps}},
		{"no declaration", GiteaOptions{Client: client, Owner: "a", Repo: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewGitea(tc.opts); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

// countRequests is how many times a path was asked for.
func countRequests(fake *giteaFake, path string) int {
	var n int
	for _, asked := range fake.Requests() {
		if asked == path {
			n++
		}
	}
	return n
}

func TestGiteaReadsWhereTheNumberComesFromOncePerRun(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	for _, number := range []int{1, 2, 3} {
		if _, err := adapter.PullRequest(t.Context(), number); err != nil {
			t.Fatalf("PullRequest(%d): %v", number, err)
		}
	}
	if got := countRequests(fake, fake.prefix()); got != 1 {
		t.Errorf("the repository was read %d times for a setting that cannot change mid-run, want 1", got)
	}
}

func TestGiteaWritesTheReferenceTheRepositoryUses(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	// A repository whose issues live somewhere else: Gitea then writes the
	// pull request's number with a different character in front of it,
	// because `#12` in that repository means somebody else's ticket.
	fake.external = "https://tracker.example/acme/widget/issues/{index}"
	adapter := newGiteaAdapter(t, srv, fx, nil)

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	want := pr.Title + " (!2)"
	if pr.MergeHeader() != want {
		t.Errorf("MergeHeader() = %q, want %q: the gate would judge a header the trunk never sees", pr.MergeHeader(), want)
	}
}

// refusingGitea fails the repository's own endpoint while it is told to,
// which is how a run that could not read a setting is told apart from one
// that read it.
type refusingGitea struct {
	inner   *giteaFake
	refuse  bool
	refused int
}

func (g *refusingGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.refuse && r.URL.EscapedPath() == g.inner.prefix() {
		g.refused++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"token does not have permission"}`))
		return
	}
	g.inner.ServeHTTP(w, r)
}

func TestGiteaDoesNotKeepAnAnswerItNeverGot(t *testing.T) {
	fx := coretest.ContractForge()
	fake, _ := newGiteaServer(t, fx)
	gate := &refusingGitea{inner: fake, refuse: true}
	srv := httptest.NewServer(gate)
	t.Cleanup(srv.Close)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	if _, err := adapter.PullRequest(t.Context(), 2); err == nil {
		t.Fatal("a pull request was read through a repository flow could not read")
	}
	gate.refuse = false

	pr, err := adapter.PullRequest(t.Context(), 2)
	if err != nil {
		t.Fatalf("PullRequest after the refusal lifted: %v", err)
	}
	if want := pr.Title + " (#2)"; pr.MergeHeader() != want {
		t.Errorf("MergeHeader() = %q, want %q: a failed read became the answer for the rest of the run", pr.MergeHeader(), want)
	}
}

func TestGiteaWalksPastTheFirstPage(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	if fake.pageSize >= len(fx.PullRequests) {
		t.Fatalf("the fake pages at %d and the fixture holds %d, so no walk is a walk", fake.pageSize, len(fx.PullRequests))
	}

	prs, err := adapter.PullRequests(t.Context(), core.PRQuery{})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if got := numbers(prs); !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("PullRequests() = %v, want every one of them newest first", got)
	}
}

func TestGiteaStopsPagingAsSoonAsTheWindowCloses(t *testing.T) {
	fx := coretest.ContractForge()
	// Two more pull requests older than #3, so that a walk which read the
	// whole history would cost a page more than one which stops.
	for _, number := range []int{4, 5} {
		fx.PullRequests = append(fx.PullRequests, core.PullRequest{
			Number:    number,
			Title:     "chore(deps): an older bump",
			Author:    "dev",
			State:     core.PRStateClosed,
			HeadRef:   "chore/older",
			BaseRef:   "main",
			CreatedAt: coretest.ContractNow.Add(-time.Duration(40+number) * 24 * time.Hour),
			UpdatedAt: coretest.ContractNow.Add(-time.Duration(39+number) * 24 * time.Hour),
		})
	}
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	// #3 was last touched thirty-nine days ago, so a fortnight's window
	// ends the walk on the page it is on rather than reading the rest of
	// the history.
	prs, err := adapter.PullRequests(t.Context(), core.PRQuery{UpdatedSince: coretest.ContractNow.Add(-14 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("PullRequests: %v", err)
	}
	if got := numbers(prs); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("PullRequests() = %v, want only the two inside the window", got)
	}
	var pages int
	for _, asked := range fake.Requests() {
		if asked == fake.prefix()+"/pulls" {
			pages++
		}
	}
	if whole := 3; pages >= whole {
		t.Errorf("the walk read %d pages of %d, want it to stop: the list is newest first, so the first one outside the window ends it", pages, whole)
	}
}

func TestGiteaFiltersTheStateGiteaCannotExpress(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	// Gitea has no merged state: #2 and #3 are both closed to it, and only
	// one of them was merged.
	merged, err := adapter.PullRequests(t.Context(), core.PRQuery{State: core.PRStateMerged})
	if err != nil {
		t.Fatalf("PullRequests(merged): %v", err)
	}
	if got := numbers(merged); !slices.Equal(got, []int{2}) {
		t.Errorf("the merged pull requests are %v, want just #2", got)
	}

	closed, err := adapter.PullRequests(t.Context(), core.PRQuery{State: core.PRStateClosed})
	if err != nil {
		t.Fatalf("PullRequests(closed): %v", err)
	}
	if got := numbers(closed); !slices.Equal(got, []int{3}) {
		t.Errorf("the closed pull requests are %v, want just #3", got)
	}
}

func TestGiteaReadsAHistoryAsTheVerdictsThatStand(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	status := core.CommitStatus{Context: "flow/policy", State: core.StatusFailure, Description: "cc.header.length"}
	if err := adapter.SetCommitStatus(t.Context(), coretest.SHAMerged, status); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
	if got := len(fake.statusHistory(coretest.SHAMerged)); got != 3 {
		t.Fatalf("the commit now carries %d statuses, want 3: Gitea replaces nothing", got)
	}

	statuses, err := adapter.CommitStatuses(t.Context(), coretest.SHAMerged)
	if err != nil {
		t.Fatalf("CommitStatuses: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("CommitStatuses returned %d, want one per context: a history is not what a caller asking what the checks say means", len(statuses))
	}
	for _, s := range statuses {
		if s.Context == "flow/policy" && s.State != core.StatusFailure {
			t.Errorf("flow/policy reads %q, want the verdict that replaced the old one", s.State)
		}
	}
}

func TestGiteaDoesNotRepeatAVerdictTheCommitAlreadyCarries(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	before := len(fake.statusHistory(coretest.SHAMerged))
	// The three strings Gitea trims before storing, written back with the
	// whitespace a template left on them: trimmed, this is what the commit
	// already says.
	same := core.CommitStatus{Context: " flow/policy ", State: core.StatusSuccess, Description: "✓ all rules pass "}
	if err := adapter.SetCommitStatus(t.Context(), coretest.SHAMerged, same); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
	if got := len(fake.statusHistory(coretest.SHAMerged)); got != before {
		t.Errorf("the history grew from %d to %d for a sentence the commit already carried", before, got)
	}
}

func TestGiteaRefusesAStatusNoForgeCouldPublish(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	cases := []struct {
		name   string
		sha    string
		status core.CommitStatus
	}{
		{"no commit", "", core.CommitStatus{Context: "flow/policy", State: core.StatusSuccess}},
		{"no context", coretest.SHAMerged, core.CommitStatus{State: core.StatusSuccess}},
		{"a state no forge has", coretest.SHAMerged, core.CommitStatus{Context: "flow/policy", State: "mostly"}},
		{"a state Gitea has and flow does not mean", coretest.SHAMerged, core.CommitStatus{Context: "flow/policy", State: "warning"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := adapter.SetCommitStatus(t.Context(), tc.sha, tc.status)
			if !errors.Is(err, core.ErrValidation) {
				t.Fatalf("SetCommitStatus = %v, want a refusal flow could explain", err)
			}
		})
	}
}

func TestGiteaReadsProtectionWithoutBeingAnAdministrator(t *testing.T) {
	fx := coretest.ContractForge()
	// The ordinary state of a bot account: it may push and it may not read
	// a rule. What the gate depends on is what the branch itself says.
	fx.Permissions.Tokens = []string{"repo", "statuses_write"}
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	if fake.admin {
		t.Fatal("the fake is an administrator, so the refusal under test never happens")
	}

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if !protection.Enabled {
		t.Error("the branch reads as unprotected because flow could not read the rule behind it")
	}
	if !slices.Equal(protection.RequiredChecks, []string{"ci/test"}) {
		t.Errorf("RequiredChecks = %v, want the contexts the branch itself carries", protection.RequiredChecks)
	}
}

func TestGiteaCreatesARuleWhereThereIsNoneToEdit(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	fake.refs["release/1.0"] = coretest.SHADeployed

	want := core.Protection{Branch: "release/1.0", Enabled: true, RequiredChecks: []string{"flow/policy"}}
	if err := adapter.SetProtection(t.Context(), "release/1.0", want); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	rule, ok := fake.ruleFor("release/1.0")
	if !ok {
		t.Fatal("no rule was written: the edit answered 404 and nothing followed it")
	}
	if !slices.Equal(rule.RequiredChecks, []string{"flow/policy"}) {
		t.Errorf("the rule requires %v, want flow/policy", rule.RequiredChecks)
	}
	if !slices.Contains(fake.Methods(), http.MethodPost) {
		t.Errorf("methods = %v, want a create among them", fake.Methods())
	}
}

func TestGiteaWritesNothingTheBranchAlreadySays(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	already := core.Protection{Branch: "main", Enabled: true, RequiredChecks: []string{"ci/test"}, RequiredApprovals: 1}
	if err := adapter.SetProtection(t.Context(), "main", already); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}
	for _, method := range fake.Methods() {
		if method != http.MethodGet {
			t.Fatalf("methods = %v, want reads only: the branch already said all of this", fake.Methods())
		}
	}
}

func TestGiteaRefusesToTakeProtectionOff(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	err := adapter.SetProtection(t.Context(), "main", core.Protection{Branch: "main"})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("SetProtection = %v, want a refusal: flow only ever adds protection", err)
	}
}

func TestGiteaNamesTheRelationTwoCountsCannot(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	cases := []struct {
		name       string
		base, head string
		want       string
	}{
		{"the same commit", coretest.SHAMerged, coretest.SHAMerged, core.CompareIdentical},
		{"the trunk has moved on", coretest.SHAMerged, coretest.SHADeployed, core.CompareAhead},
		{"the merge is behind the trunk", coretest.SHADeployed, coretest.SHAMerged, core.CompareBehind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapter.Compare(t.Context(), tc.base, tc.head)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Compare(%s, %s).Status = %q, want %q", tc.base, tc.head, got.Status, tc.want)
			}
		})
	}
}

func TestGiteaRefusesAComparisonWithOnlyOneEnd(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	if _, err := adapter.Compare(t.Context(), "", coretest.SHAMerged); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Compare with no base = %v, want a refusal", err)
	}
	if _, err := adapter.Compare(t.Context(), coretest.SHAMerged, ""); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Compare with no head = %v, want a refusal", err)
	}
}

func TestGiteaReportsMergeSettingsTheInstanceDidNotKeep(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	// A site administrator who pins the strategies: the write succeeds and
	// changes nothing, which is the outcome flow must not report as done.
	fake.fixedMerge = true

	err := adapter.SetMergeSettings(t.Context(), core.MergeSettings{AllowSquash: true})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("SetMergeSettings = %v, want a conflict: the instance kept its own settings", err)
	}
}

func TestGiteaRefusesMergeSettingsThatLeaveNoWayToMerge(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	err := adapter.SetMergeSettings(t.Context(), core.MergeSettings{DeleteBranchOnMerge: true})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("SetMergeSettings = %v, want a refusal", err)
	}
}

func TestGiteaReadsAFileFromTheEndpointThatServesBytes(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	content, err := adapter.File(t.Context(), ".github/workflows/flow.yml", "main")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if !strings.Contains(string(content), "flow check pr") {
		t.Errorf("the file reads %q, want the workflow the fixture holds", content)
	}
	if !slices.Contains(fake.Requests(), fake.prefix()+"/raw/.github/workflows/flow.yml") {
		t.Errorf("requests = %v, want the raw endpoint: Gitea serves the bytes there and nowhere else", fake.Requests())
	}
}

func TestGiteaRefusesAFileWithNoPath(t *testing.T) {
	fx := coretest.ContractForge()
	_, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	if _, err := adapter.File(t.Context(), "", "main"); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("File = %v, want a refusal", err)
	}
}

func TestGiteaAsksAboutABranchWithSlashesAsOneBranch(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	fake.refs["release/1.0"] = coretest.SHADeployed

	if _, err := adapter.Protection(t.Context(), "release/1.0"); err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if !slices.Contains(fake.Requests(), fake.prefix()+"/branches/release/1.0") {
		t.Errorf("requests = %v, want the branch asked for whole: an escaped slash is a different branch", fake.Requests())
	}
}

func TestGiteaLeavesCommentsItDidNotWriteAlone(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	fake.addComment(1, "dev", "This looks good to me.")

	marker := core.NewMarker(core.MarkerPolicy, "pr", "1")
	if err := adapter.UpsertComment(t.Context(), 1, marker, "the gate's verdict"); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}
	comments := fake.commentsOn(1)
	if len(comments) != 2 {
		t.Fatalf("the pull request carries %d comments, want the person's and flow's", len(comments))
	}
	if comments[0].Body != "This looks good to me." {
		t.Errorf("the first comment now reads %q; flow edited somebody else's writing", comments[0].Body)
	}
	if !core.HasMarker(comments[1].Body, marker) {
		t.Errorf("flow's own comment %q carries no marker, so it could never be found again", comments[1].Body)
	}
	if !strings.Contains(comments[1].Body, "<!--") {
		t.Errorf("the marker is visible in %q; Gitea renders markdown and would show it to everybody", comments[1].Body)
	}
}

func TestGiteaValidatesWithETagsOnTheSecondRun(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	cache := core.NewMemoryCache()
	adapter := newGiteaAdapter(t, srv, fx, func(o *core.ClientOptions) { o.Cache = cache })

	first, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	second, err := adapter.Repo(t.Context())
	if err != nil {
		t.Fatalf("Repo again: %v", err)
	}
	if second.Slug() != first.Slug() || second.Merge != first.Merge {
		t.Errorf("the cached answer differs: %+v vs %+v", second, first)
	}
	if fake.NotModified() != 1 {
		t.Errorf("%d conditional requests were answered 304, want 1", fake.NotModified())
	}
}

func TestGiteaIdentifiesItselfOnEveryRequest(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)

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

func TestGiteaReadsTheRuleThatGovernsABranchAndNotOneOfItsName(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	// A wider rule covering a branch: the branch's protection is kept
	// under the rule's name, and a reader that asked for `release/1.0`
	// would be told there is no such rule and stop at what the branch
	// alone says.
	fake.refs["release/1.0"] = coretest.SHADeployed
	fake.rules["release/*"] = core.Protection{
		Enabled:        true,
		RequiredChecks: []string{"ci/test"},
		StrictChecks:   true,
	}
	fake.effective["release/1.0"] = "release/*"

	protection, err := adapter.Protection(t.Context(), "release/1.0")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if protection.Branch != "release/1.0" {
		t.Errorf("Branch = %q, want the branch asked about rather than the rule that governs it", protection.Branch)
	}
	if !protection.StrictChecks {
		t.Error("StrictChecks is off: the settings only the rule carries were never read, because flow looked under the branch's own name")
	}
	// The rule's name is a name, not a path, so the character that makes it
	// a glob is escaped on the way out and means itself on the way in.
	if !slices.Contains(fake.Requests(), fake.prefix()+"/branch_protections/release/%2A") {
		t.Errorf("requests = %v, want the rule read under the name the branch gave for it", fake.Requests())
	}
}

// recordingGitea keeps the bytes of every write, so that a request can be
// read as the forge would read it rather than as the adapter composed it.
type recordingGitea struct {
	inner  *giteaFake
	mu     sync.Mutex
	bodies map[string][]byte
}

func (g *recordingGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		g.mu.Lock()
		if g.bodies == nil {
			g.bodies = map[string][]byte{}
		}
		g.bodies[r.Method+" "+r.URL.EscapedPath()] = body
		g.mu.Unlock()
	}
	g.inner.ServeHTTP(w, r)
}

func (g *recordingGitea) body(t *testing.T, key string) map[string]any {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	raw, ok := g.bodies[key]
	if !ok {
		t.Fatalf("nothing was sent to %q; the writes were %v", key, slices.Sorted(maps.Keys(g.bodies)))
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the body of %s is not JSON: %v", key, err)
	}
	return doc
}

// giteaProtectionFields is EditBranchProtectionOption as Gitea publishes it,
// read from an instance's own schema (https://codeberg.org/swagger.v1.json,
// Gitea 1.22) rather than from this adapter. A key flow sends that is not
// here is a key Gitea ignores, and an ignored key is protection a plan
// claimed to write and nobody has.
var giteaProtectionFields = []string{
	"apply_to_admins", "approvals_whitelist_teams", "approvals_whitelist_username",
	"block_on_official_review_requests", "block_on_outdated_branch", "block_on_rejected_reviews",
	"dismiss_stale_approvals", "enable_approvals_whitelist", "enable_merge_whitelist",
	"enable_push", "enable_push_whitelist", "enable_status_check", "ignore_stale_approvals",
	"merge_whitelist_teams", "merge_whitelist_usernames", "protected_file_patterns",
	"push_whitelist_deploy_keys", "push_whitelist_teams", "push_whitelist_usernames",
	"require_signed_commits", "required_approvals", "status_check_contexts",
	"unprotected_file_patterns",
}

func TestGiteaWritesProtectionInGiteasOwnVocabulary(t *testing.T) {
	fx := coretest.ContractForge()
	fake, _ := newGiteaServer(t, fx)
	recorder := &recordingGitea{inner: fake}
	srv := httptest.NewServer(recorder)
	t.Cleanup(srv.Close)
	adapter := newGiteaAdapter(t, srv, fx, nil)

	want := core.Protection{
		Branch:            "main",
		Enabled:           true,
		RequiredChecks:    []string{"ci/test", "flow/policy"},
		RequiredApprovals: 1,
		StrictChecks:      true,
		EnforceAdmins:     true,
	}
	if err := adapter.SetProtection(t.Context(), "main", want); err != nil {
		t.Fatalf("SetProtection: %v", err)
	}

	body := recorder.body(t, http.MethodPatch+" "+fake.prefix()+"/branch_protections/main")
	for key := range body {
		if !slices.Contains(giteaProtectionFields, key) {
			t.Errorf("the write carries %q, which Gitea's own schema does not name: it would be ignored", key)
		}
	}
	// The rule is the only place these two are kept, so a write that omits
	// them is a write that leaves them as they were.
	for _, key := range []string{"block_on_outdated_branch", "apply_to_admins", "status_check_contexts"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the write says nothing about %q, so the rule keeps whatever it had", key)
		}
	}
	// A create takes the rule's name and an edit does not, so the name is
	// absent until there is a rule to name.
	for _, key := range []string{"rule_name", "branch_name"} {
		if _, ok := body[key]; ok {
			t.Errorf("the edit carries %q; Gitea accepts it only when the rule is created", key)
		}
	}
}

func TestGiteaDoesNotClaimAForcePushSettingGiteaHasNot(t *testing.T) {
	fx := coretest.ContractForge()
	fake, srv := newGiteaServer(t, fx)
	adapter := newGiteaAdapter(t, srv, fx, nil)
	fake.rules["main"] = core.Protection{
		Enabled:        true,
		RequiredChecks: []string{"ci/test"},
		AllowForcePush: true,
	}

	protection, err := adapter.Protection(t.Context(), "main")
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if protection.AllowForcePush {
		t.Error("the branch reads as force-pushable: Gitea has no such setting, so a true here was invented")
	}
}
