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

// jiraCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func jiraCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(JiraCloudKind)
	if err != nil {
		t.Fatalf("Of(jira-cloud): %v", err)
	}
	return set
}

// newJiraClient is the HTTP client the adapter talks through.
func newJiraClient(t *testing.T, srv *httptest.Server, adjust func(*core.ClientOptions)) *core.Client {
	t.Helper()
	opts := core.ClientOptions{
		BaseURL:    srv.URL + JiraAPIPath,
		Token:      "ZmxvdzpzZWNyZXQ=",
		Version:    "v1.0.0",
		AuthScheme: "Basic",
		Clock:      coretest.NewClock(coretest.ContractNow),
		Backoff:    func(int, core.RateLimit) time.Duration { return time.Millisecond },
	}
	if adjust != nil {
		adjust(&opts)
	}
	client, err := core.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// newJiraAdapter builds an adapter talking to srv.
func newJiraAdapter(t *testing.T, srv *httptest.Server, adjust func(*JiraCloudOptions)) *JiraCloud {
	t.Helper()
	client := newJiraClient(t, srv, nil)
	opts := JiraCloudOptions{
		Client:       client,
		Site:         srv.URL,
		Project:      jiraFixtureProject,
		Capabilities: jiraCapabilities(t),
	}
	if adjust != nil {
		adjust(&opts)
	}
	adapter, err := NewJiraCloud(opts)
	if err != nil {
		t.Fatalf("NewJiraCloud: %v", err)
	}
	return adapter
}

// jiraWorld stands a fake Jira up in front of the contract's fixture and
// returns both halves, for the tests that are about this adapter rather than
// about the contract.
func jiraWorld(t *testing.T) (*jiraFake, *JiraCloud) {
	t.Helper()
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	return fake, newJiraAdapter(t, srv, nil)
}

// The whole tracker contract passes against Jira's own shapes. The suite is
// run whole rather than as a named subset, so a case added to the contract
// later runs here without anybody remembering to list it.
func TestJiraCloudMeetsTheTrackerContract(t *testing.T) {
	coretest.TrackerContract(t, func(t *testing.T, fx *coretest.TrackerFixture) core.Tracker {
		t.Helper()
		_, srv := newJiraServer(t, fx)
		return newJiraAdapter(t, srv, nil)
	})
}

// What Jira says when it refuses reaches the person who ran flow. Jira writes
// a refusal the other way round from GitHub — a list under errorMessages,
// and "errors" holding a map of field to message rather than a list of
// objects — so a reader that knows only one shape reads neither. The adapter
// owns no second error path for it: core.Client already wrote the error, and
// teaching it both shapes is what makes a Jira refusal legible.
func TestJiraRefusalsArriveInJirasOwnWords(t *testing.T) {
	_, jira := jiraWorld(t)

	_, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"4040")
	if err == nil {
		t.Fatal("a key the project does not hold came back as a ticket")
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("err = %v, want something a person can read", err)
	}
	if !strings.Contains(user.Why, "Issue does not exist") {
		t.Errorf("the refusal explains itself as %q, want Jira's own words", user.Why)
	}
}

// An archived issue is the one thing two readings of the same Jira disagree
// about, and Jira publishes no flag for it anywhere: it answers by key in
// full and is absent from every search. So the adapter derives it, and the
// listing it derives it from has to be the whole project — absence from a
// narrowed one means the filter.
func TestJiraReadsArchivingOutOfTheTwoReadingsDisagreeing(t *testing.T) {
	fake, jira := jiraWorld(t)

	archived, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"15")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !archived.Archived {
		t.Error("the issue answered by key and is in no search, which is the whole of what archived means here")
	}
	live, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"10")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if live.Archived {
		t.Error("an issue a search returns is not archived")
	}
	// A Jira key is case-insensitive to ask with and canonical to read
	// back, so the same issue asked for in another spelling is the same
	// issue: what the two readings are compared by is the key Jira
	// answered with, never the one flow was handed.
	spelled, err := jira.Ticket(t.Context(), strings.ToLower(jiraFixtureKeyPrefix+"10"))
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if spelled.Archived {
		t.Errorf("%s is archived and %s is not: the key was compared as it was spelled", spelled.Key, live.Key)
	}
	// The listing is read once a run: archiving is not something that
	// happens while flow is looking, and a search for every ticket read
	// would double the cost of an audit that walks a fortnight of them.
	if searches := fake.asked("POST search/jql"); searches > 3 {
		t.Errorf("the adapter searched %d times for two tickets", searches)
	}
}

// A blind run must not read as a quiet project: if the listing that tells an
// archived issue from a live one cannot be had, the read fails rather than
// answering "not archived", which is what every count downstream would take
// as a fact.
func TestJiraWillNotGuessWhetherAnIssueIsArchived(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, nil)
	fake.mu.Lock()
	fake.key = "OTHER"
	fake.mu.Unlock()

	if _, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"10"); err == nil {
		t.Fatal("the project could not be listed and the ticket came back anyway")
	}
}

// A search is asked for the fields it needs and answered with those alone, so
// a listing that forgot to ask for a description would come back with none.
// The follow-up hunt is the one that cannot survive it: a marker is in the
// body and nowhere else.
func TestJiraAsksASearchForTheFieldsItWillRead(t *testing.T) {
	_, jira := jiraWorld(t)
	found, err := jira.FindFollowUp(t.Context(), coretest.ContractFollowUpMarker())
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil {
		t.Fatal("the fixture's follow-up was not found, and its marker is in its description")
	}
	if !core.HasMarker(found.Body, coretest.ContractFollowUpMarker()) {
		t.Errorf("the ticket came back without the marker that identified it: %q", found.Body)
	}
	if found.Title == "" || found.Status.Name == "" {
		t.Errorf("the follow-up came back without the fields a report shows: %+v", *found)
	}
}

// Jira's search pages by an opaque token, and a token is not a number flow
// may invent. The fixture is larger than a page on purpose: an adapter that
// read the first page and stopped answers with two of the five.
func TestJiraWalksEveryPageOfASearch(t *testing.T) {
	fake, jira := jiraWorld(t)
	tickets, err := jira.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(tickets) <= fake.pageSize {
		t.Fatalf("got %d tickets, and a page holds %d: the adapter stopped at the first page", len(tickets), fake.pageSize)
	}
}

// A Jira timestamp is ISO 8601 and is not RFC 3339: the offset has no colon
// in it, and the standard library refuses it. A tracker whose dates were all
// zero would make every window empty and every staleness check silent.
func TestJiraReadsItsOwnTimestamps(t *testing.T) {
	_, jira := jiraWorld(t)
	ticket, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"10")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if ticket.UpdatedAt.IsZero() {
		t.Fatal("the ticket has no updated date")
	}
	for _, raw := range []string{
		`"2026-10-14T19:09:44.000+1100"`,
		`"2026-10-14T19:09:44.000+11:00"`,
		`"2026-10-14T08:09:44-0000"`,
	} {
		var when jiraTime
		if err := when.UnmarshalJSON([]byte(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		} else if when.IsZero() {
			t.Errorf("%s read as no time at all", raw)
		}
	}
	var absent jiraTime
	if err := absent.UnmarshalJSON([]byte("null")); err != nil || !absent.IsZero() {
		t.Errorf("null read as %v, %v: an issue that was never resolved has no resolution date", absent, err)
	}
	if err := absent.UnmarshalJSON([]byte(`"yesterday"`)); err == nil {
		t.Error("anything at all was read as a date")
	}
}

// A move is a workflow transition, so a column the workflow cannot reach from
// here is a conflict and not a validation failure: the configuration is
// right and the workflow is what has to change.
func TestJiraSaysWhenTheWorkflowWillNotGoThere(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, nil)

	fake.mu.Lock()
	// A workflow that offers nothing from where this issue is, which is
	// the ordinary shape of a Jira: a column exists and the work cannot
	// get to it from here.
	fake.frozenWorkflow = true
	fake.mu.Unlock()

	err := jira.Move(t.Context(), jiraFixtureKeyPrefix+"10", coretest.ColumnTodo)
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Move = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "transition") {
		t.Errorf("the error says %q, which does not tell anybody it is the workflow in the way", err)
	}
}

// Closing is a transition and a resolution, and the resolution may only be
// set where the transition's screen takes one. Setting a field a transition
// does not offer is a refusal flow could not explain, so it asks first.
func TestJiraSetsAResolutionOnlyWhereTheScreenTakesOne(t *testing.T) {
	for _, screened := range []bool{true, false} {
		t.Run(map[bool]string{true: "with the field", false: "without it"}[screened], func(t *testing.T) {
			fx := coretest.ContractTracker()
			fake, srv := newJiraServer(t, &fx)
			jira := newJiraAdapter(t, srv, nil)
			fake.mu.Lock()
			fake.noResolutionField = !screened
			fake.mu.Unlock()

			if err := jira.Close(t.Context(), jiraFixtureKeyPrefix+"13", core.ClosedNotPlanned); err != nil {
				t.Fatalf("Close: %v", err)
			}
			closed, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"13")
			if err != nil {
				t.Fatalf("Ticket: %v", err)
			}
			if !closed.Closed {
				t.Fatal("the work is still open after being closed")
			}
			switch {
			case screened && closed.CloseReason != core.ClosedNotPlanned:
				t.Errorf("close reason = %q, want the one the resolution records", closed.CloseReason)
			case !screened && closed.CloseReason != core.ClosedCompleted:
				t.Errorf("close reason = %q: with no resolution on the screen, a status in the done category is all there is", closed.CloseReason)
			}
		})
	}
}

// Flow names several resolutions it would accept and takes the first the
// instance holds, because the order is a preference: an abandoned piece of
// work is better recorded as "Won't Do" than as "Duplicate", and a scheme
// holding both must not decide that by whichever it happens to list first.
func TestJiraTakesTheResolutionItWouldRatherHave(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, nil)
	fake.mu.Lock()
	fake.resolutions = []jiraResolution{
		{ID: "10002", Name: "Duplicate"},
		{ID: "10001", Name: "Won't Do"},
	}
	fake.mu.Unlock()

	if err := jira.Close(t.Context(), jiraFixtureKeyPrefix+"13", core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fake.mu.Lock()
	recorded := fake.find(jiraFixtureKeyPrefix + "13").fields.Resolution
	fake.mu.Unlock()
	if recorded == nil || recorded.Name != "Won't Do" {
		t.Errorf("the issue resolved as %+v, want the first name flow asked for", recorded)
	}
}

// A resolution scheme is the team's, and a credential that may not read it is
// not a reason to leave the work open: the transition is what closes it.
func TestJiraClosesWorkWithoutReadingTheResolutionScheme(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, nil)
	fake.mu.Lock()
	fake.refuseResolutions = true
	fake.mu.Unlock()

	if err := jira.Close(t.Context(), jiraFixtureKeyPrefix+"13", core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"13")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !closed.Closed {
		t.Error("the work is still open")
	}
}

// What an issue may do next changes with every move it makes, and flow keeps
// the tags Jira answers with between runs. A move decided on a reading older
// than the issue would ask for a transition the workflow no longer offers, so
// the store is revalidated rather than believed.
func TestJiraAsksWhatAnIssueMayDoNextBeforeEachMove(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, func(o *JiraCloudOptions) {
		o.Client = newJiraClient(t, srv, func(c *core.ClientOptions) { c.Cache = core.NewMemoryCache() })
	})

	key := jiraFixtureKeyPrefix + "10"
	start, err := jira.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	// The first move fills the store with the transitions offered from
	// where the issue was — a list with no way back to where it is now,
	// because a workflow offers no transition to the status an issue is
	// already in.
	if err := jira.Move(t.Context(), key, core.ColumnID("In review")); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if err := jira.Move(t.Context(), key, core.ColumnID(start.Status.Name)); err != nil {
		t.Fatalf("moving %s back to %q: %v", key, start.Status.Name, err)
	}
	back, err := jira.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if back.Status.Name != start.Status.Name {
		t.Errorf("%s is in %q, want %q", key, back.Status.Name, start.Status.Name)
	}
	// The store earned its keep: something flow already had was answered
	// as something flow already had, rather than sent again.
	fake.mu.Lock()
	revalidated := fake.notModified
	fake.mu.Unlock()
	if revalidated == 0 {
		t.Error("nothing was revalidated, so the run never asked a question it had an answer to")
	}
}

// A resolution scheme is the team's, and a team spells its own names: an
// instance whose Done is DONE records the same thing by it, and flow matching
// what it was looking for letter by letter would close the work with no
// resolution at all.
func TestJiraMatchesAResolutionHoweverTheTeamSpelledIt(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, nil)
	fake.mu.Lock()
	fake.resolutions = []jiraResolution{{ID: "10001", Name: "WON'T DO"}}
	fake.mu.Unlock()

	if err := jira.Close(t.Context(), jiraFixtureKeyPrefix+"13", core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := jira.Ticket(t.Context(), jiraFixtureKeyPrefix+"13")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if closed.CloseReason != core.ClosedNotPlanned {
		t.Errorf("close reason = %q: the scheme holds the resolution flow asked for, spelled its own way", closed.CloseReason)
	}
}

// Jira is not the forge's tracker: what a pull request says it closes is an
// issue of the repository and never a Jira key. The link is the key in the
// branch, which is Jira's own convention — and flow never applies a pattern
// somebody configured (P12).
func TestJiraLinksByItsOwnConventionAndNotByTheForgesLink(t *testing.T) {
	_, jira := jiraWorld(t)
	pr := coretest.ContractForge().PullRequests[1]
	pr.ClosingIssues = []string{"#11"}
	pr.HeadRef = "main"
	pr.Title = "fix: the last event is lost"
	if keys := jira.LinkedKeys(pr); len(keys) != 0 {
		t.Errorf("LinkedKeys = %v, and the forge was talking about its own issues", keys)
	}

	pr.HeadRef = "fix/" + jiraFixtureKeyPrefix + "11-the-last-event"
	if keys := jira.LinkedKeys(pr); !slices.Equal(keys, []string{jiraFixtureKeyPrefix + "11"}) {
		t.Errorf("LinkedKeys = %v, want the key in the branch", keys)
	}

	pr.HeadRef = "main"
	pr.Title = jiraFixtureKeyPrefix + "11 fix: the last event is lost"
	if keys := jira.LinkedKeys(pr); !slices.Equal(keys, []string{jiraFixtureKeyPrefix + "11"}) {
		t.Errorf("LinkedKeys = %v, want the key in the title", keys)
	}

	// A forge composes the header a squash lands, and a team whose
	// template names the branch puts the key somewhere neither the title
	// nor the ref flow was handed has it.
	pr.Title = "fix: the last event is lost"
	pr.SquashHeader = "fix: the last event is lost (" + jiraFixtureKeyPrefix + "11)"
	if keys := jira.LinkedKeys(pr); !slices.Equal(keys, []string{jiraFixtureKeyPrefix + "11"}) {
		t.Errorf("LinkedKeys = %v, want the key in the header a squash will land", keys)
	}
	pr.SquashHeader = ""

	pr.Title = "fix: something near flow-11a and OTHER-3"
	if keys := jira.LinkedKeys(pr); len(keys) != 0 {
		t.Errorf("LinkedKeys = %v, want none: neither is a key of this project", keys)
	}
}

// A project's statuses come from its workflows, and a project with two issue
// types sharing a workflow lists the same status twice. Two columns of the
// same name would make every plan propose deleting one of them.
func TestJiraReportsEachColumnOnce(t *testing.T) {
	_, jira := jiraWorld(t)
	observation, err := jira.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range observation.Items {
		if seen[item.ID] {
			t.Errorf("the column %q is in the observation twice", item.Name)
		}
		seen[item.ID] = true
	}
	review, ok := observation.Find("In review")
	if !ok {
		t.Fatalf("the board's columns are %+v, and In review is not among them", observation.Items)
	}
	if review.Field("category") != categoryInProgress {
		t.Errorf("In review is in the %q category, want %q", review.Field("category"), categoryInProgress)
	}
}

// What cannot be read is said to be unreadable rather than answered with an
// empty list: a label registry Jira has not got would otherwise read as a
// board whose labels have all been deleted, which is drift flow would propose
// to fix for ever (spec 05 §1).
func TestJiraSaysNothingAboutWhatItCannotSee(t *testing.T) {
	_, jira := jiraWorld(t)
	for _, what := range []string{core.ItemTrackerLabels, core.ItemTrackerWIP, core.ItemTrackerExpedite} {
		observation, err := jira.Observe(t.Context(), what)
		if err != nil {
			t.Fatalf("Observe(%s): %v", what, err)
		}
		if observation.Exists || len(observation.Items) > 0 {
			t.Errorf("Observe(%s) = %+v, want nothing at all", what, observation)
		}
	}
	declaration := jiraCapabilities(t)
	for _, capability := range []string{core.CapLabels, core.CapWIPLimits, core.CapExpediteView} {
		if declaration.CanVerify(capability) {
			t.Errorf("the declaration says %s can be verified, and the adapter never looks", capability)
		}
	}
	if _, err := jira.Observe(t.Context(), "tracker.sprint"); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Observe of something no tracker has = %v, want ErrValidation", err)
	}
}

// Every address Jira writes for a project carries the key, and the one
// somebody pastes is as likely to be an issue's as the project's.
func TestJiraReadsTheAddressesItsOwnPagesHave(t *testing.T) {
	_, jira := jiraWorld(t)
	for _, raw := range []string{
		"https://acme.atlassian.net/browse/FLOW",
		"https://acme.atlassian.net/browse/FLOW-12",
		"https://acme.atlassian.net/projects/FLOW/issues",
		"https://acme.atlassian.net/jira/software/projects/FLOW/boards/1",
	} {
		ref, err := jira.ParseRef(raw)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", raw, err)
			continue
		}
		if ref.ID != "FLOW" {
			t.Errorf("ParseRef(%q).ID = %q, want FLOW", raw, ref.ID)
		}
		if ref.What != core.ItemTrackerProject {
			t.Errorf("ParseRef(%q).What = %q", raw, ref.What)
		}
	}
	for _, raw := range []string{
		"https://acme.atlassian.net/",
		"https://acme.atlassian.net/jira/software/projects",
		"the delivery board",
	} {
		if _, err := jira.ParseRef(raw); !errors.Is(err, core.ErrValidation) {
			t.Errorf("ParseRef(%q) = %v, want ErrValidation", raw, err)
		}
	}
}

// Before setup has chosen a project there is nothing to ask about, and the
// answer has to say which question is outstanding rather than failing against
// the network.
func TestJiraSaysWhenNobodyHasChosenAProject(t *testing.T) {
	fx := coretest.ContractTracker()
	_, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, func(o *JiraCloudOptions) { o.Project = "" })

	if _, err := jira.Tickets(t.Context(), core.TicketQuery{}); !errors.Is(err, core.ErrNeedsInput) {
		t.Errorf("Tickets with no project = %v, want ErrNeedsInput", err)
	}
	if _, err := jira.FindFollowUp(t.Context(), coretest.ContractFollowUpMarker()); !errors.Is(err, core.ErrNeedsInput) {
		t.Errorf("FindFollowUp with no project = %v, want ErrNeedsInput", err)
	}
	observation, err := jira.Observe(t.Context(), core.ItemTrackerProject)
	if err != nil {
		t.Fatalf("Observe(tracker.project): %v", err)
	}
	if observation.Exists {
		t.Error("a project nobody has chosen is one that does not exist yet, which is what discovery is for")
	}
	// Discovery is the whole point of being here, and it does not need a
	// project to work.
	candidates, err := jira.Discover(t.Context(), core.ItemTrackerProject, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) < 2 {
		t.Errorf("got %d candidates with no project configured", len(candidates))
	}
}

// Discovery pages, and the project somebody is looking for is as likely to be
// on the second page as the first.
func TestJiraOffersEveryProjectItCanSee(t *testing.T) {
	fake, jira := jiraWorld(t)
	candidates, err := jira.Discover(t.Context(), core.ItemTrackerProject, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(candidates) <= fake.pageSize {
		t.Fatalf("got %d candidates, and a page holds %d", len(candidates), fake.pageSize)
	}
	if !slices.ContainsFunc(candidates, func(c core.Candidate) bool { return c.ID == jiraFixtureProject }) {
		t.Errorf("the project flow is configured for is not among %+v", candidates)
	}
}

// A probe reports what the credential may do in the vocabulary the
// declaration is written in, because a probe and a declaration that do not
// share one can never agree.
func TestJiraProbesInTheVocabularyTheDeclarationUses(t *testing.T) {
	_, jira := jiraWorld(t)
	permissions, err := jira.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, want := range []string{jiraTokenCreateIssues, jiraTokenTransitionIssues} {
		if !slices.Contains(permissions.Tokens, want) {
			t.Errorf("Probe reported %v, want %q among them", permissions.Tokens, want)
		}
	}
	if slices.Contains(permissions.Tokens, jiraTokenProjectAdmin) {
		t.Error("the fixture's credential does not administer the project, and the probe says it does")
	}
	declaration := jiraCapabilities(t)
	for _, capability := range []string{core.CapFollowUpTickets, core.CapTicketMove} {
		for _, required := range declaration.Capabilities[capability].Requires {
			if !slices.Contains(permissions.Tokens, required) {
				t.Errorf("%s requires %q, which no probe of a credential that holds everything reports", capability, required)
			}
		}
	}
}

// Nothing of the board is flow's to write here, and the refusal has to be the
// one the engine reads as "somebody else does this" rather than a failure.
func TestJiraRefusesToConfigureSomebodyElsesBoard(t *testing.T) {
	_, jira := jiraWorld(t)
	for _, change := range []core.Change{
		{What: core.ItemTrackerStatusOptions, Op: core.ChangeCreate, Item: core.Item{Name: "Ready"}},
		{What: core.ItemTrackerLabels, Op: core.ChangeUpdate, Item: core.Item{Name: "blocked"}},
	} {
		if _, err := jira.Apply(t.Context(), change); !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("Apply(%s %s) = %v, want ErrUnsupported", change.Op, change.What, err)
		}
	}
	if _, err := jira.Apply(t.Context(), core.Change{What: "tracker.sprint", Op: core.ChangeCreate}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Apply of something no tracker has = %v, want ErrValidation", err)
	}
}

// A description is a document, and flow's is text it expects back unchanged:
// a follow-up that already says the right thing must not be rewritten, which
// is what keeps the pinned health report one issue rather than one a morning.
func TestJiraKeepsAFollowUpsTextThroughTheDocument(t *testing.T) {
	_, jira := jiraWorld(t)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "forge.files", "repo", "acme/widget")
	body := "## What to do\n\n- the first thing\n- the second thing\n\nThen it is done.\n"
	filed, err := jira.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: marker, Title: "flow: do the thing", Body: body,
		Labels: []string{"flow-followup"}, Assignee: "dev@acme.test",
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	if want := core.MarkBody(body, marker, true); filed.Body != want {
		t.Errorf("the description came back as\n%q\nwant\n%q", filed.Body, want)
	}
	if !slices.Contains(filed.Labels, "flow-followup") {
		t.Errorf("the follow-up carries %v, want the label people find it by", filed.Labels)
	}
	if !slices.Contains(filed.Assignees, "dev") {
		t.Errorf("the follow-up is assigned to %v, want the person configuration named", filed.Assignees)
	}
	// Filing it again finds it rather than writing a second one.
	again, err := jira.CreateFollowUp(t.Context(), core.FollowUp{Marker: marker, Title: "flow: do the thing", Body: body})
	if err != nil {
		t.Fatalf("CreateFollowUp again: %v", err)
	}
	if again.Key != filed.Key {
		t.Errorf("filing it again filed %s beside %s", again.Key, filed.Key)
	}
}

// A label with a space in it is no label to Jira, and a filing it refused
// would be a follow-up nobody ever sees. The label is how people find flow's
// tickets, not what makes one true.
func TestJiraFilesAFollowUpTheLabelsCannotStop(t *testing.T) {
	_, jira := jiraWorld(t)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.labels", "repo", "acme/widget")
	filed, err := jira.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: marker, Title: "flow: label the work", Body: "it needs a label",
		Labels: []string{"needs triage", "flow-followup"},
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	if slices.Contains(filed.Labels, "needs triage") {
		t.Error("a label with a space in it was sent to Jira, which refuses one")
	}
	if !slices.Contains(filed.Labels, "flow-followup") {
		t.Errorf("the labels that could be applied were not: %v", filed.Labels)
	}
}

// jiraSeedFollowUp puts an issue carrying a marker into the fake, as a
// follow-up filed by an earlier run would be.
func jiraSeedFollowUp(fake *jiraFake, title string, marker core.Marker, labels ...string) string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.tick()
	fake.nextID++
	body := adfFromText(core.MarkBody("filed by an earlier run", marker, true))
	issue := &jiraFakeIssue{
		id:  strconv.Itoa(fake.nextID),
		key: fake.key + "-" + strconv.Itoa(fake.nextID),
		fields: jiraIssueFields{
			Summary: title, Description: &body, Labels: labels,
			Status:  fake.statusAt(0),
			Updated: jiraTime{Time: fake.now}, Created: jiraTime{Time: fake.now},
		},
	}
	fake.issues = append(fake.issues, issue)
	return issue.key
}

// The follow-up search is narrowed by the follow-up label, so a large
// project is not read whole on every run (issue #296). The marker still
// decides: issues with the label and another marker are passed over, and
// there are more of them than fit on a page, so an adapter that read only
// the first page would miss the target.
func TestJiraNarrowsTheFollowUpSearchByTheLabel(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, func(o *JiraCloudOptions) {
		o.FollowUpLabels = []string{"needs triage", "flow-followup"}
	})
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.issue296", "repo", "acme/widget")
	other := core.NewMarker(core.MarkerFollowUp, "check", "tracker.labels", "repo", "acme/widget")
	// The search reads newest first, so the target is filed first and
	// read last, on the third page.
	want := jiraSeedFollowUp(fake, "flow: limit the work", marker, "flow-followup")
	for range 5 {
		jiraSeedFollowUp(fake, "another check's follow-up", other, "flow-followup")
	}
	// Created last, so newest first it is read before the target: the
	// match has to be the label's, not the order's.
	jiraSeedFollowUp(fake, "somebody pasted the marker", marker)

	found, err := jira.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil || found.Key != want {
		t.Fatalf("FindFollowUp = %+v, want %s", found, want)
	}
	fake.mu.Lock()
	jqls := slices.Clone(fake.jqls)
	fake.mu.Unlock()
	if len(jqls) == 0 || !strings.Contains(jqls[0], `labels = "flow-followup"`) {
		t.Errorf("the search was not narrowed by the usable label: %q", jqls)
	}
}

// An issue carrying the marker and not the label is not flow's: with a
// label configured nothing outside it is read, and a fresh labelled
// follow-up is filed rather than the whole project walked (issue #296).
func TestJiraDoesNotFindAnUnlabelledFollowUp(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, func(o *JiraCloudOptions) {
		o.FollowUpLabels = []string{"flow-followup"}
	})
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.issue296", "repo", "acme/widget")
	jiraSeedFollowUp(fake, "the label was taken off", marker)

	found, err := jira.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found != nil {
		t.Errorf("FindFollowUp matched %s, which does not carry the follow-up label", found.Key)
	}
}

// Without a label Jira would take, the search reads the whole project as it
// always has: narrowing by a label the filing leaves off finds nothing.
func TestJiraReadsTheProjectWithoutAUsableLabel(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newJiraServer(t, &fx)
	jira := newJiraAdapter(t, srv, func(o *JiraCloudOptions) {
		o.FollowUpLabels = []string{"needs triage"}
	})
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.issue296", "repo", "acme/widget")
	want := jiraSeedFollowUp(fake, "filed before labels were configured", marker)

	found, err := jira.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil || found.Key != want {
		t.Errorf("FindFollowUp = %+v, want %s", found, want)
	}
}
