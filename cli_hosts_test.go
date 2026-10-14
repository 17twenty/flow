package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/forge"
)

// Where a credential may be sent (spec 15 §1, scenario S02). `.flow.yaml`
// names the URL and the repository can change it, so the file flow is reading
// cannot be what approves the host it is read from.

// hostContext is an invocation with nobody watching, which is what every
// run in CI is.
func hostContext(t *testing.T, flags CLI) *Context {
	t.Helper()
	return &Context{
		Ctx:    context.Background(),
		CLI:    &flags,
		Stdin:  strings.NewReader(""),
		Stdout: io.Discard,
		Stderr: io.Discard,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// hostSession is the little of a session the question needs: the two files
// its answer could be written in.
func hostSession() *session {
	return &session{ConfigPath: "/repo/.flow.yaml", SettingsPath: "/home/dev/.config/flow/config.toml"}
}

// credentialsFor builds a resolver holding one provider's approved hosts and
// no credential at all: nothing here should get as far as wanting one.
func credentialsFor(provider string, hosts ...string) *config.Credentials {
	return config.NewCredentials(
		func(string) string { return "" },
		map[string]config.AuthSource{provider: {Hosts: hosts}},
	)
}

// TestTheProvidersOwnHostIsNotAQuestion: flow ships github's API host, so the
// ordinary run asks nobody anything.
func TestTheProvidersOwnHostIsNotAQuestion(t *testing.T) {
	app := hostContext(t, CLI{})
	asked := false
	ask := func(string) (bool, error) { asked = true; return true, nil }

	host, err := app.approveHost(hostSession(), credentialsFor("github"), forge.GitHubKind, forge.GitHubAPI, ask)
	if err != nil {
		t.Fatalf("approveHost: %v", err)
	}
	if host != "api.github.com" {
		t.Errorf("host = %q, want api.github.com", host)
	}
	if asked {
		t.Error("flow asked permission to talk to github's own API")
	}
}

// TestScenario_S02_AnUnapprovedHostWithNobodyToAskStopsTheRun is scenario S02: no terminal,
// so the run exits 77 and says the one line that would let it through.
func TestScenario_S02_AnUnapprovedHostWithNobodyToAskStopsTheRun(t *testing.T) {
	app := hostContext(t, CLI{})
	s := hostSession()

	_, err := app.approveHost(s, credentialsFor("github"), forge.GitHubKind, "https://ghe.example.com/api/v3", nil)
	if err == nil {
		t.Fatal("flow was ready to send the credential to a host nobody approved")
	}
	if core.ExitCode(err) != core.ExitNoPerm {
		t.Errorf("ExitCode = %d, want %d", core.ExitCode(err), core.ExitNoPerm)
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("not a UserError: %v", err)
	}
	switch {
	case !strings.Contains(user.Error(), "ghe.example.com"):
		t.Errorf("%q does not name the host", user.Error())
	case !strings.Contains(user.Fix, `hosts = ["ghe.example.com"]`):
		t.Errorf("fix %q is not the line to write", user.Fix)
	case !strings.Contains(user.Fix, s.SettingsPath):
		t.Errorf("fix %q does not point at the user's own file", user.Fix)
	}
}

// TestAHostTheUserListedIsApproved: the list is in a file the repository
// cannot write, which is what makes it an answer.
func TestAHostTheUserListedIsApproved(t *testing.T) {
	app := hostContext(t, CLI{})
	credentials := credentialsFor(forge.GitHubKind, "ghe.example.com")

	host, err := app.approveHost(hostSession(), credentials, forge.GitHubKind, "https://ghe.example.com/api/v3", nil)
	if err != nil {
		t.Fatalf("a host the user listed was refused: %v", err)
	}
	if host != "ghe.example.com" {
		t.Errorf("host = %q, want ghe.example.com", host)
	}
}

// TestAPersonCanApproveAHostForTheRun: yes carries on and is remembered, so
// the second provider on the same host is not a second question.
func TestAPersonCanApproveAHostForTheRun(t *testing.T) {
	app := hostContext(t, CLI{})
	credentials := credentialsFor(forge.GitHubKind)
	questions := 0
	ask := func(question string) (bool, error) {
		questions++
		if !strings.Contains(question, "ghe.example.com") {
			t.Errorf("question %q does not name the host", question)
		}
		return true, nil
	}

	for range 2 {
		if _, err := app.approveHost(hostSession(), credentials, forge.GitHubKind, "https://ghe.example.com/api/v3", ask); err != nil {
			t.Fatalf("approveHost: %v", err)
		}
	}
	if questions != 1 {
		t.Errorf("asked %d times, want once", questions)
	}
}

// TestNoMeansNo: and the refusal still says how to make the answer permanent.
func TestNoMeansNo(t *testing.T) {
	app := hostContext(t, CLI{})
	credentials := credentialsFor(forge.GitHubKind)
	ask := func(string) (bool, error) { return false, nil }

	_, err := app.approveHost(hostSession(), credentials, forge.GitHubKind, "https://ghe.example.com/api/v3", ask)
	if core.ExitCode(err) != core.ExitNoPerm {
		t.Fatalf("err = %v (exit %d), want a refusal at %d", err, core.ExitCode(err), core.ExitNoPerm)
	}
	var user *core.UserError
	if !errors.As(err, &user) || user.Why != "you said no" {
		t.Errorf("the refusal does not say the person declined: %+v", user)
	}
	if credentials.ApprovedHost(forge.GitHubKind, "ghe.example.com", "api.github.com") {
		t.Error("a no was remembered as a yes")
	}
}

// TestAQuestionThatCouldNotBeAskedIsNotAnAnswer: a terminal that stopped
// taking bytes is the run failing, and reporting it as a refusal would hide
// the cause.
func TestAQuestionThatCouldNotBeAskedIsNotAnAnswer(t *testing.T) {
	app := hostContext(t, CLI{})
	broken := errors.New("ask for confirmation: broken pipe")
	ask := func(string) (bool, error) { return false, broken }

	_, err := app.approveHost(hostSession(), credentialsFor(forge.GitHubKind), forge.GitHubKind, "https://ghe.example.com", ask)
	if !errors.Is(err, broken) {
		t.Errorf("err = %v, want the reason the question could not be put", err)
	}
}

// TestAnAddressFlowCannotReadIsNotAHost: the refusal names the file that
// carries the URL, because that is where the mistake is.
func TestAnAddressFlowCannotReadIsNotAHost(t *testing.T) {
	app := hostContext(t, CLI{})
	s := hostSession()

	_, err := app.approveHost(s, credentialsFor(forge.GitHubKind), forge.GitHubKind, "not a url", nil)
	if core.ExitCode(err) != core.ExitUsage {
		t.Fatalf("err = %v (exit %d), want %d", err, core.ExitCode(err), core.ExitUsage)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, s.ConfigPath) {
		t.Errorf("the refusal does not point at the configuration: %+v", user)
	}
}

// TestNobodyIsThereToAsk: every reason flow has for believing there is no
// person at the other end.
func TestNobodyIsThereToAsk(t *testing.T) {
	cases := []struct {
		name     string
		flags    CLI
		ci       bool
		terminal bool
	}{
		{name: "output for a machine", flags: CLI{JSON: true}, terminal: true},
		{name: "--non-interactive", flags: CLI{NonInteractive: true}, terminal: true},
		{name: "a CI job", ci: true, terminal: true},
		{name: "stdout is not a terminal", terminal: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", "")
			if tc.ci {
				t.Setenv("CI", "1")
			}
			app := hostContext(t, tc.flags)
			if app.hostApprover(tc.terminal) != nil {
				t.Error("flow was ready to ask a question nobody would see")
			}
		})
	}
}

// And when somebody is watching, flow asks rather than stopping.
func TestAPersonWatchingIsAsked(t *testing.T) {
	t.Setenv("CI", "")
	if hostContext(t, CLI{}).hostApprover(true) == nil {
		t.Error("flow would not ask a question the person in front of it could answer")
	}
}

// TestTheStandardHostIsFlowsOwnKnowledge: the kinds flow ships adapters for
// have one, and a kind it does not ship has none — which approves nothing
// rather than everything.
func TestTheStandardHostIsFlowsOwnKnowledge(t *testing.T) {
	for _, kind := range []string{"github", "github-projects"} {
		if got := standardHost(kind); got != "api.github.com" {
			t.Errorf("standardHost(%q) = %q, want api.github.com", kind, got)
		}
	}
	if got := standardHost("jira-cloud"); got != "" {
		t.Errorf("standardHost(jira-cloud) = %q, want nothing", got)
	}
}

// TestTheApprovalBindsTheClientAndNotOneRequest: what was agreed to is a
// host, so a redirect off it is refused before any packet leaves — the
// credential would have gone with it.
//
// Both hosts here are loopback, so the test guard of spec 14 allows both and
// the refusal can only be the one this item added.
func TestTheApprovalBindsTheClientAndNotOneRequest(t *testing.T) {
	t.Setenv("FLOW_GITHUB_TOKEN", "x")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	port := portOf(t, server.URL)
	app := hostContext(t, CLI{})
	credentials := config.NewCredentials(
		func(name string) string {
			if name == "FLOW_GITHUB_TOKEN" {
				return "x"
			}
			return ""
		},
		map[string]config.AuthSource{forge.GitHubKind: {Hosts: []string{"127.0.0.1"}}},
	)

	client, err := app.client(hostSession(), credentials, forge.GitHubKind, config.Auth{}, "http://127.0.0.1:"+port, forge.GitHubHeader())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Do(app.Ctx, core.Request{Method: http.MethodGet, Path: "/"}); err != nil {
		t.Fatalf("the approved host was refused: %v", err)
	}

	_, err = client.Do(app.Ctx, core.Request{Method: http.MethodGet, Path: "http://localhost:" + port + "/"})
	if !errors.Is(err, core.ErrPermission) {
		t.Fatalf("err = %v, want the credential kept off a host nobody approved", err)
	}
}

// portOf is the port a test server is listening on.
func portOf(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed.Port()
}

// TestScenario_S02_AWholeRunStopsAtAnUnapprovedHost is scenario S02 end to end: the
// configuration names an API nobody approved, nothing in the environment is
// a terminal, and flow exits 77 before it even asks for the credential —
// which is why the message is about the host rather than the missing token.
func TestScenario_S02_AWholeRunStopsAtAnUnapprovedHost(t *testing.T) {
	dir := configuredRepo(t)
	path := filepath.Join(dir, ".flow.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := config.Set(source, "forge.url", "https://ghe.example.com")
	if err != nil {
		t.Fatalf("set forge.url: %v", err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "plan")

	if code != core.ExitNoPerm {
		t.Fatalf("flow plan exited %d, want %d\n%s", code, core.ExitNoPerm, stderr)
	}
	switch {
	case !strings.Contains(stderr, "ghe.example.com"):
		t.Errorf("the refusal does not name the host:\n%s", stderr)
	case !strings.Contains(stderr, "[auth.github]"):
		t.Errorf("the refusal does not say where to approve it:\n%s", stderr)
	}
}

// TestNoCredentialIsAskedForBeforeFlowKnowsWhereItMayGo: a `token_cmd`
// unlocks a vault or asks for a fingerprint, and running one to produce a
// secret flow has already decided it will not send is a question put to
// somebody for nothing.
func TestNoCredentialIsAskedForBeforeFlowKnowsWhereItMayGo(t *testing.T) {
	dir := configuredRepo(t)
	marker := filepath.Join(dir, "unlocked")
	user := filepath.Join(t.TempDir(), "config.toml")
	personal := "[auth.github]\ntoken_cmd = [\"touch\", " + strconv.Quote(marker) + "]\n"
	if err := os.WriteFile(user, []byte(personal), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".flow.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := config.Set(source, "forge.url", "https://ghe.example.com")
	if err != nil {
		t.Fatalf("set forge.url: %v", err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := exec(t, "-C", dir, "--user-config", user, "plan")

	if code != core.ExitNoPerm {
		t.Fatalf("flow plan exited %d, want %d\n%s", code, core.ExitNoPerm, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("flow unlocked a credential it had already decided it would not send")
	}
}
