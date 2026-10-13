//go:build flowtest

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// What `flow doctor` does when there is a person at a terminal watching it
// (spec 02 §2, spec 12 §3): a progress display while a run is in flight, and
// `--watch` to keep asking. What is proved here is that the command reaches
// the screens at all, that the report is still the document it always was,
// and that a flag flow cannot honour is refused rather than ignored.

// watched is a terminal's output, written by flow and read by the test while
// flow is still running.
type watched struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements [io.Writer].
func (w *watched) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// String is what has been drawn so far.
func (w *watched) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// onWatch runs flow at a terminal and types what it is given once the screen
// has drawn the text it is waiting for.
//
// Waiting is what makes it a test rather than a race: the screen asks the
// checks as it opens, and a q typed before that run came back would prove
// only that the window closes.
func onWatch(t *testing.T, dir, until, typed string, args ...string) (code int, stdout string) {
	t.Helper()
	t.Setenv("CI", "")
	keys, typing := io.Pipe()
	stdin := io.Reader(keys)
	drawn := &watched{}
	var errs bytes.Buffer

	was := terminalCheck
	t.Cleanup(func() { terminalCheck = was })
	terminalCheck = func(stream any) bool {
		return stream == stdin || stream == io.Writer(drawn)
	}
	// A test process has no terminal, so the screen has no width to draw
	// inside and would cut every line it rendered to nothing.
	wasSize := screenOptions
	t.Cleanup(func() { screenOptions = wasSize })
	screenOptions = []tea.ProgramOption{tea.WithWindowSize(100, 40)}

	keyed := make(chan struct{})
	go func() {
		defer close(keyed)
		deadline := time.Now().Add(20 * time.Second)
		for !strings.Contains(drawn.String(), until) {
			if time.Now().After(deadline) {
				t.Errorf("the screen never drew %q:\n%s", until, drawn.String())
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = io.WriteString(typing, typed)
		_ = typing.Close()
	}()

	code = run(append([]string{"-C", dir}, args...), stdin, drawn, &errs)
	<-keyed
	return code, drawn.String()
}

// TestWatchKeepsAskingUntilSomebodyClosesIt states what the screen is for and
// what it leaves behind. It draws every finding, it asks again when it is
// told to, and the exit code is the last run's — a window closed on a
// repository that still needs work has not made it well.
func TestWatchKeepsAskingUntilSomebodyClosesIt(t *testing.T) {
	dir := fakeRepo(t)
	want, plain := doctorJSON(t, dir, owners()...)

	code, stdout := onWatch(t, dir, "exit ", "q", append([]string{"doctor", "--watch"}, owners()...)...)

	if code != plain {
		t.Errorf("the watch session exited %d, want %d, which is what the same run exits", code, plain)
	}
	if code != core.ExitActionRequired {
		t.Fatalf("the repository had nothing to report, so the exit code proves nothing (exit %d)", code)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Fatalf("nothing was drawn:\n%q", stdout)
	}
	for _, check := range want.Checks[:3] {
		if !strings.Contains(stdout, check.ID) {
			t.Errorf("%s was never on the screen:\n%s", check.ID, stdout)
		}
	}
}

// TestWatchNeedsATerminalToDrawOn states that the screen is not the only way
// to ask. A run with nowhere to draw is refused rather than quietly turning
// into the report, because `--watch` is a request to keep asking and a report
// printed once is not that.
func TestWatchNeedsATerminalToDrawOn(t *testing.T) {
	dir := fakeRepo(t)
	code, _, stderr := execAt(t, dir, append([]string{"doctor", "--watch"}, owners()...)...)

	if code != core.ExitUsage {
		t.Fatalf("a watch with no terminal exited %d, want %d", code, core.ExitUsage)
	}
	if !strings.Contains(stderr, "no terminal to draw on") {
		t.Errorf("the refusal does not say what was missing:\n%s", stderr)
	}
}

// TestWatchAndAReportAreDifferentThings states the other refusal. A watch
// session has no last run until somebody closes it, so a file written from
// one would hold whichever run happened to be on the screen at the time.
func TestWatchAndAReportAreDifferentThings(t *testing.T) {
	dir := fakeRepo(t)
	report := filepath.Join(t.TempDir(), "health.md")
	code, _, stderr := execAt(t, dir, append([]string{"doctor", "--watch", "--report", report}, owners()...)...)

	if code != core.ExitUsage {
		t.Fatalf("a watch that was asked for a report exited %d, want %d", code, core.ExitUsage)
	}
	if !strings.Contains(stderr, "--report") {
		t.Errorf("the refusal does not name the flag to drop:\n%s", stderr)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Errorf("the refused run wrote %s anyway", report)
	}
}

// TestAWatchThatNeverAnsweredExitsOnWhyNot states what a window closed on
// nothing exits with. The exit code is the last run that finished, and when
// no run ever did there is no verdict to give: exiting 0 would report a
// healthy repository that nobody managed to look at.
func TestAWatchThatNeverAnsweredExitsOnWhyNot(t *testing.T) {
	dir := fakeRepo(t)
	args := append([]string{"doctor", "--watch", "--check", "no.such.check"}, owners()...)
	code, stdout := onWatch(t, dir, "the run went wrong", "q", args...)

	if want := core.ExitCode(core.ErrValidation); code != want {
		t.Errorf("the session exited %d, want %d, which is what the run went wrong with", code, want)
	}
	if !strings.Contains(stdout, "matches no check") {
		t.Errorf("the screen never said what went wrong:\n%s", stdout)
	}
}

// TestARunAtATerminalSaysWhereItHasGotTo states what the inline display adds
// and what it must not take away. A doctor run is silent for as long as it
// takes to read a fortnight of pull requests; the display fills that silence,
// and the report still lands underneath it as the document it always was.
func TestARunAtATerminalSaysWhereItHasGotTo(t *testing.T) {
	dir := fakeRepo(t)
	want, plain := doctorJSON(t, dir, owners()...)
	slowBoard(t)
	code, stdout := onWatch(t, dir, "", "", append([]string{"doctor"}, owners()...)...)

	if code != plain {
		t.Fatalf("doctor exited %d, want %d", code, plain)
	}
	if !strings.Contains(stdout, "\x1b[") {
		t.Errorf("nothing was drawn while the run was in flight:\n%q", stdout)
	}
	// The display says which check is being asked and how many are left,
	// which is the whole of what it adds.
	if !strings.Contains(stdout, " of "+itoa(len(want.Checks))) {
		t.Errorf("the display never counted the checks:\n%q", stdout)
	}
	// The display empties itself when the run ends, so the last thing on the
	// terminal is the report: the document the command came to print, under
	// the display rather than instead of it.
	if !strings.HasSuffix(strings.TrimRight(stdout, "\n"), "exit "+itoa(plain)) {
		t.Errorf("the report did not land under the display:\n%s", stdout)
	}
}

// slowBoard makes the board take a moment to answer.
//
// It is the condition the display exists for, and without it there is nothing
// to see: a run against fakes comes back inside a single frame, so the first
// thing the renderer ever flushes is the empty screen of a finished run. What
// would be under test then is bubble tea's frame rate.
func slowBoard(t *testing.T) {
	t.Helper()
	was := newAdapters
	t.Cleanup(func() { newAdapters = was })
	newAdapters = func(a *Context, s *session) (engine.Adapters, error) {
		adapters, err := was(a, s)
		if err != nil {
			return adapters, err
		}
		adapters.Tracker = slowTracker{Tracker: adapters.Tracker}
		return adapters, nil
	}
}

// slowTracker is a board that answers as slowly as a real one over a network.
type slowTracker struct {
	core.Tracker
}

// Tickets implements [core.Tracker], after long enough for the screen to be
// drawn several times over.
func (s slowTracker) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	time.Sleep(250 * time.Millisecond)
	return s.Tracker.Tickets(ctx, q)
}

// itoa is a number as the report writes it.
func itoa(n int) string { return strconv.Itoa(n) }
