package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/deploys"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/forge"
	"github.com/17twenty/flow/internal/git"
	"github.com/17twenty/flow/internal/tracker"
	"github.com/17twenty/flow/internal/ui"
	"github.com/alecthomas/kong"
	"golang.org/x/term"
)

// CLI is flow's command tree and global flags (spec 02 §1–2).
type CLI struct {
	Dir            string   `short:"C" name:"dir" env:"FLOW_DIR" default:"." type:"path" help:"Repo directory; flow walks up to the git root."`
	Config         string   `name:"config" env:"FLOW_CONFIG" type:"path" placeholder:"PATH" help:"Team config (default <root>/.flow.yaml)."`
	UserConfig     string   `name:"user-config" env:"FLOW_USER_CONFIG" type:"path" placeholder:"PATH" help:"User config (default $XDG_CONFIG_HOME/flow/config.toml)."`
	JSON           bool     `name:"json" env:"FLOW_JSON" help:"Emit one JSON document on stdout; no TUI, no ANSI."`
	NonInteractive bool     `name:"non-interactive" env:"FLOW_NON_INTERACTIVE" help:"Never prompt. Automatic when stdout is not a TTY or CI is set."`
	LogLevel       string   `name:"log-level" env:"FLOW_LOG_LEVEL" default:"warn" enum:"debug,info,warn,error" help:"Log level on stderr: debug, info, warn, error."`
	LogFormat      string   `name:"log-format" env:"FLOW_LOG_FORMAT" default:"text" enum:"text,json" help:"Log format on stderr: text or json."`
	NoColor        bool     `name:"no-color" env:"FLOW_NO_COLOR" help:"Disable ANSI colour. NO_COLOR is honoured too."`
	ASCII          bool     `name:"ascii" env:"FLOW_ASCII" help:"Use ASCII glyphs instead of Unicode."`
	Timeout        Duration `name:"timeout" env:"FLOW_TIMEOUT" default:"2m" help:"Overall deadline for the command."`

	Init   InitCmd   `cmd:"" group:"setup" help:"Write .flow.yaml and supporting files."`
	Plan   PlanCmd   `cmd:"" group:"setup" help:"Show what flow would configure. Read-only."`
	Apply  ApplyCmd  `cmd:"" group:"setup" help:"Converge forge and tracker on the desired state."`
	Attest AttestCmd `cmd:"" group:"setup" help:"Record a human confirmation for a check flow cannot verify."`

	Doctor DoctorCmd `cmd:"" group:"verify" help:"Prove the setup is intact and the workflow is behaving."`

	Check CheckCmd `cmd:"" group:"ci" help:"CI entry points: pr, deploy, commit."`

	Hook    HookCmd    `cmd:"" group:"meta" help:"Manage the optional local commit-msg hook."`
	Schema  SchemaCmd  `cmd:"" group:"meta" help:"Print a JSON Schema for a flow document."`
	Version VersionCmd `cmd:"" group:"meta" help:"Print version and build information."`
}

// Duration is a span of time as a flag takes it: Go's own syntax, plus `d`
// for a day and `w` for a week, which is exactly what flow's configuration
// takes (spec 03 §1).
//
// Somebody who writes `max_age: 7d` in `.flow.yaml` and `--window 7d` on the
// command line is writing the same thing, and a tool that understood one and
// not the other would be two tools wearing one name.
type Duration time.Duration

// Decode reads the flag's value. kong looks for this method on a field's type
// before reaching for any mapper of its own, so naming the type is the whole
// of the wiring — including for a `default:` tag, which is parsed the same
// way as something typed.
func (d *Duration) Decode(ctx *kong.DecodeContext) error {
	token, err := ctx.Scan.PopValue("duration")
	if err != nil {
		return err
	}
	text, ok := token.Value.(string)
	if !ok {
		text = fmt.Sprint(token.Value)
	}
	parsed, err := config.ParseDuration(text)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// Std is the span as the standard library counts it.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String writes the span the way somebody would have typed it, so that an
// error about a deadline names the deadline they set.
func (d Duration) String() string { return config.Duration(d).String() }

// helpGroups titles the help sections. Spec 02 §2 fixes these four names.
var helpGroups = kong.Groups{
	"setup":  "Setup",
	"verify": "Verify",
	"ci":     "CI",
	"meta":   "Meta",
}

// logLevel maps the --log-level flag to its slog level.
func (c *CLI) logLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// newLogger builds the stderr logger. Every record passes through the redacting
// handler, so no credential can reach the terminal (spec 15 §2).
func (c *CLI) newLogger(stderr io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.logLevel()}
	var inner slog.Handler
	if strings.ToLower(c.LogFormat) == "json" {
		inner = slog.NewJSONHandler(stderr, opts)
	} else {
		inner = slog.NewTextHandler(stderr, opts)
	}
	return slog.New(core.NewRedactingHandler(inner))
}

// Context is the per-invocation state bound into every command's Run method.
type Context struct {
	// Ctx carries the deadline and cancels on SIGINT or SIGTERM.
	Ctx context.Context
	// CLI holds the parsed global flags.
	CLI *CLI
	// Stdin is the message source for commands that read one, such as
	// check commit with no file argument.
	Stdin io.Reader
	// Stdout receives human and JSON output.
	Stdout io.Writer
	// Stderr receives logs and error explanations.
	Stderr io.Writer
	// Log writes to Stderr through the redacting handler.
	Log *slog.Logger
	// Code is the exit code the command asks for when it succeeded but left
	// work outstanding (spec 02 §4). Zero means it did everything asked.
	Code int
	// CI is set by a command told it is running as a CI job (`doctor --ci`).
	// It is the one place that is asked: what is printed goes into a log read
	// later by somebody who was not there, the findings worth interrupting
	// anybody for go to the job's own interface, and nothing flow learns is
	// worth writing into a checkout that is about to be deleted.
	CI bool
}

// exit records the code this command's own result asks for (spec 02 §4).
//
// It is not an error. A plan that found work to do has answered the question
// it was asked, and printing an `error:` line over the top of a plan somebody
// asked to see would be flow calling its own answer a failure.
func (a *Context) exit(code int) { a.Code = code }

// session is the repository, the configuration and the providers that one
// command acts through. It is what spec 01 §1 calls the adapter registry
// wiring: everything below the root is handed what it needs, and nothing
// below the root goes looking for a credential, a file or a network.
type session struct {
	// Repo is the working tree, and Root its absolute path.
	Repo *git.Repo
	Root string
	// ConfigPath is where `.flow.yaml` was read from, Source its bytes,
	// Config what it said, and ConfigHash the hash a plan is pinned to
	// (spec 06 §3).
	ConfigPath string
	Source     []byte
	Config     *config.Config
	ConfigHash string
	// ConfigErr is why the configuration did not parse and SemanticsErr why
	// it does not hang together, for the one caller that wants to carry on
	// anyway. Every command but doctor gets these as an error from [open];
	// doctor reports them as `config.syntax` and `config.semantics`, because
	// a report saying which part is wrong is worth more than a refusal to
	// start (spec 07 §5).
	ConfigErr    error
	SemanticsErr error
	// Settings are the personal preferences after every layer has had its
	// say, and SettingsPath is where the user's own file lives (spec 03 §4).
	Settings     config.Settings
	SettingsPath string
	// LockPath is where `.flow.lock` lives and Lock what it holds. A
	// repository with no lock yet gets a new one rather than an error.
	LockPath string
	Lock     *config.Lock
	// Registry is every capability declaration flow ships (spec 05 §1).
	Registry core.Registry
	// Desired is what the configuration asks of the providers (spec 06 §1).
	Desired engine.Desired
	// Adapters are the providers to read and write through.
	Adapters engine.Adapters
	// Clock is where times written to the lock come from.
	Clock core.Clock
}

// open loads everything a command needs to plan or apply: the repository, the
// configuration, the capability declarations, the lock and the desired state.
//
// It stops short of the providers. Nothing here needs a credential, and
// comparison mode (spec 05 §6) never has one: asking for a token before
// reading two declarations off the disk would make an offline question fail
// for want of a secret it is not going to send anywhere.
func (a *Context) open() (*session, error) {
	s, err := a.inspect()
	if err != nil {
		return nil, err
	}
	if s.ConfigErr != nil {
		return nil, s.ConfigErr
	}
	if s.SemanticsErr != nil {
		return nil, s.SemanticsErr
	}
	return s, nil
}

// inspect is [open] without the refusal: a configuration that did not parse
// or does not hang together is recorded on the session rather than returned,
// and loading stops at the first thing that genuinely cannot be done without
// it.
//
// Only doctor uses this. Every other command wants the error, because it
// cannot do its job with a workflow nobody can read — but doctor's job is to
// say which part is wrong, and it cannot say that if it will not start.
func (a *Context) inspect() (*session, error) {
	repo, err := git.Open(a.Ctx, a.CLI.Dir)
	if err != nil {
		return nil, core.Errorf(core.ErrValidation, "read this repository").
			WithWhy("%s is not inside a git repository", a.CLI.Dir).
			WithFix("run flow from inside a repository, or pass -C <dir>")
	}

	s := &session{Repo: repo, Root: repo.Dir(), Clock: core.SystemClock{}}
	s.ConfigPath = a.CLI.Config
	if s.ConfigPath == "" {
		s.ConfigPath = filepath.Join(s.Root, config.DefaultPath)
	}
	source, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return nil, core.Errorf(core.ErrNotFound, "read %s", s.ConfigPath).
			WithWhy("%v", err).
			WithFix("run flow init to write one")
	}
	s.Source = source
	s.ConfigHash = config.Hash(source)
	if s.Config, err = config.Parse(source, s.ConfigPath); err != nil {
		s.ConfigErr = err
	}

	declarations, err := assets.Capabilities()
	if err != nil {
		return nil, fmt.Errorf("read the capability declarations: %w", err)
	}
	if s.Registry, err = core.LoadRegistry(declarations); err != nil {
		return nil, err
	}
	if s.ConfigErr != nil {
		// There is nothing further to load: every remaining step reads the
		// configuration, and a session half-built from a file that did not
		// parse would answer questions with made-up defaults.
		return s, nil
	}
	if err := config.Validate(s.Config, s.ConfigPath, s.Registry); err != nil {
		s.SemanticsErr = err
	}

	if err := a.settings(s, repo); err != nil {
		return nil, err
	}

	s.LockPath = filepath.Join(s.Root, config.LockPath)
	// A repository that has never been applied has no lock, which is the
	// state flow starts in rather than an error: the first plan is exactly
	// the one nobody has a lock for.
	if s.Lock, err = config.LoadLock(s.LockPath); err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			return nil, err
		}
		s.Lock = config.NewLock(version)
	}

	if s.SemanticsErr != nil {
		// The lock and the personal settings are loaded above and are worth
		// having — doctor reports on both. The desired state is not: it is
		// built out of the parts of the configuration that were just found
		// not to hang together.
		return s, nil
	}

	templates, err := assets.Templates()
	if err != nil {
		return nil, fmt.Errorf("read the templates: %w", err)
	}
	if s.Desired, err = engine.NewDesired(s.Config, templates); err != nil {
		return nil, err
	}
	return s, nil
}

// connect builds the providers this session acts through. A credential is
// resolved here because an adapter needs one; the first request is still the
// caller's.
func (s *session) connect(a *Context) error {
	adapters, err := newAdapters(a, s)
	if err != nil {
		return err
	}
	s.Adapters = adapters
	return nil
}

// settings resolves the personal layers onto the session: the user's own
// TOML, then the repository-local override, which is left unread if it is
// tracked (spec 03 §4). The path to the user's own file is kept, because it
// is where flow sends anybody whose configuration needs an answer adding to
// it.
//
// A layer flow would not read is a warning on stderr rather than a refusal,
// so it is said once, to the person, and nothing downstream has to decide
// what to do about it.
func (a *Context) settings(s *session, repo *git.Repo) error {
	s.SettingsPath = a.userConfigPath()
	tracked := func(path string) (bool, error) { return repo.Tracked(a.Ctx, path) }
	layers, warnings, err := config.LoadPersonal(s.SettingsPath, filepath.Join(repo.Dir(), config.LocalConfigName), tracked)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		a.Log.Warn(warning)
	}
	s.Settings = config.Resolve(layers...)
	return nil
}

// userConfigPath is where this invocation reads personal settings from: the
// flag when it was given, and otherwise wherever this machine keeps them.
func (a *Context) userConfigPath() string {
	if a.CLI.UserConfig != "" {
		return a.CLI.UserConfig
	}
	return config.UserConfigPath(os.Getenv)
}

// saveLock writes the lock where it belongs, atomically.
func (s *session) saveLock(lock *config.Lock) error { return config.WriteLock(s.LockPath, lock) }

// plan looks at the providers and decides what flow would do about what it
// found (spec 06 §2). Observing is the one part that touches a network; the
// plan itself is a pure function of what came back, which is what lets
// `flow apply --plan` re-decide later and compare the two.
func (s *session) plan(a *Context, only, targets []string) (engine.Plan, error) {
	providers, err := s.Adapters.Providers(a.Ctx)
	if err != nil {
		return engine.Plan{}, err
	}
	observations, err := engine.Observe(a.Ctx, s.Desired, s.Adapters)
	if err != nil {
		return engine.Plan{}, err
	}
	return engine.NewPlan(engine.PlanInput{
		Config:       s.Config,
		Desired:      s.Desired,
		Observations: observations,
		Providers:    providers,
		Lock:         s.Lock,
		ConfigHash:   s.ConfigHash,
		FlowVersion:  version,
		Now:          s.Clock.Now(),
		Only:         only,
		Targets:      targets,
	})
}

// capabilityNames is every capability this repository's providers declare, in
// order and without repeats, which is what `--only` may name.
func (s *session) capabilityNames() []string {
	var names []string
	for _, kind := range []string{s.Config.Forge.Type, s.Config.Tracker.Type, s.Config.Deploys.Source} {
		declaration, err := s.Registry.Of(kind)
		if err != nil {
			continue
		}
		for _, name := range declaration.Names() {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// render writes one of the engine's documents to stdout, in the shape this
// invocation asked for. Every command renders through here so that --json,
// --no-color and --ascii mean the same thing whatever is being printed
// (spec 02 §1, §3).
func (a *Context) render(s *session, document func(ui.Renderer) (string, error)) error {
	text, err := document(a.renderer(s.Settings))
	if err != nil {
		return err
	}
	return ui.Fprint(a.Stdout, text)
}

// renderer builds the renderer for this invocation from the flags, the
// environment and the user's own configuration (spec 02 §1, spec 12).
func (a *Context) renderer(settings config.Settings) ui.Renderer {
	return ui.New(a.options(settings, terminalCheck(a.Stdout)))
}

// options is the rendering this invocation asked for, given whether what it
// writes to is a terminal. That last part is a parameter rather than a
// question asked here, because a test cannot be a terminal and a decision
// nothing can exercise is a decision nothing is holding flow to.
func (a *Context) options(settings config.Settings, terminal bool) ui.Options {
	opts := ui.Options{
		Format: ui.FormatText,
		Colour: !a.CI && !a.CLI.NoColor && !settings.NoColor && os.Getenv("NO_COLOR") == "" && terminal,
		ASCII:  a.CLI.ASCII || settings.ASCII,
	}
	if a.CLI.JSON {
		opts.Format, opts.Colour = ui.FormatJSON, false
	}
	if opts.Colour {
		opts.Dark = settings.Theme != config.ThemeLight
	}
	return opts
}

// keepsWhatItLearns reports whether this run may write what it found into the
// working tree it is standing in.
//
// A CI runner's checkout is deleted when the job ends, so a file written
// there is a file nobody will ever read, and what flow learned has to reach
// the trunk another way (see [DoctorCmd.proposeLock]).
func (a *Context) keepsWhatItLearns() bool { return !a.CI }

// isTerminal reports whether a stream flow was handed is a terminal, which is
// what decides colour and whether a question may be asked at all.
//
// It takes any, because the answer is the same question of a reader and of a
// writer — is there a person on the other end of this file — and two
// functions differing only in a parameter type would be two places to get it
// wrong.
func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// terminalCheck is how flow asks whether there is a person on the other end
// of a stream. It is a variable for the same reason [newAdapters] is one: a
// test is never a terminal, so a question asked of the real world can only
// ever be answered no, and the half of every one of these rules that matters
// is the half that happens when both ends really are terminals.
//
// The shipped binary only ever holds [isTerminal].
var terminalCheck = isTerminal

// interactive reports whether flow may ask a question: not with
// --non-interactive, not in CI, and not when stdout is not a terminal
// (spec 02 §1).
func (a *Context) interactive() bool { return a.mayAsk(terminalCheck(a.Stdout)) }

// mayAsk is that same decision with the terminal as a parameter, for the same
// reason [Context.options] takes one: a test is never a terminal, so a rule
// asked of the real world can only ever be answered no, and the half of it
// that matters is the half where somebody really is watching.
func (a *Context) mayAsk(terminal bool) bool {
	if a.CLI.NonInteractive || os.Getenv("CI") != "" {
		return false
	}
	return terminal
}

// tui reports whether flow may draw a screen rather than print a document
// (spec 12): both ends have to be a terminal, --json is a document for a
// machine, and a terminal calling itself dumb has said it cannot do this.
//
// It is strictly narrower than [Context.interactive]: everything flow draws
// has a plain equivalent, so the question is only ever which of the two to
// use and never whether the command can go on.
func (a *Context) tui() bool {
	if a.CLI.JSON || !a.interactive() {
		return false
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	return terminalCheck(a.Stdin)
}

// confirm asks a yes/no question and reads the answer, defaulting to no: an
// apply that proceeded because somebody pressed return is the one outcome a
// confirmation exists to prevent.
//
// This is the plain prompt, the plain equivalent spec 12 requires of every
// screen. [Context.onScreen] draws the same question for a terminal that can
// take one; what either of them is asked is decided before both.
func (a *Context) confirm(question string) (bool, error) {
	if _, err := fmt.Fprintf(a.Stdout, "\n%s [y/N] ", question); err != nil {
		return false, fmt.Errorf("ask for confirmation: %w", err)
	}
	answer, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read the answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// onScreen puts a question to a person as the screen spec 12 §4 describes,
// and reports what they answered.
//
// Ctrl-c is one of the screen's own keys, so a person stopping this is a
// refusal and arrives here as one. What is left — a terminal that could not
// be put into raw mode, an output that stopped taking bytes, the run's own
// deadline — is the run failing, and answering "nothing was applied" would
// report the symptom and hide the cause. A model that came back as something
// else cannot be read at all, and the safe reading of an unreadable answer is
// the zero screen: every one of flow's screens reads as "nobody said yes".
//
// It is a function rather than a method because the type of the answer is the
// type of the screen, and a method cannot say so.
func onScreen[M tea.Model](a *Context, what string, screen M) (M, error) {
	var nobody M
	final, err := a.programFor(screen).Run()
	if err != nil {
		return nobody, fmt.Errorf("%s: %w", what, err)
	}
	answered, ok := final.(M)
	if !ok {
		return nobody, nil
	}
	return answered, nil
}

// screenOptions are extra options every screen's program is run with.
//
// It exists for the same reason [terminalCheck] does, and only for tests. A
// test process has no terminal, so bubbletea has no width to draw inside and
// cuts every line it renders to nothing: a test can see that a screen was
// reached but not a word of what it said. A test that wants to read the
// screen hands it a size here. The shipped binary adds nothing, and asks the
// terminal.
var screenOptions []tea.ProgramOption

// programFor builds the bubbletea program a screen runs in, on this
// invocation's streams and deadline.
func (a *Context) programFor(screen tea.Model) *tea.Program {
	options := []tea.ProgramOption{
		tea.WithContext(a.Ctx),
		tea.WithInput(a.Stdin),
		tea.WithOutput(a.Stdout),
	}
	return tea.NewProgram(screen, append(options, screenOptions...)...)
}

// whileWorking draws a screen for as long as some work takes, handing the
// work a way to say where it has got to (spec 12 §3).
//
// The screen goes on its own goroutine because the work stays on this one. A
// bubbletea program is an event loop, so one of the two has to be elsewhere,
// and it is the work that can fail and whose answer the command needs: a
// command that read its report out of a closure would have to decide what a
// screen that never started means.
//
// The error returned is the screen's, never the work's. A terminal that could
// not be drawn on has not stopped the work, and the caller is the one that
// knows whether that matters.
func (a *Context) whileWorking(screen tea.Model, work func(say func(tea.Msg))) error {
	program := a.programFor(screen)
	drawn := make(chan error, 1)
	go func() {
		_, err := program.Run()
		drawn <- err
	}()
	work(program.Send)
	// The screen is told rather than killed, so that it can take itself off
	// the terminal: what it drew is a few lines of this shell session, and
	// the document the command is about to print goes underneath them.
	return <-drawn
}

// chooser is how a placeholder with more than one possible answer gets one
// (spec 07 §3, spec 12 §2). Nil is the answer when there is nobody to ask,
// and the engine reads nil as a rung of the ladder it cannot stand on.
//
// A terminal that cannot draw is not a terminal that cannot be asked, but it
// is this question: the candidates are a list to browse and filter, and the
// plain equivalent is the one the engine already writes — the candidates in
// the report, with the `--set` command that records the answer.
func (a *Context) chooser(s *session) engine.Chooser {
	if !a.tui() {
		return nil
	}
	return func(_ context.Context, p engine.Placeholder, candidates []core.Candidate) (core.Candidate, error) {
		question := fmt.Sprintf("which of these should %s be?", p.Path)
		screen := a.renderer(s.Settings).Picker(question, candidates)
		picked, err := onScreen(a, "ask which to use", screen)
		switch {
		case err != nil:
			return core.Candidate{}, err
		case !picked.Picked():
			// Escape, ctrl-c, or a screen nobody ever answered. None of
			// them is a failure: the placeholder is simply still a
			// question, and resolution falls through to saying so.
			return core.Candidate{}, core.Errorf(core.ErrNeedsInput, "choose %s", p.Path).
				WithWhy("nobody picked one").
				WithFix("run flow doctor --fix --set %s=<the one you meant>", p.Path)
		}
		return picked.Chosen(), nil
	}
}

// adapterBuilder builds the providers a command acts through. It is a
// variable so that the CLI tests can register fakes in its place
// (fakes_flowtest.go); the shipped binary only ever holds [liveAdapters].
type adapterBuilder func(a *Context, s *session) (engine.Adapters, error)

// newAdapters is the registry: one function, replaced wholesale rather than
// per provider, because a run whose forge is real and whose tracker is a fake
// is not a configuration anybody wants to debug.
var newAdapters adapterBuilder = liveAdapters

// liveAdapters builds the real providers named in `.flow.yaml`.
//
// A kind flow has no adapter for is refused here rather than resolved to an
// empty one: spec 05 §1 makes an undeclared capability unsupported, and a
// whole provider silently missing would make a plan that says "nothing to do"
// about a tracker nobody can reach.
func liveAdapters(a *Context, s *session) (engine.Adapters, error) {
	credentials := config.NewCredentials(os.Getenv, s.Settings.Auth)

	var adapters engine.Adapters
	forgeClient, err := a.client(s, credentials, s.Config.Forge.Type, s.Config.Forge.Auth, forgeAPI(s.Config.Forge.URL))
	if err != nil {
		return engine.Adapters{}, err
	}
	owner, name, err := splitRepo(s.Config.Forge.Repo)
	if err != nil {
		return engine.Adapters{}, err
	}
	switch s.Config.Forge.Type {
	case forge.GitHubKind:
		declaration, err := s.Registry.Of(forge.GitHubKind)
		if err != nil {
			return engine.Adapters{}, err
		}
		adapters.Forge, err = forge.NewGitHub(forge.GitHubOptions{
			Client: forgeClient, Owner: owner, Repo: name, Capabilities: declaration,
		})
		if err != nil {
			return engine.Adapters{}, err
		}
	default:
		return engine.Adapters{}, core.Errorf(core.ErrUnsupported, "talk to the %s forge", s.Config.Forge.Type).
			WithWhy("this build ships no adapter for it").
			WithFix("set forge.type to github in %s", s.ConfigPath)
	}

	switch s.Config.Tracker.Type {
	case tracker.GitHubProjectsKind:
		declaration, err := s.Registry.Of(tracker.GitHubProjectsKind)
		if err != nil {
			return engine.Adapters{}, err
		}
		trackerClient, err := a.client(s, credentials, s.Config.Tracker.Type, s.Config.Tracker.Auth, forgeAPI(s.Config.Tracker.URL))
		if err != nil {
			return engine.Adapters{}, err
		}
		adapters.Tracker, err = tracker.NewGitHubProjects(tracker.GitHubProjectsOptions{
			Client:       trackerClient,
			Owner:        s.Config.Tracker.Owner,
			OwnerType:    s.Config.Tracker.OwnerType,
			Number:       projectNumber(s.Config, s.Lock),
			Repo:         s.Config.Forge.Repo,
			Capabilities: declaration,
		})
		if err != nil {
			return engine.Adapters{}, err
		}
	default:
		return engine.Adapters{}, core.Errorf(core.ErrUnsupported, "talk to the %s tracker", s.Config.Tracker.Type).
			WithWhy("this build ships no adapter for it").
			WithFix("set tracker.type to github-projects in %s", s.ConfigPath)
	}

	switch s.Config.Deploys.Source {
	case "", config.DeploySourceNone:
	case config.DeploySourceForge, config.DeploySourceCommitStatus:
		// One declaration covers both sources: what a deployment record is
		// does not change with where it was written (assets/capabilities/
		// deploys.yaml), so the registry is keyed by the kind rather than by
		// the source.
		declaration, err := s.Registry.Of(core.KindDeploys)
		if err != nil {
			return engine.Adapters{}, err
		}
		if s.Config.Deploys.Source == config.DeploySourceCommitStatus {
			adapters.Deploys, err = deploys.NewCommitStatus(deploys.CommitStatusOptions{
				Client:       forgeClient,
				Owner:        owner,
				Repo:         name,
				Environment:  s.Config.Deploys.Environment,
				Contexts:     s.Config.Deploys.Contexts,
				Capabilities: declaration,
			})
		} else {
			adapters.Deploys, err = deploys.NewGitHub(deploys.GitHubOptions{
				Client: forgeClient, Owner: owner, Repo: name, Capabilities: declaration,
			})
		}
		if err != nil {
			return engine.Adapters{}, err
		}
	}
	return adapters, nil
}

// client builds the HTTP client one provider speaks through, carrying its
// credential, its API root and flow's own headers.
//
// Where the credential may go is settled before it is asked for. A
// `token_cmd` that unlocks a vault or asks for a fingerprint should not be
// run to produce a secret flow has already decided it will not send
// (spec 15 §1).
func (a *Context) client(s *session, credentials *config.Credentials, provider string, auth config.Auth, base string) (*core.Client, error) {
	host, err := a.approveHost(s, credentials, provider, base, a.hostApprover(terminalCheck(a.Stdout)))
	if err != nil {
		return nil, err
	}
	credential, err := credentials.Token(a.Ctx, provider, auth)
	if err != nil {
		return nil, err
	}
	return core.NewClient(core.ClientOptions{
		BaseURL: base,
		Token:   credential.Secret(),
		Version: version,
		Header:  forge.GitHubHeader(),
		Logger:  a.Log,
		// What was approved is a host, not one request to it: a redirect
		// off it would take the credential along, so the guard holds every
		// request this client makes to the host the approval was about.
		AllowHost: func(other string) bool { return strings.EqualFold(other, host) },
	})
}

// hostApprover is how flow puts the host question to the person running it,
// and is nil when there is nobody to put it to: `--json` is output for a
// machine, and a run that is not interactive has no one at the other end
// (spec 02 §1).
type hostApprover func(question string) (bool, error)

// hostApprover returns the asker for this invocation, given whether what
// flow is writing to is a terminal.
func (a *Context) hostApprover(terminal bool) hostApprover {
	if a.CLI.JSON || !a.mayAsk(terminal) {
		return nil
	}
	return a.confirm
}

// approveHost decides whether provider's credential may be sent to the host
// base names, and returns that host (spec 15 §1).
//
// The configuration under which flow runs cannot answer this for itself:
// `.flow.yaml` is the repository's and names the URL, while what approves a
// host is flow's own knowledge of where the provider answers or a list only
// the person running it can write. When neither does and ask is nil, the run
// stops at 77 with the edit that would let it through, rather than asking a
// question nobody is there to read (scenario S02).
func (a *Context) approveHost(s *session, credentials *config.Credentials, provider, base string, ask hostApprover) (string, error) {
	host := hostOf(base)
	if host == "" {
		return "", core.Errorf(core.ErrValidation, "work out where the %s API is", provider).
			WithWhy("%q is not a URL flow can address", base).
			WithFix("write the provider's url as https://<host> in %s", s.ConfigPath)
	}
	standard := standardHost(provider)
	if credentials.ApprovedHost(provider, host, standard) {
		return host, nil
	}
	if ask == nil {
		return "", credentials.UnapprovedHost(provider, host, standard, s.SettingsPath)
	}
	agreed, err := ask(config.HostQuestion(provider, host))
	if err != nil {
		return "", err
	}
	if !agreed {
		return "", config.DeclinedHost(provider, host, s.SettingsPath)
	}
	credentials.ApproveHost(provider, host)
	return host, nil
}

// standardHost is where a provider answers when nobody has configured
// anything else, and is empty for a provider flow ships no API host for.
//
// It is flow's own knowledge rather than the repository's, which is the whole
// point of the question: a `.flow.yaml` that could nominate the host it is
// trusted to be sent to would approve itself (spec 15 §1).
func standardHost(provider string) string {
	switch provider {
	case forge.GitHubKind, tracker.GitHubProjectsKind:
		return hostOf(forge.GitHubAPI)
	default:
		return ""
	}
}

// hostOf is the host name a URL addresses, without its port, or "" when the
// text does not address one.
func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// forgeAPI turns a configured base URL into an API root: github.com answers
// at api.github.com, and GitHub Enterprise Server under /api/v3 (spec 10 §2).
func forgeAPI(configured string) string {
	trimmed := strings.TrimSuffix(configured, "/")
	switch {
	case trimmed == "", trimmed == "https://github.com":
		return forge.GitHubAPI
	case strings.HasSuffix(trimmed, "/api/v3"):
		return trimmed
	default:
		return trimmed + "/api/v3"
	}
}

// splitRepo splits an owner/name slug, refusing anything else: an adapter
// built from half a slug fails later, against the network, with an error
// about a repository nobody wrote down.
func splitRepo(slug string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" {
		return "", "", core.Errorf(core.ErrValidation, "read forge.repo").
			WithWhy("%q is not an owner/name slug", slug).
			WithFix("write forge.repo as owner/name")
	}
	return owner, name, nil
}

// projectNumber is the board's number, from the configuration when it is
// known and from the lock when doctor has resolved it. Zero means nobody has
// found it yet, which is what discovery is for rather than an error.
func projectNumber(cfg *config.Config, lock *config.Lock) int {
	text := cfg.Tracker.Project.Text
	if text == "" && lock != nil {
		text, _ = lock.Lookup("tracker.project")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(text, "#"))
	if err != nil {
		return 0
	}
	return number
}
