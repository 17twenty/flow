package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// What flow does with a credential it will not take and a credential it must
// not repeat (spec 15 §1–2, scenarios S01 and S03).

// configuredRepo is a repository flow has been initialised in, with nothing
// in the environment to authenticate with.
func configuredRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t, "git@github.com:acme/api.git")
	if code, _, stderr := exec(t, "-C", dir, "init", "--tracker", "github-projects", "--owner", "acme", "--project", "2", "--yes"); code != core.ExitOK {
		t.Fatalf("flow init exited %d: %s", code, stderr)
	}
	for _, name := range []string{"GITHUB_TOKEN", "FLOW_GITHUB_TOKEN", "FLOW_GITHUB_PROJECTS_TOKEN", "FLOW_PROJECT_TOKEN"} {
		t.Setenv(name, "")
	}
	return dir
}

// TestScenario_S01_ATrackedOverrideIsIgnoredNotObeyed is scenario S01: a `.flow.local.toml`
// somebody committed names a command to run for a token. The file is the
// team's, so flow does not read it, says so once, and carries on — and then
// fails at 77 for the credential nobody gave it, which is the failure it
// would have had if the file had never existed.
func TestScenario_S01_ATrackedOverrideIsIgnoredNotObeyed(t *testing.T) {
	dir := configuredRepo(t)
	marker := filepath.Join(dir, "ran")
	local := filepath.Join(dir, ".flow.local.toml")
	override := "[auth.github]\ntoken_cmd = [\"touch\", " + strconv.Quote(marker) + "]\n"
	if err := os.WriteFile(local, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".flow.local.toml")

	code, _, stderr := exec(t, "-C", dir, "plan")

	if code != core.ExitNoPerm {
		t.Errorf("flow plan exited %d, want %d — the credential was never supplied\n%s", code, core.ExitNoPerm, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("flow ran a command named by a file the repository can change")
	}
	switch {
	case !strings.Contains(stderr, "tracked by git"):
		t.Errorf("nothing on stderr says the override was ignored:\n%s", stderr)
	case !strings.Contains(stderr, "git rm --cached"):
		t.Errorf("the warning does not say how to make the file personal:\n%s", stderr)
	}
}

// TestAnUntrackedOverrideIsStillTheirOwnFile: the same file, uncommitted, is
// the user's own mistake rather than the team's imposition — flow stops and
// says which key is the problem instead of warning and carrying on.
func TestAnUntrackedOverrideIsStillTheirOwnFile(t *testing.T) {
	dir := configuredRepo(t)
	local := filepath.Join(dir, ".flow.local.toml")
	if err := os.WriteFile(local, []byte("[auth.github]\ntoken_cmd = [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "plan")

	if code != core.ExitUsage {
		t.Errorf("flow plan exited %d, want %d\n%s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "token_cmd") {
		t.Errorf("the refusal does not name the key:\n%s", stderr)
	}
}

// TestScenario_S03_ACredentialPastedWhereItsNameBelongsIsNotEchoed is scenario S03: the
// mistake flow is most likely to be handed a real token by is somebody
// pasting one into `auth.token_env`, and the error about it must not put the
// token on a terminal, into a log or into a scrollback buffer.
func TestScenario_S03_ACredentialPastedWhereItsNameBelongsIsNotEchoed(t *testing.T) {
	dir := configuredRepo(t)
	secret := "gh" + "p_" + strings.Repeat("0aZ", 8)

	path := filepath.Join(dir, ".flow.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := config.Set(source, "forge.auth.token_env", secret)
	if err != nil {
		t.Fatalf("set forge.auth.token_env: %v", err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exec(t, "-C", dir, "--log-level", "debug", "plan")

	if code == core.ExitOK {
		t.Errorf("flow plan accepted a credential where a variable name belongs")
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Errorf("the credential was printed back:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr, "credential") {
		t.Errorf("the error does not say what is wrong:\n%s", stderr)
	}
}
