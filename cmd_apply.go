package main

// ApplyCmd converges the forge and tracker on the desired state (spec 02 §2,
// spec 06). Exit 2 means follow-ups or inputs remain for a human.
type ApplyCmd struct {
	Plan        string `help:"Apply a saved plan; refused if it is stale." placeholder:"FILE"`
	AutoApprove bool   `name:"auto-approve" help:"Skip confirmation. Required when non-interactive."`
	Prune       bool   `help:"Remove options flow created that are no longer desired."`
	NoFollowups bool   `name:"no-followups" help:"Do not file follow-up tickets for unconfigurable settings."`
}

// Run implements the apply command.
func (c *ApplyCmd) Run(app *Context) error { return app.unimplemented("apply") }
