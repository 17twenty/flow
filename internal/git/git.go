// Package git answers questions about a working tree by running the git
// command line.
//
// flow shells out rather than linking a Git implementation. git is already
// installed wherever flow runs; it is the only implementation that agrees with
// itself about configuration, hooks and trailers; and spec 01 §4 bans go-git.
// Every question flow asks is read-only, so nothing here changes a repository.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// Repo is a git working tree flow can ask questions of. Its methods run git in
// the tree's root, so they answer for the repository rather than for whatever
// directory the process happens to be in.
type Repo struct {
	dir string
}

// Open returns the working tree containing dir. An empty dir means the
// process's working directory.
func Open(ctx context.Context, dir string) (*Repo, error) {
	root, err := Root(ctx, dir)
	if err != nil {
		return nil, err
	}
	return &Repo{dir: root}, nil
}

// Dir returns the absolute path of the working tree root.
func (r *Repo) Dir() string { return r.dir }

// Root returns the root of the working tree containing dir, as git resolves it:
// an absolute path with symbolic links already followed.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := output(ctx, dir, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", core.Errorf(core.ErrUnsupported, "find the repository root").
			WithWhy("this is a bare repository, which has no working tree").
			WithFix("run flow from a clone that has a working tree")
	}
	return filepath.Clean(root), nil
}

// Branch returns the name of the checked-out branch. A detached HEAD — which is
// how most CI checkouts arrive — is not an error: the branch is reported empty.
func (r *Repo) Branch(ctx context.Context) (string, error) {
	res, err := run(ctx, r.dir, "", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	if res.code == 1 {
		return "", nil
	}
	if res.code != 0 {
		return "", failure(res, "symbolic-ref")
	}
	return strings.TrimSpace(res.stdout), nil
}

// ConfigValue returns the value of a git configuration key and whether it is
// set at all. Being unset is an answer, not a failure.
func (r *Repo) ConfigValue(ctx context.Context, key string) (string, bool, error) {
	res, err := run(ctx, r.dir, "", "config", "--get", key)
	if err != nil {
		return "", false, err
	}
	switch res.code {
	case 0:
		return strings.TrimRight(res.stdout, "\n"), true, nil
	case 1:
		return "", false, nil
	default:
		return "", false, failure(res, "config")
	}
}

// HasCommits reports whether HEAD names a commit. A repository that has been
// initialised but never committed to answers false, which is a state flow
// meets whenever it is run on a brand new project.
func (r *Repo) HasCommits(ctx context.Context) (bool, error) {
	res, err := run(ctx, r.dir, "", "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return false, err
	}
	return res.code == 0, nil
}

// outcome is what one git invocation said and how it ended.
type outcome struct {
	stdout string
	stderr string
	code   int
}

// run executes git in dir with stdin as its input. A non-zero exit is reported
// in the outcome rather than as an error, because some questions are answered
// by git's exit status. An error means git could not be run at all, or the
// context ended first.
func run(ctx context.Context, dir, stdin string, args ...string) (outcome, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// GIT_OPTIONAL_LOCKS stops a question from taking the index lock, so flow
	// can never block a developer's own git. GIT_TERMINAL_PROMPT stops git
	// stopping to ask for a password where nobody is watching.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrNotFound):
		return outcome{}, core.Errorf(core.ErrUnavailable, "run git").
			WithWhy("git is not installed, or not on PATH").
			WithFix("install git, then run this again")
	case ctx.Err() != nil:
		return outcome{}, core.Errorf(core.ErrUnavailable, "run git %s", args[0]).
			WithWhy("%s", ctx.Err())
	default:
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return outcome{}, core.Errorf(core.ErrUnavailable, "run git %s", args[0]).
				WithWhy("%s", core.Redact(err.Error()))
		}
	}
	return outcome{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}, nil
}

// output runs git and treats any non-zero exit as a failure, which is what
// most questions want.
func output(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	res, err := run(ctx, dir, stdin, args...)
	if err != nil {
		return "", err
	}
	if res.code != 0 {
		return "", failure(res, args[0])
	}
	return res.stdout, nil
}

// failure explains a git command that ran and refused, in git's own words.
func failure(res outcome, subcommand string) error {
	why := core.Redact(firstLine(res.stderr))
	if why == "" {
		why = fmt.Sprintf("git %s exited %d without saying why", subcommand, res.code)
	}
	if strings.Contains(strings.ToLower(res.stderr), "not a git repository") {
		return core.Errorf(core.ErrNotFound, "find a git repository").
			WithWhy("%s", why).
			WithFix("run flow inside a clone, or run git init to start one")
	}
	return core.Errorf(core.ErrUnavailable, "run git %s", subcommand).WithWhy("%s", why)
}

// firstLine returns the first non-empty line of s, trimmed, with git's "fatal:"
// and "error:" prefixes removed: they say nothing the what/why/fix shape has
// not already said.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, prefix := range []string{"fatal: ", "error: ", "warning: "} {
			line = strings.TrimPrefix(line, prefix)
		}
		return line
	}
	return ""
}
