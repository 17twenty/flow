package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Glyphs and the ASCII that stands in for them (spec 12).
//
// Which glyph a finding gets is the engine's decision (spec 05 §5, spec 07
// §2); what to do when the terminal cannot draw it is this package's. The
// table is deliberately injective: two findings that look different in a
// Unicode terminal must look different in an ASCII one, or --ascii would
// quietly merge a warning into an error.

// asciiGlyph is what each glyph the engine uses becomes under --ascii. The
// error glyph is already ASCII and is still translated, because "!" is what
// the warning glyph falls back to and the two must not collide.
var asciiGlyph = map[string]string{
	"✓": "+",
	"✗": "x",
	"⚠": "!",
	"✎": "*",
	"↻": "~",
	"≠": "!=",
	"⏸": "||",
	"⏳": "...",
	"·": ".",
	"!": "E",
	"?": "?",
}

// asciiText is what the punctuation flow writes in prose becomes under
// --ascii. It is applied to a line's words, never to its glyph: "!" means
// error at the head of a line and means nothing in particular inside a
// sentence, and a replacer cannot tell the two apart. The interpunct is a
// separator here and a glyph in the table above, which is why the two differ:
// as punctuation it becomes a dash, and a pipe — the other obvious choice —
// would turn the summary line of a markdown report into a table row.
var asciiText = strings.NewReplacer(
	"→", "->",
	"…", "...",
	"·", "-",
	"—", "--",
)

// glyphText is the glyph as characters, with no colour: itself, or its ASCII
// stand-in. A glyph with no entry in the table is left alone rather than
// dropped — a reader seeing a character their font cannot draw is better off
// than one seeing nothing at all.
func (r Renderer) glyphText(symbol string) string {
	if !r.ascii {
		return symbol
	}
	if ascii, ok := asciiGlyph[symbol]; ok {
		return ascii
	}
	return symbol
}

// words renders prose: ASCII punctuation when the terminal is held to it,
// and the engine's own characters otherwise.
func (r Renderer) words(text string) string {
	if !r.ascii {
		return text
	}
	return asciiText.Replace(text)
}

// forGlyph is the style a glyph is painted in. The second result reports
// whether the symbol is one of flow's at all, which is what lets the text
// renderer tell a finding's line from a line of prose that happens to start
// with a short word.
func (s Styles) forGlyph(symbol string) (lipgloss.Style, bool) {
	switch symbol {
	case "✓":
		return s.Ok, true
	case "✗", "!":
		return s.Err, true
	case "⚠", "≠":
		return s.Warn, true
	case "↻", "⏳", "?":
		return s.Info, true
	case "✎":
		return s.Accent, true
	case "·", "⏸":
		return s.Muted, true
	default:
		return lipgloss.Style{}, false
	}
}
