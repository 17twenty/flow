package main

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// exec runs the CLI in-process and returns its exit code with both streams.
func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// Help must present the four groups of spec 02 §2, in order.
func TestHelpShowsCommandGroups(t *testing.T) {
	code, stdout, _ := exec(t, "--help")
	if code != core.ExitOK {
		t.Fatalf("flow --help exited %d, want %d", code, core.ExitOK)
	}
	at := -1
	for _, group := range []string{"Setup", "Verify", "CI", "Meta"} {
		i := strings.Index(stdout, "\n"+group+"\n")
		if i < 0 {
			t.Fatalf("help is missing the %q group:\n%s", group, stdout)
		}
		if i < at {
			t.Errorf("group %q appears out of order in help:\n%s", group, stdout)
		}
		at = i
	}
}

// Every command spec 00 §5 promises must be reachable from the CLI.
func TestHelpListsEveryDocumentedCommand(t *testing.T) {
	_, stdout, _ := exec(t, "--help")
	for _, cmd := range []string{
		"init", "plan", "apply", "attest", "doctor",
		"check pr", "check deploy", "check commit",
		"hook install", "hook uninstall", "schema", "version",
	} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("help does not list %q:\n%s", cmd, stdout)
		}
	}
}

// The shipped CLI surface is exactly spec 02; test-only commands must appear
// only when the flowtest tag is set.
func TestTestCommandsAreBuildTagged(t *testing.T) {
	_, stdout, _ := exec(t, "--help")
	hasSleep := strings.Contains(stdout, "sleep")
	if wantSleep := len(testCommands()) > 0; hasSleep != wantSleep {
		t.Errorf("help lists sleep = %v, want %v for this build", hasSleep, wantSleep)
	}
}

// Unimplemented commands are the only permitted stub, and they must fail
// cleanly as ErrUnsupported rather than pretending to work (plan item 0.1).
// Commands drop off this list as their plan item lands: check commit did so
// with item 1.3, schema with item 1.9, hook with item 1.13, init with item
// 4.1, plan and apply with item 4.2, check pr with item 4.3, check deploy
// with item 4.4, doctor with item 4.5, attest with item 4.6 and
// doctor --report with item 5.7. A flag whose own item has not landed is on
// the list too, because doctor accepting it and ignoring it would be the stub
// this test exists to forbid.
func TestUnimplementedCommandsExitFail(t *testing.T) {
	cases := [][]string{
		{"doctor", "--ci"},
		{"doctor", "--watch"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := exec(t, args...)
			if want := core.ExitCode(core.ErrUnsupported); code != want {
				t.Errorf("exited %d, want %d (stderr: %s)", code, want, stderr)
			}
			if stdout != "" {
				t.Errorf("a failing command wrote to stdout: %q", stdout)
			}
			if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "fix:") {
				t.Errorf("error output lacks the what/why/fix shape (P10):\n%s", stderr)
			}
		})
	}
}

// Kong parse failures are usage errors (spec 02 §4).
func TestParseErrorsExitUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown command", []string{"nosuchcommand"}},
		{"unknown flag", []string{"version", "--nosuchflag"}},
		{"bad enum", []string{"version", "--log-level=silly"}},
		{"bad duration", []string{"--timeout=soon", "version"}},
		{"missing required flag", []string{"attest", "some.check"}},
		{"bad subcommand", []string{"check", "nothing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := exec(t, tc.args...)
			if code != core.ExitUsage {
				t.Errorf("exited %d, want %d (stderr: %s)", code, core.ExitUsage, stderr)
			}
			if !strings.Contains(stderr, "error:") {
				t.Errorf("usage error not reported on stderr:\n%s", stderr)
			}
		})
	}
}

func TestVersionHuman(t *testing.T) {
	code, stdout, _ := exec(t, "version")
	if code != core.ExitOK {
		t.Fatalf("flow version exited %d", code)
	}
	if !strings.HasPrefix(stdout, "flow ") {
		t.Errorf("version output should start with the binary name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "go:") {
		t.Errorf("version output should report the toolchain:\n%s", stdout)
	}
}

func TestVersionJSON(t *testing.T) {
	code, stdout, _ := exec(t, "version", "--json")
	if code != core.ExitOK {
		t.Fatalf("flow version --json exited %d", code)
	}
	var doc versionDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, stdout)
	}
	if want := core.SchemaID("version"); doc.Schema != want {
		t.Errorf("schema = %q, want %q", doc.Schema, want)
	}
	for name, got := range map[string]string{
		"version": doc.Version, "go": doc.Go, "os": doc.OS, "arch": doc.Arch,
	} {
		if got == "" {
			t.Errorf("JSON field %q is empty", name)
		}
	}
}

func TestBuildDetails(t *testing.T) {
	t.Parallel()
	info := &debug.BuildInfo{
		GoVersion: "go1.26.4",
		Main:      debug.Module{Path: "github.com/17twenty/flow", Version: "v0.3.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "2026-10-11T07:22:51Z"},
			{Key: "vcs.modified", Value: "true"},
			{Key: "GOOS", Value: "linux"},
			{Key: "GOARCH", Value: "arm64"},
		},
	}

	t.Run("ldflags version wins over module version", func(t *testing.T) {
		t.Parallel()
		got := buildDetails("v1.2.3", info, true)
		if got.Version != "v1.2.3" {
			t.Errorf("Version = %q, want the injected v1.2.3", got.Version)
		}
	})

	t.Run("falls back to module version", func(t *testing.T) {
		t.Parallel()
		got := buildDetails(devVersion, info, true)
		if got.Version != "v0.3.0" {
			t.Errorf("Version = %q, want the module's v0.3.0", got.Version)
		}
	})

	t.Run("reads vcs settings", func(t *testing.T) {
		t.Parallel()
		got := buildDetails(devVersion, info, true)
		if got.Commit != "deadbeef" || got.Built != "2026-10-11T07:22:51Z" || !got.Modified {
			t.Errorf("vcs settings not read: %+v", got)
		}
		if got.OS != "linux" || got.Arch != "arm64" {
			t.Errorf("target platform not read: %s/%s", got.OS, got.Arch)
		}
		if got.Go != "go1.26.4" || got.Module != "github.com/17twenty/flow" {
			t.Errorf("toolchain or module not read: %+v", got)
		}
	})

	t.Run("devel module version is not a version", func(t *testing.T) {
		t.Parallel()
		devel := *info
		devel.Main = debug.Module{Path: info.Main.Path, Version: "(devel)"}
		if got := buildDetails(devVersion, &devel, true); got.Version != devVersion {
			t.Errorf("Version = %q, want %q", got.Version, devVersion)
		}
	})

	t.Run("survives missing build info", func(t *testing.T) {
		t.Parallel()
		got := buildDetails("", nil, false)
		if got.Version != devVersion {
			t.Errorf("Version = %q, want %q", got.Version, devVersion)
		}
		if got.Schema != core.SchemaID("version") || got.Go == "" || got.OS == "" || got.Arch == "" {
			t.Errorf("runtime fallbacks not populated: %+v", got)
		}
	})
}
