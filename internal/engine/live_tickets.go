package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit, board half (spec 07 §6). The pull-request checks ask
// the forge whether the work that landed was done the way the team agreed;
// these ask the board the same question from the other side.
//
// A board is read once per run and bucketed into flow's own column vocabulary,
// never queried column by column. The tracker's column ids are its own — a
// Projects status option id, a Jira column — and the mapping from those onto
// `in_review` and `ready` is [ColumnOf]'s, through the lock, so that renaming
// a column in the tracker does not change what doctor is asking about.
//
// Only the Done check has a window. The others are about the board as it
// stands right now: a ticket that has sat in review without a pull request for
// a month is not less wrong for having arrived there before the fortnight
// began, and "in review since before the window" is not a thing a tracker will
// tell you anyway.

// The live-health checks about tickets (spec 07 §6).
const (
	// CheckLiveTicketsDoneMerged is whether the work that reached Done went
	// through the trunk.
	CheckLiveTicketsDoneMerged = "live.tickets.done_merged"
	// CheckLiveTicketsDoneDeployed is whether work called done is running,
	// for a team that says done means deployed.
	CheckLiveTicketsDoneDeployed = "live.tickets.done_deployed"
	// CheckLiveTicketsReviewHasPR is whether everything waiting for review
	// has something to review.
	CheckLiveTicketsReviewHasPR = "live.tickets.review_has_pr"
	// CheckLiveTicketsReviewStuck is whether anything that has been
	// approved is being held up by something only a push can fix.
	CheckLiveTicketsReviewStuck = "live.tickets.review_stuck"
	// CheckLiveTicketsReviewStale is whether anything has been waiting for
	// review longer than the team's patience.
	CheckLiveTicketsReviewStale = "live.tickets.review_stale"
	// CheckLiveTicketsProgressStale is whether anything in progress has
	// stopped moving.
	CheckLiveTicketsProgressStale = "live.tickets.progress_stale"
	// CheckLiveTicketsReadyDoR is whether what is ready to start really is.
	CheckLiveTicketsReadyDoR = "live.tickets.ready_dor"
	// CheckLiveTicketsBlockedExplained is whether work the team has stopped
	// says why it stopped.
	CheckLiveTicketsBlockedExplained = "live.tickets.blocked_explained"
)

// errNoTracker is what a board check reports when there is no tracker to ask.
// Like [errNoForge] it cannot happen through doctor, where `auth.tracker`
// fails first and blocks these checks.
var errNoTracker = errors.New("there is no tracker adapter to ask")

// errNoDeploys is the same for the deployment records.
var errNoDeploys = errors.New("there is no deployment source to ask")

// TicketChecks builds the board half of the audit, in report order. It shares
// the [audit] the pull-request checks were built over, so that one run reads
// the board once and the window once however many of them are selected.
func (a *audit) ticketChecks() []Check {
	return []Check{
		a.ticketsDoneMerged(),
		a.ticketsDoneDeployed(),
		a.ticketsReviewHasPR(),
		a.ticketsReviewStuck(),
		a.ticketsReviewStale(),
		a.ticketsProgressStale(),
		a.ticketsReadyDoR(),
		a.ticketsBlockedExplained(),
	}
}

// board is every item the tracker has, read once.
//
// The whole board rather than a query per column: four checks asking for four
// columns would be four round trips for one answer, and the column a ticket is
// in has to be translated out of the tracker's vocabulary here anyway.
func (a *audit) board(ctx context.Context, env *Env) ([]core.Ticket, error) {
	a.boardOnce.Do(func() {
		if env.Adapters.Tracker == nil {
			a.boardErr = errNoTracker
			return
		}
		tickets, err := env.Adapters.Tracker.Tickets(ctx, core.TicketQuery{})
		if err != nil {
			a.boardErr = core.Explain(err, "read the board")
			return
		}
		a.tickets = tickets
	})
	return a.tickets, a.boardErr
}

// inColumn is what sits in one of flow's configured columns, in key order so
// that two runs of the same board produce the same report.
func (a *audit) inColumn(ctx context.Context, env *Env, id string) ([]core.Ticket, error) {
	board, err := a.board(ctx, env)
	if err != nil {
		return nil, err
	}
	var out []core.Ticket
	for _, ticket := range board {
		if ColumnOf(env.Config, env.Lock, ticket) == id {
			out = append(out, ticket)
		}
	}
	slices.SortStableFunc(out, func(x, y core.Ticket) int { return cmp.Compare(x.Key, y.Key) })
	return out, nil
}

// inFlight is the pull requests still open, read once.
//
// No window and no base branch: a pull request open for three months is the
// most interesting one a board check can find, and bounding the query would
// hide exactly the ticket that has been waiting longest.
func (a *audit) inFlight(ctx context.Context, env *Env) ([]core.PullRequest, error) {
	a.openOnce.Do(func() {
		if env.Adapters.Forge == nil {
			a.openErr = errNoForge
			return
		}
		prs, err := env.Adapters.Forge.PullRequests(ctx, core.PRQuery{State: core.PRStateOpen})
		if err != nil {
			a.openErr = core.Explain(err, "read the open pull requests")
			return
		}
		a.open, a.openErr = detailed(ctx, env.Adapters.Forge, prs)
	})
	return a.open, a.openErr
}

// ticketsDoneMerged asks whether the work that reached Done went through the
// trunk.
//
// A ticket closed as not planned holds: spec 07 §6 exempts it, and abandoned
// work having no merge behind it is the point of abandoning it rather than a
// hole in the process.
func (a *audit) ticketsDoneMerged() Check {
	return Check{
		ID:        CheckLiveTicketsDoneMerged,
		Title:     "Every ticket that reached Done had a merge behind it",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := doneColumn(env.Config)
			if !named {
				return Skip("nothing in the workflow says which column work ends in")
			}
			done, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, prs)
			since := env.Since()
			var t tally
			for _, ticket := range done {
				if finished(ticket).Before(since) {
					continue
				}
				if ticket.CloseReason == core.ClosedNotPlanned {
					t.held()
					continue
				}
				if len(work[ticketKey(ticket)]) > 0 {
					t.held()
					continue
				}
				t.broke(ticket.URL, "%s %s — no pull request merged in the window was for it", ticket.Key, ticket.Title)
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing reached %s in the window", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s that reached %s had work behind them",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name)
			}
			return result.Said("%d of %d tickets reached %s with no merge behind them",
				t.missed(), t.counts.Total, column.Name).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "the merge should be what moves a ticket to " + column.Name + ", not a hand on the board",
					Steps: []string{
						"check tracker.native.pr_merged in this report: the tracker's own automation is what moves a ticket when its work merges",
						linkFix(env.Config),
						"check forge.protection in this report: work pushed straight to the trunk finishes a ticket with no pull request to point at",
						"work that was dropped rather than finished should be closed as not planned, which this check exempts",
					},
				})
		},
	}
}

// ticketsDoneDeployed asks whether work a team calls done is actually running.
//
// It exists only for `done_on: deployed`. For everybody else the question is
// already answered by the merge, and asking it anyway would report a repository
// with no deployments at all as unhealthy for not having them.
func (a *audit) ticketsDoneDeployed() Check {
	return Check{
		ID:        CheckLiveTicketsDoneDeployed,
		Title:     "Work called done is running",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			if watched, why := WatchesDeployments(env.Config); !watched {
				return Skip("%s", why)
			}
			if env.Adapters.Deploys == nil {
				return Failed(errNoDeploys)
			}
			column, named := doneColumn(env.Config)
			if !named {
				return Skip("nothing in the workflow says which column work ends in")
			}
			where := env.Config.Deploys.Environment
			running, err := a.deployedSHA(ctx, env, where)
			if err != nil {
				return Failed(err)
			}
			if running == "" {
				return Result{State: StateFail}.
					Said("nothing is recorded as deployed to %s, so no ticket can be shown to be running", where).
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "flow reads deployment records to decide what is running; there are none",
						Steps: []string{
							"check deploys.source in this report: it says which records flow is reading and where from",
							"a workflow whose done_on is deployed but which records no deployments can never finish a ticket",
						},
					})
			}
			done, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, prs)
			since := env.Since()
			var t tally
			for _, ticket := range done {
				if finished(ticket).Before(since) || ticket.CloseReason == core.ClosedNotPlanned {
					continue
				}
				theirs := work[ticketKey(ticket)]
				if len(theirs) == 0 {
					// Whether a ticket has a merge at all is done_merged's
					// question, and counting it here too would report one
					// mistake as two.
					continue
				}
				behind, err := notRunning(ctx, env.Comparer(), theirs, running)
				if err != nil {
					return Failed(err)
				}
				if behind == 0 {
					t.held()
					continue
				}
				t.broke(ticket.URL, "%s %s — %d of its %d %s is not in %s yet",
					ticket.Key, ticket.Title, behind, len(theirs), plural(len(theirs), "merge"), shortSHA(running))
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing reached %s in the window", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s in %s are running in %s",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name, where)
			}
			return result.Said("%d of %d tickets in %s are not running in %s",
				t.missed(), t.counts.Total, column.Name, where).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "a ticket reached " + column.Name + " before its work was deployed",
					Steps: []string{
						"check tracker.native.deployed in this report: nothing but a deployment should move a ticket to " + column.Name,
						"check live.deploys.lag: work that merged and never shipped is the same problem seen from the other end",
					},
				})
		},
	}
}

// ticketsReviewHasPR asks whether everything waiting for review has something
// to review. A draft does not count: offering it for review is the act the
// column is named after.
func (a *audit) ticketsReviewHasPR() Check {
	return Check{
		ID:        CheckLiveTicketsReviewHasPR,
		Title:     "Everything waiting for review has something to review",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := columnFor(env.Config, config.EventPRReadyForReview)
			if !named {
				return Skip("nothing in the workflow says which column work waits for review in")
			}
			waiting, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			open, err := a.inFlight(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, open)
			var t tally
			for _, ticket := range waiting {
				theirs := work[ticketKey(ticket)]
				switch {
				case len(theirs) == 0:
					t.broke(ticket.URL, "%s %s — nothing open is for it", ticket.Key, ticket.Title)
				case !slices.ContainsFunc(theirs, func(pr core.PullRequest) bool { return !pr.Draft }):
					t.broke(ticket.URL, "%s %s — everything open for it is still a draft", ticket.Key, ticket.Title)
				default:
					t.held()
				}
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing is in %s", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s in %s have a pull request open for review",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name)
			}
			return result.Said("%d of %d tickets in %s have nothing to review",
				t.missed(), t.counts.Total, column.Name).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "a ticket in " + column.Name + " is asking for somebody's time; it should say what to look at",
					Steps: []string{
						"check tracker.native.pr_ready_for_review in this report: marking a pull request ready is what should move a ticket here",
						"move anything with no pull request back to the column it is really in",
					},
				})
		},
	}
}

// ticketsReviewStuck asks whether anything that has been approved is being
// held up by something only a push can fix.
//
// This is the failure the board hides best. A pull request that conflicts, or
// that has fallen behind a trunk the protection requires it to be level with,
// is finished as far as everybody involved is concerned: it is approved, its
// checks are green — computed on a head that has not moved and green there for
// ever — and no forge sends anybody mail about it. The card sits in the review
// column saying the work is moving.
//
// Only approved work is judged, for two reasons. Falling behind the trunk is
// the ordinary condition of every pull request in a queue the moment one of
// them merges, and a check that failed for each of those would be noise about
// nothing. And a forge that reports one blocker at a time reports the
// approvals it is missing before it reports anything else, so until the
// approval is there, "what is in the way" is not a question it has answered.
func (a *audit) ticketsReviewStuck() Check {
	return Check{
		ID:        CheckLiveTicketsReviewStuck,
		Title:     "Nothing approved is stuck in review",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := columnFor(env.Config, config.EventPRReadyForReview)
			if !named {
				return Skip("nothing in the workflow says which column work waits for review in")
			}
			waiting, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			open, err := a.inFlight(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, open)
			var t tally
			for _, ticket := range waiting {
				approved := approvedWork(work[ticketKey(ticket)])
				if len(approved) == 0 {
					// Nothing has been approved, so nothing is
					// waiting on a push rather than on a person.
					continue
				}
				// One ticket counts once however many pull
				// requests are behind it, because the tally is
				// the board: a message reading "2 of 1 tickets"
				// is what counting both at once gets.
				var blocked []string
				var link string
				for _, pr := range approved {
					why, stuck := pr.Stuck()
					if !stuck {
						continue
					}
					blocked = append(blocked, fmt.Sprintf("#%d %s", pr.Number, why))
					if link == "" {
						link = pr.URL
					}
				}
				if len(blocked) == 0 {
					t.held()
					continue
				}
				t.broke(link, "%s %s — approved and cannot merge: %s",
					ticket.Key, ticket.Title, strings.Join(blocked, "; "))
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing in %s has been approved yet", column.Name)
			case result.State == StatePass:
				return result.Said("all %d approved %s in %s could be merged now",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name)
			}
			return result.Said("%d of %d approved tickets in %s cannot be merged",
				t.missed(), t.counts.Total, column.Name).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "work that is finished and cannot land is the queue stopping without anything going red",
					Steps: []string{
						"rebase or merge the trunk into each branch named above, and push",
						"nothing here is a flow finding to clear: the checks on these are green, computed on a head that has not changed",
						"a trunk with strict required status checks puts every open pull request behind it each time one merges, so merge the queue in order rather than letting it build",
					},
				})
		},
	}
}

// ticketsReviewStale asks whether anything has been waiting for review longer
// than the team's patience.
//
// It warns and never fails, for the same reason [audit.ticketsProgressStale]
// does: a review that has not happened is a fact about what people had time
// for, and a repository marked unhealthy for it would teach the team that red
// means nothing. What it is for is the question nobody asks out loud, which is
// whose turn it is.
func (a *audit) ticketsReviewStale() Check {
	return Check{
		ID:        CheckLiveTicketsReviewStale,
		Title:     "Nothing has been waiting for review too long",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := columnFor(env.Config, config.EventPRReadyForReview)
			if !named {
				return Skip("nothing in the workflow says which column work waits for review in")
			}
			waiting, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			open, err := a.inFlight(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, open)
			patience := env.Patience()
			cutoff := workingDaysBefore(env.Now(), patience)
			var t tally
			for _, ticket := range waiting {
				last := lastTouched(ticket, work[ticketKey(ticket)])
				if last.IsZero() {
					continue
				}
				if !last.Before(cutoff) {
					t.held()
					continue
				}
				t.broke(ticket.URL, "%s %s — waiting for review since %s",
					ticket.Key, ticket.Title, day(last))
			}
			result := t.gradeWarning(env)
			switch {
			case t.counts.Total == 0 && len(waiting) == 0:
				return result.Said("nothing is in %s", column.Name)
			case t.counts.Total == 0:
				return result.Said("nothing in %s has a date to judge it by", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s in %s have moved within %d working days",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name, patience)
			}
			return result.Said("%d of %d tickets in %s have been waiting more than %d working days",
				t.missed(), t.counts.Total, column.Name, patience).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "a review nobody has done is the slowest part of most workflows, and the only one nothing measures",
					Steps: []string{
						"review these, or say who will",
						"check the WIP limit on " + column.Name + ": a review queue longer than the team reads is work finished and not delivered",
						"health.stale_after sets how long is too long; it is " + strconv.Itoa(patience) + " working days here",
					},
				})
		},
	}
}

// approvedWork is the pull requests somebody has approved and nobody has since
// asked for changes on. A draft is not among them whatever its reviews say:
// offering it for review is the act that puts it in the queue at all.
func approvedWork(prs []core.PullRequest) []core.PullRequest {
	var out []core.PullRequest
	for _, pr := range prs {
		if pr.Draft || pr.ChangesRequested() || len(pr.Approvals()) == 0 {
			continue
		}
		out = append(out, pr)
	}
	return out
}

// lastTouched is the most recent thing to happen to a ticket or to any of the
// pull requests for it.
//
// Both halves are read because either alone is wrong: a tracker's own date
// does not move when somebody pushes, and a pull request's does not move when
// somebody writes on the card. A zero means nothing could be dated, which is
// not the same as nothing having happened.
func lastTouched(ticket core.Ticket, prs []core.PullRequest) time.Time {
	last := ticket.UpdatedAt
	for _, pr := range prs {
		if pr.UpdatedAt.After(last) {
			last = pr.UpdatedAt
		}
	}
	return last
}

// ticketsProgressStale asks whether anything in progress has stopped moving.
//
// It only ever warns. Work goes quiet for reasons flow cannot see — leave,
// an incident, a dependency on somebody else — and a check that failed a
// repository for one of them would teach the team that a red doctor means
// nothing. What it is for is the stand-up question: is this still in flight?
func (a *audit) ticketsProgressStale() Check {
	return Check{
		ID:        CheckLiveTicketsProgressStale,
		Title:     "Nothing in progress has gone quiet",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := columnFor(env.Config, config.EventPROpenedDraft)
			if !named {
				return Skip("nothing in the workflow says which column work is in progress in")
			}
			started, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			open, err := a.inFlight(ctx, env)
			if err != nil {
				return Failed(err)
			}
			work := prsByTicket(env, open)
			patience := env.Patience()
			cutoff := workingDaysBefore(env.Now(), patience)
			var t tally
			for _, ticket := range started {
				last := lastTouched(ticket, work[ticketKey(ticket)])
				if last.IsZero() {
					// A tracker that will not say when an item last moved
					// cannot be asked whether it has stopped moving, and
					// guessing would make the answer depend on the adapter.
					continue
				}
				if !last.Before(cutoff) {
					t.held()
					continue
				}
				t.broke(ticket.URL, "%s %s — nothing has happened to it or its pull requests since %s",
					ticket.Key, ticket.Title, day(last))
			}
			result := t.gradeWarning(env)
			switch {
			case t.counts.Total == 0 && len(started) == 0:
				return result.Said("nothing is in %s", column.Name)
			case t.counts.Total == 0:
				return result.Said("nothing in %s has a date to judge it by", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s in %s have moved within %d working days",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name, patience)
			}
			return result.Said("%d of %d tickets in %s have not moved in %d working days",
				t.missed(), t.counts.Total, column.Name, patience).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "ask whoever has these whether they are really still in flight",
					Steps: []string{
						"work that is waiting on somebody else is not in progress; move it back or say what it is blocked on",
						"check the WIP limit on " + column.Name + ": a column holding more than the team can carry is how work goes quiet",
					},
				})
		},
	}
}

// ticketsReadyDoR asks whether what the board says is ready to start really is.
//
// The column is found by its gate rather than by its id, because the gate is
// what makes it the ready column: a team that renamed it has not stopped
// meaning it, and a team that gates no column on the Definition of Ready has
// not asked this question of their board at all.
func (a *audit) ticketsReadyDoR() Check {
	return Check{
		ID:        CheckLiveTicketsReadyDoR,
		Title:     "What is ready to start really is ready",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker},
		Run: func(ctx context.Context, env *Env) Result {
			column, named := readyColumn(env.Config)
			if !named {
				return Skip("no column is gated on the Definition of Ready")
			}
			if len(env.Config.DefinitionOfReady.Rules) == 0 {
				return Skip("the team has not written a Definition of Ready")
			}
			ready, err := a.inColumn(ctx, env, column.ID)
			if err != nil {
				return Failed(err)
			}
			var t tally
			for _, ticket := range ready {
				judged := Judge(ticket, env.Config)
				if judged.Ready() {
					t.held()
					continue
				}
				t.broke(ticket.URL, "%s %s — %s", ticket.Key, ticket.Title,
					strings.Join(judged.Reasons(), "; "))
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing is in %s", column.Name)
			case result.State == StatePass:
				return result.Said("all %d %s in %s meet the Definition of Ready",
					t.counts.Total, plural(t.counts.Total, "ticket"), column.Name)
			}
			return result.Said("%d of %d tickets in %s do not meet the Definition of Ready",
				t.missed(), t.counts.Total, column.Name).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "finish these before anybody starts them, or take them out of " + column.Name,
					Steps: []string{
						"the gate refuses a pull request for a ticket that is not ready; these will be refused when somebody picks them up",
						"check tracker.issue_forms in this report: a form that asks for acceptance criteria is how they come in ready",
					},
				})
		},
	}
}

// deployedSHA is the newest commit recorded as successfully deployed to the
// environment, or empty when nothing is. It is not cached on the audit: only
// one check asks, and caching a thing one caller reads is how a field comes to
// be stale.
func (a *audit) deployedSHA(ctx context.Context, env *Env, where string) (string, error) {
	records, err := env.Adapters.Deploys.Deployments(ctx, core.DeployQuery{Environment: where})
	if err != nil {
		return "", core.Explain(err, "read the deployments of %s", where)
	}
	var newest core.Deployment
	for _, record := range records {
		if !record.Succeeded() {
			continue
		}
		if newest.SHA == "" || record.CreatedAt.After(newest.CreatedAt) {
			newest = record
		}
	}
	return newest.SHA, nil
}

// notRunning is how many of a ticket's merges the deployed commit does not
// contain. Spec 04 §5 rule 4: a ticket is done when all of its work is, so one
// merge left behind is enough to say the ticket is not running.
func notRunning(ctx context.Context, comparer core.Comparer, prs []core.PullRequest, deployed string) (int, error) {
	behind := 0
	for _, pr := range prs {
		running, err := deployedWith(ctx, comparer, pr, deployed)
		if err != nil {
			return 0, err
		}
		if !running {
			behind++
		}
	}
	return behind, nil
}

// doneColumn is where work ends, which is wherever the event that finishes it
// takes a ticket. A team whose `done_on` is `deployed` finishes on the
// deployment; everybody else finishes on the merge. Closing the issue is the
// last fallback, for a workflow that drives the board from the tracker.
func doneColumn(cfg *config.Config) (config.Column, bool) {
	if cfg == nil {
		return config.Column{}, false
	}
	if cfg.Workflow.DoneOn == config.DoneOnDeployed {
		if column, found := columnFor(cfg, config.EventDeployed); found {
			return column, true
		}
	}
	if column, found := columnFor(cfg, config.EventPRMerged); found {
		return column, true
	}
	return columnFor(cfg, config.EventIssueClosed)
}

// readyColumn is the column gated on the Definition of Ready.
func readyColumn(cfg *config.Config) (config.Column, bool) {
	if cfg == nil {
		return config.Column{}, false
	}
	for _, column := range cfg.Workflow.Columns {
		if column.Gate == config.GateDefinitionOfReady {
			return column, true
		}
	}
	return config.Column{}, false
}

// finished is when a ticket's work ended, as nearly as a tracker will say.
// The close is the moment the audit is about; a tracker that records no close
// date, or a board whose Done column holds items still open, leaves the last
// edit as the only thing there is.
func finished(ticket core.Ticket) time.Time {
	if !ticket.ClosedAt.IsZero() {
		return ticket.ClosedAt
	}
	return ticket.UpdatedAt
}

// ticketsBlockedExplained asks whether work the team has stopped says why it
// stopped.
//
// It exists only for a team that asked for it. `workflow.blocked.label` on its
// own is a team that marks blocked work and says nothing more about it, which
// is their business; `require_comment` is the team writing down that a label
// is not enough. Flow holds them to what they wrote and to nothing else.
//
// It fails rather than warns. Unlike a ticket that has gone quiet, this is not
// a fact about what people had time for: the label was put on deliberately,
// and the explanation is the half of the gesture that was left out.
func (a *audit) ticketsBlockedExplained() Check {
	return Check{
		ID:        CheckLiveTicketsBlockedExplained,
		Title:     "Work the team has blocked says why",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker},
		Run: func(ctx context.Context, env *Env) Result {
			if env.Config == nil || !env.Config.Workflow.Blocked.RequireComment {
				return Skip("workflow.blocked.require_comment is off, so a label is all this team asks for")
			}
			if env.Adapters.Tracker == nil {
				return Failed(errNoTracker)
			}
			board, err := a.board(ctx, env)
			if err != nil {
				return Failed(err)
			}
			label := blockedLabel(env.Config)
			var stopped []core.Ticket
			for _, ticket := range board {
				if ticket.Closed {
					// Work that is finished is not waiting on anybody,
					// whatever label it still carries.
					continue
				}
				if ticket.Blocked || (label != "" && ticket.HasLabel(label)) {
					stopped = append(stopped, ticket)
				}
			}
			slices.SortStableFunc(stopped, func(x, y core.Ticket) int { return cmp.Compare(x.Key, y.Key) })
			var t tally
			for _, ticket := range stopped {
				comments, err := env.Adapters.Tracker.Comments(ctx, ticket.Key)
				if err != nil {
					// A ticket flow could not read is not a ticket with
					// nothing written on it, and reporting it as one would
					// be flow inventing a finding out of its own blindness.
					return Failed(core.Explain(err, "read the comments on %s", ticket.Key))
				}
				said, last := explanation(comments)
				switch {
				case said:
					t.held()
				case last.IsZero():
					t.broke(ticket.URL, "%s %s — nobody has written anything on it", ticket.Key, ticket.Title)
				default:
					t.broke(ticket.URL, "%s %s — only flow has written on it, last on %s",
						ticket.Key, ticket.Title, day(last))
				}
			}
			result := t.grade(env, false)
			switch {
			case t.counts.Total == 0:
				return result.Said("nothing on the board is marked blocked")
			case result.State == StatePass:
				return result.Said("all %d blocked %s say why",
					t.counts.Total, plural(t.counts.Total, "ticket"))
			}
			return result.Said("%d of %d blocked tickets do not say why",
				t.missed(), t.counts.Total).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "a blocked ticket is a request for somebody's help; the label alone does not say what help",
					Steps: []string{
						"comment on each ticket below saying why it is blocked",
						"say what will unblock it, so that somebody can tell when it is no longer true",
						"say who is monitoring it, so that it is somebody's to chase rather than nobody's",
						unblockStep(label),
						"set workflow.blocked.require_comment to false if this team does not want to be asked",
					},
				})
		},
	}
}

// explanation reports whether anybody but flow has written on a ticket, and
// when flow last wrote where nobody else has.
//
// Flow's own writing does not count: a nudge flow left on a follow-up is not
// somebody explaining themselves, and a check that counted it would be
// answered by flow's own daily run. The marker is what tells them apart,
// rather than the author, because the account flow writes as is the team's own
// and may be a person's.
func explanation(comments []core.TicketComment) (said bool, lastFromFlow time.Time) {
	for _, comment := range comments {
		if strings.TrimSpace(core.StripMarkers(comment.Body)) == "" {
			continue
		}
		if !core.Marked(comment.Body) {
			return true, time.Time{}
		}
		if comment.CreatedAt.After(lastFromFlow) {
			lastFromFlow = comment.CreatedAt
		}
	}
	return false, lastFromFlow
}

// unblockStep is the remediation step for work that is not really blocked. A
// team that marks blocked work through the tracker's own flag has no label to
// take off, and naming one they have never configured would send them looking
// for it.
func unblockStep(label string) string {
	if label == "" {
		return "work that is not really blocked should lose the tracker's blocked flag instead"
	}
	return "work that is not really blocked should lose the " + label + " label instead"
}

// prsByTicket indexes pull requests by the tickets they are for, so that a
// board check can ask about a ticket's work without a query per ticket.
func prsByTicket(env *Env, prs []core.PullRequest) map[string][]core.PullRequest {
	index := make(map[string][]core.PullRequest)
	for _, pr := range prs {
		for _, key := range linkedKeys(env, pr) {
			if id := normalKey(key); id != "" {
				index[id] = append(index[id], pr)
			}
		}
	}
	return index
}

// ticketKey and normalKey are the one spelling of a key both sides of the
// index agree on. Trackers differ on the case of their own keys — Jira writes
// PROJ-12 and accepts proj-12 — and an index that did not fold them would
// report a ticket as having no work because somebody typed it in lower case.
func ticketKey(ticket core.Ticket) string { return normalKey(ticket.Key) }

func normalKey(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

// workingDaysBefore walks back n working days from now.
//
// Saturday and Sunday are the weekend, in whatever zone the clock is in. flow
// does not know a team's calendar and will not ask for one: the furthest this
// can be wrong is the two days of a different weekend, and the only check that
// reads it warns.
func workingDaysBefore(now time.Time, n int) time.Time {
	cutoff := now
	for left := n; left > 0; {
		cutoff = cutoff.AddDate(0, 0, -1)
		switch cutoff.Weekday() {
		case time.Saturday, time.Sunday:
		default:
			left--
		}
	}
	return cutoff
}
