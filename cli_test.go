package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/alecthomas/kong"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/forge"
	"github.com/17twenty/flow/internal/ui"
)

// How an invocation decides what it is writing to (spec 02 §1, spec 12).
// The terminal is a parameter here rather than a question asked of the real
// one, because a test is never a terminal and the interesting half of the
// decision only happens when it is.

// TestColourIsForAPersonWatching: everything that says nobody is watching
// turns it off, and the flag `doctor --ci` sets is one of them — a log read
// a week later by somebody who was not there is not a terminal, whatever the
// runner allocated (spec 02 §2).
func TestColourIsForAPersonWatching(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	cases := []struct {
		name     string
		ci       bool
		flags    CLI
		settings config.Settings
		terminal bool
		colour   bool
	}{
		{name: "a person at a terminal", terminal: true, colour: true},
		{name: "output going somewhere else", terminal: false, colour: false},
		{name: "a CI job", ci: true, terminal: true, colour: false},
		{name: "--no-color", flags: CLI{NoColor: true}, terminal: true, colour: false},
		{name: "the user's own configuration", settings: config.Settings{NoColor: true}, terminal: true, colour: false},
		{name: "--json", flags: CLI{JSON: true}, terminal: true, colour: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags := tc.flags
			app := &Context{CLI: &flags, CI: tc.ci}
			opts := app.options(tc.settings, tc.terminal)
			if opts.Colour != tc.colour {
				t.Errorf("colour = %v, want %v", opts.Colour, tc.colour)
			}
		})
	}
}

// TestNoColorIsHonouredWhoeverSetIt: the environment variable is the one a
// person sets once for every tool on the machine.
func TestNoColorIsHonouredWhoeverSetIt(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	app := &Context{CLI: &CLI{}}
	if app.options(config.Settings{}, true).Colour {
		t.Error("NO_COLOR was set and flow coloured its output anyway")
	}
}

// TestTheThemeIsOnlyAskedForWhenItIsPainting: a light theme and a dark one
// are the same bytes with no colour in them, so a run that is not colouring
// must not carry a background it did not look at.
func TestTheThemeIsOnlyAskedForWhenItIsPainting(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	app := &Context{CLI: &CLI{}, CI: true}
	if opts := app.options(config.Settings{Theme: config.ThemeDark}, true); opts.Dark {
		t.Errorf("a CI job was told the background is dark: %+v", opts)
	}
	lit := &Context{CLI: &CLI{}}
	if opts := lit.options(config.Settings{Theme: config.ThemeLight}, true); opts.Format != ui.FormatText || opts.Dark {
		t.Errorf("a light terminal was painted dark: %+v", opts)
	}
}

// How long is a flag that is a span of time (spec 02 §1, spec 03 §1). The
// configuration has said `7d` since item 1.6, and a command line that only
// understood Go's own units would make the same fortnight two things to write.

// TestADurationFlagTakesTheSyntaxTheConfigurationTakes: `d` and `w` are the
// units a delivery objective is actually written in, and both of flow's
// duration flags take them.
func TestADurationFlagTakesTheSyntaxTheConfigurationTakes(t *testing.T) {
	cases := []struct {
		args []string
		want time.Duration
	}{
		{args: []string{"--timeout", "1d"}, want: 24 * time.Hour},
		{args: []string{"--timeout", "2w"}, want: 14 * 24 * time.Hour},
		{args: []string{"--timeout", "90m"}, want: 90 * time.Minute},
		{args: []string{"--timeout", "1d12h"}, want: 36 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var cli CLI
			parser, err := kong.New(&cli, kongOptions(io.Discard, io.Discard)...)
			if err != nil {
				t.Fatalf("build the parser: %v", err)
			}
			if _, err := parser.Parse(append(tc.args, "version")); err != nil {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
			if got := cli.Timeout.Std(); got != tc.want {
				t.Errorf("--timeout %v is %s, want %s", tc.args, got, tc.want)
			}
		})
	}
}

// TestADurationStillNeedsAUnit: a bare number is a question, not a span, and
// the refusal has to say what to write instead.
func TestADurationStillNeedsAUnit(t *testing.T) {
	code, _, stderr := exec(t, "--timeout", "30", "version")
	if code != core.ExitUsage {
		t.Fatalf("flow --timeout 30 exited %d, want %d:\n%s", code, core.ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "7d") {
		t.Errorf("the refusal does not say how to write one:\n%s", stderr)
	}
}

// TestTheWindowIsAFortnightUnlessAsked: spec 02 §2 fixes the default, and a
// scheduled run that quietly looked at a different fortnight than the one a
// person sees by hand would make the two reports impossible to compare.
func TestTheWindowIsAFortnightUnlessAsked(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli, kongOptions(io.Discard, io.Discard)...)
	if err != nil {
		t.Fatalf("build the parser: %v", err)
	}
	if _, err := parser.Parse([]string{"doctor"}); err != nil {
		t.Fatalf("parse doctor: %v", err)
	}
	if got := cli.Doctor.Window.Std(); got != 14*24*time.Hour {
		t.Errorf("--window defaults to %s, want 14d", got)
	}
}

func TestTheAPIRootIsTheOneTheForgeAnswersAt(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		configured string
		want       string
		accept     string
	}{
		{"github.com", forge.GitHubKind, "https://github.com", forge.GitHubAPI, "application/vnd.github+json"},
		{"an enterprise server", forge.GitHubKind, "https://git.acme.test/api/v3", "https://git.acme.test/api/v3", "application/vnd.github+json"},
		{"a gitea somebody installed", forge.GiteaKind, "https://codeberg.org", "https://codeberg.org" + forge.GiteaAPIPath, "application/json"},
		{"a gitea named with its path already", forge.GiteaKind, "https://codeberg.org/api/v1", "https://codeberg.org" + forge.GiteaAPIPath, "application/json"},
		{"a gitea named with a trailing slash", forge.GiteaKind, "https://codeberg.org/", "https://codeberg.org" + forge.GiteaAPIPath, "application/json"},
		{"gitlab.com", forge.GitLabKind, "https://gitlab.com", forge.GitLabHost + forge.GitLabAPIPath, "application/json"},
		{"a self-managed gitlab", forge.GitLabKind, "https://gitlab.acme.test", "https://gitlab.acme.test" + forge.GitLabAPIPath, "application/json"},
		{"a gitlab named with its path already", forge.GitLabKind, "https://gitlab.acme.test/api/v4", "https://gitlab.acme.test" + forge.GitLabAPIPath, "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, header, err := forgeEndpoint(tc.kind, tc.configured, ".flow.yaml")
			if err != nil {
				t.Fatalf("forgeEndpoint: %v", err)
			}
			if root != tc.want {
				t.Errorf("root = %q, want %q", root, tc.want)
			}
			if got := header.Get("Accept"); got != tc.accept {
				t.Errorf("Accept = %q, want %q", got, tc.accept)
			}
		})
	}
}

func TestAForgeWithNowhereToAnswerIsAQuestionAndNotADefault(t *testing.T) {
	// GitHub has one host and a Gitea has whichever one it was installed
	// on, so silence means different things to the two of them.
	if _, _, err := forgeEndpoint(forge.GitHubKind, "", ".flow.yaml"); err != nil {
		t.Errorf("forgeEndpoint(github, no url) = %v, want the one host GitHub has", err)
	}
	if _, _, err := forgeEndpoint(forge.GitLabKind, "", ".flow.yaml"); err != nil {
		t.Errorf("forgeEndpoint(gitlab, no url) = %v, want gitlab.com", err)
	}
	_, _, err := forgeEndpoint(forge.GiteaKind, "", ".flow.yaml")
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("forgeEndpoint(gitea, no url) = %v, want a question", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "forge.url") {
		t.Errorf("the refusal does not say to set forge.url: %+v", err)
	}
}

func TestAForgeThisBuildCannotSpeakToIsRefusedBeforeTheCredential(t *testing.T) {
	_, _, err := forgeEndpoint("bitbucket", "https://bitbucket.org", ".flow.yaml")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("forgeEndpoint(bitbucket) = %v, want an unsupported forge", err)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "gitlab") {
		t.Errorf("the refusal does not offer the forges this build has: %+v", err)
	}
}
