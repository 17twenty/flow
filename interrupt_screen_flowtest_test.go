//go:build flowtest

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// TestDoctorOnATerminalLetsGo is issue #289 end to end: `flow doctor` run by a
// person draws its progress screen, and every run of it since that screen was
// written answered every check and then never exited — not even to ctrl-c,
// which the screen's raw terminal turns into a key the run never heard. CI
// never draws the screen, so nothing but a person at a terminal ever saw it.
//
// Both ways out are driven here, on a terminal flow believes is real: the run
// finishing by itself, and somebody pressing ctrl-c.
func TestDoctorOnATerminalLetsGo(t *testing.T) {
	for name, tc := range map[string]struct {
		typed string
		check func(t *testing.T, code int, stderr string)
	}{
		"left alone": {typed: "", check: func(t *testing.T, code int, _ string) {
			if code == core.ExitInterrupted {
				t.Errorf("exited %d, interrupted, with nobody pressing anything", code)
			}
		}},
		"ctrl-c": {typed: "\x03", check: func(t *testing.T, code int, stderr string) {
			if code != core.ExitInterrupted || !strings.Contains(stderr, "interrupted") {
				t.Errorf("exited %d with %q, want %d and to say it was interrupted", code, stderr, core.ExitInterrupted)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := fakeRepo(t)
			t.Setenv("CI", "")
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			stdin := strings.NewReader(tc.typed)
			var stdout, stderr bytes.Buffer
			terminals(t, io.Reader(stdin), io.Writer(&stdout))

			finished := make(chan int, 1)
			go func() { finished <- run([]string{"-C", dir, "doctor"}, stdin, &stdout, &stderr) }()

			select {
			case code := <-finished:
				tc.check(t, code, stderr.String())
			case <-time.After(30 * time.Second):
				t.Fatal("flow doctor on a terminal never exited")
			}
		})
	}
}
