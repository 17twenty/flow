package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// docs/delivery-board.md verifies flow against the proposal it was built
// from, and a verification nobody re-runs is a claim rather than a check.
// These tests are what re-run it: the configuration the document prints must
// be one flow accepts, and every key, rule and check it names must exist.

const (
	deliveryBoardDoc = "docs/delivery-board.md"

	// proposalDoc is the proposal itself, kept in the repository so the
	// verification is checked against the thing it verifies.
	proposalDoc = "assets/docs/lean-kanban-workflow.md"

	// overviewSpec is where flow's non-goals live.
	overviewSpec = ".ralph/specs/00-overview.md"
)

// gapIssue is the link a gap carries: the issue that will close it.
var gapIssue = regexp.MustCompile(`/issues/[0-9]+`)

// readDeliveryBoard is the document.
func readDeliveryBoard(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(deliveryBoardDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", deliveryBoardDoc, err)
	}
	return string(raw)
}

// deliveryBoardConfig is the configuration the document prints, which is the
// proposal's delivery board written as `.flow.yaml`.
func deliveryBoardConfig(t *testing.T) *config.Config {
	t.Helper()
	text := readDeliveryBoard(t)
	_, rest, ok := strings.Cut(text, "```yaml\n")
	if !ok {
		t.Fatalf("%s prints no configuration", deliveryBoardDoc)
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("%s leaves its configuration block open", deliveryBoardDoc)
	}
	cfg, err := config.Parse([]byte(block), deliveryBoardDoc)
	if err != nil {
		t.Fatalf("the delivery board does not parse: %v", err)
	}
	return cfg
}

// The delivery board the document prints is a configuration flow accepts —
// parsed strictly and validated against the capability registry, exactly as
// `flow doctor` would. A worked example that does not load is worse than no
// example, because somebody copies it.
func TestTheDeliveryBoardDocumentPrintsAConfigurationFlowAccepts(t *testing.T) {
	cfg := deliveryBoardConfig(t)
	registry, err := core.LoadRegistry(os.DirFS("assets/capabilities"))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if err := config.Validate(cfg, deliveryBoardDoc, registry); err != nil {
		t.Fatalf("the delivery board does not hang together: %v", err)
	}

	// The proposal's §5.2 table, in its own order.
	wantColumns := []string{"Backlog", "Needs Design", "Ready", "In Progress", "In Review", "Done"}
	var gotColumns []string
	for _, column := range cfg.Workflow.Columns {
		gotColumns = append(gotColumns, column.Name)
	}
	if !slices.Equal(gotColumns, wantColumns) {
		t.Errorf("columns = %v, want the proposal's six: %v", gotColumns, wantColumns)
	}

	// The proposal's §6 table: the Git events flow can see, and the
	// deployment that is the last row's second half.
	for event, want := range map[string]string{
		config.EventPROpenedDraft:    "in_progress",
		config.EventPRReadyForReview: "in_review",
		config.EventChangesRequested: "in_progress",
		config.EventDeployed:         "done",
	} {
		transition, ok := cfg.Workflow.Transitions[event]
		if !ok {
			t.Errorf("the delivery board answers no %s event, which §6 gives a row to", event)
			continue
		}
		if transition.To != want {
			t.Errorf("%s moves work to %q, want %q", event, transition.To, want)
		}
	}
	if cfg.Workflow.DoneOn != config.DoneOnDeployed {
		t.Errorf("done_on = %q, want %q: §5.4 says Done means synced to production",
			cfg.Workflow.DoneOn, config.DoneOnDeployed)
	}
	if cfg.DefinitionOfReady.MaxSize <= 0 {
		t.Error("the delivery board sets no max_size, which is §5.3's last rule")
	}
}

// The delivery board document is read for the rules, checks and keys it
// names by TestEveryDocumentNamesRealRulesChecksAndKeys, which reads every
// document flow ships the same way.

// The three rules the proposal's own words rest on are named in the document,
// so that a reader can find what enforces them. A table that lists a
// requirement and points at nothing is the gap this document exists to close.
func TestTheDeliveryBoardDocumentPointsAtWhatEnforcesTheProposal(t *testing.T) {
	doc := readDeliveryBoard(t)
	rules := engine.PolicyRuleIDs()
	for _, rule := range []string{"ticket.ready", "ticket.not_blocked", "agent.human_review"} {
		if !slices.Contains(rules, rule) {
			t.Fatalf("the test itself names %s, which flow does not have", rule)
		}
		if !strings.Contains(doc, rule) {
			t.Errorf("%s does not mention %s, which the proposal asks for", deliveryBoardDoc, rule)
		}
	}
	// What flow does not do is a list of issues, not of paragraphs: a gap
	// with no ticket behind it is a gap nobody will ever close. The list is
	// read rather than enumerated here, because it shortens as flow closes
	// them and a copy of it in this file would be the thing that went stale.
	_, gaps, ok := strings.Cut(doc, "## What the proposal asks for and flow does not do")
	if !ok {
		t.Fatalf("%s no longer says what flow does not do", deliveryBoardDoc)
	}
	bullets := 0
	for _, line := range strings.Split(gaps, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		bullets++
		if !gapIssue.MatchString(line) {
			t.Errorf("%s records a gap with no issue behind it: %q", deliveryBoardDoc, line)
		}
	}
	// An empty list is the good outcome and a broken reading looks exactly
	// like it, so the document has to say in words that it meant to be
	// empty. The heading alone is not enough: it survives a rename of
	// everything under it.
	if bullets == 0 && !strings.Contains(gaps, "Nothing is left on it.") {
		t.Errorf("%s lists no gap and does not say the list is meant to be empty, which is a reading that broke",
			deliveryBoardDoc)
	}
}

// The proposal describes two boards and flow implements one. A reader who
// comes from the proposal arrives looking for the other one, so both places
// that reader will look — the verification and the overview spec — have to
// say whose it is. The columns are read out of the proposal rather than
// listed here: a column the proposal gains and this document never answers
// is exactly the silence the test is for.
func TestTheProductBoardIsAnsweredWhereverSomebodyLooksForIt(t *testing.T) {
	section := productBoardSection(t)
	for _, column := range proposalProductColumns(t) {
		if !strings.Contains(section, column) {
			t.Errorf("%s §4 does not say what becomes of the proposal's %q column",
				deliveryBoardDoc, column)
		}
	}
	// Naming a column is not answering it: every exit rule the proposal
	// states gets a row saying who can answer it, and a board that gains a
	// rule leaves this short by one.
	if got, want := answeredExitRules(section), len(proposalExitRules(t)); got != want {
		t.Errorf("%s §4 answers %d of the proposal's %d exit rules", deliveryBoardDoc, got, want)
	}

	// The one exit rule flow can answer is answered, and the facts it holds
	// for the two it cannot are named. "Out of scope" on its own tells a
	// reader nothing about what they have instead.
	for _, held := range []string{"live.tickets.done_merged", "live.flags.cleanup", "live.flags.age"} {
		if !strings.Contains(section, held) {
			t.Errorf("%s §4 declines the product board without naming %s, which holds a fact the board rests on",
				deliveryBoardDoc, held)
		}
	}

	// And the spec says it is a non-goal, where the non-goals are, because a
	// decision recorded only in a document beside the code is a document.
	overview, err := os.ReadFile(overviewSpec)
	if err != nil {
		t.Fatalf("reading %s: %v", overviewSpec, err)
	}
	_, not, ok := strings.Cut(string(overview), "## 2. What flow is NOT")
	if !ok {
		t.Fatalf("%s no longer says what flow is not", overviewSpec)
	}
	not, _, _ = strings.Cut(not, "\n## ")
	if !strings.Contains(not, "product") {
		t.Errorf("%s does not list the product board among the things flow is not", overviewSpec)
	}
	if !strings.Contains(string(overview), deliveryBoardDoc) {
		t.Errorf("%s declines the product board without saying where the reasoning is", overviewSpec)
	}
}

// productBoardSection is §4 of the verification: everything from its heading
// to the next one.
func productBoardSection(t *testing.T) string {
	t.Helper()
	_, section, ok := strings.Cut(readDeliveryBoard(t), "## §4 ")
	if !ok {
		t.Fatalf("%s has no §4, which is where the product board is answered", deliveryBoardDoc)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// proposalProductColumns is the list of product-board columns flow declines.
func proposalProductColumns(t *testing.T) []string {
	t.Helper()
	var columns []string
	for _, row := range proposalColumnTable(t) {
		columns = append(columns, row[0])
	}
	return columns
}

// proposalExitRules is the subset of those columns the proposal gives an exit
// rule. A column with none of its own — Dropped — holds a decision rather
// than a state anything derives, so there is nobody for §4 to name.
func proposalExitRules(t *testing.T) []string {
	t.Helper()
	var rules []string
	for _, row := range proposalColumnTable(t) {
		if strings.TrimSuffix(row[2], ".") == "None" {
			continue
		}
		rules = append(rules, row[2])
	}
	return rules
}

// proposalColumnTable is the proposal's own §4.2 table, row by row:
// the column, what it means, and the rule for leaving it.
func proposalColumnTable(t *testing.T) [][3]string {
	t.Helper()
	raw, err := os.ReadFile(proposalDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", proposalDoc, err)
	}
	_, table, ok := strings.Cut(string(raw), "### 4.2 Columns")
	if !ok {
		t.Fatalf("%s no longer has a §4.2, so this test cannot read the board it is about", proposalDoc)
	}
	table, _, _ = strings.Cut(table, "\n### ")

	var rows [][3]string
	for _, line := range strings.Split(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 5 {
			continue
		}
		name := strings.TrimSpace(cells[1])
		if name == "" || name == "Column" || strings.HasPrefix(name, "-") {
			continue
		}
		rows = append(rows, [3]string{name, strings.TrimSpace(cells[2]), strings.TrimSpace(cells[3])})
	}
	if len(rows) == 0 {
		t.Fatalf("read no columns out of %s §4.2", proposalDoc)
	}
	return rows
}

// answeredExitRules counts the rows of §4's own table, each of which says who
// can answer one of the proposal's exit rules.
func answeredExitRules(section string) int {
	_, table, ok := strings.Cut(section, "| Proposal §4.2: exit rule | Who can answer it |")
	if !ok {
		return 0
	}
	table, _, _ = strings.Cut(table, "\n\n")
	answered := 0
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, "|") && !strings.HasPrefix(line, "|---") {
			answered++
		}
	}
	return answered
}

// A verification nobody is told about is a file. The README is where a reader
// of this repository finds out it exists.
func TestTheReadmeSaysWhereTheVerificationIs(t *testing.T) {
	content, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	if !strings.Contains(string(content), deliveryBoardDoc) {
		t.Errorf("README.md does not say where %s is", deliveryBoardDoc)
	}
}
