package main

// HookCmd manages the optional local commit-msg hook that calls
// flow check commit --fix (spec 02 §2, spec 08).
type HookCmd struct {
	Install   HookInstallCmd   `cmd:"" help:"Install the commit-msg hook."`
	Uninstall HookUninstallCmd `cmd:"" help:"Remove the commit-msg hook flow installed."`
}

// HookInstallCmd installs the hook, honouring core.hooksPath and refusing to
// overwrite a hook flow did not write. Where a hook manager is in use it prints
// the snippet to add instead.
type HookInstallCmd struct {
	Force bool `help:"Replace an existing commit-msg hook."`
}

// Run implements the hook install command.
func (c *HookInstallCmd) Run(app *Context) error { return app.unimplemented("hook install") }

// HookUninstallCmd removes the hook flow installed, leaving foreign hooks alone.
type HookUninstallCmd struct{}

// Run implements the hook uninstall command.
func (c *HookUninstallCmd) Run(app *Context) error { return app.unimplemented("hook uninstall") }
