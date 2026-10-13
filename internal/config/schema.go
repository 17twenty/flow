package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SchemaID is where the published `.flow.yaml` schema lives. It is the URL the
// `# yaml-language-server:` line at the top of a generated config points at, so
// an editor offers completion and inline validation (spec 03 §1).
const SchemaID = "https://raw.githubusercontent.com/17twenty/flow/main/assets/schema/flow.v1.json"

// SchemaFile is the path the generated schema is committed to, relative to the
// repository root.
const SchemaFile = "assets/schema/flow.v1.json"

// The committed schema is written by the test that guards it against going
// stale, rather than by a generator binary: a second main package would be a
// package outside the layout spec 01 §1 fixes (spec 03 §6 Decisions).
//
//go:generate go test . -run TestSchemaFileIsCurrent -update

// Schema is a JSON Schema document. flow writes its schema by hand rather than
// deriving it from the structs: a schema is a contract with editors and other
// tools, and it says things Go types cannot — which strings are durations,
// which may hold a placeholder, what a key means. A test keeps it honest
// against the structs in both directions (spec 03 §6).
type Schema struct {
	// Version is the JSON Schema dialect, on the root document only.
	Version string `json:"$schema,omitempty"`
	// ID is the canonical URL of the schema, on the root document only.
	ID string `json:"$id,omitempty"`
	// Title names the document the schema describes.
	Title string `json:"title,omitempty"`
	// Description says what the value is for, in the terms a reader needs.
	Description string `json:"description,omitempty"`
	// Type is the JSON type: object, array, string, integer or boolean.
	Type string `json:"type,omitempty"`
	// Enum lists every value allowed.
	Enum []string `json:"enum,omitempty"`
	// Pattern constrains a string.
	Pattern string `json:"pattern,omitempty"`
	// Minimum constrains a number from below.
	Minimum *int `json:"minimum,omitempty"`
	// Maximum constrains it from above.
	Maximum *int `json:"maximum,omitempty"`
	// Items describes the elements of an array.
	Items *Schema `json:"items,omitempty"`
	// Properties describes the known keys of an object.
	Properties map[string]*Schema `json:"properties,omitempty"`
	// Required lists the keys an object must have.
	Required []string `json:"required,omitempty"`
	// AdditionalProperties describes keys not named in Properties. false
	// refuses them, which is what strict decoding does.
	AdditionalProperties *Schema `json:"additionalProperties,omitempty"`
	// Examples shows values a reader can copy.
	Examples []string `json:"examples,omitempty"`
}

// deny is the "additionalProperties": false that mirrors strict decoding.
var deny = &Schema{}

// MarshalJSON renders false for the sentinel that refuses extra keys.
func (s *Schema) MarshalJSON() ([]byte, error) {
	if s == deny {
		return []byte("false"), nil
	}
	type plain Schema // avoid recursing into this method
	return json.Marshal((*plain)(s))
}

// ConfigSchema returns the JSON Schema for `.flow.yaml` v1.
func ConfigSchema() *Schema {
	root := object("flow workflow configuration: the board a team agreed to, and how flow keeps it true.",
		props{
			"version": integer("Schema version. Only 1 exists."),
			"preset":  enum("The shipped starting point this file was derived from.", "lean-kanban", "deploy-done", "minimal"),

			"forge": object("The Git host: pull requests, reviews, branch protection, commit statuses.", props{
				"type": enum("Which forge adapter to use.", ForgeGitHub, ForgeGitea, ForgeGitLab),
				"repo": pattern("The repository, as owner/name.", `^[^/\s]+/[^/\s]+$`, "17twenty/flow"),
				"url":  str("Base web URL, for self-managed installations.", "https://github.example.com"),
				"required_checks": array("Status contexts the trunk requires beyond flow's own gate.",
					str("One status context, matched exactly.", "test")),
				"auth": authSchema(),
			}, "type", "repo"),

			"tracker": object("The ticket system: the board, its columns and its items.", props{
				"type":       enum("Which tracker adapter to use.", TrackerGitHubProjects, TrackerJiraCloud, TrackerLinear),
				"owner":      str("The account the board belongs to.", "17twenty"),
				"owner_type": enum("Whether the owner is a person or an organisation.", OwnerUser, OwnerOrg),
				"project":    pending("The board: a GitHub project number, a Jira project key or a Linear team key.", "3"),
				"url":        str("Base URL, for trackers whose host is per-tenant.", "https://17twenty.atlassian.net"),
				"auth":       authSchema(),
			}, "type"),

			"workflow": object("The board flow keeps true.", props{
				"done_on": enum("Whether work is done when it merges, or when it is running.", DoneOnMerged, DoneOnDeployed),
				"columns": array("The board columns, in left-to-right order.", object("One board column.", props{
					"id":       pattern("Stable identifier, used everywhere else in this file.", `^[a-z][a-z0-9_]*$`, "in_progress"),
					"name":     str("The tracker's display name for this column.", "In progress"),
					"category": enum("Which of the three states this column counts as.", CategoryTodo, CategoryInProgress, CategoryDone),
					"gate":     enum("A condition tickets must satisfy to enter.", GateDefinitionOfReady),
					"wip": object("Work-in-progress limits for this column.", props{
						"limit":      count("Most items allowed in the column at once."),
						"per_person": count("Most items allowed per person at once."),
					}),
				}, "id", "name", "category")),
				"transitions": mapOf("Who moves a ticket for each event, keyed by event name (spec 04 §4).",
					object("Where an event takes a ticket, and who takes it there.", props{
						"to":    str("The id of the column the ticket moves to.", "in_review"),
						"owner": enum("flow performs the move; native means the tracker's own automation does and flow verifies it.", OwnerFlow, OwnerNative),
					}, "to", "owner")),
				"expedite": object("The agreed way to jump the queue, bounded so it stays the exception.", props{
					"labels": array("Labels that mark expedited work; any one is enough.", str("A label.", "incident")),
					"max":    count("Most expedited items allowed in flight at once."),
				}),
				"blocked": object("How the team records work that cannot move.", props{
					"label":           str("The label that marks a blocked ticket.", "blocked"),
					"require_comment": boolean("Demand an explanation alongside the label."),
				}),
			}, "done_on", "columns"),

			"definition_of_ready": object("What must be true of a ticket before anyone starts it (spec 04 §7).", props{
				"rules": array("The rules to enforce.", enum("A readiness rule.",
					"acceptance_criteria", "parent_link", "not_blocked", "flag_if_user_facing", "design_if_ui")),
				"max_size": duration("Largest estimate a ready ticket may carry, for trackers that record one."),
			}),

			"commits": object("The commit convention, which under a squash merge is also the pull request title convention (spec 08).", props{
				"convention":     enum("The grammar. Only Conventional Commits exists in v1.", "conventional"),
				"types":          array("The permitted commit types.", pattern("A commit type.", `^[a-z]+$`, "feat")),
				"header_max":     count("Longest header allowed, in characters."),
				"merge_strategy": enum("Which text becomes the commit header.", "squash", "merge", "rebase"),
				"scopes": object("Constraints on the scope part of the header.", props{
					"required": boolean("Demand a scope on every header."),
					"allowed":  array("Permitted scopes. An empty list allows any.", str("A scope.", "cli")),
				}),
				"unlinked_types": array("Types allowed to merge without a ticket.", str("A commit type.", "docs")),
			}),

			"deploys": object("Where flow looks to find out whether a merge is actually running (spec 13).", props{
				"source":      enum("Where deployment facts come from.", DeploySourceForge, DeploySourceCommitStatus, DeploySourceNone),
				"environment": str("The environment name the deployer records.", "production"),
				"contexts":    array("Commit status contexts to read, for the commit_status source. A trailing * matches by prefix.", str("A status context.", "argocd/app-prod")),
				"max_lag":     duration("The merged-to-deployed objective doctor holds the team to."),
				"max_age":     duration("How long flow tolerates seeing no deployment at all."),
			}),

			"policy": object("How the pull request gate presents itself (spec 09).", props{
				"status_context": str("The commit status name the gate publishes under.", "flow/policy"),
				"strictness":     enum("The baseline severity set.", StrictnessRelaxed, StrictnessStandard, StrictnessStrict),
				"comment":        enum("When the gate leaves a pull request comment.", CommentAlways, CommentOnFailure, CommentNever),
				"rules":          mapOf("Per-rule severity overrides, keyed by rule id.", enum("A severity.", SeverityOff, SeverityWarn, SeverityError)),
			}),

			"agents": object("Which contributors are machines, so the gate can insist a human stays accountable.", props{
				"authors":              array("Logins treated as agents.", str("A login.", "17twenty-flow-agent[bot]")),
				"labels":               array("Labels that mark a pull request as agent work.", str("A label.", "agent")),
				"require_human_review": boolean("Demand a human approval on agent pull requests."),
			}),

			"followups": object("How flow files the work it cannot do itself (spec 07 §4).", props{
				"labels": array("Labels applied to every follow-up flow opens.", str("A label.", "flow-followup")),
				"owners": object("Who to route a follow-up to, by the access it needs.", props{
					"default":       pending("Anyone the other roles do not claim.", "@17twenty"),
					"tracker_admin": pending("Work needing tracker administration.", "@17twenty"),
					"forge_admin":   pending("Work needing repository administration.", "@17twenty"),
					"platform":      pending("Work needing deployment or infrastructure access.", "@17twenty"),
				}),
			}),

			"attestation": object("How long a human's word stands (spec 07 §8).", props{
				"default_ttl": duration("How long an attestation stands before it must be renewed."),
			}),

			"health": object("How strictly the live-health audit grades what it finds (spec 07 §6).", props{
				"fail_below": percentage("The percentage of items a live check must see hold before it fails rather than warns. Omitted means 90."),
			}),
		}, "version")

	root.Version = "https://json-schema.org/draft/2020-12/schema"
	root.ID = SchemaID
	root.Title = "flow configuration (.flow.yaml) v1"
	return root
}

// SchemaJSON renders a schema as the bytes flow prints and commits: indented
// with two spaces, HTML left alone, and one trailing newline.
func SchemaJSON(schema *Schema) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(schema); err != nil {
		return nil, fmt.Errorf("render the schema: %w", err)
	}
	return out.Bytes(), nil
}

// props is the shape of an object's properties, named so the schema below
// reads as the document it describes.
type props map[string]*Schema

// object builds an object that refuses keys it does not name, mirroring the
// strict decoding every flow file gets.
func object(description string, properties props, required ...string) *Schema {
	return &Schema{
		Description:          description,
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: deny,
	}
}

// mapOf builds an object whose keys are open but whose values are described.
func mapOf(description string, values *Schema) *Schema {
	return &Schema{Description: description, Type: "object", AdditionalProperties: values}
}

// array builds a list of one kind of thing.
func array(description string, items *Schema) *Schema {
	return &Schema{Description: description, Type: "array", Items: items}
}

// str builds a plain string.
func str(description string, examples ...string) *Schema {
	return &Schema{Description: description, Type: "string", Examples: examples}
}

// pattern builds a string constrained by a regular expression.
func pattern(description, expression string, examples ...string) *Schema {
	return &Schema{Description: description, Type: "string", Pattern: expression, Examples: examples}
}

// enum builds a string drawn from a fixed set.
func enum(description string, values ...string) *Schema {
	return &Schema{Description: description, Type: "string", Enum: values}
}

// integer builds a whole number.
func integer(description string) *Schema {
	return &Schema{Description: description, Type: "integer"}
}

// count builds a whole number that cannot be negative.
func count(description string) *Schema {
	zero := 0
	return &Schema{Description: description, Type: "integer", Minimum: &zero}
}

// percentage builds a whole number of per cent, between one and a hundred.
// Zero is excluded because it is how an absent key reads, and a threshold
// nothing can fall below is not a threshold anybody meant to write.
func percentage(description string) *Schema {
	one, hundred := 1, 100
	return &Schema{Description: description, Type: "integer", Minimum: &one, Maximum: &hundred}
}

// boolean builds a true or false.
func boolean(description string) *Schema {
	return &Schema{Description: description, Type: "boolean"}
}

// durationPattern matches the durations ParseDuration accepts: Go's syntax
// extended with d and w, or a bare zero.
const durationPattern = `^0$|^[+-]?(\d+(\.\d+)?(ns|us|µs|μs|ms|s|m|h|d|w))+$`

// duration builds a string holding a span of time.
func duration(description string) *Schema {
	return &Schema{
		Description: description + " Go duration syntax plus d (24h) and w (7d).",
		Type:        "string",
		Pattern:     durationPattern,
		Examples:    []string{"90m", "2h", "7d", "30d"},
	}
}

// pending builds a string that may instead be a placeholder for a value
// nobody knows yet (spec 03 §2).
func pending(description string, examples ...string) *Schema {
	return &Schema{
		Description: description + " May be ${pending:name} until flow doctor resolves it.",
		Type:        "string",
		Examples:    append(examples, "${pending:project}"),
	}
}

// authSchema describes a credential source. The same shape serves the forge
// and the tracker, and neither ever holds a secret: flow stores the names of
// environment variables, not their contents (spec 15 §1).
func authSchema() *Schema {
	return object("Where the credential comes from. Names of environment variables only, never a secret.", props{
		"token_env": pattern("Name of the environment variable holding the API token.", `^[A-Z][A-Z0-9_]*$`, "GITHUB_TOKEN"),
		"email_env": pattern("Name of the environment variable holding the account email, for providers that pair one with a token.", `^[A-Z][A-Z0-9_]*$`, "JIRA_EMAIL"),
	})
}
