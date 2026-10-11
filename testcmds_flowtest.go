//go:build flowtest

package main

import (
	"sync"
	"time"

	"github.com/alecthomas/kong"
)

// sleepCmd blocks until the context is cancelled or its duration elapses. It
// exists only in flowtest builds so that tests can exercise interrupt and
// deadline handling against the real signal plumbing in run.
type sleepCmd struct {
	For time.Duration `default:"1m" help:"How long to block."`
}

var (
	// sleepStartedCh is closed once a sleep command is blocked and the signal
	// handler installed by run is therefore active.
	sleepStartedCh = make(chan struct{})
	sleepStartOnce sync.Once
)

// sleepStarted returns a channel closed when a sleep command begins blocking.
func sleepStarted() <-chan struct{} { return sleepStartedCh }

// Run implements the test-only sleep command.
func (c *sleepCmd) Run(app *Context) error {
	sleepStartOnce.Do(func() { close(sleepStartedCh) })
	select {
	case <-app.Ctx.Done():
		return app.Ctx.Err()
	case <-time.After(c.For):
		return nil
	}
}

// testCommands registers commands that exist only in flowtest builds.
func testCommands() []kong.Option {
	return []kong.Option{
		kong.DynamicCommand("sleep", "Block until cancelled (test builds only).", "meta", &sleepCmd{}),
	}
}
