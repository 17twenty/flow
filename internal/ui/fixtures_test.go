package ui_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// samplePlan is a plan with one item of every outcome, which is what makes a
// golden worth having: a rendering that is right for a passing plan and wrong
// for a failing one is wrong where it matters.
func samplePlan() engine.Plan {
	return engine.Plan{
		Schema:      engine.PlanVersion,
		FlowVersion: "1.0.0",
		GeneratedAt: time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC),
		Providers: map[string]string{
			engine.TargetForge:   "github",
			engine.TargetTracker: "github-projects",
			engine.TargetDeploys: "github-deployments",
		},
		ConfigHash:  "cfg-1111",
		DesiredHash: "des-2222",
		Items: []engine.PlanItem{
			{
				ID:         "forge.branch_protection",
				Target:     engine.TargetForge,
				Capability: "branch_protection",
				Outcome:    engine.OutcomeNative,
				Why:        "main is unprotected and the credential may protect it",
				Changes: []core.Change{{
					What: "forge.branch_protection",
					Op:   core.ChangeUpdate,
					Item: core.Item{Name: "main", Fields: map[string]string{
						"required_checks": "flow/policy",
						"reviews":         "1",
					}},
				}},
				Hazards: []string{"rewriting protection clears any rule flow does not send"},
			},
			{
				ID:         "tracker.columns",
				Target:     engine.TargetTracker,
				Capability: "status_options",
				Outcome:    engine.OutcomeCompensated,
				Mechanism:  "a saved view",
				Why:        "the board cannot name its own columns through the API",
			},
			{
				ID:         "tracker.automation",
				Target:     engine.TargetTracker,
				Capability: "transitions",
				Outcome:    engine.OutcomeFollowUp,
				Owner:      "@platform",
				Missing:    []string{"project:admin"},
				Why:        "the credential may not change this board's workflow",
			},
			{
				ID:         "tracker.saved_view",
				Target:     engine.TargetTracker,
				Capability: "views",
				Outcome:    engine.OutcomeAttest,
				Why:        "no API reports whether the view exists — a human must say",
			},
			{
				ID:         "deploys.environments",
				Target:     engine.TargetDeploys,
				Capability: "environments",
				Outcome:    engine.OutcomeUnsupported,
				Why:        "this deployment source has no environments to configure",
			},
			{
				ID:      "forge.pr_template",
				Target:  engine.TargetForge,
				Outcome: engine.OutcomeNoop,
				Exists:  true,
				Why:     "the template is already what the configuration asks for",
			},
		},
	}
}

// sampleReport is a doctor run with a check in every state that renders
// differently: counts, evidence beyond the five shown, every kind of remedy,
// and a category that is empty and must not appear at all.
func sampleReport() engine.Report {
	return engine.Report{
		Version:     engine.DoctorVersion,
		FlowVersion: "1.0.0",
		GeneratedAt: time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC),
		Window:      "14d",
		Checks: []engine.Checked{
			{
				ID:       "config.syntax",
				Title:    "the configuration parses",
				Category: engine.CategoryConfig,
				Result:   engine.Pass(".flow.yaml is valid"),
			},
			{
				ID:       "forge.protection",
				Title:    "the trunk is protected",
				Category: engine.CategoryForge,
				Result: engine.Fail("main may be pushed to directly").Fix(engine.Remediation{
					Kind:    engine.RemedyAuto,
					Summary: "protect main",
					Command: "flow apply",
				}),
			},
			{
				ID:        "forge.required_checks",
				Title:     "flow/policy is required to merge",
				Category:  engine.CategoryForge,
				DependsOn: []string{"forge.protection"},
				BlockedBy: "forge.protection",
				Result:    engine.Result{State: engine.StateBlocked, Message: "forge.protection did not hold"},
			},
			{
				ID:       "tracker.project",
				Title:    "the board flow manages is known",
				Category: engine.CategoryTracker,
				Result: engine.Result{State: engine.StateInput, Message: "which board is this team's?"}.
					Fix(engine.Remediation{
						Kind:    engine.RemedyInput,
						Summary: "name the board",
						Command: "flow doctor --fix --set tracker.project=<id>",
					}),
			},
			{
				ID:       "tracker.automation",
				Title:    "the board moves tickets by itself",
				Category: engine.CategoryTracker,
				Result: engine.Result{State: engine.StateExternal, Message: "asked 9 days ago"}.
					Fix(engine.Remediation{
						Kind:    engine.RemedyFollowUp,
						Summary: "the follow-up is open",
						Owner:   "@platform",
					}),
			},
			{
				ID:       "tracker.saved_view",
				Title:    "the ready view exists",
				Category: engine.CategoryTracker,
				Result: engine.Result{State: engine.StateAttest, Message: "nobody has vouched for this yet"}.
					Fix(engine.Remediation{
						Kind:    engine.RemedyAttest,
						Summary: "say what you see",
						Command: "flow attest tracker.saved_view --note '…'",
					}),
			},
			{
				ID:       "live.prs.linked",
				Title:    "every pull request names its ticket",
				Category: engine.CategoryLive,
				Result: engine.Result{
					State:   engine.StateWarn,
					Message: "some pull requests name no ticket",
					Counts:  &engine.Counts{Pass: 12, Total: 19},
					Evidence: []engine.Evidence{
						{Text: "#101 tidy the loader", URL: "https://example.invalid/101"},
						{Text: "#104 bump deps", URL: "https://example.invalid/104"},
						{Text: "#109 fix the flake", URL: "https://example.invalid/109"},
						{Text: "#112 rename a field", URL: "https://example.invalid/112"},
						{Text: "#115 drop dead code", URL: "https://example.invalid/115"},
						{Text: "#118 widen a timeout", URL: "https://example.invalid/118"},
						{Text: "#121 split a test", URL: "https://example.invalid/121"},
					},
				}.Fix(engine.Remediation{
					Kind:    engine.RemedyManual,
					Summary: "ask for the ticket key in the title",
					Steps:   []string{"agree the convention", "turn the rule on in .flow.yaml"},
				}),
			},
			{
				ID:       "live.deploys.recent",
				Title:    "production has been deployed to lately",
				Category: engine.CategoryLive,
				Result:   engine.Failed(core.ErrUnavailable),
			},
			{
				ID:       "meta.lock",
				Title:    "the lock matches what flow found",
				Category: engine.CategoryMeta,
				Result:   engine.Skip("no lock is written in CI"),
			},
		},
	}
}

// golden compares got with a golden file, writing it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("make golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}
