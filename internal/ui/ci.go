package ui

import (
	"strings"

	"github.com/17twenty/flow/internal/engine"
)

// CI annotations (spec 02 §2). A CI runner reads the lines a job writes and
// lifts the ones shaped like a command out of the log and onto the run
// itself, where somebody sees them without opening anything. This file is
// that shape, and only that shape: which findings are annotated and what each
// one says is the engine's (see engine's annotate.go).

// Annotations renders a report as the workflow commands a CI runner
// recognises, one line each, ending in a newline. A run with nothing to
// annotate renders to nothing at all.
//
// It takes no notice of the renderer's options. Colour would be ANSI in a
// field the runner stores and shows again on a web page, and the ASCII
// glyphs exist for a terminal with a font missing — neither is true of a
// browser reading an annotation, and a document with one shape has no
// business having three.
func (r Renderer) Annotations(report engine.Report) string {
	var out strings.Builder
	for _, annotation := range report.Annotations() {
		out.WriteString("::")
		out.WriteString(string(annotation.Level))
		out.WriteString(" title=")
		out.WriteString(escapeProperty(annotation.Title))
		out.WriteString("::")
		out.WriteString(escapeData(annotation.Message))
		out.WriteString("\n")
	}
	return out.String()
}

// escapeData makes a message safe to be the body of a workflow command.
//
// The runner reads every line a job writes, so a newline inside a message
// would not wrap it: it would end the command and offer the rest of the
// message to the parser as a command of its own. A check's message carries
// whatever a repository is called and whatever somebody titled a pull
// request, so this is the boundary where that stops being able to say
// anything to the runner (spec 15).
func escapeData(text string) string {
	return strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
	).Replace(text)
}

// escapeProperty makes a string safe to be a property of a workflow command,
// where a colon would end the properties and a comma would start another one.
func escapeProperty(text string) string {
	return strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	).Replace(text)
}
