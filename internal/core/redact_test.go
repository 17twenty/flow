package core_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// Sample credentials. These are syntactically valid shapes and deliberately not
// real. Each is assembled from parts at run time: a scanner-shaped literal in a
// source file trips GitHub's push protection, even in a test fixture.
const credBody = "0123456789abcdefghijABCDEFGHIJ0123"

var (
	ghp    = "ghp" + "_" + credBody
	gho    = "gho" + "_" + credBody
	ghu    = "ghu" + "_" + credBody
	ghs    = "ghs" + "_" + credBody
	ghr    = "ghr" + "_" + credBody
	ghPAT  = "github" + "_pat_" + "11ABCDEFG0" + credBody
	glpat  = "glpat" + "-" + "ABCDEFGhijklmnop1234"
	linAPI = "lin" + "_api_" + "ABCDEFGhijklmnop1234567890"
	atatt  = "ATATT" + "3xFfGF0abcdefghijklmnop1234567890="
	jwt    = "eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMiJ9." + "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1g"
)

func TestRedactValuePatterns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, wantKind string
	}{
		{"github personal", ghp, "github_token"},
		{"github oauth", gho, "github_token"},
		{"github user-to-server", ghu, "github_token"},
		{"github server-to-server", ghs, "github_token"},
		{"github refresh", ghr, "github_token"},
		{"github fine-grained pat", ghPAT, "github_pat"},
		{"gitlab pat", glpat, "gitlab_token"},
		{"linear api key", linAPI, "linear_key"},
		{"atlassian token", atatt, "atlassian_token"},
		{"jwt", jwt, "jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := core.Redact("authenticating with " + tc.in + " now")
			if strings.Contains(got, tc.in) {
				t.Fatalf("credential survived redaction: %q", got)
			}
			if want := "[REDACTED:" + tc.wantKind + "]"; !strings.Contains(got, want) {
				t.Errorf("Redact(%s) = %q, want it to contain %q", tc.name, got, want)
			}
		})
	}
}

func TestRedactURLParams(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
	}{
		{"token", "https://api.example.com/v1/items?token=s3cret-value&page=2"},
		{"access_token", "https://api.example.com/v1?access_token=s3cret-value"},
		{"private_token", "https://gitlab.example.com/api/v4?private_token=s3cret-value"},
		{"key", "https://api.example.com/v1?key=s3cret-value"},
		{"api_key suffix", "https://api.example.com/v1?api_key=s3cret-value"},
		{"uppercase", "https://api.example.com/v1?TOKEN=s3cret-value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := core.Redact(tc.in)
			if strings.Contains(got, "s3cret-value") {
				t.Fatalf("query parameter survived redaction: %q", got)
			}
			if !strings.Contains(got, "[REDACTED:secret]") {
				t.Errorf("Redact(%q) = %q, want a redaction marker", tc.in, got)
			}
		})
	}
}

// Non-secret query parameters and ordinary text must be left alone.
func TestRedactLeavesSafeTextAlone(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"https://api.github.com/repos/17twenty/flow/pulls?state=open&page=2",
		"moved ticket #42 to In review",
		"",
	} {
		if got := core.Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestRedactKeyValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key, value, want string
	}{
		{"token", "anything at all", "[REDACTED:secret]"},
		{"secret", "anything at all", "[REDACTED:secret]"},
		{"password", "hunter2", "[REDACTED:secret]"},
		{"Authorization", "Bearer abc", "[REDACTED:secret]"},
		{"api_key", "abc", "[REDACTED:secret]"},
		{"api-key", "abc", "[REDACTED:secret]"},
		{"apikey", "abc", "[REDACTED:secret]"},
		{"cookie", "a=b", "[REDACTED:secret]"},
		{"session", "xyz", "[REDACTED:secret]"},
		{"GITHUB_TOKEN", "abc", "[REDACTED:secret]"},
		{"repo", "17twenty/flow", "17twenty/flow"},
		{"repo", ghp, "[REDACTED:github_token]"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Parallel()
			if got := core.RedactKeyValue(tc.key, tc.value); got != tc.want {
				t.Errorf("RedactKeyValue(%q, %q) = %q, want %q", tc.key, tc.value, got, tc.want)
			}
		})
	}
}

// newTestLogger returns a logger writing redacted JSON into buf.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	inner := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(core.NewRedactingHandler(inner))
}

func TestRedactingHandler(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("calling "+ghp, "token", ghp, "repo", "17twenty/flow",
		"url", "https://api.example.com/v1?access_token=s3cret-value")

	out := buf.String()
	for _, leaked := range []string{ghp, "s3cret-value"} {
		if strings.Contains(out, leaked) {
			t.Errorf("handler leaked %q in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "17twenty/flow") {
		t.Errorf("handler dropped a safe attribute: %q", out)
	}
}

// Secrets must not survive WithAttrs or WithGroup either.
func TestRedactingHandlerWithAttrsAndGroups(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newTestLogger(&buf).With("token", ghp).WithGroup("forge")
	log.Info("probe", slog.Group("auth", slog.String("secret", glpat), slog.String("kind", "app")))

	out := buf.String()
	for _, leaked := range []string{ghp, glpat} {
		if strings.Contains(out, leaked) {
			t.Errorf("handler leaked %q in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "app") {
		t.Errorf("handler dropped a safe grouped attribute: %q", out)
	}
}

// A LogValuer must be resolved before redaction, not after.
type credential struct{ raw string }

func (c credential) LogValue() slog.Value { return slog.StringValue(c.raw) }

func TestRedactingHandlerResolvesLogValuer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	newTestLogger(&buf).Info("auth", "cred", credential{raw: jwt})
	if out := buf.String(); strings.Contains(out, jwt) {
		t.Errorf("handler leaked a LogValuer secret: %q", out)
	}
}

func TestRedactingHandlerEnabled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := core.NewRedactingHandler(inner)
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Enabled must defer to the inner handler")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled must allow levels the inner handler accepts")
	}
}
