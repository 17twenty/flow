package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// DefaultTokenCmdTimeout bounds a credential helper. Helpers that prompt for a
// fingerprint or unlock a vault take a moment; one that has not answered by
// now is waiting for something flow cannot give it (spec 03 §5).
const DefaultTokenCmdTimeout = 30 * time.Second

// Credential is a resolved secret and a note of where it came from.
//
// The secret is unexported and the type formats itself as its source, so that
// printing, logging or wrapping a Credential in an error cannot leak it. The
// only way to the value is [Credential.Secret] (spec 15 §1).
type Credential struct {
	secret string
	// Provider is the adapter kind the credential authenticates, as written
	// in .flow.yaml: github, github-projects, jira-cloud, linear.
	Provider string
	// Source says where the value came from, in the user's terms: either
	// "$NAME" for an environment variable or "auth.<provider>.token_cmd".
	Source string
}

// Secret returns the credential itself. Every call site that uses this is
// sending the value to the provider it belongs to and nowhere else.
func (c Credential) Secret() string { return c.secret }

// IsZero reports whether the credential holds nothing.
func (c Credential) IsZero() bool { return c.secret == "" }

// String describes the credential without disclosing it.
func (c Credential) String() string {
	if c.IsZero() {
		return "no credential"
	}
	return fmt.Sprintf("%s credential from %s", c.Provider, c.Source)
}

// LogValue keeps the secret out of logs even when a Credential is passed to
// slog whole.
func (c Credential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("provider", c.Provider),
		slog.String("source", c.Source),
	)
}

// CommandRunner runs a credential helper and returns what it wrote to stdout.
// It exists so tests never execute anything.
type CommandRunner func(ctx context.Context, argv []string) ([]byte, error)

// Credentials resolves each provider's credential from the environment and the
// user's own configuration, in the order spec 03 §5 lays down:
//
//  1. the variable named by `.flow.yaml` `auth.token_env`;
//  2. the fallback variable `FLOW_<PROVIDER>_TOKEN`;
//  3. the user's `auth.<provider>.token_cmd`.
//
// The first source that holds a value wins. A resolved credential is kept, so
// a helper that unlocks a vault runs once per process rather than once per
// request.
//
// The zero value is unusable: build one with [NewCredentials].
type Credentials struct {
	getenv  func(string) string
	auth    map[string]AuthSource
	run     CommandRunner
	timeout time.Duration

	mu    sync.Mutex
	cache map[string]*resolved
}

// resolved is one provider's credential, and the lock that makes sure it is
// looked up once rather than once per caller.
type resolved struct {
	mu         sync.Mutex
	credential Credential
}

// NewCredentials builds a resolver. getenv reads the environment and auth is
// the resolved personal `auth` table — [Settings.Auth].
//
// Only the user's own TOML may name a token_cmd: [ParsePersonal] refuses one
// in a repository-local file, so by the time a command reaches this resolver
// every command it might run came from a file only the user can write
// (spec 15 §1).
func NewCredentials(getenv func(string) string, auth map[string]AuthSource) *Credentials {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return &Credentials{getenv: getenv, auth: auth, run: runCommand, timeout: DefaultTokenCmdTimeout}
}

// WithRunner replaces how credential helpers are run, and returns c.
func (c *Credentials) WithRunner(run CommandRunner) *Credentials {
	c.run = run
	return c
}

// WithTimeout replaces how long a credential helper may take, and returns c.
func (c *Credentials) WithTimeout(d time.Duration) *Credentials {
	c.timeout = d
	return c
}

// Token resolves the API token for one provider. auth is that provider's
// `auth:` block from `.flow.yaml`, which names variables and never holds a
// secret.
//
// Concurrent callers for the same provider wait for one another, so a helper
// that unlocks a vault is not asked twice. Failures are not remembered: only a
// credential flow actually holds is worth keeping.
//
// The returned error never contains the credential, and names the variable to
// set when there is nothing to read.
func (c *Credentials) Token(ctx context.Context, provider string, auth Auth) (Credential, error) {
	entry := c.entry(provider)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.credential.IsZero() {
		return entry.credential, nil
	}

	credential, err := c.resolve(ctx, provider, auth)
	if err != nil {
		return Credential{}, err
	}
	entry.credential = credential
	return credential, nil
}

// entry returns the slot one provider's credential is resolved in.
func (c *Credentials) entry(provider string) *resolved {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]*resolved{}
	}
	entry, ok := c.cache[provider]
	if !ok {
		entry = &resolved{}
		c.cache[provider] = entry
	}
	return entry
}

// resolve walks the sources in precedence order.
func (c *Credentials) resolve(ctx context.Context, provider string, auth Auth) (Credential, error) {
	named := auth.TokenEnv
	if named != "" {
		if err := checkEnvName(fmt.Sprintf("%s auth.token_env", provider), named); err != nil {
			return Credential{}, err
		}
		if value := strings.TrimSpace(c.getenv(named)); value != "" {
			return Credential{secret: value, Provider: provider, Source: "$" + named}, nil
		}
	}

	fallback := FallbackTokenEnv(provider)
	if fallback != named {
		if value := strings.TrimSpace(c.getenv(fallback)); value != "" {
			return Credential{secret: value, Provider: provider, Source: "$" + fallback}, nil
		}
	}

	if argv := c.auth[provider].TokenCmd; len(argv) > 0 {
		value, err := c.fromCommand(ctx, provider, argv)
		if err != nil {
			return Credential{}, err
		}
		return Credential{secret: value, Provider: provider, Source: fmt.Sprintf("auth.%s.token_cmd", provider)}, nil
	}

	return Credential{}, missingCredential(provider, named, fallback)
}

// fromCommand runs the user's credential helper and reads the token from its
// standard output.
func (c *Credentials) fromCommand(ctx context.Context, provider string, argv []string) (string, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultTokenCmdTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	what := fmt.Sprintf("read the %s credential", provider)
	out, err := c.run(ctx, argv)
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", core.Errorf(core.ErrUnavailable, "%s", what).
			WithWhy("auth.%s.token_cmd (%s) did not finish within %s", provider, argv[0], timeout).
			WithFix("run it yourself to see what it is waiting for, or set a credential in the environment instead")
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return "", core.Errorf(core.ErrValidation, "%s", what).
			WithWhy("auth.%s.token_cmd names %s, which flow could not find", provider, argv[0]).
			WithFix("install it, or give token_cmd the absolute path to it")
	case errors.Is(err, fs.ErrPermission):
		return "", core.Errorf(core.ErrValidation, "%s", what).
			WithWhy("auth.%s.token_cmd names %s, which flow is not allowed to run", provider, argv[0]).
			WithFix("chmod +x %s", argv[0])
	default:
		return "", core.Errorf(core.ErrUnavailable, "%s", what).
			WithWhy("auth.%s.token_cmd (%s) failed: %s", provider, argv[0], commandFailure(err)).
			WithFix("run it yourself to see what went wrong")
	}

	token := strings.TrimSpace(string(out))
	switch {
	case token == "":
		return "", core.Errorf(core.ErrPermission, "%s", what).
			WithWhy("auth.%s.token_cmd (%s) printed nothing", provider, argv[0]).
			WithFix("make it print the token on standard output, and nothing else")
	case strings.ContainsAny(token, "\r\n"):
		return "", core.Errorf(core.ErrValidation, "%s", what).
			WithWhy("auth.%s.token_cmd (%s) printed more than one line; a token is one line", provider, argv[0]).
			WithFix("pipe it through head -n1, or use a command that prints the token alone")
	}
	return token, nil
}

// Email resolves the account email some providers pair with their API token,
// such as Jira Cloud. It is not a secret, so it comes back as a plain string.
func (c *Credentials) Email(provider string, auth Auth) (string, error) {
	if auth.EmailEnv == "" {
		return "", core.Errorf(core.ErrValidation, "read the %s account email", provider).
			WithWhy("%s needs an email alongside its API token, and no auth.email_env is configured", provider).
			WithFix("add auth: { email_env: JIRA_EMAIL } to the %s block in %s", provider, DefaultPath)
	}
	if err := checkEnvName(fmt.Sprintf("%s auth.email_env", provider), auth.EmailEnv); err != nil {
		return "", err
	}
	email := strings.TrimSpace(c.getenv(auth.EmailEnv))
	if email == "" {
		return "", core.Errorf(core.ErrPermission, "read the %s account email", provider).
			WithWhy("$%s is not set", auth.EmailEnv).
			WithFix("export %s=<the email your API token belongs to>", auth.EmailEnv)
	}
	return email, nil
}

// missingCredential explains that nothing was configured, naming the variables
// that would have been read.
func missingCredential(provider, named, fallback string) error {
	why := fmt.Sprintf("$%s is not set", fallback)
	fix := fmt.Sprintf("export %s=<token>", fallback)
	if named != "" && named != fallback {
		why = fmt.Sprintf("neither $%s nor $%s is set", named, fallback)
		fix = fmt.Sprintf("export %s=<token>", named)
	}
	return core.Errorf(core.ErrPermission, "read the %s credential", provider).
		WithWhy("%s, and your user config has no auth.%s.token_cmd", why, provider).
		WithFix("%s, or add [auth.%s] token_cmd = [\"…\"] to your flow config", fix, provider)
}

// FallbackTokenEnv is the variable flow reads when `.flow.yaml` names none, or
// names one that is not set: the provider kind, upper-cased, between FLOW_ and
// _TOKEN (spec 03 §5).
func FallbackTokenEnv(provider string) string {
	return "FLOW_" + envSuffix(provider) + "_TOKEN"
}

// envSuffix turns an adapter kind into the middle of an environment variable
// name: github-projects becomes GITHUB_PROJECTS.
func envSuffix(provider string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(provider) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// IsEnvName reports whether s is a plausible environment variable name.
func IsEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// checkEnvName refuses anything that is not a variable name, and anything
// shaped like a credential even if it would pass as one. The usual way to get
// here is pasting a token where its name belongs, so the message says so
// without repeating the value back (spec 03 §1.1, spec 15 §1).
func checkEnvName(where, value string) error {
	switch {
	case core.Redact(value) != value:
		return core.Errorf(core.ErrValidation, "use %s", where).
			WithWhy("the value is a credential, not the name of one, and flow will not keep a secret in a file the whole team can read").
			WithFix("export it instead, and name the variable here")
	case !IsEnvName(value):
		return core.Errorf(core.ErrValidation, "use %s", where).
			WithWhy("it must be the NAME of an environment variable, and this is not one").
			WithFix("name a variable such as GITHUB_TOKEN; flow stores names, never secrets")
	}
	return nil
}

// commandFailure describes why a helper failed, without its output. A helper
// that fails often prints the reason on stderr, which may quote the very
// secret flow is trying not to see, so only the exit status is reported.
func commandFailure(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if hint := firstLine(exit.Stderr); hint != "" {
			return fmt.Sprintf("%s: %s", exit.ProcessState.String(), core.Redact(hint))
		}
		return exit.ProcessState.String()
	}
	return core.Redact(err.Error())
}

// firstLine is the first non-empty line of a helper's stderr, trimmed to a
// length that fits on one line of a terminal.
func firstLine(b []byte) string {
	for line := range bytes.Lines(b) {
		if text := strings.TrimSpace(string(line)); text != "" {
			if len(text) > 120 {
				return text[:120] + "…"
			}
			return text
		}
	}
	return ""
}

// runCommand runs a credential helper directly, never through a shell, so that
// nothing in a token_cmd is interpreted as a command of its own (spec 15 §1).
func runCommand(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// A helper has nothing to read: give it an immediate EOF rather than the
	// terminal, so one that asks a question fails instead of hanging.
	cmd.Stdin = strings.NewReader("")
	// Do not wait forever on a child that leaves a grandchild holding stdout.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}
