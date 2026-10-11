package main

// AttestCmd records a named, expiring human confirmation for a check flow
// cannot verify through an API (spec 02 §2, spec 07 §8). Attesting a check that
// is API-verifiable is a usage error.
type AttestCmd struct {
	CheckID string `arg:"" name:"check-id" help:"Check to attest, e.g. tracker.native.pr_merged."`
	Note    string `required:"" help:"What was confirmed, and how."`
	By      string `help:"Who confirmed it. Defaults to the handle in user config." placeholder:"HANDLE"`
	Expires string `default:"30d" help:"How long the attestation stays valid." placeholder:"DURATION"`
}

// Run implements the attest command.
func (c *AttestCmd) Run(app *Context) error { return app.unimplemented("attest") }
