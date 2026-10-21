package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/git"
)

// HookCmd manages the optional local commit-msg hook that calls
// flow check commit --fix (spec 02 §2, spec 08).
type HookCmd struct {
	Install   HookInstallCmd   `cmd:"" help:"Install the commit-msg hook."`
	Uninstall HookUninstallCmd `cmd:"" help:"Remove the commit-msg hook flow installed."`
}

// hookName is the only hook flow installs. The gate on the pull request is the
// authority; this is the fast local echo of it (spec 08).
const hookName = "commit-msg"

// hookMarker is the line flow writes into its hook and recognises its own work
// by. A hook without it was written by somebody else and is never touched.
const hookMarker = "installed by flow"

// hookTemplate is the embedded script flow installs.
const hookTemplate = "hooks/" + hookName

// hookReport is the --json document for both hook commands, schema
// flow.hook/v1.
type hookReport struct {
	Schema string `json:"schema"`
	// Command is install or uninstall.
	Command string `json:"command"`
	// Action is what happened: installed, updated, unchanged, replaced,
	// removed, absent, or deferred when a hook manager is in charge.
	Action string `json:"action"`
	// Path is the hook file, absolute. Empty when nothing was written.
	Path string `json:"path"`
	// Managers lists the hook managers found in the repository, with the
	// configuration each needs.
	Managers []hookManagerReport `json:"managers"`
}

// hookManagerReport is one detected hook manager and its snippet.
type hookManagerReport struct {
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
}

// HookInstallCmd installs the hook, honouring core.hooksPath and refusing to
// overwrite a hook flow did not write. Where a hook manager is in use it prints
// the snippet to add instead.
type HookInstallCmd struct {
	Force bool `help:"Install anyway: replace an existing commit-msg hook and ignore any hook manager."`
}

// Run implements the hook install command.
func (c *HookInstallCmd) Run(app *Context) error {
	repo, err := git.Open(app.Ctx, app.CLI.Dir)
	if err != nil {
		return err
	}

	// A repository with a hook manager already has an answer to "what runs on
	// commit". Writing into .git/hooks behind it would either be ignored or
	// silently fight it, so flow says what to add and stops (spec 02 §2).
	managers, err := detectHookManagers(app, repo)
	if err != nil {
		return err
	}
	if len(managers) > 0 && !c.Force {
		return c.deferTo(app, managers)
	}

	dir, err := repo.HooksPath(app.Ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, hookName)

	script, err := hookScript()
	if err != nil {
		return err
	}

	action, err := installHook(path, script, c.Force)
	if err != nil {
		return err
	}
	return c.report(app, action, path, managers)
}

// deferTo explains that a hook manager owns this repository's hooks, and prints
// what to add to it. It is action required rather than a failure: flow did its
// part, and a person must paste the rest.
func (c *HookInstallCmd) deferTo(app *Context, managers []hookManagerReport) error {
	if err := c.report(app, "deferred", "", managers); err != nil {
		return err
	}
	names := make([]string, 0, len(managers))
	for _, m := range managers {
		names = append(names, m.Name)
	}
	return core.Errorf(core.ErrNeedsInput, "install the %s hook", hookName).
		WithWhy("%s already manages this repository's hooks", strings.Join(names, " and ")).
		WithFix("add the printed configuration, or run flow hook install --force to write .git/hooks anyway")
}

// report writes the outcome, as JSON or as a line a person reads.
func (c *HookInstallCmd) report(app *Context, action, path string, managers []hookManagerReport) error {
	if app.CLI.JSON {
		return core.WriteJSON(app.Stdout, hookReport{
			Schema:   core.SchemaID("hook"),
			Command:  "install",
			Action:   action,
			Path:     path,
			Managers: managers,
		})
	}
	if action == "deferred" {
		for _, m := range managers {
			fmt.Fprintf(app.Stdout, "%s manages this repository's hooks. Add:\n\n%s\n", m.Name, m.Snippet)
		}
		return nil
	}
	fmt.Fprintf(app.Stdout, "%s %s\n", action, path)
	return nil
}

// HookUninstallCmd removes the hook flow installed, leaving foreign hooks alone.
type HookUninstallCmd struct{}

// Run implements the hook uninstall command.
func (c *HookUninstallCmd) Run(app *Context) error {
	repo, err := git.Open(app.Ctx, app.CLI.Dir)
	if err != nil {
		return err
	}
	dir, err := repo.HooksPath(app.Ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, hookName)

	action := "removed"
	switch existing, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
		action = "absent"
	case err != nil:
		return core.Errorf(core.ErrValidation, "read %s", path).
			WithWhy("%v", err).
			WithFix("check the file's permissions")
	case !ours(existing):
		return foreignHook(path, "remove")
	default:
		if err := os.Remove(path); err != nil {
			return core.Errorf(core.ErrValidation, "remove %s", path).
				WithWhy("%v", err).
				WithFix("check the directory's permissions")
		}
	}

	if app.CLI.JSON {
		return core.WriteJSON(app.Stdout, hookReport{
			Schema:   core.SchemaID("hook"),
			Command:  "uninstall",
			Action:   action,
			Path:     path,
			Managers: []hookManagerReport{},
		})
	}
	if action == "absent" {
		fmt.Fprintf(app.Stdout, "no flow hook at %s\n", path)
		return nil
	}
	fmt.Fprintf(app.Stdout, "removed %s\n", path)
	return nil
}

// installHook writes script to path and says what it did. An identical hook is
// left alone so that running install twice changes nothing.
func installHook(path, script string, force bool) (string, error) {
	action := "installed"
	switch existing, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", core.Errorf(core.ErrValidation, "read %s", path).
			WithWhy("%v", err).
			WithFix("check the file's permissions")
	case ours(existing):
		if string(existing) == script {
			return "unchanged", nil
		}
		action = "updated"
	case force:
		action = "replaced"
	default:
		return "", foreignHook(path, "replace")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", core.Errorf(core.ErrValidation, "create %s", filepath.Dir(path)).
			WithWhy("%v", err).
			WithFix("check the directory's permissions")
	}
	if err := writeExecutable(path, script); err != nil {
		return "", err
	}
	return action, nil
}

// writeExecutable writes the hook through a temporary file in the same
// directory, so a half-written hook can never be the one git runs.
func writeExecutable(path, content string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return core.Errorf(core.ErrValidation, "write %s", path).
			WithWhy("%v", err).
			WithFix("check the directory's permissions")
	}
	defer os.Remove(temp.Name())

	fail := func(err error) error {
		temp.Close()
		return core.Errorf(core.ErrValidation, "write %s", path).
			WithWhy("%v", err).
			WithFix("check the directory's permissions")
	}
	if _, err := temp.WriteString(content); err != nil {
		return fail(err)
	}
	if err := temp.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err := temp.Sync(); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return core.Errorf(core.ErrValidation, "write %s", path).
			WithWhy("%v", err).
			WithFix("check the directory's permissions")
	}
	return nil
}

// ours reports whether flow wrote this hook.
func ours(content []byte) bool { return strings.Contains(string(content), hookMarker) }

// foreignHook is the refusal to touch somebody else's hook.
func foreignHook(path, verb string) error {
	err := core.Errorf(core.ErrConflict, "%s %s", verb, path).
		WithWhy("that hook was not written by flow")
	if verb == "replace" {
		return err.WithFix("add `flow check commit --fix \"$1\"` to it, or run flow hook install --force")
	}
	return err.WithFix("delete it yourself if you no longer want it")
}

// hookScript returns the embedded hook, which must carry the marker flow later
// recognises itself by.
func hookScript() (string, error) {
	templates, err := assets.Templates(buildVersion())
	if err != nil {
		return "", core.Errorf(core.ErrValidation, "read the hook template").WithWhy("%v", err)
	}
	content, err := fs.ReadFile(templates, hookTemplate)
	if err != nil {
		return "", core.Errorf(core.ErrValidation, "read the hook template").WithWhy("%v", err)
	}
	return string(content), nil
}

// hookManager is a tool that owns a repository's hooks, how to recognise it,
// and the snippet that adds flow to it.
type hookManager struct {
	// name is what the tool calls itself.
	name string
	// files are paths, relative to the repository root, that mean the tool is
	// in use. Any one of them is enough.
	files []string
	// hooksPath values that mean the tool has taken over core.hooksPath.
	hooksPath []string
	// snippet is the template to print.
	snippet string
}

// hookManagers are the tools spec 02 §2 names.
var hookManagers = []hookManager{
	{
		name:      "lefthook",
		files:     []string{"lefthook.yml", "lefthook.yaml", ".lefthook.yml", ".lefthook.yaml", "lefthook.toml", "lefthook.json", ".lefthook"},
		hooksPath: []string{".lefthook"},
		snippet:   "hook-managers/lefthook.yml",
	},
	{
		name:      "husky",
		files:     []string{".husky"},
		hooksPath: []string{".husky", ".husky/_"},
		snippet:   "hook-managers/husky-commit-msg",
	},
	{
		name:    "pre-commit",
		files:   []string{".pre-commit-config.yaml", ".pre-commit-config.yml"},
		snippet: "hook-managers/pre-commit-config.yaml",
	},
}

// detectHookManagers returns the hook managers in use in repo, with their
// snippets, in the order hookManagers lists them.
func detectHookManagers(app *Context, repo *git.Repo) ([]hookManagerReport, error) {
	configured, _, err := repo.HooksPathConfigured(app.Ctx)
	if err != nil {
		return nil, err
	}
	configured = filepath.ToSlash(strings.TrimSuffix(filepath.Clean(configured), "/"))

	templates, err := assets.Templates(buildVersion())
	if err != nil {
		return nil, core.Errorf(core.ErrValidation, "read the hook manager templates").WithWhy("%v", err)
	}

	found := []hookManagerReport{}
	for _, m := range hookManagers {
		if !m.present(repo.Dir(), configured) {
			continue
		}
		snippet, err := fs.ReadFile(templates, m.snippet)
		if err != nil {
			return nil, core.Errorf(core.ErrValidation, "read the %s template", m.name).WithWhy("%v", err)
		}
		found = append(found, hookManagerReport{Name: m.name, Snippet: strings.TrimRight(string(snippet), "\n")})
	}
	return found, nil
}

// present reports whether the manager is in use in the repository rooted at
// root, with core.hooksPath set to configured.
func (m hookManager) present(root, configured string) bool {
	if configured != "" && configured != "." {
		for _, path := range m.hooksPath {
			if configured == path || strings.HasPrefix(configured, path+"/") {
				return true
			}
		}
	}
	for _, name := range m.files {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			return true
		}
	}
	return false
}
