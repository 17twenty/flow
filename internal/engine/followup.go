package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Follow-ups (spec 07 §4).
//
// A follow-up is flow admitting it cannot do something: the provider has no
// API for it, or the credential is not allowed to use the one it has. Rather
// than printing a warning every day for a year, flow files a ticket in the
// team's own tracker, assigns it to whoever can act, and then gets out of the
// way — the check reports `external`, which is "waiting on somebody else"
// rather than "broken".
//
// The lifecycle is one loop, run on every doctor run: file it if it is not
// there, chase it if it has been a while, close it when the thing it asked
// for is finally true, reopen it if somebody closed it without doing the
// work. Each step is driven by what flow can see now, not by what the lock
// remembers, because the lock is the thing most likely to be wrong.

// NudgeInterval is how often flow will comment on an outstanding follow-up.
// A ticket that says the same thing every morning is a ticket people filter
// out of their inbox, and a follow-up nobody reads is worse than none: it
// looks like the work is tracked.
const NudgeInterval = 7 * 24 * time.Hour

// Owner roles. A check says which kind of access its remedy needs, and the
// configuration says who holds it here (spec 03 §2).
const (
	// OwnerDefault receives anything no other role claims.
	OwnerDefault = "default"
	// OwnerTrackerAdmin receives work needing tracker administration.
	OwnerTrackerAdmin = "tracker_admin"
	// OwnerForgeAdmin receives work needing repository administration.
	OwnerForgeAdmin = "forge_admin"
	// OwnerPlatform receives work needing deployment or infrastructure access.
	OwnerPlatform = "platform"
)

// What a round of the lifecycle did, for the log and for tests.
const (
	// FollowUpFiled means a ticket was opened.
	FollowUpFiled = "filed"
	// FollowUpAdopted means a ticket flow had forgotten was found again by
	// its marker.
	FollowUpAdopted = "adopted"
	// FollowUpNudged means flow commented to say it is still outstanding.
	FollowUpNudged = "nudged"
	// FollowUpWaiting means the ticket is open and it is not time to chase.
	FollowUpWaiting = "waiting"
	// FollowUpRefreshed means the ticket was rewritten because what flow has
	// to say about the work has changed since it was filed.
	FollowUpRefreshed = "refreshed"
	// FollowUpVerified means flow saw the work done, said so and closed it.
	FollowUpVerified = "verified"
	// FollowUpWithdrawn means the work stopped being needed, which flow said
	// before closing it.
	FollowUpWithdrawn = "withdrawn"
	// FollowUpReopened means somebody closed it but the work is not done.
	FollowUpReopened = "reopened"
	// FollowUpNone means there was nothing to do.
	FollowUpNone = "none"
)

// FollowUpInput is one check's follow-up, reconciled against the tracker.
type FollowUpInput struct {
	// Check is the check the follow-up belongs to: its id is in the marker,
	// so renaming one orphans the ticket.
	Check Check
	// Result is what the check found this run. Whether it holds is the whole
	// of what drives the lifecycle.
	Result Result
	// Config is the workflow, which says what to label a follow-up and who to
	// give it to.
	Config *config.Config
	// Lock is what flow filed before, and where this round is recorded.
	Lock *config.Lock
	// Tracker is where the ticket lives. Nil means there is nowhere to file.
	Tracker core.Tracker
	// Repo is the repository the follow-up is about, which is in its title
	// and its marker: one tracker serves many repositories.
	Repo string
	// Clock is where "a week ago" comes from.
	Clock core.Clock
	// Fix permits flow to write to the tracker. Without it the lifecycle
	// reports what it would have done (spec 02 §2).
	Fix bool
	// Log receives one line per mutating call.
	Log *slog.Logger
}

// FollowUpOutcome is what became of one follow-up.
type FollowUpOutcome struct {
	// Check is the check id it belongs to.
	Check string
	// Action is what the lifecycle did, one of the FollowUp constants.
	Action string
	// Ticket is the follow-up as the tracker now has it, zero when there is
	// none.
	Ticket core.Ticket
	// Would reports that Action is what flow would have done had it been
	// allowed to write.
	Would bool
	// Result is what the check reports once the follow-up is accounted for:
	// an open follow-up turns a failure into `external`.
	Result Result
	// Err is why the tracker could not be reconciled.
	Err error
}

// Wants reports whether a check's finding asks for a ticket: it did not hold,
// and the remedy is something only a human with other access can do.
func (in FollowUpInput) Wants() bool {
	return !in.Result.State.Satisfies() && in.Result.Remediation.Kind == RemedyFollowUp
}

// ReconcileFollowUp runs one round of the lifecycle for one check.
//
// It never fails the run: a tracker that will not answer leaves the check's
// own finding intact and reports why the ticket could not be reconciled.
// Being unable to file a ticket about a problem is not a second problem to
// report in place of the first.
func ReconcileFollowUp(ctx context.Context, in FollowUpInput) FollowUpOutcome {
	if in.Log == nil {
		in.Log = slog.New(slog.DiscardHandler)
	}
	out := FollowUpOutcome{Check: in.Check.ID, Action: FollowUpNone, Result: in.Result}
	if in.Tracker == nil || in.Lock == nil {
		return out
	}

	marker := FollowUpMarker(in.Check.ID, in.Repo)
	// The tracker is asked before the lock is believed. A lock that lost its
	// record — a bad merge, a fresh clone of a branch that never had it — must
	// not cause a second ticket for the same thing, and the marker is what
	// makes that impossible (spec 07 §4).
	found, err := in.Tracker.FindFollowUp(ctx, marker)
	if err != nil {
		out.Err = err
		return out
	}

	switch {
	case found == nil:
		out = in.file(ctx, out, marker)
	case in.Wants():
		out = in.chase(ctx, out, *found)
	default:
		out = in.finish(ctx, out, *found)
	}
	if out.Err == nil {
		out.Err = in.retire(ctx, out)
	}
	return out
}

// CheckOf is the doctor check a desired item is reported under. They are the
// same id for every item but one: the deployment records are an item flow
// reads and a check about whether it can (spec 07 §5). A follow-up is keyed
// by the check, so apply and doctor file and find the same ticket.
func CheckOf(item string) string {
	if item == core.ItemDeploysRecords {
		return CheckDeploysSource
	}
	return item
}

// formerly are the ids a check's follow-up was once filed under. apply used
// to file under the item id, so a repository can hold a ticket for
// deploys.records beside doctor's for deploys.source (issue #202).
var formerly = map[string][]string{
	CheckDeploysSource: {core.ItemDeploysRecords},
}

// retire closes a ticket an earlier flow filed for this check under another
// id, saying which ticket now stands for it, and drops it from the lock. Two
// tickets about one fact are two accounts of it, and the one nobody updates
// is the one somebody will act on.
func (in FollowUpInput) retire(ctx context.Context, out FollowUpOutcome) error {
	for _, old := range formerly[in.Check.ID] {
		stale, err := in.Tracker.FindFollowUp(ctx, FollowUpMarker(old, in.Repo))
		if err != nil {
			return err
		}
		if stale == nil || stale.Key == out.Ticket.Key {
			continue
		}
		if !stale.Closed {
			if !in.Fix {
				continue
			}
			body := fmt.Sprintf("flow now tracks this as `%s`, not `%s`. ", in.Check.ID, old)
			if out.Ticket.Key != "" {
				body += fmt.Sprintf("%s is the one ticket for it.", out.Ticket.Key)
			} else {
				body += "Nothing is outstanding under that check."
			}
			marker := core.NewMarker(core.MarkerFollowUp, "check", old, "repo", in.Repo, "part", "status")
			if err := in.Tracker.Comment(ctx, stale.Key, marker, body); err != nil {
				return err
			}
			if err := in.Tracker.Close(ctx, stale.Key, core.ClosedNotPlanned); err != nil {
				return err
			}
			in.Log.Info("flow closed a follow-up filed under a former check id",
				"check", in.Check.ID, "former", old, "ticket", stale.Key)
		}
		for i, f := range in.Lock.FollowUps {
			if f.Check == old {
				in.Lock.FollowUps = append(in.Lock.FollowUps[:i], in.Lock.FollowUps[i+1:]...)
				break
			}
		}
	}
	return nil
}

// file opens a ticket, when one is wanted and there is none.
func (in FollowUpInput) file(ctx context.Context, out FollowUpOutcome, marker core.Marker) FollowUpOutcome {
	if !in.Wants() {
		// Nothing wanted and nothing filed: the ordinary case, every day,
		// for every check that holds.
		in.forget()
		return out
	}
	owner := in.owner()
	if !in.Fix {
		out.Action, out.Would = FollowUpFiled, true
		out.Result = external(in.Result, core.Ticket{}, owner)
		return out
	}
	ticket, err := in.Tracker.CreateFollowUp(ctx, core.FollowUp{
		Marker:   marker,
		Title:    FollowUpTitle(in.Check, in.Result, in.Repo),
		Body:     FollowUpBody(in.Check, in.Result),
		Labels:   in.labels(),
		Assignee: owner,
	})
	if err != nil {
		out.Err = err
		return out
	}
	in.record(ticket, owner, config.FollowUpOpen, time.Time{})
	in.Log.Info("flow filed a follow-up",
		"check", in.Check.ID, "ticket", ticket.Key, "owner", owner)
	out.Action, out.Ticket = FollowUpFiled, ticket
	out.Result = external(in.Result, ticket, owner)
	return out
}

// refresh rewrites an open follow-up when the ticket no longer says what flow
// would say about the work today.
//
// It is not a nudge. A nudge is a comment that asks somebody to act and is
// rationed to one a week (spec 07 §4); this is flow correcting its own
// writing, which costs the reader nothing and must not wait for the clock —
// a ticket that is wrong for six days is wrong for the six days somebody
// might have read it. The tracker is asked only when the text really differs,
// so a daily run over a steady finding writes nothing.
func (in FollowUpInput) refresh(ctx context.Context, ticket core.Ticket) (bool, error) {
	marker := FollowUpMarker(in.Check.ID, in.Repo)
	title := FollowUpTitle(in.Check, in.Result, in.Repo)
	body := core.MarkBody(FollowUpBody(in.Check, in.Result), marker, true)
	if title == ticket.Title && body == ticket.Body {
		return false, nil
	}
	if !in.Fix {
		return false, nil
	}
	if err := in.Tracker.UpdateFollowUp(ctx, ticket.Key, marker, title, body); err != nil {
		return false, err
	}
	in.Log.Info("flow brought a follow-up up to date",
		"check", in.Check.ID, "ticket", ticket.Key)
	return true, nil
}

// chase is the ticket that exists and whose work is still outstanding: nudge
// it if it has been long enough, reopen it if somebody closed it anyway.
func (in FollowUpInput) chase(ctx context.Context, out FollowUpOutcome, ticket core.Ticket) FollowUpOutcome {
	out.Ticket = ticket
	owner := in.ownerOf(ticket)
	out.Result = external(in.Result, ticket, owner)

	if ticket.Closed {
		// Somebody marked it done and it is not done. Saying so, with what
		// flow just saw, is the whole value of verifying rather than trusting
		// the ticket: a closed follow-up that was never acted on is how a
		// workflow quietly stops being true.
		if !in.Fix {
			out.Action, out.Would = FollowUpReopened, true
			return out
		}
		if err := in.Tracker.Reopen(ctx, ticket.Key, in.stillWrong()); err != nil {
			out.Err = err
			return out
		}
		if err := in.Tracker.Comment(ctx, ticket.Key, in.commentMarker(), in.reopenedBody()); err != nil {
			out.Err = err
			return out
		}
		in.record(ticket, owner, config.FollowUpOpen, in.now())
		in.Log.Info("flow reopened a follow-up that was closed without the work being done",
			"check", in.Check.ID, "ticket", ticket.Key)
		out.Action = FollowUpReopened
		return out
	}

	// What flow has to say about the work can change after the ticket is
	// filed: a new release knows steps the old one did not, and a summary
	// that has improved is a title somebody can tell apart in the issue list
	// (spec 07 §4). A follow-up is flow's own writing, so flow keeps it
	// current rather than leaving a stale ticket behind a correct report.
	refreshed, err := in.refresh(ctx, ticket)
	if err != nil {
		out.Err = err
		return out
	}

	if _, known := in.recorded(); !known {
		// Flow found a ticket it had no memory of: a lock that lost the
		// record to a bad merge, or a fresh clone of a branch that never had
		// it. Adopting it is what stops a second ticket being filed for the
		// same thing, and the nudge clock starts from the adoption rather
		// than from a date flow does not know.
		in.record(ticket, owner, config.FollowUpOpen, time.Time{})
		in.Log.Info("flow adopted a follow-up it had forgotten",
			"check", in.Check.ID, "ticket", ticket.Key)
		out.Action = FollowUpAdopted
		return out
	}
	if !in.due() {
		out.Action = FollowUpWaiting
		if refreshed {
			out.Action = FollowUpRefreshed
		}
		return out
	}
	if !in.Fix {
		out.Action, out.Would = FollowUpNudged, true
		return out
	}
	if err := in.Tracker.Comment(ctx, ticket.Key, in.commentMarker(), in.nudgeBody()); err != nil {
		out.Err = err
		return out
	}
	in.record(ticket, owner, config.FollowUpOpen, in.now())
	in.Log.Info("flow chased an outstanding follow-up",
		"check", in.Check.ID, "ticket", ticket.Key)
	out.Action = FollowUpNudged
	return out
}

// finish is the ticket whose reason has gone: either the work was done, or
// the configuration stopped asking for it. Both end in a comment saying which
// and a close, because a ticket that goes silent teaches people to ignore the
// next one.
func (in FollowUpInput) finish(ctx context.Context, out FollowUpOutcome, ticket core.Ticket) FollowUpOutcome {
	out.Ticket = ticket
	action, body, reason := FollowUpVerified, in.verifiedBody(), core.ClosedCompleted
	if !in.Result.State.Satisfies() {
		// The check still does not hold, but it no longer wants a ticket:
		// flow can do it itself now, or a human must do something else. The
		// follow-up as written is no longer the right ask.
		action, body, reason = FollowUpWithdrawn, in.withdrawnBody(), core.ClosedNotPlanned
	}
	if ticket.Closed {
		// Already closed, and rightly. Catch the lock up without touching the
		// tracker: an edit would email everybody who ever commented.
		in.record(ticket, in.ownerOf(ticket), config.FollowUpClosed, time.Time{})
		out.Action = FollowUpNone
		return out
	}
	if !in.Fix {
		out.Action, out.Would = action, true
		return out
	}
	if err := in.Tracker.Comment(ctx, ticket.Key, in.commentMarker(), body); err != nil {
		out.Err = err
		return out
	}
	if err := in.Tracker.Close(ctx, ticket.Key, reason); err != nil {
		out.Err = err
		return out
	}
	in.record(ticket, in.ownerOf(ticket), config.FollowUpClosed, time.Time{})
	in.Log.Info("flow closed a follow-up", "check", in.Check.ID, "ticket", ticket.Key, "why", action)
	out.Action = action
	return out
}

// due reports whether it has been long enough to chase again. A follow-up
// filed this morning is not chased this afternoon.
func (in FollowUpInput) due() bool {
	record, ok := in.recorded()
	if !ok {
		return false
	}
	last := record.NudgedAt
	if last.IsZero() {
		last = record.OpenedAt
	}
	return !in.now().Before(last.Add(NudgeInterval))
}

// recorded is what the lock remembers about this check's follow-up.
func (in FollowUpInput) recorded() (config.FollowUp, bool) {
	for _, f := range in.Lock.FollowUps {
		if f.Check == in.Check.ID {
			return f, true
		}
	}
	return config.FollowUp{}, false
}

// record writes this round into the lock, replacing whatever was there.
func (in FollowUpInput) record(ticket core.Ticket, owner, state string, nudged time.Time) {
	opened := in.now()
	if previous, ok := in.recorded(); ok && !previous.OpenedAt.IsZero() {
		opened = previous.OpenedAt
	}
	entry := config.FollowUp{
		Check:    in.Check.ID,
		Ticket:   ticket.Key,
		Owner:    owner,
		OpenedAt: opened,
		NudgedAt: nudged,
		State:    state,
	}
	for i, f := range in.Lock.FollowUps {
		if f.Check == in.Check.ID {
			in.Lock.FollowUps[i] = entry
			return
		}
	}
	in.Lock.FollowUps = append(in.Lock.FollowUps, entry)
}

// forget drops the lock's record of a follow-up that is not wanted and does
// not exist. The alternative is a lock that accumulates every ticket flow
// ever filed, which is a changelog rather than a statement of what is true.
func (in FollowUpInput) forget() {
	for i, f := range in.Lock.FollowUps {
		if f.Check == in.Check.ID {
			in.Lock.FollowUps = append(in.Lock.FollowUps[:i], in.Lock.FollowUps[i+1:]...)
			return
		}
	}
}

// now is this run's instant.
func (in FollowUpInput) now() time.Time {
	if in.Clock == nil {
		return time.Time{}
	}
	return in.Clock.Now()
}

// owner is who this check's remedy should go to, resolved from the
// configuration and whatever doctor has since learned (spec 07 §3).
func (in FollowUpInput) owner() string {
	return Owner(in.Config, in.Lock, in.Result.Remediation.Owner)
}

// ownerOf prefers the person the ticket is actually assigned to. Somebody
// reassigned it because they knew better than the configuration did, and
// taking it back off them every morning would be flow arguing with a human.
func (in FollowUpInput) ownerOf(ticket core.Ticket) string {
	if len(ticket.Assignees) > 0 {
		return ticket.Assignees[0]
	}
	return in.owner()
}

// labels are what every follow-up carries, so a team can find them all.
func (in FollowUpInput) labels() []string {
	if in.Config == nil {
		return nil
	}
	return in.Config.FollowUps.Labels
}

// commentMarker identifies flow's own comment on a follow-up, so that the
// weekly nudge edits one comment rather than growing a column of them.
func (in FollowUpInput) commentMarker() core.Marker {
	return core.NewMarker(core.MarkerFollowUp, "check", in.Check.ID, "repo", in.Repo, "part", "status")
}

// Owner resolves an owner role to a handle. A role nobody filled falls back
// to the default, and a default nobody filled is nobody: an unassigned ticket
// in the right place beats a ticket assigned to "${pending:owners.platform}".
func Owner(cfg *config.Config, lock *config.Lock, role string) string {
	if cfg == nil {
		return ""
	}
	if role == "" {
		role = OwnerDefault
	}
	owners := map[string]config.Pending{
		OwnerDefault:      cfg.FollowUps.Owners.Default,
		OwnerTrackerAdmin: cfg.FollowUps.Owners.TrackerAdmin,
		OwnerForgeAdmin:   cfg.FollowUps.Owners.ForgeAdmin,
		OwnerPlatform:     cfg.FollowUps.Owners.Platform,
	}
	if handle := resolveOwner(lock, role, owners[role]); handle != "" {
		return handle
	}
	if role == OwnerDefault {
		return ""
	}
	return resolveOwner(lock, OwnerDefault, owners[OwnerDefault])
}

// resolveOwner is one role: the configured handle, or what doctor resolved
// into the lock for it.
func resolveOwner(lock *config.Lock, role string, value config.Pending) string {
	if !value.IsPending() {
		return strings.TrimSpace(value.Text)
	}
	if lock == nil {
		return ""
	}
	return strings.TrimSpace(lock.Resolved["followups.owners."+role])
}

// FollowUpMarker is what makes filing the same follow-up twice impossible,
// and what finds it again when the lock has forgotten (spec 07 §4).
func FollowUpMarker(check, repo string) core.Marker {
	return core.NewMarker(core.MarkerFollowUp, "check", check, "repo", repo)
}

// FollowUpTitle is the ticket's one line. The repository is in it because one
// tracker serves many, and a list of six tickets all called "flow: protect the
// trunk" is a list nobody can triage.
func FollowUpTitle(check Check, result Result, repo string) string {
	summary := strings.TrimSpace(result.Remediation.Summary)
	if summary == "" {
		summary = strings.TrimSpace(check.Title)
	}
	if repo == "" {
		return "flow: " + summary
	}
	return fmt.Sprintf("flow: %s (%s)", summary, repo)
}

// FollowUpBody is what the ticket says: what the thing is, why it was filed,
// exactly what to do, and how flow will know it is done. The last part is the
// one that matters — it is the promise that nobody will have to come back and
// close this by hand, and it is why the ticket can be trusted to mean
// something.
//
// The first part is the one that was missing. A ticket is read by whoever it
// was assigned to, who has flow's words for the thing and not the provider's:
// "what this is" and "where it lives" come from the adapter's declaration
// (spec 05 §1) and lead, because a list of instructions is no use to somebody
// who does not yet know what they are being asked to look at.
func FollowUpBody(check Check, result Result) string {
	var body strings.Builder
	body.WriteString(result.Message)
	body.WriteString("\n\n")
	switch subject, venue := result.Remediation.Subject, result.Remediation.Venue; {
	case subject != "" || venue != "":
		body.WriteString("## What this is\n\n")
		for _, part := range []string{subject, venue} {
			if part == "" {
				continue
			}
			body.WriteString(part)
			body.WriteString("\n\n")
		}
	default:
		// No declaration to speak for the provider. The remedy's own
		// summary is all there is — and it is also the ticket's title, so
		// it goes in only when it says something the message did not.
		if why := strings.TrimSpace(result.Remediation.Summary); why != "" && why != result.Message {
			body.WriteString(why)
			body.WriteString("\n\n")
		}
	}

	body.WriteString("## What to do\n\n")
	switch {
	case len(result.Remediation.Steps) > 0:
		for i, step := range result.Remediation.Steps {
			fmt.Fprintf(&body, "%d. %s\n", i+1, step)
		}
	case result.Remediation.Command != "":
		body.WriteString("Run:\n\n```\n" + result.Remediation.Command + "\n```\n")
	default:
		body.WriteString("Make the check below hold. Flow has no steps to offer for this one.\n")
	}

	if len(result.Evidence) > 0 {
		body.WriteString("\n## What flow saw\n\n")
		for _, evidence := range result.Evidence {
			body.WriteString("- " + evidenceLine(evidence) + "\n")
		}
	}

	body.WriteString("\n## How flow will know it is done\n\n")
	fmt.Fprintf(&body, "`flow doctor` asks %q on every run, as check `%s`.\n", check.Title, check.ID)
	body.WriteString("When it holds, flow will comment here and close this ticket. ")
	body.WriteString("Nobody needs to close it by hand.\n")
	return body.String()
}

// nudgeBody is the weekly chase. It says how long it has been, because "this
// is still outstanding" is easy to skim past and "this has been outstanding
// for six weeks" is not.
func (in FollowUpInput) nudgeBody() string {
	var body strings.Builder
	body.WriteString("Still outstanding")
	if record, ok := in.recorded(); ok && !record.OpenedAt.IsZero() {
		fmt.Fprintf(&body, " — filed %s ago", config.Duration(in.now().Sub(record.OpenedAt)).String())
	}
	body.WriteString(".\n\n")
	body.WriteString(in.Result.Message)
	body.WriteString("\n\nFlow last looked ")
	body.WriteString(in.now().UTC().Format(time.RFC3339))
	body.WriteString(" and the check still does not hold. ")
	body.WriteString("This comment is edited in place rather than repeated, and flow will chase at most once a week.\n")
	return body.String()
}

// verifiedBody is what flow says when the work is done. It says what it saw,
// so that the close is evidence rather than an assertion.
func (in FollowUpInput) verifiedBody() string {
	var body strings.Builder
	body.WriteString("Done — flow verified this.\n\n")
	body.WriteString(in.Result.Message)
	body.WriteString("\n")
	if len(in.Result.Evidence) > 0 {
		body.WriteString("\n")
		for _, evidence := range in.Result.Evidence {
			body.WriteString("- " + evidenceLine(evidence) + "\n")
		}
	}
	fmt.Fprintf(&body, "\nCheck `%s` held at %s. Closing.\n", in.Check.ID, in.now().UTC().Format(time.RFC3339))
	return body.String()
}

// withdrawnBody is what flow says when the ask stopped being the right one.
func (in FollowUpInput) withdrawnBody() string {
	var body strings.Builder
	body.WriteString("No longer needed.\n\n")
	fmt.Fprintf(&body, "Check `%s` no longer asks for this: ", in.Check.ID)
	switch in.Result.Remediation.Kind {
	case RemedyAuto:
		body.WriteString("flow can do it itself now — run `flow doctor --fix`.\n")
	case RemedyNone, "":
		body.WriteString("the configuration no longer asks for it.\n")
	default:
		fmt.Fprintf(&body, "the remedy is now %q rather than a ticket.\n", in.Result.Remediation.Kind)
	}
	body.WriteString("\nClosing as not planned. The check itself is still reported by `flow doctor`.\n")
	return body.String()
}

// stillWrong is the one line recorded as the reason for reopening.
func (in FollowUpInput) stillWrong() string {
	return fmt.Sprintf("flow checked %s and it still does not hold", in.Check.ID)
}

// reopenedBody explains the reopen, which otherwise looks like flow arguing
// with whoever closed it.
func (in FollowUpInput) reopenedBody() string {
	var body strings.Builder
	body.WriteString("Reopened — this was closed, but flow still cannot see it done.\n\n")
	body.WriteString(in.Result.Message)
	body.WriteString("\n")
	if len(in.Result.Evidence) > 0 {
		body.WriteString("\n")
		for _, evidence := range in.Result.Evidence {
			body.WriteString("- " + evidenceLine(evidence) + "\n")
		}
	}
	body.WriteString("\nIf this is deliberate — the team has decided not to do it — change the ")
	body.WriteString("configuration so flow stops asking, rather than closing the ticket. ")
	body.WriteString("Closing it again will reopen it again.\n")
	return body.String()
}

// evidenceLine is one piece of evidence as a line of markdown.
func evidenceLine(e Evidence) string {
	if e.URL == "" {
		return e.Text
	}
	return fmt.Sprintf("%s — %s", e.Text, e.URL)
}

// external is the check's finding once a follow-up is carrying it: somebody
// else's job, with a name on it and a ticket to watch. It stops being a
// failure the person running doctor can do anything about, which is what
// keeps `flow doctor` honest about whose move it is.
func external(result Result, ticket core.Ticket, owner string) Result {
	result.State = StateExternal
	if ticket.Key != "" {
		result.Evidence = append(result.Evidence, Evidence{
			Text: "follow-up " + ticket.Key,
			URL:  ticket.URL,
		})
	}
	if owner != "" {
		result.Remediation.Owner = owner
	}
	return result
}

// ReconcileFollowUps runs the lifecycle for every check in a report and
// returns what became of each, in the report's own order.
//
// Checks are reconciled one at a time rather than together: each one writes to
// the lock, and a run interrupted halfway must leave a lock that describes the
// tickets that really exist. A lock claiming a ticket that was never filed is
// how flow stops filing it.
func ReconcileFollowUps(ctx context.Context, report Report, in FollowUpInput) []FollowUpOutcome {
	outcomes := make([]FollowUpOutcome, 0, len(report.Checks))
	for _, checked := range report.Checks {
		if err := ctx.Err(); err != nil {
			return outcomes
		}
		round := in
		round.Check = Check{ID: checked.ID, Title: checked.Title, Category: checked.Category}
		round.Result = checked.Result
		outcomes = append(outcomes, ReconcileFollowUp(ctx, round))
	}
	return outcomes
}

// Absorb writes what the follow-up lifecycle decided back into the report.
//
// A check that failed because flow is not allowed to do something reads
// `external` once there is a ticket for it, which is the difference between
// "broken" and "waiting on somebody" — and the difference between a doctor
// run that exits non-zero every morning and one that exits non-zero when
// something has actually gone wrong (spec 07 §4).
func (r *Report) Absorb(outcomes []FollowUpOutcome) {
	for _, outcome := range outcomes {
		if outcome.Action == FollowUpNone || outcome.Result.State == "" {
			continue
		}
		for i := range r.Checks {
			if r.Checks[i].ID == outcome.Check {
				r.Checks[i].Result = outcome.Result
				break
			}
		}
	}
}
