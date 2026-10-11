package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/git"
)

func TestRootFindsTheWorkingTree(t *testing.T) {
	f := newFixture(t)
	f.write("deep/nested/file.txt", "x")

	for _, from := range []string{".", "deep", "deep/nested"} {
		t.Run(from, func(t *testing.T) {
			root, err := git.Root(ctx(t), filepath.Join(f.dir, filepath.FromSlash(from)))
			if err != nil {
				t.Fatalf("Root: %v", err)
			}
			if root != f.dir {
				t.Errorf("root = %q, want %q", root, f.dir)
			}
		})
	}
}

func TestOpenOutsideARepository(t *testing.T) {
	newFixture(t) // only to skip when git is missing
	dir := t.TempDir()
	// A temp dir can sit inside a repository on some machines; a bare .git
	// file would make this ambiguous, so assert on the error only when the
	// directory really is outside one.
	if _, err := git.Root(ctx(t), dir); err == nil {
		t.Skip("the temporary directory is inside a repository")
	} else {
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("error is %v, want it to wrap ErrNotFound", err)
		}
		var user *core.UserError
		if !errors.As(err, &user) || user.Fix == "" {
			t.Errorf("the error does not say how to fix it: %v", err)
		}
	}
}

func TestOpenReportsTheRoot(t *testing.T) {
	f := newFixture(t)
	repo, err := git.Open(ctx(t), f.dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if repo.Dir() != f.dir {
		t.Errorf("Dir() = %q, want %q", repo.Dir(), f.dir)
	}
}

func TestBranch(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): first")
	repo := open(t, f)

	branch, err := repo.Branch(ctx(t))
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}

	// A detached HEAD is how CI checks out a pull request; it is a state, not
	// a failure.
	f.git("checkout", "--quiet", "--detach", "HEAD")
	branch, err = repo.Branch(ctx(t))
	if err != nil {
		t.Fatalf("Branch when detached: %v", err)
	}
	if branch != "" {
		t.Errorf("detached branch = %q, want empty", branch)
	}
}

func TestConfigValue(t *testing.T) {
	f := newFixture(t)
	f.git("config", "flow.testing", "yes")
	repo := open(t, f)

	value, set, err := repo.ConfigValue(ctx(t), "flow.testing")
	if err != nil {
		t.Fatalf("ConfigValue: %v", err)
	}
	if !set || value != "yes" {
		t.Errorf("flow.testing = %q, %v; want \"yes\", true", value, set)
	}

	value, set, err = repo.ConfigValue(ctx(t), "flow.missing")
	if err != nil {
		t.Fatalf("ConfigValue for an unset key: %v", err)
	}
	if set || value != "" {
		t.Errorf("flow.missing = %q, %v; want \"\", false", value, set)
	}
}

func TestCommentChar(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)

	if _, set, err := repo.CommentChar(ctx(t)); err != nil || set {
		t.Errorf("unset core.commentChar reported as set=%v, err=%v", set, err)
	}

	f.git("config", "core.commentChar", ";")
	char, set, err := repo.CommentChar(ctx(t))
	if err != nil {
		t.Fatalf("CommentChar: %v", err)
	}
	if !set || char != ';' {
		t.Errorf("commentChar = %q, %v; want ';', true", char, set)
	}

	// "auto" means git chooses per message, which flow cannot predict.
	f.git("config", "core.commentChar", "auto")
	if _, set, err := repo.CommentChar(ctx(t)); err != nil || set {
		t.Errorf("auto commentChar reported as set=%v, err=%v", set, err)
	}
}

// A git that refuses must be explained in flow's terms, with git's own first
// line as the reason (P10).
func TestFailuresAreExplained(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): one")
	repo := open(t, f)

	_, err := repo.Log(ctx(t), "no-such-revision")
	if err == nil {
		t.Fatal("logging an unknown revision succeeded")
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("error is %T, want a *core.UserError: %v", err, err)
	}
	if user.Why == "" {
		t.Error("the error does not say why")
	}
	if strings.Contains(user.Why, "fatal:") {
		t.Errorf("the reason still has git's prefix on it: %q", user.Why)
	}
}

// Every command must respect cancellation, so a slow repository cannot wedge
// the CLI.
func TestContextCancellation(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.Branch(cancelled); err == nil {
		t.Fatal("a cancelled context still produced an answer")
	} else if !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("error is %v, want it to wrap ErrUnavailable", err)
	}
}

// git missing from PATH is a dependency failure with a fix, not a crash.
func TestGitMissingFromPath(t *testing.T) {
	newFixture(t)
	t.Setenv("PATH", t.TempDir())
	_, err := git.Root(context.Background(), os.TempDir())
	if err == nil {
		t.Fatal("found a repository with no git installed")
	}
	if !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("error is %v, want it to wrap ErrUnavailable", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "install git") {
		t.Errorf("the error does not say to install git: %v", err)
	}
}

// open is the Repo for a fixture.
func open(t *testing.T, f *fixture) *git.Repo {
	t.Helper()
	repo, err := git.Open(ctx(t), f.dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return repo
}
