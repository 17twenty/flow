package engine_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

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
