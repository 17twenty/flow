package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// Rendering (spec 05 §5). The words are the engine's: which glyph an outcome
// gets, what it is called, and what follows the arrow are all properties of
// the decision, and the terminal, the markdown report and the CI annotation
// must not be able to disagree about them. How they are laid out and coloured
// is the UI's (spec 12).

// Symbol is the glyph that stands for the outcome (spec 05 §5).
func (o Outcome) Symbol() string {
	switch o {
	case OutcomeNative:
		return "✓"
	case OutcomeCompensated:
		return "↻"
	case OutcomeFollowUp:
		return "⚠"
	case OutcomeAttest:
		return "✎"
	case OutcomeUnsupported:
		return "✗"
	default:
		return "·"
	}
}

// Label is the outcome in the words a person reads, which are not always the
// words the JSON uses: a plan says "follow-up", a machine says "follow_up".
func (o Outcome) Label() string {
	switch o {
	case OutcomeNoop:
		return "up to date"
	case OutcomeFollowUp:
		return "follow-up"
	default:
		return string(o)
	}
}

// Detail is what follows the arrow in a rendered line: the mechanism that
// compensates, the person a follow-up went to, the command that attests. An
// outcome that needs no arrow returns the empty string.
func (i PlanItem) Detail() string {
	switch i.Outcome {
	case OutcomeCompensated:
		return i.Mechanism
	case OutcomeFollowUp:
		return i.Owner
	case OutcomeAttest:
		return "flow attest " + i.ID
	default:
		return ""
	}
}

// Headline is the one line that stands for a planned item: what it is, what
// flow will do about it, and who or what does it.
func (i PlanItem) Headline() string {
	var line strings.Builder
	line.WriteString(i.Outcome.Symbol())
	line.WriteString(" ")
	line.WriteString(i.Outcome.Label())
	if detail := i.Detail(); detail != "" {
		line.WriteString(" → ")
		line.WriteString(detail)
	}
	line.WriteString("  ")
	line.WriteString(i.ID)
	if i.Capability != "" {
		line.WriteString(" (")
		line.WriteString(i.Capability)
		line.WriteString(")")
	}
	return line.String()
}

// Text renders the plan in plain text: the providers it was made against, one
// headline per item with its changes and hazards beneath, and a count.
//
// It is what `flow plan` prints where there is no terminal to be clever in,
// and what the UI's styled and markdown renderings are arrangements of.
func (p Plan) Text() string {
	var out strings.Builder
	out.WriteString(p.Schema)
	out.WriteString("\n")
	for _, target := range []string{TargetForge, TargetTracker, TargetDeploys} {
		if kind := p.Providers[target]; kind != "" {
			fmt.Fprintf(&out, "%-8s %s\n", target, kind)
		}
	}
	for _, item := range p.Items {
		out.WriteString("\n")
		out.WriteString(item.Headline())
		out.WriteString("\n")
		out.WriteString("    ")
		out.WriteString(item.Why)
		out.WriteString("\n")
		for _, change := range item.Changes {
			out.WriteString("    ")
			out.WriteString(change.Op)
			out.WriteString(" ")
			out.WriteString(strconv.Quote(change.Item.Name))
			for _, field := range change.Fields() {
				fmt.Fprintf(&out, " %s=%s", field, strconv.Quote(change.Item.Fields[field]))
			}
			out.WriteString("\n")
		}
		for _, hazard := range item.Hazards {
			out.WriteString("    hazard: ")
			out.WriteString(hazard)
			out.WriteString("\n")
		}
	}
	out.WriteString("\n")
	out.WriteString(p.Summary())
	out.WriteString("\n")
	return out.String()
}

// Summary is the one line that says how much there is to do.
func (p Plan) Summary() string {
	if len(p.Items) == 0 {
		return "nothing to plan"
	}
	parts := []string{fmt.Sprintf("%d items", len(p.Items))}
	if len(p.Items) == 1 {
		parts[0] = "1 item"
	}
	for _, outcome := range Outcomes {
		if count := p.Count(outcome); count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, outcome.Label()))
		}
	}
	return strings.Join(parts, " · ")
}
