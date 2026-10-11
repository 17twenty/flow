package config

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

func TestPendingDecodes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want Pending
	}{
		{"a known value", "version: 1\ntracker:\n  project: \"3\"\n", Known("3")},
		{"a value needing no quotes", "version: 1\ntracker:\n  project: FLOW\n", Known("FLOW")},
		{"a placeholder", "version: 1\ntracker:\n  project: ${pending:project}\n", Pending{Name: "project"}},
		{"a quoted placeholder", "version: 1\ntracker:\n  project: '${pending:project}'\n", Pending{Name: "project"}},
		{"nothing at all", "version: 1\ntracker:\n  type: github-projects\n", Pending{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.src), "flow.yaml")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Tracker.Project != tt.want {
				t.Errorf("project = %#v, want %#v", cfg.Tracker.Project, tt.want)
			}
		})
	}
}

func TestPendingValue(t *testing.T) {
	known := Known("3")
	if known.IsPending() {
		t.Error("a known value reports itself pending")
	}
	if known.IsZero() {
		t.Error("a known value reports itself unset")
	}
	if got := known.String(); got != "3" {
		t.Errorf("String() = %q, want 3", got)
	}

	var unset Pending
	if !unset.IsZero() || unset.IsPending() || unset.String() != "" {
		t.Errorf("the zero Pending is not empty: %#v", unset)
	}

	waiting := Pending{Name: "project"}
	if !waiting.IsPending() {
		t.Error("a placeholder does not report itself pending")
	}
	if waiting.IsZero() {
		t.Error("a placeholder reports itself unset")
	}
	if got := waiting.String(); got != "${pending:project}" {
		t.Errorf("String() = %q, want the placeholder back", got)
	}

	resolved := waiting.Resolved("3")
	if resolved.IsPending() || resolved.Text != "3" {
		t.Errorf("Resolved gave %#v, want the text with the name kept", resolved)
	}
	if resolved.Name != "project" {
		t.Errorf("Resolved forgot which placeholder it filled in: %#v", resolved)
	}
	if waiting.Text != "" {
		t.Error("Resolved mutated the original")
	}
}

func TestPendingRoundTrips(t *testing.T) {
	for _, p := range []Pending{Known("3"), {Name: "project"}, {}} {
		out, err := yaml.Marshal(map[string]Pending{"project": p})
		if err != nil {
			t.Fatalf("marshal %#v: %v", p, err)
		}
		var back struct {
			Project Pending `yaml:"project"`
		}
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if back.Project != p {
			t.Errorf("round trip of %#v gave %#v (via %q)", p, back.Project, out)
		}

		asJSON, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("json marshal %#v: %v", p, err)
		}
		if want := `"` + p.String() + `"`; string(asJSON) != want {
			t.Errorf("json of %#v = %s, want %s", p, asJSON, want)
		}
	}
}

func TestPlaceholderName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"${pending:project}", "project", true},
		{"${pending:owners.default}", "owners.default", true},
		{"${pending:a_b.c_d}", "a_b.c_d", true},
		{"3", "", false},
		{"", "", false},
		{"${pending:}", "", false},
		{"${pending:Project}", "", false},
		{"${pending:.project}", "", false},
		{"${pending:project.}", "", false},
		{"${pending:project}}", "", false},
		{"${pending:project", "", false},
		{"${project}", "", false},
		{"${env:HOME}", "", false},
		{"prefix-${pending:project}", "", false},
		{"${pending:project}-suffix", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := PlaceholderName(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("PlaceholderName(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPlaceholdersOnlyAtAllowedPaths(t *testing.T) {
	allowed := []string{
		"version: 1\ntracker:\n  project: ${pending:project}\n",
		"version: 1\nfollowups:\n  owners:\n    default: ${pending:owners.default}\n",
		"version: 1\nfollowups:\n  owners:\n    tracker_admin: ${pending:owners.tracker_admin}\n" +
			"    forge_admin: ${pending:owners.forge_admin}\n    platform: ${pending:owners.platform}\n",
	}
	for _, src := range allowed {
		if _, err := Parse([]byte(src), "flow.yaml"); err != nil {
			t.Errorf("Parse(%q) failed: %v", src, err)
		}
	}

	refused := []struct {
		name string
		src  string
		at   at
	}{
		{
			name: "a scalar flow has no way to resolve",
			src:  "version: 1\nforge:\n  repo: ${pending:repo}\n",
			at:   at{3, 9, "${pending:repo} cannot be used at forge.repo"},
		},
		{
			name: "inside a sequence",
			src:  "version: 1\nworkflow:\n  columns:\n    - id: done\n      name: ${pending:done}\n",
			at:   at{5, 13, "${pending:done} cannot be used at workflow.columns[0].name"},
		},
		{
			name: "a mapping key",
			src:  "version: 1\npolicy:\n  rules:\n    ${pending:rule}: off\n",
			at:   at{4, 5, "${pending:rule} cannot be used at policy.rules.${pending:rule}"},
		},
		{
			name: "an environment variable name",
			src:  "version: 1\nforge:\n  auth:\n    token_env: ${pending:token}\n",
			at:   at{4, 16, "cannot be used at forge.auth.token_env"},
		},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			problems := problemsFrom(t, tt.src)
			if len(problems) != 1 {
				t.Fatalf("got %d problems %v, want 1", len(problems), problems)
			}
			got := problems[0]
			if got.Line != tt.at.line || got.Column != tt.at.col {
				t.Errorf("problem at %d:%d, want %d:%d (%s)", got.Line, got.Column, tt.at.line, tt.at.col, got)
			}
			if !strings.Contains(got.Message, tt.at.message) {
				t.Errorf("message %q does not contain %q", got.Message, tt.at.message)
			}
			if !strings.Contains(got.Message, "tracker.project") {
				t.Errorf("message %q does not say where placeholders are allowed", got.Message)
			}
		})
	}
}

func TestMalformedPlaceholdersAreRefused(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		message string
	}{
		{
			name:    "no closing brace",
			src:     "version: 1\ntracker:\n  project: ${pending:project\n",
			message: "missing its closing brace",
		},
		{
			name:    "no name",
			src:     "version: 1\ntracker:\n  project: ${pending:}\n",
			message: "has no name",
		},
		{
			name:    "another interpolation entirely",
			src:     "version: 1\ntracker:\n  project: ${env:PROJECT}\n",
			message: "nothing else is interpolated",
		},
		{
			name:    "part of a larger value",
			src:     "version: 1\ntracker:\n  project: board-${pending:project}\n",
			message: "a placeholder must be the whole value",
		},
		{
			name:    "part of a larger value elsewhere",
			src:     "version: 1\nforge:\n  repo: 17twenty/${pending:repo}\n",
			message: "a placeholder must be the whole value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := problemsFrom(t, tt.src)
			if len(problems) == 0 {
				t.Fatal("no problems reported")
			}
			if !strings.Contains(problems[0].Message, tt.message) {
				t.Errorf("message %q does not contain %q", problems[0].Message, tt.message)
			}
			if problems[0].Line != 3 || problems[0].Column == 0 {
				t.Errorf("problem at %d:%d, want line 3 with a column", problems[0].Line, problems[0].Column)
			}
		})
	}
}

// TestPlaceholderInFlowMappingExplainsBlockStyle covers the mistake the spec
// warns about: YAML cannot hold an unquoted ${…} inside {…}, and says so in
// terms of braces rather than placeholders (spec 03 §2).
func TestPlaceholderInFlowMappingExplainsBlockStyle(t *testing.T) {
	tests := []struct {
		name string
		src  string
		at   at
	}{
		{
			name: "one line",
			src:  "version: 1\ntracker: { type: github-projects, project: ${pending:project} }\n",
			at:   at{2, 44, ""},
		},
		{
			name: "nested",
			src:  "version: 1\nfollowups:\n  owners: { default: ${pending:owners.default} }\n",
			at:   at{3, 22, ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := problemsFrom(t, tt.src)
			if len(problems) != 1 {
				t.Fatalf("got %d problems %v, want 1", len(problems), problems)
			}
			got := problems[0]
			if got.Line != tt.at.line || got.Column != tt.at.col {
				t.Errorf("problem at %d:%d, want %d:%d", got.Line, got.Column, tt.at.line, tt.at.col)
			}
			for _, want := range []string{"flow mapping", "own line", "quote"} {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q does not mention %q", got.Message, want)
				}
			}
		})
	}

	// A quoted placeholder inside a flow mapping is legal YAML and must be
	// accepted, so the hint must not fire on it.
	src := "version: 1\ntracker: { project: '${pending:project}' }\n"
	cfg, err := Parse([]byte(src), "flow.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Tracker.Project.IsPending() {
		t.Errorf("project = %#v, want a placeholder", cfg.Tracker.Project)
	}
}

func TestPlaceholderProblemsAreReportedWithOtherProblems(t *testing.T) {
	src := "version: 1\nforge:\n  repo: ${pending:repo}\n  nope: true\n"
	problems := problemsFrom(t, src)
	if len(problems) != 2 {
		t.Fatalf("got %d problems %v, want 2", len(problems), problems)
	}
	if problems[0].Line != 3 || !strings.Contains(problems[0].Message, "cannot be used at forge.repo") {
		t.Errorf("first problem = %s", problems[0])
	}
	if problems[1].Line != 4 || !strings.Contains(problems[1].Message, `unknown key "nope"`) {
		t.Errorf("second problem = %s", problems[1])
	}
}

// TestPresetPlaceholdersAreAllowed proves the shipped presets only use
// placeholders where doctor can resolve them.
func TestPresetPlaceholdersAreAllowed(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "assets", "presets", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no presets found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.Tracker.Project.IsPending() {
				t.Errorf("tracker.project = %#v, want a placeholder: a preset cannot know the board", cfg.Tracker.Project)
			}
			if !cfg.FollowUps.Owners.Default.IsPending() {
				t.Errorf("followups.owners.default = %#v, want a placeholder", cfg.FollowUps.Owners.Default)
			}
		})
	}
}

// problemsFrom parses src, requiring it to fail, and returns the problems.
func problemsFrom(t *testing.T, src string) []Problem {
	t.Helper()
	cfg, err := Parse([]byte(src), "flow.yaml")
	if err == nil {
		t.Fatalf("Parse succeeded with %#v, want an error", cfg)
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error is %T, want *ParseError", err)
	}
	return parseErr.Problems
}

// TestParseSurvivesAliasCycles covers a YAML document that refers to itself.
// It parses into a cyclic node tree, and walking it for placeholders must not
// follow the cycle: `&a [*a]` is four bytes and used to exhaust memory.
func TestParseSurvivesAliasCycles(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		for _, src := range []string{
			"&a [*a]\n",
			"version: 1\ntracker: &a\n  project: *a\n",
			"version: 1\nworkflow: &a\n  columns: [*a, *a, *a]\n",
		} {
			if _, err := Parse([]byte(src), "flow.yaml"); err == nil {
				done <- errors.New("Parse accepted " + src)
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Parse did not finish: a cyclic alias is being followed")
	}
}
