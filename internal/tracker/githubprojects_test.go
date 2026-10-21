package tracker

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// gitHubProjectsCapabilities is the declaration flow ships, read from the tree
// rather than embedded: internal packages do not import assets (spec 01 §2).
func gitHubProjectsCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(GitHubProjectsKind)
	if err != nil {
		t.Fatalf("Of(github-projects): %v", err)
	}
	return set
}

// newGitHubProjectsAdapter builds an adapter talking to srv.
func newGitHubProjectsAdapter(t *testing.T, srv *httptest.Server, adjust func(*GitHubProjectsOptions)) *GitHubProjects {
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
	opts := GitHubProjectsOptions{
		Client:       client,
		Owner:        ghFixtureOwner,
		OwnerType:    OwnerUser,
		Number:       1,
		Repo:         ghFixtureOwner + "/widget",
		Capabilities: gitHubProjectsCapabilities(t),
	}
	if adjust != nil {
		adjust(&opts)
	}
	adapter, err := NewGitHubProjects(opts)
	if err != nil {
		t.Fatalf("NewGitHubProjects: %v", err)
	}
	return adapter
}

// contractGitHubProjects stands a fake GitHub up in front of the fixture, with
// the board belonging to an owner of that kind.
func contractGitHubProjects(ownerType string) coretest.NewTracker {
	return func(t *testing.T, fx *coretest.TrackerFixture) core.Tracker {
		t.Helper()
		fake, srv := newGitHubProjectsServer(t, *fx)
		fake.ownerType = ownerType
		return newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.OwnerType = ownerType })
	}
}

// The whole tracker contract now passes against GitHub's own shapes: every
// method it describes has landed (plan items 3.8 to 3.11). The suite is run
// whole rather than as a named subset, so a case added to the contract later
// runs here without anybody remembering to list it.
//
// Both owner kinds sit it. A person's projects and an organisation's are
// reached through different fields of GitHub's schema and shown at different
// addresses, so a board under one owner proves nothing about the other.
func TestGitHubProjectsMeetsTheTrackerContract(t *testing.T) {
	for _, ownerType := range []string{OwnerUser, OwnerOrg} {
		t.Run(ownerType, func(t *testing.T) {
			coretest.TrackerContract(t, contractGitHubProjects(ownerType))
		})
	}
}

func TestNewGitHubProjectsRefusesAnIncompleteAdapter(t *testing.T) {
	caps := gitHubProjectsCapabilities(t)
	client, err := core.NewClient(core.ClientOptions{BaseURL: GitHubAPI})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct {
		name string
		opts GitHubProjectsOptions
	}{
		{"no client", GitHubProjectsOptions{Owner: "acme", OwnerType: OwnerUser, Capabilities: caps}},
		{"no owner", GitHubProjectsOptions{Client: client, OwnerType: OwnerUser, Capabilities: caps}},
		{"no owner type", GitHubProjectsOptions{Client: client, Owner: "acme", Capabilities: caps}},
		{"a nonsense owner type", GitHubProjectsOptions{Client: client, Owner: "acme", OwnerType: "team", Capabilities: caps}},
		{"a negative number", GitHubProjectsOptions{Client: client, Owner: "acme", OwnerType: OwnerOrg, Number: -1, Capabilities: caps}},
		{"no declaration", GitHubProjectsOptions{Client: client, Owner: "acme", OwnerType: OwnerUser}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewGitHubProjects(tc.opts); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("NewGitHubProjects = %v, want it to wrap ErrValidation", err)
			}
		})
	}
}

// A project with no number yet is not a refusal: resolving one is exactly
// what setup does, and an adapter that could not be built before its project
// was known could never be the thing that discovers it (spec 07 §3).
func TestNewGitHubProjectsAcceptsAnUnresolvedProject(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 0 })
	if _, err := adapter.Discover(t.Context(), core.ItemTrackerProject, nil); err != nil {
		t.Fatalf("Discover before a project is resolved: %v", err)
	}
}

// ParseRef reads the address a human pasted. The owner in it must be the owner
// flow is configured for: a number means a different board under a different
// login, and recording one against the other would be silent and wrong.
func TestGitHubProjectsParseRef(t *testing.T) {
	cases := []struct {
		name      string
		ownerType string
		raw       string
		wantID    string
	}{
		{name: "a user's project", ownerType: OwnerUser, raw: "https://github.com/users/acme/projects/7", wantID: "7"},
		{name: "an organisation's", ownerType: OwnerOrg, raw: "https://github.com/orgs/acme/projects/7", wantID: "7"},
		{name: "the long spelling", ownerType: OwnerOrg, raw: "https://github.com/organizations/acme/projects/2", wantID: "2"},
		{name: "a view of it", ownerType: OwnerUser, raw: "https://github.com/users/acme/projects/7/views/3", wantID: "7"},
		{name: "surrounded by spaces", ownerType: OwnerUser, raw: "  https://github.com/users/acme/projects/7  ", wantID: "7"},
		{name: "a differently spelt login", ownerType: OwnerUser, raw: "https://github.com/users/ACME/projects/7", wantID: "7"},
		{name: "empty", ownerType: OwnerUser, raw: "   "},
		{name: "not an address", ownerType: OwnerUser, raw: "project 7"},
		{name: "a repository", ownerType: OwnerUser, raw: "https://github.com/acme/widget/issues/7"},
		{name: "another owner", ownerType: OwnerUser, raw: "https://github.com/users/other/projects/7"},
		{name: "the other kind of owner", ownerType: OwnerUser, raw: "https://github.com/orgs/acme/projects/7"},
		{name: "no number", ownerType: OwnerUser, raw: "https://github.com/users/acme/projects/none"},
		{name: "a zero number", ownerType: OwnerUser, raw: "https://github.com/users/acme/projects/0"},
	}
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.OwnerType = tc.ownerType })
			ref, err := adapter.ParseRef(tc.raw)
			if tc.wantID == "" {
				if !errors.Is(err, core.ErrValidation) {
					t.Fatalf("ParseRef(%q) = %+v, %v; want it to wrap ErrValidation", tc.raw, ref, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", tc.raw, err)
			}
			if ref.ID != tc.wantID {
				t.Errorf("ParseRef(%q).ID = %q, want %q", tc.raw, ref.ID, tc.wantID)
			}
			if ref.What != core.ItemTrackerProject {
				t.Errorf("ParseRef(%q).What = %q, want %q", tc.raw, ref.What, core.ItemTrackerProject)
			}
			if ref.URL == "" {
				t.Error("the reference carries no address")
			}
		})
	}
}

// A reference flow cannot read says how to write one, and the example it gives
// is an address of the kind the adapter is configured for.
func TestGitHubProjectsParseRefSaysHowToWriteOne(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.OwnerType = OwnerOrg })
	_, err := adapter.ParseRef("the delivery board")
	if err == nil {
		t.Fatal("want a refusal")
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "/orgs/acme/projects/") {
		t.Errorf("the refusal does not show an organisation's address: %s", fix)
	}
}

// Discovery walks the cursor: the page size is a page size and not a ceiling,
// so a team with more boards than one page holds still sees all of them.
func TestGitHubProjectsDiscoverWalksEveryPage(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	for i := 3; i <= 7; i++ {
		fake.projects = append(fake.projects, ghProjectNode{
			ID: "PVT_" + strings.Repeat("x", i), Number: i, Title: "Board " + strings.Repeat("I", i),
		})
	}
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	candidates, err := adapter.Discover(t.Context(), core.ItemTrackerProject, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) != len(fake.projects) {
		t.Fatalf("got %d candidates, want the owner's %d projects", len(candidates), len(fake.projects))
	}
	if fake.asked() < 4 {
		t.Errorf("the whole connection came back in %d queries, so the fake did not page", fake.asked())
	}
}

// A closed board is not offered. Discovery takes the only candidate
// automatically (spec 07 §3), and a board the team has finished with is the
// one answer that must never be chosen on their behalf.
func TestGitHubProjectsDiscoverSkipsAClosedProject(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.projects[1].Closed = true
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	candidates, err := adapter.Discover(t.Context(), core.ItemTrackerProject, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.Name == fake.projects[1].Title {
			t.Errorf("a closed board was offered: %+v", candidate)
		}
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want the one open board", len(candidates))
	}
}

// Nothing but the board is discoverable: everything else flow resolves is a
// part of a board it has already been pointed at.
func TestGitHubProjectsDiscoversNothingElse(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	candidates, err := adapter.Discover(t.Context(), core.ItemTrackerStatusOptions, nil)
	if err != nil {
		t.Fatalf("Discover(status_options): %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates for something that is not discoverable", len(candidates))
	}
	if fake.asked() != 0 {
		t.Errorf("the adapter asked %d questions about something it cannot discover", fake.asked())
	}
}

// A hint names a project exactly rather than searching for one, because a
// setup step that silently offers a near miss is worse than one that offers
// nothing.
func TestGitHubProjectsDiscoverNarrowsByNumber(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	candidates, err := adapter.Discover(t.Context(), core.ItemTrackerProject, map[string]string{"number": "2"})
	if err != nil {
		t.Fatalf("Discover with a number: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != "2" {
		t.Fatalf("got %+v, want only the board numbered 2", candidates)
	}
}

// GitHub addresses a person's projects and an organisation's through
// different fields of the same schema, which is what tracker.owner_type
// decides. Asking the wrong one finds nothing rather than erroring, so an
// adapter that ignored the setting would quietly discover no boards at all.
func TestGitHubProjectsAsksTheOwnerItWasConfiguredFor(t *testing.T) {
	cases := []struct {
		name      string
		holder    string
		asked     string
		wantField string
		wantAny   bool
	}{
		{name: "a user's boards", holder: OwnerUser, asked: OwnerUser, wantField: "user(login", wantAny: true},
		{name: "an organisation's", holder: OwnerOrg, asked: OwnerOrg, wantField: "organization(login", wantAny: true},
		{name: "a user asked as an organisation", holder: OwnerUser, asked: OwnerOrg, wantField: "organization(login"},
		{name: "an organisation asked as a user", holder: OwnerOrg, asked: OwnerUser, wantField: "user(login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.ownerType = tc.holder
			adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.OwnerType = tc.asked })
			candidates, err := adapter.Discover(t.Context(), core.ItemTrackerProject, nil)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if got := len(candidates) > 0; got != tc.wantAny {
				t.Errorf("found candidates = %v, want %v", got, tc.wantAny)
			}
			if !strings.Contains(fake.queries[0], tc.wantField) {
				t.Errorf("the query does not ask for %s:\n%s", tc.wantField, fake.queries[0])
			}
		})
	}
}

// The board and its columns come back together: the columns are the options
// of the board's own Status field, so asking for them separately would be two
// queries about one board against a shared points budget (spec 11 §2).
func TestGitHubProjectsObservesTheBoardAndItsColumnsInOneQuery(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	observation, err := adapter.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Items) != len(fake.options[1]) {
		t.Fatalf("observed %d columns, want the board's %d", len(observation.Items), len(fake.options[1]))
	}
	if fake.asked() != 1 {
		t.Errorf("one observation cost %d queries", fake.asked())
	}
}

// A column is its name and the id the provider gave it, and nothing else.
// Its colour and its description are the provider's own, and recording them
// would make the observation's hash change when somebody recoloured a column
// (spec 06 §2, spec 07 §7).
func TestGitHubProjectsObservesAColumnAsItsNameAndID(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	observation, err := adapter.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for _, item := range observation.Items {
		if item.Name == "" || item.ID == "" {
			t.Errorf("the column %+v cannot be compared or recorded", item)
		}
		if len(item.Fields) != 0 {
			t.Errorf("the column %q carries fields flow never asked for: %v", item.Name, item.Fields)
		}
		if !slices.ContainsFunc(fake.options[1], func(o ghStatusOption) bool { return o.Name == item.Name }) {
			t.Errorf("the column %q is not one of the board's", item.Name)
		}
	}
}

// The board is identified by its number everywhere flow records it — in
// configuration, in the lock and in the address bar — so the observation says
// the number and not the opaque node id the query works in.
func TestGitHubProjectsObservesTheBoardByItsNumber(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 2 })
	observation, err := adapter.Observe(t.Context(), core.ItemTrackerProject)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observation.Exists || len(observation.Items) != 1 {
		t.Fatalf("Observe(project) = %+v, want the one board", observation)
	}
	if observation.Items[0].ID != "2" {
		t.Errorf("the board's id is %q, want its number", observation.Items[0].ID)
	}
	if observation.Items[0].Name != "Research" {
		t.Errorf("the board's name is %q, want its title", observation.Items[0].Name)
	}
}

// A board that is not there is an observation that does not exist, not an
// error: that is the plan's "the project has to be found first" case.
func TestGitHubProjectsObservesAMissingBoard(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 404 })
	for _, what := range []string{core.ItemTrackerProject, core.ItemTrackerStatusOptions} {
		observation, err := adapter.Observe(t.Context(), what)
		if err != nil {
			t.Fatalf("Observe(%s) of a board that is not there: %v", what, err)
		}
		if observation.Exists {
			t.Errorf("Observe(%s) claims a board nobody has", what)
		}
	}
}

// A board whose Status field somebody deleted holds no columns, which is a
// difference the plan can resolve rather than a failure.
func TestGitHubProjectsObservesABoardWithNoStatusField(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	delete(fake.options, 1)
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	observation, err := adapter.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observation.Exists || len(observation.Items) != 0 {
		t.Errorf("Observe = %+v, want nothing observed", observation)
	}
}

// A per-view setting the API cannot report is observed as not there, which is
// what the declaration says by refusing to verify it: resolution turns that
// into the compensation or the attestation (spec 05 §4).
func TestGitHubProjectsObservesNothingItCannotSee(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	// What a built-in workflow does is not something the Projects API will
	// say either, so a person's word is still the only evidence at setup
	// and the live audit is what watches the cards afterwards (spec 07 §6).
	// Those cost a query — whether the switch is on is answerable — so they
	// are not in this list, which is about the ones that cost nothing.
	unobservable := []string{core.ItemTrackerWIP, core.ItemTrackerExpedite}
	for _, what := range unobservable {
		observation, err := adapter.Observe(t.Context(), what)
		if err != nil {
			t.Fatalf("Observe(%s): %v", what, err)
		}
		if observation.Exists || observation.What != what {
			t.Errorf("Observe(%s) = %+v, want an observation that does not exist", what, observation)
		}
	}
	if fake.asked() != 0 {
		t.Errorf("the adapter asked %d questions about settings the API cannot answer", fake.asked())
	}
}

// Observing before a board has been resolved is the user's to fix, so it says
// which setting is missing rather than failing as the provider's fault.
func TestGitHubProjectsObserveRefusesWithoutANumber(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 0 })
	_, err := adapter.Observe(t.Context(), core.ItemTrackerProject)
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Observe without a number = %v, want it to wrap ErrValidation", err)
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "tracker.project") {
		t.Errorf("the refusal does not name the setting to fill in: %s", fix)
	}
}

// GraphQL answers 200 when it fails, so the errors array is read as the
// refusal it is, with the sentinel the provider's own type means.
func TestGitHubProjectsReadsAGraphQLRefusal(t *testing.T) {
	cases := []struct {
		errType string
		want    error
	}{
		{"NOT_FOUND", core.ErrNotFound},
		{"FORBIDDEN", core.ErrPermission},
		{"INSUFFICIENT_SCOPES", core.ErrPermission},
		{"UNAUTHORIZED", core.ErrPermission},
		{"RATE_LIMITED", core.ErrRateLimited},
		{"SOMETHING_NEW", core.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.errType, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.refusalType, fake.refusalMessage = tc.errType, "the provider said no"
			adapter := newGitHubProjectsAdapter(t, srv, nil)
			_, err := adapter.Observe(t.Context(), core.ItemTrackerStatusOptions)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Observe = %v, want it to wrap %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "the provider said no") {
				t.Errorf("the refusal drops what the provider said: %v", err)
			}
		})
	}
}

// A classic token announces its scopes in a header nothing else sends, and a
// role and a scope are two limits that both apply: reading the board proves
// the credential can read it, but writing to it needs the scope as well
// (spec 05 §3).
func TestGitHubProjectsProbeTakesTheScopeAndTheBoardTogether(t *testing.T) {
	cases := []struct {
		name    string
		scopes  *string
		number  int
		want    []string
		notWant []string
	}{
		{
			name:   "a classic token that may write",
			scopes: ptr("repo, project"),
			number: 1,
			want:   []string{ghTokenProjectRead, ghTokenProjectWrite, ghTokenIssuesWrite},
		},
		{
			name:    "a classic token that may only read",
			scopes:  ptr("read:project"),
			number:  1,
			want:    []string{ghTokenProjectRead},
			notWant: []string{ghTokenProjectWrite, ghTokenIssuesWrite},
		},
		{
			name:   "a credential that announces no scopes at all",
			scopes: nil,
			number: 1,
			want:   []string{ghTokenProjectRead, ghTokenProjectWrite, ghTokenIssuesWrite},
		},
		{
			name:    "a board nobody has pointed flow at yet",
			scopes:  ptr("repo, project"),
			number:  0,
			want:    []string{ghTokenIssuesWrite},
			notWant: []string{ghTokenProjectRead, ghTokenProjectWrite},
		},
		{
			name:    "a board the credential cannot read",
			scopes:  ptr("repo, project"),
			number:  404,
			want:    []string{ghTokenIssuesWrite},
			notWant: []string{ghTokenProjectRead, ghTokenProjectWrite},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.scopes = tc.scopes
			adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = tc.number })
			permissions, err := adapter.Probe(t.Context())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			for _, token := range tc.want {
				if !permissions.Has(token) {
					t.Errorf("Probe did not grant %q: %v", token, permissions.Tokens)
				}
			}
			for _, token := range tc.notWant {
				if permissions.Has(token) {
					t.Errorf("Probe granted %q, which nothing proved: %v", token, permissions.Tokens)
				}
			}
		})
	}
}

// Every token the declaration requires is a word the probe can say. A probe
// and a declaration that do not share a vocabulary can never agree, and the
// disagreement would read as a missing permission nobody could grant.
func TestGitHubProjectsProbeSpeaksTheVocabularyTheDeclarationRequires(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	permissions, err := adapter.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for name, capability := range gitHubProjectsCapabilities(t).Capabilities {
		for _, token := range capability.Requires {
			if !permissions.Has(token) {
				t.Errorf("%s requires %q, which the probe never grants", name, token)
			}
		}
	}
}

// The probe says what it read, because a permission flow asserts without
// evidence is a permission nobody can argue with (spec 05 §3).
func TestGitHubProjectsProbeSaysWhatItRead(t *testing.T) {
	cases := []struct {
		name   string
		scopes *string
		number int
		want   string
	}{
		{name: "the scopes themselves", scopes: ptr("repo, project"), number: 1, want: ghScopeHeader + ": repo, project"},
		{name: "no scopes at all", scopes: nil, number: 1, want: "announced no scopes"},
		{name: "the board it read", scopes: nil, number: 1, want: "read the project acme/projects/1"},
		{name: "a board that is not set", scopes: nil, number: 0, want: "no project number is set yet"},
		{name: "a board it could not read", scopes: nil, number: 404, want: "is not there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.scopes = tc.scopes
			adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = tc.number })
			permissions, err := adapter.Probe(t.Context())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if !slices.ContainsFunc(permissions.Evidence, func(line string) bool { return strings.Contains(line, tc.want) }) {
				t.Errorf("the evidence does not say %q: %v", tc.want, permissions.Evidence)
			}
		})
	}
}

// A board flow was never pointed at costs nothing to probe: there is nothing
// to try, and saying so is more use during setup than a refusal.
func TestGitHubProjectsProbeTriesNoBoardItHasNoNumberFor(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 0 })
	if _, err := adapter.Probe(t.Context()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if fake.asked() != 0 {
		t.Errorf("the probe sent %d queries about a board it has no number for", fake.asked())
	}
	if fake.visited("/user") != 1 {
		t.Errorf("the probe read the credential %d times, want once", fake.visited("/user"))
	}
}

// Enterprise Server serves its API beside the site rather than on a host of
// its own, and GraphQL beside /api/v3 rather than under it.
func TestGitHubProjectsEndpointsFollowTheAPIRoot(t *testing.T) {
	cases := []struct {
		base    string
		graphQL string
		site    string
	}{
		{base: GitHubAPI, graphQL: "https://api.github.com/graphql", site: "https://github.com"},
		{base: GitHubAPI + "/", graphQL: "https://api.github.com/graphql", site: "https://github.com"},
		{base: "https://git.acme.test/api/v3", graphQL: "https://git.acme.test/api/graphql", site: "https://git.acme.test"},
		{base: "https://git.acme.test/api/v3/", graphQL: "https://git.acme.test/api/graphql", site: "https://git.acme.test"},
	}
	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			if got := ghGraphQLEndpoint(tc.base); got != tc.graphQL {
				t.Errorf("ghGraphQLEndpoint(%q) = %q, want %q", tc.base, got, tc.graphQL)
			}
			if got := ghSite(tc.base); got != tc.site {
				t.Errorf("ghSite(%q) = %q, want %q", tc.base, got, tc.site)
			}
		})
	}
}

// TestGitHubProjectsIdentifiesItselfOnEveryRequest: every call — GraphQL and
// REST alike — carries flow's user agent, so that a repository's audit log
// says which tool acted. The fake refuses a request that pins no API version
// or asks for the wrong media type, so reaching the answers at all is the
// rest of the proof.
func TestGitHubProjectsIdentifiesItselfOnEveryRequest(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	if _, err := adapter.Observe(t.Context(), core.ItemTrackerStatusOptions); err != nil {
		t.Fatalf("Observe(status_options): %v", err)
	}
	if _, err := adapter.Observe(t.Context(), core.ItemTrackerLabels); err != nil {
		t.Fatalf("Observe(labels): %v", err)
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

// Nothing is left unimplemented: an item name a tracker has nothing for is a
// caller's mistake, not a gap, and the refusal lists what it could have asked
// about instead of naming the plan.
func TestGitHubProjectsRefusesAnItemNoTrackerHas(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	if _, err := adapter.Observe(t.Context(), "forge.branch_protection"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Observe(forge.branch_protection) = %v, want ErrValidation", err)
	} else if !strings.Contains(fixOf(t, err), core.ItemTrackerLabels) {
		t.Errorf("the refusal offers %q, which does not name the items a tracker has", fixOf(t, err))
	}
	_, err := adapter.Apply(t.Context(), core.Change{What: "forge.merge_settings", Op: core.ChangeCreate})
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("Apply(forge.merge_settings) = %v, want ErrValidation", err)
	}
}

// fixOf is the next action a refusal offers, which is where a [core.UserError]
// puts it rather than in the message.
func fixOf(t *testing.T, err error) string {
	t.Helper()
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("%v is not a core.UserError, so it tells nobody what to do", err)
	}
	return user.Fix
}

// whyOf is the explanation a refusal carries, which a [core.UserError] keeps
// apart from both the message and the next action.
func whyOf(t *testing.T, err error) string {
	t.Helper()
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("%v is not a core.UserError, so it explains nothing", err)
	}
	return user.Why
}

// ptr is the address of a value, for the fields a test sets to "this, and not
// nothing".
func ptr[T any](v T) *T { return &v }

// A board gathers work from several repositories, and the live-health audit
// can only judge a ticket against the pull requests of the repository flow is
// running in (spec 03 §1). Which one a ticket is in is therefore a fact about
// it, kept beside the key rather than parsed back out of it: a key is a thing
// people type, and the engine reading GitHub's spelling of one would be the
// engine knowing which tracker it was talking to.
func TestGitHubProjectsSaysWhichRepositoryEachTicketIsIn(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.items = append(fake.items, &ghFakeItem{
		id: "PVTI_901", number: 7, optionID: string(coretest.ColumnTodo),
		repo:      "acme/other",
		updatedAt: coretest.ContractNow,
		ticket:    core.Ticket{ID: "I_901", Title: "Elsewhere", UpdatedAt: coretest.ContractNow},
	})
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	all, err := adapter.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	in := map[string]string{}
	for _, ticket := range all {
		in[ticket.ID] = ticket.Repo
	}
	if got := in["I_901"]; got != "acme/other" {
		t.Errorf("the issue from elsewhere says it is in %q, want acme/other", got)
	}
	// The board's own tickets must say so too. An adapter that only filled
	// the field for strangers would leave every ordinary ticket looking like
	// one from a tracker that records no repository at all.
	if got := in["I_10"]; got != "acme/widget" {
		t.Errorf("this repository's own ticket says it is in %q, want acme/widget", got)
	}
}

// A ticket key is "#7" in the repository flow is configured for and
// "owner/repo#7" anywhere else, and the same adapter reads back both — a board
// gathers issues from several repositories, and a key that lost which one it
// came from would resolve against the wrong board next time.
func TestGitHubProjectsQualifiesAKeyFromAnotherRepository(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.items = append(fake.items, &ghFakeItem{
		id: "PVTI_900", number: 7, optionID: string(coretest.ColumnTodo),
		repo:       "acme/other",
		parentRepo: "acme/upstream",
		updatedAt:  coretest.ContractNow,
		ticket: core.Ticket{
			ID: "I_900", Title: "Elsewhere", Parent: "#3",
			UpdatedAt: coretest.ContractNow,
		},
	})
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	all, err := adapter.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	var found *core.Ticket
	for _, ticket := range all {
		if ticket.ID == "I_900" {
			found = &ticket
		}
	}
	if found == nil {
		t.Fatal("the issue from the other repository is not on the board flow read")
	}
	if found.Key != "acme/other#7" {
		t.Errorf("Key = %q, want acme/other#7: an unqualified key would resolve against the wrong repository", found.Key)
	}
	if found.Parent != "acme/upstream#3" {
		t.Errorf("Parent = %q, want acme/upstream#3", found.Parent)
	}

	// And the qualified key reads back as the same ticket.
	again, err := adapter.Ticket(t.Context(), found.Key)
	if err != nil {
		t.Fatalf("Ticket(%s): %v", found.Key, err)
	}
	if again.ID != found.ID {
		t.Errorf("Ticket(%s) = %s, want the ticket the board listed", found.Key, again.ID)
	}
}

// An issue in the repository flow is configured for is written "#7" and not
// qualified, however the provider spells the repository's name.
func TestGitHubProjectsDoesNotQualifyItsOwnRepository(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.items[0].repo = strings.ToUpper(fake.repo)
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	ticket, err := adapter.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket(#10): %v", err)
	}
	if ticket.Key != "#10" {
		t.Errorf("Key = %q, want #10: a repository's name is not case-sensitive", ticket.Key)
	}
}

// Only the two forms flow writes are keys. A bare number is not one: accepting
// it would resolve a reference from somewhere else — a Jira key, a pull
// request number — against this board, and the mistake would be silent.
func TestGitHubProjectsRefusesSomethingThatIsNotAKey(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	for _, key := range []string{"", "10", "PROJ-10", "#0", "#-1", "#ten", "acme#10", "a/b/c#10", "#"} {
		t.Run(key, func(t *testing.T) {
			if _, err := adapter.Ticket(t.Context(), key); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("Ticket(%q) = %v, want it to wrap ErrValidation", key, err)
			}
		})
	}
}

// The fake answers a document with the fields that document asked for, because
// a server volunteering one lets the adapter read a field it never selected —
// which is exactly how `isArchived` could have been missing from both shipped
// queries with every test still green. Both queries go through the same reader,
// so one that drifts out of the shape it can parse fails here rather than
// passing everywhere.
func TestTheProjectsFakeAnswersOnlyWhatTheQueryAsked(t *testing.T) {
	fake, _ := newGitHubProjectsServer(t, coretest.ContractTracker())
	asked := []string{"id", "isArchived", "updatedAt", "fieldValueByName"}

	for _, document := range []string{ghItemsQuery(OwnerUser), ghIssueItemQuery} {
		doc := fake.itemJSON(document, fake.items[0])
		for _, field := range asked {
			if _, ok := doc[field]; !ok {
				t.Errorf("the fake left %s out of its answer to a query that selected it", field)
			}
		}
	}

	unasked := fake.itemJSON(strings.Replace(ghItemsQuery(OwnerUser), " isArchived", "", 1), fake.items[0])
	if _, ok := unasked["isArchived"]; ok {
		t.Error("the fake volunteered isArchived to a query that did not select it, so no test could tell whether flow asks for it")
	}
}

// A write that changed nothing is not a write that worked. An item archived
// between the read and the write is answered with no errors and no item, and
// flow would otherwise report a move the board never made.
func TestGitHubProjectsRefusesAMoveTheProjectPerformedNothingFor(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.nullOnMoves = true
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	err := adapter.Move(t.Context(), "#10", coretest.ColumnDone)

	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a move the project performed nothing for = %v, want ErrConflict", err)
	}
	if why := whyOf(t, err); !strings.Contains(why, "nothing was written") {
		t.Errorf("the reason is %q, want it to say the board did not change", why)
	}
}

// Without a repository of its own, flow cannot say which repository "#7" is
// in, and says so rather than guessing.
func TestGitHubProjectsCannotReadABareKeyWithNoRepository(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Repo = "" })
	_, err := adapter.Ticket(t.Context(), "#10")
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Ticket(#10) = %v, want it to wrap ErrValidation", err)
	}
	if !strings.Contains(fixOf(t, err), "owner/repo#7") {
		t.Errorf("the refusal does not say how to write a key: %q", fixOf(t, err))
	}
}

// A board holds drafts and pull requests as well as issues. They are not
// tickets: there is no key to refer to them by and nothing a check can read.
func TestGitHubProjectsSkipsAnItemThatIsNotAnIssue(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	want := fake.count()
	fake.items = append(fake.items, &ghFakeItem{id: "PVTI_draft", draft: true, updatedAt: coretest.ContractNow})
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	all, err := adapter.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(all) != want {
		t.Errorf("got %d tickets, want %d: a draft note is not a ticket", len(all), want)
	}
}

// A ticket last changed when either half of it did. Editing the issue does not
// touch the item and moving the item does not touch the issue, so a window
// built on one of them alone would miss work.
func TestGitHubProjectsTicketChangedWhenEitherHalfDid(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	moved := coretest.ContractNow.Add(-time.Minute)
	fake.items[0].updatedAt = moved
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	ticket, err := adapter.Ticket(t.Context(), "#10")
	if err != nil {
		t.Fatalf("Ticket(#10): %v", err)
	}
	if !ticket.UpdatedAt.Equal(moved) {
		t.Errorf("UpdatedAt = %s, want the item's %s: the board moved after the issue was last edited",
			ticket.UpdatedAt, moved)
	}
	recent, err := adapter.Tickets(t.Context(), core.TicketQuery{UpdatedSince: coretest.ContractNow.Add(-2 * time.Minute)})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(recent) != 1 || recent[0].Key != "#10" {
		t.Errorf("the window returned %d tickets, want the one the board moved", len(recent))
	}
}

// Why a ticket was closed is GitHub's state reason, and a reason flow has no
// name for is no reason at all: spec 07 §6 reads only whether the work was
// abandoned, and inventing a third answer would make a check say something the
// tracker never said.
func TestGitHubProjectsReadsWhyATicketWasClosed(t *testing.T) {
	cases := map[string]string{
		"COMPLETED":   core.ClosedCompleted,
		"not_planned": core.ClosedNotPlanned,
		"DUPLICATE":   "",
		"":            "",
	}
	for reason, want := range cases {
		t.Run(reason, func(t *testing.T) {
			if got := ghCloseReason(reason); got != want {
				t.Errorf("ghCloseReason(%q) = %q, want %q", reason, got, want)
			}
		})
	}
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.items[1].ticket.CloseReason = core.ClosedNotPlanned
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	ticket, err := adapter.Ticket(t.Context(), "#11")
	if err != nil {
		t.Fatalf("Ticket(#11): %v", err)
	}
	if !ticket.Closed || ticket.CloseReason != core.ClosedNotPlanned || ticket.ClosedAt.IsZero() {
		t.Errorf("the closed ticket reads as %+v", ticket)
	}
}

// An open ticket has no close reason, whatever the provider reports: a reason
// on an open issue is GitHub's own bookkeeping from the last time it was
// closed, and reading it would make a reopened ticket look abandoned.
func TestGitHubProjectsAnOpenTicketHasNoCloseReason(t *testing.T) {
	issue := ghIssue{Number: 1, State: "OPEN", StateReason: ghReasonNotPlanned}
	if ticket := ghTicket(issue, ghItemFields{}, "acme/widget"); ticket.CloseReason != "" {
		t.Errorf("CloseReason = %q on an open ticket", ticket.CloseReason)
	}
}

// Tickets by key says what is there. A key nobody holds is left out rather
// than failing the whole query: a caller asking about several tickets is
// asking which of them exist.
func TestGitHubProjectsSkipsAKeyNobodyHolds(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	tickets, err := adapter.Tickets(t.Context(), core.TicketQuery{Keys: []string{"#10", "#4040", "#12"}})
	if err != nil {
		t.Fatalf("Tickets by key: %v", err)
	}
	if len(tickets) != 2 || tickets[0].Key != "#10" || tickets[1].Key != "#12" {
		t.Errorf("Tickets by key = %v, want the two that are there, in the order asked", tickets)
	}
}

// A limit caps what comes back, and the newest survive it: a caller asking for
// the last five tickets wants the last five.
func TestGitHubProjectsLimitsTheNewest(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	tickets, err := adapter.Tickets(t.Context(), core.TicketQuery{Limit: 2})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(tickets) != 2 {
		t.Fatalf("got %d tickets, want 2", len(tickets))
	}
	if tickets[0].UpdatedAt.Before(tickets[1].UpdatedAt) {
		t.Errorf("the tickets are oldest first: %s then %s", tickets[0].UpdatedAt, tickets[1].UpdatedAt)
	}
	if tickets[0].Key != "#10" {
		t.Errorf("the newest ticket is %s, want #10", tickets[0].Key)
	}
}

// Before setup has resolved a board there is no board to read, and saying
// which setting is missing is more use than a query about project zero.
func TestGitHubProjectsCannotReadTicketsWithNoBoard(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 0 })
	if _, err := adapter.Tickets(t.Context(), core.TicketQuery{}); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Tickets = %v, want it to wrap ErrValidation", err)
	}
	if err := adapter.Move(t.Context(), "#10", coretest.ColumnDone); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Move = %v, want it to wrap ErrValidation", err)
	}
	if fake.asked() != 0 {
		t.Errorf("the adapter sent %d queries about a board it has no number for", fake.asked())
	}
}

// A column the board has not got is the configuration's to fix, and the
// refusal says which columns it does have: the alternative is a mutation
// GitHub refuses with an opaque id nobody can act on.
func TestGitHubProjectsRefusesAColumnTheBoardHasNot(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	err := adapter.Move(t.Context(), "#10", coretest.ColumnReady)
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Move = %v, want it to wrap ErrValidation", err)
	}
	if !strings.Contains(fixOf(t, err), "In progress") {
		t.Errorf("the refusal does not list the columns the board has: %q", fixOf(t, err))
	}
}

// A move that would change nothing sends nothing. Spec 06 §4 counts a write as
// a change that happened, and an item GitHub touched on every doctor run would
// make every run report work it did not do.
func TestGitHubProjectsMoveThatChangesNothingWritesNothing(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	before := fake.asked()
	if err := adapter.Move(t.Context(), "#10", coretest.ColumnInProgress); err != nil {
		t.Fatalf("Move: %v", err)
	}
	for _, query := range fake.sent()[before:] {
		if strings.HasPrefix(query, "mutation") {
			t.Fatalf("a move to the column the ticket is already in sent %q", query)
		}
	}
	if got := fake.itemIn(10); got != string(coretest.ColumnInProgress) {
		t.Errorf("the item is in %q after a move that should have done nothing", got)
	}
}

// Adding an issue to a board is a different act from moving it — the board's
// own automations run on it — and a transition is not where that is decided.
func TestGitHubProjectsWillNotMoveAnIssueOntoTheBoard(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.items = append(fake.items, &ghFakeItem{
		id: "", number: 77, updatedAt: coretest.ContractNow,
		ticket: core.Ticket{ID: "I_77", Title: "Not on the board", UpdatedAt: coretest.ContractNow},
	})
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	ticket, err := adapter.Ticket(t.Context(), "#77")
	if err != nil {
		t.Fatalf("Ticket(#77): %v", err)
	}
	if ticket.ColumnID != "" || ticket.Status.ID != "" {
		t.Errorf("an issue on no board is in column %q", ticket.ColumnID)
	}
	err = adapter.Move(t.Context(), "#77", coretest.ColumnDone)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Move = %v, want it to wrap ErrNotFound", err)
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "add the issue") {
		t.Errorf("the refusal does not say what is missing: %q", fix)
	}
}

// A board with no Status field has no columns to move between, and says so
// rather than refusing with an id nobody can look up.
func TestGitHubProjectsCannotMoveOnABoardWithNoStatusField(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	delete(fake.options, 1)
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	err := adapter.Move(t.Context(), "#10", coretest.ColumnDone)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Move = %v, want it to wrap ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), ghStatusFieldName) {
		t.Errorf("the refusal does not name the field the board is missing: %v", err)
	}
}

// A move names the column, and a move with no column at all is the caller's
// mistake rather than a write of the empty string.
func TestGitHubProjectsRefusesAMoveToNowhere(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	if err := adapter.Move(t.Context(), "#10", ""); !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Move = %v, want it to wrap ErrValidation", err)
	}
	if fake.asked() != 0 {
		t.Errorf("a move to nowhere sent %d queries", fake.asked())
	}
}

// Editing a board's columns (spec 06 §4, spec 11 §2, item 3.10).
//
// GitHub has no mutation that adds one option to a single select: the option
// set is written whole. flow therefore sends every option it is not changing
// back exactly as it stands — with its own id, which is what lets the
// provider keep the items pointing at it, and with its own colour and
// description, which the input makes mandatory and a rewrite would otherwise
// reset.
func TestGitHubProjectsAddsAColumnWithoutDisturbingTheOthers(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	before := fake.statusOptions()
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "Blocked"},
	})
	if err != nil {
		t.Fatalf("Apply(create): %v", err)
	}
	if result.FollowUp != "" {
		t.Fatalf("the adapter retreated from a board of %d items: %s", len(fake.items), result.FollowUp)
	}
	if result.Item.ID == "" || result.Item.Name != "Blocked" {
		t.Errorf("the created column is %+v, want the name it was given and an id the lock can record", result.Item)
	}
	if result.Note == "" {
		t.Error("the result explains nothing, so the log cannot say what happened")
	}

	after := fake.statusOptions()
	if len(after) != len(before)+1 {
		t.Fatalf("the board has %d columns, want the %d it had and one more", len(after), len(before))
	}
	for i, option := range before {
		if after[i] != option {
			t.Errorf("column %d is %+v, want the %+v it was: a rewrite sends every option it is not changing back as it stands", i, after[i], option)
		}
	}
	added := after[len(after)-1]
	if added.Name != "Blocked" || added.Color != ghDefaultOptionColour || added.Description != "" {
		t.Errorf("the added column is %+v, want a grey %q with no description", added, "Blocked")
	}
	if added.ID != result.Item.ID {
		t.Errorf("the result says the column is %q, the board says %q", result.Item.ID, added.ID)
	}
}

// The provider keeping the item values is the ordinary case, and flow must
// not write over them anyway: putting 500 cards back where they already are
// would touch every one of them every time somebody added a column.
func TestGitHubProjectsLeavesItemsAloneWhenTheRewriteSparedThem(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "Blocked"},
	})
	if err != nil {
		t.Fatalf("Apply(create): %v", err)
	}
	if moves := fake.mutations(ghUpdateItemFieldOp); moves != 0 {
		t.Errorf("flow moved %d items that had not moved", moves)
	}
	if !strings.Contains(result.Note, "survived") {
		t.Errorf("the note is %q, want it to say the item columns survived", result.Note)
	}
	for _, number := range []int{10, 11, 12, 13, 14} {
		if fake.columnOf(number) == "" {
			t.Errorf("issue #%d lost its column", number)
		}
	}
}

// And when the provider does clear them — which is what it does to any option
// it is not handed back by id, and what every option of an older server's
// board gets — every value goes back by name.
func TestGitHubProjectsPutsEveryClearedItemBackByName(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.wipesIDs = true
	was := map[int]string{}
	for _, number := range []int{10, 11, 12, 13, 14} {
		was[number] = fake.columnOf(number)
	}
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "Blocked"},
	})
	if err != nil {
		t.Fatalf("Apply(create): %v", err)
	}
	for number, column := range was {
		if now := fake.columnOf(number); now != column {
			t.Errorf("issue #%d is in %q, want the %q it was in before the rewrite", number, now, column)
		}
	}
	// Four of the five items were in a column of the Status field; the
	// fifth is in one the board has moved on from, which no rewrite of
	// the options touches.
	if !strings.Contains(result.Note, "4 of 5") {
		t.Errorf("the note is %q, want it to say 4 of 5 item columns were put back", result.Note)
	}
}

// A rename carries the items over: the column an item was in is the one now
// called what that one was called.
func TestGitHubProjectsRenamesAColumnAndTakesItsItemsWithIt(t *testing.T) {
	for _, wipes := range []bool{false, true} {
		name := "the provider kept the options"
		if wipes {
			name = "the provider recreated the options"
		}
		t.Run(name, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.wipesIDs = wipes
			adapter := newGitHubProjectsAdapter(t, srv, nil)

			result, err := adapter.Apply(t.Context(), core.Change{
				What: core.ItemTrackerStatusOptions,
				Op:   core.ChangeUpdate,
				Item: core.Item{ID: string(coretest.ColumnInProgress), Name: "Doing"},
			})
			if err != nil {
				t.Fatalf("Apply(update): %v", err)
			}
			if result.Item.Name != "Doing" {
				t.Errorf("the renamed column is %+v, want it called Doing", result.Item)
			}
			if column := fake.columnOf(10); column != "Doing" {
				t.Errorf("issue #10 is in %q, want the renamed column it was in", column)
			}
			if slices.ContainsFunc(fake.statusOptions(), func(o ghStatusOption) bool { return o.Name == "In progress" }) {
				t.Error("the board still has a column called In progress")
			}
		})
	}
}

// Removing a column clears every item in it, and nothing flow does afterwards
// would put them back: there is no column left to put them in. So it refuses,
// before it writes anything.
func TestGitHubProjectsRefusesToRemoveAColumnSomebodyIsUsing(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	_, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeDelete,
		Item: core.Item{ID: string(coretest.ColumnTodo), Name: "Todo"},
	})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("removing a column in use = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "2 items") {
		t.Errorf("the refusal is %q, want it to say how many items are in the way", err)
	}
	if rewrites := fake.mutations(ghUpdateFieldOp); rewrites != 0 {
		t.Errorf("flow rewrote the option set %d times while refusing to", rewrites)
	}
	if len(fake.statusOptions()) != 4 {
		t.Errorf("the board has %d columns, want the 4 it started with", len(fake.statusOptions()))
	}
}

// An empty one goes, and the result carries the id it had so that the lock
// can stop recording it as flow's.
func TestGitHubProjectsRemovesAnEmptyColumn(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeDelete,
		Item: core.Item{ID: string(coretest.ColumnInReview), Name: "In review"},
	})
	if err != nil {
		t.Fatalf("Apply(delete): %v", err)
	}
	if result.Item.ID != string(coretest.ColumnInReview) {
		t.Errorf("the removed column is %+v, want the id the lock recorded", result.Item)
	}
	for _, option := range fake.statusOptions() {
		if option.Name == "In review" {
			t.Error("the column survived being removed")
		}
	}
}

// Above the limit spec 06 §4 sets, the rewrite is a human's: flow puts back
// what the rewrite clears, and it will not clear what it cannot put back.
func TestGitHubProjectsWillNotRewriteTheColumnsOfABoardItCannotPutBack(t *testing.T) {
	cases := []struct {
		items    int
		retreats bool
	}{
		{items: ghOptionEditLimit, retreats: false},
		{items: ghOptionEditLimit + 1, retreats: true},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.items)+" items", func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			fake.totalItems = c.items
			adapter := newGitHubProjectsAdapter(t, srv, nil)

			result, err := adapter.Apply(t.Context(), core.Change{
				What: core.ItemTrackerStatusOptions,
				Op:   core.ChangeCreate,
				Item: core.Item{Name: "Blocked"},
			})
			if err != nil {
				t.Fatalf("Apply(create): %v", err)
			}
			switch {
			case c.retreats && result.FollowUp == "":
				t.Fatal("flow rewrote the columns of a board it cannot put back")
			case !c.retreats && result.FollowUp != "":
				t.Fatalf("flow retreated from a board of %d items: %s", c.items, result.FollowUp)
			}
			rewrites := fake.mutations(ghUpdateFieldOp)
			if c.retreats {
				if rewrites != 0 {
					t.Errorf("flow sent %d rewrites while retreating from one", rewrites)
				}
				if !strings.Contains(result.FollowUp, strconv.Itoa(c.items)) {
					t.Errorf("the retreat is %q, want it to say how big the board is", result.FollowUp)
				}
				return
			}
			if rewrites != 1 {
				t.Errorf("flow sent %d rewrites, want one", rewrites)
			}
		})
	}
}

// The restore is read back, not assumed: a provider that accepts every move
// and performs none would otherwise leave a board full of cleared cards and
// an apply that said it was fine.
func TestGitHubProjectsSaysWhenTheItemsDidNotComeBack(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.wipesIDs = true
	fake.deafToMoves = true
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	_, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "Blocked"},
	})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a restore that did not happen = %v, want ErrConflict", err)
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "person") {
		t.Errorf("the fix is %q, want it to send a person to the board", fix)
	}
}

// A change the board has moved on from is news rather than a no-op: the
// planner proposes against what it observed, and the tracker contract's own
// answers say which kind of news it is.
func TestGitHubProjectsRefusesAColumnChangeTheBoardDisagreesWith(t *testing.T) {
	cases := map[string]struct {
		change core.Change
		want   error
	}{
		"adding one that is there": {
			change: core.Change{Op: core.ChangeCreate, Item: core.Item{Name: "Todo"}},
			want:   core.ErrConflict,
		},
		"adding one with no name": {
			change: core.Change{Op: core.ChangeCreate, Item: core.Item{}},
			want:   core.ErrValidation,
		},
		"renaming one that is gone": {
			change: core.Change{Op: core.ChangeUpdate, Item: core.Item{ID: "opt-nobody", Name: "Doing"}},
			want:   core.ErrNotFound,
		},
		"renaming one to a name in use": {
			change: core.Change{Op: core.ChangeUpdate, Item: core.Item{ID: string(coretest.ColumnTodo), Name: "Done"}},
			want:   core.ErrConflict,
		},
		"removing one that is gone": {
			change: core.Change{Op: core.ChangeDelete, Item: core.Item{Name: "Blocked"}},
			want:   core.ErrNotFound,
		},
		"a change that is not one": {
			change: core.Change{Op: "rearrange", Item: core.Item{Name: "Todo"}},
			want:   core.ErrValidation,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			adapter := newGitHubProjectsAdapter(t, srv, nil)
			c.change.What = core.ItemTrackerStatusOptions

			if _, err := adapter.Apply(t.Context(), c.change); !errors.Is(err, c.want) {
				t.Errorf("Apply = %v, want %v", err, c.want)
			}
			if rewrites := fake.mutations(ghUpdateFieldOp); rewrites != 0 {
				t.Errorf("flow rewrote the option set %d times while refusing to", rewrites)
			}
		})
	}
}

// A change asking for what the board already says writes nothing. A rewrite
// is the most dangerous thing flow does to a tracker, and doing one to no
// effect is the worst version of it.
func TestGitHubProjectsRewritesNothingForAColumnThatIsAlreadyRight(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions,
		Op:   core.ChangeUpdate,
		Item: core.Item{ID: string(coretest.ColumnTodo), Name: "Todo"},
	})
	if err != nil {
		t.Fatalf("Apply(update): %v", err)
	}
	if result.Item.ID != string(coretest.ColumnTodo) {
		t.Errorf("the result is %+v, want the column as it stands", result.Item)
	}
	if rewrites := fake.mutations(ghUpdateFieldOp); rewrites != 0 {
		t.Errorf("flow rewrote the option set %d times to change nothing", rewrites)
	}
}

// A board flow cannot see is not a board flow rewrites.
func TestGitHubProjectsWillNotEditTheColumnsOfABoardItCannotRead(t *testing.T) {
	t.Run("no board", func(t *testing.T) {
		_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
		adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 99 })
		_, err := adapter.Apply(t.Context(), core.Change{
			What: core.ItemTrackerStatusOptions,
			Op:   core.ChangeCreate,
			Item: core.Item{Name: "Blocked"},
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Apply against a board nobody has = %v, want ErrNotFound", err)
		}
	})
	t.Run("no status field", func(t *testing.T) {
		fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
		delete(fake.options, 1)
		adapter := newGitHubProjectsAdapter(t, srv, nil)
		_, err := adapter.Apply(t.Context(), core.Change{
			What: core.ItemTrackerStatusOptions,
			Op:   core.ChangeCreate,
			Item: core.Item{Name: "Blocked"},
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Apply to a board with no Status field = %v, want ErrNotFound", err)
		}
		if fix := fixOf(t, err); !strings.Contains(fix, ghStatusFieldName) {
			t.Errorf("the fix is %q, want it to name the field the board is missing", fix)
		}
	})
	t.Run("no board number", func(t *testing.T) {
		_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
		adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 0 })
		_, err := adapter.Apply(t.Context(), core.Change{
			What: core.ItemTrackerStatusOptions,
			Op:   core.ChangeCreate,
			Item: core.Item{Name: "Blocked"},
		})
		if !errors.Is(err, core.ErrValidation) {
			t.Errorf("Apply before setup resolved a board = %v, want ErrValidation", err)
		}
	})
}

// Labels are the repository's, not the board's, and they are the one thing
// this adapter reads over REST (spec 11 §2, item 3.11).

// TestGitHubProjectsReadsEveryLabelThroughThePages: the reader must follow
// the Link header. A repository with more labels than one page is ordinary,
// and an adapter that stopped at the first would propose creating every label
// it had not seen.
func TestGitHubProjectsReadsEveryLabelThroughThePages(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	observation, err := adapter.Observe(t.Context(), core.ItemTrackerLabels)
	if err != nil {
		t.Fatalf("Observe(labels): %v", err)
	}
	if !observation.Exists {
		t.Fatal("a repository always has labels, so the observation exists")
	}
	if len(observation.Items) != len(fake.labels) {
		t.Fatalf("read %d labels of %d: the pages were not followed", len(observation.Items), len(fake.labels))
	}
	blocked, ok := observation.Find("blocked")
	if !ok {
		t.Fatalf("the blocked label is missing: %+v", observation.Items)
	}
	if blocked.Field("color") != "b60205" {
		t.Errorf("the label's colour is %q, want the one the repository has", blocked.Field("color"))
	}
	if blocked.ID == "" {
		t.Error("the label has no id, so the lock cannot record one flow created")
	}
	// Canonical order, whatever order the provider answered in.
	if observation.Items[0].Name != "blocked" {
		t.Errorf("the observation is %v, want it canonicalised", observation.Items)
	}
}

// TestGitHubProjectsAddsALabelAndLeavesTheRestAlone covers the create case,
// including the colour a configuration never gives one.
func TestGitHubProjectsAddsALabelAndLeavesTheRestAlone(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	before := len(fake.labels)

	result, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "agent"},
	})
	if err != nil {
		t.Fatalf("Apply(create): %v", err)
	}
	if result.Item.ID == "" {
		t.Error("the created label has no id, so the lock cannot record it")
	}
	if result.Note == "" {
		t.Error("the result explains nothing")
	}
	if got := result.Item.Field("color"); got != ghDefaultLabelColour {
		t.Errorf("the new label's colour is %q, want the default %q: a configuration names labels and not their colours",
			got, ghDefaultLabelColour)
	}
	if len(fake.labels) != before+1 {
		t.Errorf("the repository has %d labels, want %d", len(fake.labels), before+1)
	}

	// A colour, when a change does carry one, is what is sent.
	coloured, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "incident", Fields: map[string]string{"color": "#D93F0B"}},
	})
	if err != nil {
		t.Fatalf("Apply(create with a colour): %v", err)
	}
	if got := coloured.Item.Field("color"); got != "d93f0b" {
		t.Errorf("the colour is %q, want it sent as GitHub takes it: six hex digits, no hash", got)
	}
}

// TestGitHubProjectsRefusesALabelThatIsAlreadyThere: a create that collides is
// ErrConflict, which is what tells apply to update instead (spec 06 §4). The
// name is matched the way GitHub matches it — labels are case-insensitive,
// so "Blocked" and "blocked" are one label and a second create would 422.
func TestGitHubProjectsRefusesALabelThatIsAlreadyThere(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	for _, name := range []string{"blocked", "BLOCKED"} {
		_, err := adapter.Apply(t.Context(), core.Change{
			What: core.ItemTrackerLabels,
			Op:   core.ChangeCreate,
			Item: core.Item{Name: name},
		})
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("creating %q = %v, want ErrConflict", name, err)
		}
	}
}

// TestGitHubProjectsEditsALabelInPlace covers update, including the rename
// GitHub only does through new_name — and finding the label by the id the
// plan copied off the observation, because the name is what is changing.
func TestGitHubProjectsEditsALabelInPlace(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	recoloured, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeUpdate,
		Item: core.Item{ID: "lbl-1", Name: "blocked", Fields: map[string]string{"color": "000000"}},
	})
	if err != nil {
		t.Fatalf("Apply(update): %v", err)
	}
	if recoloured.Item.Field("color") != "000000" {
		t.Errorf("the label is %+v, want the colour it was given", recoloured.Item)
	}

	renamed, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeUpdate,
		Item: core.Item{ID: "lbl-1", Name: "is-blocked"},
	})
	if err != nil {
		t.Fatalf("Apply(rename): %v", err)
	}
	if renamed.Item.Name != "is-blocked" {
		t.Errorf("the label is called %q, want the new name", renamed.Item.Name)
	}
	if !strings.Contains(renamed.Note, "blocked") {
		t.Errorf("the note is %q, want it to name what was renamed", renamed.Note)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if slices.ContainsFunc(fake.labels, func(l ghLabel) bool { return l.Name == "blocked" }) {
		t.Errorf("a rename left the old label behind: %+v", fake.labels)
	}
}

// TestGitHubProjectsEditsOnlyWhatTheLabelChangeNames: flow has no opinion
// about a label's colour or description (engine desires neither), so an
// update that carries neither must leave both alone. A rename that repainted
// a label the team chose the colour of would be a change nobody asked for.
func TestGitHubProjectsEditsOnlyWhatTheLabelChangeNames(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	fake.mu.Lock()
	at := slices.IndexFunc(fake.labels, func(l ghLabel) bool { return l.Name == "blocked" })
	if at < 0 {
		fake.mu.Unlock()
		t.Fatal("the fixture has no blocked label to edit")
	}
	fake.labels[at].Description = "somebody is waiting on somebody else"
	was := fake.labels[at]
	fake.mu.Unlock()

	if _, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeUpdate,
		Item: core.Item{ID: was.NodeID, Name: "is-blocked"},
	}); err != nil {
		t.Fatalf("Apply(rename): %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	now := fake.labels[slices.IndexFunc(fake.labels, func(l ghLabel) bool { return l.NodeID == was.NodeID })]
	if now.Name != "is-blocked" {
		t.Errorf("the label is called %q, want the new name", now.Name)
	}
	if now.Color != was.Color {
		t.Errorf("the rename repainted the label %q -> %q", was.Color, now.Color)
	}
	if now.Description != was.Description {
		t.Errorf("the rename rewrote the description %q -> %q", was.Description, now.Description)
	}
}

// TestGitHubProjectsRemovesALabelAndSaysSoWhenItCannot covers delete, and the
// refusal a second apply of the same plan must get.
func TestGitHubProjectsRemovesALabelAndSaysSoWhenItCannot(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	change := core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeDelete,
		Item: core.Item{ID: "lbl-1", Name: "blocked"},
	}
	if _, err := adapter.Apply(t.Context(), change); err != nil {
		t.Fatalf("Apply(delete): %v", err)
	}
	fake.mu.Lock()
	gone := !slices.ContainsFunc(fake.labels, func(l ghLabel) bool { return l.Name == "blocked" })
	fake.mu.Unlock()
	if !gone {
		t.Error("the label is still there")
	}
	if _, err := adapter.Apply(t.Context(), change); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleting it again = %v, want ErrNotFound", err)
	}
}

// TestGitHubProjectsWillNotTouchLabelsWithoutARepository: a project can hold
// issues from many repositories, so there is no one repository to infer. The
// refusal names the setting rather than failing against an empty path.
func TestGitHubProjectsWillNotTouchLabelsWithoutARepository(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Repo = "" })

	_, err := adapter.Observe(t.Context(), core.ItemTrackerLabels)
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("Observe(labels) with no repository = %v, want ErrValidation", err)
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "repo") {
		t.Errorf("the fix is %q, want it to name the setting", fix)
	}
	if _, err := adapter.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: "agent"},
	}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Apply(labels) with no repository = %v, want ErrValidation", err)
	}
}

// Follow-ups, comments and an issue's state (spec 07 §4, item 3.11). The
// contract proves these are idempotent against an index that answers at once;
// what follows is the part of idempotence a real GitHub makes harder.

// TestGitHubProjectsDoesNotFileTwiceWhileTheIndexLags: GitHub's search index
// is seconds to minutes behind its writes, so a doctor run that filed a
// follow-up and then asked whether one existed would file a second one. The
// blind fake is that window made total.
func TestGitHubProjectsDoesNotFileTwiceWhileTheIndexLags(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.blind = true
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	follow := core.FollowUp{
		Marker: core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget"),
		Title:  "flow: add an expedite view (acme/widget)",
		Body:   "Why, and how flow will check.",
	}

	first, err := adapter.CreateFollowUp(t.Context(), follow)
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	again, err := adapter.CreateFollowUp(t.Context(), follow)
	if err != nil {
		t.Fatalf("CreateFollowUp again: %v", err)
	}
	if again.Key != first.Key {
		t.Errorf("filing it again opened %s as well as %s: the index had not caught up", again.Key, first.Key)
	}
	if filed := fake.mutations("createIssue"); filed != 1 {
		t.Errorf("the fake filed %d issues, want 1", filed)
	}

	// A search naming fewer fields finds it too, from memory as from the
	// index (spec 11 §1).
	found, err := adapter.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite"))
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil || found.Key != first.Key {
		t.Errorf("a broader search found %v, want %s", found, first.Key)
	}
	// And a marker nothing carries is still nothing.
	other, err := adapter.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip"))
	if err != nil {
		t.Fatalf("FindFollowUp for another check: %v", err)
	}
	if other != nil {
		t.Errorf("the memory of one follow-up answered for another: %s", other.Key)
	}
}

// TestGitHubProjectsPutsAFollowUpOnTheBoard: filing is the one time flow adds
// an issue to a project. A follow-up nobody can see on the board is a
// follow-up nobody will do.
func TestGitHubProjectsPutsAFollowUpOnTheBoard(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	filed, err := adapter.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget"),
		Title:  "flow: add an expedite view (acme/widget)",
		Body:   "Why, and how flow will check.",
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	number, err := strconv.Atoi(strings.TrimPrefix(filed.Key, "#"))
	if err != nil {
		t.Fatalf("the filed ticket's key is %q, which is not an issue key", filed.Key)
	}
	if !fake.onBoard(number) {
		t.Error("the follow-up was filed but not put on the board")
	}
	if !core.HasMarker(filed.Body, core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget")) {
		t.Errorf("the filed ticket's body carries no marker, so nothing will ever find it again: %q", filed.Body)
	}

	// A board flow has no number for yet is setup before a board exists,
	// and the follow-up is still filed.
	bare, bareSrv := newGitHubProjectsServer(t, coretest.ContractTracker())
	unnumbered := newGitHubProjectsAdapter(t, bareSrv, func(o *GitHubProjectsOptions) { o.Number = 0 })
	if _, err := unnumbered.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: core.NewMarker(core.MarkerFollowUp, "check", "tracker.project", "repo", "acme/widget"),
		Title:  "flow: make a board (acme/widget)",
	}); err != nil {
		t.Fatalf("CreateFollowUp without a board: %v", err)
	}
	if added := bare.mutations("addProjectV2ItemById"); added != 0 {
		t.Errorf("flow tried %d times to add an issue to a board it has no number for", added)
	}
}

// TestGitHubProjectsFilesAFollowUpTheRepositoryIsNotReadyFor: a label the
// repository has not got and a login nobody has are both left off rather than
// refused. The labels are how people find flow's follow-ups, not what makes
// one true, and the item that creates them is a change apply may not have
// reached (spec 06 §3); an unassigned ticket somebody can see beats no ticket.
func TestGitHubProjectsFilesAFollowUpTheRepositoryIsNotReadyFor(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	filed, err := adapter.CreateFollowUp(t.Context(), core.FollowUp{
		Marker:   core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget"),
		Title:    "flow: add an expedite view (acme/widget)",
		Labels:   []string{"flow-followup", "no-such-label"},
		Assignee: "nobody-at-all",
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	if !slices.Equal(filed.Labels, []string{"flow-followup"}) {
		t.Errorf("the ticket carries %v, want only the label the repository has", filed.Labels)
	}
	if len(filed.Assignees) != 0 {
		t.Errorf("the ticket is assigned to %v, want nobody: the login does not exist", filed.Assignees)
	}

	// A login GitHub does know is assigned, so the tolerance above is not
	// the adapter quietly never assigning anybody.
	assigned, err := adapter.CreateFollowUp(t.Context(), core.FollowUp{
		Marker:   core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/other"),
		Title:    "flow: set a WIP limit (acme/other)",
		Assignee: "@maintainer",
	})
	if err != nil {
		t.Fatalf("CreateFollowUp with an assignee: %v", err)
	}
	if !slices.Contains(assigned.Assignees, "maintainer") {
		t.Errorf("the ticket is assigned to %v, want the maintainer", assigned.Assignees)
	}
	if fake.mutations("createIssue") != 2 {
		t.Errorf("the fake filed %d issues, want 2", fake.mutations("createIssue"))
	}
}

// TestGitHubProjectsBelievesTheBodyAndNotTheIndex: the search only narrows.
// GitHub decides which issues are worth looking at and flow decides which
// carry the marker, so an index that answers with too much is harmless.
func TestGitHubProjectsBelievesTheBodyAndNotTheIndex(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.loose = true
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	found, err := adapter.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "nothing.at.all"))
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found != nil {
		t.Errorf("FindFollowUp believed the index and answered %s, whose body carries no such marker", found.Key)
	}
	// The one that does carry it is still found among the same noise.
	marked, err := adapter.FindFollowUp(t.Context(), coretest.ContractFollowUpMarker())
	if err != nil {
		t.Fatalf("FindFollowUp for the marked ticket: %v", err)
	}
	if marked == nil || marked.Key != "#13" {
		t.Errorf("FindFollowUp = %v, want #13", marked)
	}
}

// TestGitHubProjectsRefusesAFollowUpNothingCouldFind: a marker is the only
// thing that identifies a follow-up, so filing one without a marker would be
// filing a duplicate every day for ever.
func TestGitHubProjectsRefusesAFollowUpNothingCouldFind(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	if _, err := adapter.CreateFollowUp(t.Context(), core.FollowUp{Title: "flow: something"}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("CreateFollowUp without a marker = %v, want ErrValidation", err)
	}
	if _, err := adapter.FindFollowUp(t.Context(), core.Marker{}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("FindFollowUp without a marker = %v, want ErrValidation", err)
	}
	if filed := fake.mutations("createIssue"); filed != 0 {
		t.Errorf("the fake filed %d issues for a follow-up it should have refused", filed)
	}

	// And without a repository there is nowhere to file one: a project can
	// hold issues from many, so there is nothing to infer from the board.
	_, boardOnly := newGitHubProjectsServer(t, coretest.ContractTracker())
	adrift := newGitHubProjectsAdapter(t, boardOnly, func(o *GitHubProjectsOptions) { o.Repo = "" })
	_, err := adrift.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: coretest.ContractFollowUpMarker(),
		Title:  "flow: something",
	})
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("CreateFollowUp without a repository = %v, want ErrValidation", err)
	}
	if fix := fixOf(t, err); !strings.Contains(fix, "repo") {
		t.Errorf("the refusal offers %q, which does not name the setting that is missing", fix)
	}
}

// TestGitHubProjectsEditsItsOwnCommentWhereverItIs: six weeks of weekly
// nudging is one comment that keeps changing, not six comments (spec 07 §4).
// Flow's comment is wherever it fell in a conversation people were already
// having, so finding it means reading every page and not just the first.
func TestGitHubProjectsEditsItsOwnCommentWhereverItIs(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")

	// People were talking about it before flow said anything.
	fake.comments[13] = append(fake.comments[13],
		&ghFakeComment{id: "IC_human_1", body: "is this ours?"},
		&ghFakeComment{id: "IC_human_2", body: "looks like it"},
	)
	if err := adapter.Comment(t.Context(), "#13", marker, "still not done"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	// And they carry on, so flow's comment is in the middle of the pages.
	fake.comments[13] = append(fake.comments[13],
		&ghFakeComment{id: "IC_human_3", body: "on it"},
		&ghFakeComment{id: "IC_human_4", body: "still on it"},
	)
	if err := adapter.Comment(t.Context(), "#13", marker, "still not done, a week on"); err != nil {
		t.Fatalf("Comment again: %v", err)
	}

	bodies := fake.commentBodies(13)
	if len(bodies) != 5 {
		t.Fatalf("the ticket has %d comments, want 5: flow added one instead of editing its own\n%v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[2], "a week on") {
		t.Errorf("flow's comment says %q, want the second nudge", bodies[2])
	}
	if !core.HasMarker(bodies[2], marker) {
		t.Errorf("flow's edited comment lost its marker, so the next nudge will add a second: %q", bodies[2])
	}

	// A comment whose marker is somebody else's check is not flow's to
	// edit: a second marker means a second comment.
	other := core.NewMarker(core.MarkerFollowUp, "check", "tracker.expedite", "repo", "acme/widget")
	if err := adapter.Comment(t.Context(), "#13", other, "and this one too"); err != nil {
		t.Fatalf("Comment with another marker: %v", err)
	}
	if got := len(fake.commentBodies(13)); got != 6 {
		t.Errorf("the ticket has %d comments, want 6: a different marker is a different comment", got)
	}
}

// TestGitHubProjectsRecordsWhyItClosedATicket: GitHub distinguishes work that
// was done from work that was not, and closing a follow-up flow could not
// verify as completed would be a lie recorded in the tracker.
func TestGitHubProjectsRecordsWhyItClosedATicket(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{core.ClosedCompleted, core.ClosedCompleted},
		{core.ClosedNotPlanned, core.ClosedNotPlanned},
		{"", core.ClosedNotPlanned},
		{"because I said so", core.ClosedNotPlanned},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
			adapter := newGitHubProjectsAdapter(t, srv, nil)
			if err := adapter.Close(t.Context(), "#13", tc.reason); err != nil {
				t.Fatalf("Close: %v", err)
			}
			closed, err := adapter.Ticket(t.Context(), "#13")
			if err != nil {
				t.Fatalf("Ticket: %v", err)
			}
			if closed.CloseReason != tc.want {
				t.Errorf("closing for %q recorded %q, want %q", tc.reason, closed.CloseReason, tc.want)
			}
		})
	}
}

// TestGitHubProjectsLeavesAnOpenTicketAloneWhenReopeningIt: doctor runs daily
// and reopens what it cannot verify. Reopening a ticket that is already open
// would be a mutation a day against every such ticket.
func TestGitHubProjectsLeavesAnOpenTicketAloneWhenReopeningIt(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	if err := adapter.Reopen(t.Context(), "#13", "flow could not verify it was done"); err != nil {
		t.Fatalf("Reopen an open ticket: %v", err)
	}
	if got := fake.mutations("reopenIssue"); got != 0 {
		t.Errorf("reopening an open ticket sent %d mutations, want none", got)
	}
	if err := adapter.Close(t.Context(), "#11", core.ClosedCompleted); err != nil {
		t.Fatalf("Close a closed ticket: %v", err)
	}
	if got := fake.mutations("closeIssue"); got != 0 {
		t.Errorf("closing a closed ticket sent %d mutations, want none", got)
	}
}

// TestGitHubProjectsSearchesForEveryFieldSeparately: a marker renders its
// fields in alphabetical order, so a search naming fewer of them is not a
// substring of a ticket filed with more — "check=… repo=…" against a marker
// carrying "assignee=… check=… repo=…". One phrase per field is what makes a
// broader search narrow to a superset rather than to nothing (spec 11 §1).
func TestGitHubProjectsSearchesForEveryFieldSeparately(t *testing.T) {
	full := core.NewMarker(core.MarkerFollowUp, "assignee", "ann", "check", "tracker.wip", "repo", "acme/widget")
	expression := ghSearchExpression("acme/widget", full)
	if !strings.Contains(expression, "repo:acme/widget") || !strings.Contains(expression, "is:issue") {
		t.Errorf("the search is %q, which does not narrow to this repository's issues", expression)
	}
	phrases := ghQuotedPhrases(expression)
	want := []string{core.MarkerPrefix + core.MarkerFollowUp, "assignee=ann", "check=tracker.wip", "repo=acme/widget"}
	if !slices.Equal(phrases, want) {
		t.Errorf("the search asks for %q, want %q", phrases, want)
	}

	// Every phrase a broader search asks for is one the fuller marker's
	// body carries, which is what makes the narrowing safe.
	body := core.MarkBody("why", full, true)
	broad := ghQuotedPhrases(ghSearchExpression("acme/widget", core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip")))
	for _, phrase := range broad {
		if !strings.Contains(body, phrase) {
			t.Errorf("a broader search asks for %q, which the ticket it should find does not carry:\n%s", phrase, body)
		}
	}
}

// A branch somebody has deleted leaves the link behind: GitHub answers the
// node with a null ref. The work it stood for is over, and a branch flow
// cannot name is not one it can report or put in a sentence.
func TestGitHubProjectsDropsALinkedBranchNobodyCanReachAnyMore(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	started := fake.item(t, coretest.ContractBranchedKey)
	started.goneBranches = []string{"14-a-branch-long-gone"}
	want := []string{coretest.ContractBranch}
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	ticket, err := adapter.Ticket(t.Context(), coretest.ContractBranchedKey)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}

	if !slices.Equal(ticket.Branches, want) {
		t.Errorf("Branches = %v, want %v: a link whose ref is null is a branch that is gone", ticket.Branches, want)
	}
}

// The same fidelity [TestTheProjectsFakeAnswersOnlyWhatTheQueryAsked] buys for
// an item, for the issue behind it: a fake that answers a field the document
// never selected would let flow drop it from the query with every test green.
func TestTheProjectsFakeAnswersOnlyTheIssueFieldsTheQueryAsked(t *testing.T) {
	fake, _ := newGitHubProjectsServer(t, coretest.ContractTracker())
	asked := []string{"number", "title", "body", "labels", "assignees", "linkedBranches"}

	for _, document := range []string{ghItemsQuery(OwnerUser), ghIssueItemQuery} {
		doc := fake.issueJSON(document, fake.items[0])
		for _, field := range asked {
			if _, ok := doc[field]; !ok {
				t.Errorf("the fake left %s out of its answer to a query that selected it", field)
			}
		}
	}

	unasked := fake.issueJSON(strings.Replace(ghItemsQuery(OwnerUser), "linkedBranches(first:"+ghBranchPage+"){nodes{ref{name}}} ", "", 1), fake.items[0])
	if _, ok := unasked["linkedBranches"]; ok {
		t.Error("the fake volunteered linkedBranches to a query that did not select it, so no test could tell whether flow asks for it")
	}
}

// A promise about a workflow nobody switched on is one the board cannot keep,
// and GraphQL will say so: the connection answers with the workflows the
// project has configured, so a name that is missing is a name nobody turned
// on. Flow still cannot see which column the workflow moves a card to, which
// is why the note refutes the attestation rather than replacing it.
func TestGitHubProjectsSaysWhichWorkflowIsNotSwitchedOn(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	observation, err := adapter.Observe(t.Context(), core.NativeItem("changes_requested"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Notes) != 1 {
		t.Fatalf("Observe notes = %+v, want one", observation.Notes)
	}
	note := observation.Notes[0]
	if !note.Refutes {
		t.Errorf("note = %+v, want one that refutes the item", note)
	}
	for _, want := range []string{"Code changes requested", "not switched on"} {
		if !strings.Contains(note.Text, want) {
			t.Errorf("note text %q does not mention %q", note.Text, want)
		}
	}
	if !strings.Contains(note.Fix, "Code changes requested") || note.URL == "" {
		t.Errorf("note = %+v, want a fix naming the workflow and somewhere to do it", note)
	}
}

// The two GitHub switches on for a new project are the two this board has,
// and a promise the board is keeping is left for a person to vouch for in the
// ordinary way: no note, nothing refuted.
func TestGitHubProjectsSaysNothingAboutAWorkflowThatIsOn(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	for _, event := range []string{"pr_merged", "issue_closed"} {
		observation, err := adapter.Observe(t.Context(), core.NativeItem(event))
		if err != nil {
			t.Fatalf("Observe(%s): %v", event, err)
		}
		if len(observation.Notes) != 0 {
			t.Errorf("Observe(%s) notes = %+v, want none", event, observation.Notes)
		}
	}
}

// An event GitHub has no built-in for has nothing to refute, and flow does
// not pay a query to find that out: the declaration already answers it with
// remediation none, and a board this configuration never relied on should
// not be read to say so.
func TestGitHubProjectsDoesNotLookForAWorkflowGitHubHasNot(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	for _, event := range []string{"pr_opened_draft", "pr_ready_for_review", "deployed"} {
		observation, err := adapter.Observe(t.Context(), core.NativeItem(event))
		if err != nil {
			t.Fatalf("Observe(%s): %v", event, err)
		}
		if len(observation.Notes) != 0 || observation.Exists {
			t.Errorf("Observe(%s) = %+v, want nothing", event, observation)
		}
	}
	if fake.asked() != 0 {
		t.Errorf("the adapter asked %d questions about workflows GitHub does not have", fake.asked())
	}
}

// A workflow the panel lists and nobody switched on is the same answer as one
// the panel does not list at all. GitHub reports the first as enabled false
// and leaves the second out, and flow must not read the first as a promise
// being kept.
func TestGitHubProjectsReadsAWorkflowListedAndOffAsOff(t *testing.T) {
	fake, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	fake.workflows["Pull request merged"] = false
	adapter := newGitHubProjectsAdapter(t, srv, nil)

	observation, err := adapter.Observe(t.Context(), core.NativeItem("pr_merged"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Notes) != 1 || !observation.Notes[0].Refutes {
		t.Errorf("Observe notes = %+v, want one that refutes the item", observation.Notes)
	}
}

// A board nobody has switched nothing on, and saying so would be flow
// reporting a workflow of a project that is not there. The board's own check
// is where a missing board is a finding; every other check about it waits on
// that one, and a second complaint underneath it is noise.
func TestGitHubProjectsSaysNothingAboutTheWorkflowsOfABoardNobodyHas(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.Number = 404 })

	observation, err := adapter.Observe(t.Context(), core.NativeItem("changes_requested"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observation.Notes) != 0 {
		t.Errorf("Observe notes = %+v, want nothing said about a board nobody has", observation.Notes)
	}
}

// The same fidelity [TestTheProjectsFakeAnswersOnlyWhatTheQueryAsked] buys for
// an item, for the board itself: a fake that volunteers the workflows
// connection would answer a query that had stopped selecting it, and flow
// would read every board as fully automated with the suite green.
func TestTheProjectsFakeAnswersOnlyTheBoardFieldsTheQueryAsked(t *testing.T) {
	fake, _ := newGitHubProjectsServer(t, coretest.ContractTracker())
	variables := map[string]any{"number": float64(1)}

	asked, ok := fake.project(ghProjectQuery(OwnerUser), variables).(map[string]any)
	if !ok {
		t.Fatalf("the fake answered %T for a board it has", asked)
	}
	if _, selected := asked["workflows"]; !selected {
		t.Error("the fake left workflows out of its answer to a query that selected it")
	}

	unasked, ok := fake.project("query{projectV2(number:$number){id number}}", variables).(map[string]any)
	if !ok {
		t.Fatalf("the fake answered %T for a board it has", unasked)
	}
	if _, selected := unasked["workflows"]; selected {
		t.Error("the fake volunteered workflows to a query that did not select it, so no test could tell whether flow asks for them")
	}
}
