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

// linearCapabilities is the declaration flow ships, read from the tree rather
// than embedded: internal packages do not import assets (spec 01 §2).
func linearCapabilities(t *testing.T) core.CapabilitySet {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of(LinearKind)
	if err != nil {
		t.Fatalf("Of(linear): %v", err)
	}
	return set
}

// newLinearClient is the HTTP client the adapter talks through.
func newLinearClient(t *testing.T, srv *httptest.Server, adjust func(*core.ClientOptions)) *core.Client {
	t.Helper()
	opts := core.ClientOptions{
		BaseURL:    srv.URL,
		Token:      lnFakeToken,
		Version:    "v1.0.0",
		AuthScheme: core.AuthSchemeNone,
		Header:     LinearHeader(),
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

// newLinearAdapter builds an adapter talking to srv.
func newLinearAdapter(t *testing.T, srv *httptest.Server, adjust func(*LinearOptions)) *Linear {
	t.Helper()
	opts := LinearOptions{
		Client:       newLinearClient(t, srv, nil),
		Team:         lnFakeTeam,
		Capabilities: linearCapabilities(t),
	}
	if adjust != nil {
		adjust(&opts)
	}
	adapter, err := NewLinear(opts)
	if err != nil {
		t.Fatalf("NewLinear: %v", err)
	}
	return adapter
}

// linearWorld stands a fake Linear up in front of the contract's fixture and
// returns both halves, for the tests that are about this adapter rather than
// about the contract.
func linearWorld(t *testing.T) (*lnFake, *Linear) {
	t.Helper()
	fx := coretest.ContractTracker()
	fake, srv := newLinearServer(t, &fx)
	return fake, newLinearAdapter(t, srv, nil)
}

// The whole tracker contract passes against Linear's own shapes. The suite is
// run whole rather than as a named subset, so a case added to the contract
// later runs here without anybody remembering to list it.
func TestLinearMeetsTheTrackerContract(t *testing.T) {
	coretest.TrackerContract(t, func(t *testing.T, fx *coretest.TrackerFixture) core.Tracker {
		t.Helper()
		_, srv := newLinearServer(t, fx)
		return newLinearAdapter(t, srv, nil)
	})
}

// A Linear personal API key is the whole Authorization header. Every other
// provider flow speaks to takes a scheme in front of the credential, so the
// one that does not is the one a default quietly breaks: the fake fails any
// call whose header is not the key on its own.
func TestLinearSendsTheCredentialWithNoSchemeInFrontOfIt(t *testing.T) {
	_, linear := linearWorld(t)

	if _, err := linear.Probe(t.Context()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

// An OAuth access token is the other kind of credential, and Linear wants
// that one as a Bearer token. Which kind it is, is in the credential itself.
func TestLinearNamesTheSchemeFromTheCredentialItWasGiven(t *testing.T) {
	personal := "lin_" + "api_" + "secret"
	if got := LinearAuthorization(personal); got != personal {
		t.Errorf("LinearAuthorization(a personal key) = %q, want the key on its own", got)
	}
	oauth := linearOAuthPrefix + "secret"
	if got := LinearAuthorization(oauth); got != "Bearer "+oauth {
		t.Errorf("LinearAuthorization(an OAuth token) = %q, want it sent as a Bearer token", got)
	}
}

// What Linear says when it refuses reaches the person who ran flow. Linear
// puts the name of the failure under `extensions`, where GitHub puts it
// beside the message, so a reader that knows only one shape reads neither.
func TestLinearRefusalsArriveInLinearsOwnWords(t *testing.T) {
	fake, linear := linearWorld(t)
	fake.refuses("FlowIssue", lnFakeRefusal{
		Message: "Entity not found: Issue - Could not find referenced Issue.",
		Code:    "ENTITY_NOT_FOUND",
		Type:    "invalid input",
	})

	_, err := linear.Ticket(t.Context(), lnFakeKeyPrefix+"4040")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Ticket of a missing issue = %v, want it to wrap ErrNotFound", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("err = %v, want something a person can read", err)
	}
	if !strings.Contains(user.Why, "Could not find referenced Issue") {
		t.Errorf("the refusal explains itself as %q, want Linear's own words", user.Why)
	}
}

// A code Linear puts in extensions is the precise name and the type beside it
// is the coarse one. Both are answered, because a refusal flow has no code
// for must not read as an outage worth retrying.
func TestLinearReadsTheCoarseNameOfAFailureWhenThereIsNoCode(t *testing.T) {
	fake, linear := linearWorld(t)
	fake.refuses("FlowIssue", lnFakeRefusal{Message: "You do not have access", Type: "authentication error"})

	_, err := linear.Ticket(t.Context(), lnFakeKeyPrefix+"10")
	if !errors.Is(err, core.ErrPermission) {
		t.Fatalf("Ticket with no code and an authentication type = %v, want ErrPermission", err)
	}
}

// Linear's own integration records a pull request as an attachment on the
// issue, and that is the first reading of what a pull request is for: a
// branch that names no identifier is still linked by what Linear recorded.
func TestLinearLinksAPullRequestByWhatLinearItselfRecorded(t *testing.T) {
	fake, linear := linearWorld(t)
	pr := coretest.ContractForge().PullRequests[1]
	pr.HeadRef = "dev/no-identifier-here"
	pr.Title = "Fix the last event"
	pr.ClosingIssues = nil
	fake.attach(lnFakeKeyPrefix+"11", pr.URL)

	if keys := linear.LinkedKeys(pr); len(keys) != 0 {
		t.Fatalf("LinkedKeys before flow has read anything = %v, want nothing: it has nothing to read", keys)
	}
	if _, err := linear.Tickets(t.Context(), core.TicketQuery{}); err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	keys := linear.LinkedKeys(pr)
	if !slices.Contains(keys, lnFakeKeyPrefix+"11") {
		t.Errorf("LinkedKeys = %v, want the issue Linear attached the pull request to", keys)
	}
}

// The fallback is the identifier Linear's own branch naming puts in front of
// the title, which arrives in lower case.
func TestLinearReadsItsOwnIdentifierOutOfABranchNameWhateverItsCase(t *testing.T) {
	_, linear := linearWorld(t)
	pr := coretest.ContractForge().PullRequests[1]
	pr.HeadRef = "dev/eng-11-the-last-event"
	pr.Title = "fix: the last event"
	pr.ClosingIssues = nil

	keys := linear.LinkedKeys(pr)
	if !slices.Contains(keys, lnFakeKeyPrefix+"11") {
		t.Errorf("LinkedKeys = %v, want %s11 read out of the branch", keys, lnFakeKeyPrefix)
	}
}

// A number that is not this team's is not this team's work, and neither is a
// word that merely looks like an identifier.
func TestLinearClaimsNoIdentifierOfAnotherTeam(t *testing.T) {
	_, linear := linearWorld(t)
	pr := coretest.ContractForge().PullRequests[1]
	pr.HeadRef = "dev/des-11-the-last-event"
	pr.Title = "fix: UTF-8 and ENG-0 and ENG-x"
	pr.ClosingIssues = nil

	if keys := linear.LinkedKeys(pr); len(keys) != 0 {
		t.Errorf("LinkedKeys = %v, want nothing: none of those is an %s issue", keys, lnFakeTeam)
	}
}

// A follow-up filed a month ago has been archived by Linear itself, which
// archives an issue a while after it is closed. The search that looks for it
// has to say so, or flow files the same ticket every month.
func TestLinearLooksForAFollowUpAmongTheArchivedToo(t *testing.T) {
	fake, linear := linearWorld(t)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.wip", "repo", "acme/widget")

	// The fixture's follow-up is #13; archive it the way Linear would.
	fake.mu.Lock()
	issue := fake.find(lnFakeKeyPrefix + "13")
	issue.issue.ArchivedAt = lnTime{Time: coretest.ContractNow.Add(-24 * time.Hour)}
	fake.mu.Unlock()

	found, err := linear.FindFollowUp(t.Context(), marker)
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	if found == nil {
		t.Fatal("FindFollowUp found nothing, so flow would file the ticket Linear had archived")
	}
	if !found.Archived {
		t.Errorf("the follow-up came back with Archived false, which is not what the board says")
	}
}

// An issue in the trash is gone, which is not the same as archived: an
// archived issue is still the ticket and a trashed one is not there at all.
func TestLinearReadsATrashedIssueAsGone(t *testing.T) {
	fake, linear := linearWorld(t)
	fake.trash(lnFakeKeyPrefix + "12")

	if _, err := linear.Ticket(t.Context(), lnFakeKeyPrefix+"12"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Ticket of a trashed issue = %v, want ErrNotFound", err)
	}
}

// Linear settles a workflow state's type when it is created and takes no type
// in an edit, so the one change flow cannot make is the one that would matter
// most: moving a column into another category. It says so rather than
// deleting the column and taking every issue in it off the board.
func TestLinearWillNotMoveAColumnIntoAnotherCategory(t *testing.T) {
	_, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	todo, ok := observed.Find("Todo")
	if !ok {
		t.Fatal("the board has no Todo column to try to move")
	}
	todo.Fields = map[string]string{lnFieldCategory: categoryDone}

	_, err = linear.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions, Op: core.ChangeUpdate, Item: todo,
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("recategorising a column = %v, want ErrUnsupported", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "by hand") {
		t.Errorf("the refusal says %+v, want it to say what a person does instead", err)
	}
}

// A rename is a change Linear does take, and the column keeps its id and its
// category through one.
func TestLinearRenamesAColumnWithoutMakingASecondOne(t *testing.T) {
	_, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	before, ok := observed.Find("Todo")
	if !ok {
		t.Fatal("the board has no Todo column to rename")
	}
	renamed := before
	renamed.Name = "Up next"

	result, err := linear.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions, Op: core.ChangeUpdate, Item: renamed,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Item.ID != before.ID {
		t.Errorf("the renamed column has id %q, want %q: a rename is not a new column", result.Item.ID, before.ID)
	}
	if got := result.Item.Field(lnFieldCategory); got != before.Field(lnFieldCategory) {
		t.Errorf("the renamed column is in category %q, want %q", got, before.Field(lnFieldCategory))
	}
	after, err := linear.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe again: %v", err)
	}
	if _, found := after.Find("Todo"); found {
		t.Error("the board still has a Todo column, so the rename made a second one")
	}
}

// Linear refuses to archive a workflow state that issues still stand in. That
// refusal is the board protecting itself, so flow passes it on as the retreat
// it is rather than failing the run (spec 06 §4).
func TestLinearPassesOnARefusalToArchiveAColumnThatStillHasWorkInIt(t *testing.T) {
	_, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	busy, ok := observed.Find("In progress")
	if !ok {
		t.Fatal("the fixture has no In progress column, which #10 stands in")
	}

	result, err := linear.Apply(t.Context(), core.Change{
		What: core.ItemTrackerStatusOptions, Op: core.ChangeDelete, Item: busy,
	})
	if err != nil {
		t.Fatalf("Apply = %v, want the refusal carried as a follow-up rather than as a failure", err)
	}
	if result.FollowUp == "" {
		t.Fatal("the result carries no follow-up, so nobody is ever told to move the work")
	}
	if !strings.Contains(result.FollowUp, "still has issues") {
		t.Errorf("the follow-up says %q, want Linear's own reason in it", result.FollowUp)
	}
}

// A label of another team is in the same workspace and is not this board's,
// and a label group is a heading rather than a label. Neither is offered, and
// neither is ever deleted as drift.
func TestLinearLeavesTheWorkspacesOtherLabelsAlone(t *testing.T) {
	_, linear := linearWorld(t)

	observed, err := linear.Observe(t.Context(), core.ItemTrackerLabels)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for _, unwanted := range []string{"design", "Priority"} {
		if _, found := observed.Find(unwanted); found {
			t.Errorf("the observation offers %q, which is not a label of this team", unwanted)
		}
	}
	if _, found := observed.Find("blocked"); !found {
		t.Error("the observation lost the team's own labels")
	}
}

// Work nobody will do is cancelled rather than completed, which is the
// difference Linear keeps and the one spec 07 §4 asks flow to record.
func TestLinearCancelsWorkThatWasDroppedAndCompletesTheRest(t *testing.T) {
	for _, c := range []struct {
		name   string
		key    string
		reason string
		want   string
	}{
		{name: "work that was done", key: lnFakeKeyPrefix + "10", reason: core.ClosedCompleted, want: core.ClosedCompleted},
		{name: "work that was dropped", key: lnFakeKeyPrefix + "12", reason: core.ClosedNotPlanned, want: core.ClosedNotPlanned},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake, linear := linearWorld(t)
			// A team with nowhere to cancel work is a team that
			// completes it; this one has both.
			fake.mu.Lock()
			fake.states = append(fake.states, lnState{
				ID: "state-cancelled", Name: "Cancelled", Type: lnStateCanceled, Position: 99,
			})
			fake.mu.Unlock()

			if err := linear.Close(t.Context(), c.key, c.reason); err != nil {
				t.Fatalf("Close: %v", err)
			}
			ticket, err := linear.Ticket(t.Context(), c.key)
			if err != nil {
				t.Fatalf("Ticket: %v", err)
			}
			if !ticket.Closed {
				t.Fatal("the ticket is not closed")
			}
			if ticket.CloseReason != c.want {
				t.Errorf("CloseReason = %q, want %q", ticket.CloseReason, c.want)
			}
			// Linear dates the ending itself, and that date is when
			// the work was closed: a ticket closed at no time at all
			// never ages out of a window.
			if !ticket.ClosedAt.After(coretest.ContractNow) {
				t.Errorf("ClosedAt = %v, want the moment Linear recorded", ticket.ClosedAt)
			}
		})
	}
}

// Linear answers a page at a time, and a board is more than one page. A read
// that stops at the first page is a board flow only half knows.
func TestLinearReadsEveryPageOfTheBoardAndOfItsColumns(t *testing.T) {
	fake, linear := linearWorld(t)

	tickets, err := linear.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	// The fixture holds six tickets, one of them archived, and the fake
	// serves two to a page.
	if len(tickets) != 5 {
		t.Errorf("got %d tickets, want the five on the board", len(tickets))
	}
	if fake.called("FlowIssues") < 2 {
		t.Errorf("the board was read in %d call(s), so paging was never exercised", fake.called("FlowIssues"))
	}
	observed, err := linear.Observe(t.Context(), core.ItemTrackerStatusOptions)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(observed.Items) < 5 {
		t.Errorf("got %d columns, want every page of them", len(observed.Items))
	}
}

// The team is read once a run. It is the same answer every time, and a board
// that asks for it again on every item spends a run's budget on nothing.
func TestLinearAsksWhichTeamItIsOnlyOnce(t *testing.T) {
	fake, linear := linearWorld(t)

	for _, what := range []string{core.ItemTrackerProject, core.ItemTrackerStatusOptions, core.ItemTrackerLabels} {
		if _, err := linear.Observe(t.Context(), what); err != nil {
			t.Fatalf("Observe(%s): %v", what, err)
		}
	}
	if got := fake.called("FlowTeam"); got != 1 {
		t.Errorf("the team was looked up %d times, want once", got)
	}
}

// A team key nobody has set is setup not having run yet, which is a thing to
// say rather than a query against nothing.
func TestLinearSaysWhichSettingIsMissingBeforeItAsksAnything(t *testing.T) {
	fx := coretest.ContractTracker()
	fake, srv := newLinearServer(t, &fx)
	linear := newLinearAdapter(t, srv, func(o *LinearOptions) { o.Team = "" })

	_, err := linear.Tickets(t.Context(), core.TicketQuery{})
	if !errors.Is(err, core.ErrNeedsInput) {
		t.Fatalf("Tickets with no team = %v, want ErrNeedsInput", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "tracker.project") {
		t.Errorf("the refusal says %+v, want it to name the setting", err)
	}
	if got := fake.called("FlowIssues"); got != 0 {
		t.Errorf("flow asked Linear %d time(s) for a board it has no key for", got)
	}
}

// An address Linear itself would show is a reference flow can record, and the
// team it names is the one in it.
func TestLinearReadsATeamOutOfTheAddressesPeoplePaste(t *testing.T) {
	_, linear := linearWorld(t)
	for _, c := range []struct {
		raw  string
		want string
	}{
		{raw: "https://linear.app/acme/team/ENG/all", want: "ENG"},
		{raw: "https://linear.app/acme/team/eng/active", want: "ENG"},
		{raw: "https://linear.app/acme/issue/ENG-12/add-the-filter-bar", want: "ENG"},
		{raw: "ENG", want: "ENG"},
		{raw: "ENG-12", want: "ENG"},
	} {
		ref, err := linear.ParseRef(c.raw)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", c.raw, err)
			continue
		}
		if ref.ID != c.want {
			t.Errorf("ParseRef(%q).ID = %q, want %q", c.raw, ref.ID, c.want)
		}
	}
	for _, raw := range []string{"https://github.com/acme/widget", "https://linear.app/acme", ""} {
		if _, err := linear.ParseRef(raw); !errors.Is(err, core.ErrValidation) {
			t.Errorf("ParseRef(%q) = %v, want ErrValidation", raw, err)
		}
	}
}

// The probe says what the credential may do with this team in the vocabulary
// the declaration is written in, or the two can never agree (spec 05 §1).
func TestLinearProbeAnswersInTheVocabularyTheDeclarationUses(t *testing.T) {
	fake, linear := linearWorld(t)
	fake.admin = false

	permissions, err := linear.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slices.Contains(permissions.Tokens, lnTokenTeamAdmin) {
		t.Errorf("a member who administers nothing was granted %q", lnTokenTeamAdmin)
	}
	if !slices.ContainsFunc(permissions.Evidence, func(line string) bool {
		return strings.Contains(line, "member of the "+lnFakeTeam)
	}) {
		t.Errorf("the evidence is %v, want it to say what the credential is", permissions.Evidence)
	}

	// Owning the team is the other way to be allowed to edit its columns.
	fake.mu.Lock()
	for at := range fake.teams {
		if fake.teams[at].Key == lnFakeTeam {
			fake.teams[at].Membership = &lnMembership{Owner: true}
		}
	}
	fake.mu.Unlock()
	_, owner := linearWorldOwning(t)
	permissions, err = owner.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !slices.Contains(permissions.Tokens, lnTokenTeamAdmin) {
		t.Errorf("the owner of the team was granted %v, want %q", permissions.Tokens, lnTokenTeamAdmin)
	}
}

// linearWorldOwning is a world whose credential owns the team but administers
// nothing else.
func linearWorldOwning(t *testing.T) (*lnFake, *Linear) {
	t.Helper()
	fx := coretest.ContractTracker()
	fake, srv := newLinearServer(t, &fx)
	fake.admin = false
	for at := range fake.teams {
		if fake.teams[at].Key == lnFakeTeam {
			fake.teams[at].Membership = &lnMembership{Owner: true}
		}
	}
	fake.team.Membership = &lnMembership{Owner: true}
	return fake, newLinearAdapter(t, srv, nil)
}

// A category is read from the state's type and written back as one, and the
// two must agree: a column flow creates in a category must observe in it.
func TestLinearWritesTheCategoryItReadsBack(t *testing.T) {
	for _, category := range []string{categoryTodo, categoryInProgress, categoryDone} {
		if got := lnCategory(lnStateTypeOf(category)); got != category {
			t.Errorf("a %s column is created as %q, which reads back as %q",
				category, lnStateTypeOf(category), got)
		}
	}
	// Every type Linear has reads as one of flow's three.
	for _, stateType := range []string{
		lnStateTriage, lnStateBacklog, lnStateUnstarted, lnStateStarted, lnStateCompleted, lnStateCanceled,
	} {
		if lnCategory(stateType) == "" {
			t.Errorf("a %s state reads as no category at all", stateType)
		}
	}
	if lnCategory("inbox") != "" {
		t.Error("a state type Linear does not have reads as a category, which is a guess")
	}
}

// Colour is six hex digits everywhere in flow and `#rrggbb` in Linear, and
// the configuration is written once for whichever tracker is underneath it.
func TestLinearRecordsAColourTheWayEveryOtherAdapterDoes(t *testing.T) {
	_, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerLabels)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	blocked, ok := observed.Find("blocked")
	if !ok {
		t.Fatal("the fixture's blocked label is not there")
	}
	if got := blocked.Field(lnFieldColour); got != "b60205" {
		t.Errorf("the colour reads as %q, want six hex digits and no hash", got)
	}
	if got := lnColourOf(core.Item{Fields: map[string]string{lnFieldColour: "b60205"}}); got != "#b60205" {
		t.Errorf("flow would write the colour as %q, want Linear's own spelling", got)
	}
	if got := lnColourOf(core.Item{}); got != "" {
		t.Errorf("a change that asks for no colour would write %q, want nothing", got)
	}
}

// Work lands in the leftmost column of its kind. A team with two completed
// columns has one work finishes in, and it is the one furthest left — the
// order the team put its columns in, which is the order flow reads them in.
func TestLinearLandsWorkInTheLeftmostColumnOfItsKind(t *testing.T) {
	fake, linear := linearWorld(t)
	fake.mu.Lock()
	// Further right on the board, and first by every other ordering.
	fake.states = append(fake.states, lnState{
		ID: "aaa-shipped", Name: "Shipped", Type: lnStateCompleted, Position: 99,
	})
	fake.mu.Unlock()

	if err := linear.Close(t.Context(), lnFakeKeyPrefix+"10", core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ticket, err := linear.Ticket(t.Context(), lnFakeKeyPrefix+"10")
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if ticket.Status.Name != "Done" {
		t.Errorf("the work finished in %q, want the leftmost completed column", ticket.Status.Name)
	}
}

// A follow-up flow filed in this run is remembered, so that asking for it
// again costs nothing. Filing is the common case in a `--fix` run and every
// filing asks first.
func TestLinearRemembersTheFollowUpItJustFiled(t *testing.T) {
	fake, linear := linearWorld(t)
	marker := core.NewMarker(core.MarkerFollowUp, "check", "forge.files", "repo", "acme/widget")
	follow := core.FollowUp{Marker: marker, Title: "flow: write the workflow file", Body: "Somebody has to."}

	first, err := linear.CreateFollowUp(t.Context(), follow)
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	searched := fake.called("FlowArchivedIssues")

	again, err := linear.CreateFollowUp(t.Context(), follow)
	if err != nil {
		t.Fatalf("CreateFollowUp again: %v", err)
	}
	if again.Key != first.Key {
		t.Errorf("the second filing made %s, want the %s it had already filed", again.Key, first.Key)
	}
	if got := fake.called("FlowArchivedIssues"); got != searched {
		t.Errorf("flow searched %d more time(s) for a ticket it had just filed", got-searched)
	}
	if got := fake.called("FlowIssueCreate"); got != 1 {
		t.Errorf("flow filed %d tickets, want one", got)
	}
}

// flow has one comment on a ticket and edits it, rather than writing a second
// one every run. A comment that says the same thing is not rewritten at all.
func TestLinearEditsItsOwnCommentRatherThanWritingASecond(t *testing.T) {
	fake, linear := linearWorld(t)
	marker := core.NewMarker(core.MarkerPolicy, "pr", "7", "repo", "acme/widget")
	key := lnFakeKeyPrefix + "10"

	// Other people talk on a ticket too, and flow's own comment is not
	// always on the first page of what they said.
	fake.seedComment(key, "I will look at this tomorrow")
	fake.seedComment(key, "thanks")

	if err := linear.Comment(t.Context(), key, marker, "the gate is red"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := linear.Comment(t.Context(), key, marker, "the gate is green"); err != nil {
		t.Fatalf("Comment again: %v", err)
	}
	comments := fake.commentsOn(key)
	if len(comments) != 3 {
		t.Fatalf("the ticket carries %d comments, want the two others and the one flow keeps editing", len(comments))
	}
	mine := comments[len(comments)-1]
	if !strings.Contains(mine.Body, "the gate is green") {
		t.Errorf("the comment says %q, want what flow last said", mine.Body)
	}
	at := mine.UpdatedAt.Time

	if err := linear.Comment(t.Context(), key, marker, "the gate is green"); err != nil {
		t.Fatalf("Comment a third time: %v", err)
	}
	if got := fake.commentsOn(key)[2].UpdatedAt.Time; !got.Equal(at) {
		t.Errorf("saying the same thing again moved the comment's date from %v to %v", at, got)
	}
}

// An update that would change nothing is not written. A run that rewrites
// every label it reads makes a diff of its own every day.
func TestLinearWritesNothingWhenTheLabelAlreadySaysThat(t *testing.T) {
	fake, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerLabels)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	blocked, ok := observed.Find("blocked")
	if !ok {
		t.Fatal("the fixture's blocked label is not there")
	}

	result, err := linear.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels, Op: core.ChangeUpdate, Item: blocked,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(result.Note, "already says that") {
		t.Errorf("the result says %q, want it to say the label already said that", result.Note)
	}
	if got := fake.called("FlowLabelUpdate"); got != 0 {
		t.Errorf("flow wrote the label %d time(s) to change nothing", got)
	}
}

// A change carries the id the lock recorded, and that is what finds the thing
// again: a label flow renamed is still the label flow made.
func TestLinearFindsARenamedLabelByTheIdItRecorded(t *testing.T) {
	_, linear := linearWorld(t)
	observed, err := linear.Observe(t.Context(), core.ItemTrackerLabels)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	blocked, ok := observed.Find("blocked")
	if !ok {
		t.Fatal("the fixture's blocked label is not there")
	}
	renamed := blocked
	renamed.Name = "halted"

	result, err := linear.Apply(t.Context(), core.Change{
		What: core.ItemTrackerLabels, Op: core.ChangeUpdate, Item: renamed,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Item.ID != blocked.ID {
		t.Errorf("the renamed label has id %q, want %q", result.Item.ID, blocked.ID)
	}
	if !strings.Contains(result.Note, "halted") {
		t.Errorf("the result says %q, want it to name the rename", result.Note)
	}
}

// A limit is a limit: a caller that asks for three tickets is given three,
// however many a page holds.
func TestLinearStopsReadingWhenItHasWhatItWasAskedFor(t *testing.T) {
	_, linear := linearWorld(t)

	tickets, err := linear.Tickets(t.Context(), core.TicketQuery{Limit: 3})
	if err != nil {
		t.Fatalf("Tickets: %v", err)
	}
	if len(tickets) != 3 {
		t.Errorf("got %d tickets, want the three that were asked for", len(tickets))
	}
}
