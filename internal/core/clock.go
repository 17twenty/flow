package core

import (
	"context"
	"time"
)

// Clock is where every part of flow reads the time. Nothing calls [time.Now]
// directly: a check that asks "was this deployed within max_lag" is a pure
// function of its inputs only if the time is one of them, and a test that must
// wait for a retry would otherwise have to sleep (spec 01 §5, spec 14).
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Sleep waits for d, or until ctx is done, and returns ctx.Err() if it was
	// the context that ended the wait.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the real clock, used everywhere but in tests.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep waits for d, or until ctx is done.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
