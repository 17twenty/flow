package main

// CheckCmd groups the CI entry points (spec 02 §2, specs 08, 09, 13).
type CheckCmd struct {
	PR     CheckPRCmd     `cmd:"" name:"pr" help:"Evaluate the policy gate and publish the flow/policy status."`
	Deploy CheckDeployCmd `cmd:"" help:"React to a deployment event; move tickets when done_on is deployed."`
	Commit CheckCommitCmd `cmd:"" help:"Lint one commit message."`
}

// CheckPRCmd is the CI gate. It evaluates policy, posts the flow/policy status,
// upserts its explanation comment and performs flow-owned transitions. It exits
// 0 whenever the status was posted: the status, not the job, carries the verdict.
type CheckPRCmd struct {
	PR            int  `name:"pr" help:"Pull request number. Defaults to the CI event payload." placeholder:"N"`
	DryRun        bool `name:"dry-run" help:"Evaluate and print; post nothing."`
	NoTransitions bool `name:"no-transitions" help:"Do not move tickets, only gate."`
	ExitCode      bool `name:"exit-code" help:"Exit 1 when policy fails. For local use."`
}

// Run implements the check pr command.
func (c *CheckPRCmd) Run(app *Context) error { return app.unimplemented("check pr") }

// CheckDeployCmd reacts to a deployment event. With done_on: deployed it moves
// the tickets linked to PRs merged at or before the deployed SHA (spec 04 §6).
type CheckDeployCmd struct {
	SHA         string `name:"sha" help:"Deployed commit. Defaults to the CI event payload." placeholder:"SHA"`
	Environment string `help:"Deployment environment. Defaults to the CI event payload." placeholder:"ENV"`
	DryRun      bool   `name:"dry-run" help:"Evaluate and print; change nothing."`
}

// Run implements the check deploy command.
func (c *CheckDeployCmd) Run(app *Context) error { return app.unimplemented("check deploy") }

// CheckCommitCmd lints a single commit message (spec 08). It backs the optional
// commit-msg hook.
type CheckCommitCmd struct {
	File string `arg:"" optional:"" default:"-" help:"Message file, or - for stdin."`
	Fix  bool   `help:"Apply safe autofixes in place."`
}

// Run implements the check commit command.
func (c *CheckCommitCmd) Run(app *Context) error { return app.unimplemented("check commit") }
