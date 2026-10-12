package tracker

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
	return func(t *testing.T, fx coretest.TrackerFixture) core.Tracker {
		t.Helper()
		fake, srv := newGitHubProjectsServer(t, fx)
		fake.ownerType = ownerType
		return newGitHubProjectsAdapter(t, srv, func(o *GitHubProjectsOptions) { o.OwnerType = ownerType })
	}
}

// The part of the tracker contract plan items 3.8 and 3.9 land: naming itself,
// probing, discovery, references, observation, tickets and moves. Follow-ups,
// comments and closing are 3.10 and 3.11, and the suite is run whole when they
// have landed (3.12).
//
// Both owner kinds sit it. A person's projects and an organisation's are
// reached through different fields of GitHub's schema and shown at different
// addresses, so a board under one owner proves nothing about the other.
func TestGitHubProjectsMeetsTheContractItHasLanded(t *testing.T) {
	for _, ownerType := range []string{OwnerUser, OwnerOrg} {
		t.Run(ownerType, func(t *testing.T) {
			coretest.TrackerContractSubset(t, contractGitHubProjects(ownerType),
				"identity",
				"probe reports what the credential may do",
				"what is discovered can be parsed back",
				"a hint narrows discovery",
				"nonsense is not a reference",
				"an observation is canonical and stable",
				"observing something that is not there is not an error",
				"tickets by key, by column, and all of them",
				"a ticket that is not there",
				"a move is idempotent",
				"linkage uses the forge's own link",
			)
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
	for _, what := range []string{core.ItemTrackerWIP, core.ItemTrackerExpedite} {
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

// Everything 3.10 and 3.11 will land refuses meanwhile, naming what it wanted
// rather than failing blankly — the only stub spec 01 permits, and only until
// its plan item lands.
func TestGitHubProjectsRefusesWhatItsPlanItemsHaveNotLanded(t *testing.T) {
	_, srv := newGitHubProjectsServer(t, coretest.ContractTracker())
	adapter := newGitHubProjectsAdapter(t, srv, nil)
	cases := map[string]error{
		"Observe(labels)": func() error {
			_, err := adapter.Observe(t.Context(), core.ItemTrackerLabels)
			return err
		}(),
		"Apply": func() error {
			_, err := adapter.Apply(t.Context(), core.Change{What: core.ItemTrackerStatusOptions})
			return err
		}(),
		"CreateFollowUp": func() error { _, err := adapter.CreateFollowUp(t.Context(), core.FollowUp{}); return err }(),
		"FindFollowUp": func() error {
			_, err := adapter.FindFollowUp(t.Context(), core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget"))
			return err
		}(),
		"Comment": adapter.Comment(t.Context(), "#1", core.Marker{}, "hello"),
		"Close":   adapter.Close(t.Context(), "#1", "done"),
		"Reopen":  adapter.Reopen(t.Context(), "#1", "not done"),
	}
	for name, err := range cases {
		if !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("%s = %v, want it to wrap ErrUnsupported", name, err)
		}
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

// ptr is the address of a value, for the fields a test sets to "this, and not
// nothing".
func ptr[T any](v T) *T { return &v }

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
	want := len(fake.items)
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
