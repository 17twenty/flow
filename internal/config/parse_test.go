package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// fullConfig exercises every field the v1 schema defines, so that a field
// added to the structs without a decoding test shows up as a diff here.
const fullConfig = `version: 1
preset: lean-kanban
forge:
  type: github
  repo: 17twenty/flow
  url: https://github.example.com
  required_checks: [test, lint]
  auth:
    token_env: GITHUB_TOKEN
tracker:
  type: jira-cloud
  owner: 17twenty
  owner_type: org
  project: FLOW
  url: https://17twenty.atlassian.net
  auth:
    token_env: JIRA_API_TOKEN
    email_env: JIRA_EMAIL
workflow:
  done_on: deployed
  columns:
    - { id: backlog, name: Backlog, category: todo }
    - { id: ready, name: Ready, category: todo, gate: definition_of_ready }
    - { id: in_progress, name: In progress, category: in_progress, wip: { per_person: 2 } }
    - { id: in_review, name: In review, category: in_progress, wip: { limit: 3, per_person: 1 } }
    - { id: done, name: Done, category: done }
  transitions:
    pr_opened_draft: { to: in_progress, owner: flow }
    deployed: { to: done, owner: flow }
  expedite:
    labels: [incident]
    max: 1
  blocked:
    label: blocked
    require_comment: true
definition_of_ready:
  rules: [acceptance_criteria, parent_link, not_blocked]
  max_size: 2d
commits:
  convention: conventional
  types: [feat, fix]
  header_max: 72
  merge_strategy: squash
  scopes:
    required: true
    allowed: [cli, core]
  unlinked_types: [ci, docs]
deploys:
  source: commit_status
  environment: production
  contexts: ['argocd/app-prod']
  max_lag: 2h
  max_age: 7d
policy:
  status_context: flow/policy
  strictness: strict
  comment: on_failure
  rules:
    cc.description.case: off
agents:
  authors: ['17twenty-flow-agent[bot]']
  labels: [agent]
  require_human_review: true
followups:
  labels: [flow-followup]
  owners:
    default: '@17twenty'
    tracker_admin: '@tracker'
    forge_admin: '@forge'
    platform: '@platform'
attestation:
  default_ttl: 30d
`

func TestParseFull(t *testing.T) {
	got, err := Parse([]byte(fullConfig), "flow.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := &Config{
		Version: 1,
		Preset:  "lean-kanban",
		Forge: Forge{
			Type:           ForgeGitHub,
			Repo:           "17twenty/flow",
			URL:            "https://github.example.com",
			RequiredChecks: []string{"test", "lint"},
			Auth:           Auth{TokenEnv: "GITHUB_TOKEN"},
		},
		Tracker: Tracker{
			Type:      TrackerJiraCloud,
			Owner:     "17twenty",
			OwnerType: OwnerOrg,
			Project:   Known("FLOW"),
			URL:       "https://17twenty.atlassian.net",
			Auth:      Auth{TokenEnv: "JIRA_API_TOKEN", EmailEnv: "JIRA_EMAIL"},
		},
		Workflow: Workflow{
			DoneOn: DoneOnDeployed,
			Columns: []Column{
				{ID: "backlog", Name: "Backlog", Category: CategoryTodo},
				{ID: "ready", Name: "Ready", Category: CategoryTodo, Gate: GateDefinitionOfReady},
				{ID: "in_progress", Name: "In progress", Category: CategoryInProgress, WIP: &WIP{PerPerson: 2}},
				{ID: "in_review", Name: "In review", Category: CategoryInProgress, WIP: &WIP{Limit: 3, PerPerson: 1}},
				{ID: "done", Name: "Done", Category: CategoryDone},
			},
			Transitions: map[string]Transition{
				"pr_opened_draft": {To: "in_progress", Owner: OwnerFlow},
				"deployed":        {To: "done", Owner: OwnerFlow},
			},
			Expedite: Expedite{Labels: []string{"incident"}, Max: 1},
			Blocked:  Blocked{Label: "blocked", RequireComment: true},
		},
		DefinitionOfReady: DefinitionOfReady{
			Rules:   []string{"acceptance_criteria", "parent_link", "not_blocked"},
			MaxSize: 2 * Day,
		},
		Commits: Commits{
			Convention:    "conventional",
			Types:         []string{"feat", "fix"},
			HeaderMax:     72,
			MergeStrategy: "squash",
			Scopes:        Scopes{Required: true, Allowed: []string{"cli", "core"}},
			UnlinkedTypes: []string{"ci", "docs"},
		},
		Deploys: Deploys{
			Source:      DeploySourceCommitStatus,
			Environment: "production",
			Contexts:    []string{"argocd/app-prod"},
			MaxLag:      Duration(2 * time.Hour),
			MaxAge:      7 * Day,
		},
		Policy: Policy{
			StatusContext: "flow/policy",
			Strictness:    StrictnessStrict,
			Comment:       CommentOnFailure,
			Rules:         map[string]string{"cc.description.case": SeverityOff},
		},
		Agents: Agents{
			Authors:            []string{"17twenty-flow-agent[bot]"},
			Labels:             []string{"agent"},
			RequireHumanReview: true,
		},
		FollowUps: FollowUps{
			Labels: []string{"flow-followup"},
			Owners: Owners{
				Default:      Known("@17twenty"),
				TrackerAdmin: Known("@tracker"),
				ForgeAdmin:   Known("@forge"),
				Platform:     Known("@platform"),
			},
		},
		Attestation: AttestationPolicy{DefaultTTL: 30 * Day},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse gave\n%#v\nwant\n%#v", got, want)
	}
}

func TestParseMinimal(t *testing.T) {
	got, err := Parse([]byte("version: 1\n"), "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got, &Config{Version: 1}) {
		t.Errorf("Parse gave %#v, want a bare version", got)
	}
}

// at is one expected problem location, kept short so the tables read as a map
// of source position to complaint.
type at struct {
	line, col int
	message   string
}

func TestParseReportsProblems(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []at
	}{
		{
			name: "unknown top-level key",
			src:  "version: 1\nworkflows: {}\n",
			want: []at{{2, 1, `unknown key "workflows"`}},
		},
		{
			name: "unknown nested key",
			src:  "version: 1\nforge:\n  type: github\n  repoo: a/b\n",
			want: []at{{4, 3, `unknown key "repoo"`}},
		},
		{
			name: "unknown key in a flow mapping",
			src:  "version: 1\nforge: { type: github, slug: a/b }\n",
			want: []at{{2, 24, `unknown key "slug"`}},
		},
		{
			name: "unknown key in a sequence element",
			src:  "version: 1\nworkflow:\n  columns:\n    - id: done\n      colour: green\n",
			want: []at{{5, 7, `unknown key "colour"`}},
		},
		{
			name: "every unknown key is reported, in file order",
			src:  "version: 1\nforge:\n  repoo: a/b\npolicy:\n  strictnes: strict\n",
			want: []at{
				{3, 3, `unknown key "repoo"`},
				{5, 3, `unknown key "strictnes"`},
			},
		},
		{
			name: "wrong scalar type",
			src:  "version: 1\ncommits:\n  header_max: lots\n",
			want: []at{{3, 15, "cannot unmarshal !!str `lots` into int"}},
		},
		{
			name: "wrong shape",
			src:  "version: 1\nworkflow:\n  columns: done\n",
			want: []at{{3, 12, "cannot unmarshal !!str `done` into []config.Column"}},
		},
		{
			name: "bad duration",
			src:  "version: 1\ndeploys:\n  max_lag: soon\n",
			want: []at{{3, 12, `"soon" is not a duration: write a number and a unit, for example 90m, 2h, 7d or 1w`}},
		},
		{
			name: "duplicate key",
			src:  "version: 1\npolicy:\n  strictness: strict\n  strictness: relaxed\n",
			want: []at{{4, 3, "key \"strictness\" is set twice; it is already set on line 3"}},
		},
		{
			name: "missing version",
			src:  "forge:\n  type: github\n",
			want: []at{{1, 1, "missing version; this build reads version 1"}},
		},
		{
			name: "unsupported version",
			src:  "version: 2\n",
			want: []at{{1, 10, "version 2 is not supported; this build reads version 1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.src), "flow.yaml")
			if err == nil {
				t.Fatalf("Parse succeeded with %#v, want an error", cfg)
			}
			if cfg != nil {
				t.Errorf("Parse returned a config alongside an error")
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error is %T, want *ParseError", err)
			}
			if got, want := len(parseErr.Problems), len(tt.want); got != want {
				t.Fatalf("got %d problems %v, want %d", got, parseErr.Problems, want)
			}
			for i, want := range tt.want {
				got := parseErr.Problems[i]
				if got.Path != "flow.yaml" || got.Line != want.line || got.Column != want.col || got.Message != want.message {
					t.Errorf("problem %d = %s, want flow.yaml:%d:%d: %s", i, got, want.line, want.col, want.message)
				}
			}
		})
	}
}

func TestParseErrorIsAUsageError(t *testing.T) {
	_, err := Parse([]byte("version: 1\nnope: true\n"), "flow.yaml")
	if err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("error does not wrap core.ErrValidation")
	}
	if got := core.ExitCode(err); got != core.ExitUsage {
		t.Errorf("ExitCode = %d, want %d", got, core.ExitUsage)
	}

	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatal("error does not carry a core.UserError")
	}
	if user.What != "read flow.yaml" {
		t.Errorf("What = %q", user.What)
	}
	if !strings.Contains(user.Why, `flow.yaml:2:1: unknown key "nope"`) {
		t.Errorf("Why = %q, want it to locate the key", user.Why)
	}
	if user.Fix == "" {
		t.Error("Fix is empty; a usage error must say what to do")
	}
}

func TestParseErrorListsEveryProblemInWhy(t *testing.T) {
	_, err := Parse([]byte("version: 1\nnope: true\nalso: true\n"), "flow.yaml")
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("error does not carry a core.UserError: %v", err)
	}
	lines := strings.Split(user.Why, "\n")
	if len(lines) != 2 {
		t.Fatalf("Why has %d lines, want 2: %q", len(lines), user.Why)
	}
	if !strings.HasPrefix(lines[1], "       ") {
		t.Errorf("continuation line is not indented under why: %q", lines[1])
	}
}

// TestParseRejectsBrokenYAML pins the positions flow reports for YAML the
// parser cannot read at all. These come from the library and carry a line but
// no column; an unterminated flow collection is reported against the line it
// opened on, which yaml.v3 counts from zero (see spec 03 Decisions).
func TestParseRejectsBrokenYAML(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		line    int
		message string
	}{
		{"unclosed flow mapping", "version: 1\nforge: { type: github\n", 1, "did not find expected ',' or '}'"},
		{"bad indentation", "version: 1\nforge:\n  type: github\n   repo: a/b\n", 4, "mapping values are not allowed in this context"},
		{"tab indentation", "version: 1\nforge:\n\ttype: github\n", 3, "found character that cannot start any token"},
		{"unclosed quote", "version: 1\npreset: 'lean\n", 2, "found unexpected end of stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src), "flow.yaml")
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error is %T, want *ParseError", err)
			}
			if len(parseErr.Problems) != 1 {
				t.Fatalf("got %d problems %v, want 1", len(parseErr.Problems), parseErr.Problems)
			}
			got := parseErr.Problems[0]
			if got.Line != tt.line {
				t.Errorf("problem at line %d, want %d (%s)", got.Line, tt.line, got)
			}
			if !strings.Contains(got.Message, tt.message) {
				t.Errorf("message %q does not mention %q", got.Message, tt.message)
			}
		})
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	for _, src := range []string{"", "\n", "# just a comment\n"} {
		_, err := Parse([]byte(src), "flow.yaml")
		if err == nil {
			t.Fatalf("Parse(%q) succeeded, want an error", src)
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("Parse(%q) said %q, want it to say the file is empty", src, err)
		}
	}
}

func TestParseDefaultsThePath(t *testing.T) {
	_, err := Parse([]byte("nope: true\n"), "")
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error is %T, want *ParseError", err)
	}
	if parseErr.Path != DefaultPath {
		t.Errorf("Path = %q, want %q", parseErr.Path, DefaultPath)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultPath)
	if err := os.WriteFile(path, []byte("version: 1\npreset: minimal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Preset != "minimal" {
		t.Errorf("Preset = %q, want minimal", cfg.Preset)
	}

	_, err = Load(filepath.Join(dir, "absent.yaml"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Load of a missing file gave %v, want core.ErrNotFound", err)
	}
	var user *core.UserError
	if errors.As(err, &user) && !strings.Contains(user.Fix, "flow init") {
		t.Errorf("Fix = %q, want it to point at flow init", user.Fix)
	}
}

// TestParseFlowsOwnConfig keeps the dogfood file honest: flow's own workflow
// must decode with the build that reads it (spec 17).
func TestParseFlowsOwnConfig(t *testing.T) {
	path := filepath.Join("..", "..", DefaultPath)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if cfg.Forge.Repo != "17twenty/flow" {
		t.Errorf("forge.repo = %q, want 17twenty/flow", cfg.Forge.Repo)
	}
	if _, ok := cfg.Workflow.Column("done"); !ok {
		t.Error("flow's own workflow has no done column")
	}
	if cfg.Deploys.MaxLag != Duration(2*time.Hour) {
		t.Errorf("deploys.max_lag = %s, want 2h", cfg.Deploys.MaxLag)
	}
}

func TestWorkflowColumnLookup(t *testing.T) {
	w := Workflow{Columns: []Column{
		{ID: "backlog"}, {ID: "ready"}, {ID: "done"},
	}}
	if c, ok := w.Column("ready"); !ok || c.ID != "ready" {
		t.Errorf("Column(ready) = %v, %v", c, ok)
	}
	if _, ok := w.Column("nope"); ok {
		t.Error("Column(nope) found something")
	}
	if got := w.ColumnIndex("done"); got != 2 {
		t.Errorf("ColumnIndex(done) = %d, want 2", got)
	}
	if got := w.ColumnIndex("nope"); got != -1 {
		t.Errorf("ColumnIndex(nope) = %d, want -1", got)
	}
}
