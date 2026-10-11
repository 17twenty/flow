package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates every git invocation in this package from the machine it
// runs on: no global or system configuration, no credential prompts, and a
// fixed identity so commits are reproducible (spec 14).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "flow-git-home")
	if err != nil {
		panic(err)
	}
	for key, value := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, "config"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_NAME":     "Ada Lovelace",
		"GIT_AUTHOR_EMAIL":    "ada@example.com",
		"GIT_COMMITTER_NAME":  "Flow Bot",
		"GIT_COMMITTER_EMAIL": "bot@example.com",
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fixture is a throwaway repository a test can build a history in.
type fixture struct {
	t   *testing.T
	dir string
	n   int
}

// newFixture creates an initialised repository with no commits.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: dir}
	f.git("init", "--quiet", "--initial-branch=main")
	return f
}

// git runs a git command in the fixture and returns its output, failing the
// test if git refuses.
func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// write puts content in the working tree.
func (f *fixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// commit stages everything and records a commit with the given message. Each
// commit in a fixture is a minute later than the last, so order is testable.
func (f *fixture) commit(message string) {
	f.t.Helper()
	f.n++
	when := f.date()
	cmd := exec.Command("git", "commit", "--quiet", "--allow-empty", "--file=-")
	cmd.Dir = f.dir
	cmd.Stdin = strings.NewReader(message)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// date is the timestamp of the fixture's nth commit.
func (f *fixture) date() string {
	return "2024-05-01T10:0" + string(rune('0'+f.n%10)) + ":00+00:00"
}

// add stages a path.
func (f *fixture) add(paths ...string) {
	f.t.Helper()
	f.git(append([]string{"add", "--"}, paths...)...)
}

// ctx is a context for a test's git invocations.
func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c
}
