// Package coretest holds the fakes and the adapter contract suites. It exists
// so that the engine can be tested against adapters that behave, and so that
// every real adapter is held to the same behaviour as the fakes (spec 14).
//
// It is a test helper: nothing outside a _test.go file imports it.
package coretest

import (
	"context"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// FakeClock is a clock a test moves by hand. Sleeping advances it instead of
// waiting, so a retry with a minute of backoff costs nothing (spec 14).
type FakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

// NewClock returns a clock reading at, or at a fixed instant when at is zero.
func NewClock(at time.Time) *FakeClock {
	if at.IsZero() {
		at = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: at}
}

// Now returns the time the clock is set to.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Sleep advances the clock by d and records that it was asked to, unless the
// context is already done.
func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

// Slept returns the durations the clock was asked to sleep for, in order.
func (c *FakeClock) Slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// Faults makes a fake fail on demand, so that a caller's handling of a refusal
// can be tested without a provider that refuses. The zero value is ready to
// use, and every fake embeds one.
type Faults struct {
	mu     sync.Mutex
	sticky map[string]error
	once   map[string][]error
	calls  map[string]int
}

// Fail makes every call to that method return err, until [Faults.Heal].
func (f *Faults) Fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sticky == nil {
		f.sticky = map[string]error{}
	}
	f.sticky[method] = err
}

// FailOnce queues one failure for that method. Queued failures are returned
// in the order they were added, before any sticky failure.
func (f *Faults) FailOnce(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.once == nil {
		f.once = map[string][]error{}
	}
	f.once[method] = append(f.once[method], err)
}

// Heal clears every failure queued or set for that method.
func (f *Faults) Heal(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sticky, method)
	delete(f.once, method)
}

// Calls returns how many times that method has been called.
func (f *Faults) Calls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// enter records a call and returns the failure it should produce, if any. It
// is the first thing every fake method does.
func (f *Faults) enter(ctx context.Context, method string) error {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[method]++
	var err error
	if queued := f.once[method]; len(queued) > 0 {
		err, f.once[method] = queued[0], queued[1:]
	} else {
		err = f.sticky[method]
	}
	f.mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return core.Errorf(core.ErrUnavailable, "call %s", method).WithWhy("%s", ctxErr)
	}
	return err
}

// notFound is the error every fake returns for something that is not there.
func notFound(what, which string) error {
	return core.Errorf(core.ErrNotFound, "find %s %s", what, which).
		WithWhy("the fake holds no such thing").
		WithFix("seed the fixture with it")
}

// FullCapabilities declares every capability of a kind as settable and
// verifiable: the adapter that can do everything, which is what most engine
// tests want so that the outcome under test is the engine's, not the
// provider's (spec 05 §4).
func FullCapabilities(adapter, kind string) core.CapabilitySet {
	set := core.CapabilitySet{
		Adapter:      adapter,
		Kind:         kind,
		Version:      1,
		ResearchedAt: "2026-01-01",
		Sources:      []string{"https://example.invalid/fake"},
		Capabilities: map[string]core.Capability{},
	}
	for _, name := range core.CapabilityNames(kind) {
		set.Capabilities[name] = core.Capability{
			Set:         true,
			Verify:      true,
			Remediation: core.RemediationNone,
		}
	}
	return set
}

// NoCapabilities declares every capability of a kind as impossible, with a
// follow-up owed for each: the adapter that can do nothing.
func NoCapabilities(adapter, kind string) core.CapabilitySet {
	set := FullCapabilities(adapter, kind)
	for name, capability := range set.Capabilities {
		capability.Set, capability.Verify = false, false
		capability.Remediation = core.RemediationFollowUp
		set.Capabilities[name] = capability
	}
	return set
}
