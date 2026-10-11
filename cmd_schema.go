package main

// SchemaCmd prints the JSON Schema for one of flow's documents (spec 02 §2,
// spec 03 §6).
type SchemaCmd struct {
	Kind string `default:"config" enum:"config,lock,plan,doctor,policy" help:"Document to describe: config, lock, plan, doctor, policy."`
}

// Run implements the schema command.
func (c *SchemaCmd) Run(app *Context) error { return app.unimplemented("schema") }
