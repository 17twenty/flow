package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Comparison mode (spec 05 §6): one workflow held up against two trackers'
// capability declarations, so that a team can see what adopting the other one
// would cost before anybody migrates.
//
// It touches no network, which is why it takes declarations rather than
// adapters: there is no credential to probe with and no board to look at on
// the side being compared against. Both sides are therefore judged the same
// way — as if nothing had been set up yet and every permission were held —
// and the answer is about the providers rather than about the state one of
// them happens to be in today.

// ComparisonVersion is the schema the comparison's JSON declares (spec 02 §3).
const ComparisonVersion = "flow.comparison/v1"

// CompareInput is a workflow and the two declarations to judge it against.
type CompareInput struct {
	// Config is the workflow the team asked for.
	Config *config.Config
	// Desired is what that configuration asks of a tracker.
	Desired Desired
	// Left is the tracker .flow.yaml names.
	Left core.CapabilitySet
	// Right is the tracker --tracker-override names.
	Right core.CapabilitySet
	// Now is the instant the comparison is made at.
	Now time.Time
}

// Comparison is what each tracker would make of the same workflow.
type Comparison struct {
	// Left and Right are the adapter names, in the order they are rendered.
	Left, Right string
	// DesiredHash is the state both sides were asked for, which is the same
	// state: the configuration does not change because the tracker does.
	DesiredHash string
	// Rows is one item per thing the workflow asks of a tracker.
	Rows []ComparisonRow
}

// ComparisonRow is one thing the workflow needs, and what each tracker would
// do about it.
type ComparisonRow struct {
	// ID is the desired item's id.
	ID string
	// Capability is the registry name both sides were asked about.
	Capability string
	// Left and Right are the two verdicts.
	Left, Right ComparisonCell
}

// Differs reports whether the two trackers would handle this differently.
func (r ComparisonRow) Differs() bool {
	return r.Left.Outcome != r.Right.Outcome || r.Left.Mechanism != r.Right.Mechanism
}

// ComparisonCell is one tracker's verdict on one item.
type ComparisonCell struct {
	// Outcome is what flow would do about it (spec 05 §4).
	Outcome Outcome
	// Mechanism names what would enforce the intent instead, for a
	// compensated outcome.
	Mechanism string
	// Why is the one line the ladder gave for the outcome.
	Why string
}

// Text is the cell as it appears in the table.
func (c ComparisonCell) Text() string {
	line := c.Outcome.Symbol() + " " + c.Outcome.Label()
	if c.Mechanism != "" {
		line += " → " + c.Mechanism
	}
	return line
}

// Compare plans the same workflow against two trackers' declarations and puts
// the verdicts side by side (spec 05 §6).
func Compare(in CompareInput) (Comparison, error) {
	if in.Config == nil {
		return Comparison{}, fmt.Errorf("compare: no configuration was supplied")
	}
	for _, set := range []core.CapabilitySet{in.Left, in.Right} {
		if set.Kind != core.KindTracker {
			return Comparison{}, fmt.Errorf("compare: %q is a %s adapter, and only trackers can be compared", set.Adapter, set.Kind)
		}
	}

	left, err := in.hypothetical(in.Left)
	if err != nil {
		return Comparison{}, err
	}
	right, err := in.hypothetical(in.Right)
	if err != nil {
		return Comparison{}, err
	}

	comparison := Comparison{
		Left:        in.Left.Adapter,
		Right:       in.Right.Adapter,
		DesiredHash: left.DesiredHash,
	}
	for _, item := range left.Items {
		other, ok := right.Find(item.ID)
		if !ok {
			return Comparison{}, fmt.Errorf("compare: %s is planned against %s but not against %s", item.ID, in.Left.Adapter, in.Right.Adapter)
		}
		comparison.Rows = append(comparison.Rows, ComparisonRow{
			ID:         item.ID,
			Capability: item.Capability,
			Left:       cell(item),
			Right:      cell(other),
		})
	}
	return comparison, nil
}

// hypothetical plans the workflow against one declaration as if nothing had
// been set up and every permission were held.
//
// There are no observations, because looking would need a network and the
// tracker being compared against is not the one the team uses; there is no
// lock, because an attestation is a promise about a particular board. What is
// left is the declaration, which is the thing being compared.
func (in CompareInput) hypothetical(set core.CapabilitySet) (Plan, error) {
	return NewPlan(PlanInput{
		Config:  in.Config,
		Desired: in.Desired,
		Providers: map[string]Provider{TargetTracker: {
			Kind:         set.Adapter,
			Capabilities: set,
			Permissions: core.Permissions{
				Tokens:   set.RequiredTokens(),
				Evidence: []string{"comparison assumes every permission " + set.Adapter + " asks for"},
			},
		}},
		Now:     in.Now,
		Targets: []string{TargetTracker},
	})
}

// cell reduces a planned item to the verdict the table shows.
func cell(item PlanItem) ComparisonCell {
	return ComparisonCell{Outcome: item.Outcome, Mechanism: item.Mechanism, Why: item.Why}
}

// Differences counts the rows the two trackers disagree about.
func (c Comparison) Differences() int {
	n := 0
	for _, row := range c.Rows {
		if row.Differs() {
			n++
		}
	}
	return n
}

// Text renders the comparison as the side-by-side table spec 05 §6 asks for.
// A row the two trackers disagree about is marked, because that is the only
// reason anybody runs this.
func (c Comparison) Text() string {
	const mark = "≠"
	rows := [][3]string{{"item", c.Left, c.Right}}
	for _, row := range c.Rows {
		name := row.ID
		if row.Capability != "" {
			name += " (" + row.Capability + ")"
		}
		rows = append(rows, [3]string{name, row.Left.Text(), row.Right.Text()})
	}

	widths := [3]int{}
	for _, row := range rows {
		for i, column := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(column))
		}
	}

	var out strings.Builder
	for i, row := range rows {
		marker := "  "
		if i > 0 && c.Rows[i-1].Differs() {
			marker = mark + " "
		}
		out.WriteString(marker)
		out.WriteString(pad(row[0], widths[0]))
		out.WriteString("  ")
		out.WriteString(pad(row[1], widths[1]))
		out.WriteString("  ")
		out.WriteString(row[2])
		out.WriteString("\n")
		if i == 0 {
			out.WriteString(strings.Repeat("─", 2+widths[0]+2+widths[1]+2+widths[2]))
			out.WriteString("\n")
		}
	}
	fmt.Fprintf(&out, "\n%s\n", c.Summary())
	return out.String()
}

// Summary says how far apart the two trackers are.
func (c Comparison) Summary() string {
	switch differences := c.Differences(); {
	case len(c.Rows) == 0:
		return "the workflow asks nothing of a tracker"
	case differences == 0:
		return fmt.Sprintf("%s and %s handle all %d items the same way", c.Left, c.Right, len(c.Rows))
	case differences == 1:
		return fmt.Sprintf("1 of %d items is handled differently", len(c.Rows))
	default:
		return fmt.Sprintf("%d of %d items are handled differently", differences, len(c.Rows))
	}
}

// pad right-pads to a width counted in runes, not bytes: the outcome symbols
// are three bytes each and a table padded by length would stagger.
func pad(s string, width int) string {
	return s + strings.Repeat(" ", width-utf8.RuneCountInString(s))
}

// comparisonDoc is the comparison as `--json` publishes it (spec 02 §3). The
// table's marker column is a rendering of `differs`, so the document carries
// the fact rather than the mark.
type comparisonDoc struct {
	Schema      string             `json:"schema"`
	Left        string             `json:"left"`
	Right       string             `json:"right"`
	DesiredHash string             `json:"desired_hash,omitempty"`
	Rows        []comparisonRowDoc `json:"rows"`
	Differences int                `json:"differences"`
	Summary     string             `json:"summary"`
}

type comparisonRowDoc struct {
	ID         string            `json:"id"`
	Capability string            `json:"capability,omitempty"`
	Differs    bool              `json:"differs"`
	Left       comparisonCellDoc `json:"left"`
	Right      comparisonCellDoc `json:"right"`
}

type comparisonCellDoc struct {
	Outcome   Outcome `json:"outcome"`
	Mechanism string  `json:"mechanism,omitempty"`
	Why       string  `json:"why,omitempty"`
}

// JSON renders the comparison as the one document `--json` prints.
func (c Comparison) JSON() ([]byte, error) {
	doc := comparisonDoc{
		Schema:      ComparisonVersion,
		Left:        c.Left,
		Right:       c.Right,
		DesiredHash: c.DesiredHash,
		Rows:        make([]comparisonRowDoc, 0, len(c.Rows)),
		Differences: c.Differences(),
		Summary:     c.Summary(),
	}
	for _, row := range c.Rows {
		doc.Rows = append(doc.Rows, comparisonRowDoc{
			ID:         row.ID,
			Capability: row.Capability,
			Differs:    row.Differs(),
			Left:       comparisonCellDoc{Outcome: row.Left.Outcome, Mechanism: row.Left.Mechanism, Why: row.Left.Why},
			Right:      comparisonCellDoc{Outcome: row.Right.Outcome, Mechanism: row.Right.Mechanism, Why: row.Right.Why},
		})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the comparison: %w", err)
	}
	return append(data, '\n'), nil
}
