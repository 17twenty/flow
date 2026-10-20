package engine

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The live-health audit of work in progress (spec 07 §6).
//
// A WIP limit is the one rule in a lean workflow that is about a moment
// rather than about a change: not "this pull request is wrong" but "there is
// too much in flight to be finishing any of it". The gate already holds one
// pull request to it as it opens — `wip.review` and `wip.in_progress` — and
// that is the whole of what flow knew about limits, because a gate only ever
// looks at now.
//
// Asking whether the limits held *over a fortnight* runs into the thing no
// provider does: a board carries one moment, the one it is read in. GitHub
// Projects, Jira and Linear all record that an item's status changed and none
// of them will answer what a column held last Tuesday. So the past has to be
// reconstructed from the records that do carry dates, which are the pull
// requests: when each opened, and when it merged or was closed.
//
// Two approximations follow, and both are stated in the check's own evidence
// rather than presented as a reading. A pull request counts for every day
// between opening and merging, because the day it entered the review column
// is exactly what nothing records. And it counts in the column its state now
// puts it in — a draft is somebody's work in progress, anything else is
// waiting for review — because its state now is the only one the forge will
// give. A pull request that spent a week as a draft and a day in review is
// therefore read as a week in review, which is why the check warns and never
// fails: it is a signal that the team is carrying more than it agreed to, not
// a measurement anybody should be held to.
//
// Today is different, and better: the board is right there. So the last day
// of the window is read from the tracker — the same bucketing the gate's WIP
// rules use — and only the days before it are reconstructed.
const CheckLiveWIP = "live.wip"

// wip asks how often the team was carrying more work at once than it agreed
// to carry.
func (a *audit) wip() Check {
	return Check{
		ID:        CheckLiveWIP,
		Title:     "The work in flight stayed inside the limits the team set",
		Category:  CategoryLive,
		DependsOn: []string{CheckAuthTracker, CheckForgeRepo},
		Run: func(ctx context.Context, env *Env) Result {
			limits := readWIPLimits(env.Config)
			if !limits.set() {
				return Skip("no column in the workflow carries a WIP limit")
			}
			board, err := a.board(ctx, env)
			if err != nil {
				return Failed(err)
			}
			spans, err := a.wipSpans(ctx, env, board)
			if err != nil {
				return Failed(err)
			}
			today := midnight(env.Now())
			var t tally
			for d := midnight(env.Since()); !d.After(today); d = d.AddDate(0, 0, 1) {
				count := spanCount(spans, d)
				if d.Equal(today) {
					count = boardCount(env, board)
				}
				broken := limits.broken(count)
				if len(broken) == 0 {
					t.held()
					continue
				}
				t.broke("", "%s — %s", day(d), strings.Join(broken, "; "))
			}
			result := t.gradeWarning(env)
			result.Evidence = append([]Evidence{{Text: wipNote}}, result.Evidence...)
			if result.State == StatePass {
				return result.Said("all %d %s in the window were inside %s",
					t.counts.Total, plural(t.counts.Total, "day"), limits.describe())
			}
			return result.Said("%d of %d days in the window were over %s",
				t.missed(), t.counts.Total, limits.describe()).
				Fix(Remediation{
					Kind:    RemedyManual,
					Summary: "carry less at once, or set the limit to the number the team really carries",
					Steps: []string{
						"a limit nobody keeps to is not a limit: agree the number again before raising it",
						"the gate refuses a new pull request while the review column is over its limit, so these are the days somebody worked around it",
						"expedited work is outside the limits — label it as the exception rather than going over",
						"this reading is reconstructed, so check it against what the team remembers before acting on it",
					},
				})
		},
	}
}

// wipNote is the sentence that keeps the reconstruction from reading as a
// record. It is evidence rather than part of the message because it qualifies
// every day the check counted, including the ones that held.
const wipNote = "every day but today is reconstructed from how long each pull request was open, " +
	"because no tracker records what a column held last week"

// wipLimits is what the team agreed to carry: the cap on the review column as
// a whole, and the cap on any one person's work in progress. Either may be
// unset, which is the same as uncapped.
//
// The columns are found by the transitions that land work in them, exactly as
// the gate's rules find them — a team that renamed a column has not stopped
// meaning it, and two ways of finding the review column would be two answers
// waiting to differ.
type wipLimits struct {
	review    config.Column
	limit     int
	progress  config.Column
	perPerson int
}

// readWIPLimits reads both limits out of the workflow.
func readWIPLimits(cfg *config.Config) wipLimits {
	var limits wipLimits
	if column, found := columnFor(cfg, config.EventPRReadyForReview); found && column.WIP != nil {
		limits.review, limits.limit = column, column.WIP.Limit
	}
	if column, found := columnFor(cfg, config.EventPROpenedDraft); found && column.WIP != nil {
		limits.progress, limits.perPerson = column, column.WIP.PerPerson
	}
	return limits
}

// set reports whether there is any limit to audit.
func (w wipLimits) set() bool { return w.limit > 0 || w.perPerson > 0 }

// describe names the limits the way the report should say them.
func (w wipLimits) describe() string {
	var parts []string
	if w.limit > 0 {
		parts = append(parts, fmt.Sprintf("the limit of %d on %s", w.limit, w.review.Name))
	}
	if w.perPerson > 0 {
		parts = append(parts, fmt.Sprintf("the limit of %d each in %s", w.perPerson, w.progress.Name))
	}
	return strings.Join(parts, " and ")
}

// broken says which limits one day's reading was over, in a fixed order so
// that two runs of the same window read the same.
func (w wipLimits) broken(count wipCount) []string {
	var out []string
	if w.limit > 0 && count.review > w.limit {
		out = append(out, fmt.Sprintf("%s held %d, over its limit of %d",
			w.review.Name, count.review, w.limit))
	}
	if w.perPerson > 0 {
		for _, login := range slices.Sorted(maps.Keys(count.person)) {
			if count.person[login] > w.perPerson {
				out = append(out, fmt.Sprintf("%s had %d in %s, over the limit of %d each",
					login, count.person[login], w.progress.Name, w.perPerson))
			}
		}
	}
	return out
}

// wipCount is one day's reading: what the review column held, and how much
// each person had in progress.
type wipCount struct {
	review int
	person map[string]int
}

// carried records one more thing in flight for a person.
//
// A login nobody gave is nobody's: a per-person limit cannot be applied to a
// person the provider would not name, which is the same answer the gate's
// `wip.in_progress` rule gives a pull request whose author it does not know.
// The rest are folded, because the board and the forge are two providers
// naming one account and only one of them has to be canonical about it.
func (c wipCount) carried(login string) {
	if login == "" {
		return
	}
	c.person[strings.ToLower(login)]++
}

// boardCount is today, read rather than reconstructed. It buckets the board
// the way the gate's WIP rules do, so that the day the report is printed on
// agrees with what the gate would say about a pull request opened that
// minute.
func boardCount(env *Env, board []core.Ticket) wipCount {
	counts := wipFrom(env.Config, env.Lock, board)
	count := wipCount{person: map[string]int{}}
	count.review = len(countable(env.Config, counts.Review))
	for _, item := range countable(env.Config, counts.InProgress) {
		for _, login := range item.Assignees {
			count.carried(login)
		}
	}
	return count
}

// spanCount is one past day, reconstructed. A pull request counts on every
// day it was open, from the day it was created to the day it merged or was
// closed, both included: a day it was in flight for an hour was still a day
// somebody was carrying it.
func spanCount(spans []wipSpan, d time.Time) wipCount {
	count := wipCount{person: map[string]int{}}
	next := d.AddDate(0, 0, 1)
	for _, span := range spans {
		if !span.opened.Before(next) {
			continue
		}
		if !span.closed.IsZero() && span.closed.Before(d) {
			continue
		}
		if !span.draft {
			count.review++
			continue
		}
		count.carried(span.author)
	}
	return count
}

// wipSpan is one pull request as the reconstruction reads it.
type wipSpan struct {
	opened time.Time
	closed time.Time
	author string
	draft  bool
}

// wipSpans is every pull request the audit can date. The two listings are
// disjoint — a pull request is merged or it is open — so there is nothing to
// deduplicate between them.
//
// What merged in the window and what is open now is what the audit reads, and
// a pull request somebody abandoned mid-window is in neither — so the days it
// was open are counted one short. That is the right way round for a check
// that warns: it says less than happened rather than more, and the alternative
// is a third listing of every closed pull request for a reading nobody would
// act on.
func (a *audit) wipSpans(ctx context.Context, env *Env, board []core.Ticket) ([]wipSpan, error) {
	merged, err := a.merged(ctx, env)
	if err != nil {
		return nil, err
	}
	open, err := a.inFlight(ctx, env)
	if err != nil {
		return nil, err
	}
	labels := make(map[string][]string, len(board))
	for _, ticket := range board {
		labels[ticketKey(ticket)] = ticket.Labels
	}
	var spans []wipSpan
	for _, pr := range slices.Concat(merged, open) {
		if wipExempt(env, pr, labels) {
			continue
		}
		spans = append(spans, wipSpan{
			opened: pr.CreatedAt,
			closed: cmp.Or(pr.MergedAt, pr.ClosedAt),
			author: pr.Author,
			draft:  pr.Draft,
		})
	}
	return spans, nil
}

// wipExempt reports whether this pull request's work is the team's own agreed
// exception to the limits, which is the same question `wip.review` asks of
// the pull request in front of it.
func wipExempt(env *Env, pr core.PullRequest, labels map[string][]string) bool {
	if expediteLabelled(env.Config, pr.Labels) {
		return true
	}
	for _, key := range linkedKeys(env, pr) {
		if expediteLabelled(env.Config, labels[normalKey(key)]) {
			return true
		}
	}
	return false
}

// midnight is the start of the UTC day an instant falls in. The audit counts
// days, and a day has to begin somewhere that does not move with the reader.
func midnight(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
