package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// The gate's run: what it reads before the rules judge, and what it writes
// afterwards. The rules themselves are pure and are held by policy_test.go;
// what is tested here is everything that has to ask somebody something.

// gateWorld is a repository the gate can be run against.
type gateWorld struct {
	forge   *coretest.FakeForge
	tracker *coretest.FakeTracker
	cfg     *config.Config
}

// gateFixture is a repository holding one open pull request linked to one
// ticket in Ready, which is the ordinary case every scenario varies from.
func gateFixture(t *testing.T) *gateWorld {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	pr := core.PullRequest{
		Number:        4,
		Title:         "feat(engine): publish the verdict",
		Author:        "nick",
		State:         core.PRStateOpen,
		HeadSHA:       "abc1234",
		BaseRef:       "main",
		ClosingIssues: []string{"#12"},
	}
	return &gateWorld{
		cfg:     cfg,
		forge:   coretest.NewFakeForge(coretest.ForgeFixture{PullRequests: []core.PullRequest{pr}}),
		tracker: coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: []core.Ticket{gateTicket("#12", "Ready")}}),
	}
}

// gateTicket is a ticket meeting the Definition of Ready, in one column.
func gateTicket(key, column string) core.Ticket {
	return core.Ticket{
		Key:      key,
		Title:    "publish the verdict",
		Body:     "## Acceptance criteria\n\n- the status says why\n",
		Parent:   "#1",
		Status:   core.Status{ID: column, Name: column},
		ColumnID: core.ColumnID(column),
	}
}

// unreadyTicket is a ticket nobody has written acceptance criteria on, which
// is the simplest way for the Definition of Ready to fail.
func unreadyTicket(key, column string) core.Ticket {
	ticket := gateTicket(key, column)
	ticket.Body = "we should probably do this at some point"
	return ticket
}

// input is the gate's input for this world.
func (w *gateWorld) input() engine.GateInput {
	return engine.GateInput{
		PR:       4,
		Config:   w.cfg,
		Adapters: engine.Adapters{Forge: w.forge, Tracker: w.tracker},
	}
}

// pullRequest is one pull request as the forge holds it, for a test that
// wants to change a detail of it.
func (w *gateWorld) pullRequest(t *testing.T, number int) core.PullRequest {
	t.Helper()
	pr, err := w.forge.PullRequest(context.Background(), number)
	if err != nil {
		t.Fatalf("read pull request #%d: %v", number, err)
	}
	return pr
}

// snapshot gathers the world, failing the test when it cannot be read.
func (w *gateWorld) snapshot(t *testing.T) engine.PolicySnapshot {
	t.Helper()
	snapshot, err := engine.GateSnapshot(context.Background(), w.input())
	if err != nil {
		t.Fatalf("gather the snapshot: %v", err)
	}
	return snapshot
}

// run runs the whole gate, failing the test when it returns an error.
func (w *gateWorld) run(t *testing.T, adjust ...func(*engine.GateInput)) engine.Gate {
	t.Helper()
	in := w.input()
	for _, change := range adjust {
		change(&in)
	}
	gate, err := engine.RunGate(context.Background(), in)
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	return gate
}

// TestGateSnapshotNeedsSomethingToEnforce refuses to judge a pull request
// against nothing. An unconfigured repository is a mistake to report, not a
// gate that passes everything.
func TestGateSnapshotNeedsSomethingToEnforce(t *testing.T) {
	world := gateFixture(t)
	cases := map[string]engine.GateInput{
		"no configuration": {PR: 4, Adapters: engine.Adapters{Forge: world.forge, Tracker: world.tracker}},
		"no forge":         {PR: 4, Config: world.cfg, Adapters: engine.Adapters{Tracker: world.tracker}},
		"no tracker":       {PR: 4, Config: world.cfg, Adapters: engine.Adapters{Forge: world.forge}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := engine.GateSnapshot(context.Background(), in); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("GateSnapshot returned %v, want ErrValidation", err)
			}
		})
	}
}

// TestGateSnapshotFindsARenamedColumnThroughTheLock is spec 04 §2: a column
// somebody renamed on the board is still the column flow created, because the
// lock recorded the identifier and the name it was created under.
func TestGateSnapshotFindsARenamedColumnThroughTheLock(t *testing.T) {
	world := gateFixture(t)
	renamed := gateTicket("#12", "Up next")
	renamed.ColumnID = "opt-ready"
	world.tracker.SetTicket(renamed)

	if column := world.snapshot(t).Tickets[0].Column; column != "" {
		t.Fatalf("a column named nowhere in the configuration resolved to %q", column)
	}

	in := world.input()
	in.Lock = &config.Lock{Created: []config.Creation{{
		Kind: engine.CreationKind(core.ItemTrackerStatusOptions),
		ID:   "opt-ready",
		Name: "Ready",
	}}}
	snapshot, err := engine.GateSnapshot(context.Background(), in)
	if err != nil {
		t.Fatalf("gather the snapshot: %v", err)
	}
	if column := snapshot.Tickets[0].Column; column != "ready" {
		t.Errorf("the renamed column resolved to %q, want ready", column)
	}
}

// TestColumnOfPrefersTheNameOnTheBoard holds the order the two answers are
// tried in. A column flow created and the team then repurposed under another
// configured name is where the board says it is, not where the lock remembers
// putting it.
func TestColumnOfPrefersTheNameOnTheBoard(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	lock := &config.Lock{Created: []config.Creation{{
		Kind: engine.CreationKind(core.ItemTrackerStatusOptions),
		ID:   "opt-ready",
		Name: "Ready",
	}}}
	ticket := gateTicket("#12", "In review")
	ticket.ColumnID = "opt-ready"

	if column := engine.ColumnOf(cfg, lock, ticket); column != "in_review" {
		t.Errorf("ColumnOf is %q, want in_review: the board's own name comes first", column)
	}
	if column := engine.ColumnOf(nil, lock, ticket); column != "" {
		t.Errorf("ColumnOf without a configuration is %q, want empty", column)
	}
}

// TestGateSnapshotReportsKeysTheTrackerHasNot is spec 04 §3: a key a pull
// request names that the tracker cannot produce is evidence, not a ticket.
func TestGateSnapshotReportsKeysTheTrackerHasNot(t *testing.T) {
	world := gateFixture(t)
	pr, err := world.forge.PullRequest(context.Background(), 4)
	if err != nil {
		t.Fatalf("read the pull request: %v", err)
	}
	pr.ClosingIssues = []string{"#12", "#99"}
	world.forge.SetPullRequest(pr)

	snapshot := world.snapshot(t)
	if len(snapshot.Tickets) != 1 || snapshot.Tickets[0].Ticket.Key != "#12" {
		t.Fatalf("the snapshot holds %d tickets, want only #12", len(snapshot.Tickets))
	}
	if got := snapshot.Unlinkable; len(got) != 1 || got[0] != "#99" {
		t.Errorf("the unlinkable keys are %v, want [#99]", got)
	}
}

// TestGateSnapshotStopsWhenTheTrackerBreaks separates a ticket that is not
// there from a tracker that will not say. The first is evidence; the second is
// a verdict nobody should trust, so the run fails instead of judging a pull
// request against half a board.
func TestGateSnapshotStopsWhenTheTrackerBreaks(t *testing.T) {
	world := gateFixture(t)
	world.tracker.Fail("Ticket", errors.New("the tracker is down"))

	_, err := engine.GateSnapshot(context.Background(), world.input())
	if err == nil {
		t.Fatal("GateSnapshot succeeded with a tracker that would not answer")
	}
	if !strings.Contains(err.Error(), "#12") {
		t.Errorf("the error does not say which ticket it could not read: %v", err)
	}
}

// TestGateSnapshotCountsWhatIsInFlight buckets the board into the two columns
// the WIP rules read, and leaves everything else out of them.
func TestGateSnapshotCountsWhatIsInFlight(t *testing.T) {
	world := gateFixture(t)
	world.tracker.SetTicket(gateTicket("#20", "In progress"))
	world.tracker.SetTicket(gateTicket("#21", "In review"))
	world.tracker.SetTicket(gateTicket("#22", "In review"))
	world.tracker.SetTicket(gateTicket("#23", "Backlog"))
	world.tracker.SetTicket(gateTicket("#24", "Somewhere else"))

	wip := world.snapshot(t).WIP
	if got := keysOf(wip.InProgress); len(got) != 1 || got[0] != "#20" {
		t.Errorf("in progress holds %v, want [#20]", got)
	}
	if got := keysOf(wip.Review); len(got) != 2 {
		t.Errorf("in review holds %v, want two items", got)
	}
	for _, item := range append(wip.Review, wip.InProgress...) {
		if !item.Since.IsZero() {
			t.Errorf("%s claims to have entered its column at %s; no tracker said so", item.Key, item.Since)
		}
	}
}

// keysOf is the ticket keys of some work in flight.
func keysOf(items []engine.WIPItem) []string {
	var keys []string
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	return keys
}

// boardlessTracker is a tracker whose board does not hold every ticket it can
// produce, which is what github-projects is: an issue and a board item are
// different things.
type boardlessTracker struct {
	*coretest.FakeTracker
	missing string
}

// Tickets is the board, which this one is not on.
func (b *boardlessTracker) Tickets(ctx context.Context, q core.TicketQuery) ([]core.Ticket, error) {
	tickets, err := b.FakeTracker.Tickets(ctx, q)
	if err != nil {
		return nil, err
	}
	kept := tickets[:0]
	for _, ticket := range tickets {
		if ticket.Key != b.missing {
			kept = append(kept, ticket)
		}
	}
	return kept, nil
}

// TestGateSnapshotReadsTheBoardForMembership holds `pr.ticket.in_project` to
// the board listing rather than to a second question about the ticket. An
// issue the tracker will happily produce may still not be an item of the
// project, and that is exactly the case the rule exists for.
func TestGateSnapshotReadsTheBoardForMembership(t *testing.T) {
	world := gateFixture(t)
	in := world.input()
	in.Adapters.Tracker = &boardlessTracker{FakeTracker: world.tracker, missing: "#12"}

	snapshot, err := engine.GateSnapshot(context.Background(), in)
	if err != nil {
		t.Fatalf("gather the snapshot: %v", err)
	}
	if len(snapshot.Tickets) != 1 {
		t.Fatalf("the snapshot holds %d tickets, want one", len(snapshot.Tickets))
	}
	if snapshot.Tickets[0].InProject {
		t.Error("a ticket missing from the board was reported as an item of the project")
	}
}

// TestRunGatePublishesAndMoves is the whole run on a pull request that breaks
// nothing: the status goes out, no comment is wanted, and the card follows the
// work.
func TestRunGatePublishesAndMoves(t *testing.T) {
	world := gateFixture(t)
	gate := world.run(t, func(in *engine.GateInput) { in.TargetURL = "https://ci.test/run/1" })

	if !gate.Pass() {
		t.Fatalf("a pull request breaking nothing failed: %+v", gate.Result.Failures())
	}
	if gate.StatusOutcome != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q", gate.StatusOutcome, engine.GatePosted)
	}
	if gate.CommentOutcome != engine.GateDeleted {
		t.Errorf("the comment outcome is %q, want %q", gate.CommentOutcome, engine.GateDeleted)
	}
	statuses, err := world.forge.CommitStatuses(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("read the statuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].TargetURL != "https://ci.test/run/1" {
		t.Fatalf("the published statuses are %+v, want one pointing at the run", statuses)
	}
	ticket, err := world.tracker.Ticket(context.Background(), "#12")
	if err != nil {
		t.Fatalf("read the ticket: %v", err)
	}
	if ticket.Status.Name != "In review" {
		t.Errorf("#12 is in %q, want In review", ticket.Status.Name)
	}
}

// TestRunGateSurvivesAReadOnlyCommentPath is the fork's second half: the
// credential could post the status and not the comment. The run records it and
// succeeds, because there is nothing whoever opened the pull request can do.
func TestRunGateSurvivesAReadOnlyCommentPath(t *testing.T) {
	world := gateFixture(t)
	world.tracker.SetTicket(unreadyTicket("#12", "Ready")) // the gate fails, and so wants a comment
	world.forge.Fail("UpsertComment", core.Errorf(core.ErrPermission, "comment").WithWhy("read-only"))

	gate := world.run(t)
	if gate.Pass() {
		t.Fatal("a ticket that is not ready passed the gate")
	}
	if gate.StatusOutcome != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q: the status went out before the comment", gate.StatusOutcome, engine.GatePosted)
	}
	if !gate.ReadOnly || gate.CommentOutcome != engine.GateReadOnly {
		t.Errorf("read-only is %v and the comment outcome %q, want true and %q", gate.ReadOnly, gate.CommentOutcome, engine.GateReadOnly)
	}
}

// TestRunGateLeavesTheBoardAloneWhenItMayNotWriteAtAll is the fork's third
// half: a credential refused the status will be refused the board too, and
// asking anyway would turn one refusal into a transition that half happened.
func TestRunGateLeavesTheBoardAloneWhenItMayNotWriteAtAll(t *testing.T) {
	world := gateFixture(t)
	world.forge.Fail("SetCommitStatus", core.Errorf(core.ErrPermission, "set a status").WithWhy("read-only"))

	gate := world.run(t)
	if !gate.ReadOnly {
		t.Fatal("a refused status did not mark the run read-only")
	}
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Errorf("a read-only run made %d moves", moves)
	}
	if len(gate.Moves) != 1 || !gate.Moves[0].Moves() {
		t.Errorf("the gate reported %+v, want the move it would have made", gate.Moves)
	}
}

// TestGateMatchesTicketKeysPeopleWriteBothWays holds the one piece of
// punctuation the forge and the tracker disagree about: a pull request says it
// closes "#12" and a board may call the same ticket "12".
func TestGateMatchesTicketKeysPeopleWriteBothWays(t *testing.T) {
	world := gateFixture(t)
	bare := gateTicket("12", "Ready")
	world.tracker = coretest.NewFakeTracker(coretest.TrackerFixture{Tickets: []core.Ticket{bare}})

	snapshot := world.snapshot(t)
	if len(snapshot.Tickets) != 1 {
		t.Fatalf("the snapshot holds %d tickets, want the one the board calls 12", len(snapshot.Tickets))
	}
	if !snapshot.Tickets[0].InProject {
		t.Error("the ticket the board calls 12 was not recognised as the #12 the pull request closes")
	}
	gate := world.run(t)
	if len(gate.Moves) != 1 || !gate.Moves[0].Moves() {
		t.Errorf("the gate decided %+v, want the move the linked pull request asks for", gate.Moves)
	}
}

// TestRunGateDoesNotMoveWhatItMayNotWrite holds the board to the same rule as
// the comment: a refusal is recorded, not raised.
func TestRunGateDoesNotMoveWhatItMayNotWrite(t *testing.T) {
	world := gateFixture(t)
	world.tracker.Fail("Move", core.Errorf(core.ErrPermission, "move the ticket").WithWhy("read-only"))

	gate := world.run(t)
	if !gate.ReadOnly {
		t.Error("a refused move did not mark the run read-only")
	}
	if len(gate.Moves) != 1 || !gate.Moves[0].Moves() {
		t.Fatalf("the gate decided %+v, want one move it then could not make", gate.Moves)
	}
}

// TestRunGateReportsATrackerThatBroke separates a refusal from a failure. A
// tracker that will not move a ticket for any other reason is a run that
// failed, and reporting it as read-only would hide a board quietly drifting.
func TestRunGateReportsATrackerThatBroke(t *testing.T) {
	world := gateFixture(t)
	world.tracker.Fail("Move", errors.New("the tracker is down"))

	_, err := engine.RunGate(context.Background(), world.input())
	if err == nil {
		t.Fatal("RunGate succeeded with a tracker that would not move anything")
	}
	if !strings.Contains(err.Error(), "#12") {
		t.Errorf("the error does not say which ticket it could not move: %v", err)
	}
}

// deletelessForge is a forge that reports there was no comment to delete,
// which is what a forge with no comment on the pull request may answer.
type deletelessForge struct {
	*coretest.FakeForge
}

// DeleteComment refuses, because there is nothing there.
func (d *deletelessForge) DeleteComment(ctx context.Context, number int, m core.Marker) error {
	return core.Errorf(core.ErrNotFound, "delete the comment").WithWhy("there is none")
}

// TestRunGateCountsNoCommentAsNothingToDo holds the one case where a missing
// comment is the right answer: the gate wanted none and found none.
func TestRunGateCountsNoCommentAsNothingToDo(t *testing.T) {
	world := gateFixture(t)
	in := world.input()
	in.Adapters.Forge = &deletelessForge{FakeForge: world.forge}

	gate, err := engine.RunGate(context.Background(), in)
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	if gate.CommentOutcome != engine.GateNone {
		t.Errorf("the comment outcome is %q, want %q", gate.CommentOutcome, engine.GateNone)
	}
}

// TestRunGateDryRunTouchesNothing holds --dry-run to deciding only.
func TestRunGateDryRunTouchesNothing(t *testing.T) {
	world := gateFixture(t)
	gate := world.run(t, func(in *engine.GateInput) { in.DryRun = true })

	if gate.StatusOutcome != engine.GateDryRun || gate.CommentOutcome != engine.GateDryRun {
		t.Errorf("the outcomes are %q/%q, want %q", gate.StatusOutcome, gate.CommentOutcome, engine.GateDryRun)
	}
	if posts := world.forge.Calls("SetCommitStatus"); posts != 0 {
		t.Errorf("a dry run posted %d statuses", posts)
	}
	if len(gate.Moves) != 1 {
		t.Fatalf("a dry run decided %d moves, want the one it would have made", len(gate.Moves))
	}
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Errorf("a dry run made %d moves", moves)
	}
	if !strings.Contains(gate.Text(), "dry run") {
		t.Errorf("the text rendering does not say nothing was posted:\n%s", gate.Text())
	}
}

// TestRunGateNoTransitionsLeavesTheBoardAlone holds --no-transitions.
func TestRunGateNoTransitionsLeavesTheBoardAlone(t *testing.T) {
	world := gateFixture(t)
	gate := world.run(t, func(in *engine.GateInput) { in.NoTransitions = true })

	if len(gate.Moves) != 0 {
		t.Errorf("--no-transitions decided %+v", gate.Moves)
	}
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Errorf("--no-transitions made %d moves", moves)
	}
	if gate.StatusOutcome != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q: the gate still gates", gate.StatusOutcome, engine.GatePosted)
	}
}

// TestRunGateDoesNotRepeatItself is spec 09 §2: a status already saying
// exactly this is left alone, and one already saying something else is not.
func TestRunGateDoesNotRepeatItself(t *testing.T) {
	world := gateFixture(t)
	if first := world.run(t); first.StatusOutcome != engine.GatePosted {
		t.Fatalf("the first run's status outcome is %q, want %q", first.StatusOutcome, engine.GatePosted)
	}
	if second := world.run(t); second.StatusOutcome != engine.GateUnchanged {
		t.Errorf("the second run's status outcome is %q, want %q", second.StatusOutcome, engine.GateUnchanged)
	}
	if posts := world.forge.Calls("SetCommitStatus"); posts != 1 {
		t.Fatalf("the gate posted %d statuses, want 1", posts)
	}

	// A verdict that changed is published, however many ran before it.
	world.tracker.SetTicket(unreadyTicket("#12", "Ready"))
	if changed := world.run(t); changed.StatusOutcome != engine.GatePosted {
		t.Errorf("a changed verdict's status outcome is %q, want %q", changed.StatusOutcome, engine.GatePosted)
	}
}

// TestGateUsesTheBranchesOwnApprovalRequirement reads the base branch's
// protection for the number of approvals the `approved` event needs, and
// treats a branch it cannot read as asking for none rather than inventing a
// requirement nobody configured.
func TestGateUsesTheBranchesOwnApprovalRequirement(t *testing.T) {
	world := gateFixture(t)
	world.cfg.Workflow.Transitions[config.EventApproved] = config.Transition{To: "done", Owner: config.OwnerFlow}
	world.forge.Fail("Protection", errors.New("no such branch"))

	gate := world.run(t)
	if len(gate.Moves) != 1 {
		t.Fatalf("the gate decided %+v, want one move", gate.Moves)
	}
	if gate.Moves[0].Event == config.EventApproved {
		t.Error("an unapproved pull request counted as approved when the branch asked for no approvals")
	}
}

// TestGateReadsTheOtherOpenWorkInFullBeforeMovingACard: spec 04 §5 rule 4
// wants all of a ticket's work considered, and a listing does not say what a
// pull request closes — spec 10 §2 puts the linkage in a read of its own. A
// gate that judged the window's summaries would see a ticket with one pull
// request where there are two, and walk a card backwards past work that is
// still in review.
func TestGateReadsTheOtherOpenWorkInFullBeforeMovingACard(t *testing.T) {
	world := gateFixture(t)
	world.cfg.Workflow.Transitions[config.EventChangesRequested] = config.Transition{To: "in_progress", Owner: config.OwnerFlow}
	world.tracker.SetTicket(gateTicket("#12", "In review"))
	world.forge.SetPullRequest(core.PullRequest{
		Number:        7,
		Title:         "feat(engine): the other half",
		Author:        "nick",
		State:         core.PRStateOpen,
		HeadSHA:       "def5678",
		BaseRef:       "main",
		ClosingIssues: []string{"#12"},
	})
	reviewed := world.pullRequest(t, 4)
	reviewed.Reviews = []core.Review{{Author: "sam", State: core.ReviewChangesRequested}}
	world.forge.SetPullRequest(reviewed)

	world.run(t)

	ticket, err := world.tracker.Ticket(context.Background(), "#12")
	if err != nil {
		t.Fatalf("read the ticket: %v", err)
	}
	if ticket.Status.Name != "In review" {
		t.Errorf("#12 is in %q, want In review: #7 is ready and nobody has reviewed it", ticket.Status.Name)
	}
}

// TestGateJSONIsOneDocument holds the --json contract: the declared schema,
// and findings that are a list even when there are none.
func TestGateJSONIsOneDocument(t *testing.T) {
	world := gateFixture(t)
	gate := world.run(t)

	raw, err := gate.JSON()
	if err != nil {
		t.Fatalf("render the gate: %v", err)
	}
	var doc struct {
		Schema   string            `json:"schema"`
		PR       int               `json:"pr"`
		SHA      string            `json:"sha"`
		Pass     bool              `json:"pass"`
		Findings []json.RawMessage `json:"findings"`
		ReadOnly *bool             `json:"read_only"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the gate's JSON does not parse: %v\n%s", err, raw)
	}
	if doc.Schema != engine.GateVersion {
		t.Errorf("the schema is %q, want %q", doc.Schema, engine.GateVersion)
	}
	if doc.PR != 4 || doc.SHA != "abc1234" || !doc.Pass {
		t.Errorf("the document describes a different run: %+v", doc)
	}
	if doc.Findings == nil {
		t.Error("findings is null; it must be a list, so that a reader can range over it")
	}
	if doc.ReadOnly != nil {
		t.Error("read_only is written out when it is false; it is omitted")
	}
}

// TestGateTextSaysWhatHappenedToTheCards renders the one thing the JSON
// document cannot: a line somebody reads without parsing anything.
func TestGateTextSaysWhatHappenedToTheCards(t *testing.T) {
	world := gateFixture(t)
	text := world.run(t).Text()

	if !strings.Contains(text, "#12") || !strings.Contains(text, "in_review") {
		t.Errorf("the text rendering does not say where #12 went:\n%s", text)
	}
	if strings.Contains(text, "not posted") {
		t.Errorf("a published status claimed it was not posted:\n%s", text)
	}
}

// TestGateTextReportsACredentialThatMayNotWrite tells whoever is reading a
// fork's log why there is no status on the pull request.
func TestGateTextReportsACredentialThatMayNotWrite(t *testing.T) {
	world := gateFixture(t)
	world.forge.Fail("SetCommitStatus", core.Errorf(core.ErrPermission, "set a status").WithWhy("read-only"))

	text := world.run(t).Text()
	if !strings.Contains(text, "may not write") {
		t.Errorf("the text rendering does not explain the missing status:\n%s", text)
	}
}

// TestRunGateSaysWhichReadFailed holds every provider call the gate makes
// before it decides to the same standard: a failure names the thing it could
// not read, so that whoever reads the CI log knows where to look.
func TestRunGateSaysWhichReadFailed(t *testing.T) {
	cases := map[string]struct {
		method  string
		adapter string
		says    string
	}{
		"the pull request": {"PullRequest", "forge", "#4"},
		"the board":        {"Tickets", "tracker", "board"},
		"the statuses":     {"CommitStatuses", "forge", "abc1234"},
		"the open work":    {"PullRequests", "forge", "pull requests"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			world := gateFixture(t)
			broken := errors.New("the provider is down")
			if tc.adapter == "forge" {
				world.forge.Fail(tc.method, broken)
			} else {
				world.tracker.Fail(tc.method, broken)
			}
			_, err := engine.RunGate(context.Background(), world.input())
			if !errors.Is(err, broken) {
				t.Fatalf("RunGate returned %v, want the provider's own failure", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error does not mention %q: %v", tc.says, err)
			}
		})
	}
}

// TestGateTextWarnsAboutACardItCannotPlace renders the ticket the gate
// declined to move because the board has it somewhere the configuration does
// not name. It is a warning rather than a move, and saying nothing at all
// would leave a card stuck with no explanation.
func TestGateTextWarnsAboutACardItCannotPlace(t *testing.T) {
	world := gateFixture(t)
	world.tracker.SetTicket(gateTicket("#12", "Somewhere else"))

	gate := world.run(t)
	if len(gate.Moves) != 1 || gate.Moves[0].Warning == "" {
		t.Fatalf("the gate decided %+v, want one warning", gate.Moves)
	}
	if moves := world.tracker.Calls("Move"); moves != 0 {
		t.Errorf("the gate moved a card it could not place (%d moves)", moves)
	}
	if text := gate.Text(); !strings.Contains(text, "#12") || !strings.Contains(text, gate.Moves[0].Warning) {
		t.Errorf("the text rendering does not explain the card it left alone:\n%s", text)
	}
}

// TestGateDrainsAColumnOverItsLimit is issue #231: a board that records no
// arrival times left the "entered review before the limit" exemption unable
// to apply, so with the review column over its limit every pull request in
// it failed — including the ones whose merge would have brought it back
// under, and nothing could ever merge. The forge says when each was opened,
// so the oldest `limit` pass and only the newest wait.
func TestGateDrainsAColumnOverItsLimit(t *testing.T) {
	world := gateFixture(t)
	opened := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	for i, key := range []string{"#30", "#31", "#32", "#33", "#34"} {
		world.tracker.SetTicket(gateTicket(key, "In review"))
		world.forge.SetPullRequest(core.PullRequest{
			Number:        40 + i,
			Title:         "feat(engine): part " + key,
			Author:        "nick",
			State:         core.PRStateOpen,
			HeadSHA:       "head" + key,
			BaseRef:       "main",
			ClosingIssues: []string{key},
			CreatedAt:     opened.Add(time.Duration(i) * time.Hour),
		})
	}

	for number, wantOK := range map[int]bool{40: true, 42: true, 43: false, 44: false} {
		in := world.input()
		in.PR = number
		snapshot, err := engine.GateSnapshot(context.Background(), in)
		if err != nil {
			t.Fatalf("snapshot of #%d: %v", number, err)
		}
		got := finding(t, engine.CheckPolicy(snapshot), engine.RuleWIPReview)
		if got.OK != wantOK {
			t.Errorf("#%d: wip.review ok=%v, want %v (%s)", number, got.OK, wantOK, got.Message)
		}
	}
}

// TestGateReadsNoArrivalsUnderTheLimit: the order only decides anything over
// the limit, and finding it costs a read per open pull request.
func TestGateReadsNoArrivalsUnderTheLimit(t *testing.T) {
	world := gateFixture(t)
	world.tracker.SetTicket(gateTicket("#30", "In review"))
	world.snapshot(t)
	if calls := world.forge.Calls("PullRequests"); calls != 0 {
		t.Errorf("the gate listed the open pull requests %d times for a column under its limit", calls)
	}
}
