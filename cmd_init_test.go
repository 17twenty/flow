package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// initRepo creates a throwaway repository with an origin, isolated from the
// machine's git configuration (spec 14).
func initRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := hookRepo(t)
	if origin != "" {
		gitIn(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

// readFlowConfig reads the written configuration, failing if it is not there.
func readFlowConfig(t *testing.T, dir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, ".flow.yaml"))
	if err != nil {
		t.Fatalf(".flow.yaml was not written: %v", err)
	}
	return string(content)
}

// TestScenario_A01_InitOffline is scenario A01: a repository with a GitHub origin and no
// network becomes a configured repository, with the board left for doctor to
// find.
func TestScenario_A01_InitOffline(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")

	code, stdout, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}

	config := readFlowConfig(t, dir)
	for _, want := range []string{
		"# yaml-language-server: $schema=",
		"  repo: acme/api",
		"  type: github ",
		"  owner: acme ",
		"  project: ${pending:project}",
	} {
		if !strings.Contains(config, want) {
			t.Errorf(".flow.yaml is missing %q:\n%s", want, config)
		}
	}
	for _, want := range []string{
		".github/workflows/flow.yml",
		".github/pull_request_template.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s was not written: %v", want, err)
		}
	}
	if !strings.Contains(stdout, "tracker.project") {
		t.Errorf("stdout does not say what is still unknown:\n%s", stdout)
	}
}

// TestInitKeepsThePresetItStartedFrom is what makes shipping a commented
// preset worth doing (spec 03 §7): the file a team opens after init is the
// file flow ships, with its explanations intact and its columns straight.
func TestInitKeepsThePresetItStartedFrom(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}

	config := readFlowConfig(t, dir)
	preset, err := os.ReadFile(filepath.Join("assets", "presets", "lean-kanban.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	was, now := strings.Split(string(preset), "\n"), strings.Split(config, "\n")
	if len(was) != len(now) {
		t.Fatalf("the preset has %d lines and the configuration %d", len(was), len(now))
	}
	changed := map[int]bool{}
	for i := range was {
		if was[i] != now[i] {
			changed[i] = true
		}
	}
	// Only the two values this repository actually differs on: the preset
	// already says github and https://github.com, and setting a value to what
	// it already is changes no bytes.
	if len(changed) != 2 {
		t.Errorf("%d lines differ from the preset, want 2 (forge.repo, tracker.owner)", len(changed))
	}
	for i := range changed {
		if was[i] == "" {
			continue
		}
		wasComment := strings.Index(was[i], "#")
		nowComment := strings.Index(now[i], "#")
		if wasComment != nowComment {
			t.Errorf("line %d: the comment moved from column %d to %d:\n was %s\n now %s", i+1, wasComment, nowComment, was[i], now[i])
		}
	}
}

// TestScenario_A01_InitRefusesToReconfigure is the second half of A01. The refusal does not
// depend on what the existing file says, and it happens before anything is
// written.
func TestScenario_A01_InitRefusesToReconfigure(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("the first init exited %d: %s", code, stderr)
	}
	before := readFlowConfig(t, dir)

	code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "other", "--yes")
	if code != core.ExitUsage {
		t.Fatalf("exited %d, want %d: %s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "already configured") {
		t.Errorf("stderr does not say why:\n%s", stderr)
	}
	if after := readFlowConfig(t, dir); after != before {
		t.Error("the refused init changed .flow.yaml anyway")
	}
}

// TestInitForceStartsAgain. --force is about the configuration; a pull request
// template somebody edited is theirs, and reverting it is not what starting
// the configuration again means.
func TestInitForceStartsAgain(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
		t.Fatalf("the first init exited %d: %s", code, stderr)
	}
	template := filepath.Join(dir, ".github", "pull_request_template.md")
	if err := os.WriteFile(template, []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "other", "--project", "7", "--force", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	config := readFlowConfig(t, dir)
	if !strings.Contains(config, "  owner: other") {
		t.Errorf("--force did not rewrite the configuration:\n%s", config)
	}
	if !strings.Contains(config, `  project: "7"`) {
		t.Errorf("--project was not written:\n%s", config)
	}
	kept, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "ours\n" {
		t.Errorf("--force reverted the pull request template to %q", kept)
	}
}

// TestScenario_A02_InitOutsideGit is scenario A02.
func TestScenario_A02_InitOutsideGit(t *testing.T) {
	dir := t.TempDir()
	// A temporary directory can sit inside somebody's repository, and git
	// walks up. A repository of its own with the working tree removed is the
	// portable way to be definitely outside one.
	outside := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", dir)

	code, _, stderr := exec(t, "-C", outside, "init", "--yes")
	if code != core.ExitUsage {
		t.Fatalf("exited %d, want %d: %s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "fix:   git init") {
		t.Errorf("stderr does not say to run git init:\n%s", stderr)
	}
}

// TestInitAsksWhichForgeWhenItCannotKnow. Detection past github.com is a probe
// (spec 10 §1), and init works offline, so a self-hosted remote is a question
// for the person rather than a guess.
func TestInitAsksWhichForgeWhenItCannotKnow(t *testing.T) {
	dir := initRepo(t, "https://git.example.test/team/thing.git")

	code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitActionRequired {
		t.Fatalf("exited %d, want %d: %s", code, core.ExitActionRequired, stderr)
	}
	if !strings.Contains(stderr, "--forge") {
		t.Errorf("stderr does not name the flag that answers it:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flow.yaml")); err == nil {
		t.Error("a refused init wrote .flow.yaml anyway")
	}

	code, _, stderr = exec(t, "-C", dir, "init", "--forge", "gitea", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	config := readFlowConfig(t, dir)
	for _, want := range []string{"  type: gitea ", "  repo: team/thing", "  url: https://git.example.test"} {
		if !strings.Contains(config, want) {
			t.Errorf(".flow.yaml is missing %q:\n%s", want, config)
		}
	}
}

// TestInitPrefersTheFlagToTheRemote. The remote is evidence and the person
// running init is the authority: a GitHub Enterprise instance behind a
// github.com-looking remote, or a mirror, is theirs to declare.
func TestInitPrefersTheFlagToTheRemote(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")

	code, _, stderr := exec(t, "-C", dir, "init", "--forge", "gitea", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	config := readFlowConfig(t, dir)
	if !strings.Contains(config, "  type: gitea ") {
		t.Errorf("--forge was ignored in favour of the remote's host:\n%s", config)
	}
	if !strings.Contains(config, "  repo: acme/api") {
		t.Errorf("the repository was not still read from the remote:\n%s", config)
	}
}

// TestInitWithoutARemote. A repository with no origin is one somebody has just
// created, and flow has nothing to detect — but the preset is still worth
// writing, with the repository left as it ships for the person to fill in.
func TestInitWithoutARemote(t *testing.T) {
	dir := initRepo(t, "")

	code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if config := readFlowConfig(t, dir); !strings.Contains(config, "  repo: owner/name") {
		t.Errorf("the repository was not left as the preset ships it:\n%s", config)
	}
}

// TestInitWritesAConfigurationFlowCanRead. init validates before it writes, so
// no combination of flags can leave behind a file flow itself refuses. Every
// preset is checked, because a preset is only ever read through init.
func TestInitWritesAConfigurationFlowCanRead(t *testing.T) {
	for _, preset := range []string{"lean-kanban", "deploy-done", "minimal"} {
		t.Run(preset, func(t *testing.T) {
			dir := initRepo(t, "git@github.com:acme/api.git")
			code, _, stderr := exec(t, "-C", dir, "init", "--preset", preset, "--tracker", "github-projects", "--owner", "acme", "--project", "7", "--yes")
			if code != core.ExitOK {
				t.Fatalf("exited %d: %s", code, stderr)
			}
			if code, _, stderr := exec(t, "-C", dir, "doctor", "--check", "config.*"); code == core.ExitUsage {
				t.Fatalf("the written configuration does not parse: %s", stderr)
			}
			config := readFlowConfig(t, dir)
			if !strings.Contains(config, "preset: "+preset) {
				t.Errorf("the %s preset was not the one written:\n%s", preset, config)
			}
		})
	}
}

// TestInitIsDeterministic: two inits of the same repository with the same
// flags produce the same bytes, whatever order the values happened to be
// discovered in.
func TestInitIsDeterministic(t *testing.T) {
	first := initRepo(t, "git@github.com:acme/api.git")
	second := initRepo(t, "git@github.com:acme/api.git")
	args := []string{"init", "--tracker", "github-projects", "--owner", "acme", "--project", "7", "--yes"}
	for _, dir := range []string{first, second} {
		if code, _, stderr := exec(t, append([]string{"-C", dir}, args...)...); code != core.ExitOK {
			t.Fatalf("exited %d: %s", code, stderr)
		}
	}
	if a, b := readFlowConfig(t, first), readFlowConfig(t, second); a != b {
		t.Errorf("two inits wrote different files:\n%s\n---\n%s", a, b)
	}
}

// TestInitNoTemplatesAndNoHooks. Both flags exist for a repository whose CI
// and hooks are somebody else's business.
func TestInitNoTemplatesAndNoHooks(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")

	code, stdout, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--no-templates", "--no-hooks", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	readFlowConfig(t, dir)
	if _, err := os.Stat(filepath.Join(dir, ".github")); err == nil {
		t.Error("--no-templates wrote .github anyway")
	}
	if _, err := os.Stat(hookPath(dir)); err == nil {
		t.Error("--no-hooks installed the hook anyway")
	}
	if !strings.Contains(stdout, "commit-msg hook skipped") {
		t.Errorf("stdout does not report the hook was skipped:\n%s", stdout)
	}
}

// TestInitLeavesAForeignHookAlone. A hook is a convenience and the gate on the
// pull request is the authority (spec 08), so somebody else's commit-msg hook
// is reported, not replaced, and init still succeeds.
func TestInitLeavesAForeignHookAlone(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")
	if err := os.WriteFile(hookPath(dir), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	hook, err := os.ReadFile(hookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(hook) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("init replaced somebody else's hook:\n%s", hook)
	}
	if !strings.Contains(stdout, "commit-msg hook deferred") {
		t.Errorf("stdout does not report the hook was left alone:\n%s", stdout)
	}
}

// TestInitJSON. Spec 02 §3: exactly one document, carrying its schema id.
func TestInitJSON(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")

	code, stdout, stderr := exec(t, "-C", dir, "--json", "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	var report struct {
		Schema  string              `json:"schema"`
		Root    string              `json:"root"`
		Preset  string              `json:"preset"`
		Files   []map[string]string `json:"files"`
		Pending []string            `json:"pending"`
		Hook    string              `json:"hook"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if decoder.More() {
		t.Error("stdout holds more than one JSON document")
	}
	if report.Schema != core.SchemaID("init") {
		t.Errorf("schema = %q, want %q", report.Schema, core.SchemaID("init"))
	}
	if report.Root != dir {
		t.Errorf("root = %q, want %q", report.Root, dir)
	}
	if report.Preset != "lean-kanban" {
		t.Errorf("preset = %q, want lean-kanban", report.Preset)
	}
	if len(report.Files) != 4 {
		t.Errorf("files = %v, want four", report.Files)
	}
	if len(report.Pending) != 1 || report.Pending[0] != "tracker.project" {
		t.Errorf("pending = %v, want [tracker.project]", report.Pending)
	}
	if report.Hook != "installed" {
		t.Errorf("hook = %q, want installed", report.Hook)
	}
}

// TestInitWithProjectHasNothingPending: giving every value up front means
// doctor has nothing to resolve, and init says so.
func TestInitWithProjectHasNothingPending(t *testing.T) {
	dir := initRepo(t, "git@github.com:acme/api.git")

	code, stdout, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--project", "7", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "Still to find out") {
		t.Errorf("stdout says something is pending when nothing is:\n%s", stdout)
	}
	if !strings.Contains(stdout, "flow plan") {
		t.Errorf("stdout does not say what to do next:\n%s", stdout)
	}
}

// TestInitDropsACredentialFromTheRemote. A clone URL may carry a token
// (spec 15 §2), and `forge.url` is written from the remote's host.
func TestInitDropsACredentialFromTheRemote(t *testing.T) {
	secret := "gh" + "p_" + strings.Repeat("A", 36)
	dir := initRepo(t, "https://"+secret+"@github.com/acme/api.git")

	code, stdout, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes")
	if code != core.ExitOK {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	config := readFlowConfig(t, dir)
	for what, text := range map[string]string{".flow.yaml": config, "stdout": stdout, "stderr": stderr} {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds the token from the remote URL", what)
		}
	}
	if !strings.Contains(config, "  repo: acme/api") {
		t.Errorf("the repository was not read from the remote:\n%s", config)
	}
}

// readWorkflow reads the gate workflow init scaffolds.
func readWorkflow(t *testing.T, dir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "flow.yml"))
	if err != nil {
		t.Fatalf("the workflow was not written: %v", err)
	}
	return string(content)
}

// asBuild runs fn with flow reporting itself as the given version, which is
// what ldflags inject into a release binary.
func asBuild(t *testing.T, v string, fn func()) {
	t.Helper()
	was := version
	t.Cleanup(func() { version = was })
	version = v
	fn()
}

// asInstalledBuild runs fn with flow built the way `go install` builds it:
// nothing injected, and the module version the only record of which release
// it is.
func asInstalledBuild(t *testing.T, module string, fn func()) {
	t.Helper()
	wasRead := readBuildInfo
	t.Cleanup(func() { readBuildInfo = wasRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/17twenty/flow", Version: module}}, true
	}
	asBuild(t, devVersion, fn)
}

// The workflow init writes has to install a flow that exists, and the obvious
// one is the flow that wrote it: a gate judging a pull request should be the
// build that set the repository up. The template shipped a version nobody had
// ever tagged, so every repository flow configured got a workflow whose first
// step failed (issue #186).
func TestTheWorkflowInitWritesInstallsTheBuildThatWroteIt(t *testing.T) {
	asBuild(t, "v1.4.0", func() {
		dir := initRepo(t, "git@github.com:acme/api.git")
		if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
			t.Fatalf("exited %d: %s", code, stderr)
		}
		workflow := readWorkflow(t, dir)
		if !strings.Contains(workflow, "FLOW_VERSION: v1.4.0") {
			t.Errorf("the workflow does not install the build that wrote it:\n%s", workflow)
		}
		if strings.Contains(workflow, "FLOW_VERSION: latest") {
			t.Errorf("the workflow kept the shipped fallback:\n%s", workflow)
		}
	})
}

// A build with no release behind it has nothing to pin to, and must not
// invent one: spec 16 §2 guarantees @latest always resolves, so that is what
// it leaves in place.
func TestAnUnreleasedBuildWritesAWorkflowThatStillInstalls(t *testing.T) {
	asBuild(t, devVersion, func() {
		dir := initRepo(t, "git@github.com:acme/api.git")
		if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
			t.Fatalf("exited %d: %s", code, stderr)
		}
		if workflow := readWorkflow(t, dir); !strings.Contains(workflow, "FLOW_VERSION: latest") {
			t.Errorf("an unreleased build did not fall back to latest:\n%s", workflow)
		}
	})
}

// templateReader finds every place the shipped templates are read.
var templateReader = regexp.MustCompile(`assets\.Templates\((.*)\)`)

// A workflow reaches a repository two ways: init copies it into the working
// tree, and the forge.files proposal writes it when apply finds it missing.
// Both are the same bytes only while every reader asks for the same build, so
// a second answer to "which flow is this" is a drift waiting to happen.
func TestEveryReadingOfTheTemplatesNamesTheSameBuild(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	found := 0
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range templateReader.FindAllStringSubmatch(string(content), -1) {
			found++
			if arg := strings.TrimSpace(match[1]); arg != "buildVersion()" {
				t.Errorf("%s reads the templates as %q, not as buildVersion()", path, arg)
			}
		}
	}
	if found == 0 {
		t.Fatal("nothing in the command surface reads the templates")
	}
}

// `go install` injects no ldflags, so an installed binary knows which release
// it is only from the module it was built from. That is still a release, and
// the workflow it writes must pin to it rather than drifting to whatever
// @latest resolves to months later.
func TestAnInstalledBuildPinsTheReleaseItWasInstalledAt(t *testing.T) {
	asInstalledBuild(t, "v0.3.0", func() {
		dir := initRepo(t, "git@github.com:acme/api.git")
		if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--yes"); code != core.ExitOK {
			t.Fatalf("exited %d: %s", code, stderr)
		}
		if workflow := readWorkflow(t, dir); !strings.Contains(workflow, "FLOW_VERSION: v0.3.0") {
			t.Errorf("an installed build did not pin the release it came from:\n%s", workflow)
		}
	})
}
