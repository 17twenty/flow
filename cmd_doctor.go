package main

import "time"

// DoctorCmd proves the setup is intact and that the workflow is actually
// behaving, using read APIs only (spec 02 §2, spec 07).
type DoctorCmd struct {
	Fix       bool          `help:"Apply automatic remediations, resolve placeholders, file follow-ups."`
	Set       []string      `help:"Supply a placeholder value, as name=value." placeholder:"K=V"`
	Check     []string      `help:"Run only checks matching these globs." placeholder:"GLOB"`
	Window    time.Duration `default:"336h" help:"How far back the live-health audit looks."`
	CI        bool          `name:"ci" help:"Plain output plus CI annotations."`
	Report    string        `help:"Write a Markdown report to this file." placeholder:"FILE"`
	WriteLock bool          `name:"write-lock" default:"true" negatable:"" help:"Refresh .flow.lock; in --ci mode propose it as a PR instead."`
	Watch     bool          `help:"Re-run continuously in a terminal UI."`
}

// Run implements the doctor command.
func (c *DoctorCmd) Run(app *Context) error { return app.unimplemented("doctor") }
