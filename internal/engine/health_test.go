package engine_test

import (
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
)

// healthWorld is a tracker with nothing on it yet and a clock a test can wind
// a day at a time, which is the only way to say anything about a streak.
type healthWorld struct {
	tracker *coretest.FakeTracker
	clock   *coretest.FakeClock
	cfg     *config.Config
}

func newHealthWorld(t *testing.T) healthWorld {
	t.Helper()
	fixture := coretest.ContractTracker()
	fixture.Tickets = nil
	// Midday UTC, so that winding the clock by a day cannot change the date
	// by two in some other zone.
	clock := coretest.NewClock(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC))
	fixture.Clock = clock
	cfg := preset(t, "lean-kanban")
	cfg.Forge.Repo = "acme/widget"
	cfg.FollowUps.Labels = []string{"flow-followup"}
	return healthWorld{tracker: coretest.NewFakeTracker(fixture), clock: clock, cfg: cfg}
}

// publish is one morning's run.
func (w healthWorld) publish(t *testing.T, report engine.Report, fix bool) engine.HealthOutcome {
	t.Helper()
	return engine.PublishHealth(t.Context(), w.input(report, fix))
}

// input is the publication a test is about to make, so that a test wanting
// one field different does not have to restate the other seven.
func (w healthWorld) input(report engine.Report, fix bool) engine.HealthInput {
	return engine.HealthInput{
		Report:   report,
		Rendered: "### what the run found\n\nnothing much.\n",
		Config:   w.cfg,
		Tracker:  w.tracker,
		Repo:     "acme/widget",
		Clock:    w.clock,
		Fix:      fix,
	}
}

// day moves the world on to the next morning.
func (w healthWorld) day(n int) { w.clock.Advance(time.Duration(n) * 24 * time.Hour) }

// greenReport is a run that found nothing to do.
func greenReport() engine.Report {
	return engine.Report{Checks: []engine.Checked{
		{ID: "config.syntax", Title: "the configuration parses", Result: engine.Pass("it parses")},
	}}
}

// redReport is a run with one thing for the team to do, which is what makes
// it exit non-zero.
func redReport() engine.Report {
	return engine.Report{Checks: []engine.Checked{
		{ID: "config.syntax", Title: "the configuration parses", Result: engine.Pass("it parses")},
		{ID: "tracker.wip", Title: "the in-progress column has a limit", Result: needsAHuman()},
	}}
}

// healthTicket reads the one issue flow keeps.
func (w healthWorld) healthTicket(t *testing.T) *core.Ticket {
	t.Helper()
	found, err := w.tracker.FindFollowUp(t.Context(), engine.HealthMarker("acme/widget"))
	if err != nil {
		t.Fatalf("FindFollowUp: %v", err)
	}
	return found
}

// TestTheFirstMorningOpensTheIssue: there is nowhere to publish a report
// until flow has made somewhere, so the first run files it (spec 07 §10).
func TestTheFirstMorningOpensTheIssue(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	out := w.publish(t, greenReport(), true)

	if out.Err != nil {
		t.Fatalf("publish: %v", out.Err)
	}
	if out.Action != engine.HealthFiled {
		t.Errorf("action = %q, want %q", out.Action, engine.HealthFiled)
	}
	if out.Ticket.Key == "" {
		t.Fatal("the outcome names no ticket")
	}
	if out.Ticket.Title != "flow health: acme/widget" {
		t.Errorf("title = %q, which does not say which repository it is about", out.Ticket.Title)
	}
	// Without the marker tomorrow's run cannot find today's, and the pinned
	// issue becomes a new issue every morning.
	filed := w.healthTicket(t)
	if filed == nil {
		t.Fatal("the issue flow filed cannot be found by its marker")
	}
	if !strings.Contains(filed.Body, "what the run found") {
		t.Errorf("the body does not carry the report:\n%s", filed.Body)
	}
	if !slices.Contains(filed.Labels, "flow-followup") {
		t.Errorf("labels = %v, want the labels flow's own writings carry", filed.Labels)
	}
	if out.Streak != 1 {
		t.Errorf("streak = %d, want one green day", out.Streak)
	}
}

// TestASecondMorningRewritesTheSameIssue: the issue is updated in place
// (spec 07 §10), so a fortnight of runs is one page and not fourteen.
func TestASecondMorningRewritesTheSameIssue(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	first := w.publish(t, greenReport(), true)
	w.day(1)
	second := w.publish(t, greenReport(), true)

	if second.Action != engine.HealthUpdated {
		t.Errorf("action = %q, want %q", second.Action, engine.HealthUpdated)
	}
	if second.Ticket.Key != first.Ticket.Key {
		t.Errorf("the second run wrote to %s, the first to %s", second.Ticket.Key, first.Ticket.Key)
	}
	if calls := w.tracker.Calls("CreateFollowUp"); calls != 1 {
		t.Errorf("CreateFollowUp called %d times, want once for the life of the report", calls)
	}
	if len(second.Runs) != 2 {
		t.Fatalf("runs = %+v, want yesterday's kept and today's added", second.Runs)
	}
	if second.Runs[0].Day != "2026-03-03" || second.Runs[1].Day != "2026-03-02" {
		t.Errorf("runs = %q, %q, want newest first", second.Runs[0].Day, second.Runs[1].Day)
	}
	if second.Streak != 2 {
		t.Errorf("streak = %d, want two consecutive green days", second.Streak)
	}
}

// TestTheIssueIsTheRecord: the log survives flow forgetting everything, which
// is what makes a fourteen-day streak provable from a fresh clone (spec 00
// §3). Nothing but the issue carries yesterday.
func TestTheIssueIsTheRecord(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	for range 4 {
		w.publish(t, greenReport(), true)
		w.day(1)
	}
	// A different machine, a different process, nothing remembered.
	elsewhere := engine.PublishHealth(t.Context(), engine.HealthInput{
		Report: greenReport(), Rendered: "### elsewhere\n", Config: w.cfg,
		Tracker: w.tracker, Repo: "acme/widget", Clock: w.clock, Fix: true,
	})
	if elsewhere.Err != nil {
		t.Fatalf("publish: %v", elsewhere.Err)
	}
	if elsewhere.Streak != 5 {
		t.Errorf("streak = %d, want the four days the issue remembers plus today", elsewhere.Streak)
	}
}

// TestFourRunsInAnAfternoonAreOneDay: doctor runs daily but a person may run
// it repeatedly, and a log with four rows for Tuesday would make "fourteen
// consecutive days" mean four afternoons.
func TestFourRunsInAnAfternoonAreOneDay(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.clock.Advance(2 * time.Hour)
	w.publish(t, redReport(), true)
	w.clock.Advance(2 * time.Hour)
	out := w.publish(t, greenReport(), true)

	if len(out.Runs) != 1 {
		t.Fatalf("runs = %+v, want one row for the day", out.Runs)
	}
	// The last word on a day is the day's verdict: the afternoon's fix is
	// what the team ended on.
	if !out.Runs[0].Green() {
		t.Errorf("the day reads exit %d, want the run that finished it", out.Runs[0].Exit)
	}
	if out.Streak != 1 {
		t.Errorf("streak = %d, want one day", out.Streak)
	}
}

// TestARedMorningEndsTheStreak: the streak is the proof, so a day with
// something to do must cost it.
func TestARedMorningEndsTheStreak(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.day(1)
	w.publish(t, greenReport(), true)
	w.day(1)
	red := w.publish(t, redReport(), true)

	if red.Streak != 0 {
		t.Errorf("streak = %d after a red day, want none", red.Streak)
	}
	if red.Runs[0].Actions != 1 {
		t.Errorf("today's row records %d actions, want the one thing to do", red.Runs[0].Actions)
	}
	if !strings.Contains(red.Body, "not green") {
		t.Errorf("the issue does not say it is not green:\n%s", red.Body)
	}
	w.day(1)
	after := w.publish(t, greenReport(), true)
	if after.Streak != 1 {
		t.Errorf("streak = %d, want the count to start again", after.Streak)
	}
}

// TestADayNobodyLookedEndsTheStreak: fourteen consecutive daily runs is what
// spec 00 §3 asks for, and a fortnight with a Sunday missing is thirteen days
// of evidence and one day of not having looked.
func TestADayNobodyLookedEndsTheStreak(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.day(1)
	w.publish(t, greenReport(), true)
	w.day(3)
	out := w.publish(t, greenReport(), true)

	if out.Streak != 1 {
		t.Errorf("streak = %d, want the gap to have broken it", out.Streak)
	}
	if len(out.Runs) != 3 {
		t.Errorf("runs = %+v, want the earlier days kept even though they no longer count", out.Runs)
	}
}

// TestTheLogIsKeptToAMonth: the issue has to stay one screen, and a month
// still shows a fortnight's streak and the week before it.
func TestTheLogIsKeptToAMonth(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	var out engine.HealthOutcome
	for range engine.HealthRetention + 5 {
		out = w.publish(t, greenReport(), true)
		w.day(1)
	}
	if len(out.Runs) != engine.HealthRetention {
		t.Fatalf("runs = %d, want %d", len(out.Runs), engine.HealthRetention)
	}
	if out.Runs[0].Day != "2026-04-05" {
		t.Errorf("newest row = %q, want the last run", out.Runs[0].Day)
	}
	if out.Streak != engine.HealthRetention {
		t.Errorf("streak = %d, want every day the log still holds", out.Streak)
	}
}

// TestNothingIsWrittenWithoutFix: a preview reads the report off the screen,
// and spec 02 §2 keeps writing behind --fix.
func TestNothingIsWrittenWithoutFix(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	out := w.publish(t, greenReport(), false)

	if out.Action != engine.HealthNone {
		t.Errorf("action = %q, want %q", out.Action, engine.HealthNone)
	}
	if w.healthTicket(t) != nil {
		t.Error("a run without --fix filed an issue")
	}
	// Not even a read: the tracker is a network call, and a preview has
	// nothing to tell the reader that the screen has not told them already.
	if calls := w.tracker.Calls("FindFollowUp"); calls != 1 {
		t.Errorf("FindFollowUp called %d times, want only this test's own look", calls)
	}
}

// TestThereIsNowhereToPublish: a workflow with no tracker still gets a
// report, just not an issue to keep it in.
func TestThereIsNowhereToPublish(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	in := w.input(greenReport(), true)
	in.Tracker = nil
	out := engine.PublishHealth(t.Context(), in)

	if out.Action != engine.HealthNone {
		t.Errorf("action = %q, want %q", out.Action, engine.HealthNone)
	}
	if out.Err != nil {
		t.Errorf("having no tracker was reported as a failure: %v", out.Err)
	}
}

// TestANarrowedRunIsNotTheMorningReport: `--check 'config.*'` saw a fraction
// of the workflow. Publishing it would overwrite the morning's report and put
// a day in the streak that nobody audited.
func TestANarrowedRunIsNotTheMorningReport(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	before := w.healthTicket(t)
	w.day(1)

	in := w.input(greenReport(), true)
	in.Narrowed = true
	out := engine.PublishHealth(t.Context(), in)

	if out.Action != engine.HealthNone {
		t.Errorf("action = %q, want a partial run to publish nothing", out.Action)
	}
	after := w.healthTicket(t)
	if after.Body != before.Body {
		t.Errorf("a narrowed run rewrote the report:\n%s", after.Body)
	}
}

// TestATrackerThatWillNotTakeTheReportIsNotAFinding: the report has already
// said what is wrong with the workflow, and failing to file a copy of it must
// not become the run's verdict (spec 07 §4).
func TestATrackerThatWillNotTakeTheReportIsNotAFinding(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.tracker.Fail("CreateFollowUp", core.Errorf(core.ErrUnavailable, "file the report").
		WithWhy("the tracker is having a bad morning").WithFix("try again later"))
	out := w.publish(t, greenReport(), true)

	if out.Err == nil {
		t.Fatal("the refusal was swallowed; nothing would ever say the report is not being kept")
	}
	if !errors.Is(out.Err, core.ErrUnavailable) {
		t.Errorf("err = %v, want the tracker's own", out.Err)
	}
	if out.Action != engine.HealthNone {
		t.Errorf("action = %q, want nothing to have been done", out.Action)
	}
}

// TestAReadThatFailsDoesNotFileASecondIssue: not being able to look is not
// the same as having looked and found none, and the difference is a duplicate
// health issue every morning.
func TestAReadThatFailsDoesNotFileASecondIssue(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.day(1)
	w.tracker.FailOnce("FindFollowUp", core.Errorf(core.ErrUnavailable, "look for the report").
		WithWhy("the tracker is having a bad morning").WithFix("try again later"))
	out := w.publish(t, greenReport(), true)

	if out.Err == nil {
		t.Fatal("a read that failed was taken for an empty board")
	}
	if calls := w.tracker.Calls("CreateFollowUp"); calls != 1 {
		t.Errorf("CreateFollowUp called %d times, want the one issue there is", calls)
	}
}

// TestTheReportCarriesTheRunAndTheLog: somebody opening the issue has to see
// what the run found, not only that it was green (spec 07 §9).
func TestTheReportCarriesTheRunAndTheLog(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.day(1)
	out := w.publish(t, redReport(), true)

	for _, want := range []string{
		"## flow health",
		"`acme/widget`",
		"### what the run found",
		"### Daily runs",
		"| day | exit | for you | waiting | attestations |",
		"| 2026-03-03 | 2 | 1 | 0 | 0 |",
		"| 2026-03-02 | 0 | 0 | 0 | 0 |",
	} {
		if !strings.Contains(out.Body, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out.Body)
		}
	}
}

// TestTheLogIsReadBackFromWhatWasWritten: the round trip is the whole
// mechanism, so a body flow wrote must parse to the runs it wrote.
func TestTheLogIsReadBackFromWhatWasWritten(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)
	w.day(1)
	out := w.publish(t, redReport(), true)

	read := engine.ParseHealthRuns(out.Body)
	if len(read) != len(out.Runs) {
		t.Fatalf("read back %d runs, wrote %d", len(read), len(out.Runs))
	}
	for i := range read {
		if read[i] != out.Runs[i] {
			t.Errorf("row %d read back as %+v, wrote %+v", i, read[i], out.Runs[i])
		}
	}
}

// TestARowNobodyCanReadIsNotAGreenDay: a person who edited the table by hand
// has said something flow cannot interpret, and guessing at it would put a
// morning in the streak that never happened.
func TestARowNobodyCanReadIsNotAGreenDay(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"| day | exit | for you | waiting | attestations |",
		"|---|---|---|---|---|",
		"| 2026-03-03 | 0 | 0 | 0 | 0 |",
		"| yesterday | 0 | 0 | 0 | 0 |",
		"| 2026-03-01 | fine | 0 | 0 | 0 |",
		"| 2026-02-28 | 0 | 0 | 0 |",
		"| 2026-02-27 | 0 | 0 | 0 | 0 |",
		"not a row at all",
	}, "\n")

	runs := engine.ParseHealthRuns(body)
	want := []string{"2026-03-03", "2026-02-27"}
	if len(runs) != len(want) {
		t.Fatalf("read %+v, want only the rows that say something", runs)
	}
	for i, day := range want {
		if runs[i].Day != day {
			t.Errorf("row %d = %q, want %q", i, runs[i].Day, day)
		}
	}
	if streak := engine.HealthStreak(runs); streak != 1 {
		t.Errorf("streak = %d; a row nobody can read is a day nobody looked", streak)
	}
}

// TestAHandEditedLogDoesNotBecomeTomorrowsTruth: whatever a person leaves in
// the table, the next run's own row is the one it just made.
func TestAHandEditedLogDoesNotBecomeTomorrowsTruth(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	out := w.publish(t, greenReport(), true)
	if err := w.tracker.UpdateFollowUp(t.Context(), out.Ticket.Key, engine.HealthMarker("acme/widget"),
		engine.HealthTitle("acme/widget"),
		"| 2026-03-02 | 0 | 0 | 0 | 0 |\n| 2026-03-01 | 0 | 0 | 0 | 0 |\n"); err != nil {
		t.Fatalf("UpdateFollowUp: %v", err)
	}
	w.day(1)
	after := w.publish(t, redReport(), true)

	if after.Runs[0].Exit == 0 {
		t.Errorf("today reads exit 0, want the run that just happened")
	}
	if after.Streak != 0 {
		t.Errorf("streak = %d, want a red morning to count for nothing", after.Streak)
	}
}

// TestTheTitleSaysWhichRepository: one tracker serves many repositories, and
// two reports called the same thing cannot both be found.
func TestTheTitleSaysWhichRepository(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	w.publish(t, greenReport(), true)

	in := w.input(greenReport(), true)
	in.Repo = "acme/other"
	other := engine.PublishHealth(t.Context(), in)

	if other.Action != engine.HealthFiled {
		t.Errorf("action = %q, want a second repository to get its own issue", other.Action)
	}
	if other.Ticket.Title == engine.HealthTitle("acme/widget") {
		t.Errorf("both repositories' reports are called %q", other.Ticket.Title)
	}
	if other.Streak != 1 {
		t.Errorf("streak = %d, want the other repository's days not to count here", other.Streak)
	}
}

// TestEveryDayOfAFortnightCounts is the acceptance arithmetic of spec 00 §3
// done the long way, so that an off-by-one in the streak cannot pass for a
// completed goal.
func TestEveryDayOfAFortnightCounts(t *testing.T) {
	t.Parallel()

	w := newHealthWorld(t)
	for day := 1; day <= 14; day++ {
		out := w.publish(t, greenReport(), true)
		if out.Streak != day {
			t.Fatalf("day %d of the fortnight reads a streak of %d", day, out.Streak)
		}
		if want := fmt.Sprintf("green for %d consecutive day", day); !strings.Contains(out.Body, want) {
			t.Errorf("the issue does not say %q:\n%s", want, out.Body)
		}
		w.day(1)
	}
}
