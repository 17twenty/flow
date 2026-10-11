package main

// InitCmd writes .flow.yaml and the supporting files for a repository
// (spec 02 §2). It works offline and never writes to the remote default branch.
type InitCmd struct {
	Preset      string `help:"Workflow preset to start from." default:"lean-kanban" enum:"lean-kanban,deploy-done,minimal"`
	Forge       string `help:"Forge kind, e.g. github." placeholder:"KIND"`
	Tracker     string `help:"Tracker kind, e.g. github-projects." placeholder:"KIND"`
	TrackerURL  string `name:"tracker-url" help:"Tracker base URL for self-hosted installations." placeholder:"URL"`
	Project     string `help:"Project or board reference." placeholder:"REF"`
	Owner       string `help:"GitHub Projects owner login." placeholder:"LOGIN"`
	Force       bool   `help:"Overwrite an existing configuration."`
	NoHooks     bool   `name:"no-hooks" help:"Do not install the local commit-msg hook."`
	NoTemplates bool   `name:"no-templates" help:"Do not write CI, PR or issue templates."`
	Yes         bool   `short:"y" help:"Accept every default without prompting."`
}

// Run implements the init command.
func (c *InitCmd) Run(app *Context) error { return app.unimplemented("init") }
