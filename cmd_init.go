package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/git"
	"github.com/17twenty/flow/internal/ui"
)

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

// configFile is the name of the team configuration, relative to the repository
// root (spec 03 §1).
const configFile = ".flow.yaml"

// initReport is the --json document, schema flow.init/v1.
type initReport struct {
	Schema string `json:"schema"`
	// Root is the repository flow wrote into, absolute.
	Root string `json:"root"`
	// Preset is the preset the configuration started from.
	Preset string `json:"preset"`
	// Files lists what was written, repository-relative and sorted.
	Files []initFile `json:"files"`
	// Pending names the values nobody knows yet, which doctor resolves
	// (spec 03 §2). Empty when the configuration is complete.
	Pending []string `json:"pending,omitempty"`
	// Hook says what happened to the commit-msg hook: installed, updated,
	// unchanged, deferred or skipped.
	Hook string `json:"hook"`
}

// initFile is one file flow wrote or left alone.
type initFile struct {
	Path string `json:"path"`
	// Action is written, overwritten or kept.
	Action string `json:"action"`
}

// Run implements the init command.
//
// Everything here happens on this machine. init writes files into the working
// tree and stops; the team's own review process is what puts them on the
// default branch, because a tool that configures a repository by pushing to
// main is a tool that has opinions about who may change main (spec 02 §2).
func (c *InitCmd) Run(app *Context) error {
	repo, err := c.repository(app)
	if err != nil {
		return err
	}
	if err := c.refuseExisting(repo.Dir()); err != nil {
		return err
	}

	source, asked, err := c.ask(app, repo)
	if err != nil {
		return err
	}
	if !asked {
		fmt.Fprintln(app.Stdout, "nothing was written")
		return nil
	}

	written, err := c.write(app, repo.Dir(), source)
	if err != nil {
		return err
	}

	hook, err := c.installHook(app)
	if err != nil {
		return err
	}

	return c.report(app, repo.Dir(), source, written, hook)
}

// refuseExisting stops before anything is written when the repository is
// already configured (spec 02 §2).
//
// The refusal does not look at what the existing file says. A configuration
// that happens to match the one init would write is still a file somebody
// committed and may have reasons for, and "init did nothing because it agreed
// with you" is a sentence no one should have to work out from a silent exit 0.
// Checking first is what keeps a refused init from having written four
// templates on its way to the refusal.
func (c *InitCmd) refuseExisting(root string) error {
	if c.Force {
		return nil
	}
	path := filepath.Join(root, configFile)
	switch _, err := os.Stat(path); {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return core.Errorf(core.ErrValidation, "read %s", configFile).
			WithWhy("%v", err).
			WithFix("check the file's permissions")
	}
	return core.Errorf(core.ErrValidation, "write %s", configFile).
		WithWhy("this repository is already configured").
		WithFix("run flow init --force to start again, or flow doctor to check what is there")
}

// repository opens the working tree, refusing anywhere that is not one. The
// fix is the command the person needs, because "not a git repository" with no
// next step is the error message everybody has learned to ignore (A02).
func (c *InitCmd) repository(app *Context) (*git.Repo, error) {
	repo, err := git.Open(app.Ctx, app.CLI.Dir)
	if err == nil {
		return repo, nil
	}
	return nil, core.Errorf(core.ErrValidation, "configure this directory").
		WithWhy("%s is not inside a git repository", app.CLI.Dir).
		WithFix("git init")
}

// configure builds the configuration from the preset and what flow can find
// out without a network: the repository the remote names, the forge its host
// implies, and the flags.
func (c *InitCmd) configure(app *Context, repo *git.Repo) ([]byte, error) {
	presets, err := assets.Presets()
	if err != nil {
		return nil, core.Errorf(core.ErrNotFound, "read the shipped presets").WithWhy("%s", err)
	}
	source, err := config.PresetSource(presets, c.Preset)
	if err != nil {
		return nil, err
	}

	values, err := c.values(app, repo)
	if err != nil {
		return nil, err
	}
	for _, setting := range values {
		if source, err = config.Set(source, setting.path, setting.value); err != nil {
			return nil, err
		}
	}
	return source, c.validate(source)
}

// ask puts the questions of spec 12 §1 to whoever is running init, and
// returns the configuration they agreed to.
//
// The wizard fills in the flags and nothing else: what it collects goes back
// onto the command and down the same road a command line takes, so the plain
// equivalent spec 12 asks of every screen is not a second implementation that
// has to be kept in step — it is the same one. The second result is whether
// anything was agreed to at all.
//
// --yes is what a person says when they do not want to be asked, and a run
// with nobody watching cannot be asked, so both go straight to the defaults.
func (c *InitCmd) ask(app *Context, repo *git.Repo) ([]byte, bool, error) {
	if c.Yes || !app.tui() {
		source, err := c.configure(app, repo)
		return source, err == nil, err
	}

	var s session
	if err := app.settings(&s, repo); err != nil {
		return nil, false, err
	}
	screen := app.renderer(s.Settings).Wizard(c.detected(app, repo), func(a ui.Answers) ([]byte, error) {
		asked := *c
		asked.fill(a)
		return asked.configure(app, repo)
	})
	answered, err := onScreen(app, "ask how to configure this repository", screen)
	switch {
	case err != nil:
		return nil, false, err
	case !answered.Confirmed():
		return nil, false, nil
	}

	c.fill(answered.Answers())
	source, err := c.configure(app, repo)
	return source, err == nil, err
}

// detected is where the wizard starts and what its command line is
// shortened against: the flags somebody already passed, the forge the remote
// names, and the preset's own answer to anything still blank.
//
// It is the file flow would have written had nobody been asked, which is what
// makes every question open on that answer and makes the review's `flow init`
// hold only the flags that changed something.
//
// A forge flow cannot work out is a question here rather than the refusal it
// is on the command line (A02): the whole point of the screen is that there
// is somebody to ask.
func (c *InitCmd) detected(app *Context, repo *git.Repo) ui.Answers {
	answers := ui.Answers{
		Preset:     c.Preset,
		Forge:      c.Forge,
		Tracker:    c.Tracker,
		TrackerURL: c.TrackerURL,
		Owner:      c.Owner,
		Project:    c.Project,
	}
	if kind, _, _, err := c.detect(app, repo); err == nil {
		answers.Forge = kind
	}
	return c.presetBlanks(answers)
}

// presetBlanks fills in the answers the preset already gives, so that a
// question opens on what flow would have written rather than on the first of
// its options. A preset flow cannot read leaves them blank, which is the same
// thing a flag nobody passed means.
func (c *InitCmd) presetBlanks(answers ui.Answers) ui.Answers {
	presets, err := assets.Presets()
	if err != nil {
		return answers
	}
	source, err := config.PresetSource(presets, answers.Preset)
	if err != nil {
		return answers
	}
	cfg, err := config.Parse(source, configFile)
	if err != nil {
		return answers
	}
	for _, blank := range []struct {
		into *string
		from string
	}{
		{&answers.Forge, string(cfg.Forge.Type)},
		{&answers.Tracker, string(cfg.Tracker.Type)},
	} {
		if *blank.into == "" {
			*blank.into = blank.from
		}
	}
	return answers
}

// fill puts a wizard's answers back on the command, which is what makes them
// the same answers a command line gives.
func (c *InitCmd) fill(a ui.Answers) {
	c.Preset = a.Preset
	c.Forge = a.Forge
	c.Tracker = a.Tracker
	c.TrackerURL = a.TrackerURL
	c.Owner = a.Owner
	c.Project = a.Project
}

// setting is one value init fills into the preset.
type setting struct{ path, value string }

// values is every key init fills in, in the order it applies them. Keys it has
// no value for are left out, which is how `${pending:project}` survives into
// the written file (A01) and how a repository with no remote keeps the
// preset's own placeholder owner.
func (c *InitCmd) values(app *Context, repo *git.Repo) ([]setting, error) {
	kind, slug, url, err := c.detect(app, repo)
	if err != nil {
		return nil, err
	}
	var values []setting
	for _, s := range []setting{
		{"forge.type", kind},
		{"forge.repo", slug},
		{"forge.url", url},
		{"tracker.type", c.Tracker},
		{"tracker.owner", c.Owner},
		{"tracker.url", c.TrackerURL},
		{"tracker.project", c.Project},
	} {
		if s.value != "" {
			values = append(values, s)
		}
	}
	return values, nil
}

// detect works out the forge from the origin remote, which is everything flow
// can learn about a repository without a network. A flag always wins: the
// person running init knows what they are pointing at, and the remote is only
// evidence.
func (c *InitCmd) detect(app *Context, repo *git.Repo) (kind, slug, url string, err error) {
	kind = c.Forge

	origin, err := repo.Remote(app.Ctx, "origin")
	switch {
	case errors.Is(err, core.ErrNotFound):
		app.Log.Info("no origin remote; leaving the forge for you to fill in")
		return kind, "", "", nil
	case err != nil:
		return "", "", "", err
	case origin.Local():
		app.Log.Info("origin is a path on this machine; leaving the forge for you to fill in", "url", origin.URL)
		return kind, "", "", nil
	}

	// The URL is rebuilt from the host rather than copied: a clone URL may
	// carry a credential and names a protocol, and `forge.url` is the web
	// address of the forge (spec 03 §1).
	slug, url = origin.Slug(), "https://"+origin.Host
	if kind != "" {
		return kind, slug, url, nil
	}
	if detected, ok := forgeKindFor(origin.Host); ok {
		return detected, slug, url, nil
	}
	return "", "", "", core.Errorf(core.ErrNeedsInput, "detect the forge at %s", origin.Host).
		WithWhy("flow knows that host by probing it, and init works offline").
		WithFix("say which it is: flow init --forge github|gitea|gitlab")
}

// forgeKindFor maps a host to the forge it runs, for the hosts that can be
// known without asking. Every other host is a probe (spec 10 §1), which needs
// a network init is not allowed to need, so flow asks the person instead.
func forgeKindFor(host string) (string, bool) {
	switch host {
	case "github.com", "www.github.com":
		return "github", true
	case "gitlab.com":
		return "gitlab", true
	case "codeberg.org":
		return "gitea", true
	}
	return "", false
}

// validate parses and checks the configuration before it is written, so init
// can never leave behind a file flow itself refuses to read.
func (c *InitCmd) validate(source []byte) error {
	cfg, err := config.Parse(source, configFile)
	if err != nil {
		return err
	}
	capabilities, err := assets.Capabilities()
	if err != nil {
		return core.Errorf(core.ErrNotFound, "read the capability declarations").WithWhy("%s", err)
	}
	registry, err := core.LoadRegistry(capabilities)
	if err != nil {
		return err
	}
	return config.Validate(cfg, configFile, registry)
}

// write puts the configuration and the templates in the working tree.
func (c *InitCmd) write(app *Context, root string, source []byte) ([]initFile, error) {
	written, err := c.writeFile(root, configFile, source, true)
	if err != nil {
		return nil, err
	}
	files := []initFile{written}

	if c.NoTemplates {
		return files, nil
	}
	templates, err := assets.Templates()
	if err != nil {
		return nil, core.Errorf(core.ErrNotFound, "read the shipped templates").WithWhy("%s", err)
	}
	for _, t := range repoTemplates {
		content, err := fs.ReadFile(templates, t.asset)
		if err != nil {
			return nil, core.Errorf(core.ErrNotFound, "read the %s template", t.asset).WithWhy("%s", err)
		}
		// A template is scaffolding, not configuration: flow writes it once
		// and never again, because the version in the repository is the one
		// the team edited. --force is about .flow.yaml, not about reverting
		// somebody's pull request template.
		file, err := c.writeFile(root, t.path, content, false)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// repoTemplates are the files init scaffolds beside the configuration
// (spec 02 §2).
var repoTemplates = []struct{ asset, path string }{
	{asset: "ci/github-flow.yml", path: ".github/workflows/flow.yml"},
	{asset: "pull_request_template.md", path: ".github/pull_request_template.md"},
	{asset: "issue-forms/story.yml", path: ".github/ISSUE_TEMPLATE/story.yml"},
}

// writeFile writes one file, reporting what it did. An existing file is kept
// unless overwrite says otherwise, and a file whose contents already match is
// kept either way so that re-running init is not a diff.
func (c *InitCmd) writeFile(root, rel string, content []byte, overwrite bool) (initFile, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	action := "written"
	switch existing, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return initFile{}, core.Errorf(core.ErrValidation, "read %s", rel).
			WithWhy("%v", err).
			WithFix("check the file's permissions")
	case string(existing) == string(content):
		return initFile{Path: rel, Action: "kept"}, nil
	case !overwrite:
		return initFile{Path: rel, Action: "kept"}, nil
	default:
		action = "overwritten"
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return initFile{}, core.Errorf(core.ErrValidation, "create %s", filepath.Dir(rel)).
			WithWhy("%v", err).
			WithFix("check the directory's permissions")
	}
	if err := writeRegular(path, content); err != nil {
		return initFile{}, err
	}
	return initFile{Path: rel, Action: action}, nil
}

// writeRegular writes through a temporary file in the same directory, so that
// an interrupted init leaves the old file rather than half of a new one.
func writeRegular(path string, content []byte) error {
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
	if _, err := temp.Write(content); err != nil {
		return fail(err)
	}
	if err := temp.Chmod(0o644); err != nil {
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

// installHook installs the commit-msg hook, unless the person said not to.
//
// A hook is a convenience and the gate on the pull request is the authority
// (spec 08), so nothing here can fail init: a repository with a hook manager
// in charge, or with somebody else's commit-msg hook, is reported and left
// alone rather than turned into a failed setup.
func (c *InitCmd) installHook(app *Context) (string, error) {
	if c.NoHooks {
		return "skipped", nil
	}
	repo, err := git.Open(app.Ctx, app.CLI.Dir)
	if err != nil {
		return "", err
	}
	managers, err := detectHookManagers(app, repo)
	if err != nil {
		return "", err
	}
	if len(managers) > 0 {
		return "deferred", nil
	}
	path, err := repo.HooksPath(app.Ctx)
	if err != nil {
		return "", err
	}
	script, err := hookScript()
	if err != nil {
		return "", err
	}
	action, err := installHook(filepath.Join(path, hookName), script, false)
	if err != nil {
		app.Log.Info("leaving the commit-msg hook alone", "why", err)
		return "deferred", nil
	}
	return action, nil
}

// report says what happened, as JSON or as the lines a person reads next.
func (c *InitCmd) report(app *Context, root string, source []byte, files []initFile, hook string) error {
	pending, err := pendingNames(source)
	if err != nil {
		return err
	}
	if app.CLI.JSON {
		return core.WriteJSON(app.Stdout, initReport{
			Schema:  core.SchemaID("init"),
			Root:    root,
			Preset:  c.Preset,
			Files:   files,
			Pending: pending,
			Hook:    hook,
		})
	}
	for _, file := range files {
		fmt.Fprintf(app.Stdout, "%s %s\n", file.Action, file.Path)
	}
	fmt.Fprintf(app.Stdout, "commit-msg hook %s\n", hook)
	if len(pending) > 0 {
		fmt.Fprintf(app.Stdout, "\nStill to find out: %s\n", strings.Join(pending, ", "))
		fmt.Fprintf(app.Stdout, "Next: flow doctor --fix\n")
		return nil
	}
	fmt.Fprintf(app.Stdout, "\nNext: flow plan\n")
	return nil
}

// pendingNames lists the placeholders left in the configuration, so the last
// line init prints is the thing the person has to do next (spec 03 §2).
func pendingNames(source []byte) ([]string, error) {
	cfg, err := config.Parse(source, configFile)
	if err != nil {
		return nil, err
	}
	var names []string
	if cfg.Tracker.Project.IsPending() {
		names = append(names, "tracker.project")
	}
	return names, nil
}
