package ui

import (
	"os"
	"testing"
)

// TestFlowDecidesNotToAsk holds the background query's guard, which the
// pipe test next door cannot: on Unix lipgloss declines a pipe by itself, so
// deleting the guard breaks nothing a test on this machine can see. What the
// guard is for is Windows, where lipgloss opens the console directly when
// stdio is redirected and would colour flow for whatever console happened to
// be attached to a CI job whose output is captured. So the test is of flow's
// decision rather than of the library's: when these are not terminals, the
// query is not reached at all.
func TestFlowDecidesNotToAsk(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	terminals := func(fds ...int) func(int) bool {
		return func(fd int) bool {
			for _, open := range fds {
				if open == fd {
					return true
				}
			}
			return false
		}
	}
	in, out := int(stdin.Fd()), int(stdout.Fd())

	for _, tc := range []struct {
		name       string
		isTerminal func(int) bool
		asked      bool
	}{
		{name: "neither is a terminal", isTerminal: terminals()},
		{name: "only the output is a terminal", isTerminal: terminals(out)},
		{name: "only the input is a terminal", isTerminal: terminals(in)},
		{name: "both are terminals", isTerminal: terminals(in, out), asked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			query := func(in, out *os.File) bool {
				asked = true
				return false
			}
			dark := darkBackground(stdin, stdout, tc.isTerminal, query)
			if asked != tc.asked {
				t.Errorf("asked the terminal: got %v, want %v", asked, tc.asked)
			}
			if !tc.asked && !dark {
				t.Error("a terminal flow would not ask was assumed light")
			}
			if tc.asked && dark {
				t.Error("flow asked the terminal and then ignored the answer")
			}
		})
	}
}

// TestNoFileIsNotATerminalEither covers the nil files a caller gets when
// flow's stdio has been replaced by something that is not an *os.File at all.
// Calling Fd on them would panic, which is a poor way to learn the terminal
// is not there.
func TestNoFileIsNotATerminalEither(t *testing.T) {
	always := func(int) bool { return true }
	query := func(in, out *os.File) bool {
		t.Error("flow asked a file it does not have")
		return false
	}
	for _, tc := range []struct {
		name    string
		in, out *os.File
	}{
		{name: "no input", out: os.Stdout},
		{name: "no output", in: os.Stdin},
		{name: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !darkBackground(tc.in, tc.out, always, query) {
				t.Error("a terminal flow could not ask was assumed light")
			}
		})
	}
}
