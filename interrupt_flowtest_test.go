//go:build flowtest && unix

package main

import (
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// A SIGINT during a command must exit 130 (spec 02 §4).
func TestInterruptExits130(t *testing.T) {
	// Hold our own subscription first so the process cannot be killed by the
	// default handler if the signal lands before run installs its own.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	defer signal.Stop(guard)

	done := make(chan int, 1)
	go func() { done <- run([]string{"sleep", "--for=30s"}, io.Discard, io.Discard) }()

	select {
	case <-sleepStarted():
	case <-time.After(10 * time.Second):
		t.Fatal("the sleep command never started")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	select {
	case code := <-done:
		if code != core.ExitInterrupted {
			t.Errorf("exited %d, want %d", code, core.ExitInterrupted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not stop after SIGINT")
	}
}

// Exceeding --timeout is an availability failure, not an interrupt.
func TestTimeoutExitsUnavailable(t *testing.T) {
	code := run([]string{"--timeout=20ms", "sleep", "--for=30s"}, io.Discard, io.Discard)
	if code != core.ExitUnavailable {
		t.Errorf("exited %d, want %d", code, core.ExitUnavailable)
	}
}
