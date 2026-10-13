package deploys

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// newStatusSource builds a commit status source talking to srv, reading the
// contexts the fake's deployer reports production under.
func newStatusSource(t *testing.T, srv *httptest.Server, contexts ...string) *CommitStatus {
	t.Helper()
	if len(contexts) == 0 {
		contexts = []string{fakeProdContext}
	}
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
	source, err := NewCommitStatus(CommitStatusOptions{
		Client:       client,
		Owner:        fakeOwner,
		Repo:         fakeRepo,
		Environment:  "production",
		Contexts:     contexts,
		Capabilities: deployCapabilities(t),
	})
	if err != nil {
		t.Fatalf("NewCommitStatus: %v", err)
	}
	return source
}

// contractCommitStatus stands a fake GitHub up in front of the fixture and
// reads it through commit statuses.
func contractCommitStatus(t *testing.T, fx coretest.DeployFixture) core.DeploySource {
	t.Helper()
	_, srv := newStatusServer(t, fx)
	return newStatusSource(t, srv)
}

func TestCommitStatusMeetsTheDeployContract(t *testing.T) {
	coretest.DeployContract(t, contractCommitStatus)
}

func TestNewCommitStatusRefusesAnIncompleteSource(t *testing.T) {
	caps := deployCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: "https://api.github.com"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	full := CommitStatusOptions{
		Client: client, Owner: "a", Repo: "b",
		Environment: "production", Contexts: []string{"ci/deploy"}, Capabilities: caps,
	}
	cases := []struct {
		name   string
		change func(*CommitStatusOptions)
	}{
		{"no client", func(o *CommitStatusOptions) { o.Client = nil }},
		{"no repository", func(o *CommitStatusOptions) { o.Repo = "" }},
		{"no environment", func(o *CommitStatusOptions) { o.Environment = "" }},
		{"no contexts", func(o *CommitStatusOptions) { o.Contexts = nil }},
		{"no declaration", func(o *CommitStatusOptions) { o.Capabilities = core.CapabilitySet{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := full
			tc.change(&opts)
			if _, err := NewCommitStatus(opts); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("NewCommitStatus = %v, want ErrValidation", err)
			}
		})
	}
}

func TestCommitStatusRefusesAQueryWithNoEnvironment(t *testing.T) {
	_, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{}); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Deployments without an environment = %v, want ErrValidation", err)
	}
}

func TestCommitStatusIsTheSourceItSaysItIs(t *testing.T) {
	_, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	if source.Kind() != core.SourceCommitStatus {
		t.Errorf("Kind() = %q, want %q", source.Kind(), core.SourceCommitStatus)
	}
	if source.Slug() != fakeOwner+"/"+fakeRepo {
		t.Errorf("Slug() = %q, want the application repository", source.Slug())
	}
}

// TestCommitStatusMatchesAContextExactlyOrByPrefix is the acceptance
// criterion of plan item 7.5: a configured context is a whole name unless it
// ends in a star, and nothing else about it is a pattern.
func TestCommitStatusMatchesAContextExactlyOrByPrefix(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		context  string
		want     bool
	}{
		{"the same name", []string{"argocd/app-prod"}, "argocd/app-prod", true},
		{"a different name", []string{"argocd/app-prod"}, "argocd/app-staging", false},
		{"a longer name is not the name", []string{"argocd/app"}, "argocd/app-prod", false},
		{"a star takes the rest", []string{"argocd/app*"}, "argocd/app-prod", true},
		{"a star still needs its prefix", []string{"argocd/app*"}, "flux/app-prod", false},
		{"a bare star takes everything", []string{"*"}, "anything at all", true},
		{"a star in the middle is a character", []string{"argocd/*/prod"}, "argocd/app/prod", false},
		{"a star in the middle matches itself", []string{"argocd/*/prod"}, "argocd/*/prod", true},
		{"case is the forge's, not flow's", []string{"argocd/app-prod"}, "argocd/App-Prod", false},
		{"one of several", []string{"flux/app", "argocd/app-prod"}, "argocd/app-prod", true},
		{"none of several", []string{"flux/app", "argocd/app-prod"}, "ci/test", false},
		{"nothing matches nothing", nil, "argocd/app-prod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesContext(tc.patterns, tc.context); got != tc.want {
				t.Errorf("matchesContext(%q, %q) = %v, want %v", tc.patterns, tc.context, got, tc.want)
			}
		})
	}
}

// TestCommitStatusReadsEveryContextItWasGiven: a prefix pattern that two of a
// commit's statuses answer to is two deployments, because two deployers
// reported.
func TestCommitStatusReadsEveryContextItWasGiven(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	fake.push(coretest.SHADeployed, "argocd/app-prod-eu", ghStatusSuccess, coretest.ContractNow.Add(-20*time.Hour))
	source := newStatusSource(t, srv, "argocd/app-prod*")

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	var onTip []core.Deployment
	for _, record := range records {
		if record.SHA == coretest.SHADeployed {
			onTip = append(onTip, record)
		}
	}
	if len(onTip) != 2 {
		t.Fatalf("got %d deployments of %s, want the two regions that reported", len(onTip), coretest.SHADeployed)
	}
	for _, record := range records {
		if record.Environment != "production" {
			t.Errorf("record %s says it deployed to %q; the configured environment is what a context stands for", record.ID, record.Environment)
		}
	}
}

// TestCommitStatusTakesTheNewestWordOnAContext: a deployer that said pending
// and then success recorded one deployment, not two.
func TestCommitStatusTakesTheNewestWordOnAContext(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	// Newest first, as GitHub serves them: the success landed after the
	// pending it replaced.
	fake.push(coretest.SHADeployed, fakeProdContext, ghStatusSuccess, coretest.ContractNow.Add(-10*time.Hour))
	source := newStatusSource(t, srv)

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: coretest.SHADeployed})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records for one context's history, want 1: %+v", len(records), records)
	}
	if !records[0].Succeeded() || !records[0].CreatedAt.Equal(coretest.ContractNow.Add(-10*time.Hour)) {
		t.Errorf("the record is %+v, want the newest status on the context", records[0])
	}
}

// TestCommitStatusAnswersForOneEnvironmentOnly: a commit status carries no
// environment, so the contexts stand for one and a question about another is
// not this source's to answer.
func TestCommitStatusAnswersForOneEnvironmentOnly(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)

	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "staging"})
	if err != nil {
		t.Fatalf("Deployments for another environment: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("got %d staging records from a production source: %+v", len(records), records)
	}
	if asked := fake.Requests(); len(asked) != 0 {
		t.Errorf("the provider was asked %v about an environment this source does not speak for", asked)
	}

	// The spelling is the configuration's, not the caller's.
	same, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "PRODUCTION"})
	if err != nil {
		t.Fatalf("Deployments for the same environment spelled differently: %v", err)
	}
	if len(same) == 0 {
		t.Error("a query for PRODUCTION found nothing; an environment name is not case sensitive")
	}
}

// TestCommitStatusLetsTheProviderNarrowTheWalk: the window is put to the
// commits endpoint, because GitHub can be asked for the commits of a window
// and never for the statuses of one.
func TestCommitStatusLetsTheProviderNarrowTheWalk(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	since := coretest.ContractNow.Add(-24 * time.Hour)
	if _, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Since: since}); err != nil {
		t.Fatalf("Deployments: %v", err)
	}

	var listed string
	for i, path := range fake.Requests() {
		if path == "/repos/"+fakeOwner+"/"+fakeRepo+"/commits" {
			listed = fake.Queries()[i]
			break
		}
	}
	if listed == "" {
		t.Fatal("the trunk's commits were never listed")
	}
	for _, want := range []string{"sha=" + fakeTrunk, "since=" + url.QueryEscape(since.UTC().Format(time.RFC3339)), "per_page=100"} {
		if !strings.Contains(listed, want) {
			t.Errorf("the commits query is %q, want it to carry %q", listed, want)
		}
	}
	for _, path := range fake.Requests() {
		if strings.Contains(path, coretest.SHAOld) {
			t.Errorf("the statuses of %s were read though its commit is outside the window", coretest.SHAOld)
		}
	}
}

// TestCommitStatusAsksTheRepositoryWhichBranchTheTrunkIs: configuration has no
// key for it, so the repository is asked — once.
func TestCommitStatusAsksTheRepositoryWhichBranchTheTrunkIs(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	for range 2 {
		if _, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"}); err != nil {
			t.Fatalf("Deployments: %v", err)
		}
	}
	repo := "/repos/" + fakeOwner + "/" + fakeRepo
	asked := 0
	for _, path := range fake.Requests() {
		if path == repo {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("the repository was asked for its default branch %d times, want 1", asked)
	}
}

// TestCommitStatusRefusesARepositoryWithNoTrunk: an answer with no default
// branch is not a trunk of "", which would list the commits of nothing.
func TestCommitStatusRefusesARepositoryWithNoTrunk(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	fake.branch = ""
	source := newStatusSource(t, srv)
	_, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("Deployments against a repository with no trunk = %v, want ErrUnavailable", err)
	}
}

// TestCommitStatusNeedsNoTrunkToAnswerAboutOneCommit: a query naming a commit
// is about that commit, so nothing is walked and nothing is listed.
func TestCommitStatusNeedsNoTrunkToAnswerAboutOneCommit(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	fake.branch = ""
	source := newStatusSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: coretest.SHAMerged})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 || records[0].SHA != coretest.SHAMerged {
		t.Fatalf("got %+v, want the one deployment of %s", records, coretest.SHAMerged)
	}
	if slices.Contains(fake.Requests(), "/repos/"+fakeOwner+"/"+fakeRepo+"/commits") {
		t.Error("the trunk was walked to answer about one commit")
	}
}

// TestCommitStatusStopsWalkingOnceItHasEnough: a limit is satisfied by the
// newest commits, so the rest of the trunk is not read one request at a time.
func TestCommitStatusStopsWalkingOnceItHasEnough(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", Limit: 1})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 || records[0].SHA != coretest.SHADeployed {
		t.Fatalf("got %+v, want only the newest deployment", records)
	}
	for _, path := range fake.Requests() {
		if strings.Contains(path, coretest.SHAOld) {
			t.Errorf("the statuses of %s were read though one record was asked for", coretest.SHAOld)
		}
	}
}

// TestCommitStatusRefusesAStateItDoesNotUnderstand: guessing would either
// hide a deployment or invent one.
func TestCommitStatusRefusesAStateItDoesNotUnderstand(t *testing.T) {
	fake, srv := newStatusServer(t, coretest.ContractDeploys())
	fake.push(coretest.SHADeployed, fakeProdContext, "partially_deployed", coretest.ContractNow.Add(-time.Hour))
	source := newStatusSource(t, srv)

	_, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("Deployments = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "partially_deployed") {
		t.Errorf("the error is %q, want it to name the word the provider used", err)
	}
}

// TestCommitStatusCarriesTheEvidenceOfTheDeployment: the line the deployer
// left and the address of the run behind it are what a report shows.
func TestCommitStatusCarriesTheEvidenceOfTheDeployment(t *testing.T) {
	_, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production", SHA: coretest.SHADeployed})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	switch {
	case record.ID == "":
		t.Error("the record has no identifier, so nothing can refer to it")
	case record.Description != "the deployer said success":
		t.Errorf("Description = %q, want the deployer's own line", record.Description)
	case record.URL != "https://ci.test/runs/success":
		t.Errorf("URL = %q, want the address of the run behind it", record.URL)
	case !record.UpdatedAt.Equal(record.CreatedAt):
		t.Errorf("UpdatedAt = %v, want the moment the status was written", record.UpdatedAt)
	}
}

// TestCommitStatusWalksEveryPage: the fake pages at two, and a source that
// read only the first would miss the oldest deployment.
func TestCommitStatusWalksEveryPage(t *testing.T) {
	_, srv := newStatusServer(t, coretest.ContractDeploys())
	source := newStatusSource(t, srv)
	records, err := source.Deployments(t.Context(), core.DeployQuery{Environment: "production"})
	if err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if !slices.ContainsFunc(records, func(d core.Deployment) bool { return d.SHA == coretest.SHAOld }) {
		t.Errorf("got %+v, want the deployment of %s from the second page of commits", records, coretest.SHAOld)
	}
}
