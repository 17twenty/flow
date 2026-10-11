package engine_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// readyConfig is a workflow with the given Definition of Ready rules.
func readyConfig(t *testing.T, rules ...string) *config.Config {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	cfg.DefinitionOfReady.Rules = rules
	cfg.DefinitionOfReady.MaxSize = 0
	return cfg
}

// readyTicket is a ticket that satisfies every rule, which each test then
// breaks in exactly one way.
func readyTicket() core.Ticket {
	return core.Ticket{
		Key:    "#10",
		Title:  "Add the filter bar",
		Body:   "## Acceptance criteria\n\n- the bar filters\n- the filter survives a reload\n",
		Parent: "#1",
	}
}

// judge applies one rule to one ticket.
func judge(t *testing.T, ticket core.Ticket, cfg *config.Config, rule string) engine.ReadyRule {
	t.Helper()
	readiness := engine.Judge(ticket, cfg)
	for _, got := range readiness.Rules {
		if got.Rule == rule {
			return got
		}
	}
	t.Fatalf("no verdict for %s; got %+v", rule, readiness.Rules)
	return engine.ReadyRule{}
}

// TestATicketWithEverythingIsReady is the shape the rules are written
// against, and the one the issue form flow proposes produces.
func TestATicketWithEverythingIsReady(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRAcceptanceCriteria, config.DoRParentLink, config.DoRNotBlocked)
	readiness := engine.Judge(readyTicket(), cfg)

	if !readiness.Ready() {
		t.Fatalf("not ready: %v", readiness.Reasons())
	}
	if len(readiness.Rules) != 3 {
		t.Errorf("rules = %d, want one verdict per configured rule", len(readiness.Rules))
	}
	if !strings.Contains(readiness.Summary(), "meets the Definition of Ready") {
		t.Errorf("summary = %q", readiness.Summary())
	}
}

// TestAcceptanceCriteriaMustBeFilledIn is the rule that does the most work.
// The issue form puts the heading there, so a heading with nothing under it
// means somebody accepted the template and wrote nothing — which is exactly
// the ticket the rule exists to catch.
func TestAcceptanceCriteriaMustBeFilledIn(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRAcceptanceCriteria)
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"filled in", "## Acceptance criteria\n\n- the bar filters\n", true},
		{"any heading level", "# Acceptance criteria\nit filters\n", true},
		{"deepest heading level", "###### Acceptance criteria\nit filters\n", true},
		{"case does not matter", "## ACCEPTANCE CRITERIA\n- it filters\n", true},
		{"a colon does not matter", "## Acceptance Criteria:\n- it filters\n", true},
		{"closed atx heading", "## Acceptance criteria ##\n- it filters\n", true},
		{"prose rather than a list", "## Acceptance criteria\nThe bar filters the table.\n", true},
		{"text after other sections", "## Why\n\nbecause\n\n## Acceptance criteria\n\n- it filters\n", true},
		{"no heading at all", "The bar should filter.\n", false},
		{"heading with nothing under it", "## Acceptance criteria\n\n", false},
		{"heading followed by another heading", "## Acceptance criteria\n\n## Notes\n\nsomething\n", false},
		{"only blank lines under it", "## Acceptance criteria\n\n   \n\t\n", false},
		{"a hashtag is not a heading", "#acceptance criteria\n- it filters\n", false},
		{"seven hashes is not a heading", "####### Acceptance criteria\n- it filters\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ticket := readyTicket()
			ticket.Body = tc.body
			if got := judge(t, ticket, cfg, config.DoRAcceptanceCriteria); got.OK != tc.want {
				t.Errorf("ok = %v, want %v (why: %s) for body:\n%s", got.OK, tc.want, got.Why, tc.body)
			}
		})
	}
}

// TestAnEmptyAcceptanceCriteriaSaysWhichProblemItIs: "you have no heading" and
// "you left the heading empty" are different mistakes and the person fixing
// the ticket needs to know which they made.
func TestAnEmptyAcceptanceCriteriaSaysWhichProblemItIs(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRAcceptanceCriteria)
	missing := readyTicket()
	missing.Body = "just a sentence"
	if why := judge(t, missing, cfg, config.DoRAcceptanceCriteria).Why; !strings.Contains(why, "no \"Acceptance criteria\" heading") {
		t.Errorf("why = %q, want it to say the heading is absent", why)
	}

	empty := readyTicket()
	empty.Body = "## Acceptance criteria\n\n"
	if why := judge(t, empty, cfg, config.DoRAcceptanceCriteria).Why; !strings.Contains(why, "is empty") {
		t.Errorf("why = %q, want it to say the heading is unfilled", why)
	}
}

// TestAParentLinkIsRequired: a board of forty items should still be a story
// about a handful of outcomes.
func TestAParentLinkIsRequired(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRParentLink)
	orphan := readyTicket()
	orphan.Parent = ""

	got := judge(t, orphan, cfg, config.DoRParentLink)
	if got.OK {
		t.Fatal("a ticket with no parent passed parent_link")
	}
	if !strings.Contains(got.Why, "no parent") {
		t.Errorf("why = %q", got.Why)
	}
	if !judge(t, readyTicket(), cfg, config.DoRParentLink).OK {
		t.Error("a ticket with a parent failed parent_link")
	}
}

// TestBlockedWorkIsNotReady, however it is marked. Trackers differ: some have
// a flag, some only have a label, and the workflow names which label.
func TestBlockedWorkIsNotReady(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRNotBlocked)

	flagged := readyTicket()
	flagged.Blocked = true
	if got := judge(t, flagged, cfg, config.DoRNotBlocked); got.OK {
		t.Error("a ticket the tracker flags as blocked passed not_blocked")
	}

	labelled := readyTicket()
	labelled.Labels = []string{"blocked"}
	got := judge(t, labelled, cfg, config.DoRNotBlocked)
	if got.OK {
		t.Error("a ticket labelled blocked passed not_blocked")
	}
	if !strings.Contains(got.Why, "blocked") {
		t.Errorf("why = %q", got.Why)
	}

	if !judge(t, readyTicket(), cfg, config.DoRNotBlocked).OK {
		t.Error("an unblocked ticket failed not_blocked")
	}
}

// TestTheBlockedLabelIsTheWorkflowsOwn: a team that calls it "on-hold" is
// still describing blocked work.
func TestTheBlockedLabelIsTheWorkflowsOwn(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRNotBlocked)
	cfg.Workflow.Blocked.Label = "on-hold"

	ticket := readyTicket()
	ticket.Labels = []string{"on-hold"}
	if judge(t, ticket, cfg, config.DoRNotBlocked).OK {
		t.Error("the workflow's own blocked label was not read")
	}

	stale := readyTicket()
	stale.Labels = []string{"blocked"}
	if !judge(t, stale, cfg, config.DoRNotBlocked).OK {
		t.Error("a label the workflow does not use blocked a ticket")
	}
}

// TestUserFacingWorkNeedsAFlagDecidedUpFront: trunk-based delivery means the
// half-finished thing is already in production. The flag is what makes that
// safe, and deciding on it afterwards is deciding too late.
func TestUserFacingWorkNeedsAFlagDecidedUpFront(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRFlagIfUserFacing)
	for _, tc := range []struct {
		name   string
		labels []string
		want   bool
	}{
		{"not user-facing, no flag needed", []string{"chore"}, true},
		{"user-facing with a flag", []string{"user-facing", "flag:filter-bar"}, true},
		{"the flag label is matched without regard to case", []string{"user-facing", "Flag:FilterBar"}, true},
		{"user-facing without a flag", []string{"user-facing"}, false},
		{"a flag with no name is not a flag", []string{"user-facing", "flag:"}, false},
		{"a flag without the user-facing label is nobody's business", []string{"flag:x"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ticket := readyTicket()
			ticket.Labels = tc.labels
			if got := judge(t, ticket, cfg, config.DoRFlagIfUserFacing); got.OK != tc.want {
				t.Errorf("ok = %v, want %v (why: %s)", got.OK, tc.want, got.Why)
			}
		})
	}
}

// TestUIWorkNeedsADesignLink so that the first time anybody sees the design
// is not in code review.
func TestUIWorkNeedsADesignLink(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRDesignIfUI)
	base := "## Acceptance criteria\n\n- it filters\n\n"
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"a link under Design", base + "## Design\n\nhttps://figma.test/file/1\n", true},
		{"a markdown link under Design", base + "## Design\n\n[the mock](https://figma.test/file/1)\n", true},
		{"a reference link under Design", base + "## Design\n\n[the mock][1]\n", true},
		{"no Design heading", base, false},
		{"a Design heading with no link", base + "## Design\n\nAsk Priya.\n", false},
		{"an empty Design heading", base + "## Design\n\n", false},
		{"a link elsewhere is not a design", base + "https://figma.test/file/1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ticket := readyTicket()
			ticket.Labels = []string{"ui"}
			ticket.Body = tc.body
			if got := judge(t, ticket, cfg, config.DoRDesignIfUI); got.OK != tc.want {
				t.Errorf("ok = %v, want %v (why: %s) for body:\n%s", got.OK, tc.want, got.Why, tc.body)
			}
		})
	}
}

// TestARuleWhoseTriggerIsAbsentIsSkippedNotFailed: flow does not hold work up
// for a question nobody asked. A ticket that is not user-facing has no
// opinion about feature flags.
func TestARuleWhoseTriggerIsAbsentIsSkippedNotFailed(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRFlagIfUserFacing, config.DoRDesignIfUI)
	readiness := engine.Judge(readyTicket(), cfg)

	if !readiness.Ready() {
		t.Fatalf("not ready: %v", readiness.Reasons())
	}
	for _, rule := range readiness.Rules {
		if !rule.Skipped {
			t.Errorf("%s was applied to a ticket that does not trigger it", rule.Rule)
		}
		if !rule.OK {
			t.Errorf("%s: a skipped rule must be satisfied", rule.Rule)
		}
	}
}

// TestMaxSizeBoundsAnEstimate, and is on exactly when the configuration gives
// a limit: spec 04 §7 lists it as a rule, but it is written as its own key
// with a duration rather than a name in the rules list.
func TestMaxSizeBoundsAnEstimate(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRParentLink)
	cfg.DefinitionOfReady.MaxSize = config.Duration(2 * 24 * time.Hour)

	big := readyTicket()
	big.Size = 5 * 24 * time.Hour
	got := judge(t, big, cfg, "max_size")
	if got.OK {
		t.Fatal("a five-day ticket passed a two-day limit")
	}
	if !strings.Contains(got.Why, "5d") || !strings.Contains(got.Why, "2d") {
		t.Errorf("why = %q, want both the estimate and the limit", got.Why)
	}
	if !strings.Contains(got.Why, "split it") {
		t.Errorf("why = %q, want it to say what to do", got.Why)
	}

	small := readyTicket()
	small.Size = 24 * time.Hour
	if !judge(t, small, cfg, "max_size").OK {
		t.Error("a one-day ticket failed a two-day limit")
	}
}

// TestATrackerWithNoEstimateFieldDoesNotBlockWork: flow will not refuse a
// ticket for failing to carry a number its tracker has nowhere to put.
func TestATrackerWithNoEstimateFieldDoesNotBlockWork(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRParentLink)
	cfg.DefinitionOfReady.MaxSize = config.Duration(2 * 24 * time.Hour)

	got := judge(t, readyTicket(), cfg, "max_size")
	if !got.OK || !got.Skipped {
		t.Errorf("ok = %v skipped = %v, want a rule that cannot apply to be skipped", got.OK, got.Skipped)
	}
}

// TestNoMaxSizeMeansNoSuchRule: zero disables it, and a disabled rule does
// not appear in the verdict at all.
func TestNoMaxSizeMeansNoSuchRule(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRParentLink)
	big := readyTicket()
	big.Size = 500 * 24 * time.Hour

	readiness := engine.Judge(big, cfg)
	for _, rule := range readiness.Rules {
		if rule.Rule == "max_size" {
			t.Fatalf("max_size was judged with no limit configured: %+v", rule)
		}
	}
	if !readiness.Ready() {
		t.Errorf("not ready: %v", readiness.Reasons())
	}
}

// TestNoRulesMeansNoDefinitionOfReady: a team that has not agreed what ready
// means should not have flow inventing one for them.
func TestNoRulesMeansNoDefinitionOfReady(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t)
	readiness := engine.Judge(core.Ticket{Key: "#10"}, cfg)

	if !readiness.Ready() {
		t.Errorf("an empty Definition of Ready refused a ticket: %v", readiness.Reasons())
	}
	if len(readiness.Rules) != 0 {
		t.Errorf("rules = %+v, want none", readiness.Rules)
	}
}

// TestRulesAreJudgedInTheOrderTheyAreWritten, so that the reasons a person
// reads come out in the order their own file put them.
func TestRulesAreJudgedInTheOrderTheyAreWritten(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, config.DoRNotBlocked, config.DoRAcceptanceCriteria, config.DoRParentLink)
	bad := core.Ticket{Key: "#10", Blocked: true}

	readiness := engine.Judge(bad, cfg)
	var order []string
	for _, rule := range readiness.Rules {
		order = append(order, rule.Rule)
	}
	want := []string{config.DoRNotBlocked, config.DoRAcceptanceCriteria, config.DoRParentLink}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if len(readiness.Missing()) != 3 {
		t.Errorf("missing = %d, want all three", len(readiness.Missing()))
	}
	if !strings.Contains(readiness.Summary(), "#10 is not ready") {
		t.Errorf("summary = %q", readiness.Summary())
	}
}

// TestARuleThisBuildDoesNotKnowFailsClosed: silently ignoring a rule the team
// wrote down would let work through a gate they believe is closed. An old
// binary against a newer configuration must say so rather than wave it past.
func TestARuleThisBuildDoesNotKnowFailsClosed(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, "telepathy_check")
	readiness := engine.Judge(readyTicket(), cfg)

	if readiness.Ready() {
		t.Fatal("a rule flow does not understand was treated as satisfied")
	}
	if why := readiness.Reasons()[0]; !strings.Contains(why, "does not know the rule") {
		t.Errorf("why = %q, want it to name the problem as flow's own", why)
	}
}

// TestJudgingIsPure: the same ticket gets the same answer on a laptop, in CI
// and in doctor's live audit. A rule that could disagree with itself between
// those three is worse than no rule.
func TestJudgingIsPure(t *testing.T) {
	t.Parallel()

	cfg := readyConfig(t, engine.ReadyRuleIDs()...)
	cfg.DefinitionOfReady.MaxSize = config.Duration(2 * 24 * time.Hour)
	ticket := readyTicket()
	ticket.Labels = []string{"ui", "user-facing"}
	ticket.Size = 5 * 24 * time.Hour

	first := engine.Judge(ticket, cfg)
	for range 5 {
		again := engine.Judge(ticket, cfg)
		if !slices.Equal(first.Reasons(), again.Reasons()) {
			t.Fatalf("judging twice gave %v then %v", first.Reasons(), again.Reasons())
		}
	}
	if first.Ready() {
		t.Fatal("this ticket should fail three rules")
	}
	if got := len(first.Missing()); got != 3 {
		t.Errorf("missing = %d, want flag, design and size; got %v", got, first.Reasons())
	}
}

// TestEveryRuleTheConfigurationAllowsIsImplemented pins the two lists
// together: a rule the schema accepts but the engine does not know would fail
// closed on every ticket, which is a configuration nobody can use.
func TestEveryRuleTheConfigurationAllowsIsImplemented(t *testing.T) {
	t.Parallel()

	known := engine.ReadyRuleIDs()
	for _, id := range []string{
		config.DoRAcceptanceCriteria, config.DoRParentLink, config.DoRNotBlocked,
		config.DoRFlagIfUserFacing, config.DoRDesignIfUI,
	} {
		if !slices.Contains(known, id) {
			t.Errorf("the configuration allows %q but the engine does not implement it", id)
		}
		cfg := readyConfig(t, id)
		readiness := engine.Judge(readyTicket(), cfg)
		if len(readiness.Rules) != 1 {
			t.Fatalf("%s: rules = %+v, want one verdict", id, readiness.Rules)
		}
		if strings.Contains(readiness.Rules[0].Why, "does not know the rule") {
			t.Errorf("%s is allowed by the configuration but unknown to the engine", id)
		}
	}
}

// TestTheLabelsTheRulesReadAreOnesFlowCreates: a team cannot start labelling
// work `user-facing` before the label exists, so the rule would be
// unsatisfiable on a board flow had just set up.
func TestTheLabelsTheRulesReadAreOnesFlowCreates(t *testing.T) {
	t.Parallel()

	for _, label := range []string{engine.LabelUI, engine.LabelUserFacing} {
		if !slices.Contains(engine.ReadyLabels, label) {
			t.Errorf("the rules read %q but flow does not create it", label)
		}
	}
}
