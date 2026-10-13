package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/engine"
)

// The clock is the one part of the watch screen a test cannot reach from
// outside it. `watchTicked` is the screen's own message, sent a minute after
// the last one, and a test that waited for it would wait a minute — so these
// two are written from inside the package and deliver it by hand.

// stubRunner is a flow that counts what it was asked for.
type stubRunner struct{ runs int }

// Doctor implements [Runner].
func (s *stubRunner) Doctor(bool) (engine.Report, error) {
	s.runs++
	return engine.Report{Checks: []engine.Checked{{
		ID:     "tracker.saved_view",
		Title:  "the ready view exists",
		Result: attestWanted(),
	}}}, nil
}

// Attest implements [Runner].
func (s *stubRunner) Attest(string, string) error { return nil }

// attestWanted is the one finding the stub reports: a check waiting on
// somebody's word, which is the only kind the note can be opened on.
func attestWanted() engine.Result {
	return engine.Result{State: engine.StateAttest, Message: "nobody has vouched for this yet"}.
		Fix(engine.Remediation{Kind: engine.RemedyAttest, Summary: "say what you see"})
}

// ticking builds the screen with its opening run already answered.
func ticking(t *testing.T) (Watch, *stubRunner) {
	t.Helper()
	flow := &stubRunner{}
	screen := New(Options{}).Watch(flow)
	report, err := flow.Doctor(false)
	if err != nil {
		t.Fatalf("the stub refused to run: %v", err)
	}
	flow.runs = 1
	updated, _ := screen.Update(watchRan{report: report})
	settled, ok := updated.(Watch)
	if !ok {
		t.Fatalf("the screen updated into a %T", updated)
	}
	return settled, flow
}

// feed delivers a message and whatever the command it produced answers with,
// one layer deep, which is as far as a run ever goes.
//
// A command that has not answered in a moment is the clock the screen just
// wound, and waiting on that one would wait out [WatchEvery].
func feed(t *testing.T, screen Watch, msgs ...tea.Msg) Watch {
	t.Helper()
	model := tea.Model(screen)
	for _, msg := range msgs {
		var cmd tea.Cmd
		model, cmd = model.Update(msg)
		for _, answer := range answered(t, cmd) {
			if _, isRan := answer.(watchRan); isRan {
				model, _ = model.Update(answer)
			}
		}
	}
	final, ok := model.(Watch)
	if !ok {
		t.Fatalf("the screen updated into a %T", model)
	}
	return final
}

// answered runs a command and returns what it produced, flattening a batch.
// A command that has not answered in a moment is a timer — the clock, the
// spinner — and waiting on one of those would wait out [WatchEvery].
func answered(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		batch, isBatch := msg.(tea.BatchMsg)
		if !isBatch {
			return []tea.Msg{msg}
		}
		var msgs []tea.Msg
		for _, each := range batch {
			msgs = append(msgs, answered(t, each)...)
		}
		return msgs
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}

// TestTheScreenKeepsOneClock states what makes [WatchEvery] a rate limit. The
// screen winds one clock when it opens and that clock winds itself; nothing
// else does. A run that wound one as it finished would leave a second chain
// of ticks behind every manual rerun, and a window left open all afternoon
// would end up asking a forge faster than anybody asked it to.
func TestTheScreenKeepsOneClock(t *testing.T) {
	t.Parallel()
	flow := &stubRunner{}
	screen := New(Options{}).Watch(flow)
	wound := 0
	screen.clock = func() tea.Cmd {
		wound++
		return func() tea.Msg { return nil }
	}

	screen.Init()
	if wound != 1 {
		t.Fatalf("opening the screen wound %d clocks, want 1", wound)
	}
	report, err := flow.Doctor(false)
	if err != nil {
		t.Fatalf("the stub refused to run: %v", err)
	}
	screen = feed(t, screen, watchRan{report: report})
	if wound != 1 {
		t.Errorf("the opening run wound a clock of its own (%d in all)", wound)
	}
	screen = feed(t, screen, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if wound != 1 {
		t.Errorf("a rerun somebody asked for wound a clock of its own (%d in all)", wound)
	}
	was := flow.runs
	screen = feed(t, screen, watchTicked{})
	if wound != 2 {
		t.Errorf("the clock came round and left %d wound, want 2", wound)
	}
	if flow.runs != was+1 {
		t.Errorf("the clock asked for %d runs, want %d", flow.runs-was, 1)
	}
}

// TestTheClockAsksAgainOnItsOwn states why the screen is worth leaving open.
// Somebody putting a repository right wants to see it go green without
// reaching for a key, which is the whole difference between this and running
// `flow doctor` twice.
func TestTheClockAsksAgainOnItsOwn(t *testing.T) {
	t.Parallel()
	screen, flow := ticking(t)
	feed(t, screen, watchTicked{})

	if flow.runs != 2 {
		t.Errorf("the clock asked for %d runs in all, want 2", flow.runs)
	}
}

// TestTheClockWaitsForWhoeverIsTyping states the one thing that must not
// happen while a note is open. A refresh replaces the findings under the
// cursor, and a check that moved out from under a half-written attestation
// would record a promise about a different check.
func TestTheClockWaitsForWhoeverIsTyping(t *testing.T) {
	t.Parallel()
	screen, flow := ticking(t)
	screen = feed(t, screen, tea.KeyPressMsg{Code: 'a', Text: "a"})
	screen = feed(t, screen, watchTicked{})

	if flow.runs != 1 {
		t.Errorf("the clock asked for %d runs while a note was open, want 1", flow.runs)
	}
	if view := screen.View().Content; !strings.Contains(view, "why does tracker.saved_view hold?") {
		t.Errorf("the clock closed the note:\n%s", view)
	}
}

// TestTheClockKeepsTicking states that a refusal does not stop the screen. A
// tick that declined to act and did not wind the clock again would leave a
// window open for the afternoon that never asked anything after the first
// note was abandoned.
func TestTheClockKeepsTicking(t *testing.T) {
	t.Parallel()
	screen, flow := ticking(t)
	screen = feed(t, screen, tea.KeyPressMsg{Code: 'a', Text: "a"})

	updated, cmd := screen.Update(watchTicked{})
	if cmd == nil {
		t.Fatal("a tick that acted on nothing did not wind the clock again")
	}
	screen, ok := updated.(Watch)
	if !ok {
		t.Fatalf("the screen updated into a %T", updated)
	}
	screen = feed(t, screen, tea.KeyPressMsg{Code: tea.KeyEscape}, watchTicked{})
	if flow.runs != 2 {
		t.Errorf("the clock asked for %d runs in all, want 2", flow.runs)
	}
	if view := screen.View().Content; strings.Contains(view, "why does") {
		t.Errorf("the abandoned note is still open:\n%s", view)
	}
}
