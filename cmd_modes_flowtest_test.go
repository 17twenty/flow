//go:build flowtest

package main

import (
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// Modes (spec 20 §8). A team adopts flow in stages: it reads the report
// before it lets flow post a verdict, and lets it post a verdict before it
// lets it act. Each stage has to write exactly what it says and nothing more.

// inMode puts a repository's configuration in a mode.
func inMode(t *testing.T, dir, mode string) {
	t.Helper()
	editFlowConfig(t, dir, "preset: lean-kanban\n", "preset: lean-kanban\nmode: "+mode+"\n")
}

func TestObserveWritesNothingAnywhere(t *testing.T) {
	merged := time.Now().Add(-3 * time.Hour)
	dir, world := gateRepo(t, func(w *fakeWorld) {
		mergedAndLeftOpen(merged)(w)
		pr := gatePR(14, "feat(engine): add x")
		pr.ClosingIssues = []string{"#12"}
		w.forge.SetPullRequest(pr)
		w.tracker.SetTicket(readyTicket("#12"))
	})
	inMode(t, dir, "observe")

	doc, code := gateJSON(t, dir, "--pr", "14")
	if code != core.ExitOK {
		t.Fatalf("check pr exited %d in observe", code)
	}
	if doc.Status != engine.GateDryRun {
		t.Errorf("the gate's status outcome is %q in observe, want %q", doc.Status, engine.GateDryRun)
	}
	if posts := world().forge.Calls("SetCommitStatus"); posts != 0 {
		t.Errorf("observe posted %d statuses", posts)
	}

	if code, _, stderr := execAt(t, dir, "sync"); code == core.ExitFail {
		t.Fatalf("sync failed in observe: %s", stderr)
	}
	if calls := world().tracker.Calls("Close") + world().tracker.Calls("Move"); calls != 0 {
		t.Errorf("sync made %d changes to the board in observe", calls)
	}

	if code, _, stderr := execAt(t, dir, "apply", "--auto-approve"); code != core.ExitUsage {
		t.Errorf("apply exited %d in observe, want %d: %s", code, core.ExitUsage, stderr)
	}

	execAt(t, dir, append([]string{"doctor", "--fix"}, owners()...)...)
	health, err := world().tracker.FindFollowUp(t.Context(), engine.HealthMarker("acme/api"))
	if err != nil {
		t.Fatal(err)
	}
	if health != nil {
		t.Errorf("doctor --fix published %s in observe", health.Key)
	}
	if calls := world().tracker.Calls("CreateFollowUp"); calls != 0 {
		t.Errorf("doctor --fix filed %d follow-ups in observe", calls)
	}
}

func TestGateModePostsTheVerdictAndActsOnNothing(t *testing.T) {
	dir, world := gateRepo(t, func(w *fakeWorld) {
		pr := gatePR(14, "feat(engine): add x")
		pr.ClosingIssues = []string{"#12"}
		w.forge.SetPullRequest(pr)
		w.tracker.SetTicket(readyTicket("#12"))
	})
	inMode(t, dir, "gate")
	editFlowConfig(t, dir, "  type: github", "  auto_merge: true\n  type: github")

	doc, _ := gateJSON(t, dir, "--pr", "14")
	if doc.Status != engine.GatePosted {
		t.Errorf("the status outcome is %q in gate, want %q", doc.Status, engine.GatePosted)
	}
	if len(doc.Moves) != 0 {
		t.Errorf("gate mode moved %+v", doc.Moves)
	}
	if got := ticketNow(t, world(), "#12").Status.Name; got != "Ready" {
		t.Errorf("gate mode moved #12 to %q", got)
	}
	if calls := world().forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("gate mode armed a merge %d times", calls)
	}
}
