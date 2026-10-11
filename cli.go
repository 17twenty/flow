package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/alecthomas/kong"
)

// CLI is flow's command tree and global flags (spec 02 §1–2).
type CLI struct {
	Dir            string        `short:"C" name:"dir" env:"FLOW_DIR" default:"." type:"path" help:"Repo directory; flow walks up to the git root."`
	Config         string        `name:"config" env:"FLOW_CONFIG" type:"path" placeholder:"PATH" help:"Team config (default <root>/.flow.yaml)."`
	UserConfig     string        `name:"user-config" env:"FLOW_USER_CONFIG" type:"path" placeholder:"PATH" help:"User config (default $XDG_CONFIG_HOME/flow/config.toml)."`
	JSON           bool          `name:"json" env:"FLOW_JSON" help:"Emit one JSON document on stdout; no TUI, no ANSI."`
	NonInteractive bool          `name:"non-interactive" env:"FLOW_NON_INTERACTIVE" help:"Never prompt. Automatic when stdout is not a TTY or CI is set."`
	LogLevel       string        `name:"log-level" env:"FLOW_LOG_LEVEL" default:"warn" enum:"debug,info,warn,error" help:"Log level on stderr: debug, info, warn, error."`
	LogFormat      string        `name:"log-format" env:"FLOW_LOG_FORMAT" default:"text" enum:"text,json" help:"Log format on stderr: text or json."`
	NoColor        bool          `name:"no-color" env:"FLOW_NO_COLOR" help:"Disable ANSI colour. NO_COLOR is honoured too."`
	ASCII          bool          `name:"ascii" env:"FLOW_ASCII" help:"Use ASCII glyphs instead of Unicode."`
	Timeout        time.Duration `name:"timeout" env:"FLOW_TIMEOUT" default:"2m" help:"Overall deadline for the command."`

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
}

// unimplemented reports that a command exists in the CLI contract but has no
// implementation yet. It is the only stub flow permits, and only until the
// owning plan item lands (plan item 0.1).
func (a *Context) unimplemented(command string) error {
	return core.Errorf(core.ErrUnsupported, "run %s", command).
		WithWhy("this build of flow does not implement %s yet", command).
		WithFix("upgrade flow, or follow the plan item for %s in .ralph/IMPLEMENTATION_PLAN.md", command)
}
