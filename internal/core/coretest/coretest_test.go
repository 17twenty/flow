package coretest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
)

// The fakes are the reference implementation: if they cannot pass the
// contract, no real adapter can be held to it.
func TestFakeForgeMeetsTheContract(t *testing.T) {
	coretest.ForgeContract(t, func(t *testing.T, fx coretest.ForgeFixture) core.Forge {
		return coretest.NewFakeForge(fx)
	})
}

func TestFakeTrackerMeetsTheContract(t *testing.T) {
	coretest.TrackerContract(t, func(t *testing.T, fx coretest.TrackerFixture) core.Tracker {
		return coretest.NewFakeTracker(fx)
	})
}

func TestFakeDeploysMeetsTheContract(t *testing.T) {
	coretest.DeployContract(t, func(t *testing.T, fx coretest.DeployFixture) core.DeploySource {
		return coretest.NewFakeDeploys(fx)
	})
}

func TestFaultsFailOnceThenRecover(t *testing.T) {
	forge := coretest.NewFakeForge(coretest.ContractForge())
	forge.FailOnce("Repo", core.Errorf(core.ErrRateLimited, "read the repository"))

	if _, err := forge.Repo(t.Context()); !errors.Is(err, core.ErrRateLimited) {
		t.Fatalf("Repo = %v, want the injected rate limit", err)
	}
	if _, err := forge.Repo(t.Context()); err != nil {
		t.Fatalf("Repo after the queued failure: %v", err)
	}
	if got := forge.Calls("Repo"); got != 2 {
		t.Errorf("Calls(Repo) = %d, want 2", got)
	}
}

func TestFaultsFailUntilHealed(t *testing.T) {
	tracker := coretest.NewFakeTracker(coretest.ContractTracker())
	tracker.Fail("Observe", core.Errorf(core.ErrPermission, "read the board"))

	for range 3 {
		if _, err := tracker.Observe(t.Context(), "tracker.labels"); !errors.Is(err, core.ErrPermission) {
			t.Fatalf("Observe = %v, want the injected refusal", err)
		}
	}
	tracker.Heal("Observe")
	if _, err := tracker.Observe(t.Context(), "tracker.labels"); err != nil {
		t.Fatalf("Observe after healing: %v", err)
	}
}

// A cancelled context must stop a fake just as it stops a provider, so that
// the root's signal handling can be tested without the network.
func TestFakesRespectACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	forge := coretest.NewFakeForge(coretest.ContractForge())
	if _, err := forge.Repo(ctx); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("Repo with a cancelled context = %v, want ErrUnavailable", err)
	}
	tracker := coretest.NewFakeTracker(coretest.ContractTracker())
	if _, err := tracker.Ticket(ctx, "#10"); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("Ticket with a cancelled context = %v, want ErrUnavailable", err)
	}
	deploys := coretest.NewFakeDeploys(coretest.ContractDeploys())
	if _, err := deploys.Deployments(ctx, core.DeployQuery{Environment: "production"}); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("Deployments with a cancelled context = %v, want ErrUnavailable", err)
	}
}

func TestFakeClockSleepsWithoutWaiting(t *testing.T) {
	clock := coretest.NewClock(time.Time{})
	start := clock.Now()

	if err := clock.Sleep(t.Context(), 90*time.Second); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if got := clock.Now().Sub(start); got != 90*time.Second {
		t.Errorf("the clock moved %s, want 90s", got)
	}
	if slept := clock.Slept(); len(slept) != 1 || slept[0] != 90*time.Second {
		t.Errorf("Slept() = %v, want one 90s wait", slept)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := clock.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestSystemClockSleepStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (core.SystemClock{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep = %v, want context.Canceled", err)
	}
	if err := (core.SystemClock{}).Sleep(t.Context(), time.Millisecond); err != nil {
		t.Errorf("Sleep: %v", err)
	}
}

// Declaring nothing is not the same as declaring that nothing is possible:
// both fakes exist so that an engine test can choose which world it is in.
func TestCapabilityHelpers(t *testing.T) {
	full := coretest.FullCapabilities("fake-tracker", core.KindTracker)
	none := coretest.NoCapabilities("fake-tracker", core.KindTracker)

	for _, name := range core.CapabilityNames(core.KindTracker) {
		if !full.Can(name) || !full.CanVerify(name) {
			t.Errorf("FullCapabilities cannot %q", name)
		}
		if none.Can(name) || none.CanVerify(name) {
			t.Errorf("NoCapabilities can %q", name)
		}
		if got := none.Capability(name).Remediation; got != core.RemediationFollowUp {
			t.Errorf("NoCapabilities remediation for %q = %q, want follow_up", name, got)
		}
	}
	if full.Researched().IsZero() {
		t.Error("FullCapabilities has no research date, so it would fail validation")
	}
}

// The fakes are safe to use from the bounded concurrency the adapters run
// under (spec 01 §5).
func TestFakesAreSafeForConcurrentUse(t *testing.T) {
	forge := coretest.NewFakeForge(coretest.ContractForge())
	tracker := coretest.NewFakeTracker(coretest.ContractTracker())

	done := make(chan struct{})
	for i := range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			ctx := context.Background()
			if _, err := forge.PullRequests(ctx, core.PRQuery{}); err != nil {
				t.Errorf("PullRequests: %v", err)
			}
			marker := core.NewMarker(core.MarkerPolicy, "pr", "1")
			if err := forge.UpsertComment(ctx, 1, marker, "working"); err != nil {
				t.Errorf("UpsertComment: %v", err)
			}
			if err := tracker.Move(ctx, "#10", coretest.ColumnInReview); err != nil {
				t.Errorf("Move: %v", err)
			}
			if _, err := tracker.Apply(ctx, core.Change{
				What: "tracker.labels",
				Op:   core.ChangeCreate,
				Item: core.Item{Name: "worker-" + string(rune('a'+i))},
			}); err != nil {
				t.Errorf("Apply: %v", err)
			}
		}()
	}
	for range 4 {
		<-done
	}
	if got := len(forge.Comments(1)); got != 1 {
		t.Errorf("four writers left %d comments, want one", got)
	}
}
