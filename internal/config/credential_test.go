package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// fakeToken is credential-shaped: enough of a GitHub token for core.Redact to
// recognise it, assembled from pieces so that this file never holds a string a
// secret scanner has to think about.
var fakeToken = "gh" + "p_" + strings.Repeat("0aZ", 8)

// env builds a getenv from a map, so no test touches the real environment.
func env(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

// fixedRunner is a credential helper that always says the same thing.
func fixedRunner(out string, err error) CommandRunner {
	return func(context.Context, []string) ([]byte, error) { return []byte(out), err }
}

func TestTokenResolutionOrder(t *testing.T) {
	const cmdToken = "from-token-cmd"

	tests := []struct {
		name     string
		provider string
		auth     Auth
		env      map[string]string
		tokenCmd []string
		want     string
		source   string
	}{
		{
			name:     "the configured variable wins",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			env:      map[string]string{"GITHUB_TOKEN": "named", "FLOW_GITHUB_TOKEN": "fallback"},
			tokenCmd: []string{"helper"},
			want:     "named",
			source:   "$GITHUB_TOKEN",
		},
		{
			name:     "the fallback is used when the configured variable is unset",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			env:      map[string]string{"FLOW_GITHUB_TOKEN": "fallback"},
			tokenCmd: []string{"helper"},
			want:     "fallback",
			source:   "$FLOW_GITHUB_TOKEN",
		},
		{
			name:     "the fallback is used when no variable is configured",
			provider: "linear",
			env:      map[string]string{"FLOW_LINEAR_TOKEN": "fallback"},
			want:     "fallback",
			source:   "$FLOW_LINEAR_TOKEN",
		},
		{
			name:     "a hyphenated provider becomes an underscored variable",
			provider: "github-projects",
			env:      map[string]string{"FLOW_GITHUB_PROJECTS_TOKEN": "fallback"},
			want:     "fallback",
			source:   "$FLOW_GITHUB_PROJECTS_TOKEN",
		},
		{
			name:     "the helper runs when neither variable is set",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			tokenCmd: []string{"helper"},
			want:     cmdToken,
			source:   "auth.github.token_cmd",
		},
		{
			name:     "a variable set to whitespace counts as unset",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			env:      map[string]string{"GITHUB_TOKEN": "   ", "FLOW_GITHUB_TOKEN": "fallback"},
			want:     "fallback",
			source:   "$FLOW_GITHUB_TOKEN",
		},
		{
			name:     "surrounding whitespace is trimmed from a variable",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			env:      map[string]string{"GITHUB_TOKEN": "  named\n"},
			want:     "named",
			source:   "$GITHUB_TOKEN",
		},
		{
			name:     "the configured variable may be the fallback itself",
			provider: "github",
			auth:     Auth{TokenEnv: "FLOW_GITHUB_TOKEN"},
			env:      map[string]string{"FLOW_GITHUB_TOKEN": "named"},
			want:     "named",
			source:   "$FLOW_GITHUB_TOKEN",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := map[string]AuthSource{test.provider: {TokenCmd: test.tokenCmd}}
			credentials := NewCredentials(env(test.env), auth).WithRunner(fixedRunner(cmdToken+"\n", nil))

			got, err := credentials.Token(t.Context(), test.provider, test.auth)
			if err != nil {
				t.Fatalf("Token: %v", err)
			}
			if got.Secret() != test.want {
				t.Errorf("secret = %q, want %q", got.Secret(), test.want)
			}
			if got.Source != test.source {
				t.Errorf("Source = %q, want %q", got.Source, test.source)
			}
			if got.Provider != test.provider {
				t.Errorf("Provider = %q, want %q", got.Provider, test.provider)
			}
		})
	}
}

func TestTokenWithoutAnySourceNamesTheVariable(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		auth     Auth
		wantIn   []string
	}{
		{
			name:     "a configured variable is named in the fix",
			provider: "github",
			auth:     Auth{TokenEnv: "GITHUB_TOKEN"},
			wantIn:   []string{"GITHUB_TOKEN", "FLOW_GITHUB_TOKEN", "auth.github.token_cmd"},
		},
		{
			name:     "the fallback is named when nothing is configured",
			provider: "github-projects",
			wantIn:   []string{"FLOW_GITHUB_PROJECTS_TOKEN"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := NewCredentials(env(nil), nil)

			_, err := credentials.Token(t.Context(), test.provider, test.auth)
			if err == nil {
				t.Fatal("Token succeeded with no credential anywhere")
			}
			if !errors.Is(err, core.ErrPermission) {
				t.Errorf("error is %v, want it to wrap ErrPermission", err)
			}
			if got := core.ExitCode(err); got != core.ExitNoPerm {
				t.Errorf("exit code = %d, want %d", got, core.ExitNoPerm)
			}
			var user *core.UserError
			if !errors.As(err, &user) {
				t.Fatalf("error %v is not a UserError, so it cannot explain itself", err)
			}
			whole := fmt.Sprintf("%s %s %s", user.What, user.Why, user.Fix)
			for _, want := range test.wantIn {
				if !strings.Contains(whole, want) {
					t.Errorf("error does not mention %q:\n%s", want, whole)
				}
			}
		})
	}
}

func TestTokenEnvMustBeAName(t *testing.T) {
	// The mistake this guards against is pasting the token itself where its
	// name belongs. flow must refuse, and must not repeat the value back.
	pasted := fakeToken
	credentials := NewCredentials(env(map[string]string{pasted: "ignored"}), nil)

	_, err := credentials.Token(t.Context(), "github", Auth{TokenEnv: pasted})
	if err == nil {
		t.Fatal("Token accepted a secret where a variable name belongs")
	}
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("error is %v, want it to wrap ErrValidation", err)
	}
	if strings.Contains(err.Error(), pasted) {
		t.Errorf("the error repeats the pasted value back: %v", err)
	}
}

func TestIsEnvName(t *testing.T) {
	names := map[string]bool{
		"GITHUB_TOKEN":    true,
		"_private":        true,
		"a1":              true,
		"":                false,
		"1GITHUB":         false,
		"GITHUB-TOKEN":    false,
		"$GITHUB_TOKEN":   false,
		"GITHUB_TOKEN=x":  false,
		fakeToken:         true, // name-shaped; checkEnvName refuses it on its shape instead
		"token value":     false,
		"github.token":    false,
		"FLOW_PROJECT_TO": true,
	}
	for name, want := range names {
		if got := IsEnvName(name); got != want {
			t.Errorf("IsEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFallbackTokenEnv(t *testing.T) {
	for provider, want := range map[string]string{
		"github":          "FLOW_GITHUB_TOKEN",
		"github-projects": "FLOW_GITHUB_PROJECTS_TOKEN",
		"jira-cloud":      "FLOW_JIRA_CLOUD_TOKEN",
		"linear":          "FLOW_LINEAR_TOKEN",
	} {
		if got := FallbackTokenEnv(provider); got != want {
			t.Errorf("FallbackTokenEnv(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestTokenCmdFailures(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		err      error
		sentinel error
		wantIn   string
	}{
		{
			name:     "a missing command is a configuration error",
			err:      fmt.Errorf("exec: %w", exec.ErrNotFound),
			sentinel: core.ErrValidation,
			wantIn:   "could not find",
		},
		{
			name:     "an absent absolute path is a configuration error",
			err:      &fs.PathError{Op: "fork/exec", Path: "/nope/helper", Err: fs.ErrNotExist},
			sentinel: core.ErrValidation,
			wantIn:   "could not find",
		},
		{
			name:     "a command flow may not run is a configuration error",
			err:      &fs.PathError{Op: "fork/exec", Path: "/bin/helper", Err: fs.ErrPermission},
			sentinel: core.ErrValidation,
			wantIn:   "not allowed to run",
		},
		{
			name:     "a failing command is unavailable",
			err:      errors.New("exit status 1"),
			sentinel: core.ErrUnavailable,
			wantIn:   "failed",
		},
		{
			name:     "a silent command cannot authenticate",
			out:      "\n  \n",
			sentinel: core.ErrPermission,
			wantIn:   "printed nothing",
		},
		{
			name:     "more than one line is not a token",
			out:      "line one\nline two\n",
			sentinel: core.ErrValidation,
			wantIn:   "more than one line",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := map[string]AuthSource{"github": {TokenCmd: []string{"helper", "--quiet"}}}
			credentials := NewCredentials(env(nil), auth).WithRunner(fixedRunner(test.out, test.err))

			_, err := credentials.Token(t.Context(), "github", Auth{})
			if err == nil {
				t.Fatal("Token succeeded")
			}
			if !errors.Is(err, test.sentinel) {
				t.Errorf("error is %v, want it to wrap %v", err, test.sentinel)
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Errorf("error %q does not explain %q", err, test.wantIn)
			}
			if !strings.Contains(err.Error(), "auth.github.token_cmd") {
				t.Errorf("error %q does not say which setting is at fault", err)
			}
		})
	}
}

func TestTokenCmdIsBounded(t *testing.T) {
	started := make(chan struct{})
	runner := func(ctx context.Context, _ []string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	auth := map[string]AuthSource{"github": {TokenCmd: []string{"waits-forever"}}}
	credentials := NewCredentials(env(nil), auth).WithRunner(runner).WithTimeout(10 * time.Millisecond)

	_, err := credentials.Token(t.Context(), "github", Auth{})
	<-started
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("error is %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error %q does not say the helper timed out", err)
	}
}

func TestTokenCmdStderrIsRedacted(t *testing.T) {
	// A helper that fails often explains itself on stderr, and what it quotes
	// back may be the very secret flow is trying not to see.
	leak := "could not use token " + fakeToken
	runner := func(context.Context, []string) ([]byte, error) {
		return nil, &exec.ExitError{Stderr: []byte(leak + "\n")}
	}
	auth := map[string]AuthSource{"github": {TokenCmd: []string{"helper"}}}
	credentials := NewCredentials(env(nil), auth).WithRunner(runner)

	_, err := credentials.Token(t.Context(), "github", Auth{})
	if err == nil {
		t.Fatal("Token succeeded")
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Errorf("the helper's stderr leaked a token into %q", err)
	}
	if !strings.Contains(err.Error(), "could not use token") {
		t.Errorf("error %q dropped the helper's explanation entirely", err)
	}
}

func TestCredentialKeepsItsSecretToItself(t *testing.T) {
	secret := fakeToken
	credentials := NewCredentials(env(map[string]string{"GITHUB_TOKEN": secret}), nil)

	credential, err := credentials.Token(t.Context(), "github", Auth{TokenEnv: "GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if credential.Secret() != secret {
		t.Fatalf("Secret() = %q, want the token", credential.Secret())
	}

	for _, rendering := range []string{
		credential.String(),
		fmt.Sprint("credential=", credential),
		fmt.Sprintf("credential=%v", credential),
		fmt.Sprintf("credential=%+v", credential),
		fmt.Errorf("wrapped: %v", credential).Error(),
	} {
		if strings.Contains(rendering, secret) {
			t.Errorf("a Credential rendered as %q, which discloses the token", rendering)
		}
		if !strings.Contains(rendering, "$GITHUB_TOKEN") {
			t.Errorf("a Credential rendered as %q, which does not say where it came from", rendering)
		}
	}

	var logged strings.Builder
	log := slog.New(core.NewRedactingHandler(slog.NewTextHandler(&logged, nil)))
	log.Info("resolved", "credential", credential)
	if strings.Contains(logged.String(), secret) {
		t.Errorf("logging a Credential wrote the token: %s", logged.String())
	}
	if !strings.Contains(logged.String(), "credential.source=$GITHUB_TOKEN") {
		t.Errorf("logging a Credential lost its source: %s", logged.String())
	}
}

func TestZeroCredential(t *testing.T) {
	var credential Credential
	if !credential.IsZero() {
		t.Error("the zero Credential does not report itself empty")
	}
	if got := credential.String(); got != "no credential" {
		t.Errorf("String() = %q, want %q", got, "no credential")
	}
}

func TestTokenIsResolvedOncePerProvider(t *testing.T) {
	var mu sync.Mutex
	runs := map[string]int{}
	runner := func(_ context.Context, argv []string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		runs[argv[0]]++
		return []byte("token-for-" + argv[0]), nil
	}
	auth := map[string]AuthSource{
		"github":          {TokenCmd: []string{"forge-helper"}},
		"github-projects": {TokenCmd: []string{"tracker-helper"}},
	}
	credentials := NewCredentials(env(nil), auth).WithRunner(runner)

	var wait sync.WaitGroup
	for provider := range auth {
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				credential, err := credentials.Token(t.Context(), provider, Auth{})
				if err != nil {
					t.Errorf("Token(%s): %v", provider, err)
					return
				}
				if credential.Provider != provider {
					t.Errorf("Token(%s) resolved %s", provider, credential.Provider)
				}
			}()
		}
	}
	wait.Wait()

	if runs["forge-helper"] != 1 || runs["tracker-helper"] != 1 {
		t.Errorf("helpers ran %v, want each exactly once: a vault should be unlocked once per process", runs)
	}
}

func TestEmail(t *testing.T) {
	credentials := NewCredentials(env(map[string]string{"JIRA_EMAIL": " someone@example.com\n"}), nil)

	got, err := credentials.Email("jira-cloud", Auth{EmailEnv: "JIRA_EMAIL"})
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if want := "someone@example.com"; got != want {
		t.Errorf("Email = %q, want %q", got, want)
	}

	if _, err := credentials.Email("jira-cloud", Auth{}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("Email without email_env returned %v, want ErrValidation", err)
	}
	_, err = credentials.Email("jira-cloud", Auth{EmailEnv: "JIRA_EMAIL_UNSET"})
	if !errors.Is(err, core.ErrPermission) {
		t.Fatalf("Email with an unset variable returned %v, want ErrPermission", err)
	}
	if !strings.Contains(err.Error(), "JIRA_EMAIL_UNSET") {
		t.Errorf("error %q does not name the variable to set", err)
	}
}

// TestDefaultRunnerExecutesDirectly runs a real process: the test binary
// itself, re-entered as a credential helper. It proves the default runner
// takes argv as argv — no shell, so nothing in a token_cmd is expanded,
// globbed or treated as a second command.
func TestDefaultRunnerExecutesDirectly(t *testing.T) {
	const metacharacters = "tok$(whoami)en; echo more > /dev/null"

	credentials := NewCredentials(env(nil), map[string]AuthSource{
		"github": {TokenCmd: helperArgv(t, metacharacters)},
	})

	credential, err := credentials.Token(t.Context(), "github", Auth{})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if credential.Secret() != metacharacters {
		t.Errorf("secret = %q, want the argument passed through untouched: %q", credential.Secret(), metacharacters)
	}
}

func TestDefaultRunnerReportsAMissingCommand(t *testing.T) {
	for _, argv := range [][]string{
		{"flow-no-such-credential-helper"},
		{filepath.Join(t.TempDir(), "no-such-credential-helper")},
	} {
		credentials := NewCredentials(env(nil), map[string]AuthSource{"github": {TokenCmd: argv}})

		_, err := credentials.Token(t.Context(), "github", Auth{})
		if !errors.Is(err, core.ErrValidation) {
			t.Errorf("Token with %v returned %v, want ErrValidation", argv, err)
		}
	}
}

func TestDefaultRunnerDoesNotWaitOnInput(t *testing.T) {
	// A helper that reads stdin must see EOF at once rather than inherit the
	// terminal and hang: flow would otherwise stop dead with no explanation.
	credentials := NewCredentials(env(nil), map[string]AuthSource{
		"github": {TokenCmd: helperArgv(t, "", "-read-stdin")},
	}).WithTimeout(10 * time.Second)

	credential, err := credentials.Token(t.Context(), "github", Auth{})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if want := "stdin-was-empty"; credential.Secret() != want {
		t.Errorf("secret = %q, want %q", credential.Secret(), want)
	}
}

// helperArgv is a command line that re-enters this test binary as
// TestCredentialHelperProcess, printing token on stdout.
func helperArgv(t *testing.T, token string, extra ...string) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Skipf("the test binary is not executable on this platform: %v", err)
	}
	argv := []string{binary, "-test.run=TestCredentialHelperProcess", "-test.v=false", "--", "flow-credential-helper", token}
	return append(argv, extra...)
}

// TestCredentialHelperProcess is not a test: it is the body of the credential
// helper the two tests above run. It does nothing unless it is handed its own
// sentinel argument.
func TestCredentialHelperProcess(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "flow-credential-helper" {
		args = args[1:]
	}
	if len(args) < 2 {
		return
	}
	token := args[1]
	if len(args) > 2 && args[2] == "-read-stdin" {
		buffer, err := io.ReadAll(os.Stdin)
		if err == nil && len(buffer) == 0 {
			token = "stdin-was-empty"
		} else {
			token = fmt.Sprintf("stdin-had-%d-bytes-err-%v", len(buffer), err)
		}
	}
	fmt.Fprintln(os.Stdout, token)
	os.Exit(0)
}
