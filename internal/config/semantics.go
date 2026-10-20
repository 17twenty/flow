package config

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/core"
	yaml "go.yaml.in/yaml/v3"
)

// Vocabularies the configuration is checked against. Decoding only proves a
// value is a string; these decide whether it is a string flow knows.
var (
	forgeKinds      = []string{ForgeGitHub, ForgeGitea, ForgeGitLab}
	trackerKinds    = []string{TrackerGitHubProjects, TrackerJiraCloud, TrackerLinear}
	ownerTypes      = []string{OwnerUser, OwnerOrg}
	categories      = []string{CategoryTodo, CategoryInProgress, CategoryDone}
	doneOn          = []string{DoneOnMerged, DoneOnDeployed}
	owners          = []string{OwnerFlow, OwnerNative}
	deploySources   = []string{DeploySourceForge, DeploySourceCommitStatus, DeploySourceNone}
	strictnesses    = []string{StrictnessRelaxed, StrictnessStandard, StrictnessStrict}
	commentWhen     = []string{CommentAlways, CommentOnFailure, CommentNever}
	severities      = []string{SeverityOff, SeverityWarn, SeverityError}
	mergeStrategies = []string{MergeSquash, MergeCommit, MergeRebase}
	readinessRules  = []string{DoRAcceptanceCriteria, DoRParentLink, DoRNotBlocked, DoRFlagIfUserFacing, DoRDesignIfUI}
)

// Patterns values must match to be usable as identifiers.
var (
	columnIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	repoRe     = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
	typeRe     = regexp.MustCompile(`^[a-z]+$`)
	scopeRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
)

// Header length bounds. A limit below the lower bound leaves no room for a
// type, a scope and a verb; one above the upper bound is not a limit.
const (
	minHeaderMax = 20
	maxHeaderMax = 200
)

// Validate reports everything wrong with a decoded configuration that decoding
// alone cannot see: values outside their vocabulary, parts that contradict
// each other, and ownership the tracker cannot actually honour. It is the
// `config.semantics` check (spec 03 §1.1).
//
// registry supplies the capability declarations. It may be nil, in which case
// the checks that need to know what a tracker can do are skipped — every
// command that can reach the embedded assets passes it.
func Validate(cfg *Config, path string, registry core.Registry) error {
	if cfg == nil {
		return core.Errorf(core.ErrValidation, "validate a configuration").WithWhy("there is none")
	}
	if path == "" {
		path = DefaultPath
	}

	v := &validator{path: path, cfg: cfg, registry: registry}
	v.preset()
	v.forge()
	v.tracker()
	v.workflow()
	v.readiness()
	v.commits()
	v.deploys()
	v.policy()
	v.agents()
	v.followUps()
	v.health()

	if len(v.problems) == 0 {
		return nil
	}
	return newParseError(path, v.problems)
}

// validator accumulates problems so that one run reports everything wrong
// with a file, in the order the file declares it.
type validator struct {
	path     string
	cfg      *Config
	registry core.Registry
	problems []Problem
}

// add records a problem. Semantic problems have no position: they are about
// the configuration as a whole, so each one names the key it is about.
func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: v.path, Message: fmt.Sprintf(format, args...)})
}

// oneOf records a problem unless value is in the vocabulary. An empty value is
// the caller's business: some keys have defaults and some are required.
func (v *validator) oneOf(key, value string, vocabulary []string) bool {
	if slices.Contains(vocabulary, value) {
		return true
	}
	v.add("%s is %q; it must be %s", key, value, strings.Join(vocabulary, ", "))
	return false
}

// required records a problem when a value that has no default is missing.
func (v *validator) required(key, value, example string) bool {
	if strings.TrimSpace(value) != "" {
		return true
	}
	v.add("%s is not set; it must be %s", key, example)
	return false
}

// envName checks a key that must hold the name of an environment variable.
// The mistake it exists for is pasting the credential itself, which would put
// a secret in a committed file (spec 03 §1.1, spec 15 §1).
func (v *validator) envName(key, value string) {
	switch {
	case value == "":
	case core.Redact(value) != value:
		v.add("%s holds a credential, not the name of one; %s is committed, so export the secret and name the variable here", key, DefaultPath)
	case !IsEnvName(value):
		v.add("%s is %q; it must be the NAME of an environment variable, such as GITHUB_TOKEN", key, value)
	}
}

// preset checks the preset this file says it came from.
func (v *validator) preset() {
	if p := v.cfg.Preset; p != "" {
		v.oneOf("preset", p, PresetNames)
	}
}

// forge checks the Git host.
func (v *validator) forge() {
	forge := v.cfg.Forge
	if v.required("forge.type", forge.Type, strings.Join(forgeKinds, ", ")) {
		if v.oneOf("forge.type", forge.Type, forgeKinds) {
			v.declares("forge.type", forge.Type, core.KindForge)
		}
	}
	if v.required("forge.repo", forge.Repo, "the repository, as owner/name") && !repoRe.MatchString(forge.Repo) {
		v.add("forge.repo is %q; it must be owner/name, with no spaces and exactly one slash", forge.Repo)
	}
	v.webURL("forge.url", forge.URL)
	seen := map[string]bool{}
	for i, context := range forge.RequiredChecks {
		at := fmt.Sprintf("forge.required_checks[%d]", i)
		switch {
		case strings.TrimSpace(context) == "":
			v.add("%s is empty", at)
		case strings.TrimSpace(context) != context:
			v.add("%s is %q; a status context is matched exactly, so the spaces around it are part of it", at, context)
		case seen[context]:
			v.add("%s repeats %q", at, context)
		default:
			seen[context] = true
		}
	}
	v.envName("forge.auth.token_env", forge.Auth.TokenEnv)
	if forge.Auth.EmailEnv != "" {
		v.add("forge.auth.email_env is set; a forge authenticates with a token alone")
	}
}

// tracker checks the ticket system, including the keys that mean something
// for one tracker and nothing for another.
func (v *validator) tracker() {
	tracker := v.cfg.Tracker
	if !v.required("tracker.type", tracker.Type, strings.Join(trackerKinds, ", ")) {
		return
	}
	if !v.oneOf("tracker.type", tracker.Type, trackerKinds) {
		return
	}
	v.declares("tracker.type", tracker.Type, core.KindTracker)

	if tracker.Project.IsZero() {
		v.add("tracker.project is not set; it must be the board: a GitHub project number, a Jira project key or a Linear team key, or %sproject} until flow doctor resolves it", PlaceholderPrefix)
	}
	if tracker.OwnerType != "" {
		v.oneOf("tracker.owner_type", tracker.OwnerType, ownerTypes)
	}
	v.envName("tracker.auth.token_env", tracker.Auth.TokenEnv)
	v.envName("tracker.auth.email_env", tracker.Auth.EmailEnv)

	switch tracker.Type {
	case TrackerGitHubProjects:
		v.required("tracker.owner", tracker.Owner, "the user or organisation the project belongs to")
		if tracker.OwnerType == "" {
			v.add("tracker.owner_type is not set; GitHub addresses a user's projects and an organisation's differently, so flow will not guess: set %s or %s", OwnerUser, OwnerOrg)
		}
		if tracker.Auth.EmailEnv != "" {
			v.add("tracker.auth.email_env is set; GitHub Projects authenticates with a token alone")
		}
	case TrackerJiraCloud:
		v.required("tracker.url", tracker.URL, "your Jira site, such as https://acme.atlassian.net")
		if tracker.Auth.EmailEnv == "" {
			v.add("tracker.auth.email_env is not set; a Jira Cloud API token only works alongside the email of the account that made it")
		}
		v.trackerHasNoOwner()
	case TrackerLinear:
		if tracker.URL != "" {
			v.add("tracker.url is set; Linear has one host, so there is nothing to point flow at")
		}
		if tracker.Auth.EmailEnv != "" {
			v.add("tracker.auth.email_env is set; a Linear API key authenticates on its own")
		}
		v.trackerHasNoOwner()
	}
	v.webURL("tracker.url", tracker.URL)
}

// trackerHasNoOwner reports owner keys set for a tracker that has no such
// concept, rather than ignoring them and leaving a reader to believe they did
// something.
func (v *validator) trackerHasNoOwner() {
	if v.cfg.Tracker.Owner != "" {
		v.add("tracker.owner is set; only github-projects scopes a board to an account, and %s identifies it by tracker.project", v.cfg.Tracker.Type)
	}
	if v.cfg.Tracker.OwnerType != "" {
		v.add("tracker.owner_type is set; only github-projects distinguishes a user from an organisation")
	}
}

// declares checks that flow ships a capability declaration for an adapter of
// the right kind. Without one, nothing downstream could decide what flow may
// attempt (spec 05 §1).
func (v *validator) declares(key, adapter, kind string) {
	if v.registry == nil {
		return
	}
	set, err := v.registry.Of(adapter)
	if err != nil {
		v.add("%s is %q, which this build ships no capability declaration for", key, adapter)
		return
	}
	if set.Kind != kind {
		v.add("%s is %q, which is a %s, not a %s", key, adapter, set.Kind, kind)
	}
}

// webURL checks an optional base URL.
func (v *validator) webURL(key, raw string) {
	if raw == "" {
		return
	}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		v.add("%s is %q, which is not a URL", key, raw)
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		v.add("%s is %q; it must start with https://", key, raw)
	case parsed.Host == "":
		v.add("%s is %q; it names no host", key, raw)
	}
}

// workflow checks the board: its columns, and the events that move tickets
// across it.
func (v *validator) workflow() {
	v.columns()
	v.transitions()
	v.doneOn()

	expedite := v.cfg.Workflow.Expedite
	if expedite.Max < 0 {
		v.add("workflow.expedite.max is %d; it cannot be negative", expedite.Max)
	}
	if expedite.Max > 0 && len(expedite.Labels) == 0 {
		v.add("workflow.expedite.max is set but workflow.expedite.labels is empty; nothing would ever count as expedited")
	}
	for i, label := range expedite.Labels {
		if strings.TrimSpace(label) == "" {
			v.add("workflow.expedite.labels[%d] is empty", i)
		}
	}
	if blocked := v.cfg.Workflow.Blocked; blocked.RequireComment && blocked.Label == "" {
		v.add("workflow.blocked.require_comment is set but workflow.blocked.label is not; flow would have nothing to notice")
	}
}

// columns checks the board columns and the one thing every board needs: a
// single place work ends up.
func (v *validator) columns() {
	workflow := v.cfg.Workflow
	if len(workflow.Columns) == 0 {
		v.add("workflow.columns is empty; a board needs at least one column, and exactly one of them must be the %s one", CategoryDone)
		return
	}

	seenID := map[string]bool{}
	seenName := map[string]bool{}
	done := 0
	for i, column := range workflow.Columns {
		at := fmt.Sprintf("workflow.columns[%d]", i)
		switch {
		case column.ID == "":
			v.add("%s.id is not set; it is the stable name the rest of this file refers to", at)
		case !columnIDRe.MatchString(column.ID):
			v.add("%s.id is %q; it must start with a lowercase letter and hold only lowercase letters, digits and underscores", at, column.ID)
		case seenID[column.ID]:
			v.add("%s.id is %q, which another column already uses", at, column.ID)
		default:
			seenID[column.ID] = true
		}

		if v.required(at+".name", column.Name, "the column's name on the board") {
			key := strings.ToLower(strings.TrimSpace(column.Name))
			if seenName[key] {
				v.add("%s.name is %q, which another column already uses; flow matches columns to the board by name", at, column.Name)
			}
			seenName[key] = true
		}

		if v.oneOf(at+".category", column.Category, categories) && column.Category == CategoryDone {
			done++
		}
		if column.Gate != "" && v.oneOf(at+".gate", column.Gate, []string{GateDefinitionOfReady}) && len(v.cfg.DefinitionOfReady.Rules) == 0 {
			v.add("%s.gate is %s but definition_of_ready.rules is empty; the gate would let everything through", at, GateDefinitionOfReady)
		}
		v.wip(at, column.WIP)
	}

	if done != 1 {
		v.add("%d columns are in the %s category; exactly one must be, because it is what finished means", done, CategoryDone)
	}
}

// wip checks one column's limits.
func (v *validator) wip(at string, wip *WIP) {
	if wip == nil {
		return
	}
	if wip.Limit < 0 {
		v.add("%s.wip.limit is %d; it cannot be negative", at, wip.Limit)
	}
	if wip.PerPerson < 0 {
		v.add("%s.wip.per_person is %d; it cannot be negative", at, wip.PerPerson)
	}
	if wip.Limit == 0 && wip.PerPerson == 0 {
		v.add("%s.wip sets no limit; remove it, or set limit or per_person", at)
	}
}

// transitions checks who moves a ticket for each event, and whether the
// tracker can actually be made to do it (P11, spec 04 §5).
func (v *validator) transitions() {
	for _, event := range slices.Sorted(maps.Keys(v.cfg.Workflow.Transitions)) {
		transition := v.cfg.Workflow.Transitions[event]
		at := "workflow.transitions." + event

		if !slices.Contains(Events, event) {
			v.add("%s is not an event flow knows; the vocabulary is %s", at, strings.Join(Events, ", "))
			continue
		}
		if event == EventPROpened {
			v.add("%s is not configured on its own: spec 04 §4 defines %s as %s followed by %s, so configure those two instead", at, EventPROpened, EventPROpenedDraft, EventPRReadyForReview)
			continue
		}
		if v.required(at+".to", transition.To, "the id of the column the ticket moves to") {
			if _, ok := v.cfg.Workflow.Column(transition.To); !ok {
				v.add("%s.to is %q, which is not a column id in workflow.columns", at, transition.To)
			}
		}
		if !v.oneOf(at+".owner", transition.Owner, owners) || transition.Owner != OwnerNative {
			continue
		}
		v.nativeOwnership(at, event)
	}
}

// nativeOwnership checks that the tracker really does have an automation for
// an event flow has been told it owns. Claiming native for something the
// tracker cannot do means nobody moves the ticket (spec 03 §1.1).
func (v *validator) nativeOwnership(at, event string) {
	if v.registry == nil || v.cfg.Tracker.Type == "" {
		return
	}
	set, err := v.registry.Of(v.cfg.Tracker.Type)
	if err != nil {
		return
	}
	name, known := core.NativeTransitionCapability(event)
	if !known {
		v.add("%s.owner is %s, but no tracker offers an automation for %s: use owner: %s", at, OwnerNative, event, OwnerFlow)
		return
	}
	capability := set.Capability(name)
	// An automation flow cannot read back is still an automation: the
	// declaration says so by asking for an attestation rather than for
	// nothing (spec 05 §4). What makes `owner: native` a lie is a tracker
	// that has no such workflow at all, which is the declaration that
	// offers no remedy either.
	if !capability.Set && !capability.Verify && capability.Remediation != core.RemediationAttest {
		v.add("%s.owner is %s, but %s has no automation for %s: use owner: %s", at, OwnerNative, v.cfg.Tracker.Type, event, OwnerFlow)
	}
}

// doneOn checks that what the team means by done, how tickets get there, and
// whether flow can see deployments at all, are the same answer (spec 04 §6).
func (v *validator) doneOn() {
	workflow := v.cfg.Workflow
	if !v.oneOf("workflow.done_on", workflow.DoneOn, doneOn) {
		return
	}
	_, deployed := workflow.Transitions[EventDeployed]
	source := v.cfg.Deploys.Source

	switch workflow.DoneOn {
	case DoneOnDeployed:
		if !deployed {
			v.add("workflow.done_on is %s but there is no workflow.transitions.%s; nothing would ever reach the %s column", DoneOnDeployed, EventDeployed, CategoryDone)
		}
		if source == DeploySourceNone || source == "" {
			v.add("workflow.done_on is %s but deploys.source is %s; flow would have no way to learn that anything was deployed", DoneOnDeployed, DeploySourceNone)
		}
	case DoneOnMerged:
		if deployed {
			v.add("workflow.done_on is %s but workflow.transitions.%s is set; under %s a deployment is verified as lag, not tracked as status (spec 04 §6)", DoneOnMerged, EventDeployed, DoneOnMerged)
		}
	}
}

// readiness checks the definition of ready.
func (v *validator) readiness() {
	seen := map[string]bool{}
	for i, rule := range v.cfg.DefinitionOfReady.Rules {
		at := fmt.Sprintf("definition_of_ready.rules[%d]", i)
		if !v.oneOf(at, rule, readinessRules) {
			continue
		}
		if seen[rule] {
			v.add("%s repeats %q", at, rule)
		}
		seen[rule] = true
	}
	if v.cfg.DefinitionOfReady.MaxSize < 0 {
		v.add("definition_of_ready.max_size is negative")
	}
}

// commits checks the commit convention.
func (v *validator) commits() {
	commits := v.cfg.Commits
	if commits.Convention != "" {
		v.oneOf("commits.convention", commits.Convention, []string{ConventionConventional})
	}
	if commits.MergeStrategy != "" {
		v.oneOf("commits.merge_strategy", commits.MergeStrategy, mergeStrategies)
	}
	if commits.HeaderMax != 0 && (commits.HeaderMax < minHeaderMax || commits.HeaderMax > maxHeaderMax) {
		v.add("commits.header_max is %d; it must be between %d and %d", commits.HeaderMax, minHeaderMax, maxHeaderMax)
	}

	seen := map[string]bool{}
	for i, kind := range commits.Types {
		at := fmt.Sprintf("commits.types[%d]", i)
		switch {
		case !typeRe.MatchString(kind):
			v.add("%s is %q; a type is lowercase letters, such as feat", at, kind)
		case seen[kind]:
			v.add("%s repeats %q", at, kind)
		default:
			seen[kind] = true
		}
	}
	for i, kind := range commits.UnlinkedTypes {
		at := fmt.Sprintf("commits.unlinked_types[%d]", i)
		if len(commits.Types) > 0 && !seen[kind] {
			v.add("%s is %q, which is not in commits.types; a type that cannot be used cannot be exempted", at, kind)
		}
	}

	scopes := map[string]bool{}
	for i, scope := range commits.Scopes.Allowed {
		at := fmt.Sprintf("commits.scopes.allowed[%d]", i)
		switch {
		case !scopeRe.MatchString(scope):
			v.add("%s is %q; a scope is lowercase letters, digits, dots, dashes and underscores", at, scope)
		case scopes[scope]:
			v.add("%s repeats %q", at, scope)
		default:
			scopes[scope] = true
		}
	}
	if commits.Scopes.Required && len(commits.Scopes.Allowed) == 0 {
		v.add("commits.scopes.required is set but commits.scopes.allowed is empty; every scope would be accepted, which is not a convention")
	}
}

// deploys checks where flow looks to find out whether a merge is running.
func (v *validator) deploys() {
	deploys := v.cfg.Deploys
	source := deploys.Source
	if source == "" {
		source = DeploySourceNone
	}
	if !v.oneOf("deploys.source", source, deploySources) {
		return
	}

	if deploys.MaxLag < 0 {
		v.add("deploys.max_lag is negative")
	}
	if deploys.MaxAge < 0 {
		v.add("deploys.max_age is negative")
	}

	switch source {
	case DeploySourceForge:
		v.required("deploys.environment", deploys.Environment, "the environment name the deployer records, such as production")
		if len(deploys.Contexts) > 0 {
			v.add("deploys.contexts is set but deploys.source is %s; contexts name commit statuses, which only the %s source reads", DeploySourceForge, DeploySourceCommitStatus)
		}
	case DeploySourceCommitStatus:
		if len(deploys.Contexts) == 0 {
			v.add("deploys.source is %s but deploys.contexts is empty; flow would not know which status means deployed", DeploySourceCommitStatus)
		}
		for i, context := range deploys.Contexts {
			if strings.TrimSpace(context) == "" {
				v.add("deploys.contexts[%d] is empty", i)
			}
		}
	case DeploySourceNone:
		if deploys.Environment != "" || len(deploys.Contexts) > 0 || deploys.MaxLag != 0 || deploys.MaxAge != 0 {
			v.add("deploys.source is %s but other deploys keys are set; flow reads no deployments at all, so none of them can be honoured", DeploySourceNone)
		}
	}
}

// policy checks how the pull request gate presents itself.
func (v *validator) policy() {
	policy := v.cfg.Policy
	if policy.Strictness != "" {
		v.oneOf("policy.strictness", policy.Strictness, strictnesses)
	}
	if policy.Comment != "" {
		v.oneOf("policy.comment", policy.Comment, commentWhen)
	}
	if context := policy.StatusContext; context != "" && strings.ContainsAny(context, " \t") {
		v.add("policy.status_context is %q; a commit status context has no spaces in it", context)
	}
	// Rule ids themselves are checked against the rule registry by the gate
	// (spec 09 §2); here only the severity has a fixed vocabulary.
	for _, rule := range slices.Sorted(maps.Keys(policy.Rules)) {
		v.oneOf("policy.rules."+rule, policy.Rules[rule], severities)
	}
}

// agents checks the description of which contributors are machines.
func (v *validator) agents() {
	agents := v.cfg.Agents
	seen := map[string]bool{}
	for i, author := range agents.Authors {
		at := fmt.Sprintf("agents.authors[%d]", i)
		switch {
		case strings.TrimSpace(author) == "":
			v.add("%s is empty", at)
		case seen[author]:
			v.add("%s repeats %q", at, author)
		default:
			seen[author] = true
		}
	}
	if agents.RequireHumanReview && len(agents.Authors) == 0 && len(agents.Labels) == 0 {
		v.add("agents.require_human_review is set but no agents.authors or agents.labels are; nothing would ever count as agent work")
	}

	types := map[string]bool{}
	for _, kind := range v.cfg.Commits.Types {
		types[kind] = true
	}
	for i, kind := range agents.UnreviewedTypes {
		at := fmt.Sprintf("agents.unreviewed_types[%d]", i)
		if len(v.cfg.Commits.Types) > 0 && !types[kind] {
			v.add("%s is %q, which is not in commits.types; a type that cannot be used cannot be exempted", at, kind)
		}
	}
}

// followUps checks how flow files the work it cannot do itself.
func (v *validator) followUps() {
	for i, label := range v.cfg.FollowUps.Labels {
		if strings.TrimSpace(label) == "" {
			v.add("followups.labels[%d] is empty", i)
		}
	}
	owners := v.cfg.FollowUps.Owners
	for key, owner := range map[string]Pending{
		"followups.owners.default":       owners.Default,
		"followups.owners.tracker_admin": owners.TrackerAdmin,
		"followups.owners.forge_admin":   owners.ForgeAdmin,
		"followups.owners.platform":      owners.Platform,
	} {
		if !owner.IsZero() && !owner.IsPending() && strings.TrimSpace(owner.Text) == "" {
			v.add("%s is blank; a follow-up nobody owns is not a follow-up", key)
		}
	}
	if v.cfg.Attestation.DefaultTTL < 0 {
		v.add("attestation.default_ttl is negative")
	}
}

// health checks how strictly the live-health audit is graded. A share outside
// one and a hundred per cent is not a threshold anybody meant: zero is how an
// absent key reads and would mean nothing can ever fall below the line, and
// anything above a hundred would fail a fortnight in which nothing went wrong.
func (v *validator) health() {
	below := v.cfg.Health.FailBelow
	if below != 0 && (below < 1 || below > 100) {
		v.add("health.fail_below is %d; it must be a percentage between 1 and 100", below)
	}
	// An age that has already passed makes every flag ever announced a
	// finding on the day it is announced, which is not a review of anything.
	if age := v.cfg.Health.FlagMaxAge; age < 0 {
		v.add("health.flag_max_age is %s; it must be a length of time, not a negative one", age)
	}
	// A patience of nought working days would report everything on the
	// board as stale the moment it arrived, and a negative one is not a
	// length of time. There is no upper bound: a team that waits a quarter
	// has said so deliberately, and the check only ever warns.
	if after := v.cfg.Health.StaleAfter; after < 0 {
		v.add("health.stale_after is %d; it must be a number of working days above nought", after)
	}
}

// checkSecrets reports any scalar in the document that looks like a credential.
// It runs on the document rather than the decoded struct so that every string
// is seen, wherever it sits, and reported at the line it is on (spec 03 §1.1).
func checkSecrets(root *yaml.Node, path string) []Problem {
	var problems []Problem
	walkScalars(root, "", func(node *yaml.Node, at string) {
		if node.Tag != "!!str" || core.Redact(node.Value) == node.Value {
			return
		}
		problems = append(problems, Problem{
			Path:   path,
			Line:   node.Line,
			Column: node.Column,
			Message: fmt.Sprintf("%s looks like a credential; %s is committed, so put the secret in an environment variable and name the variable here",
				at, DefaultPath),
		})
	})
	return problems
}
