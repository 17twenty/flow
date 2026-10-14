package main

import (
	"os"
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

const deliveryBoardDoc = "docs/delivery-board.md"

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

// Every dotted name in the prose is a policy rule flow publishes, a check it
// runs, or a key it reads. A verification that recommends a key flow has never
// had costs somebody an afternoon, and it rots by itself when flow renames
// something.
func TestTheDeliveryBoardDocumentNamesRealRulesChecksAndKeys(t *testing.T) {
	prose := fence.ReplaceAllString(readDeliveryBoard(t), "")
	schema := readConfigSchema(t)
	rules := engine.PolicyRuleIDs()
	checks := deliveryBoardCheckIDs(t)

	var sawRules, sawChecks, sawKeys int
	for _, match := range dottedName.FindAllStringSubmatch(prose, -1) {
		name := match[1]
		switch {
		case slices.Contains(rules, name):
			sawRules++
		case slices.Contains(checks, name):
			sawChecks++
		case schemaHas(schema, name):
			sawKeys++
		default:
			t.Errorf("%s names %q, which is no rule, no check and no configuration key",
				deliveryBoardDoc, name)
		}
	}
	if sawRules == 0 || sawChecks == 0 || sawKeys == 0 {
		t.Errorf("the document named %d rules, %d checks and %d keys; this test is not testing anything",
			sawRules, sawChecks, sawKeys)
	}
}

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
	// The five things flow does not do are issues, not paragraphs: a gap
	// with no ticket behind it is a gap nobody will ever close.
	for _, issue := range []string{"/issues/143", "/issues/144", "/issues/145", "/issues/146", "/issues/147"} {
		if !strings.Contains(doc, issue) {
			t.Errorf("%s records a gap with no issue behind it: %s is missing", deliveryBoardDoc, issue)
		}
	}
}

// deliveryBoardCheckIDs is every check `flow doctor` would run for the
// document's own configuration.
func deliveryBoardCheckIDs(t *testing.T) []string {
	t.Helper()
	registry, err := engine.DoctorRegistry(engine.SetupInput{
		Config: deliveryBoardConfig(t), ConfigPath: deliveryBoardDoc,
	})
	if err != nil {
		t.Fatalf("DoctorRegistry: %v", err)
	}
	var ids []string
	for _, check := range registry.Checks() {
		ids = append(ids, check.ID)
	}
	return ids
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
