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

// The part of the tracker contract plan item 3.8 lands: naming itself,
// probing, discovery, references and observation. Items, moves and follow-ups
// are 3.9 to 3.11, and the suite is run whole when they have landed (3.12).
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

// Everything 3.9 to 3.11 will land refuses meanwhile, naming what it wanted
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
		"Ticket": func() error { _, err := adapter.Ticket(t.Context(), "#1"); return err }(),
		"Tickets": func() error {
			_, err := adapter.Tickets(t.Context(), core.TicketQuery{})
			return err
		}(),
		"Move":           adapter.Move(t.Context(), "#1", "col"),
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
