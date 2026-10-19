package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
)

// testRegistry loads the shipped capability declarations from the tree, the
// same way the other tests reach the assets directory.
func testRegistry(t *testing.T) core.Registry {
	t.Helper()
	registry, err := core.LoadRegistry(os.DirFS(filepath.Join("..", "..", "assets", "capabilities")))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return registry
}

// presetConfig parses one shipped preset.
func presetConfig(t *testing.T, name string) *Config {
	t.Helper()
	path := filepath.Join("..", "..", "assets", "presets", name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data, path)
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	return cfg
}

// TestValidateAcceptsWhatFlowShips is the check that matters most: every
// preset, and flow's own workflow, must pass its own semantic validation.
func TestValidateAcceptsWhatFlowShips(t *testing.T) {
	registry := testRegistry(t)

	paths, err := filepath.Glob(filepath.Join("..", "..", "assets", "presets", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no presets found: %v", err)
	}
	paths = append(paths, filepath.Join("..", "..", ".flow.yaml"))

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := Parse(data, path)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := Validate(cfg, path, registry); err != nil {
				t.Errorf("Validate:\n%v", err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	registry := testRegistry(t)

	tests := []struct {
		name   string
		from   string
		change func(*Config)
		wantIn string
	}{
		{
			name:   "a preset nobody ships",
			change: func(c *Config) { c.Preset = "kanban-plus" },
			wantIn: `preset is "kanban-plus"`,
		},
		{
			name:   "no forge",
			change: func(c *Config) { c.Forge.Type = "" },
			wantIn: "forge.type is not set",
		},
		{
			name:   "a tracker named as a forge",
			change: func(c *Config) { c.Forge.Type = TrackerGitHubProjects },
			wantIn: `forge.type is "github-projects"`,
		},
		{
			name:   "a repository that is not owner/name",
			change: func(c *Config) { c.Forge.Repo = "flow" },
			wantIn: "it must be owner/name",
		},
		{
			name:   "a forge URL that is not a URL",
			change: func(c *Config) { c.Forge.URL = "github.example.com" },
			wantIn: "it must start with https://",
		},
		{
			name:   "a required check that is not named",
			change: func(c *Config) { c.Forge.RequiredChecks = []string{"test", ""} },
			wantIn: "forge.required_checks[1] is empty",
		},
		{
			name:   "a required check padded with the spaces that are part of it",
			change: func(c *Config) { c.Forge.RequiredChecks = []string{" test "} },
			wantIn: "a status context is matched exactly",
		},
		{
			name:   "the same check required twice",
			change: func(c *Config) { c.Forge.RequiredChecks = []string{"test", "test"} },
			wantIn: `forge.required_checks[1] repeats "test"`,
		},
		{
			name:   "an email for a forge",
			change: func(c *Config) { c.Forge.Auth.EmailEnv = "GITHUB_EMAIL" },
			wantIn: "a forge authenticates with a token alone",
		},
		{
			name:   "a variable name that is not one",
			change: func(c *Config) { c.Forge.Auth.TokenEnv = "GITHUB TOKEN" },
			wantIn: "it must be the NAME of an environment variable",
		},
		{
			name:   "a token pasted where its name belongs",
			change: func(c *Config) { c.Tracker.Auth.TokenEnv = fakeToken },
			wantIn: "holds a credential, not the name of one",
		},
		{
			name:   "no board",
			change: func(c *Config) { c.Tracker.Project = Pending{} },
			wantIn: "tracker.project is not set",
		},
		{
			name:   "a GitHub project with no owner",
			change: func(c *Config) { c.Tracker.Owner = "" },
			wantIn: "tracker.owner is not set",
		},
		{
			name:   "a GitHub project that does not say whose it is",
			change: func(c *Config) { c.Tracker.OwnerType = "" },
			wantIn: "tracker.owner_type is not set",
		},
		{
			name:   "an owner that is neither a user nor an organisation",
			change: func(c *Config) { c.Tracker.OwnerType = "team" },
			wantIn: `tracker.owner_type is "team"`,
		},
		{
			name: "Jira without a site",
			change: func(c *Config) {
				c.Tracker.Type = TrackerJiraCloud
				c.Tracker.Owner, c.Tracker.OwnerType = "", ""
				c.Tracker.Auth.EmailEnv = "JIRA_EMAIL"
			},
			wantIn: "tracker.url is not set",
		},
		{
			name: "Jira without the email its token belongs to",
			change: func(c *Config) {
				c.Tracker.Type = TrackerJiraCloud
				c.Tracker.Owner, c.Tracker.OwnerType = "", ""
				c.Tracker.URL = "https://acme.atlassian.net"
			},
			wantIn: "only works alongside the email",
		},
		{
			name: "an owner for a tracker that has none",
			change: func(c *Config) {
				c.Tracker.Type = TrackerLinear
				c.Workflow.Transitions = map[string]Transition{EventPRMerged: {To: "done", Owner: OwnerFlow}}
			},
			wantIn: "only github-projects scopes a board to an account",
		},
		{
			name:   "no columns",
			change: func(c *Config) { c.Workflow.Columns = nil },
			wantIn: "workflow.columns is empty",
		},
		{
			name:   "two columns with one id",
			change: func(c *Config) { c.Workflow.Columns[0].ID = c.Workflow.Columns[1].ID },
			wantIn: "which another column already uses",
		},
		{
			name:   "an id that is not an identifier",
			change: func(c *Config) { c.Workflow.Columns[0].ID = "In Progress" },
			wantIn: "it must start with a lowercase letter",
		},
		{
			name:   "two columns with one name",
			change: func(c *Config) { c.Workflow.Columns[0].Name = c.Workflow.Columns[1].Name },
			wantIn: "flow matches columns to the board by name",
		},
		{
			name:   "a category flow does not have",
			change: func(c *Config) { c.Workflow.Columns[0].Category = "waiting" },
			wantIn: `category is "waiting"`,
		},
		{
			name: "two ways to be finished",
			change: func(c *Config) {
				c.Workflow.Columns[0].Category = CategoryDone
			},
			wantIn: "exactly one must be",
		},
		{
			name: "no way to be finished",
			change: func(c *Config) {
				c.Workflow.Columns = c.Workflow.Columns[:len(c.Workflow.Columns)-1]
				c.Workflow.Transitions = map[string]Transition{}
			},
			wantIn: "exactly one must be",
		},
		{
			name:   "a readiness gate with no rules behind it",
			change: func(c *Config) { c.DefinitionOfReady.Rules = nil },
			wantIn: "the gate would let everything through",
		},
		{
			name:   "a limit that is not a limit",
			change: func(c *Config) { c.Workflow.Columns[2].WIP = &WIP{} },
			wantIn: "wip sets no limit",
		},
		{
			name:   "a negative limit",
			change: func(c *Config) { c.Workflow.Columns[2].WIP = &WIP{Limit: -1} },
			wantIn: "cannot be negative",
		},
		{
			name: "an event nobody raises",
			change: func(c *Config) {
				c.Workflow.Transitions["pr_reopened"] = Transition{To: "in_review", Owner: OwnerFlow}
			},
			wantIn: "is not an event flow knows",
		},
		{
			name: "an event that is really two events",
			change: func(c *Config) {
				c.Workflow.Transitions[EventPROpened] = Transition{To: "in_review", Owner: OwnerFlow}
			},
			wantIn: "is not configured on its own",
		},
		{
			name: "a transition to nowhere",
			change: func(c *Config) {
				c.Workflow.Transitions[EventPROpenedDraft] = Transition{To: "doing", Owner: OwnerFlow}
			},
			wantIn: `.to is "doing", which is not a column id`,
		},
		{
			name: "a transition nobody owns",
			change: func(c *Config) {
				c.Workflow.Transitions[EventPROpenedDraft] = Transition{To: "in_progress", Owner: "github"}
			},
			wantIn: `.owner is "github"`,
		},
		{
			name: "native ownership the tracker cannot honour",
			change: func(c *Config) {
				c.Workflow.Transitions[EventPROpenedDraft] = Transition{To: "in_progress", Owner: OwnerNative}
			},
			wantIn: "has no automation for pr_opened_draft: use owner: flow",
		},
		{
			name: "native ownership of an event no tracker automates",
			change: func(c *Config) {
				c.Workflow.Transitions[EventApproved] = Transition{To: "in_review", Owner: OwnerNative}
			},
			wantIn: "no tracker offers an automation for approved",
		},
		{
			name:   "a done that is neither merged nor deployed",
			change: func(c *Config) { c.Workflow.DoneOn = "released" },
			wantIn: `workflow.done_on is "released"`,
		},
		{
			name:   "deployed with nothing to move the ticket",
			from:   PresetDeployDone,
			change: func(c *Config) { delete(c.Workflow.Transitions, EventDeployed) },
			wantIn: "nothing would ever reach the done column",
		},
		{
			name:   "deployed with no deployments to read",
			from:   PresetDeployDone,
			change: func(c *Config) { c.Deploys = Deploys{Source: DeploySourceNone} },
			wantIn: "no way to learn that anything was deployed",
		},
		{
			name: "merged but tracking deployments anyway",
			change: func(c *Config) {
				c.Workflow.Transitions[EventDeployed] = Transition{To: "done", Owner: OwnerFlow}
			},
			wantIn: "verified as lag, not tracked as status",
		},
		{
			name:   "expedite with nothing to expedite",
			change: func(c *Config) { c.Workflow.Expedite.Labels = nil },
			wantIn: "nothing would ever count as expedited",
		},
		{
			name: "a blocked rule with no label",
			change: func(c *Config) {
				c.Workflow.Blocked = Blocked{RequireComment: true}
			},
			wantIn: "flow would have nothing to notice",
		},
		{
			name:   "a readiness rule flow has not written",
			change: func(c *Config) { c.DefinitionOfReady.Rules = []string{"has_estimate"} },
			wantIn: `definition_of_ready.rules[0] is "has_estimate"`,
		},
		{
			name: "the same readiness rule twice",
			change: func(c *Config) {
				c.DefinitionOfReady.Rules = []string{DoRNotBlocked, DoRNotBlocked}
			},
			wantIn: "repeats",
		},
		{
			name:   "a convention flow does not implement",
			change: func(c *Config) { c.Commits.Convention = "gitmoji" },
			wantIn: `commits.convention is "gitmoji"`,
		},
		{
			name:   "a merge strategy that is not one",
			change: func(c *Config) { c.Commits.MergeStrategy = "fast-forward" },
			wantIn: `commits.merge_strategy is "fast-forward"`,
		},
		{
			name:   "a header limit with no room in it",
			change: func(c *Config) { c.Commits.HeaderMax = 8 },
			wantIn: "it must be between 20 and 200",
		},
		{
			name:   "a health threshold that is not a percentage",
			change: func(c *Config) { c.Health.FailBelow = 150 },
			wantIn: "health.fail_below is 150",
		},
		{
			name:   "a health threshold below one per cent",
			change: func(c *Config) { c.Health.FailBelow = -1 },
			wantIn: "a percentage between 1 and 100",
		},
		{
			name:   "a commit type that is not a word",
			change: func(c *Config) { c.Commits.Types = []string{"feat", "Fix!"} },
			wantIn: "a type is lowercase letters",
		},
		{
			name:   "exempting a type nobody may use",
			change: func(c *Config) { c.Commits.UnlinkedTypes = []string{"wip"} },
			wantIn: "which is not in commits.types",
		},
		{
			name:   "requiring a scope from an open list",
			change: func(c *Config) { c.Commits.Scopes = Scopes{Required: true} },
			wantIn: "which is not a convention",
		},
		{
			name:   "a scope that is not a word",
			change: func(c *Config) { c.Commits.Scopes.Allowed = []string{"CLI"} },
			wantIn: `commits.scopes.allowed[0] is "CLI"`,
		},
		{
			name:   "a deployment source flow cannot read",
			change: func(c *Config) { c.Deploys.Source = "argocd" },
			wantIn: `deploys.source is "argocd"`,
		},
		{
			name: "commit statuses with no contexts",
			change: func(c *Config) {
				c.Deploys.Source = DeploySourceCommitStatus
			},
			wantIn: "would not know which status means deployed",
		},
		{
			name: "contexts the source never reads",
			change: func(c *Config) {
				c.Deploys.Contexts = []string{"argocd/app-prod"}
			},
			wantIn: "contexts name commit statuses",
		},
		{
			name: "deployment settings with no deployments",
			change: func(c *Config) {
				c.Deploys = Deploys{Source: DeploySourceNone, Environment: "production"}
			},
			wantIn: "flow reads no deployments at all",
		},
		{
			name:   "an environment nobody named",
			change: func(c *Config) { c.Deploys.Environment = "" },
			wantIn: "deploys.environment is not set",
		},
		{
			name:   "a strictness flow does not have",
			change: func(c *Config) { c.Policy.Strictness = "pedantic" },
			wantIn: `policy.strictness is "pedantic"`,
		},
		{
			name:   "a comment policy flow does not have",
			change: func(c *Config) { c.Policy.Comment = "sometimes" },
			wantIn: `policy.comment is "sometimes"`,
		},
		{
			name:   "a severity that is not one",
			change: func(c *Config) { c.Policy.Rules = map[string]string{"title.conventional": "loud"} },
			wantIn: `policy.rules.title.conventional is "loud"`,
		},
		{
			name:   "a status context with a space in it",
			change: func(c *Config) { c.Policy.StatusContext = "flow policy" },
			wantIn: "a commit status context has no spaces",
		},
		{
			name:   "the same agent twice",
			change: func(c *Config) { c.Agents.Authors = []string{"bot", "bot"} },
			wantIn: `agents.authors[1] repeats "bot"`,
		},
		{
			name:   "excusing a type nobody may write",
			change: func(c *Config) { c.Agents.UnreviewedTypes = []string{"wibble"} },
			wantIn: `agents.unreviewed_types[0] is "wibble", which is not in commits.types`,
		},
		{
			name: "insisting on human review of nobody's work",
			change: func(c *Config) {
				c.Agents = Agents{RequireHumanReview: true}
			},
			wantIn: "nothing would ever count as agent work",
		},
		{
			name:   "a follow-up label that is not one",
			change: func(c *Config) { c.FollowUps.Labels = []string{""} },
			wantIn: "followups.labels[0] is empty",
		},
		{
			name:   "an owner who is nobody",
			change: func(c *Config) { c.FollowUps.Owners.Default = Known("   ") },
			wantIn: "a follow-up nobody owns",
		},
		{
			name:   "an attestation that expires before it is made",
			change: func(c *Config) { c.Attestation.DefaultTTL = -1 },
			wantIn: "attestation.default_ttl is negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			from := test.from
			if from == "" {
				from = PresetLeanKanban
			}
			cfg := presetConfig(t, from)
			test.change(cfg)

			err := Validate(cfg, DefaultPath, registry)
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Errorf("error does not mention %q:\n%v", test.wantIn, err)
			}
			if got := core.ExitCode(err); got != core.ExitUsage {
				t.Errorf("exit code = %d, want %d", got, core.ExitUsage)
			}
			var parse *ParseError
			if !asParseError(err, &parse) {
				t.Fatalf("error is %T, want a *ParseError so tooling can read the problems", err)
			}
			if len(parse.Problems) == 0 {
				t.Error("the error carries no structured problems")
			}
		})
	}
}

// TestAnExcusedTypeIsJudgedAgainstWhatTheTeamNarrowedTo: an empty
// commits.types is the convention's own set of types, not an empty one, so a
// team that never narrowed the list may still excuse a routine type.
func TestAnExcusedTypeIsJudgedAgainstWhatTheTeamNarrowedTo(t *testing.T) {
	for _, test := range []struct {
		name  string
		types []string
	}{
		{name: "a list the team narrowed", types: []string{"feat", "fix", "chore", "ci", "build", "docs"}},
		{name: "no list at all", types: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := presetConfig(t, PresetLeanKanban)
			cfg.Commits.Types = test.types
			cfg.Agents.UnreviewedTypes = []string{"chore"}

			if err := Validate(cfg, DefaultPath, testRegistry(t)); err != nil {
				t.Fatalf("Validate refused a type the convention allows:\n%v", err)
			}
		})
	}
}

// asParseError is errors.As for the one type this file asks about.
func asParseError(err error, target **ParseError) bool {
	parse, ok := err.(*ParseError)
	if ok {
		*target = parse
	}
	return ok
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	cfg := presetConfig(t, PresetLeanKanban)
	cfg.Forge.Repo = "flow"
	cfg.Policy.Strictness = "pedantic"
	cfg.Workflow.Columns[0].Category = "waiting"

	err := Validate(cfg, DefaultPath, testRegistry(t))
	if err == nil {
		t.Fatal("Validate accepted three broken keys")
	}
	var parse *ParseError
	if !asParseError(err, &parse) {
		t.Fatalf("error is %T, want a *ParseError", err)
	}
	if len(parse.Problems) < 3 {
		t.Errorf("reported %d problems, want all 3: %v", len(parse.Problems), parse.Problems)
	}
}

// Without the capability declarations flow cannot know what a tracker's own
// automation can do, so it says nothing rather than guessing. Everything else
// is still checked.
func TestValidateWithoutARegistry(t *testing.T) {
	cfg := presetConfig(t, PresetLeanKanban)
	cfg.Workflow.Transitions[EventPROpenedDraft] = Transition{To: "in_progress", Owner: OwnerNative}

	if err := Validate(cfg, DefaultPath, nil); err != nil {
		t.Errorf("Validate without a registry reported %v, want it to skip what it cannot know", err)
	}

	cfg.Forge.Repo = "flow"
	if err := Validate(cfg, DefaultPath, nil); err == nil {
		t.Error("Validate without a registry stopped checking everything else too")
	}
}

func TestValidateRefusesAnAdapterThisBuildDoesNotShip(t *testing.T) {
	registry := core.Registry{}
	for adapter, set := range testRegistry(t) {
		if adapter != TrackerGitHubProjects {
			registry[adapter] = set
		}
	}

	cfg := presetConfig(t, PresetLeanKanban)
	err := Validate(cfg, DefaultPath, registry)
	if err == nil {
		t.Fatal("Validate accepted a tracker with no capability declaration")
	}
	if !strings.Contains(err.Error(), "no capability declaration") {
		t.Errorf("error does not explain the missing declaration:\n%v", err)
	}
}

func TestValidateNeedsAConfig(t *testing.T) {
	if err := Validate(nil, DefaultPath, nil); err == nil {
		t.Error("Validate accepted no configuration at all")
	}
}

// TestParseRefusesSecrets covers the heuristic in spec 03 §1.1. It runs at
// parse time, on the document, so a secret is reported at the line it is on
// wherever in the file it was written.
func TestParseRefusesSecrets(t *testing.T) {
	tests := []struct {
		name string
		src  string
		line int
	}{
		{
			name: "a token where a variable name belongs",
			src:  "version: 1\nforge:\n  auth:\n    token_env: " + fakeToken + "\n",
			line: 4,
		},
		{
			name: "a token in a comment-like value nobody reads",
			src:  "version: 1\nagents:\n  labels: ['" + fakeToken + "']\n",
			line: 3,
		},
		{
			name: "a credential in a URL",
			src:  "version: 1\ntracker:\n  url: https://acme.atlassian.net/?api_key=s3cr3tvalue\n",
			line: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.src), DefaultPath)
			if err == nil {
				t.Fatal("Parse accepted a committed secret")
			}
			if !strings.Contains(err.Error(), "looks like a credential") {
				t.Errorf("error does not say what is wrong:\n%v", err)
			}
			var parse *ParseError
			if !asParseError(err, &parse) {
				t.Fatalf("error is %T, want a *ParseError", err)
			}
			if got := parse.Problems[0].Line; got != test.line {
				t.Errorf("reported line %d, want %d", got, test.line)
			}
			if strings.Contains(err.Error(), fakeToken) {
				t.Error("the error repeats the secret back")
			}
		})
	}
}

func TestParseAcceptsThingsThatMerelyLookLikeNames(t *testing.T) {
	// The heuristic matches credential shapes, not the word "token": a file
	// that names variables must still parse.
	src := "version: 1\nforge:\n  auth: { token_env: GITHUB_TOKEN }\ntracker:\n  auth: { token_env: FLOW_PROJECT_TOKEN }\n"
	if _, err := Parse([]byte(src), DefaultPath); err != nil {
		t.Errorf("Parse refused a file that only names variables:\n%v", err)
	}
}
