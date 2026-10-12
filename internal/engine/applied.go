package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Rendering an apply (spec 02 §2–3, spec 06 §3). As with a plan, the words
// belong to the engine and the arrangement to the UI: what became of a change
// is a property of the apply, not of the terminal it is printed in.

// ApplyVersion is the schema the apply's JSON declares (spec 02 §3).
const ApplyVersion = "flow.apply/v1"

// Verb says what became of one change, in one word.
func (c AppliedChange) Verb() string {
	switch {
	case c.Err != nil:
		return "failed"
	case c.FollowUp != "":
		return "follow-up"
	case c.Refused != "":
		return "refused"
	default:
		return "done"
	}
}

// Reason is why, in one line: the provider's own words when it made the change,
// and the reason when it did not.
func (c AppliedChange) Reason() string {
	switch {
	case c.Err != nil:
		return c.Err.Error()
	case c.FollowUp != "":
		return c.FollowUp
	case c.Refused != "":
		return c.Refused
	default:
		return c.Note
	}
}

// Headline is the one line that stands for an applied item: what flow did,
// and to what.
func (i AppliedItem) Headline() string {
	var line strings.Builder
	switch {
	case i.Err != nil:
		line.WriteString("✗ failed")
	case i.Skipped != "":
		line.WriteString("· skipped")
	default:
		line.WriteString(i.Outcome.Symbol())
		line.WriteString(" ")
		line.WriteString(i.Outcome.Label())
	}
	line.WriteString("  ")
	line.WriteString(i.ID)
	return line.String()
}

// Why is the item's own explanation: the failure, what stopped it, or why
// nothing was attempted. An item that simply worked has none.
func (i AppliedItem) Why() string {
	switch {
	case i.Err != nil && i.BlockedBy != "":
		return fmt.Sprintf("blocked by %s: %s", i.BlockedBy, i.Err)
	case i.Err != nil:
		return i.Err.Error()
	case i.Skipped != "":
		return i.Skipped
	default:
		return ""
	}
}

// Text renders what apply did in plain text: one headline per item with each
// change beneath it, and a count.
func (a Applied) Text() string {
	var out strings.Builder
	out.WriteString(ApplyVersion)
	out.WriteString("\n")
	for _, item := range a.Items {
		out.WriteString("\n")
		out.WriteString(item.Headline())
		out.WriteString("\n")
		if why := item.Why(); why != "" {
			out.WriteString("    ")
			out.WriteString(why)
			out.WriteString("\n")
		}
		for _, change := range item.Changes {
			fmt.Fprintf(&out, "    %-9s %s %q", change.Verb(), change.Change.Op, change.Change.Item.Name)
			if reason := change.Reason(); reason != "" {
				out.WriteString(" — ")
				out.WriteString(reason)
			}
			out.WriteString("\n")
		}
	}
	for _, followUp := range a.FollowUps {
		out.WriteString("\n")
		out.WriteString(followUp.Headline())
		out.WriteString("\n")
	}
	out.WriteString("\n")
	out.WriteString(a.Summary())
	out.WriteString("\n")
	return out.String()
}

// Headline is the one line that stands for a follow-up flow filed, nudged or
// closed on the way through (spec 07 §4).
func (o FollowUpOutcome) Headline() string {
	line := "⚠ follow-up " + o.Action + "  " + o.Check
	if o.Would {
		line = "⚠ would " + o.Action + " follow-up  " + o.Check
	}
	switch {
	case o.Err != nil:
		return "✗ follow-up  " + o.Check + "    " + o.Err.Error()
	case o.Ticket.Key != "":
		return line + " (" + o.Ticket.Key + ")"
	default:
		return line
	}
}

// Summary says how much landed and how much is still owed.
func (a Applied) Summary() string {
	if len(a.Items) == 0 {
		return "nothing to apply"
	}
	done, skipped, failed := a.Counts()
	parts := []string{fmt.Sprintf("%d items", len(a.Items))}
	if len(a.Items) == 1 {
		parts[0] = "1 item"
	}
	for _, part := range []struct {
		count int
		label string
	}{
		{done, "applied"},
		{skipped, "skipped"},
		{failed, "failed"},
		{a.Outstanding(), "outstanding"},
	} {
		if part.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.count, part.label))
		}
	}
	return strings.Join(parts, " · ")
}

// Outstanding counts the changes a human still owes: every one the provider
// handed back and every one flow refused to make.
func (a Applied) Outstanding() int {
	count := 0
	for _, item := range a.Items {
		for _, change := range item.Changes {
			if change.FollowUp != "" || change.Refused != "" {
				count++
			}
		}
	}
	return count
}

// ExitCode is what `flow apply` exits with (spec 02 §2): 0 when the providers
// converged, 2 when follow-ups or inputs remain, 1 when something failed.
func (a Applied) ExitCode() int {
	outstanding := a.Outstanding()
	for _, item := range a.Items {
		if item.Err != nil {
			return 1
		}
		switch item.Outcome {
		case OutcomeUnsupported:
			return 1
		case OutcomeNoop, OutcomeNative:
		default:
			outstanding++
		}
	}
	// A follow-up flow could not file is a failure of the apply. The work is
	// outstanding either way, and now nobody has been told about it. What
	// the lifecycle did when it worked adds nothing: the work it accounts
	// for is already counted above, and a ticket flow closed because the
	// setting is now true is not something anybody still owes.
	for _, followUp := range a.FollowUps {
		if followUp.Err != nil {
			return 1
		}
	}
	if outstanding > 0 {
		return 2
	}
	return 0
}

// appliedDoc is the apply as `--json` publishes it. The in-memory types carry
// errors and the document carries the sentences they rendered to, which is
// what a consumer can actually read.
type appliedDoc struct {
	Schema      string           `json:"schema"`
	FlowVersion string           `json:"flow_version,omitempty"`
	AppliedAt   time.Time        `json:"applied_at"`
	Items       []appliedItemDoc `json:"items"`
	FollowUps   []followUpDoc    `json:"follow_ups,omitempty"`
	Summary     string           `json:"summary"`
	Outstanding int              `json:"outstanding"`
}

type appliedItemDoc struct {
	ID        string             `json:"id"`
	Outcome   Outcome            `json:"outcome"`
	Changes   []appliedChangeDoc `json:"changes,omitempty"`
	Skipped   string             `json:"skipped,omitempty"`
	BlockedBy string             `json:"blocked_by,omitempty"`
	Error     string             `json:"error,omitempty"`
}

type appliedChangeDoc struct {
	What     string `json:"what"`
	Op       string `json:"op"`
	Item     string `json:"item"`
	Verb     string `json:"verb"`
	Note     string `json:"note,omitempty"`
	Refused  string `json:"refused,omitempty"`
	FollowUp string `json:"follow_up,omitempty"`
	Error    string `json:"error,omitempty"`
}

type followUpDoc struct {
	Check  string `json:"check"`
	Action string `json:"action"`
	Would  bool   `json:"would,omitempty"`
	Ticket string `json:"ticket,omitempty"`
	URL    string `json:"url,omitempty"`
	Error  string `json:"error,omitempty"`
}

// JSON renders the apply as the one document `--json` prints (spec 02 §3).
func (a Applied) JSON() ([]byte, error) {
	doc := appliedDoc{
		Schema:      ApplyVersion,
		FlowVersion: a.FlowVersion,
		AppliedAt:   a.AppliedAt,
		Items:       make([]appliedItemDoc, 0, len(a.Items)),
		Summary:     a.Summary(),
		Outstanding: a.Outstanding(),
	}
	for _, item := range a.Items {
		entry := appliedItemDoc{
			ID:        item.ID,
			Outcome:   item.Outcome,
			Skipped:   item.Skipped,
			BlockedBy: item.BlockedBy,
			Error:     errorText(item.Err),
		}
		for _, change := range item.Changes {
			entry.Changes = append(entry.Changes, appliedChangeDoc{
				What:     change.Change.What,
				Op:       change.Change.Op,
				Item:     change.Change.Item.Name,
				Verb:     change.Verb(),
				Note:     change.Note,
				Refused:  change.Refused,
				FollowUp: change.FollowUp,
				Error:    errorText(change.Err),
			})
		}
		doc.Items = append(doc.Items, entry)
	}
	for _, followUp := range a.FollowUps {
		doc.FollowUps = append(doc.FollowUps, followUpDoc{
			Check:  followUp.Check,
			Action: followUp.Action,
			Would:  followUp.Would,
			Ticket: followUp.Ticket.Key,
			URL:    followUp.Ticket.URL,
			Error:  errorText(followUp.Err),
		})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the apply: %w", err)
	}
	return append(data, '\n'), nil
}

// errorText renders an error for a document, empty when there is none.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
