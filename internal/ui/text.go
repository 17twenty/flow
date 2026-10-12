package ui

import (
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/engine"
)

// Text rendering (spec 12). The layout is the engine's — what is indented
// under what, how much evidence is shown, what the last line says — and this
// file is everything a terminal adds to it: the colour a finding is painted
// in and the characters a font can be relied on to have.
//
// Decorating the engine's own rendering, rather than walking the result types
// a second time, is what keeps the plain and the coloured view the same view.
// A styled plan with the colour taken out again is byte-for-byte the plain
// one, which is a property a test can hold flow to, and which a second
// implementation of the same layout could only promise.

// decorate paints one of the engine's plain renderings.
func (r Renderer) decorate(plain string) string {
	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		lines[i] = r.Line(line)
	}
	return strings.Join(lines, "\n")
}

// Line paints one line of the engine's text, which is also what the doctor
// screen prints as each check finishes and what a CI annotation carries: a
// finding is renderable on its own, and waiting for the whole report to paint
// one would mean a terminal that says nothing for thirty seconds.
//
// A line is read by its shape, and the shapes are the engine's: a finding
// begins with a glyph, a heading is a category standing on its own, and
// everything indented beneath a finding is its detail. Nothing else is
// touched, because a line flow cannot recognise is a line it has no business
// recolouring.
func (r Renderer) Line(line string) string {
	body := strings.TrimLeft(line, " ")
	if body == "" {
		return line
	}
	indent := line[:len(line)-len(body)]

	symbol, rest := body, ""
	if space := strings.IndexByte(body, ' '); space >= 0 {
		symbol, rest = body[:space], body[space:]
	}
	if style, known := r.styles.forGlyph(symbol); known {
		return indent + style.Render(r.glyphText(symbol)) + r.words(rest)
	}

	if indent == "" {
		if slices.Contains(engine.Categories, body) {
			return r.styles.Accent.Render(r.words(body))
		}
		return r.words(body)
	}
	switch {
	case strings.HasPrefix(body, "hazard:"):
		return indent + r.styles.Warn.Render(r.words(body))
	case strings.HasPrefix(body, "fix:"), strings.HasPrefix(body, "run:"):
		return indent + r.styles.Info.Render(r.words(body))
	default:
		return indent + r.styles.Muted.Render(r.words(body))
	}
}
