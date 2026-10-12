// Command flow makes a lean, trunk-based delivery workflow true and provably
// true across a Git forge and a ticket tracker. See .ralph/specs for the
// contract this implementation follows.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/17twenty/flow/internal/core"
	"github.com/alecthomas/kong"
)

// version is the released version, injected at build time with
// -ldflags "-X main.version=vX.Y.Z". Unreleased builds report "dev" and fall
// back to the VCS data in the embedded build info.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// exitPanic unwinds kong's internal os.Exit calls (help, parse errors) so that
// run returns an exit code instead of terminating the process itself.
type exitPanic int

// run parses args, executes the selected command and returns its exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	var cli CLI

	opts := []kong.Option{
		kong.Name("flow"),
		kong.Description("Make a lean, trunk-based workflow true and provably true."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(c int) { panic(exitPanic(c)) }),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		helpGroups,
	}
	parser, err := kong.New(&cli, append(opts, testCommands()...)...)
	if err != nil {
		fmt.Fprintf(stderr, "error: build the command line: %v\n", err)
		return core.ExitFail
	}

	defer func() {
		if r := recover(); r != nil {
			ep, ok := r.(exitPanic)
			if !ok {
				panic(r)
			}
			code = int(ep)
		}
	}()

	kctx, err := parser.Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", core.Redact(err.Error()))
		fmt.Fprintln(stderr, "fix:   run flow --help")
		return core.ExitUsage
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runCtx := sigCtx
	if cli.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(sigCtx, cli.Timeout)
		defer cancel()
	}

	app := &Context{
		Ctx:    runCtx,
		CLI:    &cli,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Log:    cli.newLogger(stderr),
	}
	err = kctx.Run(app)

	// An interrupt outranks whatever error the command reported on its way out.
	if sigCtx.Err() != nil {
		fmt.Fprintln(stderr, "interrupted")
		return core.ExitInterrupted
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			printError(stderr, core.Errorf(core.ErrUnavailable, "finish within %s", cli.Timeout).
				WithWhy("the overall deadline expired").
				WithFix("raise --timeout, or narrow the work with --check or --only"))
			return core.ExitUnavailable
		}
		printError(stderr, err)
		return core.ExitCode(err)
	}
	// A command that succeeded may still have left work for a human, which
	// is exit 2 and not a failure (spec 02 §4). It has already said what, on
	// stdout, in its own words.
	return app.Code
}

// printError renders err as the what/why/fix lines of principle P10, with any
// credential scrubbed out first.
func printError(stderr io.Writer, err error) {
	var ue *core.UserError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "error: could not %s\n", core.Redact(ue.What))
		if ue.Why != "" {
			fmt.Fprintf(stderr, "why:   %s\n", core.Redact(ue.Why))
		}
		if ue.Fix != "" {
			fmt.Fprintf(stderr, "fix:   %s\n", core.Redact(ue.Fix))
		}
		return
	}
	fmt.Fprintf(stderr, "error: %s\n", core.Redact(err.Error()))
}
