// Package ui renders what flow decided for the people who have to read it.
//
// The words are the engine's: which glyph a finding gets, what an outcome is
// called, what follows the arrow and in what order the categories come are
// all properties of the decision (spec 05 §5, spec 07 §9), and the terminal,
// the markdown report and the CI annotation must not be able to disagree
// about them. What belongs here is everything that is presentation and only
// presentation: colour, the glyphs a terminal can draw, and the shape a
// document takes when it is going to be read on a web page instead of in a
// shell (spec 12).
package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// Format is the shape a document is rendered in.
type Format string

// The formats every renderable document supports.
const (
	// FormatText is for a terminal: the engine's own layout, coloured.
	FormatText Format = "text"
	// FormatMarkdown is for a web page: GitHub-flavoured markdown, as an
	// issue comment or a --report file.
	FormatMarkdown Format = "markdown"
	// FormatJSON is the one document --json prints, exactly as the engine
	// serialises it.
	FormatJSON Format = "json"
)

// Options is how a person has asked to be written to. The root resolves them
// from the flags, the environment and the user's own configuration (spec 02
// §1, spec 03 §4); this package is handed the answers and does not go looking
// for them, which is what makes a rendering reproducible in a test.
type Options struct {
	// Format is the shape. The zero value is [FormatText].
	Format Format
	// Colour turns the palette on. It is false for --no-color, for NO_COLOR,
	// and whenever the output is not a terminal.
	Colour bool
	// ASCII swaps the Unicode glyphs and punctuation for ASCII.
	ASCII bool
	// Dark says the terminal has a dark background, which only matters when
	// Colour is on. See [DarkBackground].
	Dark bool
}

// Renderer turns an engine result into the bytes a person reads. It holds no
// state beyond the options it was built with, so the same renderer may render
// any number of documents, and two renderers built from the same options
// always agree.
type Renderer struct {
	format Format
	ascii  bool
	styles Styles
}

// New builds a renderer for these options.
func New(opts Options) Renderer {
	renderer := Renderer{format: opts.Format, ascii: opts.ASCII, styles: PlainStyles()}
	if renderer.format == "" {
		renderer.format = FormatText
	}
	if opts.Colour {
		renderer.styles = NewStyles(opts.Dark)
	}
	return renderer
}

// Styles is the palette this renderer paints with, for the screens that draw
// themselves rather than being rendered (spec 12's wizard, picker and watch).
func (r Renderer) Styles() Styles { return r.styles }

// Plan renders a plan (spec 05 §5, spec 06 §2).
func (r Renderer) Plan(plan engine.Plan) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(plan.Text()), nil
	case FormatMarkdown:
		return r.planMarkdown(plan), nil
	case FormatJSON:
		return r.json(plan.JSON())
	default:
		return "", r.unknownFormat()
	}
}

// Report renders a doctor report (spec 07 §9).
func (r Renderer) Report(report engine.Report) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(report.Text()), nil
	case FormatMarkdown:
		return r.reportMarkdown(report), nil
	case FormatJSON:
		return r.json(report.JSON())
	default:
		return "", r.unknownFormat()
	}
}

// Applied renders what an apply did (spec 02 §3, spec 06 §3).
func (r Renderer) Applied(applied engine.Applied) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(applied.Text()), nil
	case FormatJSON:
		return r.json(applied.JSON())
	default:
		return "", r.noRendering("an apply")
	}
}

// Comparison renders two trackers side by side (spec 05 §6).
func (r Renderer) Comparison(comparison engine.Comparison) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(comparison.Text()), nil
	case FormatJSON:
		return r.json(comparison.JSON())
	default:
		return "", r.noRendering("a comparison")
	}
}

// Policy renders a run of the policy gate (spec 09).
//
// The gate's markdown already exists and is not this: it is the comment the
// gate leaves on the pull request, written by the engine so that the same
// bytes go out whoever is running flow. Rendering a second markdown here would
// be a second answer to a question already settled.
func (r Renderer) Policy(gate engine.Gate) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(gate.Text()), nil
	case FormatJSON:
		return r.json(gate.JSON())
	default:
		return "", r.noRendering("a policy result")
	}
}

// Deploy renders what a deployment finished (spec 02 §2, spec 13).
func (r Renderer) Deploy(deploy engine.Deploy) (string, error) {
	switch r.format {
	case FormatText:
		return r.decorate(deploy.Text()), nil
	case FormatJSON:
		return r.json(deploy.JSON())
	default:
		return "", r.noRendering("a deployment")
	}
}

// json passes the engine's serialisation through untouched. A JSON document
// is the one rendering with no presentation in it at all: colouring it would
// break the parser the flag exists for, and translating its glyphs would
// change the data (spec 02 §3).
func (r Renderer) json(data []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unknownFormat refuses a format nothing can render, rather than falling back
// to text: a caller that asked for JSON and was handed a table would put it
// somewhere a machine was waiting.
func (r Renderer) unknownFormat() error {
	return fmt.Errorf("%w: there is no %q rendering; flow writes text, markdown or json", core.ErrValidation, r.format)
}

// noRendering refuses a format this particular document has no shape for.
//
// Markdown belongs to the two documents somebody reads on a web page: the
// plan a setup PR quotes and the doctor report `--report` writes (spec 02
// §2). An apply, a comparison and a policy result are read where they were
// asked for, so a caller asking for markdown has made a mistake, and answering
// with the terminal layout would put ANSI-shaped text in a comment.
func (r Renderer) noRendering(what string) error {
	return fmt.Errorf("%w: there is no %q rendering of %s; flow writes it as text or json", core.ErrValidation, r.format, what)
}

// Fprint writes rendered output to w, downsampling its colour to what the
// terminal behind w can show — and removing it entirely when w is not a
// terminal at all, which is what keeps ANSI out of a pipe, a file and a CI
// log without every caller having to ask (spec 12).
func Fprint(w io.Writer, text string) error {
	if _, err := lipgloss.Fprint(w, text); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
