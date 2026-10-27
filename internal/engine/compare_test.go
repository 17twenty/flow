package engine_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// comparison compares the lean-kanban workflow across two tracker
// declarations.
func comparison(t *testing.T, left, right string) engine.Comparison {
	t.Helper()
	return comparisonOf(t, preset(t, config.PresetLeanKanban), left, right)
}

// comparisonOf compares two providers under a configuration of the caller's.
func comparisonOf(t *testing.T, cfg *config.Config, left, right string) engine.Comparison {
	t.Helper()
	registry := testRegistry(t)
	result, err := engine.Compare(engine.CompareInput{
		Config:  cfg,
		Desired: desired(t, cfg),
		Left:    registry[left],
		Right:   registry[right],
		Now:     coretest.ContractNow,
	})
	if err != nil {
		t.Fatalf("compare %s against %s: %v", left, right, err)
	}
	return result
}

// TestCompareGolden pins the side-by-side table a team reads before it moves
// its board from one tracker to another (spec 05 §6).
//
// Run go test ./internal/engine -run TestCompareGolden -update to rewrite it.
func TestCompareGolden(t *testing.T) {
	for _, tc := range []struct{ left, right string }{
		{left: "github-projects", right: "jira-cloud"},
		{left: "github-projects", right: "linear"},
	} {
		name := tc.left + "-vs-" + tc.right
		t.Run(name, func(t *testing.T) {
			goldenFile(t, filepath.Join("compare", name+".txt"), []byte(comparison(t, tc.left, tc.right).Text()))
		})
	}
}

// TestCompareIsAboutTheTracker proves the comparison asks only what the
// workflow needs from a tracker. The forge and the deployment records do not
// change because the board moved, and listing them would pad the table with
// rows that are identical by construction.
func TestCompareIsAboutTheTracker(t *testing.T) {
	result := comparison(t, "github-projects", "jira-cloud")
	if len(result.Rows) == 0 {
		t.Fatal("the comparison is empty")
	}
	for _, row := range result.Rows {
		if !strings.HasPrefix(row.ID, engine.TargetTracker+".") {
			t.Errorf("%s is not a tracker item, which a tracker comparison should not hold", row.ID)
		}
	}
}

// TestCompareJudgesBothSidesTheSameWay proves a tracker is not flattered by
// the state its board happens to be in: both sides are planned as if nothing
// were set up and every permission were held, so the verdict is about the
// provider.
func TestCompareJudgesBothSidesTheSameWay(t *testing.T) {
	result := comparison(t, "github-projects", "github-projects")
	for _, row := range result.Rows {
		if row.Differs() {
			t.Errorf("%s differs from itself: %s then %s", row.ID, row.Left.Text(), row.Right.Text())
		}
	}
	if got := result.Differences(); got != 0 {
		t.Errorf("a tracker differs from itself in %d items", got)
	}
}

// TestCompareFindsWhatWouldChange states what moving this workflow to another
// tracker would actually cost, which is the question the mode exists to
// answer.
func TestCompareFindsWhatWouldChange(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		id          string
		want        [2]engine.Outcome
		mechanism   string
		sameVerdict bool
		native      bool
	}{
		{
			// Nothing can be asked of Jira about an issue closing, where
			// GitHub Projects has the automation and flow simply cannot see
			// whether it is on — for a team that leaves it to the tracker.
			left: "github-projects", right: "jira-cloud",
			id:     core.NativeItem("issue_closed"),
			want:   [2]engine.Outcome{engine.OutcomeAttest, engine.OutcomeUnsupported},
			native: true,
		},
		{
			// Linear has no saved view to expedite with, so the gate enforces
			// it instead.
			left: "github-projects", right: "linear",
			id:        core.ItemTrackerExpedite,
			want:      [2]engine.Outcome{engine.OutcomeAttest, engine.OutcomeCompensated},
			mechanism: core.CompensatePolicyExpedite,
		},
		{
			// Both can be told their columns. Linear's needs a permission
			// the credential in TestPlanOutcomes did not hold, and that is
			// not what a comparison is about: the question is which tracker
			// can do this, not whose token is in the environment today.
			left: "github-projects", right: "linear",
			id:          core.ItemTrackerStatusOptions,
			want:        [2]engine.Outcome{engine.OutcomeNative, engine.OutcomeNative},
			sameVerdict: true,
		},
	} {
		t.Run(tc.id+"/"+tc.right, func(t *testing.T) {
			cfg := preset(t, config.PresetLeanKanban)
			if tc.native {
				cfg = nativeMerges(cfg)
			}
			row, ok := find(comparisonOf(t, cfg, tc.left, tc.right), tc.id)
			if !ok {
				t.Fatalf("%s is not in the comparison", tc.id)
			}
			if row.Left.Outcome != tc.want[0] || row.Right.Outcome != tc.want[1] {
				t.Errorf("%s is %s then %s, want %s then %s",
					tc.id, row.Left.Outcome, row.Right.Outcome, tc.want[0], tc.want[1])
			}
			if tc.mechanism != "" && row.Right.Mechanism != tc.mechanism {
				t.Errorf("%s is compensated by %q, want %q", tc.id, row.Right.Mechanism, tc.mechanism)
			}
			if row.Differs() == tc.sameVerdict {
				t.Errorf("%s reports Differs() == %v", tc.id, row.Differs())
			}
			if row.Left.Why == "" || row.Right.Why == "" {
				t.Errorf("%s gives no reason: %q then %q", tc.id, row.Left.Why, row.Right.Why)
			}
		})
	}
}

// TestCompareRefusesWhatIsNotATracker proves the mode cannot be pointed at a
// forge declaration, which would compare a board against a repository.
func TestCompareRefusesWhatIsNotATracker(t *testing.T) {
	registry := testRegistry(t)
	cfg := preset(t, config.PresetLeanKanban)
	for _, right := range []core.CapabilitySet{registry["github"], {}} {
		_, err := engine.Compare(engine.CompareInput{
			Config:  cfg,
			Desired: desired(t, cfg),
			Left:    registry["github-projects"],
			Right:   right,
			Now:     coretest.ContractNow,
		})
		if err == nil {
			t.Errorf("%q was accepted as a tracker", right.Adapter)
		}
	}
	if _, err := engine.Compare(engine.CompareInput{}); err == nil {
		t.Error("a comparison was made with no configuration")
	}
}

// TestRequiredTokens proves the assumed credential is the union of what the
// declaration itself asks for, rather than a list kept somewhere central.
func TestRequiredTokens(t *testing.T) {
	set := testRegistry(t)["github-projects"]
	tokens := set.RequiredTokens()
	if len(tokens) == 0 {
		t.Fatal("github-projects requires no permissions at all")
	}
	for _, name := range set.Names() {
		for _, token := range set.Capability(name).Requires {
			if !contains(tokens, token) {
				t.Errorf("%s requires %q, which the union does not hold", name, token)
			}
		}
	}
	for i := 1; i < len(tokens); i++ {
		if tokens[i-1] >= tokens[i] {
			t.Errorf("the tokens are not sorted and unique: %v", tokens)
			break
		}
	}
}

func find(c engine.Comparison, id string) (engine.ComparisonRow, bool) {
	for _, row := range c.Rows {
		if row.ID == id {
			return row, true
		}
	}
	return engine.ComparisonRow{}, false
}

func contains(haystack []string, needle string) bool {
	for _, straw := range haystack {
		if straw == needle {
			return true
		}
	}
	return false
}
