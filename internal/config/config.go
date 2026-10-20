// Package config reads the files that tell flow what the team wants: the
// committed `.flow.yaml`, the machine-written `.flow.lock`, and the personal
// TOML that may only soften presentation and credentials. It owns strict
// decoding, placeholders, precedence and the JSON Schema (spec 03).
//
// Nothing in here talks to a network or a forge: a Config is a value, and the
// engine is what turns it into intent.
package config

// Version is the only `.flow.yaml` schema version this build understands.
const Version = 1

// Preset names (spec 03 §7).
const (
	// PresetLeanKanban is the default: done on merge, native where GitHub
	// Projects offers it.
	PresetLeanKanban = "lean-kanban"
	// PresetDeployDone is lean-kanban with done meaning deployed.
	PresetDeployDone = "deploy-done"
	// PresetMinimal is the smallest workflow that still holds together.
	PresetMinimal = "minimal"
)

// PresetNames is every shipped preset, in the order they are offered.
var PresetNames = []string{PresetLeanKanban, PresetDeployDone, PresetMinimal}

// Forge kinds (spec 03 §1).
const (
	ForgeGitHub = "github"
	ForgeGitea  = "gitea"
	ForgeGitLab = "gitlab"
)

// Tracker kinds (spec 03 §1).
const (
	TrackerGitHubProjects = "github-projects"
	TrackerJiraCloud      = "jira-cloud"
	TrackerLinear         = "linear"
)

// Owner types for trackers that distinguish personal from organisation scope.
const (
	OwnerUser = "user"
	OwnerOrg  = "org"
)

// Column categories. Every column maps onto one of the three states the engine
// reasons about (spec 04).
const (
	CategoryTodo       = "todo"
	CategoryInProgress = "in_progress"
	CategoryDone       = "done"
)

// Gates a column can require before work may enter it.
const GateDefinitionOfReady = "definition_of_ready"

// Definition of Ready rule ids (spec 04 §7).
const (
	// DoRAcceptanceCriteria demands a filled-in acceptance criteria heading.
	DoRAcceptanceCriteria = "acceptance_criteria"
	// DoRParentLink demands the ticket hang off something bigger.
	DoRParentLink = "parent_link"
	// DoRNotBlocked refuses work that is already stuck.
	DoRNotBlocked = "not_blocked"
	// DoRFlagIfUserFacing demands a feature flag on user-facing work.
	DoRFlagIfUserFacing = "flag_if_user_facing"
	// DoRDesignIfUI demands a design link on UI work.
	DoRDesignIfUI = "design_if_ui"
)

// Commit conventions (spec 08).
const ConventionConventional = "conventional"

// Merge strategies. Which one the repository uses decides which text becomes
// the commit header, and so what the gate must check (spec 03 §1).
const (
	MergeSquash = "squash"
	MergeCommit = "merge"
	MergeRebase = "rebase"
)

// Event vocabulary (spec 04 §4). These are the keys of workflow.transitions.
const (
	// EventBranchCreated is somebody starting a branch for a ticket. Its
	// source is the tracker's own link between the two, never a branch name
	// flow read a key out of (P12, spec 04 §3).
	EventBranchCreated = "branch_created"
	// EventPROpenedDraft is a pull request opened as a draft.
	EventPROpenedDraft = "pr_opened_draft"
	// EventPROpened is a pull request opened ready for review, which counts
	// as EventPROpenedDraft followed by EventPRReadyForReview.
	EventPROpened = "pr_opened"
	// EventPRReadyForReview is a draft marked ready.
	EventPRReadyForReview = "pr_ready_for_review"
	// EventPRConvertedToDraft is a ready pull request put back to draft.
	EventPRConvertedToDraft = "pr_converted_to_draft"
	// EventChangesRequested is a review asking for changes.
	EventChangesRequested = "changes_requested"
	// EventApproved is the required approvals being met.
	EventApproved = "approved"
	// EventPRMerged is a merge.
	EventPRMerged = "pr_merged"
	// EventPRClosedUnmerged is a pull request closed without merging.
	EventPRClosedUnmerged = "pr_closed_unmerged"
	// EventIssueClosed is the linked ticket being closed.
	EventIssueClosed = "issue_closed"
	// EventDeployed is a successful deployment of the merge (spec 13).
	EventDeployed = "deployed"
)

// Events is the whole event vocabulary, in the order spec 04 §4 lists it.
var Events = []string{
	EventBranchCreated,
	EventPROpenedDraft,
	EventPROpened,
	EventPRReadyForReview,
	EventPRConvertedToDraft,
	EventChangesRequested,
	EventApproved,
	EventPRMerged,
	EventPRClosedUnmerged,
	EventIssueClosed,
	EventDeployed,
}

// What makes a ticket done (spec 04 §6).
const (
	DoneOnMerged   = "merged"
	DoneOnDeployed = "deployed"
)

// Who performs a transition (P11, spec 04 §5).
const (
	// OwnerFlow means flow moves the ticket itself.
	OwnerFlow = "flow"
	// OwnerNative means the tracker's own automation moves it and flow verifies.
	OwnerNative = "native"
)

// Deployment sources (spec 13).
const (
	DeploySourceForge        = "forge_deployments"
	DeploySourceCommitStatus = "commit_status"
	DeploySourceNone         = "none"
)

// Policy strictness levels (spec 09).
const (
	StrictnessRelaxed  = "relaxed"
	StrictnessStandard = "standard"
	StrictnessStrict   = "strict"
)

// When the policy gate leaves a comment on a pull request (spec 09).
const (
	CommentAlways    = "always"
	CommentOnFailure = "on_failure"
	CommentNever     = "never"
)

// Per-rule severity overrides under policy.rules (spec 09 §2).
const (
	SeverityOff   = "off"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Config is a decoded `.flow.yaml`. It is the team's desired workflow: what
// the board looks like, who moves tickets, and what the gate insists on.
type Config struct {
	// Version pins the schema. Only [Version] is accepted.
	Version int `yaml:"version"`
	// Preset names the shipped starting point this file was derived from.
	Preset string `yaml:"preset,omitempty"`

	// Forge is where the code and the pull requests live.
	Forge Forge `yaml:"forge"`
	// Tracker is where the tickets live.
	Tracker Tracker `yaml:"tracker"`
	// Workflow is the board and the rules for moving across it.
	Workflow Workflow `yaml:"workflow"`
	// DefinitionOfReady is what a ticket must have before work starts.
	DefinitionOfReady DefinitionOfReady `yaml:"definition_of_ready"`
	// Commits is the commit and pull request title convention.
	Commits Commits `yaml:"commits"`
	// Deploys is how flow learns that a merge reached production.
	Deploys Deploys `yaml:"deploys"`
	// Policy is how the pull request gate reports.
	Policy Policy `yaml:"policy"`
	// Agents describes which authors are machines.
	Agents Agents `yaml:"agents"`
	// FollowUps is how flow files work it cannot do itself.
	FollowUps FollowUps `yaml:"followups"`
	// Attestation is how long a human's word stands for.
	Attestation AttestationPolicy `yaml:"attestation"`
	// Health is how strictly the live-health audit grades what it finds.
	Health Health `yaml:"health,omitempty"`
}

// Auth names the environment variables a credential is read from. It never
// holds a credential: flow stores names, not secrets (spec 15 §1).
type Auth struct {
	// TokenEnv is the environment variable holding the API token.
	TokenEnv string `yaml:"token_env,omitempty"`
	// EmailEnv is the variable holding the account email, for providers like
	// Jira Cloud that pair an email with an API token.
	EmailEnv string `yaml:"email_env,omitempty"`
}

// Forge is the Git host: pull requests, reviews, branch protection, statuses.
type Forge struct {
	// Type selects the adapter: github, gitea or gitlab.
	Type string `yaml:"type"`
	// Repo is the owner/name of the repository.
	Repo string `yaml:"repo"`
	// URL is the base web URL for self-managed installations.
	URL string `yaml:"url,omitempty"`
	// RequiredChecks are the status contexts the team requires of the trunk
	// beyond flow's own gate, such as the build. Flow asks for them
	// additively, so a check nobody wrote here is left alone (spec 03 §1).
	RequiredChecks []string `yaml:"required_checks,omitempty"`
	// AutoMerge lets a pull request land itself once everything it is
	// waiting for has gone green: the repository setting is asked for in
	// the plan, and `flow check pr` arms each pull request it judges. It is
	// off unless the team says otherwise, because merging unattended is a
	// decision about who is accountable rather than a convenience (spec 09
	// §1).
	AutoMerge bool `yaml:"auto_merge,omitempty"`
	// Auth names the credential's environment variables.
	Auth Auth `yaml:"auth"`
}

// Tracker is the ticket system: the board, its columns and its items.
type Tracker struct {
	// Type selects the adapter: github-projects, jira-cloud or linear.
	Type string `yaml:"type"`
	// Owner is the account the board belongs to, for trackers that scope
	// projects to a user or an organisation.
	Owner string `yaml:"owner,omitempty"`
	// OwnerType disambiguates a personal account from an organisation.
	OwnerType string `yaml:"owner_type,omitempty"`
	// Project identifies the board: a GitHub project number, a Jira project
	// key or a Linear team key. It may be a placeholder until doctor resolves
	// it (spec 03 §2).
	Project Pending `yaml:"project,omitempty"`
	// URL is the tracker's base URL, for providers like Jira Cloud whose host
	// is per-tenant.
	URL string `yaml:"url,omitempty"`
	// SharedWith names the other repositories whose work appears on this
	// board, as owner/name. A board shared by a frontend and a backend holds
	// tickets this repository has no pull requests for, and the live-health
	// audit would report every one of them as work that reached Done without
	// a merge. Naming the sibling says that its tickets are audited by the
	// flow running there; a ticket from a repository nobody named is still a
	// finding, because nobody is watching it (spec 03 §1).
	SharedWith []string `yaml:"shared_with,omitempty"`
	// Auth names the credential's environment variables.
	Auth Auth `yaml:"auth"`
}

// Workflow is the board flow keeps true: the columns in order, who moves
// tickets on each event, and the two escape hatches teams actually need.
type Workflow struct {
	// DoneOn decides whether done means merged or deployed (spec 04 §6).
	DoneOn string `yaml:"done_on"`
	// Columns are the board columns in left-to-right order.
	Columns []Column `yaml:"columns"`
	// Transitions maps an event from the vocabulary in spec 04 §4 to the
	// column it lands in and who performs the move.
	Transitions map[string]Transition `yaml:"transitions"`
	// Expedite lets urgent work bypass WIP limits.
	Expedite Expedite `yaml:"expedite"`
	// Blocked describes how the team marks work it cannot progress.
	Blocked Blocked `yaml:"blocked"`
}

// Column is one board column. Ids are stable and internal; names are whatever
// the tracker displays and may be renamed without breaking configuration.
type Column struct {
	// ID is the stable identifier used everywhere else in the configuration.
	ID string `yaml:"id"`
	// Name is the tracker's display name for this column.
	Name string `yaml:"name"`
	// Category maps the column onto todo, in_progress or done.
	Category string `yaml:"category"`
	// Gate names a condition tickets must satisfy to enter, if any.
	Gate string `yaml:"gate,omitempty"`
	// WIP limits how much work may sit here at once. Nil means unlimited.
	WIP *WIP `yaml:"wip,omitempty"`
}

// WIP is a work-in-progress limit for one column. A column may limit the whole
// column, the work per person, or both.
type WIP struct {
	// Limit caps the items in the column. Zero means no column-wide cap.
	Limit int `yaml:"limit,omitempty"`
	// PerPerson caps the items assigned to any one person. Zero means no cap.
	PerPerson int `yaml:"per_person,omitempty"`
}

// Transition says where an event takes a ticket, and who takes it there.
type Transition struct {
	// To is the id of the column the ticket moves to.
	To string `yaml:"to"`
	// Owner is flow or native (P11).
	Owner string `yaml:"owner"`
}

// Expedite is the agreed way to jump the queue, bounded so that it stays the
// exception.
type Expedite struct {
	// Labels mark expedited work; any one of them is enough.
	Labels []string `yaml:"labels,omitempty"`
	// Max caps how many expedited items may be in flight at once.
	Max int `yaml:"max,omitempty"`
}

// Blocked is how the team records work that cannot move, and what it owes the
// rest of the team when it does.
type Blocked struct {
	// Label marks a blocked ticket.
	Label string `yaml:"label,omitempty"`
	// RequireComment demands an explanation alongside the label.
	RequireComment bool `yaml:"require_comment,omitempty"`
}

// DefinitionOfReady is what must be true of a ticket before anyone starts it
// (spec 04 §7).
type DefinitionOfReady struct {
	// Rules are the rule ids to enforce, in spec 04 §7.
	Rules []string `yaml:"rules,omitempty"`
	// MaxSize bounds a ticket's estimate, for trackers that record one. Zero
	// disables the check.
	MaxSize Duration `yaml:"max_size,omitempty"`
}

// Commits is the commit convention, which under a squash merge is also the
// pull request title convention (spec 08).
type Commits struct {
	// Convention names the grammar. Only "conventional" exists in v1.
	Convention string `yaml:"convention"`
	// Types are the permitted commit types.
	Types []string `yaml:"types,omitempty"`
	// HeaderMax limits the header length in runes.
	HeaderMax int `yaml:"header_max,omitempty"`
	// MergeStrategy tells the gate which text becomes the commit header.
	MergeStrategy string `yaml:"merge_strategy,omitempty"`
	// Scopes constrains the scope part of the header.
	Scopes Scopes `yaml:"scopes"`
	// UnlinkedTypes are the types allowed to merge without a ticket.
	UnlinkedTypes []string `yaml:"unlinked_types,omitempty"`
}

// Scopes constrains the optional scope in a Conventional Commit header.
type Scopes struct {
	// Required demands a scope on every header.
	Required bool `yaml:"required,omitempty"`
	// Allowed restricts scopes to this list. Empty allows any scope.
	Allowed []string `yaml:"allowed,omitempty"`
}

// Deploys is where flow looks to find out whether a merge is actually running
// (spec 13).
type Deploys struct {
	// Source selects forge deployment records, commit statuses, or nothing.
	Source string `yaml:"source"`
	// Environment is the environment name the deployer records.
	Environment string `yaml:"environment,omitempty"`
	// Contexts are the commit status contexts to read, for the commit_status
	// source. A trailing * matches by prefix.
	Contexts []string `yaml:"contexts,omitempty"`
	// MaxLag is the merged-to-deployed objective doctor holds the team to.
	MaxLag Duration `yaml:"max_lag,omitempty"`
	// MaxAge is how long flow tolerates seeing no deployment at all.
	MaxAge Duration `yaml:"max_age,omitempty"`
}

// Policy is how the pull request gate presents itself (spec 09).
type Policy struct {
	// StatusContext is the commit status name the gate publishes under.
	StatusContext string `yaml:"status_context,omitempty"`
	// Strictness selects the baseline severity set.
	Strictness string `yaml:"strictness,omitempty"`
	// Comment decides when the gate leaves a pull request comment.
	Comment string `yaml:"comment,omitempty"`
	// Rules overrides individual rule severities: off, warn or error.
	Rules map[string]string `yaml:"rules,omitempty"`
}

// Agents tells flow which contributors are machines, so that the gate can
// insist a human stays accountable (spec 09, spec 17).
type Agents struct {
	// Authors are the logins treated as agents.
	Authors []string `yaml:"authors,omitempty"`
	// Labels mark a pull request as agent work.
	Labels []string `yaml:"labels,omitempty"`
	// RequireHumanReview demands a human approval on agent pull requests.
	RequireHumanReview bool `yaml:"require_human_review,omitempty"`
	// UnreviewedTypes are the commit types a machine may land without one.
	// The exemption is a class of change the team wrote down here, not a
	// marker on a pull request: an agent can label its own work, and it
	// cannot approve a change to this file.
	UnreviewedTypes []string `yaml:"unreviewed_types,omitempty"`
}

// FollowUps is how flow files the work it cannot do itself, so that a refusal
// becomes a tracked ticket rather than a dead end (spec 07 §4).
type FollowUps struct {
	// Labels are applied to every follow-up ticket flow opens.
	Labels []string `yaml:"labels,omitempty"`
	// Owners routes a follow-up to whoever can actually act on it.
	Owners Owners `yaml:"owners"`
}

// Owners routes follow-ups by the kind of access they need. Values may be
// placeholders until doctor resolves them (spec 03 §2).
type Owners struct {
	// Default receives anything the other roles do not claim.
	Default Pending `yaml:"default,omitempty"`
	// TrackerAdmin receives work needing tracker administration.
	TrackerAdmin Pending `yaml:"tracker_admin,omitempty"`
	// ForgeAdmin receives work needing repository administration.
	ForgeAdmin Pending `yaml:"forge_admin,omitempty"`
	// Platform receives work needing deployment or infrastructure access.
	Platform Pending `yaml:"platform,omitempty"`
}

// AttestationPolicy is how long a human's "I checked this myself" counts for
// (spec 07 §8). The attestations themselves live in the lock.
type AttestationPolicy struct {
	// DefaultTTL is how long an attestation stands before it must be renewed.
	DefaultTTL Duration `yaml:"default_ttl,omitempty"`
}

// DefaultFailBelow is the share a live-health check must reach before it fails
// rather than warns, as a percentage (spec 07 §6).
const DefaultFailBelow = 90

// DefaultFlagMaxAge is how long a feature flag may stand before the audit
// reports it as one nobody came back for (spec 07 §6).
const DefaultFlagMaxAge = 90 * Day

// DefaultFlagKeptLabel is the label on a cleanup ticket that says the flag it
// is about stays on purpose.
const DefaultFlagKeptLabel = "flag-kept"

// DefaultStaleAfter is how long work may sit in a column with nothing
// happening to it before the audit mentions it, in working days (spec 07 §6).
const DefaultStaleAfter = 5

// Health is how the live-health audit grades what it saw (spec 07 §6).
//
// The window it looks back over is not here: `--window` is a property of the
// run, and a daily job and somebody checking by hand want different ones from
// the same repository. How strictly the answer is graded is a property of the
// team, so it is written down with the rest of what the team agreed to.
type Health struct {
	// FailBelow is the percentage of items a live check must see hold before
	// it fails rather than warns. Zero means [DefaultFailBelow].
	FailBelow int `yaml:"fail_below,omitempty"`
	// FlagMaxAge is how long a feature flag may stand before the audit asks
	// about it, measured from the commit that announced it. Zero means
	// [DefaultFlagMaxAge].
	FlagMaxAge Duration `yaml:"flag_max_age,omitempty"`
	// FlagKeptLabel is the label a team puts on a cleanup ticket to say that
	// flag stays on purpose. Empty means [DefaultFlagKeptLabel].
	FlagKeptLabel string `yaml:"flag_kept_label,omitempty"`
	// StaleAfter is how many working days work may sit in a column with
	// nothing happening to it or to its pull requests before the audit
	// says so. Zero means [DefaultStaleAfter].
	//
	// One number covers every column that has a patience, rather than one
	// per column: a team that waits a week for a review and a week for
	// work in progress has one answer to "how long is too long", and two
	// keys would mostly be set to the same thing and occasionally be set
	// to different things by accident.
	StaleAfter int `yaml:"stale_after,omitempty"`
}

// Threshold is the share a live check must reach, as a ratio. An unset
// FailBelow is zero here rather than [DefaultFailBelow]: the engine applies
// the default, and two places knowing one number is how they come to disagree.
func (h Health) Threshold() float64 {
	return float64(h.FailBelow) / 100
}

// Column returns the column with the given id, and whether it exists.
func (w Workflow) Column(id string) (Column, bool) {
	for _, c := range w.Columns {
		if c.ID == id {
			return c, true
		}
	}
	return Column{}, false
}

// ColumnIndex returns the position of the column with the given id in board
// order, or -1. Order is what makes "forward-only" meaningful (spec 04 §5).
func (w Workflow) ColumnIndex(id string) int {
	for i, c := range w.Columns {
		if c.ID == id {
			return i
		}
	}
	return -1
}
