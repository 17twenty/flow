package ui

import (
	"os"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// Styles is the palette flow paints with (spec 12). Seven styles is the whole
// vocabulary: a renderer that wants an eighth colour is inventing meaning the
// reader has not been taught.
//
// The zero value is the plain palette. Every lipgloss.Style in it is the zero
// style, which renders text exactly as it was given, so a renderer never has
// to ask whether colour is on — it paints, and the palette decides whether
// painting does anything.
type Styles struct {
	// Ok is a thing that holds.
	Ok lipgloss.Style
	// Warn is a thing that holds less than it should.
	Warn lipgloss.Style
	// Err is a thing that does not hold.
	Err lipgloss.Style
	// Info is a thing somebody else is doing, or a thing to run.
	Info lipgloss.Style
	// Muted is detail: why, evidence, the things that did not change.
	Muted lipgloss.Style
	// Accent is a heading or a human's word.
	Accent lipgloss.Style
	// Code is a literal — an id, a command, a value.
	Code lipgloss.Style
}

// PlainStyles is the palette that paints nothing. It is what --no-color,
// NO_COLOR and a pipe all end up with.
func PlainStyles() Styles { return Styles{} }

// NewStyles builds the palette for a terminal with a dark or a light
// background. The pairs are GitHub's own light and dark hues, which is the
// palette the markdown these renderers also produce will be read in.
func NewStyles(dark bool) Styles {
	pick := lipgloss.LightDark(dark)
	colours := func(light, dark string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(pick(lipgloss.Color(light), lipgloss.Color(dark)))
	}
	return Styles{
		Ok:     colours("#1a7f37", "#3fb950"),
		Warn:   colours("#9a6700", "#d29922"),
		Err:    colours("#cf222e", "#f85149"),
		Info:   colours("#0969da", "#58a6ff"),
		Muted:  colours("#59636e", "#8b949e"),
		Accent: colours("#8250df", "#bc8cff").Bold(true),
		Code:   colours("#24292f", "#e6edf3"),
	}
}

// DarkBackground reports whether the terminal behind out has a dark
// background, asking it only when both files really are terminals: the query
// is an escape sequence and a read, and sending it down a pipe writes rubbish
// into somebody's output and then waits for an answer that cannot come.
//
// lipgloss refuses a pipe too, on Unix. On Windows it does the opposite — it
// opens the console directly when stdio is redirected — so a flow running in
// a Windows CI job with its output captured would colour itself for whatever
// console happened to be attached. The guard is what makes the answer depend
// on where the output is going on every platform.
//
// When flow cannot ask, it assumes dark, which is both lipgloss's own default
// and the background most terminals ship with. Getting it wrong costs a shade
// of contrast; refusing to colour anything until a terminal has answered a
// question would cost every pipe-to-less its colour.
func DarkBackground(in, out *os.File) bool {
	query := func(in, out *os.File) bool { return lipgloss.HasDarkBackground(in, out) }
	return darkBackground(in, out, term.IsTerminal, query)
}

// darkBackground is [DarkBackground] with the two things it asks of the world
// handed to it. The seam is what makes the guard testable: on Unix lipgloss
// declines a pipe by itself, so a test that only watched a pipe would pass
// just as happily with the guard deleted, and the Windows behaviour the guard
// exists for cannot be reproduced on the machine running the test. What can
// be tested anywhere is flow's own decision — that when the files are not
// terminals, flow does not ask at all.
func darkBackground(in, out *os.File, isTerminal func(fd int) bool, query func(in, out *os.File) bool) bool {
	if in == nil || out == nil {
		return true
	}
	if !isTerminal(int(in.Fd())) || !isTerminal(int(out.Fd())) {
		return true
	}
	return query(in, out)
}
