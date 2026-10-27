package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/17twenty/flow/internal/core"
)

// SyncChecks are the live checks whose remedy is flow's own act, which is
// what `flow sync` runs (spec 20 §2). `doctor --fix` runs the same checks
// through the same code, so the two cannot disagree about what is owed.
var SyncChecks = []string{CheckLiveTransitionsFlow, CheckLivePRsClosedIssues}

// prsClosedIssues asks whether every merged pull request closed the issues
// the forge says it closes, and under --fix closes the ones it did not.
//
// A merge GitHub performs for the Actions token — which is how a pull
// request the gate armed lands — is one it does not let cause further
// events, so `Closes #N` is read and nothing is closed (issue #201). The
// ticket then sits in review with its work merged, and the native
// `pr_merged` and `issue_closed` transitions never fire. The forge has
// already said which issues the merge closes, so finishing the job is a
// catch-up like any other flow owns, not a guess.
func (a *audit) prsClosedIssues() Check {
	return Check{
		ID:        CheckLivePRsClosedIssues,
		Title:     "Every merged pull request closed the issues it names",
		Category:  CategoryLive,
		DependsOn: []string{CheckForgeRepo, CheckAuthTracker},
		Run: func(ctx context.Context, env *Env) Result {
			if env.Adapters.Tracker == nil {
				return Failed(errNoTracker)
			}
			prs, err := a.merged(ctx, env)
			if err != nil {
				return Failed(err)
			}
			var t tally
			var caught, refused []Evidence
			for _, pr := range prs {
				for _, key := range pr.ClosingIssues {
					ticket, err := env.Adapters.Tracker.Ticket(ctx, key)
					switch {
					case errors.Is(err, core.ErrNotFound):
						// An issue in another repository, or one deleted
						// since: not this tracker's to say anything about.
						continue
					case err != nil:
						return Failed(core.Explain(err, "read %s, which #%d closes", key, pr.Number))
					case ticket.Closed:
						t.held()
						continue
					case !env.Fix:
						t.broke(ticket.URL, "%s %s — #%d merged %s and it is still open",
							ticket.Key, ticket.Title, pr.Number, day(pr.MergedAt))
						continue
					}
					if err := closeMerged(ctx, env, ticket, pr); err != nil {
						t.broke(ticket.URL, "%s %s — flow could not close it: %v", ticket.Key, ticket.Title, err)
						refused = append(refused, Evidence{Text: ticket.Key, URL: ticket.URL})
						continue
					}
					t.held()
					caught = append(caught, Evidence{
						Text: fmt.Sprintf("%s %s — #%d merged %s and the forge left it open, so flow closed it",
							ticket.Key, ticket.Title, pr.Number, day(pr.MergedAt)),
						URL: ticket.URL,
					})
				}
			}
			result := t.grade(env, false)
			for _, evidence := range caught {
				result = result.Saw(evidence.Text, evidence.URL)
			}
			switch {
			case t.counts.Total == 0:
				return result.Said("no merged pull request in the window named an issue this tracker holds")
			case result.State == StatePass && len(caught) > 0:
				return result.Said("all %d %s a merge names are closed, %d of them by flow just now",
					t.counts.Total, plural(t.counts.Total, "issue"), len(caught))
			case result.State == StatePass:
				return result.Said("all %d %s a merge names are closed", t.counts.Total, plural(t.counts.Total, "issue"))
			}
			result = result.Said("%d of %d issues a merged pull request names are still open", t.missed(), t.counts.Total)
			if !env.Fix {
				return result.Fix(Remediation{
					Kind:    RemedyAuto,
					Summary: "flow can close them itself",
					Command: "flow doctor --fix",
				})
			}
			return result.Fix(Remediation{
				Kind:    RemedyManual,
				Summary: fmt.Sprintf("flow tried to close %d %s and could not", len(refused), plural(len(refused), "issue")),
				Steps: []string{
					"check auth.tracker in this report: closing an issue needs a credential that may write to it",
					"close them by hand; the next run will agree with whatever you do",
				},
			})
		},
	}
}

// closeMerged closes one issue a merged pull request names, saying why on it
// first so that whoever is notified can see which merge did the work.
func closeMerged(ctx context.Context, env *Env, ticket core.Ticket, pr core.PullRequest) error {
	marker := core.NewMarker(core.MarkerClosed, "pr", strconv.Itoa(pr.Number))
	body := fmt.Sprintf("#%d merged on %s and closes this, but the forge did not close it — a merge GitHub performs for the Actions token causes no further events. Closed by `flow doctor --fix`.",
		pr.Number, day(pr.MergedAt))
	if err := env.Adapters.Tracker.Comment(ctx, ticket.Key, marker, body); err != nil {
		return err
	}
	if err := env.Adapters.Tracker.Close(ctx, ticket.Key, core.ClosedCompleted); err != nil {
		return err
	}
	if env.Log != nil {
		env.Log.Info("doctor closed an issue its merged pull request left open",
			"ticket", ticket.Key, "pull_request", pr.Number)
	}
	return nil
}
