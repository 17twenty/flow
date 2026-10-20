package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// openPR is a pull request open and ready for review.
func openPR(number int) core.PullRequest {
	return core.PullRequest{
		Number:  number,
		Title:   "feat(engine): move the ticket with the work",
		Author:  "nick",
		State:   core.PRStateOpen,
		HeadSHA: "abc1234",
	}
}

// draftPR is a pull request open but not offered for review.
func draftPR(number int) core.PullRequest {
	pr := openPR(number)
	pr.Draft = true
	return pr
}

// mergedPR is a pull request that landed.
func mergedPR(number int) core.PullRequest {
	pr := openPR(number)
	pr.State = core.PRStateMerged
	pr.MergeSHA = "def5678"
	return pr
}

// closedPR is a pull request abandoned without merging.
func closedPR(number int) core.PullRequest {
	pr := openPR(number)
	pr.State = core.PRStateClosed
	return pr
}

// work is a snapshot of one ticket sitting in a column with some pull
// requests against it.
func work(cfg *config.Config, column string, prs ...engine.LinkedPR) engine.TransitionSnapshot {
	return engine.TransitionSnapshot{
		Ticket: core.Ticket{Key: "#12", Status: core.Status{Name: "In review"}},
		Column: column,
		PRs:    prs,
		Config: cfg,
	}
}

// linked wraps a pull request with nothing special about it.
func linked(pr core.PullRequest) engine.LinkedPR { return engine.LinkedPR{PR: pr} }

// step returns the decision about one event, failing the test when the event
// was never considered.
func step(t *testing.T, move engine.TicketMove, event string) engine.TransitionStep {
	t.Helper()
	for _, s := range move.Steps {
		if s.Event == event {
			return s
		}
	}
	t.Fatalf("event %s was not considered; steps were %v", event, events(move))
	return engine.TransitionStep{}
}

// events lists the events a decision considered, in order.
func events(move engine.TicketMove) []string {
	var names []string
	for _, s := range move.Steps {
		names = append(names, s.Event)
	}
	return names
}

func TestTransitionStartsTheWorkWhenADraftOpens(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(work(cfg, "ready", linked(draftPR(7))))

	if !move.Moves() {
		t.Fatalf("a draft pull request did not start the work: %+v", move)
	}
	if move.From != "ready" || move.To != "in_progress" {
		t.Errorf("moved %s → %s, want ready → in_progress", move.From, move.To)
	}
	if move.Event != config.EventPROpenedDraft {
		t.Errorf("event = %s, want %s", move.Event, config.EventPROpenedDraft)
	}
}

func TestTransitionSendsReadyWorkToReview(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(work(cfg, "ready", linked(openPR(7))))

	if move.To != "in_review" {
		t.Fatalf("to = %q, want in_review (%+v)", move.To, move.Steps)
	}
	// Spec 04 §4: a pull request opened ready is pr_opened_draft followed by
	// pr_ready_for_review, and both apply in order.
	if got := events(move); !slices.Equal(got, []string{config.EventPROpenedDraft, config.EventPRReadyForReview}) {
		t.Errorf("events = %v, want the opened-draft and ready-for-review pair", got)
	}
	if !step(t, move, config.EventPROpenedDraft).Moved {
		t.Error("the draft step did not apply on the way past")
	}
}

// A workflow that configures only the first half of pr_opened still starts the
// work, which is why both halves are applied rather than just the last one.
func TestTransitionAppliesTheFirstHalfOfAnOpeningOnItsOwn(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	delete(cfg.Workflow.Transitions, config.EventPRReadyForReview)

	move := engine.Transition(work(cfg, "ready", linked(openPR(7))))

	if move.To != "in_progress" {
		t.Fatalf("to = %q, want in_progress", move.To)
	}
	if got := step(t, move, config.EventPRReadyForReview); got.Reason != engine.ReasonUnconfigured {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonUnconfigured)
	}
}

func TestTransitionIsIdempotent(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(work(cfg, "in_review", linked(openPR(7))))

	if move.Moves() {
		t.Fatalf("a ticket already in review was moved to %s", move.To)
	}
	if got := step(t, move, config.EventPRReadyForReview); got.Reason != engine.ReasonSatisfied {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonSatisfied)
	}
}

func TestTransitionLeavesNativeTransitionsToTheTracker(t *testing.T) {
	cfg := preset(t, "lean-kanban") // pr_merged: { to: done, owner: native }

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7))))

	if move.Moves() {
		t.Fatalf("flow performed a transition the tracker owns: %+v", move)
	}
	merged := step(t, move, config.EventPRMerged)
	if merged.Reason != engine.ReasonNative {
		t.Errorf("reason = %q, want %q", merged.Reason, engine.ReasonNative)
	}
	if merged.Owner != config.OwnerNative || merged.To != "done" {
		t.Errorf("step = %+v, want the native transition to done reported in full", merged)
	}
}

func TestTransitionPerformsAMergeItOwns(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{To: "done", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7))))

	if move.To != "done" || move.Event != config.EventPRMerged {
		t.Fatalf("move = %s → %s on %s, want in_review → done on pr_merged", move.From, move.To, move.Event)
	}
}

// Only a review asking for changes and a pull request put back to draft may
// move work backwards, and only when the workflow configures them.
func TestTransitionNeedsAConfiguredBackMoveToGoBack(t *testing.T) {
	cfg := preset(t, "lean-kanban") // configures no pr_converted_to_draft

	move := engine.Transition(work(cfg, "in_review", linked(draftPR(7))))

	if move.Moves() {
		t.Fatalf("a ticket went backwards without a transition saying it could: %+v", move)
	}
	if got := step(t, move, config.EventPROpenedDraft); got.Reason != engine.ReasonBackwards {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonBackwards)
	}
	if got := step(t, move, config.EventPRConvertedToDraft); got.Reason != engine.ReasonUnconfigured {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonUnconfigured)
	}
}

func TestTransitionMovesBackWhenAPullRequestReturnsToDraft(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRConvertedToDraft] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(draftPR(7))))

	if move.To != "in_progress" || move.Event != config.EventPRConvertedToDraft {
		t.Fatalf("move = %s → %s on %s, want in_review → in_progress on pr_converted_to_draft", move.From, move.To, move.Event)
	}
}

func TestTransitionMovesBackWhenChangesAreRequested(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	pr := openPR(7)
	pr.Reviews = []core.Review{{Author: "sam", State: core.ReviewChangesRequested}}

	move := engine.Transition(work(cfg, "in_review", linked(pr)))

	if move.To != "in_progress" || move.Event != config.EventChangesRequested {
		t.Fatalf("move = %s → %s on %s, want in_review → in_progress on changes_requested", move.From, move.To, move.Event)
	}
}

// A reviewer who asked for changes and then approved is no longer asking.
func TestTransitionReadsTheLatestReviewFromEachPerson(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	pr := openPR(7)
	pr.Reviews = []core.Review{
		{Author: "sam", State: core.ReviewChangesRequested},
		{Author: "sam", State: core.ReviewApproved},
	}

	move := engine.Transition(work(cfg, "in_review", linked(pr)))

	if move.Moves() {
		t.Fatalf("an answered review still held the work back: %+v", move)
	}
	if slices.Contains(events(move), config.EventChangesRequested) {
		t.Error("changes_requested was raised for a review that was answered")
	}
}

func TestTransitionNeverMovesWorkOutOfDone(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRConvertedToDraft] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "done", linked(draftPR(7))))

	if move.Moves() {
		t.Fatalf("flow took work back out of done: %+v", move)
	}
	if got := step(t, move, config.EventPRConvertedToDraft); got.Reason != engine.ReasonDone {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonDone)
	}
}

// Done means all of the work, not the last piece of it.
func TestTransitionWaitsForEveryLinkedPullRequest(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{To: "done", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_progress", linked(mergedPR(7)), linked(openPR(9))))

	if move.To != "in_review" {
		t.Fatalf("to = %q, want in_review: one pull request is still open", move.To)
	}
	merged := step(t, move, config.EventPRMerged)
	if merged.Reason != engine.ReasonOpenWork {
		t.Fatalf("reason = %q, want %q", merged.Reason, engine.ReasonOpenWork)
	}
	if !strings.Contains(merged.Why, "#9") {
		t.Errorf("why = %q, want it to name the pull request holding the ticket back", merged.Why)
	}
}

func TestTransitionIsDoneWhenAllTheWorkIs(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{To: "done", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7)), linked(mergedPR(9))))

	if move.To != "done" {
		t.Fatalf("to = %q, want done", move.To)
	}
}

// An abandoned pull request is not unfinished work: it is no work at all.
func TestTransitionDoesNotCountAnAbandonedPullRequestAsOpen(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{To: "done", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7)), linked(closedPR(9))))

	if move.To != "done" {
		t.Fatalf("to = %q, want done (%+v)", move.To, move.Steps)
	}
}

// A closed pull request never passed through review on its way out, so the
// engine does not replay a history that would strand the ticket there.
func TestTransitionReportsThePresentStateNotTheHistory(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(work(cfg, "in_progress", linked(closedPR(7))))

	if move.Moves() {
		t.Fatalf("an abandoned pull request moved the ticket to %s", move.To)
	}
	if got := events(move); !slices.Equal(got, []string{config.EventPRClosedUnmerged}) {
		t.Errorf("events = %v, want only pr_closed_unmerged", got)
	}
}

func TestTransitionUnderDoneOnDeployedWaitsForTheDeployment(t *testing.T) {
	cfg := preset(t, "deploy-done")

	move := engine.Transition(work(cfg, "in_review",
		engine.LinkedPR{PR: mergedPR(7), Deployed: true},
		engine.LinkedPR{PR: mergedPR(9)},
	))

	if move.Moves() {
		t.Fatalf("a ticket reached done with work that is not running: %+v", move)
	}
	deployed := step(t, move, config.EventDeployed)
	if deployed.Reason != engine.ReasonUndeployed {
		t.Fatalf("reason = %q, want %q", deployed.Reason, engine.ReasonUndeployed)
	}
	if !strings.Contains(deployed.Why, "#9") || !strings.Contains(deployed.Why, "production") {
		t.Errorf("why = %q, want the pull request and the environment named", deployed.Why)
	}
}

func TestTransitionUnderDoneOnDeployedIsDoneWhenItIsRunning(t *testing.T) {
	cfg := preset(t, "deploy-done")

	move := engine.Transition(work(cfg, "in_review",
		engine.LinkedPR{PR: mergedPR(7), Deployed: true},
		engine.LinkedPR{PR: mergedPR(9), Deployed: true},
	))

	if move.To != "done" || move.Event != config.EventDeployed {
		t.Fatalf("move = %s → %s on %s, want in_review → done on deployed", move.From, move.To, move.Event)
	}
}

func TestTransitionSkipsATicketTheBoardPutsSomewhereElse(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	snapshot := work(cfg, "", linked(openPR(7)))
	snapshot.Ticket.Status = core.Status{Name: "Icebox"}

	move := engine.Transition(snapshot)

	if move.Moves() {
		t.Fatalf("flow guessed where an unmapped ticket belonged: %+v", move)
	}
	if move.Warning == "" {
		t.Fatal("an unmapped ticket was skipped silently")
	}
	if !strings.Contains(move.Warning, "Icebox") || !strings.Contains(move.Warning, "#12") {
		t.Errorf("warning = %q, want the ticket and the status it is in named", move.Warning)
	}
	if len(move.Steps) != 0 {
		t.Errorf("steps = %v, want none: flow does not know where the ticket is", move.Steps)
	}
}

// Spec 04 §5 rule 4: the ticket is in review while *any* linked pull request
// is ready. A second pull request a reviewer sent back does not undo that, and
// which order the two arrived in cannot matter.
func TestTransitionKeepsTheTicketInReviewWhileAnyPullRequestIsReady(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	refused := openPR(9)
	refused.Reviews = []core.Review{{Author: "sam", State: core.ReviewChangesRequested}}

	for _, test := range []struct {
		name string
		prs  []engine.LinkedPR
	}{
		{name: "ready first", prs: []engine.LinkedPR{linked(openPR(7)), linked(refused)}},
		{name: "refused first", prs: []engine.LinkedPR{linked(refused), linked(openPR(7))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			move := engine.Transition(work(cfg, "in_review", test.prs...))

			if move.Moves() {
				t.Fatalf("the ticket left review with #7 still ready: → %s (%+v)", move.To, move.Steps)
			}
			changes := step(t, move, config.EventChangesRequested)
			if changes.Moved {
				t.Error("an overruled step still claims flow is making that move")
			}
			if changes.Reason != engine.ReasonOverruled {
				t.Errorf("reason = %q, want %q", changes.Reason, engine.ReasonOverruled)
			}
			if !strings.Contains(changes.Why, "#7") {
				t.Errorf("why = %q, want it to name the pull request holding the ticket in review", changes.Why)
			}
		})
	}
}

// With nothing holding it in review, the same refusal does take the work back.
func TestTransitionTakesTheWorkBackWhenNothingHoldsItInReview(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	refused := openPR(9)
	refused.Reviews = []core.Review{{Author: "sam", State: core.ReviewChangesRequested}}

	move := engine.Transition(work(cfg, "in_review", linked(closedPR(7)), linked(refused)))

	if move.To != "in_progress" || move.Event != config.EventChangesRequested {
		t.Fatalf("move = %s → %s on %s, want in_review → in_progress on changes_requested", move.From, move.To, move.Event)
	}
}

// abandoning the last pull request a ticket has means the work came back to
// somebody, which is the whole of what spec 04 §5 rule 1 allows a back-move
// for. Before this the only configuration that fired said abandoned work was
// finished (issue #100).
func TestTransitionTakesTheWorkBackWhenTheLastPullRequestIsAbandoned(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(closedPR(7))))

	if move.To != "in_progress" || move.Event != config.EventPRClosedUnmerged {
		t.Fatalf("move = %s → %s on %s, want in_review → in_progress on pr_closed_unmerged (%+v)", move.From, move.To, move.Event, move.Steps)
	}
}

// Abandoning one of three pull requests leaves two. The ticket is still being
// worked on and the board should go on saying so.
func TestTransitionKeepsTheTicketWhereItIsWhileOtherWorkIsOpen(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(closedPR(7)), linked(openPR(9))))

	if move.Moves() {
		t.Fatalf("abandoning #7 took the ticket to %s while #9 is still open (%+v)", move.To, move.Steps)
	}
	got := step(t, move, config.EventPRClosedUnmerged)
	if got.Reason != engine.ReasonOpenWork {
		t.Fatalf("reason = %q, want %q", got.Reason, engine.ReasonOpenWork)
	}
	if !strings.Contains(got.Why, "#9") {
		t.Errorf("why = %q, and it does not name the pull request somebody is still working on", got.Why)
	}
}

// A ticket with a merge behind it is further on than any closed tab. The
// trunk has part of the answer and abandoning the rest does not take it back.
func TestTransitionDoesNotTakeBackATicketThatHasAlreadyLandedSomething(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7)), linked(closedPR(9))))

	if move.To == "in_progress" {
		t.Fatalf("abandoning #9 took the ticket back to in_progress though #7 is on the trunk (%+v)", move.Steps)
	}
	got := step(t, move, config.EventPRClosedUnmerged)
	if got.Reason != engine.ReasonMerged {
		t.Fatalf("reason = %q, want %q: a dry run has to say which of the two refusals it was", got.Reason, engine.ReasonMerged)
	}
	if !strings.Contains(got.Why, "#7") {
		t.Errorf("why = %q, and it does not name the merge that holds the ticket where it is", got.Why)
	}
	// The tracker is about to move this ticket to Done on the merge, and
	// that is what the one line says: a refused move back is the smaller
	// news whenever something else is going to happen anyway.
	if text := move.Text(); !strings.Contains(text, "automation") {
		t.Errorf("text = %q, want the tracker's own automation, which is what is really happening to this ticket", text)
	}
}

// Every other forward-only event keeps the refusal it had, and the sentence
// now offers the third way back without promising it unconditionally.
func TestTransitionStillRefusesAnUnlistedEventGoingBackwards(t *testing.T) {
	cfg := preset(t, "lean-kanban") // configures no pr_converted_to_draft

	move := engine.Transition(work(cfg, "in_review", linked(draftPR(7))))

	got := step(t, move, config.EventPROpenedDraft)
	if got.Reason != engine.ReasonBackwards {
		t.Fatalf("reason = %q, want %q", got.Reason, engine.ReasonBackwards)
	}
	if !strings.Contains(got.Why, "abandoned") {
		t.Errorf("why = %q, and it does not list abandoning the last pull request among the ways back", got.Why)
	}
}

// Rule 1 and rule 4 read one count of one set of pull requests, so a ticket
// cannot be told that nothing is left of its work and that its work is
// unfinished in the same breath. Here #7 is merged, #9 is open and #11 was
// abandoned: the ticket is neither going back nor done, and each refusal
// names the pull request that is really holding it.
func TestTransitionCountsTheWorkOnceForBothRules(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	cfg.Workflow.Transitions[config.EventPRMerged] = config.Transition{To: "done", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7)), linked(openPR(9)), linked(closedPR(11))))

	if move.Moves() {
		t.Fatalf("the ticket moved to %s with #7 merged, #9 open and #11 abandoned (%+v)", move.To, move.Steps)
	}
	back := step(t, move, config.EventPRClosedUnmerged)
	if back.Reason != engine.ReasonMerged || !strings.Contains(back.Why, "#7") {
		t.Errorf("pr_closed_unmerged = %q %q, want %q naming #7", back.Reason, back.Why, engine.ReasonMerged)
	}
	done := step(t, move, config.EventPRMerged)
	if done.Reason != engine.ReasonOpenWork || !strings.Contains(done.Why, "#9") {
		t.Errorf("pr_merged = %q %q, want %q naming #9", done.Reason, done.Why, engine.ReasonOpenWork)
	}
}

// When the merge is the only thing holding the ticket where it is, that is
// the line a dry run prints: a person looking at a ticket that did not move
// is owed the pull request that is on the trunk, not the event nobody
// configured.
func TestTransitionSaysTheMergeIsWhyTheWorkDidNotComeBack(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	delete(cfg.Workflow.Transitions, config.EventPRMerged)

	move := engine.Transition(work(cfg, "in_review", linked(mergedPR(7)), linked(closedPR(9))))

	if move.Moves() {
		t.Fatalf("the ticket moved to %s (%+v)", move.To, move.Steps)
	}
	if text := move.Text(); !strings.Contains(text, "#7 merged") {
		t.Errorf("text = %q, and it does not say the merge is why the work did not come back", text)
	}
}

func TestTransitionGivesTheSameAnswerWhateverOrderThePullRequestsCameIn(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	refused := openPR(9)
	refused.Reviews = []core.Review{{Author: "sam", State: core.ReviewChangesRequested}}

	forwards := engine.Transition(work(cfg, "ready", linked(openPR(7)), linked(refused)))
	backwards := engine.Transition(work(cfg, "ready", linked(refused), linked(openPR(7))))

	if forwards.To != backwards.To || forwards.Event != backwards.Event {
		t.Fatalf("order changed the answer: %s on %s vs %s on %s", forwards.To, forwards.Event, backwards.To, backwards.Event)
	}
	if !slices.Equal(events(forwards), events(backwards)) {
		t.Errorf("order changed the reasoning: %v vs %v", events(forwards), events(backwards))
	}
}

func TestTransitionRefusesAColumnTheWorkflowDoesNotHave(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPROpenedDraft] = config.Transition{To: "doing", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "ready", linked(draftPR(7))))

	if move.Moves() {
		t.Fatalf("flow moved a ticket to a column that does not exist: %+v", move)
	}
	if got := step(t, move, config.EventPROpenedDraft); got.Reason != engine.ReasonUnknownColumn {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonUnknownColumn)
	}
}

func TestTransitionApproval(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventApproved] = config.Transition{To: "done", Owner: config.OwnerFlow}

	tests := []struct {
		name      string
		required  int
		approvals []string
		want      bool
	}{
		{name: "nobody has looked, and the branch asks for nobody", required: 0, want: false},
		{name: "one approval where the branch asks for nobody", required: 0, approvals: []string{"sam"}, want: true},
		{name: "one approval of the two the branch asks for", required: 2, approvals: []string{"sam"}, want: false},
		{name: "both approvals the branch asks for", required: 2, approvals: []string{"sam", "jo"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := openPR(7)
			for _, author := range tt.approvals {
				pr.Reviews = append(pr.Reviews, core.Review{Author: author, State: core.ReviewApproved})
			}
			move := engine.Transition(work(cfg, "in_review", engine.LinkedPR{PR: pr, RequiredApprovals: tt.required}))

			got := slices.Contains(events(move), config.EventApproved)
			if got != tt.want {
				t.Fatalf("approved raised = %v, want %v (events %v)", got, tt.want, events(move))
			}
		})
	}
}

// A ticket that is closed is finished work, whatever column the board has it
// in — and the board does lag, which is what doctor's transitions.native
// checks are for. This is the live bug: a pull request opened against an
// already-closed ticket took it from In progress back to In review, 15
// minutes after it was closed and merged.
func TestTransitionDoesNotTouchAClosedTicket(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	snapshot := work(cfg, "in_progress", linked(openPR(7)))
	snapshot.Ticket.Closed = true

	move := engine.Transition(snapshot)

	if move.Moves() {
		t.Fatalf("flow moved a closed ticket to %s: %+v", move.To, move)
	}
	got := step(t, move, config.EventPRReadyForReview)
	if got.Reason != engine.ReasonClosed {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonClosed)
	}
	if !strings.Contains(got.Why, "closed") {
		t.Errorf("why = %q, want it to say the ticket is closed", got.Why)
	}
}

// The line a dry run prints has to say that the ticket is finished. Where no
// other event has anything to report — here the workflow leaves the closing to
// nobody — the alternative is the pull request the ticket is already in the
// column of, which reads like work still under way.
func TestTransitionSaysWhyAClosedTicketStaysPut(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	delete(cfg.Workflow.Transitions, config.EventIssueClosed)
	snapshot := work(cfg, "in_progress", linked(openPR(7)))
	snapshot.Ticket.Closed = true

	move := engine.Transition(snapshot)

	if !strings.Contains(move.Text(), "the only move left in finished work") {
		t.Errorf("the line a dry run prints is %q, want it to say the ticket is closed", move.Text())
	}
}

// Refusing is not the same as not looking: the pull requests are still
// considered, so a dry run can say which ones it declined and why.
func TestTransitionStillExplainsThePullRequestsOfAClosedTicket(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	snapshot := work(cfg, "in_progress", linked(openPR(7)))
	snapshot.Ticket.Closed = true

	move := engine.Transition(snapshot)

	if !slices.Contains(events(move), config.EventPRReadyForReview) {
		t.Errorf("events = %v, want the pull request's events considered and refused", events(move))
	}
	for _, s := range move.Steps {
		if s.Moved {
			t.Errorf("step %s moved a closed ticket: %+v", s.Event, s)
		}
	}
}

// An archived ticket is one the board has put out of sight, and the tracker
// will not write to it — GitHub Projects accepts the mutation and performs
// nothing. Flow therefore decides no move for one: a move it asked for here
// would be reported as made and would not be.
func TestTransitionDoesNotMoveAnArchivedTicket(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	snapshot := work(cfg, "ready", linked(openPR(7)))
	snapshot.Ticket.Archived = true

	move := engine.Transition(snapshot)

	if move.Moves() {
		t.Fatalf("flow moved an archived ticket to %s: %+v", move.To, move)
	}
	got := step(t, move, config.EventPRReadyForReview)
	if got.Reason != engine.ReasonArchived {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonArchived)
	}
	if !strings.Contains(got.Why, "archived") {
		t.Errorf("why = %q, want it to say the ticket is archived", got.Why)
	}
	if !strings.Contains(move.Text(), "archived") {
		t.Errorf("the line a dry run prints is %q, want archiving to lead it", move.Text())
	}
}

// Being archived answers every event, including the ones that would have moved
// the ticket nowhere: a step that reported "already in the right column" would
// read like a board flow agrees with, when flow cannot touch it at all.
func TestTransitionSaysArchivedEvenWhereNothingWouldHaveMoved(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	snapshot := work(cfg, "in_review", linked(openPR(7)))
	snapshot.Ticket.Archived = true

	move := engine.Transition(snapshot)

	for _, s := range move.Steps {
		if s.Reason != engine.ReasonArchived {
			t.Errorf("step %s is %q, want every event to answer %q", s.Event, s.Reason, engine.ReasonArchived)
		}
	}
}

// The control for the two above: the same snapshot without the archiving
// moves, so the refusal is about being archived and not about the fixture.
func TestTransitionMovesTheSameTicketWhenItIsNotArchived(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(work(cfg, "ready", linked(openPR(7))))

	if !move.Moves() {
		t.Fatalf("an unarchived ticket did not move: %+v", move)
	}
}

// Closing is still the one event finished work acts on, so a workflow that
// owns it still reaches done even though every other event has been refused.
func TestTransitionClosesAClosedTicketAnyway(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventIssueClosed] = config.Transition{To: "done", Owner: config.OwnerFlow}
	snapshot := work(cfg, "in_progress", linked(mergedPR(7)))
	snapshot.Ticket.Closed = true

	move := engine.Transition(snapshot)

	if move.From != "in_progress" || move.To != "done" || move.Event != config.EventIssueClosed {
		t.Fatalf("move = %s → %s on %s, want in_progress → done on issue_closed", move.From, move.To, move.Event)
	}
}

func TestTransitionFollowsTheTicketBeingClosed(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventIssueClosed] = config.Transition{To: "done", Owner: config.OwnerFlow}
	snapshot := work(cfg, "in_review")
	snapshot.Ticket.Closed = true

	move := engine.Transition(snapshot)

	if move.To != "done" || move.Event != config.EventIssueClosed {
		t.Fatalf("move = %s → %s on %s, want in_review → done on issue_closed", move.From, move.To, move.Event)
	}
}

// The conversion is read off the ticket's position, so a pull request that was
// opened as a draft is never reported as one that was put back to draft.
func TestTransitionReportsAConversionOnlyWhereOneHappened(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventPRConvertedToDraft] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}

	move := engine.Transition(work(cfg, "backlog", linked(draftPR(7))))

	if move.To != "in_progress" {
		t.Fatalf("to = %q, want in_progress", move.To)
	}
	if slices.Contains(events(move), config.EventPRConvertedToDraft) {
		t.Errorf("events = %v, want no conversion for a pull request that opened as a draft", events(move))
	}
}

func TestTransitionWithoutAConfigDoesNothing(t *testing.T) {
	move := engine.Transition(engine.TransitionSnapshot{Ticket: core.Ticket{Key: "#12"}, Column: "ready"})

	if move.Moves() || len(move.Steps) != 0 || move.Warning != "" {
		t.Fatalf("a snapshot with no workflow produced a decision: %+v", move)
	}
}

func TestTransitionText(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	moved := engine.Transition(work(cfg, "ready", linked(draftPR(7))))
	if got, want := moved.Text(), "#12  ready → in_progress  (pr_opened_draft)"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}

	native := engine.Transition(work(cfg, "in_review", linked(mergedPR(7))))
	if got := native.Text(); !strings.Contains(got, "stays in in_review") || !strings.Contains(got, "automation") {
		t.Errorf("Text() = %q, want it to say the ticket stays put and why", got)
	}

	quiet := engine.Transition(work(cfg, "in_review", linked(openPR(7))))
	if got := quiet.Text(); !strings.Contains(got, "already in In review") {
		t.Errorf("Text() = %q, want it to say the ticket is where it belongs", got)
	}

	idle := engine.Transition(work(cfg, "ready"))
	if got := idle.Text(); !strings.Contains(got, "nothing has happened") {
		t.Errorf("Text() = %q, want it to say nothing asked the ticket to move", got)
	}

	unmapped := engine.Transition(work(cfg, "", linked(openPR(7))))
	if got := unmapped.Text(); !strings.HasPrefix(got, "#12  skipped — ") {
		t.Errorf("Text() = %q, want the skip and its reason", got)
	}
}

// startsOnABranch is the workflow the proposal asks for: the work begins when
// somebody cuts a branch for the ticket, before any pull request exists.
func startsOnABranch(t *testing.T) *config.Config {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	cfg.Workflow.Transitions[config.EventBranchCreated] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	return cfg
}

// branched is a snapshot of a ticket the tracker links some branches to.
func branched(cfg *config.Config, column string, branches []string, prs ...engine.LinkedPR) engine.TransitionSnapshot {
	snap := work(cfg, column, prs...)
	snap.Ticket.Branches = branches
	return snap
}

func TestTransitionStartsTheWorkWhenABranchIsCut(t *testing.T) {
	cfg := startsOnABranch(t)

	move := engine.Transition(branched(cfg, "ready", []string{"12-tidy-the-icons"}))

	if !move.Moves() {
		t.Fatalf("a branch did not start the work: %+v", move)
	}
	if move.From != "ready" || move.To != "in_progress" {
		t.Errorf("moved %s → %s, want ready → in_progress", move.From, move.To)
	}
	if move.Event != config.EventBranchCreated {
		t.Errorf("event = %s, want %s", move.Event, config.EventBranchCreated)
	}
}

// A branch is the one thing a ticket can have that is not a pull request, so
// a ticket the tracker links no branch to must not be asked about as though
// it had one.
func TestTransitionAsksNothingAboutABranchTheTrackerNamesNone(t *testing.T) {
	cfg := startsOnABranch(t)

	move := engine.Transition(branched(cfg, "ready", nil))

	if move.Moves() {
		t.Fatalf("a ticket with no branch was moved to %s", move.To)
	}
	if got := events(move); slices.Contains(got, config.EventBranchCreated) {
		t.Errorf("events = %v, want branch_created not to be among them", got)
	}
}

// The event is configuration like any other: a team that has not asked for it
// gets the first pull request as the start of the work, however many branches
// the tracker knows about.
func TestTransitionIgnoresABranchTheWorkflowDoesNotAskAbout(t *testing.T) {
	cfg := preset(t, "lean-kanban")

	move := engine.Transition(branched(cfg, "ready", []string{"12-tidy-the-icons"}))

	if move.Moves() {
		t.Fatalf("a branch moved the ticket to %s under a workflow that configures no branch_created", move.To)
	}
	if got := step(t, move, config.EventBranchCreated); got.Reason != engine.ReasonUnconfigured {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonUnconfigured)
	}
}

// Rule 4 (spec 04 §5): the branch is one opinion among the pull requests' and
// never an override of them, so work already in review stays in review.
func TestTransitionLetsAPullRequestOverruleTheBranchItCameFrom(t *testing.T) {
	cfg := startsOnABranch(t)

	move := engine.Transition(branched(cfg, "ready", []string{"12-tidy-the-icons"}, linked(openPR(7))))

	if move.To != "in_review" {
		t.Fatalf("to = %q, want in_review (%+v)", move.To, move.Steps)
	}
	got := step(t, move, config.EventBranchCreated)
	if got.Moved || got.Reason != engine.ReasonOverruled {
		t.Fatalf("the branch step is %+v, want it overruled by the pull request", got)
	}
	if !strings.Contains(got.Why, "#7") {
		t.Errorf("why = %q, want it to name #7 as the request that held the ticket further on", got.Why)
	}
}

// And the other way round: the last pull request was abandoned, but somebody's
// branch is still there, so the work has not gone back to Ready.
func TestTransitionKeepsWorkStartedWhileTheBranchIsStillThere(t *testing.T) {
	cfg := startsOnABranch(t)
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "ready", Owner: config.OwnerFlow}

	move := engine.Transition(branched(cfg, "in_progress", []string{"12-tidy-the-icons"}, linked(closedPR(7))))

	if move.Moves() {
		t.Fatalf("the work went back to %s while its branch still existed", move.To)
	}
	got := step(t, move, config.EventPRClosedUnmerged)
	if got.Moved || got.Reason != engine.ReasonOverruled {
		t.Fatalf("the abandoned pull request's step is %+v, want it overruled by the branch", got)
	}
	if !strings.Contains(got.Why, "the branch 12-tidy-the-icons") {
		t.Errorf("why = %q, want it to name the branch that held the work where it is", got.Why)
	}
}

// Several branches are counted rather than listed: which of them it was is
// nobody's decision, and five names say less than the number does.
func TestTransitionCountsBranchesRatherThanListingThem(t *testing.T) {
	cfg := startsOnABranch(t)
	cfg.Workflow.Transitions[config.EventPRClosedUnmerged] = config.Transition{To: "ready", Owner: config.OwnerFlow}
	branches := []string{"12-tidy-the-icons", "12-tidy-the-icons-again"}

	move := engine.Transition(branched(cfg, "in_progress", branches, linked(closedPR(7))))

	got := step(t, move, config.EventPRClosedUnmerged)
	if !strings.Contains(got.Why, "2 branches") {
		t.Errorf("why = %q, want it to count the branches", got.Why)
	}
	for _, branch := range branches {
		if strings.Contains(got.Why, branch) {
			t.Errorf("why = %q, want it not to name %q", got.Why, branch)
		}
	}
}

// A branch outlives the start of the work, so a ticket past In progress must
// not be dragged back to it every time the audit looks (spec 04 §5 rule 2).
func TestTransitionDoesNotTakeWorkBackToTheBranchItStartedOn(t *testing.T) {
	cfg := startsOnABranch(t)

	move := engine.Transition(branched(cfg, "in_review", []string{"12-tidy-the-icons"}))

	if move.Moves() {
		t.Fatalf("a branch took work in review back to %s", move.To)
	}
	if got := step(t, move, config.EventBranchCreated); got.Reason != engine.ReasonBackwards {
		t.Errorf("reason = %q, want %q", got.Reason, engine.ReasonBackwards)
	}
}
