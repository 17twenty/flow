package main

import (
	"encoding/json"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/core"
)

// hookRepo creates a throwaway repository, isolated from the machine's git
// configuration, and returns its root (spec 14).
func hookRepo(t *testing.T) string {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(key, value)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Anything flow caches between runs belongs to the test too: a suite
	// that wrote into the machine's own cache directory would leave state
	// behind it and read state it did not write (spec 14).
	t.Setenv(core.CacheDirEnv, filepath.Join(t.TempDir(), "cache"))
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	return dir
}

// gitIn runs a git command in dir, failing the test if git refuses.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// hookPath is where the commit-msg hook lives in a default repository.
func hookPath(dir string) string { return filepath.Join(dir, ".git", "hooks", "commit-msg") }

func TestHookInstall(t *testing.T) {
	dir := hookRepo(t)

	code, stdout, stderr := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "installed ") {
		t.Errorf("stdout = %q, want it to report the install", stdout)
	}

	content, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatalf("the hook was not written: %v", err)
	}
	if !strings.Contains(string(content), "flow check commit --fix") {
		t.Errorf("the hook does not call flow check commit:\n%s", content)
	}
	if !strings.HasPrefix(string(content), "#!/bin/sh") {
		t.Errorf("the hook has no shebang:\n%s", content)
	}

	info, err := os.Stat(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the hook is not executable: %v", info.Mode())
	}
}

// Installing twice must change nothing: a hook is a file people diff.
func TestHookInstallIsIdempotent(t *testing.T) {
	dir := hookRepo(t)
	if code, _, stderr := exec(t, "-C", dir, "hook", "install"); code != core.ExitOK {
		t.Fatalf("first install exited %d: %s", code, stderr)
	}
	before, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitOK {
		t.Fatalf("second install exited %d: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "unchanged ") {
		t.Errorf("stdout = %q, want it to report no change", stdout)
	}
	after, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the second install rewrote the hook")
	}
}

// An older flow hook is upgraded: flow owns what it wrote.
func TestHookInstallUpgradesItsOwnHook(t *testing.T) {
	dir := hookRepo(t)
	stale := "#!/bin/sh\n# installed by flow — https://github.com/17twenty/flow\nexec flow hook run commit-msg \"$1\"\n"
	if err := os.WriteFile(hookPath(dir), []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "updated ") {
		t.Errorf("stdout = %q, want it to report the upgrade", stdout)
	}
	content, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "hook run") {
		t.Error("the stale hook survived")
	}
}

// Somebody else's hook is never silently replaced.
func TestHookInstallRefusesAForeignHook(t *testing.T) {
	dir := hookRepo(t)
	foreign := "#!/bin/sh\n# our own careful hook\nexit 0\n"
	if err := os.WriteFile(hookPath(dir), []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitFail {
		t.Errorf("exited %d, want %d", code, core.ExitFail)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr does not offer a way through: %s", stderr)
	}
	content, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != foreign {
		t.Error("the foreign hook was modified anyway")
	}

	code, stdout, stderr := exec(t, "-C", dir, "hook", "install", "--force")
	if code != core.ExitOK {
		t.Fatalf("--force exited %d: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "replaced ") {
		t.Errorf("stdout = %q, want it to report the replacement", stdout)
	}
}

// core.hooksPath decides where git looks, so it decides where flow writes.
func TestHookInstallHonoursHooksPath(t *testing.T) {
	dir := hookRepo(t)
	gitIn(t, dir, "config", "core.hooksPath", "githooks")

	code, _, stderr := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "githooks", "commit-msg")); err != nil {
		t.Errorf("the hook was not written to core.hooksPath: %v", err)
	}
	if _, err := os.Stat(hookPath(dir)); err == nil {
		t.Error("a hook was written to .git/hooks, which git is not reading")
	}
}

// A repository with a hook manager gets the snippet, not a file git will
// ignore (spec 02 §2).
func TestHookInstallDefersToHookManagers(t *testing.T) {
	cases := []struct {
		name    string
		create  func(t *testing.T, dir string)
		manager string
		snippet string
	}{
		{
			name:    "lefthook",
			create:  func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "lefthook.yml"), "pre-commit:\n") },
			manager: "lefthook",
			snippet: "commit-msg:",
		},
		{
			name:    "husky",
			create:  func(t *testing.T, dir string) { mkdir(t, filepath.Join(dir, ".husky")) },
			manager: "husky",
			snippet: ".husky/commit-msg",
		},
		{
			name: "pre-commit",
			create: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
			},
			manager: "pre-commit",
			snippet: "flow-commit-msg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := hookRepo(t)
			tc.create(t, dir)

			code, stdout, stderr := exec(t, "-C", dir, "hook", "install")
			if code != core.ExitActionRequired {
				t.Fatalf("exited %d, want %d: %s", code, core.ExitActionRequired, stderr)
			}
			if !strings.Contains(stdout, tc.manager) || !strings.Contains(stdout, tc.snippet) {
				t.Errorf("stdout does not print the %s snippet:\n%s", tc.manager, stdout)
			}
			if !strings.Contains(stdout, "flow check commit --fix") {
				t.Errorf("the snippet does not call flow check commit:\n%s", stdout)
			}
			if _, err := os.Stat(hookPath(dir)); err == nil {
				t.Error("a hook was written behind the manager's back")
			}

			if code, _, stderr := exec(t, "-C", dir, "hook", "install", "--force"); code != core.ExitOK {
				t.Fatalf("--force exited %d: %s", code, stderr)
			}
			if _, err := os.Stat(hookPath(dir)); err != nil {
				t.Errorf("--force did not install: %v", err)
			}
		})
	}
}

// core.hooksPath pointing at a manager's directory is detection enough, even
// before that manager has written anything.
func TestHookInstallDetectsManagersByHooksPath(t *testing.T) {
	dir := hookRepo(t)
	gitIn(t, dir, "config", "core.hooksPath", ".husky/_")

	code, stdout, _ := exec(t, "-C", dir, "hook", "install")
	if code != core.ExitActionRequired {
		t.Fatalf("exited %d, want %d", code, core.ExitActionRequired)
	}
	if !strings.Contains(stdout, "husky") {
		t.Errorf("husky was not detected from core.hooksPath:\n%s", stdout)
	}
}

func TestHookUninstall(t *testing.T) {
	dir := hookRepo(t)
	if code, _, stderr := exec(t, "-C", dir, "hook", "install"); code != core.ExitOK {
		t.Fatalf("install exited %d: %s", code, stderr)
	}

	code, stdout, stderr := exec(t, "-C", dir, "hook", "uninstall")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "removed ") {
		t.Errorf("stdout = %q", stdout)
	}
	if _, err := os.Stat(hookPath(dir)); err == nil {
		t.Error("the hook is still there")
	}

	// Removing what is not there is not a failure.
	code, stdout, stderr = exec(t, "-C", dir, "hook", "uninstall")
	if code != core.ExitOK {
		t.Fatalf("second uninstall exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no flow hook") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestHookUninstallRefusesAForeignHook(t *testing.T) {
	dir := hookRepo(t)
	foreign := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(hookPath(dir), []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "hook", "uninstall")
	if code != core.ExitFail {
		t.Errorf("exited %d, want %d", code, core.ExitFail)
	}
	if !strings.Contains(stderr, "not written by flow") {
		t.Errorf("stderr = %s", stderr)
	}
	if _, err := os.Stat(hookPath(dir)); err != nil {
		t.Error("flow deleted a hook it did not write")
	}
}

func TestHookJSON(t *testing.T) {
	dir := hookRepo(t)

	for _, tc := range []struct {
		args            []string
		command, action string
	}{
		{[]string{"hook", "install"}, "install", "installed"},
		{[]string{"hook", "install"}, "install", "unchanged"},
		{[]string{"hook", "uninstall"}, "uninstall", "removed"},
		{[]string{"hook", "uninstall"}, "uninstall", "absent"},
	} {
		args := append([]string{"-C", dir, "--json"}, tc.args...)
		code, stdout, stderr := exec(t, args...)
		if code != core.ExitOK {
			t.Fatalf("%v exited %d: %s", tc.args, code, stderr)
		}
		var doc struct {
			Schema  string `json:"schema"`
			Command string `json:"command"`
			Action  string `json:"action"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("%v: stdout is not one JSON document: %v", tc.args, err)
		}
		if doc.Schema != core.SchemaID("hook") {
			t.Errorf("schema = %q, want %q", doc.Schema, core.SchemaID("hook"))
		}
		if doc.Command != tc.command || doc.Action != tc.action {
			t.Errorf("%v reported %s/%s, want %s/%s", tc.args, doc.Command, doc.Action, tc.command, tc.action)
		}
		if doc.Path != hookPath(dir) {
			t.Errorf("path = %q, want %q", doc.Path, hookPath(dir))
		}
	}
}

// Outside a repository there is nowhere to put a hook, and saying so is the
// whole job.
func TestHookOutsideARepository(t *testing.T) {
	hookRepo(t) // skips when git is missing, and isolates configuration
	dir := t.TempDir()
	code, _, stderr := exec(t, "-C", dir, "hook", "install")
	if code == core.ExitOK {
		t.Skip("the temporary directory is inside a repository")
	}
	if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "fix:") {
		t.Errorf("the failure is not explained: %s", stderr)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// topLevelCommands are the commands spec 02 §2 fixes. A template that invokes
// one of them must invoke it correctly.
var topLevelCommands = map[string]bool{
	"init": true, "plan": true, "apply": true, "attest": true, "doctor": true,
	"check": true, "hook": true, "schema": true, "version": true,
}

// invocation finds "flow <words>" in a template.
var invocation = regexp.MustCompile(`\bflow ((?:[a-z][a-z-]*)(?: [a-z][a-z-]*)*)`)

// Every shipped template that tells a user or a hook manager to run flow must
// name a command flow has. The templates shipped before this item called
// `flow hook run`, which has never existed: a snippet nobody runs in CI is a
// promise only a test can keep (spec 02 §2).
func TestShippedTemplatesInvokeRealCommands(t *testing.T) {
	templates, err := assets.Templates()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = fs.WalkDir(templates, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(templates, path)
		if err != nil {
			return err
		}
		for _, match := range invocation.FindAllStringSubmatch(string(content), -1) {
			words := strings.Fields(match[1])
			if len(words) == 0 || !topLevelCommands[words[0]] {
				continue
			}
			checked++
			if code, _, stderr := exec(t, append(words, "--help")...); code != core.ExitOK {
				t.Errorf("%s invokes `flow %s`, which exits %d: %s", path, match[1], code, stderr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Error("no template invokes flow; this test is not testing anything")
	}
}
