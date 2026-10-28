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

// The person a ticket is assigned to has flow's words for the thing and not
// the provider's. `create deploys.records "production"` is an item id and the
// name of one of its entries, and a reader holding neither is being asked to
// go and find out what they have been handed. What the thing is and where it
// lives come from the adapter's declaration (spec 05 §1) and lead, because a
// list of instructions is no use to somebody who does not yet know what they
// are being asked to look at.
func TestTheTicketSaysWhatTheThingIsBeforeItSaysWhatToDo(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	finding := needsAHuman()
	finding.Remediation.Subject = "A cap on how many issues may sit in In progress at once."
	finding.Remediation.Venue = "A setting on the board's own view, not on acme/widget."
	body := w.reconcile(t, finding, true).Ticket.Body

	what := strings.Index(body, "## What this is")
	todo := strings.Index(body, "## What to do")
	switch {
	case what < 0:
		t.Fatalf("the ticket never says what the thing is:\n%s", body)
	case todo < 0:
		t.Fatalf("the ticket never says what to do:\n%s", body)
	case what > todo:
		t.Errorf("the ticket says what to do before it says what the thing is:\n%s", body)
	}
	for _, want := range []string{finding.Remediation.Subject, finding.Remediation.Venue} {
		if !strings.Contains(body, want) {
			t.Errorf("the ticket body does not carry %q; it is:\n%s", want, body)
		}
	}
}

// Half a declaration is still better than none: a provider that says what
// the thing is and has nowhere to point at is not made clearer by a heading
// with a blank line under it.
func TestATicketWithOnlyHalfADeclarationSaysTheHalfItHas(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		subject string
		venue   string
	}{
		{name: "only what it is", subject: "A cap on how many issues may sit in In progress."},
		{name: "only where it lives", venue: "A setting on the board's own view."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			w := newFollowUpWorld(t)
			finding := needsAHuman()
			finding.Remediation.Subject = test.subject
			finding.Remediation.Venue = test.venue
			body := w.reconcile(t, finding, true).Ticket.Body

			if !strings.Contains(body, "## What this is") {
				t.Fatalf("the ticket never says what the thing is:\n%s", body)
			}
			said := test.subject + test.venue
			if !strings.Contains(body, said) {
				t.Errorf("the ticket body does not carry %q; it is:\n%s", said, body)
			}
			if strings.Contains(body, "## What this is\n\n\n") {
				t.Errorf("the heading has nothing under it:\n%s", body)
			}
		})
	}
}

// Not every finding comes from a declaration — a live audit is flow's own
// reading of the team's week, and there is no provider to speak for it. The
// remedy's summary is all there is, and it carried the ticket before any of
// this; losing it would take the "why" out of every check that is not about
// a capability.
func TestWithNobodyToSpeakForTheProviderTheRemedyStillSaysWhy(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	finding := needsAHuman()
	body := w.reconcile(t, finding, true).Ticket.Body

	if strings.Contains(body, "## What this is") {
		t.Errorf("the ticket announced a declaration nobody wrote:\n%s", body)
	}
	if !strings.Contains(body, finding.Remediation.Summary) {
		t.Errorf("the ticket body does not carry %q; it is:\n%s", finding.Remediation.Summary, body)
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
	if len(w.tracker.Written(filed.Ticket.Key)) != 0 {
		t.Errorf("comments = %v, want adoption to be silent", w.tracker.Written(filed.Ticket.Key))
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
	if got := len(w.tracker.Written(filed.Ticket.Key)); got != 0 {
		t.Fatalf("comments after six days = %d, want none", got)
	}

	w.clock.Advance(2 * time.Hour)
	out := w.reconcile(t, needsAHuman(), true)
	if out.Action != engine.FollowUpNudged {
		t.Fatalf("action after a week = %q, want %q", out.Action, engine.FollowUpNudged)
	}
	comments := w.tracker.Written(filed.Ticket.Key)
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

	comments := w.tracker.Written(filed.Ticket.Key)
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
	comments := w.tracker.Written(filed.Ticket.Key)
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
	comments := w.tracker.Written(filed.Ticket.Key)
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want one explaining the reopen", len(comments))
	}
	if !strings.Contains(comments[0].Body, "Reopened") {
		t.Errorf("comment = %q, want it to explain itself", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, "close this as **not planned**") {
		t.Errorf("comment = %q, want it to say how to decline it instead", comments[0].Body)
	}
}

// TestAFollowUpDeclinedIsLeftClosed is issue #265. Closed as not planned is a
// person's answer, and reopening it is flow arguing with them. Flow leaves it
// closed, says once on the ticket how to write the decision into the
// configuration, and the check stays a warning — declined, not yet written
// down — rather than a pass, because the configuration still asks for it.
func TestAFollowUpDeclinedIsLeftClosed(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w.clock.Advance(24 * time.Hour)
	out := w.reconcile(t, needsAHuman(), true)

	if out.Action != engine.FollowUpDeclined {
		t.Fatalf("action = %q, want %q", out.Action, engine.FollowUpDeclined)
	}
	ticket, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("Ticket: %v", err)
	}
	if !ticket.Closed {
		t.Fatal("a declined follow-up was reopened")
	}
	if out.Result.State != engine.StateWarn || !strings.Contains(out.Result.Message, "declined, not yet written down") {
		t.Errorf("result = %q %q, want a warning that it was declined", out.Result.State, out.Result.Message)
	}
	if !strings.Contains(strings.Join(out.Result.Remediation.Steps, " "), ".flow.yaml") {
		t.Errorf("the remedy does not say where to write the decision: %+v", out.Result.Remediation)
	}
	if got := w.locked(t).State; got != config.FollowUpDeclined {
		t.Errorf("lock state = %q, want %q", got, config.FollowUpDeclined)
	}
	comments := w.tracker.Written(filed.Ticket.Key)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "Left closed") {
		t.Fatalf("comments = %+v, want one saying why it was left closed", comments)
	}
	said := comments[0].UpdatedAt

	// The next run says nothing more, and the check is still a warning.
	w.clock.Advance(24 * time.Hour)
	again := w.reconcile(t, needsAHuman(), true)
	if again.Action != engine.FollowUpDeclined || again.Would || again.Result.State != engine.StateWarn {
		t.Errorf("the next run did %q (would %v) and reported %q, want it still declined, nothing to do, and a warning", again.Action, again.Would, again.Result.State)
	}
	report := engine.Report{Checks: []engine.Checked{{ID: followUpCheck.ID, Result: needsAHuman()}}}
	report.Absorb([]engine.FollowUpOutcome{again})
	if report.Checks[0].Result.State != engine.StateWarn {
		t.Errorf("the report reads %q after a quiet run, want the decline to stand", report.Checks[0].Result.State)
	}
	if comments := w.tracker.Written(filed.Ticket.Key); len(comments) != 1 || !comments[0].UpdatedAt.Equal(said) {
		t.Errorf("the explanation was written again: %+v", comments)
	}
}

// Once the configuration stops asking for the work, the decision is written
// down: the warning goes, the ticket stays closed and untouched, and the lock
// stops calling it declined.
func TestADeclineWrittenIntoTheConfigurationIsSettled(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w.reconcile(t, needsAHuman(), true)

	out := w.reconcile(t, engine.Skip("this configuration does not ask for it"), true)

	if out.Result.State != engine.StateSkip {
		t.Errorf("state = %q, want the check's own answer once nothing is asked", out.Result.State)
	}
	if got := w.locked(t).State; got != config.FollowUpClosed {
		t.Errorf("lock state = %q, want %q", got, config.FollowUpClosed)
	}
	if comments := w.tracker.Written(filed.Ticket.Key); len(comments) != 1 {
		t.Errorf("comments = %d, want only the one explaining the decline", len(comments))
	}
}

// Declining is answered in writing, and a read-only run does not write.
func TestADeclinedFollowUpIsNotAnsweredWithoutFix(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := w.reconcile(t, needsAHuman(), false)
	if out.Action != engine.FollowUpDeclined || !out.Would || out.Result.State != engine.StateWarn {
		t.Fatalf("action = %q would = %v state = %q, want it reported as a warning rather than done", out.Action, out.Would, out.Result.State)
	}
	if comments := w.tracker.Written(filed.Ticket.Key); len(comments) != 0 {
		t.Errorf("a read-only run commented: %+v", comments)
	}
}

// Flow closes a follow-up as not planned itself when the work stops being
// needed. That is housekeeping, not a person's decision: if the configuration
// asks for the work again, the ticket is reopened as before.
func TestAFollowUpFlowWithdrewIsReopenedWhenWantedAgain(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i := range w.lock.FollowUps {
		w.lock.FollowUps[i].State = config.FollowUpClosed
	}

	out := w.reconcile(t, needsAHuman(), true)
	if out.Action != engine.FollowUpReopened {
		t.Fatalf("action = %q, want %q: flow closed it, nobody declined it", out.Action, engine.FollowUpReopened)
	}
}

// Somebody who declined a follow-up and then reopens it wants the work after
// all: it is outstanding again, and counted as such.
func TestAReopenedDeclineIsOutstandingAgain(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Close(t.Context(), filed.Ticket.Key, core.ClosedNotPlanned); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w.reconcile(t, needsAHuman(), true)
	if err := w.tracker.Reopen(t.Context(), filed.Ticket.Key, "we will do it after all"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	out := w.reconcile(t, needsAHuman(), true)
	if out.Result.State != engine.StateExternal {
		t.Errorf("state = %q, want it waiting on somebody again", out.Result.State)
	}
	if got := w.locked(t).State; got != config.FollowUpOpen {
		t.Errorf("lock state = %q, want %q", got, config.FollowUpOpen)
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
	comments := w.tracker.Written(filed.Ticket.Key)
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

	before := w.tracker.Written(filed.Ticket.Key)
	w.clock.Advance(24 * time.Hour)
	out := w.reconcile(t, done, true)

	if out.Action != engine.FollowUpNone {
		t.Fatalf("action = %q, want nothing to do", out.Action)
	}
	if after := w.tracker.Written(filed.Ticket.Key); len(after) != len(before) {
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

// knowsBetter is the same finding, said by a build that can now say what to
// do about it. Flow's own writing goes stale: a ticket filed when the
// remediation had no steps keeps "flow has no steps to offer for this one"
// behind a report that now offers two.
func knowsBetter() engine.Result {
	return engine.Fail("the In progress column has no work-in-progress limit").
		Saw("column limits are per-view and UI-only in github-projects", "https://tracker.test/projects/1").
		Fix(engine.Remediation{
			Kind:    engine.RemedyFollowUp,
			Summary: "the in-progress column has a limit — needs tracker admin",
			Owner:   engine.OwnerTrackerAdmin,
			Steps: []string{
				"open the board's In progress column settings",
				"set the column limit to 2",
			},
		})
}

// TestAFollowUpIsBroughtUpToDate: the ticket is flow's own writing, so when
// flow has something better to say it says it in the ticket that is already
// there rather than leaving a stale one behind a correct report.
func TestAFollowUpIsBroughtUpToDate(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	before, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}

	out := w.reconcile(t, knowsBetter(), true)
	if out.Action != engine.FollowUpRefreshed {
		t.Errorf("action = %q, want %q", out.Action, engine.FollowUpRefreshed)
	}
	after, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if after.Key != before.Key {
		t.Errorf("flow filed %s instead of rewriting %s", after.Key, before.Key)
	}
	if after.Title == before.Title {
		t.Errorf("the title is still %q, which is what every such ticket was called", after.Title)
	}
	if !strings.Contains(after.Title, "needs tracker admin") {
		t.Errorf("the title does not say what is wanted: %q", after.Title)
	}
	if !strings.Contains(after.Body, "set the column limit to 2") {
		t.Errorf("the body does not carry the steps flow now has:\n%s", after.Body)
	}
	if strings.Contains(after.Body, "no steps to offer") {
		t.Errorf("the body still says flow has nothing to suggest:\n%s", after.Body)
	}
	// Rewriting is not chasing. A nudge asks somebody to act and is rationed
	// to one a week; correcting flow's own writing costs the reader nothing.
	if record := w.locked(t); !record.NudgedAt.IsZero() {
		t.Errorf("the refresh was recorded as a chase, at %v", record.NudgedAt)
	}
	if comments := w.tracker.Written(filed.Ticket.Key); len(comments) != 0 {
		t.Errorf("the refresh commented on the ticket: %+v", comments)
	}
}

// TestASteadyFindingIsNotRewrittenEveryMorning: doctor runs daily, and a
// ticket whose "last updated" moved every morning would look like news every
// morning and teach the team to ignore it.
func TestASteadyFindingIsNotRewrittenEveryMorning(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	before, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	w.clock.Advance(24 * time.Hour)

	out := w.reconcile(t, needsAHuman(), true)
	if out.Action == engine.FollowUpRefreshed {
		t.Error("flow rewrote a ticket that already said exactly this")
	}
	after, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updated at %v then %v: an unchanged finding moved the date",
			before.UpdatedAt, after.UpdatedAt)
	}
}

// TestNothingIsRewrittenWithoutFix: spec 02 §2 keeps every write behind
// --fix, and a preview that edited a ticket would be a preview that changed
// the world.
func TestNothingIsRewrittenWithoutFix(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, needsAHuman(), true)
	before, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}

	out := w.reconcile(t, knowsBetter(), false)
	if out.Action == engine.FollowUpRefreshed {
		t.Error("a run without --fix rewrote the ticket")
	}
	after, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if after.Title != before.Title || after.Body != before.Body {
		t.Error("a run without --fix rewrote the ticket")
	}
}

// wipFinding is the same finding every time, said under whichever summary
// the build that found it had to offer.
func wipFinding(summary string) engine.Result {
	return engine.Fail("the In progress column has no work-in-progress limit").
		Saw("column limits are per-view and UI-only in github-projects", "https://tracker.test/projects/1").
		Fix(engine.Remediation{
			Kind:    engine.RemedyFollowUp,
			Summary: summary,
			Owner:   engine.OwnerTrackerAdmin,
			Steps: []string{
				"open the board's In progress column settings",
				"set the column limit to 2",
			},
		})
}

// TestATitleThatImprovedIsRewrittenEvenThoughTheBodyDidNot: the body is not a
// proxy for the title. A summary that only repeats the finding is left out of
// the body, so a run that acquires one changes the title and nothing else —
// and the title is the whole of what a person reads in the issue list.
func TestATitleThatImprovedIsRewrittenEvenThoughTheBodyDidNot(t *testing.T) {
	t.Parallel()

	w := newFollowUpWorld(t)
	filed := w.reconcile(t, wipFinding(""), true)
	before, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}

	out := w.reconcile(t, wipFinding("the In progress column has no work-in-progress limit"), true)
	if out.Action != engine.FollowUpRefreshed {
		t.Errorf("action = %q, want %q", out.Action, engine.FollowUpRefreshed)
	}
	after, err := w.tracker.Ticket(t.Context(), filed.Ticket.Key)
	if err != nil {
		t.Fatalf("read the ticket back: %v", err)
	}
	if after.Body != before.Body {
		t.Fatalf("the body moved too, so this proves nothing about the title:\n%s", after.Body)
	}
	if after.Title == before.Title {
		t.Errorf("the title is still %q, which is the one the ticket was filed under", after.Title)
	}
}

// TestOneFactHasOneFollowUp is issue #202. apply filed the deployment history
// under the item id and doctor under its check id, so one repository held two
// tickets about one thing. apply now files under the check, and doctor closes
// the one an earlier flow left under the item, saying which ticket stands.
func TestOneFactHasOneFollowUp(t *testing.T) {
	t.Parallel()

	if got := engine.CheckOf(core.ItemDeploysRecords); got != engine.CheckDeploysSource {
		t.Fatalf("CheckOf(%s) = %s, want %s", core.ItemDeploysRecords, got, engine.CheckDeploysSource)
	}
	if got := engine.CheckOf(core.ItemForgeProtection); got != core.ItemForgeProtection {
		t.Errorf("CheckOf(%s) = %s, want it unchanged", core.ItemForgeProtection, got)
	}

	w := newFollowUpWorld(t)
	formerMarker := engine.FollowUpMarker(core.ItemDeploysRecords, "acme/widget")
	former, err := w.tracker.CreateFollowUp(t.Context(), core.FollowUp{Marker: formerMarker, Title: "flow: make deploys.records true by hand"})
	if err != nil {
		t.Fatal(err)
	}
	w.lock.FollowUps = append(w.lock.FollowUps, config.FollowUp{Check: core.ItemDeploysRecords, Ticket: former.Key, State: config.FollowUpOpen})

	out := engine.ReconcileFollowUp(t.Context(), engine.FollowUpInput{
		Check:   engine.Check{ID: engine.CheckDeploysSource, Title: "Flow can read the deployment history", Category: engine.CategoryDeploys},
		Result:  needsAHuman(),
		Config:  w.cfg,
		Lock:    w.lock,
		Tracker: w.tracker,
		Repo:    "acme/widget",
		Clock:   w.clock,
		Fix:     true,
	})
	if out.Err != nil {
		t.Fatalf("reconcile: %v", out.Err)
	}
	if out.Ticket.Key == "" || out.Ticket.Key == former.Key {
		t.Fatalf("the check's ticket is %q; want one of its own beside %s", out.Ticket.Key, former.Key)
	}
	stale, err := w.tracker.FindFollowUp(t.Context(), formerMarker)
	if err != nil || stale == nil {
		t.Fatalf("the former ticket is gone: %v", err)
	}
	if !stale.Closed {
		t.Errorf("%s is still open beside %s", former.Key, out.Ticket.Key)
	}
	for _, f := range w.lock.FollowUps {
		if f.Check == core.ItemDeploysRecords {
			t.Errorf("the lock still records a follow-up under %s: %+v", core.ItemDeploysRecords, f)
		}
	}
	if calls := w.tracker.Calls("Comment"); calls == 0 {
		t.Errorf("%s was closed without saying which ticket stands for it", former.Key)
	}
}
