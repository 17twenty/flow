package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit, flow's own half (spec 07 §6, 04 §5).
//
// `live.transitions.native.<event>` audits the moves flow declined to make,
// and can only ever report them: somebody else's automation is somebody
// else's to switch on. These are the moves flow owns, and the reason the
// board can be wrong about them is never that flow decided differently — the
// decision is deterministic — but that flow was never asked. A CI run that
// did not start, a webhook the forge dropped, a credential that had expired
// at eleven on Friday: in all three the event happened, nobody told flow, and
// the ticket is still in the column before.
//
// So this is the one live check whose remedy is flow's own: `doctor --fix`
// performs the moves rather than printing a sentence for somebody to carry
// out by hand. It is the catch-up of spec 07 §6, and it is what makes a
// missed `flow check pr` a delay rather than a hole in the board.

// CheckLiveTransitionsFlow is whether the board shows the moves flow owns.
const CheckLiveTransitionsFlow = "live.transitions.flow"

// transitionsFlow finds the moves flow owns and has not made, and under --fix
// makes them.
//
// It is one check for every event rather than one per event, which is where
// it parts company with the native side. The native side can ask per event
// because each event is a separate claim about a separate automation. Here
// there is one decision — [Transition] folds everything a ticket's work says
// about it into the single column the ticket belongs in — and one act that
// settles it. Asking per event would run that fold once per event, count the
// same ticket as many times as it had work, and leave flow deciding one event
// at a time what the engine is careful to decide whole.
func (a *audit) transitionsFlow() Check {
	return Check{
		ID:        CheckLiveTransitionsFlow,
		Title:     "The board shows the moves flow performs",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if len(flowEvents(env.Config)) == 0 {
				return Skip("no transition in this workflow is flow's to perform")
			}
			board, err := a.board(ctx, env)
			if err != nil {
				return Failed(err)
			}
			// Strictly, unlike the checks that only read: a ticket's column
			// depends on all of its work (spec 04 §5 rule 4), so a pull
			// request flow failed to read is one that could have been the
			// reason not to move. Under --fix that is the difference between
			// catching up and putting a ticket somewhere it does not belong.
			work, err := a.work(ctx, env)
			if err != nil {
				return Failed(err)
			}
			running, err := a.deploymentsFor(ctx, env)
			if err != nil {
				return Failed(err)
			}

			deadline := env.Now().Add(-TransitionGrace)
			var t tally
			var waiting int
			var caught []Evidence
			for _, ticket := range board {
				theirs := work[ticketKey(ticket)]
				if len(theirs) == 0 && !closedInWindow(env, ticket) && !branchStarted(env.Config, ticket) {
					continue
				}
				linked, err := a.linkedPRs(ctx, env, theirs, running, deadline)
				if err != nil {
					return Failed(err)
				}
				move := Transition(TransitionSnapshot{
					Ticket: ticket,
					Column: ColumnOf(env.Config, env.Lock, ticket),
					PRs:    linked,
					Config: env.Config,
				})
				switch {
				case move.Warning != "":
					// The board has it in a status the workflow does not
					// name. Flow does not guess where that belongs and will
					// not move it (spec 04 §5 rule 3), so it is not evidence
					// about anything either.
					continue
				case !move.Moves():
					t.held()
					continue
				}
				at, datable := owedSince(move, ticket, theirs, deadline)
				switch {
				case !datable:
					continue
				case at.After(deadline):
					waiting++
					continue
				}
				target, _ := env.Config.Workflow.Column(move.To)
				if !env.Fix {
					t.broke(ticket.URL, "%s %s — %s %s, and it is still in %s",
						ticket.Key, ticket.Title, movedBy(move.Event), day(at), whereItIs(env, ticket))
					continue
				}
				if err := catchUp(ctx, env, ticket, move); err != nil {
					t.broke(ticket.URL, "%s %s — flow could not move it to %s: %v",
						ticket.Key, ticket.Title, target.Name, err)
					continue
				}
				t.held()
				caught = append(caught, Evidence{
					Text: fmt.Sprintf("%s %s — %s %s, so flow moved it from %s to %s",
						ticket.Key, ticket.Title, movedBy(move.Event), day(at),
						whereItIs(env, ticket), target.Name),
					URL: ticket.URL,
				})
			}

			result := t.grade(env, false)
			for _, evidence := range caught {
				result = result.Saw(evidence.Text, evidence.URL)
			}
			switch {
			case t.counts.Total == 0 && waiting > 0:
				return result.Said("the %d %s flow owes the board %s less than %d minutes old",
					waiting, plural(waiting, "move"), isAre(waiting), int(TransitionGrace/time.Minute))
			case t.counts.Total == 0:
				return result.Said("nothing in the window needed flow to move a ticket")
			case len(caught) > 0 && result.State == StatePass:
				return result.Said("flow caught up %d %s; all %d %s where the workflow says",
					len(caught), plural(len(caught), "move"), t.counts.Total,
					areIs(t.counts.Total, "ticket"))
			case result.State == StatePass:
				return result.Said("all %d %s where flow's transitions put them",
					t.counts.Total, areIs(t.counts.Total, "ticket"))
			}
			return result.Said("%d of %d tickets %s not where flow's transitions should have put them",
				t.missed(), t.counts.Total, isAre(t.missed())).
				Fix(catchUpRemedy(env, t.evidence))
		},
	}
}

// catchUpRemedy is what to do about moves flow owns and has not made. A run
// that was not allowed to fix anything has the whole remedy in one command;
// a run that tried and was refused does not, so it says what it hit instead
// of offering the command that just failed.
func catchUpRemedy(env *Env, refused []Evidence) Remediation {
	if !env.Fix {
		return Remediation{
			Kind:    RemedyAuto,
			Summary: "flow can make these moves itself",
			Command: "flow doctor --fix",
		}
	}
	return Remediation{
		Kind:    RemedyManual,
		Summary: fmt.Sprintf("flow tried to move %d %s and could not", len(refused), plural(len(refused), "ticket")),
		Steps: []string{
			"check auth.tracker in this report: moving an item needs a credential that may write to the board",
			"check tracker.status_options: a column the board does not have is one no ticket can be moved to",
			"move them by hand if the board is urgent; the next run will agree with whatever you do",
		},
	}
}

// catchUp performs one move flow owed the board.
func catchUp(ctx context.Context, env *Env, ticket core.Ticket, move TicketMove) error {
	if env.Adapters.Tracker == nil {
		return errors.New("there is no tracker adapter to ask")
	}
	to, found := columnTarget(env.Config, move.To)
	if !found {
		return fmt.Errorf("%q is not a column in workflow.columns", move.To)
	}
	if err := env.Adapters.Tracker.Move(ctx, ticket.Key, to); err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info("doctor caught up a transition flow owns",
			"ticket", ticket.Key, "from", move.From, "to", move.To, "event", move.Event)
	}
	return nil
}

// flowEvents is the transitions the configuration gives flow, in the order
// spec 04 §5 lists the events.
func flowEvents(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var events []string
	for event, transition := range cfg.Workflow.Transitions {
		if transition.Owner == config.OwnerFlow && transition.To != "" {
			events = append(events, event)
		}
	}
	slices.Sort(events)
	return events
}

// work is every pull request the window knows about, indexed by the ticket it
// is for, or the reason flow could not read them all.
func (a *audit) work(ctx context.Context, env *Env) (map[string][]core.PullRequest, error) {
	merged, err := a.merged(ctx, env)
	if err != nil {
		return nil, err
	}
	open, err := a.inFlight(ctx, env)
	if err != nil {
		return nil, err
	}
	return prsByTicket(env, append(slices.Clone(merged), open...)), nil
}

// closedInWindow reports that a ticket was closed recently enough for the
// audit to be about it. A closed ticket is the one occasion a ticket with no
// pull requests at all still implies an event.
func closedInWindow(env *Env, ticket core.Ticket) bool {
	if !ticket.Closed {
		return false
	}
	at := finished(ticket)
	return !at.IsZero() && !at.Before(env.Since())
}

// deploymentsFor is the deployment history the catch-up needs, which is none
// unless some transition flow owns is about a deployment.
//
// When one is, the records are not optional. A `deployed` event flow cannot
// see is a move flow would never make, and a check that quietly skipped it
// would report a board that is permanently one column short as correct.
func (a *audit) deploymentsFor(ctx context.Context, env *Env) ([]core.Deployment, error) {
	transition, configured := env.Config.Workflow.Transitions[config.EventDeployed]
	if !configured || transition.Owner != config.OwnerFlow || transition.To == "" {
		return nil, nil
	}
	if env.Adapters.Deploys == nil {
		return nil, errNoDeploys
	}
	return a.shipped(ctx, env)
}

// linkedPRs is a ticket's work in the form [Transition] reads it.
//
// Deployment is resolved at the grace deadline rather than now, which is what
// gives the `deployed` event the same fifteen minutes every other event gets:
// a merge that reached the environment a minute ago is not yet a move flow is
// late with. RequiredApprovals is left at zero, where [approved] reads it as
// one: the catch-up is about events that already happened, and a repository
// that demands two approvals has two recorded by the time it matters.
func (a *audit) linkedPRs(ctx context.Context, env *Env, prs []core.PullRequest, running []core.Deployment, deadline time.Time) ([]LinkedPR, error) {
	linked := make([]LinkedPR, 0, len(prs))
	for _, pr := range prs {
		shipped := false
		if len(running) > 0 && pr.Merged() {
			was, err := runningBy(ctx, env.Comparer(), pr, running, deadline)
			if err != nil {
				return nil, err
			}
			shipped = was
		}
		linked = append(linked, LinkedPR{PR: pr, Deployed: shipped})
	}
	return linked, nil
}

// owedSince is when the move became flow's to make, and whether flow can tell.
//
// Several pull requests can ask for the same move, and it is the first of
// them that the grace period runs from: the move has been owed since the
// earliest event that asked for it. Measuring from the most recent would be
// the generous reading, but generosity costs something here and buys nothing.
// It costs a move that has been missing for three days waiting another
// quarter of an hour because somebody opened a second pull request. It buys
// avoiding a race with the `flow check pr` that the newest event is probably
// running right now — and that race has no loser, because both of them would
// perform the same move to the same column, which is what spec 04 §5 rule 5
// makes idempotent.
func owedSince(move TicketMove, ticket core.Ticket, prs []core.PullRequest, deadline time.Time) (time.Time, bool) {
	byNumber := make(map[int]core.PullRequest, len(prs))
	for _, pr := range prs {
		byNumber[pr.Number] = pr
	}
	var first time.Time
	for _, step := range move.Steps {
		if !step.Moved || step.Event != move.Event {
			continue
		}
		at, datable := stepAt(step, ticket, byNumber, deadline)
		if !datable {
			return time.Time{}, false
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first, !first.IsZero()
}

// branchStarted reports a ticket somebody has cut a branch for, under a workflow
// that asks flow to move work when they do. It is what lets the catch-up look
// at a ticket with no pull request at all: until somebody opens one there is
// nothing else about the ticket to notice, and noticing is the whole of the
// event.
func branchStarted(cfg *config.Config, ticket core.Ticket) bool {
	if cfg == nil || len(ticket.Branches) == 0 {
		return false
	}
	// Who owns the transition is the whole question. A workflow that names
	// no branch_created at all has no owner for it either, and one that
	// names a column flow does not have was refused before any of this ran
	// — so asking about the column here would be a second reading of what
	// the validator and the fold have each already answered.
	return cfg.Workflow.Transitions[config.EventBranchCreated].Owner == config.OwnerFlow
}

// stepAt is when the event behind one step happened.
//
// A deployment is dated at the deadline itself, because that is exactly what
// the flag means here: [audit.linkedPRs] only sets Deployed when the merge
// was running by then, so the event is at or before it and flow has no reason
// to care which.
//
// So is a branch, for the opposite reason: no tracker dates the link between
// a ticket and a branch, and the grace the date is measured against exists to
// let an in-flight CI run settle the move first. Nothing in CI moves a ticket
// when a branch appears — no pull request exists for a workflow to run on —
// so there is nobody to wait for, and the catch-up is not a catch-up here but
// the only thing that ever performs the event.
func stepAt(step TransitionStep, ticket core.Ticket, byNumber map[int]core.PullRequest, deadline time.Time) (time.Time, bool) {
	if step.Event == config.EventDeployed || step.Event == config.EventBranchCreated {
		return deadline, true
	}
	if step.PR == 0 {
		at := finished(ticket)
		return at, !at.IsZero()
	}
	pr, known := byNumber[step.PR]
	if !known {
		return time.Time{}, false
	}
	at := prEventAt(step.Event, pr)
	return at, !at.IsZero()
}

// movedBy is the event in the words of the line it appears in. An event the
// catch-up can reach but the native audit never dates still needs a phrase,
// because the evidence line is read by somebody who has to recognise what
// flow is talking about.
func movedBy(event string) string {
	if what, named := transitionSources[event]; named {
		return what
	}
	switch event {
	case config.EventApproved:
		return "its pull request was approved on"
	case config.EventPRClosedUnmerged:
		return "its pull request was closed unmerged on"
	case config.EventPRConvertedToDraft:
		return "its pull request went back to draft on"
	case config.EventBranchCreated:
		return "a branch for it already existed by"
	case config.EventDeployed:
		return "its work reached the environment by"
	default:
		return event + " happened on"
	}
}

// whereItIs is the column name to show for a ticket, which is the board's own
// word for it when the workflow has one and the tracker's otherwise.
func whereItIs(env *Env, ticket core.Ticket) string {
	if column, found := env.Config.Workflow.Column(ColumnOf(env.Config, env.Lock, ticket)); found {
		return column.Name
	}
	return ticket.Status.Name
}

// areIs agrees the verb with what was counted, for the sentences that put the
// noun before it.
func areIs(n int, word string) string {
	if n == 1 {
		return word + " is"
	}
	return word + "s are"
}
