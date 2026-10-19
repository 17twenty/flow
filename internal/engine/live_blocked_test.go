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

// live.tickets.blocked_explained (spec 07 §6): a team that wrote down that a
// blocked label is not enough is held to the comment it asked for.

// stopped is a ticket carrying the blocked label.
func stopped(key, title string) core.Ticket {
	ticket := boarded(key, title, "In progress", daysAgo(2))
	ticket.Labels = []string{"blocked"}
	return ticket
}

// saidWhy is somebody's comment, which is to say one with no marker on it.
func saidWhy(body string, at time.Time) core.Comment {
	return core.Comment{Author: "dev", Body: body, CreatedAt: at, UpdatedAt: at}
}

// flowSaid is flow's own writing, marked as flow's.
func flowSaid(body string, at time.Time) core.Comment {
	marker := core.NewMarker(core.MarkerFollowUp, "check", "tracker.labels")
	return core.Comment{
		Author:    "flow-bot",
		Body:      core.MarkBody(body, marker, true),
		Marker:    marker,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

// blockedEnv is a board with comments already written on it.
func blockedEnv(t *testing.T, cfg *config.Config, comments map[string][]core.Comment, tickets ...core.Ticket) (*engine.Env, *coretest.FakeTracker) {
	t.Helper()
	env, _ := liveEnv(t, cfg, coretest.ForgeFixture{})
	tracker := coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: tickets, Comments: comments})
	env.Adapters.Tracker = tracker
	return env, tracker
}

// blockedResult runs the check alone.
func blockedResult(t *testing.T, env *engine.Env) engine.Result {
	t.Helper()
	return liveResult(t, env, engine.CheckLiveTicketsBlockedExplained)
}

// TestABlockedTicketNobodyHasExplainedIsAFinding is the check doing its job:
// the label went on, the explanation did not follow, and the one ticket that
// is missing it is named with a link while the one that is not is left alone.
func TestABlockedTicketNobodyHasExplainedIsAFinding(t *testing.T) {
	explained := stopped("#1", "waiting on the vendor")
	silent := stopped("#2", "waiting on nothing anybody can see")
	env, _ := blockedEnv(t, liveConfig(), map[string][]core.Comment{
		"#1": {saidWhy("the vendor has to ship the certificate; Dana is chasing them", daysAgo(1))},
	}, explained, silent)

	result := blockedResult(t, env)

	if result.State != engine.StateFail {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StateFail, result.Message)
	}
	if result.Counts == nil || result.Counts.Total != 2 || result.Counts.Pass != 1 {
		t.Fatalf("counts = %+v, want 1 of 2", result.Counts)
	}
	evidenceLinks(t, result, "#2", silent.URL)
	for _, seen := range result.Evidence {
		if strings.Contains(seen.Text, "#1 ") {
			t.Errorf("a ticket somebody explained is reported as a finding: %q", seen.Text)
		}
	}
}

// TestTheRemediationSaysWhatTheCommentMustSay holds the half of the proposal
// that is not the label. A team told to "add a comment" writes "blocked", and
// the next person reading the board is no better off: it has to ask for why,
// for what would clear it, and for whose it is to chase.
func TestTheRemediationSaysWhatTheCommentMustSay(t *testing.T) {
	env, _ := blockedEnv(t, liveConfig(), nil, stopped("#1", "waiting"))

	result := blockedResult(t, env)

	steps := strings.ToLower(strings.Join(result.Remediation.Steps, "\n"))
	for _, want := range []string{"why it is blocked", "what will unblock it", "who is monitoring it"} {
		if !strings.Contains(steps, want) {
			t.Errorf("the remediation does not ask for %q: %v", want, result.Remediation.Steps)
		}
	}
	if !strings.Contains(steps, "workflow.blocked.require_comment") {
		t.Errorf("the remediation does not name the key that turns this off: %v", result.Remediation.Steps)
	}
}

// TestFlowsOwnWritingIsNotAnExplanation is the check refusing to be answered
// by itself. Flow nudges its own follow-ups, and a reading that counted a
// nudge as an explanation would pass every ticket flow had ever written on —
// which is exactly the blocked ones that have been waiting longest.
func TestFlowsOwnWritingIsNotAnExplanation(t *testing.T) {
	nudged := stopped("#1", "a follow-up flow filed and then chased")
	env, _ := blockedEnv(t, liveConfig(), map[string][]core.Comment{
		"#1": {flowSaid("this has been waiting three weeks", daysAgo(4))},
	}, nudged)

	result := blockedResult(t, env)

	if result.State != engine.StateFail {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StateFail, result.Message)
	}
	evidenceMentions(t, result, "only flow has written on it")
}

// TestATicketWithNothingWrittenOnItSaysSo distinguishes the two silences. A
// ticket nobody has touched and one flow has been talking to itself on need
// different things from whoever reads the report.
func TestATicketWithNothingWrittenOnItSaysSo(t *testing.T) {
	env, _ := blockedEnv(t, liveConfig(), nil, stopped("#1", "waiting"))

	result := blockedResult(t, env)

	evidenceMentions(t, result, "nobody has written anything on it")
}

// TestAnEmptyCommentIsNotAnExplanation: a comment that says nothing is not
// somebody saying why, and a tracker whose comment bodies arrive as whitespace
// must not thereby clear the board.
func TestAnEmptyCommentIsNotAnExplanation(t *testing.T) {
	env, _ := blockedEnv(t, liveConfig(), map[string][]core.Comment{
		"#1": {saidWhy("   \n\n", daysAgo(1))},
	}, stopped("#1", "waiting"))

	if result := blockedResult(t, env); result.State != engine.StateFail {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StateFail, result.Message)
	}
}

// TestTheTrackersOwnBlockedFlagCountsAsBlocked holds the other half of what
// blocked means. A tracker with a flag of its own — a Jira flag, a Linear
// blocking relation — says so through [core.Ticket.Blocked], and a check that
// only read the label would see nothing wrong on a board that uses neither.
func TestTheTrackersOwnBlockedFlagCountsAsBlocked(t *testing.T) {
	flagged := boarded("#1", "blocked by the tracker's own flag", "In progress", daysAgo(2))
	flagged.Blocked = true
	env, _ := blockedEnv(t, liveConfig(), nil, flagged)

	result := blockedResult(t, env)

	if result.Counts == nil || result.Counts.Total != 1 {
		t.Fatalf("counts = %+v, want the one ticket the tracker flagged", result.Counts)
	}
	if result.State != engine.StateFail {
		t.Errorf("state = %q, want %q: %s", result.State, engine.StateFail, result.Message)
	}
}

// TestBlockedWorkThatIsFinishedIsNotWaitingOnAnybody: a closed ticket keeps
// whatever labels it had, and asking a team to explain work they have already
// finished is asking them to tidy history.
func TestBlockedWorkThatIsFinishedIsNotWaitingOnAnybody(t *testing.T) {
	done := stopped("#1", "blocked, then dropped")
	done.Closed, done.ClosedAt, done.CloseReason = true, daysAgo(1), core.ClosedNotPlanned
	env, _ := blockedEnv(t, liveConfig(), nil, done)

	result := blockedResult(t, env)

	if result.State != engine.StatePass {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StatePass, result.Message)
	}
	if result.Counts == nil || result.Counts.Total != 0 {
		t.Errorf("counts = %+v, want nothing counted", result.Counts)
	}
	if strings.Contains(result.Message, "all 0") {
		t.Errorf("message = %q, which reads as health rather than silence", result.Message)
	}
}

// TestATeamThatDoesNotAskForAnExplanationIsNotAsked: `blocked.label` on its
// own is a team that marks blocked work and says nothing more about it, which
// is their business. Flow holds them to what they wrote down.
func TestATeamThatDoesNotAskForAnExplanationIsNotAsked(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Blocked = config.Blocked{Label: "blocked"}
	env, tracker := blockedEnv(t, cfg, nil, stopped("#1", "waiting"))

	result := blockedResult(t, env)

	if result.State != engine.StateSkip {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StateSkip, result.Message)
	}
	if !strings.Contains(result.Message, "workflow.blocked.require_comment") {
		t.Errorf("the skip does not name the key that would turn it on: %q", result.Message)
	}
	if got := tracker.Calls("Comments"); got != 0 {
		t.Errorf("the tracker was asked for comments %d times by a check that does not apply", got)
	}
}

// TestATicketFlowCouldNotReadIsNotATicketWithNothingOnIt: a tracker that would
// not answer is a run that could not look, and a check that read its own
// blindness as "nobody has said anything" would file a finding against a team
// for flow's own failure.
func TestATicketFlowCouldNotReadIsNotATicketWithNothingOnIt(t *testing.T) {
	env, tracker := blockedEnv(t, liveConfig(), nil, stopped("#1", "waiting"))
	tracker.Fail("Comments", errors.New("the tracker is down"))

	result := blockedResult(t, env)

	if result.State != engine.StateError {
		t.Fatalf("state = %q, want %q", result.State, engine.StateError)
	}
	if result.Err == nil {
		t.Fatalf("a tracker that would not answer produced a finding rather than an error: %s", result.Message)
	}
	if result.Counts != nil && result.Counts.Total != 0 {
		t.Errorf("counts = %+v, want nothing counted by a run that could not look", result.Counts)
	}
}

// TestNothingBlockedIsNotHealthAndNotIllHealth: an empty answer and a clean
// one read differently on the dashboard the daily run is for.
func TestNothingBlockedIsNotHealthAndNotIllHealth(t *testing.T) {
	env, _ := blockedEnv(t, liveConfig(), nil, boarded("#1", "moving along", "In progress", daysAgo(1)))

	result := blockedResult(t, env)

	if result.State != engine.StatePass {
		t.Fatalf("state = %q, want %q: %s", result.State, engine.StatePass, result.Message)
	}
	if !strings.Contains(result.Message, "nothing on the board is marked blocked") {
		t.Errorf("message = %q, want it to say the board has nothing blocked", result.Message)
	}
}

// TestHowMuchUnexplainedBlockedWorkATeamWillCarryIsTheirs: the check is graded
// rather than critical, so `health.fail_below` is what decides. One ticket in
// twenty is a reminder; a board where a fifth of the blocked work says nothing
// is a workflow that has stopped working.
func TestHowMuchUnexplainedBlockedWorkATeamWillCarryIsTheirs(t *testing.T) {
	tests := []struct {
		name      string
		explained int
		want      engine.State
	}{
		{name: "one of twenty unexplained", explained: 19, want: engine.StateWarn},
		{name: "three of twenty unexplained", explained: 17, want: engine.StateFail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var tickets []core.Ticket
			comments := map[string][]core.Comment{}
			for number := 1; number <= 20; number++ {
				key := fmt.Sprintf("#%d", number)
				tickets = append(tickets, stopped(key, "waiting"))
				if number <= test.explained {
					comments[key] = []core.Comment{saidWhy("the vendor is shipping it on Friday", daysAgo(1))}
				}
			}
			env, _ := blockedEnv(t, liveConfig(), comments, tickets...)

			wantState(t, blockedResult(t, env), test.want)
		})
	}
}

// TestBlockedTicketsAreNamedInTheirOwnOrder: the report is read by somebody
// comparing this morning's to yesterday's, and a list that reorders itself
// because the board did reads as a change when nothing changed.
func TestBlockedTicketsAreNamedInTheirOwnOrder(t *testing.T) {
	env, _ := blockedEnv(t, liveConfig(), nil,
		stopped("#3", "waiting"), stopped("#1", "waiting"), stopped("#2", "waiting"))

	result := blockedResult(t, env)

	var keys []string
	for _, seen := range result.Evidence {
		keys = append(keys, strings.Fields(seen.Text)[0])
	}
	if want := []string{"#1", "#2", "#3"}; !slices.Equal(keys, want) {
		t.Errorf("the tickets are named %v, want %v: the board's order is not flow's", keys, want)
	}
}

// TestATeamWithNoBlockedLabelIsNotToldToTakeOneOff: a board that marks blocked
// work through the tracker's own flag has no label to remove, and naming one
// they have never configured sends them looking for it.
func TestATeamWithNoBlockedLabelIsNotToldToTakeOneOff(t *testing.T) {
	cfg := liveConfig()
	cfg.Workflow.Blocked = config.Blocked{RequireComment: true}
	flagged := boarded("#1", "blocked by the tracker's own flag", "In progress", daysAgo(2))
	flagged.Blocked = true
	env, _ := blockedEnv(t, cfg, nil, flagged)

	result := blockedResult(t, env)

	steps := strings.Join(result.Remediation.Steps, "\n")
	if !strings.Contains(steps, "blocked flag") {
		t.Errorf("the remediation does not name the flag this board uses: %v", result.Remediation.Steps)
	}
	if strings.Contains(steps, "label instead") {
		t.Errorf("the remediation names a label this team has not configured: %v", result.Remediation.Steps)
	}
}
