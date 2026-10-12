package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The pinned health report (spec 07 §10).
//
// A daily doctor run that only printed to a CI log would be a run nobody
// reads. The report therefore goes where the team already is: one issue in
// the tracker, found by its marker and rewritten in place, so that a
// fortnight of green mornings is one page somebody can open rather than
// fourteen notifications they have muted.
//
// Rewriting in place costs the history, and the history is the point — spec
// 00 §3 asks for fourteen consecutive green days. So the report carries its
// own log: each run reads back the table the last one wrote, adds a row for
// today, and keeps the most recent [HealthRetention] days. The issue is the
// record, which is what makes the proof survive a fresh clone, a lost lock
// and a CI runner that keeps nothing.

// HealthRetention is how many days of runs the report keeps. A month is long
// enough to show a fourteen-day streak and the week that preceded it, and
// short enough that the issue stays one screen.
const HealthRetention = 30

// healthDay is how a run's day is written. It sorts lexically in date order,
// which is what lets the log be kept in order without parsing every row.
const healthDay = "2006-01-02"

// What a round of publication did.
const (
	// HealthFiled means the issue was opened.
	HealthFiled = "filed"
	// HealthUpdated means the issue was rewritten with today's run.
	HealthUpdated = "updated"
	// HealthNone means there was nothing to publish to.
	HealthNone = "none"
)

// HealthRun is one doctor run as the report records it: the day it ran, what
// the shell saw, and the three numbers of the summary line (spec 07 §9).
type HealthRun struct {
	// Day is the UTC day it ran, as yyyy-mm-dd.
	Day string
	// Exit is the code the run exited with.
	Exit int
	// Actions is how much was the team's own to do.
	Actions int
	// Waiting is how much was waiting on somebody else.
	Waiting int
	// Attestations is how many promises were due.
	Attestations int
}

// Green reports whether the run left nothing to do.
func (r HealthRun) Green() bool { return r.Exit == 0 }

// HealthInput is one publication of the report.
type HealthInput struct {
	// Report is the run being published.
	Report Report
	// Rendered is the report as markdown. The engine does not render it:
	// the shape a document takes on a web page belongs to internal/ui, and
	// a second markdown here would be a second answer (spec 01 §2).
	Rendered string
	// Config says what to label flow's own writings.
	Config *config.Config
	// Tracker is where the issue lives. Nil means there is nowhere to
	// publish.
	Tracker core.Tracker
	// Repo is the repository the report is about: one tracker serves many,
	// so it is in the title and in the marker.
	Repo string
	// Clock is where today comes from.
	Clock core.Clock
	// Fix permits flow to write to the tracker.
	Fix bool
	// Narrowed reports that the run answered only some of the questions,
	// because --check named a subset.
	Narrowed bool
	// Log receives one line per publication.
	Log *slog.Logger
}

// HealthOutcome is what became of the report.
type HealthOutcome struct {
	// Action is what was done, one of the Health constants.
	Action string
	// Ticket is the issue as the tracker now has it, zero when there is
	// none.
	Ticket core.Ticket
	// Runs is the log the issue now carries, newest first.
	Runs []HealthRun
	// Streak is how many consecutive days up to and including the latest run
	// exited 0.
	Streak int
	// Body is what was written, for a test and for the log.
	Body string
	// Err is why the report could not be published.
	Err error
}

// HealthMarker identifies the one issue flow keeps its report in.
func HealthMarker(repo string) core.Marker {
	return core.NewMarker(core.MarkerHealth, "repo", repo)
}

// HealthTitle is the issue's one line.
func HealthTitle(repo string) string {
	if repo == "" {
		return "flow health"
	}
	return "flow health: " + repo
}

// PublishHealth writes today's run into the pinned health issue, opening it
// the first time and rewriting it in place every day after (spec 07 §10).
//
// It never fails the run. The report has already been made and already said
// what is wrong; a tracker that will not take a copy of it is not a finding
// about the workflow, and reporting it in place of what the run actually
// found would be the one thing a health report must not do (spec 07 §4).
func PublishHealth(ctx context.Context, in HealthInput) HealthOutcome {
	if in.Log == nil {
		in.Log = slog.New(slog.DiscardHandler)
	}
	out := HealthOutcome{Action: HealthNone}
	// Nothing is written without leave, and nothing is read either: without
	// --fix the report is already on the reader's screen, which is the whole
	// of what a preview could tell them (spec 02 §2).
	if in.Tracker == nil || !in.Fix {
		return out
	}
	// A run that was asked only about `config.*` saw a fraction of the
	// workflow. Publishing it would overwrite a whole morning's report with
	// a partial one, and the streak would record a day nobody audited.
	if in.Narrowed {
		return out
	}

	marker := HealthMarker(in.Repo)
	found, err := in.Tracker.FindFollowUp(ctx, marker)
	if err != nil {
		out.Err = err
		return out
	}

	previous := []HealthRun(nil)
	if found != nil {
		previous = ParseHealthRuns(found.Body)
	}
	out.Runs = recordRun(previous, in.run())
	out.Streak = HealthStreak(out.Runs)
	out.Body = healthBody(in.Repo, out.Runs, out.Streak, in.Rendered)

	if found == nil {
		ticket, err := in.Tracker.CreateFollowUp(ctx, core.FollowUp{
			Marker: marker,
			Title:  HealthTitle(in.Repo),
			Body:   out.Body,
			Labels: in.labels(),
		})
		if err != nil {
			out.Err = err
			return out
		}
		out.Action, out.Ticket = HealthFiled, ticket
		in.Log.Info("flow opened the health report", "ticket", ticket.Key, "streak", out.Streak)
		return out
	}

	if err := in.Tracker.UpdateFollowUp(ctx, found.Key, marker, HealthTitle(in.Repo), out.Body); err != nil {
		out.Err = err
		out.Ticket = *found
		return out
	}
	out.Action, out.Ticket = HealthUpdated, *found
	in.Log.Info("flow rewrote the health report", "ticket", found.Key, "streak", out.Streak)
	return out
}

// run is today's row.
func (in HealthInput) run() HealthRun {
	now := time.Now()
	if in.Clock != nil {
		now = in.Clock.Now()
	}
	return HealthRun{
		Day:          now.UTC().Format(healthDay),
		Exit:         in.Report.Exit(),
		Actions:      in.Report.Actions(),
		Waiting:      in.Report.Waiting(),
		Attestations: in.Report.Attestations(),
	}
}

// labels are what flow's own writings carry, so a team can find them all.
// The health issue takes the follow-ups' labels because it is the same kind
// of thing — something flow opened and keeps — and a second label nobody
// configured would be a second thing to explain.
func (in HealthInput) labels() []string {
	if in.Config == nil {
		return nil
	}
	return in.Config.FollowUps.Labels
}

// recordRun folds today's run into the log: newest first, one row per day,
// and no more than [HealthRetention] of them.
//
// A day already in the log is replaced rather than appended to. Doctor runs
// daily but a person may run it four times in an afternoon, and a log with
// four rows for Tuesday would make "fourteen consecutive days" mean nothing.
func recordRun(previous []HealthRun, today HealthRun) []HealthRun {
	runs := make([]HealthRun, 0, len(previous)+1)
	runs = append(runs, today)
	for _, run := range previous {
		if run.Day != today.Day {
			runs = append(runs, run)
		}
	}
	slices.SortStableFunc(runs, func(a, b HealthRun) int { return strings.Compare(b.Day, a.Day) })
	if len(runs) > HealthRetention {
		runs = runs[:HealthRetention]
	}
	return runs
}

// HealthStreak is how many consecutive days, ending at the most recent run,
// exited 0.
//
// A day with no run breaks it. Spec 00 §3 asks for fourteen consecutive daily
// runs, and a fortnight with a Sunday missing is thirteen days of evidence
// and one day of not having looked — which is not the same as green.
func HealthStreak(runs []HealthRun) int {
	streak := 0
	var last time.Time
	for i, run := range runs {
		day, err := time.Parse(healthDay, run.Day)
		if err != nil || !run.Green() {
			break
		}
		if i > 0 && !day.Equal(last.AddDate(0, 0, -1)) {
			break
		}
		streak, last = streak+1, day
	}
	return streak
}

// healthBody is the whole issue: what it is, how the streak stands, the run
// that has just finished, and the log behind it.
func healthBody(repo string, runs []HealthRun, streak int, rendered string) string {
	var out strings.Builder
	out.WriteString("## flow health\n\n")
	if repo != "" {
		fmt.Fprintf(&out, "`%s` · ", repo)
	}
	out.WriteString(healthHeadline(runs, streak))
	out.WriteString("\n\n")
	out.WriteString(strings.TrimRight(rendered, "\n"))
	out.WriteString("\n\n### Daily runs\n\n")
	out.WriteString("| day | exit | for you | waiting | attestations |\n|---|---|---|---|---|\n")
	for _, run := range runs {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %d |\n",
			run.Day, run.Exit, run.Actions, run.Waiting, run.Attestations)
	}
	return out.String()
}

// healthHeadline is the one sentence somebody reads from the issue list.
func healthHeadline(runs []HealthRun, streak int) string {
	if len(runs) == 0 {
		return "no run has been recorded yet"
	}
	latest := runs[0]
	if streak == 0 {
		return fmt.Sprintf("not green: %s exited %d", latest.Day, latest.Exit)
	}
	return fmt.Sprintf("green for %d consecutive %s, to %s", streak, plural(streak, "day"), latest.Day)
}

// ParseHealthRuns reads the run log back out of a report flow wrote before.
//
// The issue is the record, so the record has to survive being read by the
// next run. A row that does not parse is dropped rather than guessed at: a
// person who edited the table by hand has said something flow cannot
// interpret, and inventing a day for it would put a green morning in the
// streak that never happened.
func ParseHealthRuns(body string) []HealthRun {
	var runs []HealthRun
	for line := range strings.Lines(body) {
		run, ok := parseHealthRow(line)
		if !ok {
			continue
		}
		runs = append(runs, run)
	}
	slices.SortStableFunc(runs, func(a, b HealthRun) int { return strings.Compare(b.Day, a.Day) })
	return runs
}

// parseHealthRow reads one row of the log table.
func parseHealthRow(line string) (HealthRun, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "|") || !strings.HasSuffix(text, "|") {
		return HealthRun{}, false
	}
	cells := strings.Split(strings.Trim(text, "|"), "|")
	if len(cells) != 5 {
		return HealthRun{}, false
	}
	day := strings.TrimSpace(cells[0])
	if _, err := time.Parse(healthDay, day); err != nil {
		return HealthRun{}, false
	}
	numbers := make([]int, 4)
	for i, cell := range cells[1:] {
		value, err := strconv.Atoi(strings.TrimSpace(cell))
		if err != nil {
			return HealthRun{}, false
		}
		numbers[i] = value
	}
	return HealthRun{
		Day:          day,
		Exit:         numbers[0],
		Actions:      numbers[1],
		Waiting:      numbers[2],
		Attestations: numbers[3],
	}, true
}
