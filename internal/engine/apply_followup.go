package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Follow-ups for an apply (spec 06 §3). A plan item flow could not make true
// is work somebody owes, and spec 07 §4's lifecycle is the one place that
// knows how to ask for it: file it once, chase it when it goes stale, close
// it when flow sees the work done.
//
// Apply therefore files through that lifecycle rather than beside it, and
// uses the plan item's own id as the check id — which is also the doctor
// check id and the attestation id (spec 07 §5). The three are deliberately
// the same string: an apply that files a ticket about `forge.protection` and
// a doctor run that would file one about the same thing must find each
// other's ticket rather than open a second.

// ApplyFollowUpInput is an apply, and the tracker to account for it on.
type ApplyFollowUpInput struct {
	// Applied is what became of the plan.
	Applied Applied
	// Plan is what was decided, which is where an item's owner and its
	// changes are written down.
	Plan Plan
	// Config is the workflow, which says what to label a follow-up and who
	// to give it to.
	Config *config.Config
	// Lock is what flow filed before, and where this round is recorded.
	Lock *config.Lock
	// Tracker is where the tickets live. Nil means there is nowhere to file,
	// and nothing is attempted.
	Tracker core.Tracker
	// Repo is the repository the follow-ups are about.
	Repo string
	// Clock is where "a week ago" comes from.
	Clock core.Clock
	// File permits flow to write to the tracker. Without it the lifecycle
	// reports what it would have done (spec 02 §2 `--no-followups`).
	File bool
	// Log receives one line per mutating call.
	Log *slog.Logger
}

// ApplyFollowUps reconciles one follow-up per applied item: filed for the
// work a provider would not do, closed for the work that is now done.
//
// Every item is offered to the lifecycle, not only the ones that owe
// something. An item that has just been made true is how flow learns that an
// old follow-up can be verified and closed, and skipping it would leave the
// ticket open for ever.
func ApplyFollowUps(ctx context.Context, in ApplyFollowUpInput) []FollowUpOutcome {
	if in.Tracker == nil || in.Lock == nil || in.Config == nil {
		return nil
	}
	var outcomes []FollowUpOutcome
	for _, item := range in.Applied.Items {
		planned, _ := in.Plan.Find(item.ID)
		outcome := ReconcileFollowUp(ctx, FollowUpInput{
			Check:   applyCheck(planned, item),
			Result:  applyResult(planned, item),
			Config:  in.Config,
			Lock:    in.Lock,
			Tracker: in.Tracker,
			Repo:    in.Repo,
			Clock:   in.Clock,
			Fix:     in.File,
			Log:     in.Log,
		})
		if outcome.Action == FollowUpNone && outcome.Err == nil {
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// applyCheck is the item as the follow-up lifecycle sees it: the same id the
// doctor check and the attestation use, so one ticket serves all three.
func applyCheck(planned PlanItem, item AppliedItem) Check {
	return Check{
		ID:       item.ID,
		Title:    "set up " + item.ID,
		Category: categoryOf(planned.Target),
	}
}

// categoryOf maps a plan target onto the doctor category a follow-up is
// reported under (spec 07 §1).
func categoryOf(target string) string {
	switch target {
	case TargetForge:
		return CategoryForge
	case TargetTracker:
		return CategoryTracker
	case TargetDeploys:
		return CategoryDeploys
	default:
		return CategoryMeta
	}
}

// applyResult turns what became of an item into the finding the lifecycle
// reconciles against: a failure with a follow-up remedy when work is still
// owed, and a pass when there is none.
//
// Only outstanding work asks for a ticket. A change flow made, and a change
// it refused to make because nobody passed --prune, are both settled: the
// first is done and the second is flow declining to do something destructive
// that the team can ask for at any time.
func applyResult(planned PlanItem, item AppliedItem) Result {
	owed := outstandingChanges(item)
	if len(owed) == 0 && item.Err == nil {
		return Result{State: StatePass, Message: item.ID + " is as the configuration asks"}
	}
	result := Result{
		State:   StateFail,
		Message: applyMessage(planned, item),
		Remediation: Remediation{
			Kind:    RemedyFollowUp,
			Summary: "make " + item.ID + " true by hand",
			Owner:   planned.Owner,
			Steps:   owed,
		},
		ObservedHash: planned.ObservedHash,
	}
	if len(planned.Missing) > 0 {
		result.Remediation.Summary += ", or grant " + strings.Join(planned.Missing, ", ")
	}
	return result
}

// outstandingChanges are the changes a human still owes, as the sentences the
// ticket lists.
func outstandingChanges(item AppliedItem) []string {
	var steps []string
	for _, change := range item.Changes {
		if change.Done() || change.Refused != "" {
			continue
		}
		step := fmt.Sprintf("%s %q", change.Change.Op, change.Change.Item.Name)
		if reason := change.Reason(); reason != "" {
			step += " — " + reason
		}
		steps = append(steps, step)
	}
	return steps
}

// applyMessage is why the ticket exists, in one line.
func applyMessage(planned PlanItem, item AppliedItem) string {
	switch {
	case item.Err != nil:
		return fmt.Sprintf("flow could not set up %s: %s", item.ID, item.Err)
	case planned.Why != "":
		return planned.Why
	default:
		return "flow cannot make " + item.ID + " true itself"
	}
}
