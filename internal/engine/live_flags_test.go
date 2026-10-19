package engine_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/git"
)

// The feature flag audit of spec 07 §6, and scenario V05: a commit announced
// a flag, so there has to be a ticket to take it out again, and `--fix` files
// the one that is missing — once.

// trailers is what git made of the messages a test wrote.
//
// It is a lookup rather than a parser. Where a trailer block begins is a
// question only git answers consistently (spec 08 §2), which is why flow asks
// it; a second parser written here would agree with flow and with nothing
// else, and would make the fake the thing under test. The wiring to the real
// `git interpret-trailers` is held by TestDoctorReadsTrailersWithGitItself in
// the root package, against a repository on disk.
type trailers struct {
	byMessage map[string][]git.Trailer
	err       error
	asked     int
}

func newTrailers() *trailers {
	return &trailers{byMessage: map[string][]git.Trailer{}}
}

func (p *trailers) Trailers(_ context.Context, message string) ([]git.Trailer, error) {
	p.asked++
	if p.err != nil {
		return nil, p.err
	}
	return p.byMessage[message], nil
}

// announcing is a commit on the trunk whose message announces each named flag
// with a Flag-Added trailer, registered with the parser as git would read it.
func (p *trailers) announcing(n int, header string, flags ...string) core.Commit {
	message := header
	if len(flags) > 0 {
		message += "\n\n"
		for _, name := range flags {
			message += "Flag-Added: " + name + "\n"
		}
	}
	for _, name := range flags {
		p.byMessage[message] = append(p.byMessage[message], git.Trailer{Key: "Flag-Added", Value: name})
	}
	return core.Commit{
		SHA:         fmt.Sprintf("c0mmit%03d", n),
		Message:     message,
		AuthoredAt:  daysAgo(n),
		CommittedAt: daysAgo(n),
		URL:         fmt.Sprintf("https://github.com/acme/api/commit/c0mmit%03d", n),
	}
}

// flagConfig is liveConfig with somewhere for a cleanup ticket to go.
func flagConfig() *config.Config {
	cfg := liveConfig()
	cfg.FollowUps.Labels = []string{"flow-followup"}
	cfg.FollowUps.Owners.Default = config.Known("@dev")
	return cfg
}

// flagEnv is a window whose trunk holds those commits, with a parser that
// knows what they say.
func flagEnv(t *testing.T, commits ...core.Commit) (*engine.Env, *coretest.FakeTracker) {
	t.Helper()
	env, _ := liveEnv(t, flagConfig(), coretest.ForgeFixture{Commits: commits})
	tracker, ok := env.Adapters.Tracker.(*coretest.FakeTracker)
	if !ok {
		t.Fatalf("the live tests no longer use a fake tracker: %T", env.Adapters.Tracker)
	}
	return env, tracker
}

// TestScenario_V05_AFlagNobodyHasFiledForIsAFinding: the commit made a promise and the
// board has no record of it, which is the whole of what the check is about.
func TestScenario_V05_AFlagNobodyHasFiledForIsAFinding(t *testing.T) {
	parser := newTrailers()
	env, _ := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateFail {
		t.Errorf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	evidenceMentions(t, got, "new_basket")
	if got.Remediation.Kind != engine.RemedyAuto || got.Remediation.Command != "flow doctor --fix" {
		t.Errorf("remediation is %+v, want flow's own command", got.Remediation)
	}
}

// TestScenario_V05_FixFilesOneCleanupTicketAndThenLeavesItAlone is scenario V05: --fix
// opens exactly one ticket, and running again opens none.
func TestScenario_V05_FixFilesOneCleanupTicketAndThenLeavesItAlone(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	env.Fix = true

	first := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if first.State != engine.StatePass {
		t.Errorf("state is %q, want %q: %s", first.State, engine.StatePass, first.Message)
	}
	wantCounts(t, first, 1, 1)
	evidenceMentions(t, first, "so flow filed")

	filed := flagTickets(t, tracker)
	if len(filed) != 1 {
		t.Fatalf("flow filed %d tickets, want 1: %+v", len(filed), filed)
	}
	ticket := filed[0]
	if want := "flow: remove the feature flag new_basket (acme/api)"; ticket.Title != want {
		t.Errorf("title is %q, want %q", ticket.Title, want)
	}
	if !strings.Contains(ticket.Body, "c0mmit002") {
		t.Errorf("the ticket does not say where the flag came from:\n%s", ticket.Body)
	}
	if !strings.Contains(ticket.Body, "closed by a person") {
		t.Errorf("the ticket does not say who closes it:\n%s", ticket.Body)
	}
	if len(ticket.Labels) != 1 || ticket.Labels[0] != "flow-followup" {
		t.Errorf("labels are %v, want the configured follow-up labels", ticket.Labels)
	}
	if len(ticket.Assignees) != 1 || ticket.Assignees[0] != "@dev" {
		t.Errorf("assignees are %v, want the default owner", ticket.Assignees)
	}

	before := tracker.Calls("CreateFollowUp")
	second := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if second.State != engine.StatePass {
		t.Errorf("the second run is %q: %s", second.State, second.Message)
	}
	if again := tracker.Calls("CreateFollowUp") - before; again != 0 {
		t.Errorf("the second run filed %d more tickets, want none", again)
	}
	if len(second.Evidence) != 0 {
		t.Errorf("the second run reported %+v, want nothing to say", second.Evidence)
	}
	if !strings.Contains(second.Message, "has a cleanup ticket") {
		t.Errorf("the second run says %q", second.Message)
	}
}

// TestAFlagAnnouncedTwiceIsOneTicket: the marker names the flag, not the
// commit, so a flag two commits mention is one promise and one ticket.
func TestAFlagAnnouncedTwiceIsOneTicket(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t,
		parser.announcing(2, "feat(checkout): the new basket", "new_basket"),
		parser.announcing(5, "feat(checkout): more of the new basket", "new_basket"),
	)
	env.Trailers = parser
	env.Fix = true

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	wantCounts(t, got, 1, 1)
	if filed := flagTickets(t, tracker); len(filed) != 1 {
		t.Fatalf("flow filed %d tickets for one flag: %+v", len(filed), filed)
	}
}

// TestAFlagAlreadyDealtWithStillCounts: the check asks whether the promise
// was written down, not whether it has been kept — flow cannot read the code,
// so a closed cleanup ticket is as good an answer as an open one.
func TestAFlagAlreadyDealtWithStillCounts(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	seedFlagTicket(t, tracker, "new_basket", "acme/api", true)

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StatePass {
		t.Errorf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 1, 1)
}

// TestACleanupTicketBelongsToOneRepository: one tracker serves many
// repositories, and `new_basket` in somebody else's is not this repository's
// flag. Without the repository in the marker the two would be one ticket and
// the second repository would never get one.
func TestACleanupTicketBelongsToOneRepository(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	seedFlagTicket(t, tracker, "new_basket", "acme/other", false)

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateFail {
		t.Errorf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
}

// TestFlagsAreReportedInNameOrder: the evidence is a list somebody reads on
// two consecutive mornings, and a list that reorders itself by whatever order
// the forge answered in is a list that cannot be compared with yesterday's.
func TestFlagsAreReportedInNameOrder(t *testing.T) {
	parser := newTrailers()
	env, _ := flagEnv(t,
		parser.announcing(2, "feat(checkout): three at once", "zebra", "apple", "mango"),
	)
	env.Trailers = parser

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	wantCounts(t, got, 0, 3)
	var names []string
	for _, seen := range got.Evidence {
		names = append(names, strings.Fields(seen.Text)[0])
	}
	if want := []string{"apple", "mango", "zebra"}; !slices.Equal(names, want) {
		t.Errorf("the flags are reported %v, want %v", names, want)
	}
}

// TestAWindowThatAddedNoFlagsIsAQuietPass: nothing to say and nothing wrong,
// said in those words rather than as "all 0 flags were fine".
func TestAWindowThatAddedNoFlagsIsAQuietPass(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "fix(core): tidy up"))
	env.Trailers = parser
	env.Fix = true

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StatePass {
		t.Errorf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
	if want := "nothing in the window added a feature flag"; got.Message != want {
		t.Errorf("message is %q, want %q", got.Message, want)
	}
	if tracker.Calls("FindFollowUp") != 0 {
		t.Errorf("a window with no flags asked the tracker %d questions", tracker.Calls("FindFollowUp"))
	}
}

// TestATrailerThatNamesNoFlagAnnouncesNothing: `Flag-Added:` with nothing
// after it is a flag nobody could file a ticket for, and judging the trailer
// itself is the commit linter's job — which spec 08 §4 took away from it.
func TestATrailerThatNamesNoFlagAnnouncesNothing(t *testing.T) {
	parser := newTrailers()
	commit := parser.announcing(2, "feat(checkout): the new basket", "   ")
	env, _ := flagEnv(t, commit)
	env.Trailers = parser

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	wantCounts(t, got, 0, 0)
	if got.State != engine.StatePass {
		t.Errorf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
}

// TestFlowThatCannotReadTheTrailersSaysSo: a run with no working copy has not
// established that nobody added a flag. An empty answer and "nothing was
// wanted" must never be the same value.
func TestFlowThatCannotReadTheTrailersSaysSo(t *testing.T) {
	parser := newTrailers()
	env, _ := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = nil

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateError {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateError, got.Message)
	}
	if strings.Contains(got.Message, "nothing in the window") {
		t.Errorf("a blind run reported a quiet one: %q", got.Message)
	}

	parser.err = errors.New("git is not on the path")
	env.Trailers = parser
	if got := liveResult(t, env, engine.CheckLiveFlagsCleanup); got.State != engine.StateError {
		t.Errorf("a parser that failed gave %q: %s", got.State, got.Message)
	}
}

// TestATrackerThatWillNotTakeTheTicketSaysWhatItHit: the run was allowed to
// fix it and could not, so offering `flow doctor --fix` would be offering the
// command that has just failed.
func TestATrackerThatWillNotTakeTheTicketSaysWhatItHit(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	env.Fix = true
	tracker.Fail("CreateFollowUp", errors.New("the credential may not open issues"))

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	evidenceMentions(t, got, "may not open issues")
	if got.Remediation.Kind != engine.RemedyManual {
		t.Errorf("remediation is %+v, want steps for a person", got.Remediation)
	}
	if got.Remediation.Command != "" {
		t.Errorf("it offered %q, which is the command that just failed", got.Remediation.Command)
	}
}

// TestATrackerThatCannotBeSearchedIsNotAnEmptyBoard: a failed search says
// there is no ticket, which is exactly the shape of a finding — and under
// --fix would file a duplicate of a ticket that is already there.
func TestATrackerThatCannotBeSearchedIsNotAnEmptyBoard(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	env.Fix = true
	tracker.Fail("FindFollowUp", errors.New("the tracker is having a bad minute"))

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateError {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateError, got.Message)
	}
	if tracker.Calls("CreateFollowUp") != 0 {
		t.Errorf("flow filed a ticket without knowing whether one was there")
	}
}

// TestWithNoTrackerThereIsNowhereForACleanupTicketToGo: the promise was made
// and there is no board to have written it down on, which is a run that could
// not look rather than a fortnight in which everybody kept their word.
func TestWithNoTrackerThereIsNowhereForACleanupTicketToGo(t *testing.T) {
	parser := newTrailers()
	env, _ := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	env.Adapters.Tracker = nil

	got := liveResult(t, env, engine.CheckLiveFlagsCleanup)
	if got.State != engine.StateError {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateError, got.Message)
	}
	if !strings.Contains(got.Message, "tracker") {
		t.Errorf("the message does not say what was missing: %q", got.Message)
	}
}

// flagTickets is every cleanup ticket on the board.
func flagTickets(t *testing.T, tracker *coretest.FakeTracker) []core.Ticket {
	t.Helper()
	tickets, err := tracker.Tickets(t.Context(), core.TicketQuery{})
	if err != nil {
		t.Fatalf("read the board: %v", err)
	}
	var flags []core.Ticket
	for _, ticket := range tickets {
		if marker, found := core.FindMarker(ticket.Body); found && marker.Kind == core.MarkerFlag {
			flags = append(flags, ticket)
		}
	}
	return flags
}

// seedFlagTicket puts a cleanup ticket on the board as somebody — or an
// earlier run — left it.
func seedFlagTicket(t *testing.T, tracker *coretest.FakeTracker, name, repo string, closed bool) {
	t.Helper()
	ticket, err := tracker.CreateFollowUp(t.Context(), core.FollowUp{
		Marker: engine.FlagMarker(name, repo),
		Title:  engine.FlagTitle(name, repo),
		Body:   "somebody is on it",
	})
	if err != nil {
		t.Fatalf("seed a cleanup ticket: %v", err)
	}
	if !closed {
		return
	}
	if err := tracker.Close(t.Context(), ticket.Key, "done"); err != nil {
		t.Fatalf("close the seeded ticket: %v", err)
	}
}

// The other half of the family (spec 07 §6, issue #146): the cleanup check
// asks whether the promise was written down, and these ask what became of it
// once the flag has stood longer than the team agreed to.
//
// Every one of them files its tickets by running the cleanup check under
// `--fix` and then moves the clock on, rather than writing a marker of its
// own. The date in that marker is the only thing the two checks say to each
// other, so a test that wrote it by hand would be a second author of flow's
// one format, and would keep agreeing with itself after the two halves had
// stopped agreeing with each other.

// aged files a cleanup ticket for each named flag the way `--fix` does, then
// moves the clock on so that they are flags of that age.
func aged(t *testing.T, cfg *config.Config, days int, names ...string) (*engine.Env, *coretest.FakeTracker) {
	t.Helper()
	if cfg == nil {
		cfg = flagConfig()
	}
	parser := newTrailers()
	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{
		Commits: []core.Commit{parser.announcing(1, "feat(checkout): the new basket", names...)},
	})
	tracker, ok := env.Adapters.Tracker.(*coretest.FakeTracker)
	if !ok {
		t.Fatalf("the live tests no longer use a fake tracker: %T", env.Adapters.Tracker)
	}
	env.Trailers = parser
	env.Fix = true
	if got := liveResult(t, env, engine.CheckLiveFlagsCleanup); got.State != engine.StatePass {
		t.Fatalf("the cleanup check did not file the tickets: %s", got.Message)
	}
	env.Fix = false
	clock, ok := env.Clock.(*coretest.FakeClock)
	if !ok {
		t.Fatalf("the live tests no longer use a clock a test can move: %T", env.Clock)
	}
	clock.Advance(time.Duration(days) * 24 * time.Hour)
	return env, tracker
}

// label puts a label on a ticket, as a person would.
func label(t *testing.T, tracker *coretest.FakeTracker, key string, labels ...string) {
	t.Helper()
	ticket, err := tracker.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	ticket.Labels = append(ticket.Labels, labels...)
	tracker.SetTicket(ticket)
}

// onlyFlagTicket is the key of the one cleanup ticket on the board.
func onlyFlagTicket(t *testing.T, tracker *coretest.FakeTracker) string {
	t.Helper()
	tickets := flagTickets(t, tracker)
	if len(tickets) != 1 {
		t.Fatalf("the board holds %d cleanup tickets, want one", len(tickets))
	}
	return tickets[0].Key
}

// TestAFlagThatHasOutstayedTheAgreementIsAFinding is the whole point of the
// check: the ticket is open, the flag is older than the team said flags live,
// and nobody has said why.
func TestAFlagThatHasOutstayedTheAgreementIsAFinding(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	key := onlyFlagTicket(t, tracker)
	evidenceMentions(t, got, "101 days")
	evidenceMentions(t, got, key)
	// The flag by name, and not by way of the ticket's title, which has the
	// name inside it and would answer to every reading of this.
	if !strings.HasPrefix(got.Evidence[0].Text, "new_basket has stood") {
		t.Errorf("the evidence does not lead with the flag: %q", got.Evidence[0].Text)
	}
	evidenceLinks(t, got, "new_basket", "https://tracker.test/issues/1")
	if !strings.Contains(got.Message, "90d") {
		t.Errorf("the message does not say what the agreement was: %q", got.Message)
	}
	if got.Remediation.Kind != engine.RemedyManual {
		t.Errorf("remediation is %+v, want steps for a person", got.Remediation)
	}
	if got.Remediation.Command != "" {
		t.Errorf("it offered %q; flow cannot read the code, so it cannot do this one", got.Remediation.Command)
	}
	if !slices.ContainsFunc(got.Remediation.Steps, func(step string) bool {
		return strings.Contains(step, config.DefaultFlagKeptLabel)
	}) {
		t.Errorf("the remedy never says how a flag is allowed to stay: %v", got.Remediation.Steps)
	}
}

// TestAFlagThatSaysWhyItStaysIsNotAFinding: the label is the team's word that
// this one is deliberate, which is the other half of the proposal's rule.
func TestAFlagThatSaysWhyItStaysIsNotAFinding(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	label(t, tracker, onlyFlagTicket(t, tracker), config.DefaultFlagKeptLabel)

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 1, 1)
	if !strings.Contains(got.Message, "gone or said why it stays") {
		t.Errorf("the message does not say what held: %q", got.Message)
	}
}

// TestALabelIsTheTeamsWordHoweverTheyCapitalisedIt: trackers differ on
// whether a label's case is part of its identity, and no team means two
// spellings to be two labels.
func TestALabelIsTheTeamsWordHoweverTheyCapitalisedIt(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	label(t, tracker, onlyFlagTicket(t, tracker), strings.ToUpper(config.DefaultFlagKeptLabel))

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 1, 1)
}

// TestAFlagWhoseTicketIsClosedIsOneThatWentOutAgain: closing the ticket is
// how a person says the flag is gone, and the check is never a finding about
// work that was done.
func TestAFlagWhoseTicketIsClosedIsOneThatWentOutAgain(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	if err := tracker.Close(t.Context(), onlyFlagTicket(t, tracker), "the flag is out"); err != nil {
		t.Fatalf("close the cleanup ticket: %v", err)
	}

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
}

// TestAFlagYoungerThanTheAgreementIsNobodysBusinessYet: only flags past the
// date are counted, so one forgotten flag among fifty healthy ones is one of
// one and not an encouraging ninety-eight per cent.
func TestAFlagYoungerThanTheAgreementIsNobodysBusinessYet(t *testing.T) {
	env, _ := aged(t, nil, 10, "new_basket")

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
	if !strings.Contains(got.Message, "no feature flag") || !strings.Contains(got.Message, "90d") {
		t.Errorf("the message does not say which nothing this was: %q", got.Message)
	}
}

// TestOnlyTheFlagsPastTheDateAreCounted holds the counting itself: a board
// with one old flag and one young one is one of one, whatever the young one
// is doing.
func TestOnlyTheFlagsPastTheDateAreCounted(t *testing.T) {
	cfg := flagConfig()
	cfg.Health.FlagMaxAge = config.Duration(30 * 24 * time.Hour)
	env, tracker := aged(t, cfg, 100, "new_basket")

	// A second flag, announced a hundred days after the first, so that only
	// one of the two is past the thirty days this team agreed to.
	parser := newTrailers()
	forge, ok := env.Adapters.Forge.(*coretest.FakeForge)
	if !ok {
		t.Fatalf("the live tests no longer use a fake forge: %T", env.Adapters.Forge)
	}
	forge.SetCommits(core.Commit{
		SHA:         "n3wf1ag",
		Message:     "feat(search): try the new ranking\n\nFlag-Added: new_ranking\n",
		CommittedAt: env.Now(),
		URL:         "https://github.com/acme/api/commit/n3wf1ag",
	})
	parser.byMessage["feat(search): try the new ranking\n\nFlag-Added: new_ranking\n"] =
		[]git.Trailer{{Key: "Flag-Added", Value: "new_ranking"}}
	env.Trailers = parser
	env.Fix = true
	if got := liveResult(t, env, engine.CheckLiveFlagsCleanup); got.State != engine.StatePass {
		t.Fatalf("the cleanup check did not file the second ticket: %s", got.Message)
	}
	env.Fix = false
	if len(flagTickets(t, tracker)) != 2 {
		t.Fatalf("the board holds %d cleanup tickets, want two", len(flagTickets(t, tracker)))
	}

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	evidenceMentions(t, got, "new_basket")
	for _, seen := range got.Evidence {
		if strings.Contains(seen.Text, "new_ranking") {
			t.Errorf("a flag added today is already a finding: %q", seen.Text)
		}
	}
}

// TestTheTeamsOwnAgeAndLabelAreTheOnesRead: both halves of the agreement are
// the team's to write down, and a team that renamed the label has not
// thereby lost the way out of the finding.
func TestTheTeamsOwnAgeAndLabelAreTheOnesRead(t *testing.T) {
	cfg := flagConfig()
	cfg.Health.FlagMaxAge = config.Duration(7 * 24 * time.Hour)
	cfg.Health.FlagKeptLabel = "wontfix-flag"
	env, tracker := aged(t, cfg, 10, "new_basket")

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	if !strings.Contains(got.Message, "7d") {
		t.Errorf("the message quotes somebody else's agreement: %q", got.Message)
	}

	// The default label is not this team's word for anything.
	key := onlyFlagTicket(t, tracker)
	label(t, tracker, key, config.DefaultFlagKeptLabel)
	if got := liveResult(t, env, engine.CheckLiveFlagsAge); got.State != engine.StateFail {
		t.Fatalf("a label this team never agreed to answered for the flag: %s", got.Message)
	}

	label(t, tracker, key, "wontfix-flag")
	if got := liveResult(t, env, engine.CheckLiveFlagsAge); got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
}

// TestTheClockIsTheCommitsAndNotTheTickets: the ticket has been closed,
// reopened and touched this morning, so every date it carries says today —
// and the flag is still as old as the day the commit landed.
func TestTheClockIsTheCommitsAndNotTheTickets(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	key := onlyFlagTicket(t, tracker)
	if err := tracker.Close(t.Context(), key, "wrong button"); err != nil {
		t.Fatalf("close the cleanup ticket: %v", err)
	}
	if err := tracker.Reopen(t.Context(), key, "no, it is still there"); err != nil {
		t.Fatalf("reopen the cleanup ticket: %v", err)
	}
	touched, err := tracker.Ticket(t.Context(), key)
	if err != nil {
		t.Fatalf("read the cleanup ticket: %v", err)
	}
	touched.UpdatedAt = env.Now()
	touched.ClosedAt = time.Time{}
	tracker.SetTicket(touched)

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	evidenceMentions(t, got, "101 days")
}

// TestATicketFromBeforeFlowRecordedTheDateCannotBeAged: the one date such a
// ticket has is its own, and that is the date this check must not use.
func TestATicketFromBeforeFlowRecordedTheDateCannotBeAged(t *testing.T) {
	parser := newTrailers()
	env, tracker := flagEnv(t, parser.announcing(2, "feat(checkout): the new basket", "new_basket"))
	env.Trailers = parser
	seedFlagTicket(t, tracker, "new_basket", "acme/api", false)

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
}

// TestAFlagInAnotherRepositoryIsNotThisAuditsBusiness: one tracker serves
// many repositories, and the audit is about one of them.
func TestAFlagInAnotherRepositoryIsNotThisAuditsBusiness(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	elsewhere, err := tracker.Ticket(t.Context(), onlyFlagTicket(t, tracker))
	if err != nil {
		t.Fatalf("read the cleanup ticket: %v", err)
	}
	elsewhere.Body = strings.ReplaceAll(elsewhere.Body, "repo=acme/api", "repo=acme/other")
	tracker.SetTicket(elsewhere)

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
}

// TestABoardNobodyCanReadIsNotABoardWithNoOldFlagsOnIt: a run that could not
// look has not established that every flag is accounted for.
func TestABoardNobodyCanReadIsNotABoardWithNoOldFlagsOnIt(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	tracker.Fail("Tickets", errors.New("the tracker is having a bad minute"))

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateError {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateError, got.Message)
	}
}

// TestTheTicketFlowFilesSaysHowAFlagIsAllowedToStay: the ticket is where a
// person reads about this, so the way out of the finding has to be in it.
func TestTheTicketFlowFilesSaysHowAFlagIsAllowedToStay(t *testing.T) {
	_, tracker := aged(t, nil, 0, "new_basket")
	tickets := flagTickets(t, tracker)
	if len(tickets) != 1 {
		t.Fatalf("the board holds %d cleanup tickets, want one", len(tickets))
	}
	for _, want := range []string{config.DefaultFlagKeptLabel, "90d", engine.CheckLiveFlagsAge} {
		if !strings.Contains(tickets[0].Body, want) {
			t.Errorf("the ticket never mentions %q:\n%s", want, tickets[0].Body)
		}
	}
}

// TestATicketThatNamesNoRepositoryIsTakenAsThisOnes: a marker from before
// flow recorded the repository says nothing about which one it meant, and
// the audit it turns up in is the only candidate there is.
func TestATicketThatNamesNoRepositoryIsTakenAsThisOnes(t *testing.T) {
	env, tracker := aged(t, nil, 100, "new_basket")
	anonymous, err := tracker.Ticket(t.Context(), onlyFlagTicket(t, tracker))
	if err != nil {
		t.Fatalf("read the cleanup ticket: %v", err)
	}
	anonymous.Body = strings.ReplaceAll(anonymous.Body, " repo=acme/api", "")
	tracker.SetTicket(anonymous)

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
}

// TestAFlagIsAsOldAsTheDayItLanded: a branch somebody started in the spring
// and merged this morning put the flag in the code this morning, and the
// date the commit was written is not the date the team has to live with.
func TestAFlagIsAsOldAsTheDayItLanded(t *testing.T) {
	parser := newTrailers()
	announced := parser.announcing(1, "feat(checkout): the new basket", "new_basket")
	announced.AuthoredAt = daysAgo(200)
	env, _ := liveEnv(t, flagConfig(), coretest.ForgeFixture{Commits: []core.Commit{announced}})
	env.Trailers = parser
	env.Fix = true
	if got := liveResult(t, env, engine.CheckLiveFlagsCleanup); got.State != engine.StatePass {
		t.Fatalf("the cleanup check did not file the ticket: %s", got.Message)
	}

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StatePass {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StatePass, got.Message)
	}
	wantCounts(t, got, 0, 0)
}

// TestOneDayIsOneDay: the evidence counts in the unit the agreement is
// written in, and a team whose flags live for hours still reads English.
func TestOneDayIsOneDay(t *testing.T) {
	cfg := flagConfig()
	cfg.Health.FlagMaxAge = config.Duration(12 * time.Hour)
	env, _ := aged(t, cfg, 0, "new_basket")

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	evidenceMentions(t, got, "1 day and")
}

// TestAFlagIsAsOldAsTheFirstCommitThatAnnouncedIt: a flag mentioned again
// last week has been in the code since the first commit that put it there,
// and saying so twice does not make it younger.
func TestAFlagIsAsOldAsTheFirstCommitThatAnnouncedIt(t *testing.T) {
	cfg := flagConfig()
	cfg.Health.FlagMaxAge = config.Duration(5 * 24 * time.Hour)
	parser := newTrailers()
	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{Commits: []core.Commit{
		parser.announcing(1, "feat(checkout): the basket, again", "new_basket"),
		parser.announcing(10, "feat(checkout): the new basket", "new_basket"),
	}})
	env.Trailers = parser
	env.Fix = true
	if got := liveResult(t, env, engine.CheckLiveFlagsCleanup); got.State != engine.StatePass {
		t.Fatalf("the cleanup check did not file the ticket: %s", got.Message)
	}
	env.Fix = false

	got := liveResult(t, env, engine.CheckLiveFlagsAge)
	if got.State != engine.StateFail {
		t.Fatalf("state is %q, want %q: %s", got.State, engine.StateFail, got.Message)
	}
	wantCounts(t, got, 0, 1)
	evidenceMentions(t, got, "10 days")
}
