package deploys

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

// deployCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func deployCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(CapabilityAdapter)
	if err != nil {
		t.Fatalf("Of(%s): %v", CapabilityAdapter, err)
	}
	return set
}

// newGitHubSource builds a source talking to srv.
func newGitHubSource(t *testing.T, srv *httptest.Server) *GitHub {
	t.Helper()
	client, err := core.NewClient(core.ClientOptions{
		BaseURL: srv.URL,
		Token:   "test-token",
		Version: "v1.0.0",
		Clock:   coretest.NewClock(coretest.ContractNow),
		Backoff: func(int, core.RateLimit) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	source, err := NewGitHub(GitHubOptions{
		Client:       client,
		Owner:        fakeOwner,
		Repo:         fakeRepo,
		Capabilities: deployCapabilities(t),
	})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	return source
}

// contractGitHub stands a fake GitHub up in front of the fixture.
func contractGitHub(t *testing.T, fx coretest.DeployFixture) core.DeploySource {
	t.Helper()
	_, srv := newGitHubServer(t, fx)
	return newGitHubSource(t, srv)
}

func TestGitHubMeetsTheDeployContract(t *testing.T) {
	coretest.DeployContract(t, contractGitHub)
}

func TestNewGitHubRefusesAnIncompleteSource(t *testing.T) {
	caps := deployCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: "https://api.github.com"})
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
			if _, err := NewGitHub(tc.opts); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("NewGitHub = %v, want ErrValidation", err)
			}
		})
	}
}

func TestGitHubRefusesAQueryWithNoEnvironment(t *testing.T) {
	_, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{}); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Deployments without an environment = %v, want ErrValidation", err)
	}
}

func TestGitHubLetsTheProviderDoTheFiltering(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: coretest.SHAMerged}); err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	queries := fake.Queries()
	if len(queries) == 0 {
		t.Fatal("nothing was asked of the provider")
	}
	first := queries[0]
	for _, want := range []string{"environment=production", "sha=" + coretest.SHAMerged, "per_page=100"} {
		if !strings.Contains(first, want) {
			t.Errorf("the list query is %q, want it to carry %q so the provider narrows the history", first, want)
		}
	}
}

// TestGitHubIdentifiesItselfOnEveryRequest: every call carries flow's user
// agent, and the fake refuses one that pins no API version or asks for the
// wrong media type — so a source that stopped sending either fails here
// rather than against a GitHub that changed under it.
func TestGitHubIdentifiesItselfOnEveryRequest(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"}); err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.agents) != 1 {
		t.Fatalf("user agents = %v, want exactly one", fake.agents)
	}
	for agent := range fake.agents {
		if agent != core.UserAgent("v1.0.0") {
			t.Errorf("User-Agent = %q, want %q", agent, core.UserAgent("v1.0.0"))
		}
	}
}

func TestGitHubStopsAtTheEdgeOfTheWindow(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{
		Environment: "production",
		Since:       coretest.ContractNow.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	// One page of records, and the state of the one record inside the
	// window: the records behind it are never asked about.
	if got := len(fake.Requests()); got != 2 {
		t.Errorf("the window cost %d requests (%v), want 2", got, fake.Requests())
	}
}

func TestGitHubStopsAtTheLimit(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("a limit of 1 returned %d records", len(records))
	}
	if got := len(fake.Requests()); got != 2 {
		t.Errorf("a limit of 1 cost %d requests (%v), want 2", got, fake.Requests())
	}
}

func TestGitHubWalksEveryPage(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records across %d pages of %d, want 3", len(records), len(fake.Requests()), fake.pageSize)
	}
	pages := 0
	for _, path := range fake.Requests() {
		if !strings.HasSuffix(path, "/statuses") {
			pages++
		}
	}
	if pages != 2 {
		t.Errorf("the walk read %d pages, want 2: three records at two a page", pages)
	}
}

func TestGitHubReadsTheStateFromTheNewestStatusThatMeansSomething(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	// The fake numbers the records newest first across every environment,
	// so the oldest production deployment is id 4. GitHub marks it
	// inactive once a later deployment supersedes it, which says nothing
	// about whether it worked.
	fake.pushStatus(4, ghDeployInactive, coretest.ContractNow.Add(-23*time.Hour))

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	superseded := records[len(records)-1]
	if superseded.SHA != coretest.SHAOld {
		t.Fatalf("the oldest production record is %s, want %s", superseded.SHA, coretest.SHAOld)
	}
	if !superseded.Succeeded() {
		t.Errorf("the superseded deployment reads as %q; inactive is not an outcome, and what production ran at the time depends on it", superseded.State)
	}
}

func TestGitHubReadsADeploymentNobodyReportedOnAsPending(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	// Id 2 is the newest production record; id 1 is the staging one.
	fake.clearStatuses(2)

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if records[0].State != core.DeployPending {
		t.Errorf("a deployment with no status reads as %q, want %q", records[0].State, core.DeployPending)
	}
	if records[0].Succeeded() {
		t.Error("a deployment nothing has reported on was read as proof that the commit is running")
	}
}

func TestGitHubRefusesAStateItDoesNotUnderstand(t *testing.T) {
	fake, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	fake.setStatus(2, "sideways", coretest.ContractNow)

	_, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("Deployments = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "sideways") {
		t.Errorf("the refusal is %q, want it to name the state it could not read", err)
	}
}

func TestGitHubCarriesTheEvidenceOfTheDeployment(t *testing.T) {
	_, srv := newGitHubServer(t, coretest.ContractDeploys())
	source := newGitHubSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	newest := records[0]
	if newest.URL != "https://ci.test/runs/success" {
		t.Errorf("the record links to %q, want the run behind it", newest.URL)
	}
	if newest.Description != "the deployer said success" {
		t.Errorf("the record says %q, want the line the deployer left", newest.Description)
	}
	if !newest.CreatedAt.Equal(coretest.ContractNow.Add(-23 * time.Hour)) {
		t.Errorf("the record was created at %s, want when the deployment was asked for", newest.CreatedAt)
	}
	if !newest.UpdatedAt.Equal(coretest.ContractNow.Add(-23*time.Hour + ghStatusDelay)) {
		t.Errorf("the record changed state at %s, want when the status was written", newest.UpdatedAt)
	}
}

func TestGitHubAsksConditionallyTheSecondTime(t *testing.T) {
	_, srv := newGitHubServer(t, coretest.ContractDeploys())
	client, err := core.NewClient(core.ClientOptions{
		BaseURL: srv.URL,
		Token:   "test-token",
		Clock:   coretest.NewClock(coretest.ContractNow),
		Cache:   &core.MemoryCache{},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	source, err := NewGitHub(GitHubOptions{Client: client, Owner: fakeOwner, Repo: fakeRepo, Capabilities: deployCapabilities(t)})
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	query := core.DeployQuery{Environment: "production"}
	first, err := source.Deployments(t.Context(), query)
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	second, err := source.Deployments(t.Context(), query)
	if err != nil {
		t.Fatalf("Deployments again: %v", err)
	}
	if !slices.Equal(shas(first), shas(second)) {
		t.Errorf("the cached read returned %v, want %v", shas(second), shas(first))
	}
}

func shas(records []core.Deployment) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.SHA)
	}
	return out
}
