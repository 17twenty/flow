//go:build flowtest

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// `flow sync` (spec 20 §2). The catch-ups flow owns, run often and cheaply: it
// must settle what a lost event left behind, and must do nothing else — no
// health report, no lock proposal, no follow-up — because the audit is
// doctor's and an audit every hour is a rate limit spent on nothing.

// mergedAndLeftOpen is the case that forced sync (issue #201): a pull request
// merged, named an issue, and the forge closed nothing.
func mergedAndLeftOpen(merged time.Time) func(*fakeWorld) {
	return func(w *fakeWorld) {
		w.forge.SetPermissions(adminAccess)
		w.forge.SetPullRequest(core.PullRequest{
			Number:        7,
			Title:         "feat(api): ship it",
			Author:        "nick",
			State:         core.PRStateMerged,
			MergeSHA:      "merge007",
			BaseRef:       "main",
			ClosingIssues: []string{"#42"},
			UpdatedAt:     merged,
			MergedAt:      merged,
		})
		w.tracker.SetTicket(core.Ticket{
			Key:      "#42",
			Title:    "ship it",
			Body:     "## Acceptance criteria\n\n- it is running\n",
			Parent:   "#1",
			Status:   core.Status{ID: "In review", Name: "In review"},
			ColumnID: core.ColumnID("In review"),
		})
	}
}

func TestSyncClosesWhatAMergeLeftOpenAndNothingElse(t *testing.T) {
	dir, world := gateRepo(t, mergedAndLeftOpen(time.Now().Add(-3*time.Hour)))

	code, _, stderr := execAt(t, dir, "sync")
	if code != core.ExitOK {
		t.Fatalf("sync exited %d: %s", code, stderr)
	}
	ticket, err := world().tracker.Ticket(t.Context(), "#42")
	if err != nil {
		t.Fatal(err)
	}
	if !ticket.Closed {
		t.Error("#42 is still open after sync")
	}

	health, err := world().tracker.FindFollowUp(t.Context(), engine.HealthMarker("acme/api"))
	if err != nil {
		t.Fatal(err)
	}
	if health != nil {
		t.Errorf("sync published the health report as %s", health.Key)
	}
	if got := proposals(t, world()); len(got) != 0 {
		t.Errorf("sync proposed %d pull requests; it proposes nothing", len(got))
	}
	if calls := world().tracker.Calls("CreateFollowUp"); calls != 0 {
		t.Errorf("sync filed %d follow-ups; filing is the audit's", calls)
	}
}

// The window is hours, not a fortnight: a merge older than it is the daily
// audit's to find, and an hourly run that walked two weeks of history would
// spend the rate limit the audit needs.
func TestSyncLooksOnlyAsFarBackAsItsWindow(t *testing.T) {
	dir, world := gateRepo(t, mergedAndLeftOpen(time.Now().Add(-5*24*time.Hour)))

	if code, _, stderr := execAt(t, dir, "sync"); code == core.ExitFail {
		t.Fatalf("sync failed: %s", stderr)
	}
	if calls := world().tracker.Calls("Close"); calls != 0 {
		t.Errorf("sync closed something merged five days ago, outside its 48h window")
	}
	if code, _, stderr := execAt(t, dir, "sync", "--window", "7d"); code == core.ExitFail {
		t.Fatalf("sync --window 7d failed: %s", stderr)
	}
	ticket, _ := world().tracker.Ticket(t.Context(), "#42")
	if !ticket.Closed {
		t.Error("a wider window did not reach the merge it covers")
	}
}

// The merge transitions are flow's in the shipped preset (spec 20 §4), so a
// merge whose card the tracker never moved is moved by the next sync — no
// automation of the tracker's to attest, and nothing for a person to do.
func TestSyncMovesTheCardAMergeLeftBehind(t *testing.T) {
	dir, world := gateRepo(t, mergedAndLeftOpen(time.Now().Add(-3*time.Hour)))

	if code, _, stderr := execAt(t, dir, "sync"); code != core.ExitOK {
		t.Fatalf("sync exited %d: %s", code, stderr)
	}
	ticket, err := world().tracker.Ticket(t.Context(), "#42")
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Status.Name != "Done" {
		t.Errorf("#42 is in %q after sync, want Done: pr_merged is flow's in the preset", ticket.Status.Name)
	}
	if !ticket.Closed {
		t.Error("#42 was moved and left open: both catch-ups belong to one run")
	}
}

// TestSyncNamesAPullRequestOnlyBehindWithoutFailing is issue #240 on a forge
// flow cannot update branches on: the pull request that passed the gate and
// is only behind is named, and sync says somebody has something to do (2)
// rather than failing (1) — it runs every hour, and a chore only a person can
// do must not turn every one of those runs red.
func TestSyncNamesAPullRequestOnlyBehindWithoutFailing(t *testing.T) {
	dir, _ := gateRepo(t, func(w *fakeWorld) {
		w.forge.SetPermissions(adminAccess)
		pr := gatePR(14, "feat(engine): ready")
		pr.Mergeability = core.MergeabilityBehind
		w.forge.SetPullRequest(pr)
		if err := w.forge.SetCommitStatus(t.Context(), pr.HeadSHA, core.CommitStatus{Context: engine.DefaultStatusContext, State: core.StatusSuccess}); err != nil {
			t.Fatal(err)
		}
	})

	code, stdout, stderr := execAt(t, dir, "sync")
	if code != core.ExitActionRequired {
		t.Fatalf("sync exited %d, want %d: %s", code, core.ExitActionRequired, stderr)
	}
	if !strings.Contains(stdout, "#14") || !strings.Contains(stdout, engine.CheckLivePRsCurrent) {
		t.Errorf("sync does not name the pull request waiting on being behind:\n%s", stdout)
	}
}
