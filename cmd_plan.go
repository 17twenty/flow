package main

// PlanCmd shows what flow would configure, without changing anything
// (spec 02 §2, spec 06). Exit 0 means nothing to do, 2 means changes pending.
type PlanCmd struct {
	Only            []string `help:"Restrict the plan to these capabilities." placeholder:"CAPABILITY"`
	Target          []string `help:"Restrict the plan to these targets: forge, tracker, deploys." placeholder:"TARGET"`
	TrackerOverride string   `name:"tracker-override" help:"Compare against another tracker kind. Offline." placeholder:"KIND"`
	Out             string   `help:"Write the plan to a file for a later flow apply --plan." placeholder:"FILE"`
}

// Run implements the plan command.
func (c *PlanCmd) Run(app *Context) error { return app.unimplemented("plan") }
