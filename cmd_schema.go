package main

import (
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// SchemaCmd prints the JSON Schema for one of flow's documents (spec 02 §2,
// spec 03 §6).
type SchemaCmd struct {
	Kind string `default:"config" enum:"config,lock,plan,doctor,policy" help:"Document to describe: config, lock, plan, doctor, policy."`
}

// Run implements the schema command. The output is a JSON Schema document, so
// it is the same with or without --json: it is already exactly one JSON
// document on stdout, and its $id says what it describes.
func (c *SchemaCmd) Run(app *Context) error {
	if c.Kind != "config" {
		return core.Errorf(core.ErrUnsupported, "print the %s schema", c.Kind).
			WithWhy("this build describes the configuration file; the %s document does not exist yet", c.Kind).
			WithFix("run flow schema --kind config")
	}

	data, err := config.SchemaJSON(config.ConfigSchema())
	if err != nil {
		return core.Errorf(core.ErrValidation, "print the %s schema", c.Kind).WithWhy("%s", err)
	}
	_, err = app.Stdout.Write(data)
	return err
}
