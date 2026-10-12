package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit, native-transition half (spec 07 §6, 04 §5).
//
// `owner: native` is flow declining to do something, on the grounds that the
// tracker's own automation already does it. Apply cannot prove that: GitHub's
// built-in workflows are not listable through the API, so the capability
// registry marks them `remediation: attest` and somebody signs a note saying
// they are switched on. An attestation is a claim. This is what makes it
// checkable — the one audit in §6 that is about the tracker rather than about
// the team.
//
// It reads the board as it stands now against events it can date, rather than
// asking for field history: a Projects item's history is not in the API, Jira
// keeps a changelog and Linear an issue history, and a check whose question
// changed shape per tracker would be three checks wearing one id. What every
// tracker does say is where a ticket is now, and that is enough: fifteen
// minutes after a merge, a ticket that has not reached the column the merge
// was supposed to take it to is a transition that did not happen.

// CheckLiveTransitionsPrefix begins the id of the check that verifies one
// natively-owned transition: live.transitions.native.pr_merged. One per
// event, mirroring the setup side's tracker.native.<event>, because an
// automation can be off for merges and on for closes and a single verdict
// could not say which (spec 04 §5).
const CheckLiveTransitionsPrefix = "live.transitions.native."

// LiveTransitionCheck is the check id that verifies one native transition.
func LiveTransitionCheck(event string) string { return CheckLiveTransitionsPrefix + event }

// TransitionGrace is how long the tracker's automation is given to react to an
// event before doctor counts it as missed (spec 07 §6). It is deliberately
// generous: the question is whether the automation runs at all, not how fast
// its queue is on a bad morning.
const TransitionGrace = 15 * time.Minute

// transitionChecks builds one check per natively-owned transition, in event
// order. The workflow decides how many there are, which is why the live half
// of the registry needs the configuration the setup half already had.
func (a *audit) transitionChecks(cfg *config.Config) []Check {
	var checks []Check
	for _, event := range nativeEvents(cfg) {
		checks = append(checks, a.transitionNative(event))
	}
	return checks
}

// occurrence is one event flow can date, and the ticket it should have moved.
type occurrence struct {
	ticket core.Ticket
	at     time.Time
	what   string
}

// transitionNative verifies that the tracker's own automation performed one
// event's move.
func (a *audit) transitionNative(event string) Check {
	return Check{
		ID:        LiveTransitionCheck(event),
		Title:     fmt.Sprintf("The tracker really does move tickets itself on %s", event),
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			transition, configured := env.Config.Workflow.Transitions[event]
			if !configured || transition.Owner != config.OwnerNative {
				return Skip("%s is no longer the tracker's to perform", event)
			}
			target, known := env.Config.Workflow.Column(transition.To)
			if !known {
				return Skip("%s sends tickets to %q, which is not a column", event, transition.To)
			}
			if _, datable := transitionSources[event]; !datable {
				return Skip("flow cannot tell when %s last happened to a ticket, so it cannot say whether the move was late", event)
			}
			happened, err := a.occurrences(ctx, env, event)
			if err != nil {
				return Failed(err)
			}
			to := env.Config.Workflow.ColumnIndex(transition.To)
			forward := !slices.Contains(backMoves, event)
			deadline := env.Now().Add(-TransitionGrace)
			work := a.linkedWork(ctx, env)

			var t tally
			for _, seen := range happened {
				if seen.at.After(deadline) {
					continue
				}
				at := env.Config.Workflow.ColumnIndex(ColumnOf(env.Config, env.Lock, seen.ticket))
				if at < 0 {
					// A ticket in a status the workflow does not name is not
					// evidence about anything: flow does not know where it is
					// (spec 04 §5 rule 3).
					continue
				}
				if target.Category == config.CategoryDone && unfinishedWork(work[ticketKey(seen.ticket)]) {
					continue
				}
				if (forward && at >= to) || (!forward && at <= to) {
					t.held()
					continue
				}
				t.broke(seen.ticket.URL, "%s %s — %s %s, and it is still in %s",
					seen.ticket.Key, seen.ticket.Title, seen.what, day(seen.at),
					env.Config.Workflow.Columns[at].Name)
			}

			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0 && len(happened) == 0:
				return result.Said("nothing in the window needed the tracker to move a ticket on %s", event)
			case t.counts.Total == 0:
				return result.Said("none of the %d %s on %s in the window was for a ticket flow can place on the board",
					len(happened), plural(len(happened), "event"), event)
			case result.State == StatePass:
				return result.Said("all %d %s reached %s on %s",
					t.counts.Total, plural(t.counts.Total, "ticket"), target.Name, event)
			}
			return result.Said("%d of %d tickets did not reach %s within %d minutes of %s",
				t.missed(), t.counts.Total, target.Name, int(TransitionGrace/time.Minute), event).
				Fix(Remediation{
					Kind: RemedyManual,
					Summary: fmt.Sprintf("workflow.transitions.%s says the tracker does this one, and the board says it did not",
						event),
					Steps: []string{
						fmt.Sprintf("check %s in this report: the attestation behind it is a person's word that the automation is on, and this is the evidence against it",
							core.NativeItem(event)),
						"turn the tracker's built-in workflow back on, and re-attest once the board catches up",
						fmt.Sprintf("or give the transition to flow — workflow.transitions.%s.owner: flow — which puts it in `flow check pr` and lets `flow doctor --fix` catch up what it misses",
							event),
					},
				})
		},
	}
}

// transitionSources names the events flow can date from what the audit
// already reads. An event missing from here is one the check declines to
// judge rather than one it passes: a tracker's automation being unverifiable
// is a different answer from its being verified.
var transitionSources = map[string]string{
	config.EventPRMerged:         "its work merged on",
	config.EventIssueClosed:      "it was closed on",
	config.EventPROpenedDraft:    "a pull request was opened for it on",
	config.EventPRReadyForReview: "its pull request was last touched on",
	config.EventChangesRequested: "a review asked for changes on",
}

// occurrences is every time the event happened inside the window that flow can
// put a date on, with the ticket it was about. One event may be about several
// tickets and one ticket may appear twice, because two pull requests merging
// are two occasions on which the automation had to work.
func (a *audit) occurrences(ctx context.Context, env *Env, event string) ([]occurrence, error) {
	if event == config.EventIssueClosed {
		return a.closures(ctx, env)
	}
	prs, err := a.prsFor(ctx, env, event)
	if err != nil {
		return nil, err
	}
	board, err := a.board(ctx, env)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]core.Ticket, len(board))
	for _, ticket := range board {
		byKey[ticketKey(ticket)] = ticket
	}
	var out []occurrence
	for _, pr := range prs {
		at := prEventAt(event, pr)
		if at.IsZero() || at.Before(env.Since()) {
			continue
		}
		for _, key := range linkedKeys(env, pr) {
			// A ticket nobody put on the board comes back as the zero ticket,
			// which has no column — and a ticket flow cannot place is one
			// spec 04 §5 rule 3 already declines to judge, below. One rule
			// for "flow does not know where this is" rather than two.
			out = append(out, occurrence{ticket: byKey[normalKey(key)], at: at, what: transitionSources[event]})
		}
	}
	return out, nil
}

// prsFor is the pull requests an event is read from: the window's merges for
// the one event that is about merging, and what is open for the events that
// are about a pull request's present state.
func (a *audit) prsFor(ctx context.Context, env *Env, event string) ([]core.PullRequest, error) {
	if event == config.EventPRMerged {
		return a.merged(ctx, env)
	}
	open, err := a.inFlight(ctx, env)
	if err != nil {
		return nil, err
	}
	var out []core.PullRequest
	for _, pr := range open {
		switch event {
		case config.EventPROpenedDraft:
			// Every pull request was opened as one or passed through the
			// column drafts belong in, so this event is about all of them.
			out = append(out, pr)
		case config.EventPRReadyForReview:
			if !pr.Draft {
				out = append(out, pr)
			}
		case config.EventChangesRequested:
			if !pr.Draft && pr.ChangesRequested() {
				out = append(out, pr)
			}
		}
	}
	return out, nil
}

// closures is every ticket the board says was closed inside the window.
func (a *audit) closures(ctx context.Context, env *Env) ([]occurrence, error) {
	board, err := a.board(ctx, env)
	if err != nil {
		return nil, err
	}
	var out []occurrence
	for _, ticket := range board {
		if !ticket.Closed {
			continue
		}
		at := finished(ticket)
		if at.IsZero() || at.Before(env.Since()) {
			continue
		}
		out = append(out, occurrence{ticket: ticket, at: at, what: transitionSources[config.EventIssueClosed]})
	}
	return out, nil
}

// prEventAt is when an event happened to a pull request, as nearly as the
// forge will say.
//
// Merging and opening are dated exactly. Being marked ready for review is not
// recorded anywhere, so the last time anything happened to the pull request
// stands in for it — which is always at or after the real moment, so the
// grace period is only ever generous. A check that guessed early would blame
// the tracker for an event that had not finished happening.
func prEventAt(event string, pr core.PullRequest) time.Time {
	switch event {
	case config.EventPRMerged:
		return pr.MergedAt
	case config.EventPROpenedDraft:
		return pr.CreatedAt
	case config.EventChangesRequested:
		return lastChangesRequested(pr)
	case config.EventApproved:
		return lastApproved(pr)
	default:
		return pr.UpdatedAt
	}
}

// lastApproved is when somebody last approved. It is only ever asked for by
// the catch-up: the native audit declines events it cannot date and approval
// is not one of the five it reads.
func lastApproved(pr core.PullRequest) time.Time {
	var at time.Time
	for _, review := range pr.Reviews {
		if review.State == core.ReviewApproved && review.SubmittedAt.After(at) {
			at = review.SubmittedAt
		}
	}
	return at
}

// lastChangesRequested is when somebody last asked for changes.
func lastChangesRequested(pr core.PullRequest) time.Time {
	var at time.Time
	for _, review := range pr.Reviews {
		if review.State == core.ReviewChangesRequested && review.SubmittedAt.After(at) {
			at = review.SubmittedAt
		}
	}
	return at
}

// linkedWork indexes every pull request the audit knows about by the ticket it
// is for, so that the done rule can be asked about a ticket's work as a whole.
// A provider that will not answer is not an error here: the index is only used
// to let a ticket off, so an empty one judges more, never less.
func (a *audit) linkedWork(ctx context.Context, env *Env) map[string][]core.PullRequest {
	work, _ := a.work(ctx, env)
	return work
}

// unfinishedWork reports that a ticket still has work in flight, which is what
// spec 04 §5 rule 4 says stops it being done. A tracker that moved such a
// ticket to its done column would be making the board lie, so the audit does
// not hold it against the automation for leaving it where it is.
func unfinishedWork(prs []core.PullRequest) bool {
	for _, pr := range prs {
		if !pr.Merged() && pr.State != core.PRStateClosed {
			return true
		}
	}
	return false
}
