package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// followUpCheck is the check every test in this file files a follow-up for:
// something flow genuinely cannot do, which is what a follow-up is for.
var followUpCheck = engine.Check{
	ID:       "tracker.wip",
	Title:    "the in-progress column has a work-in-progress limit",
	Category: engine.CategoryTracker,
}

// needsAHuman is a finding that asks for a ticket.
func needsAHuman() engine.Result {
	return engine.Fail("the In progress column has no work-in-progress limit").
		Saw("column limits are per-view and UI-only in github-projects", "https://tracker.test/projects/1").
		Fix(engine.Remediation{
			Kind:    engine.RemedyFollowUp,
			Summary: "set a work-in-progress limit of 2 on In progress",
			Owner:   engine.OwnerTrackerAdmin,
			Steps: []string{
				"open the board's In progress column settings",
				"set the column limit to 2",
			},
		})
}

// followUpConfig is a workflow with its owners filled in, so that routing is
// about the roles rather than about placeholder resolution.
func followUpConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	cfg.Forge.Repo = "acme/widget"
	cfg.FollowUps.Owners.Default = config.Known("@dev")
	cfg.FollowUps.Owners.TrackerAdmin = config.Known("@board-admin")
	cfg.FollowUps.Owners.ForgeAdmin = config.Known("@repo-admin")
	cfg.FollowUps.Owners.Platform = config.Known("@sre")
	return cfg
}

// followUpWorld is a tracker with no follow-ups on it yet, a clock that can
// be wound forward, and an empty lock.
type followUpWorld struct {
	tracker *coretest.FakeTracker
	clock   *coretest.FakeClock
	lock    *config.Lock
	cfg     *config.Config
}

func newFollowUpWorld(t *testing.T) followUpWorld {
	t.Helper()
	fixture := coretest.ContractTracker()
	// The contract fixture already carries a follow-up for tracker.wip, which
	// is the thing most of these tests are about filing. Start from a board
	// that has none.
	fixture.Tickets = nil
	clock := coretest.NewClock(coretest.ContractNow)
	fixture.Clock = clock
	return followUpWorld{
		tracker: coretest.NewFakeTracker(fixture),
		clock:   clock,
		lock:    config.NewLock("v0.3.0"),
		cfg:     followUpConfig(t),
	}
}

// reconcile runs one round of the lifecycle against the world.
func (w followUpWorld) reconcile(t *testing.T, result engine.Result, fix bool) engine.FollowUpOutcome {
	t.Helper()
	out := engine.ReconcileFollowUp(t.Context(), engine.FollowUpInput{
		Check:   followUpCheck,
		Result:  result,
		Config:  w.cfg,
		Lock:    w.lock,
		Tracker: w.tracker,
		Repo:    "acme/widget",
		Clock:   w.clock,
		Fix:     fix,
	})
	if out.Err != nil {
		t.Fatalf("reconcile follow-up: %v", out.Err)
	}
	return out
}

// locked is what the lock remembers about the check.
func (w followUpWorld) locked(t *testing.T) config.FollowUp {
	t.Helper()
	for _, f := range w.lock.FollowUps {
		if f.Check == followUpCheck.ID {
			return f
		}
	}
	t.Fatalf("the lock has no follow-up for %s; it has %+v", followUpCheck.ID, w.lock.FollowUps)
	return config.FollowUp{}
}

// TestAFollowUpIsFiledForWorkFlowCannotDo is the first half of spec 07 §4:
// rather than printing the same warning every morning for a year, flow puts
// the work where the team already looks.
func TestAFollowUpIsFiledForWorkFlowCannotDo(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	out := w.reconcile(t, needsAHuman(), true)

	if out.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpFiled)
	}
	if out.Ticket.Key == "" {
		t.Fatal("no ticket key: the follow-up must be findable")
	}
	if want := "flow: set a work-in-progress limit of 2 on In progress (acme/widget)"; out.Ticket.Title != want {
		t.Errorf("title = %q, want %q", out.Ticket.Title, want)
	}
	if !core.HasMarker(out.Ticket.Body, engine.FollowUpMarker(followUpCheck.ID, "acme/widget")) {
		t.Error("the ticket carries no marker: flow could never find it again")
	}
	if len(out.Ticket.Assignees) != 1 || out.Ticket.Assignees[0] != "@board-admin" {
		t.Errorf("assignees = %v, want the tracker administrator", out.Ticket.Assignees)
	}
	if len(out.Ticket.Labels) != 1 || out.Ticket.Labels[0] != "flow-followup" {
		t.Errorf("labels = %v, want the configured follow-up label", out.Ticket.Labels)
	}

	record := w.locked(t)
	if record.Ticket != out.Ticket.Key || record.State != config.FollowUpOpen {
		t.Errorf("lock record = %+v, want the open ticket", record)
	}
	if record.Owner != "@board-admin" {
		t.Errorf("lock owner = %q, want who it was given to", record.Owner)
	}
}

// TestAFiledFollowUpMakesTheCheckSomebodyElsesMove: once the work is on
// somebody's plate, the check stops being a failure the person running doctor
// can do anything about. Reporting it as a failure forever is what makes
// people stop reading the report.
func TestAFiledFollowUpMakesTheCheckSomebodyElsesMove(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	out := w.reconcile(t, needsAHuman(), true)

	if out.Result.State != engine.StateExternal {
		t.Fatalf("state = %q, want %q", out.Result.State, engine.StateExternal)
	}
	if out.Result.State.OK() {
		t.Error("external counts as OK; waiting on somebody is not the same as done")
	}
	if out.Result.Remediation.Owner != "@board-admin" {
		t.Errorf("owner = %q, want the handle rather than the role", out.Result.Remediation.Owner)
	}
	var seen bool
	for _, evidence := range out.Result.Evidence {
		if strings.Contains(evidence.Text, out.Ticket.Key) && evidence.URL == out.Ticket.URL {
			seen = true
		}
	}
	if !seen {
		t.Errorf("evidence = %v, want the ticket to watch", out.Result.Evidence)
	}
}

// TestTheTicketSaysWhyWhatAndHow is what makes a follow-up worth filing. The
// third part is the promise nobody will have to close it by hand.
func TestTheTicketSaysWhyWhatAndHow(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	out := w.reconcile(t, needsAHuman(), true)
	body := out.Ticket.Body

	for _, want := range []string{
		"the In progress column has no work-in-progress limit",
		"## What to do",
		"1. open the board's In progress column settings",
		"2. set the column limit to 2",
		"## What flow saw",
		"https://tracker.test/projects/1",
		"## How flow will know it is done",
		"tracker.wip",
		"Nobody needs to close it by hand.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the ticket body does not mention %q; it is:\n%s", want, body)
		}
	}
}

// TestNothingIsFiledWithoutFix is spec 02 §2: a plain `flow doctor` reports.
func TestNothingIsFiledWithoutFix(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	out := w.reconcile(t, needsAHuman(), false)

	if out.Action != engine.FollowUpFiled || !out.Would {
		t.Fatalf("action = %q would = %v, want it to say what it would have filed", out.Action, out.Would)
	}
	if out.Ticket.Key != "" {
		t.Errorf("a ticket was filed without --fix: %s", out.Ticket.Key)
	}
	if len(w.lock.FollowUps) != 0 {
		t.Errorf("lock = %+v, want nothing written", w.lock.FollowUps)
	}
	if out.Result.State != engine.StateExternal {
		t.Errorf("state = %q, want the report to say whose move it is either way", out.Result.State)
	}
}

// TestTheSameFollowUpIsNeverFiledTwice: doctor runs daily, and a check that
// stays broken for a month must produce one ticket rather than thirty.
func TestTheSameFollowUpIsNeverFiledTwice(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	first := w.reconcile(t, needsAHuman(), true)
	w.clock.Advance(24 * time.Hour)
	second := w.reconcile(t, needsAHuman(), true)

	if second.Action == engine.FollowUpFiled {
		t.Fatalf("filed a second ticket %s for the same check", second.Ticket.Key)
	}
	if second.Ticket.Key != first.Ticket.Key {
		t.Errorf("second round found %q, want the ticket already filed %q", second.Ticket.Key, first.Ticket.Key)
	}
	if second.Action != engine.FollowUpWaiting {
		t.Errorf("action = %q, want it to leave a day-old ticket alone", second.Action)
	}
	if len(w.lock.FollowUps) != 1 {
		t.Errorf("lock = %+v, want one record", w.lock.FollowUps)
	}
}

// TestAForgottenFollowUpIsFoundByItsMarker is the lost-lock recovery of spec
// 07 §4. A bad merge, or a fresh clone of a branch that never had the record,
// must not produce a duplicate: the marker is the memory that survives.
func TestAForgottenFollowUpIsFoundByItsMarker(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)

	// The lock loses everything, as it would in a fresh clone.
	w.lock = config.NewLock("v0.3.0")
	w.clock.Advance(30 * 24 * time.Hour)

	out := w.reconcile(t, needsAHuman(), true)
	if out.Action != engine.FollowUpAdopted {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpAdopted)
	}
	if out.Ticket.Key != filed.Ticket.Key {
		t.Errorf("adopted %q, want the ticket already on the board %q", out.Ticket.Key, filed.Ticket.Key)
	}
	if record := w.locked(t); record.Ticket != filed.Ticket.Key {
		t.Errorf("lock record = %+v, want the adopted ticket written back", record)
	}
	// Adoption does not chase: flow does not know how long it has really been
	// outstanding, and a comment on every fresh clone is noise.
	if len(w.tracker.Comments(filed.Ticket.Key)) != 0 {
		t.Errorf("comments = %v, want adoption to be silent", w.tracker.Comments(filed.Ticket.Key))
	}
}

// TestAnOutstandingFollowUpIsChasedWeekly: once a week is often enough to be
// noticed and rare enough to stay readable.
func TestAnOutstandingFollowUpIsChasedWeekly(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)

	w.clock.Advance(engine.NudgeInterval - time.Hour)
	if out := w.reconcile(t, needsAHuman(), true); out.Action != engine.FollowUpWaiting {
		t.Fatalf("action after six days = %q, want %q", out.Action, engine.FollowUpWaiting)
	}
	if got := len(w.tracker.Comments(filed.Ticket.Key)); got != 0 {
		t.Fatalf("comments after six days = %d, want none", got)
	}

	w.clock.Advance(2 * time.Hour)
	out := w.reconcile(t, needsAHuman(), true)
	if out.Action != engine.FollowUpNudged {
		t.Fatalf("action after a week = %q, want %q", out.Action, engine.FollowUpNudged)
	}
	comments := w.tracker.Comments(filed.Ticket.Key)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one chase", len(comments))
	}
	if !strings.Contains(comments[0].Body, "Still outstanding") {
		t.Errorf("comment = %q, want it to say the work is still owed", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, "7d") {
		t.Errorf("comment = %q, want it to say how long it has been", comments[0].Body)
	}
	if record := w.locked(t); record.NudgedAt.IsZero() {
		t.Error("the lock did not record the chase, so flow will chase again tomorrow")
	}
}

// TestChasingEditsOneCommentRatherThanGrowingAColumn: six weeks of nudges
// should be one comment that says "six weeks", not six comments.
func TestChasingEditsOneCommentRatherThanGrowingAColumn(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	for range 4 {
		w.clock.Advance(engine.NudgeInterval + time.Hour)
		if out := w.reconcile(t, needsAHuman(), true); out.Action != engine.FollowUpNudged {
			t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpNudged)
		}
	}

	comments := w.tracker.Comments(filed.Ticket.Key)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one, edited in place", len(comments))
	}
	if !strings.Contains(comments[0].Body, "28d") {
		t.Errorf("comment = %q, want it to say it has been four weeks", comments[0].Body)
	}
}

// TestAFollowUpIsClosedWhenFlowSeesTheWorkDone is the promise the ticket
// made. Verifying rather than trusting is the point: the ticket closes
// because flow looked, not because somebody said so.
func TestAFollowUpIsClosedWhenFlowSeesTheWorkDone(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)

	w.clock.Advance(3 * 24 * time.Hour)
	done := engine.Pass("In progress is limited to 2 per person").
		Saw("column limit: 2", "https://tracker.test/projects/1")
	out := w.reconcile(t, done, true)

	if out.Action != engine.FollowUpVerified {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpVerified)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !ticket.Closed {
		t.Fatal("the follow-up is still open after the work was verified")
	}
	if ticket.CloseReason != core.ClosedCompleted {
		t.Errorf("close reason = %q, want %q", ticket.CloseReason, core.ClosedCompleted)
	}
	comments := w.tracker.Comments(filed.Ticket.Key)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one saying what flow saw", len(comments))
	}
	if !strings.Contains(comments[0].Body, "flow verified this") {
		t.Errorf("comment = %q, want the close to carry its evidence", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, "column limit: 2") {
		t.Errorf("comment = %q, want it to say what it saw", comments[0].Body)
	}
	if record := w.locked(t); record.State != config.FollowUpClosed {
		t.Errorf("lock state = %q, want %q", record.State, config.FollowUpClosed)
	}
	if out.Result.State != engine.StatePass {
		t.Errorf("state = %q, want the check's own finding to stand", out.Result.State)
	}
}

// TestAFollowUpClosedWithoutTheWorkIsReopened: a closed follow-up that was
// never acted on is how a workflow quietly stops being true. Flow checked,
// and it is not done.
func TestAFollowUpClosedWithoutTheWorkIsReopened(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w.clock.Advance(24 * time.Hour)
	out := w.reconcile(t, needsAHuman(), true)

	if out.Action != engine.FollowUpReopened {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpReopened)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if ticket.Closed {
		t.Fatal("the follow-up is still closed")
	}
	if reason := w.tracker.ReopenReason(filed.Ticket.Key); !strings.Contains(reason, followUpCheck.ID) {
		t.Errorf("reopen reason = %q, want it to name the check", reason)
	}
	comments := w.tracker.Comments(filed.Ticket.Key)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one explaining the reopen", len(comments))
	}
	if !strings.Contains(comments[0].Body, "Reopened") {
		t.Errorf("comment = %q, want it to explain itself", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, "change the configuration so flow stops asking") {
		t.Errorf("comment = %q, want it to say how to stop being asked", comments[0].Body)
	}
}

// TestReopeningIsNotDoneWithoutFix: arguing with a human is a write, and a
// read-only run does not write.
func TestReopeningIsNotDoneWithoutFix(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedCompleted); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := w.reconcile(t, needsAHuman(), false)
	if out.Action != engine.FollowUpReopened || !out.Would {
		t.Fatalf("action = %q would = %v, want it reported rather than done", out.Action, out.Would)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !ticket.Closed {
		t.Error("the ticket was reopened without --fix")
	}
}

// TestAFollowUpNoLongerNeededIsWithdrawn: the check still does not hold, but
// it no longer wants a ticket — flow can do it itself now. The ask as written
// is wrong, so it is closed as not planned with a comment saying which.
func TestAFollowUpNoLongerNeededIsWithdrawn(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)

	w.clock.Advance(24 * time.Hour)
	nowAutomatic := engine.Fail("the In progress column has no work-in-progress limit").
		Fix(engine.Remediation{Kind: engine.RemedyAuto, Summary: "flow can set it", Command: "flow doctor --fix"})
	out := w.reconcile(t, nowAutomatic, true)

	if out.Action != engine.FollowUpWithdrawn {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpWithdrawn)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !ticket.Closed {
		t.Fatal("the withdrawn follow-up is still open")
	}
	if ticket.CloseReason != core.ClosedNotPlanned {
		t.Errorf("close reason = %q, want %q: the work was not done, it stopped being asked for",
			ticket.CloseReason, core.ClosedNotPlanned)
	}
	comments := w.tracker.Comments(filed.Ticket.Key)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "No longer needed") {
		t.Fatalf("comments = %v, want one saying why it was withdrawn", comments)
	}
	if !strings.Contains(comments[0].Body, "flow doctor --fix") {
		t.Errorf("comment = %q, want it to say what happens instead", comments[0].Body)
	}
	if out.Result.State != engine.StateFail {
		t.Errorf("state = %q, want the check's own finding, which still does not hold", out.Result.State)
	}
}

// TestAFollowUpAlreadyClosedAndVerifiedIsLeftAlone: doctor runs daily and
// must not comment on a finished ticket every morning. Editing it would email
// everybody who ever touched it.
func TestAFollowUpAlreadyClosedAndVerifiedIsLeftAlone(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	done := engine.Pass("In progress is limited to 2 per person")
	w.clock.Advance(24 * time.Hour)
	w.reconcile(t, done, true)

	before := w.tracker.Comments(filed.Ticket.Key)
	w.clock.Advance(24 * time.Hour)
	out := w.reconcile(t, done, true)

	if out.Action != engine.FollowUpNone {
		t.Fatalf("action = %q, want nothing to do", out.Action)
	}
	if after := w.tracker.Comments(filed.Ticket.Key); len(after) != len(before) {
		t.Errorf("comments went from %d to %d on a ticket already closed", len(before), len(after))
	}
}

// TestACheckThatHoldsAndNeverAskedForAnythingDoesNothing is the common case:
// most checks, most days.
func TestACheckThatHoldsAndNeverAskedForAnythingDoesNothing(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	out := w.reconcile(t, engine.Pass("the trunk is protected"), true)

	if out.Action != engine.FollowUpNone {
		t.Errorf("action = %q, want nothing to do", out.Action)
	}
	if len(w.lock.FollowUps) != 0 {
		t.Errorf("lock = %+v, want nothing written", w.lock.FollowUps)
	}
}

// TestALockRecordForATicketThatIsGoneIsDropped: the lock is a statement of
// what is true, not a changelog. A record for a deleted ticket that nobody
// wants any more is neither.
func TestALockRecordForATicketThatIsGoneIsDropped(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	w.lock.FollowUps = append(w.lock.FollowUps, config.FollowUp{
		Check:    followUpCheck.ID,
		Ticket:   "#404",
		Owner:    "@board-admin",
		OpenedAt: coretest.ContractNow.Add(-90 * 24 * time.Hour),
		State:    config.FollowUpOpen,
	})

	out := w.reconcile(t, engine.Pass("In progress is limited to 2 per person"), true)
	if out.Action != engine.FollowUpNone {
		t.Errorf("action = %q, want nothing to do about a ticket that is not there", out.Action)
	}
	if len(w.lock.FollowUps) != 0 {
		t.Errorf("lock = %+v, want the stale record dropped", w.lock.FollowUps)
	}
}

// TestAFollowUpIsFiledAgainIfTheTicketWasDeleted: the marker search is the
// authority, not the lock. Somebody deleting the ticket means the work is
// untracked again, and flow files it again.
func TestAFollowUpIsFiledAgainIfTheTicketWasDeleted(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	w.lock.FollowUps = append(w.lock.FollowUps, config.FollowUp{
		Check:    followUpCheck.ID,
		Ticket:   "#404",
		Owner:    "@board-admin",
		OpenedAt: coretest.ContractNow.Add(-90 * 24 * time.Hour),
		State:    config.FollowUpOpen,
	})

	out := w.reconcile(t, needsAHuman(), true)
	if out.Action != engine.FollowUpFiled {
		t.Fatalf("action = %q, want it filed again", out.Action)
	}
	if record := w.locked(t); record.Ticket != out.Ticket.Key {
		t.Errorf("lock record = %+v, want the new ticket", record)
	}
}

// TestOwnersAreRoutedByTheAccessTheyNeed: a column limit is for whoever
// administers the board, not for whoever ran doctor.
func TestOwnersAreRoutedByTheAccessTheyNeed(t *testing.T) {
	t.Parallel()

	cfg := followUpConfig(t)
	lock := config.NewLock("v0.3.0")
	for _, tc := range []struct{ role, want string }{
		{engine.OwnerTrackerAdmin, "@board-admin"},
		{engine.OwnerForgeAdmin, "@repo-admin"},
		{engine.OwnerPlatform, "@sre"},
		{engine.OwnerDefault, "@dev"},
		{"", "@dev"},
	} {
		if got := engine.Owner(cfg, lock, tc.role); got != tc.want {
			t.Errorf("Owner(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

// TestAnUnfilledRoleFallsBackToTheDefault: a team that named one person and
// no specialists still gets its follow-ups assigned.
func TestAnUnfilledRoleFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	cfg := preset(t, "lean-kanban")
	cfg.FollowUps.Owners.Default = config.Known("@dev")

	if got := engine.Owner(cfg, config.NewLock("v0.3.0"), engine.OwnerPlatform); got != "@dev" {
		t.Errorf("Owner(platform) = %q, want the default to catch it", got)
	}
}

// TestAnOwnerResolvedIntoTheLockIsUsed ties the follow-up lifecycle to
// placeholder resolution: `--set followups.owners.platform=@sre` is answered
// in the lock, and the next ticket goes to them.
func TestAnOwnerResolvedIntoTheLockIsUsed(t *testing.T) {
	t.Parallel()

	cfg := preset(t, "lean-kanban")
	lock := config.NewLock("v0.3.0")
	lock.Resolve("followups.owners.platform", "@sre")

	if got := engine.Owner(cfg, lock, engine.OwnerPlatform); got != "@sre" {
		t.Errorf("Owner(platform) = %q, want what doctor resolved", got)
	}
}

// TestNobodyIsBetterThanAPlaceholder: assigning a ticket to the literal
// string "${pending:owners.platform}" would fail against every real tracker,
// and an unassigned ticket in the right place is still useful.
func TestNobodyIsBetterThanAPlaceholder(t *testing.T) {
	t.Parallel()

	cfg := preset(t, "lean-kanban")
	if got := engine.Owner(cfg, config.NewLock("v0.3.0"), engine.OwnerPlatform); got != "" {
		t.Errorf("Owner(platform) = %q, want nobody rather than a placeholder", got)
	}
}

// TestWhoeverTookTheTicketKeepsIt: somebody reassigned it because they knew
// better than the configuration did, and taking it back every morning would
// be flow arguing with a human.
func TestWhoeverTookTheTicketKeepsIt(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)

	moved := filed.Ticket
	moved.Assignees = []string{"@priya"}
	w.tracker.SetTicket(moved)

	w.clock.Advance(engine.NudgeInterval + time.Hour)
	out := w.reconcile(t, needsAHuman(), true)

	if out.Result.Remediation.Owner != "@priya" {
		t.Errorf("owner = %q, want whoever actually has it", out.Result.Remediation.Owner)
	}
	if record := w.locked(t); record.Owner != "@priya" {
		t.Errorf("lock owner = %q, want it to follow the ticket", record.Owner)
	}
}

// TestATrackerThatWillNotAnswerLeavesTheFindingAlone: being unable to file a
// ticket about a problem is not a second problem to report in place of the
// first. The check's own finding is what the report says.
func TestATrackerThatWillNotAnswerLeavesTheFindingAlone(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	w.tracker.Fail("FindFollowUp", core.Errorf(core.ErrRateLimited, "search for the follow-up").
		WithWhy("the tracker is rate limiting"))

	out := engine.ReconcileFollowUp(t.Context(), engine.FollowUpInput{
		Check: followUpCheck, Result: needsAHuman(), Config: w.cfg, Lock: w.lock,
		Tracker: w.tracker, Repo: "acme/widget", Clock: w.clock, Fix: true,
	})

	if !errors.Is(out.Err, core.ErrRateLimited) {
		t.Fatalf("err = %v, want the tracker's own failure", out.Err)
	}
	if out.Result.State != engine.StateFail {
		t.Errorf("state = %q, want the check's own finding", out.Result.State)
	}
	if len(w.lock.FollowUps) != 0 {
		t.Errorf("lock = %+v, want nothing written", w.lock.FollowUps)
	}
}

// TestWithoutATrackerThereIsNowhereToFile: a repository whose tracker is
// misconfigured still gets its report, with the failures as failures.
func TestWithoutATrackerThereIsNowhereToFile(t *testing.T) {
	t.Parallel()

	out := engine.ReconcileFollowUp(t.Context(), engine.FollowUpInput{
		Check: followUpCheck, Result: needsAHuman(),
		Config: followUpConfig(t), Lock: config.NewLock("v0.3.0"), Fix: true,
	})
	if out.Action != engine.FollowUpNone {
		t.Errorf("action = %q, want nothing to do", out.Action)
	}
	if out.Result.State != engine.StateFail {
		t.Errorf("state = %q, want the failure to stay a failure", out.Result.State)
	}
}

// TestEveryCheckInAReportIsReconciled is how the lifecycle is actually
// driven: once per doctor run, over everything it found.
func TestEveryCheckInAReportIsReconciled(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	report := engine.Report{Checks: []engine.Checked{
		{ID: "tracker.wip", Title: "the in-progress column has a limit", Result: needsAHuman()},
		{ID: "forge.protection", Title: "the trunk is protected", Result: engine.Pass("it is")},
		{ID: "tracker.expedite", Title: "there is an expedite view", Result: needsAHuman()},
	}}

	outcomes := engine.ReconcileFollowUps(t.Context(), report, engine.FollowUpInput{
		Config: w.cfg, Lock: w.lock, Tracker: w.tracker,
		Repo: "acme/widget", Clock: w.clock, Fix: true,
	})

	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want one per check", len(outcomes))
	}
	want := []string{engine.FollowUpFiled, engine.FollowUpNone, engine.FollowUpFiled}
	for i, outcome := range outcomes {
		if outcome.Err != nil {
			t.Fatalf("%s: %v", outcome.Check, outcome.Err)
		}
		if outcome.Action != want[i] {
			t.Errorf("%s: action = %q, want %q", outcome.Check, outcome.Action, want[i])
		}
	}
	if len(w.lock.FollowUps) != 2 {
		t.Fatalf("lock = %+v, want two tickets recorded", w.lock.FollowUps)
	}
	// Two checks, two tickets: a marker carries the check id, so they do not
	// collide.
	if w.lock.FollowUps[0].Ticket == w.lock.FollowUps[1].Ticket {
		t.Errorf("both checks recorded ticket %s", w.lock.FollowUps[0].Ticket)
	}
}

// TestTheLockRecordsTheNudgeNotTheTracker: the chase comment is edited in
// place, so the tracker cannot say how long ago it was without reading every
// comment back. The lock is where that lives.
func TestTheLockRecordsTheNudgeNotTheTracker(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	w.reconcile(t, needsAHuman(), true)
	if record := w.locked(t); !record.NudgedAt.IsZero() {
		t.Errorf("nudged at %v on the day it was filed", record.NudgedAt)
	}

	w.clock.Advance(engine.NudgeInterval + time.Hour)
	w.reconcile(t, needsAHuman(), true)
	record := w.locked(t)
	if record.NudgedAt.IsZero() {
		t.Fatal("the chase was not recorded")
	}
	if !record.NudgedAt.Equal(w.clock.Now()) {
		t.Errorf("nudged at = %v, want now (%v)", record.NudgedAt, w.clock.Now())
	}
	if record.OpenedAt.Equal(record.NudgedAt) {
		t.Error("the filing date moved when the ticket was chased")
	}
}

// TestAFollowUpOnALockWithNoNudgeKeyRoundTrips: a follow-up nobody has had to
// chase yet carries no nudged_at, because a key whose value is "the zero
// time" is a line in a reviewed file that says nothing.
func TestAFollowUpOnALockWithNoNudgeKeyRoundTrips(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	w.reconcile(t, needsAHuman(), true)

	data, err := w.lock.Bytes()
	if err != nil {
		t.Fatalf("lock bytes: %v", err)
	}
	if strings.Contains(string(data), "nudged_at") {
		t.Errorf("the lock mentions nudged_at before anything was chased:\n%s", data)
	}

	w.clock.Advance(engine.NudgeInterval + time.Hour)
	w.reconcile(t, needsAHuman(), true)
	chased, err := w.lock.Bytes()
	if err != nil {
		t.Fatalf("lock bytes: %v", err)
	}
	if !strings.Contains(string(chased), "nudged_at") {
		t.Errorf("the lock does not record the chase:\n%s", chased)
	}
}
