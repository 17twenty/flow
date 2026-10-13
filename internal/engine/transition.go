package engine

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The transition engine (spec 04 §4–5).
//
// A board is only true if the column a ticket sits in says what is actually
// happening to it. This file decides, for one ticket, where the workflow the
// team wrote down says it belongs — and, just as importantly, where flow will
// not take it: because the tracker's own automation owns that event, because
// the move would be backwards, or because the work is not finished.
//
// It reads state, not events. A webhook payload's action is a claim about a
// moment that has already passed by the time flow reads it; the pull requests
// as they stand now are what is true. Deriving the move from state is what
// lets `flow check pr` in CI and doctor's catch-up of missed transitions
// (spec 07 §6) be the same function, so that they cannot disagree about where
// a ticket belongs. The event vocabulary stays exactly as spec 04 §4 writes
// it, because the column and the owner are configured per event: the engine
// asks which event describes a pull request's present state, and looks that
// event up.
//
// Nothing here talks to a provider. Gathering the ticket, its column and every
// pull request linked to it is the command's work.

// Reason codes for a transition flow did not perform. They are stable, so a
// caller can tell "nobody will ever do this" from "somebody else will".
const (
	// ReasonUnconfigured means the workflow names no transition for the
	// event, so nothing is supposed to happen.
	ReasonUnconfigured = "unconfigured"
	// ReasonUnknownColumn means the transition names a column the workflow
	// does not have.
	ReasonUnknownColumn = "unknown_column"
	// ReasonSatisfied means the ticket is already in the target column.
	ReasonSatisfied = "satisfied"
	// ReasonDone means the ticket is in a done column, which flow never moves
	// work out of.
	ReasonDone = "done"
	// ReasonClosed means the ticket is closed, and the only event flow will
	// act on for finished work is the closing itself.
	ReasonClosed = "closed"
	// ReasonArchived means the board has put the ticket out of sight. An
	// archived item takes no change at all, so flow does not ask for one.
	ReasonArchived = "archived"
	// ReasonNative means the tracker's own automation owns this transition.
	ReasonNative = "native"
	// ReasonBackwards means the move would go against the board's order, and
	// the event is not one of the two allowed to.
	ReasonBackwards = "backwards"
	// ReasonOpenWork means a done column was asked for while a linked pull
	// request is still open.
	ReasonOpenWork = "open_work"
	// ReasonUndeployed means a done column was asked for under
	// done_on: deployed while a merge has not reached the environment.
	ReasonUndeployed = "undeployed"
	// ReasonUnmapped means the ticket's status is not one of the configured
	// columns, so flow does not know where it is and will not guess.
	ReasonUnmapped = "unmapped"
	// ReasonOverruled means another pull request has the ticket further on.
	ReasonOverruled = "overruled"
)

// backMoves are the two events spec 04 §5 rule 1 allows to move a ticket
// against the board's order. Both of them mean the same thing — the work came
// back to somebody — and a board that could not show that would be lying for
// as long as the change took.
var backMoves = []string{config.EventChangesRequested, config.EventPRConvertedToDraft}

// LinkedPR is one pull request a ticket is for, with the two things the
// engine cannot work out for itself.
type LinkedPR struct {
	// PR is the pull request as the forge describes it.
	PR core.PullRequest
	// Deployed reports that this pull request's merge has reached
	// deploys.environment. It is only read under done_on: deployed, and
	// proving it is the command's work (spec 13 §3).
	Deployed bool
	// RequiredApprovals is how many approvals the base branch's protection
	// demands, which is what makes the `approved` event true.
	RequiredApprovals int
}

// TransitionSnapshot is one ticket, where the board has it, and every pull
// request linked to it. All of the linked pull requests are needed, not just
// the one a CI run happens to be about: a ticket is only done when all of its
// work is (spec 04 §5 rule 4).
type TransitionSnapshot struct {
	// Ticket is the ticket as the tracker describes it.
	Ticket core.Ticket
	// Column is the configured column id the ticket sits in, resolved through
	// the lock by whoever built the snapshot, and empty when the ticket is in
	// a status the configuration does not name.
	Column string
	// PRs are the pull requests linked to the ticket, in any order.
	PRs []LinkedPR
	// Config is the workflow the team agreed to.
	Config *config.Config
}

// TransitionStep is one event the snapshot implies, what the workflow says
// about it, and what the engine decided. Every event considered appears, in
// the order it was considered, so that a dry run explains itself.
type TransitionStep struct {
	// Event is the event from spec 04 §4's vocabulary.
	Event string `json:"event"`
	// PR is the pull request the event came from, zero for an event about the
	// ticket itself.
	PR int `json:"pr,omitempty"`
	// To is the column id the workflow sends this event to, empty when it
	// configures none.
	To string `json:"to,omitempty"`
	// Owner is who performs it: flow or native.
	Owner string `json:"owner,omitempty"`
	// Moved reports that this step is one flow will act on.
	Moved bool `json:"moved,omitempty"`
	// Reason is the code for why it will not, empty when Moved.
	Reason string `json:"reason,omitempty"`
	// Why is the same thing in a sentence.
	Why string `json:"why,omitempty"`
}

// TicketMove is where one ticket should be, and how that was decided.
type TicketMove struct {
	// Key is the ticket's key.
	Key string `json:"ticket"`
	// From is the configured column id it is in now.
	From string `json:"from,omitempty"`
	// To is the column id flow will move it to, empty when flow will not move
	// it at all.
	To string `json:"to,omitempty"`
	// Event is the event that asked for the last move flow will make.
	Event string `json:"event,omitempty"`
	// Steps are every event considered, in order.
	Steps []TransitionStep `json:"steps,omitempty"`
	// Warning is set when the ticket was skipped without being considered:
	// the board has it somewhere the configuration does not name (spec 04 §5
	// rule 3).
	Warning string `json:"warning,omitempty"`
}

// Moves reports whether flow has a move to make.
func (m TicketMove) Moves() bool { return m.To != "" && m.To != m.From }

// Transition decides where one ticket belongs: every pull request linked to it
// says where it would have the ticket, and the one furthest along the board
// wins. That is spec 04 §5 rule 4 — the review column when *any* linked pull
// request is ready — and it is why each pull request is judged from the
// ticket's real column rather than from wherever the one before it got to. A
// colleague's review asking for changes on a second pull request does not undo
// the first one being in review.
//
// Starting from the ticket's real column is also what makes the decision
// idempotent (rule 5), and what makes it safe to ignore natively owned events:
// whatever the tracker's own automation has already done is in that column
// before flow looks.
//
// A closed ticket is the exception to all of it. Finished work is finished
// wherever the board happens to have it, so the only event flow will act on is
// the closing itself; every pull request linked to it is still considered, and
// refused, so that a dry run says why. Without that, a board column lagging
// behind a close — which is precisely what doctor's `transitions.native.*`
// checks exist to notice — would let a new pull request naming an old ticket
// drag it back to in review, and flow would be the thing making the board
// untrue.
func Transition(s TransitionSnapshot) TicketMove {
	move := TicketMove{Key: s.Ticket.Key, From: s.Column}
	if s.Config == nil {
		return move
	}
	at := s.Config.Workflow.ColumnIndex(s.Column)
	if at < 0 {
		move.Warning = unmappedWhy(s.Ticket)
		return move
	}

	f := fold{cfg: s.Config, snap: s, at: at, move: move}
	var requests []request
	for _, linked := range sortedPRs(s.PRs) {
		f.at, f.asked, f.event = at, false, ""
		first := len(f.move.Steps)
		f.pullRequest(linked)
		if f.asked {
			requests = append(requests, request{
				pr:    linked.PR.Number,
				to:    f.at,
				event: f.event,
				first: first,
				last:  len(f.move.Steps),
			})
		}
	}

	f.at = at
	if winner, found := furthest(requests); found {
		f.at = requests[winner].to
		f.move.Event = requests[winner].event
		f.overrule(requests, winner)
	}

	// Closing the ticket is the one event that is about the ticket and not
	// about any pull request, and the only one a closed ticket acts on at
	// all, so it is considered once and last.
	if s.Ticket.Closed {
		f.event = ""
		f.step(config.EventIssueClosed, 0)
		if f.event != "" {
			f.move.Event = f.event
		}
	}

	if to := f.cfg.Workflow.Columns[f.at].ID; to != s.Column {
		f.move.To = to
	}
	return f.move
}

// request is where one pull request would have the ticket, and which of the
// decision's steps were its.
type request struct {
	pr          int
	to          int
	event       string
	first, last int
}

// furthest picks the request that has the ticket furthest along the board.
// Ties go to the lowest pull request number, which is only a tie-break: they
// agree about the column, which is the only thing that is acted on.
func furthest(requests []request) (int, bool) {
	winner := -1
	for i, r := range requests {
		if winner < 0 || r.to > requests[winner].to {
			winner = i
		}
	}
	return winner, winner >= 0
}

// overrule rewrites the moves of the pull requests that did not win, so that
// the decision does not claim to be making a move it is not. They are not
// wrong about their own pull request — they are behind the work as a whole.
func (f *fold) overrule(requests []request, winner int) {
	best := requests[winner].to
	for i, r := range requests {
		if i == winner || r.to == best {
			continue
		}
		for at := r.first; at < r.last; at++ {
			step := &f.move.Steps[at]
			if !step.Moved {
				continue
			}
			step.Moved = false
			step.Reason = ReasonOverruled
			step.Why = fmt.Sprintf("#%d has the ticket in %s, which is further on", requests[winner].pr, f.cfg.Workflow.Columns[best].Name)
		}
	}
}

// fold is the running decision about one pull request: where it would have the
// ticket, and what has been decided so far.
type fold struct {
	cfg   *config.Config
	snap  TransitionSnapshot
	at    int
	asked bool
	event string
	move  TicketMove
}

// pullRequest adds the events that describe one pull request's present state.
//
// It is the state that is reported, not the history: a pull request that was
// closed without merging never asks for the review column on its way past,
// because it is not in review and never will be again. The one exception is
// spec 04 §4's own: a pull request open and ready is `pr_opened_draft`
// followed by `pr_ready_for_review`, both of which apply in order, so that a
// workflow which configures only the first still starts the work.
func (f *fold) pullRequest(linked LinkedPR) {
	pr := linked.PR
	switch {
	case pr.Merged():
		f.step(config.EventPRMerged, pr.Number)
		if linked.Deployed {
			f.step(config.EventDeployed, pr.Number)
		}
	case pr.State == core.PRStateClosed:
		f.step(config.EventPRClosedUnmerged, pr.Number)
	case pr.Draft:
		// A draft pull request and a ticket already past the column drafts
		// belong in can only have come about one way: somebody put the pull
		// request back to draft. The position is the evidence, so flow does
		// not report a conversion that did not happen.
		converted := f.ahead(config.EventPROpenedDraft)
		f.step(config.EventPROpenedDraft, pr.Number)
		if converted {
			f.step(config.EventPRConvertedToDraft, pr.Number)
		}
	default:
		f.step(config.EventPROpenedDraft, pr.Number)
		f.step(config.EventPRReadyForReview, pr.Number)
		switch {
		case pr.ChangesRequested():
			f.step(config.EventChangesRequested, pr.Number)
		case approved(pr, linked.RequiredApprovals):
			f.step(config.EventApproved, pr.Number)
		}
	}
}

// step considers one event: what the workflow does about it, whether the rules
// of spec 04 §5 allow it, and if they do, moves the fold on.
func (f *fold) step(event string, pr int) {
	step := TransitionStep{Event: event, PR: pr}
	defer func() { f.move.Steps = append(f.move.Steps, step) }()

	transition, configured := f.cfg.Workflow.Transitions[event]
	if !configured || transition.To == "" {
		step.Reason = ReasonUnconfigured
		step.Why = fmt.Sprintf("the workflow configures no transition for %s", event)
		return
	}
	step.To, step.Owner = transition.To, transition.Owner

	target, known := f.cfg.Workflow.Column(transition.To)
	to := f.cfg.Workflow.ColumnIndex(transition.To)
	if !known || to < 0 {
		step.Reason = ReasonUnknownColumn
		step.Why = fmt.Sprintf("%s sends tickets to %q, which is not a column in workflow.columns", event, transition.To)
		return
	}

	switch {
	case f.snap.Ticket.Archived:
		// Archiving is a decision about the whole item, not about one
		// event, so it answers every event the same way. The tracker
		// will not write to an archived item at all — on GitHub
		// Projects it accepts the mutation and performs nothing — so a
		// move flow asked for here would be a move it then reported
		// and never made.
		step.Reason = ReasonArchived
		step.Why = fmt.Sprintf("%s is archived, and an archived item takes no change until somebody restores it to the board", f.snap.Ticket.Key)

	case to == f.at:
		// Being already there is still an opinion about where the ticket
		// belongs, and a pull request in review holds it in review against
		// another that would take it back (rule 4).
		f.asked = true
		step.Reason = ReasonSatisfied
		step.Why = fmt.Sprintf("the ticket is already in %s", target.Name)

	case f.cfg.Workflow.Columns[f.at].Category == config.CategoryDone:
		step.Reason = ReasonDone
		step.Why = fmt.Sprintf("the ticket is in %s, and flow never moves work back out of done", f.cfg.Workflow.Columns[f.at].Name)

	case f.snap.Ticket.Closed && event != config.EventIssueClosed:
		step.Reason = ReasonClosed
		step.Why = fmt.Sprintf("%s is closed, and the only move left in finished work is the one its closing asks for", f.snap.Ticket.Key)

	case transition.Owner != config.OwnerFlow:
		step.Reason = ReasonNative
		step.Why = fmt.Sprintf("the tracker's own automation moves tickets to %s on %s", target.Name, event)

	case to < f.at && !slices.Contains(backMoves, event):
		step.Reason = ReasonBackwards
		step.Why = fmt.Sprintf("%s would take the ticket back to %s, and only a review asking for changes or a pull request put back to draft may move work backwards", event, target.Name)

	case target.Category == config.CategoryDone && f.unfinished() != "":
		step.Reason, step.Why = f.unfinished(), f.unfinishedWhy()

	default:
		step.Moved = true
		f.at = to
		f.asked = true
		f.event = event
	}
}

// ahead reports whether the ticket has already gone past the column an event
// would take it to.
func (f *fold) ahead(event string) bool {
	transition, configured := f.cfg.Workflow.Transitions[event]
	if !configured {
		return false
	}
	to := f.cfg.Workflow.ColumnIndex(transition.To)
	return to >= 0 && f.at > to
}

// unfinished reports why the ticket's work is not finished, or the empty
// string when it is. Done is the one column a single pull request cannot
// deliver on its own: a ticket is done when all of its work is, and under
// done_on: deployed when all of it is running (spec 04 §5 rule 4, §6).
func (f *fold) unfinished() string {
	for _, linked := range f.snap.PRs {
		if !linked.PR.Merged() && linked.PR.State != core.PRStateClosed {
			return ReasonOpenWork
		}
	}
	if f.cfg.Workflow.DoneOn != config.DoneOnDeployed {
		return ""
	}
	for _, linked := range f.snap.PRs {
		if linked.PR.Merged() && !linked.Deployed {
			return ReasonUndeployed
		}
	}
	return ""
}

// unfinishedWhy names the pull requests holding the ticket back, so that the
// person reading it knows what to go and look at.
func (f *fold) unfinishedWhy() string {
	switch f.unfinished() {
	case ReasonOpenWork:
		return fmt.Sprintf("%s still open", prList(f.snap.PRs, func(l LinkedPR) bool {
			return !l.PR.Merged() && l.PR.State != core.PRStateClosed
		}))
	case ReasonUndeployed:
		return fmt.Sprintf("%s merged but not yet in %s", prList(f.snap.PRs, func(l LinkedPR) bool {
			return l.PR.Merged() && !l.Deployed
		}), f.cfg.Deploys.Environment)
	default:
		return ""
	}
}

// prList names the pull requests a predicate picks out, in number order.
func prList(prs []LinkedPR, pick func(LinkedPR) bool) string {
	var numbers []string
	for _, linked := range sortedPRs(prs) {
		if pick(linked) {
			numbers = append(numbers, fmt.Sprintf("#%d", linked.PR.Number))
		}
	}
	return joinWords(numbers)
}

// sortedPRs puts the pull requests in number order, which is what makes the
// fold give the same answer however the command gathered them.
func sortedPRs(prs []LinkedPR) []LinkedPR {
	sorted := slices.Clone(prs)
	slices.SortStableFunc(sorted, func(a, b LinkedPR) int {
		return cmp.Compare(a.PR.Number, b.PR.Number)
	})
	return sorted
}

// approved reports whether a pull request has the approvals its branch
// demands. A repository that requires none has not said that nobody need look
// at it — it has said it does not enforce review — so the event still waits
// for somebody to approve, which is what `approved` means to everybody who
// reads it (spec 04 §4).
func approved(pr core.PullRequest, required int) bool {
	return len(pr.Approvals()) >= max(required, 1)
}

// unmappedWhy says where the board has the ticket, since that is the thing the
// person fixing it has to recognise.
func unmappedWhy(ticket core.Ticket) string {
	where := ticket.Status.Name
	if where == "" {
		where = "no status at all"
	} else {
		where = fmt.Sprintf("%q", where)
	}
	return fmt.Sprintf("the board has %s in %s, which is not a column in workflow.columns, so flow will not guess where it belongs", ticket.Key, where)
}

// Text renders the decision as the single line a dry run prints.
func (m TicketMove) Text() string {
	switch {
	case m.Warning != "":
		return fmt.Sprintf("%s  skipped — %s", m.Key, m.Warning)
	case m.Moves():
		return fmt.Sprintf("%s  %s → %s  (%s)", m.Key, m.From, m.To, m.Event)
	default:
		return fmt.Sprintf("%s  stays in %s — %s", m.Key, m.From, m.held())
	}
}

// heldOrder ranks the reasons a ticket stayed put by how much the reader needs
// to hear them. A ticket that is already where it belongs should say so rather
// than report the event that would have taken it backwards, and a column the
// workflow does not have is the first thing anybody should be told. An
// archived ticket leads: nothing else said about it can be acted on until it
// is back on the board.
var heldOrder = []string{
	ReasonArchived,
	ReasonUnknownColumn,
	ReasonOpenWork,
	ReasonUndeployed,
	ReasonNative,
	ReasonClosed,
	ReasonDone,
	ReasonOverruled,
	ReasonSatisfied,
	ReasonBackwards,
	ReasonUnconfigured,
}

// held is the reason the ticket is staying put. A ticket nothing asked to move
// says so plainly.
func (m TicketMove) held() string {
	for _, reason := range heldOrder {
		for _, step := range m.Steps {
			if step.Reason == reason {
				return step.Why
			}
		}
	}
	return "nothing has happened that would move it"
}
