package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Setup checks (spec 07 §5): the twenty-odd questions doctor asks about
// whether the workflow is wired up, as opposed to whether it is being
// followed.
//
// Two of them are worth explaining, because they are what keeps this file
// short.
//
// The first is that a check about a provider's state is not a second
// implementation of the planner. `tracker.status_options` and the plan item
// `tracker.status_options` are the same question — spec 06 §1 says so, which
// is why [core.ItemTrackerStatusOptions] is both ids — so the check reads the
// plan rather than the provider. A plan that needs no change is a pass; one
// that needs a native change is a fail flow can fix; one flow is not allowed
// to make is a follow-up. Asking twice would let doctor and apply disagree
// about the same board, which is the one thing a daily check must never do.
//
// The second is that nothing here reaches for a network except the three
// checks whose whole subject is reaching for one: `auth.forge`,
// `auth.tracker` and `forge.repo`. Everything else was already looked at when
// the plan was made.

// Setup check ids that are not also desired item ids.
const (
	// CheckConfigSyntax is whether `.flow.yaml` parsed.
	CheckConfigSyntax = "config.syntax"
	// CheckConfigSemantics is whether it hangs together.
	CheckConfigSemantics = "config.semantics"
	// CheckConfigSecrets is whether it carries a credential it should not.
	CheckConfigSecrets = "config.secrets"
	// CheckLockConsistent is whether `.flow.lock` is flow's own and current.
	CheckLockConsistent = "lock.consistent"
	// CheckLocalGit is whether the working copy is the repository the
	// configuration is about.
	CheckLocalGit = "local.git"
	// CheckLocalHook is whether the commit-msg hook is installed.
	CheckLocalHook = "local.hook"
	// CheckAuthForge is whether the forge credential resolves and is allowed.
	CheckAuthForge = "auth.forge"
	// CheckAuthTracker is the same for the tracker.
	CheckAuthTracker = "auth.tracker"
	// CheckForgeRepo is whether the repository is there, and its trunk is the
	// branch the workflow is about.
	CheckForgeRepo = "forge.repo"
	// CheckDeploysSource is whether flow can read the deployment history.
	// It is the check id for [core.ItemDeploysRecords], which is named for
	// the records and reported as the source.
	CheckDeploysSource = "deploys.source"
	// CheckFollowUpsHealth is whether the tickets flow filed are being dealt
	// with.
	CheckFollowUpsHealth = "followups.health"
)

// Local is what flow saw in the working copy before doctor started.
//
// It is plain facts rather than a [git.Repo] because the checks must be
// runnable against a world a test made up: a doctor that can only be tested
// by making real repositories on disk is a doctor whose unhappy paths never
// get tested.
type Local struct {
	// Err is why flow could not read the working copy at all.
	Err error
	// Repo is the slug the origin remote points at, e.g. "acme/api", empty
	// when there is no remote or it is not a slug flow recognised.
	Repo string
	// Remote is the URL origin is, redacted, for the message.
	Remote string
	// Branch is the branch currently checked out.
	Branch string
	// HookPath is where the commit-msg hook would go.
	HookPath string
	// HookInstalled reports whether flow's own hook is there.
	HookInstalled bool
	// HookForeign reports that something else's hook is in that place, which
	// flow will not overwrite.
	HookForeign bool
	// HookManagers names the hook managers in use, if any. One of those owns
	// the hook and flow defers to it.
	HookManagers []string
}

// SetupInput is what the root found out before the run started, and what the
// setup checks are therefore not asked to find out again.
type SetupInput struct {
	// Config is the workflow, which the registry needs in order to know
	// which checks exist: one `tracker.native.<event>` per native transition.
	// The checks themselves read Env.Config, so that a test may hand the run
	// a different one.
	Config *config.Config
	// ConfigPath is where the configuration was read from, for the messages.
	ConfigPath string
	// Source is the configuration file's bytes, for the credential scan.
	Source []byte
	// ConfigErr is why it did not parse, if it did not.
	ConfigErr error
	// SemanticsErr is why it does not hang together, if it does not.
	SemanticsErr error
	// Local is the working copy.
	Local Local
	// CI reports that the run is inside a CI job, which is what makes the
	// working copy a throwaway: a runner's checkout is gone when the job
	// ends, so anything flow would put in it is gone too (spec 07 §2).
	CI bool
	// Unavailable is why a target has no adapter, keyed by target. A target
	// missing from the map and missing from Env.Adapters was simply not
	// asked for.
	Unavailable map[string]error
	// Placeholders is what became of the values the configuration does not
	// know yet (spec 07 §3). Resolution happens before the plan is made,
	// because the plan needs the answers.
	Placeholders []Answer
}

// DoctorRegistry builds every check a run of doctor may ask: the setup checks
// of spec 07 §5, then the live-health audit of §6. A registry is good for one
// run, because the live checks share one reading of the window.
func DoctorRegistry(in SetupInput) (*Registry, error) {
	return NewRegistry(append(SetupChecks(in), LiveChecks(in.Config)...)...)
}

// SetupRegistry builds the setup checks of spec 07 §5, in report order.
func SetupRegistry(in SetupInput) (*Registry, error) {
	return NewRegistry(SetupChecks(in)...)
}

// SetupChecks is the list. Order here is report order; dependencies decide run
// order, and [Registry.Order] re-sorts if the two disagree.
func SetupChecks(in SetupInput) []Check {
	checks := []Check{
		in.configSyntax(),
		in.configSemantics(),
		in.configSecrets(),
		in.lockConsistent(),
		in.localGit(),
		in.localHook(),
		in.authForge(),
		in.authTracker(),
		in.forgeRepo(),
		planned(core.ItemForgeProtection, "The trunk is protected", CategoryForge, CheckForgeRepo),
		planned(core.ItemForgeRequiredStatus, "A merge must pass the checks the workflow names", CategoryForge, CheckForgeRepo),
		planned(core.ItemForgeMergeSettings, "Only the merge strategies the workflow allows are offered", CategoryForge, CheckForgeRepo),
		planned(core.ItemForgeSquashText, "A squash commit says what the pull request said", CategoryForge, CheckForgeRepo),
		planned(core.ItemForgeFiles, "The files that make the workflow run are on the trunk", CategoryForge, CheckForgeRepo),
		in.trackerProject(),
		planned(core.ItemTrackerStatusOptions, "The board has the columns the workflow describes", CategoryTracker, core.ItemTrackerProject),
		planned(core.ItemTrackerLabels, "The labels flow's rules read exist", CategoryTracker, core.ItemTrackerProject),
	}
	for _, event := range nativeEvents(in.Config) {
		checks = append(checks, planned(
			core.NativeItem(event),
			fmt.Sprintf("The tracker moves a ticket itself on %s", event),
			CategoryTracker, core.ItemTrackerProject,
		))
	}
	checks = append(checks,
		planned(core.ItemTrackerWIP, "The work-in-progress limits are on the board", CategoryTracker, core.ItemTrackerProject),
		planned(core.ItemTrackerExpedite, "Expedited work is visible", CategoryTracker, core.ItemTrackerProject),
		in.deploysSource(),
		in.followUpsHealth(),
	)
	return checks
}

// nativeEvents is the transitions the configuration gives the tracker's own
// automation, in the order spec 04 §5 lists the events, so that two runs of
// the same configuration register the same checks in the same order.
func nativeEvents(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var events []string
	for event, transition := range cfg.Workflow.Transitions {
		if transition.Owner == config.OwnerNative {
			events = append(events, event)
		}
	}
	slices.Sort(events)
	return events
}

// path is the configuration's name in a message.
func (in SetupInput) path() string {
	if in.ConfigPath == "" {
		return ".flow.yaml"
	}
	return in.ConfigPath
}

// configSyntax is the check every other one depends on, directly or through
// another: there is nothing to say about a workflow nobody can read.
func (in SetupInput) configSyntax() Check {
	return Check{
		ID:       CheckConfigSyntax,
		Title:    "The configuration parses",
		Category: CategoryConfig,
		Run: func(context.Context, *Env) Result {
			if in.ConfigErr != nil {
				return Fail("%s could not be read", in.path()).
					Saw(core.Redact(in.ConfigErr.Error()), "").
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "fix the file, or start again from a preset",
						Steps:   []string{"flow schema > /tmp/flow.schema.json, and point your editor at it", "or: flow init --force, which rewrites " + in.path()},
					})
			}
			return Pass("%s parses", in.path())
		},
	}
}

// configSemantics is the rest of spec 03 §4: the rules a schema cannot state,
// like a transition naming a column that exists.
func (in SetupInput) configSemantics() Check {
	return Check{
		ID:        CheckConfigSemantics,
		Title:     "The configuration hangs together",
		Category:  CategoryConfig,
		DependsOn: []string{CheckConfigSyntax},
		Run: func(_ context.Context, env *Env) Result {
			if in.SemanticsErr != nil {
				return Fail("%s does not hang together", in.path()).
					Saw(core.Redact(in.SemanticsErr.Error()), "").
					Fix(Remediation{Kind: RemedyManual, Summary: "fix the file"})
			}
			if env.Config == nil {
				return Fail("there is no configuration to check")
			}
			return Pass("every column, transition and gate the configuration names exists")
		},
	}
}

// configSecrets is spec 15: a credential belongs in the environment or behind
// a command, never in a file that is committed.
//
// The scan is [core.Redact] rather than a second list of token shapes. That
// is deliberate: the shapes flow knows how to keep out of a log are exactly
// the shapes it should refuse to find in a file, and one list cannot drift
// from the other if there is only one.
func (in SetupInput) configSecrets() Check {
	return Check{
		ID:        CheckConfigSecrets,
		Title:     "The configuration carries no credential",
		Category:  CategoryConfig,
		DependsOn: []string{CheckConfigSyntax},
		Run: func(context.Context, *Env) Result {
			if len(in.Source) == 0 {
				return Skip("flow did not read the file itself")
			}
			source := string(in.Source)
			if redacted := core.Redact(source); redacted != source {
				return Fail("%s contains something shaped like a credential", in.path()).
					Saw("found on "+joinWords(secretLines(source, redacted)), "").
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "move it out of the file and revoke it",
						Steps: []string{
							"revoke the credential: it is in the history of a file that is committed",
							"put the new one in an environment variable, and name that variable in forge.auth.token_env or tracker.auth.token_env",
							"or put a token_cmd in your personal configuration, which is not committed",
						},
					})
			}
			return Pass("%s names where to find its credentials and holds none", in.path())
		},
	}
}

// secretLines is which lines changed under redaction, so that the refusal can
// say where to look without printing what it found.
func secretLines(source, redacted string) []string {
	before, after := strings.Split(source, "\n"), strings.Split(redacted, "\n")
	var lines []string
	for i := range before {
		if i < len(after) && before[i] != after[i] {
			lines = append(lines, fmt.Sprintf("line %d", i+1))
		}
	}
	return lines
}

// lockConsistent is spec 03 §3. The lock is flow's memory, and a memory
// written by a newer flow is one this build must not edit.
//
// A configuration that has changed since the lock was written is not a
// failure. It is the ordinary result of editing the workflow, and all it
// means is that an attestation taken against the whole file has lapsed —
// which is [FindAttestation]'s business, and is said there, on the check the
// attestation was about.
func (in SetupInput) lockConsistent() Check {
	return Check{
		ID:        CheckLockConsistent,
		Title:     "The lock is flow's own and readable",
		Category:  CategoryConfig,
		DependsOn: []string{CheckConfigSyntax},
		Run: func(_ context.Context, env *Env) Result {
			if env.Lock == nil {
				return Skip("there is no lock yet: flow has not learned anything to write down")
			}
			if env.Lock.Version > config.LockVersion {
				return Fail("the lock is version %d and this flow writes version %d", env.Lock.Version, config.LockVersion).
					Saw("a newer flow wrote it, and editing it with this one would lose what that flow knew", "").
					Fix(Remediation{Kind: RemedyManual, Summary: "upgrade flow to at least the version that wrote the lock"})
			}
			result := Pass("the lock is version %d, with %s", env.Lock.Version, lockHolds(env.Lock))
			if hash := env.Plan.ConfigHash; hash != "" && env.Lock.ConfigHash != "" && hash != env.Lock.ConfigHash {
				result = result.Saw("the configuration has changed since the lock was written, so an attestation taken against the whole of it has lapsed", "")
			}
			return result
		},
	}
}

// lockHolds describes what is in the lock, for the passing line.
func lockHolds(lock *config.Lock) string {
	var parts []string
	if n := len(lock.Resolved); n > 0 {
		parts = append(parts, fmt.Sprintf("%d resolved %s", n, plural(n, "value")))
	}
	if n := len(lock.Observed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "observation")))
	}
	if n := len(lock.Attestations); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "attestation")))
	}
	if n := len(lock.FollowUps); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "follow-up")))
	}
	if len(parts) == 0 {
		return "nothing recorded yet"
	}
	return joinWords(parts)
}

// localGit is the check that catches the commonest confusion of all: running
// flow in the wrong working copy. Everything else flow says would be about
// another repository, and would be wrong without looking wrong.
func (in SetupInput) localGit() Check {
	return Check{
		ID:        CheckLocalGit,
		Title:     "The working copy is the repository the configuration is about",
		Category:  CategoryLocal,
		DependsOn: []string{CheckConfigSemantics},
		Run: func(_ context.Context, env *Env) Result {
			if in.Local.Err != nil {
				return Fail("flow could not read the working copy").
					Saw(core.Redact(in.Local.Err.Error()), "").
					Fix(Remediation{Kind: RemedyManual, Summary: "run flow inside a git repository, or pass -C"})
			}
			want := env.Config.Forge.Repo
			switch {
			case in.Local.Repo == "":
				return Fail("this working copy has no origin flow recognised, so it cannot tell whether it is %s", want).
					Saw("origin is "+core.Redact(in.Local.Remote), "").
					Fix(Remediation{Kind: RemedyManual, Summary: "point origin at " + want})
			case !strings.EqualFold(in.Local.Repo, want):
				return Fail("this working copy is %s and the configuration is about %s", in.Local.Repo, want).
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "run flow in the right repository, or correct forge.repo",
						Steps:   []string{"cd to a clone of " + want, "or: edit forge.repo in " + in.path() + " to " + in.Local.Repo},
					})
			}
			return Pass("this working copy is %s", in.Local.Repo)
		},
	}
}

// localHook is the one check whose subject is a convenience. The hook catches
// a bad commit message before it is written rather than after it is pushed,
// and the only thing that makes it worth a check is that an uninstalled hook
// is silent: nothing goes wrong until CI says so.
//
// Which is also why CI is the one place the check has nothing to say. The
// hook is a thing in a working copy, a runner's working copy lasts as long as
// the job, and nobody commits from it — so a scheduled audit that reported a
// missing hook every morning would be reporting the weather.
func (in SetupInput) localHook() Check {
	return Check{
		ID:        CheckLocalHook,
		Title:     "The commit-msg hook is installed",
		Category:  CategoryLocal,
		DependsOn: []string{CheckLocalGit},
		Run: func(_ context.Context, env *Env) Result {
			if env.Config.Commits.Convention == "" || env.Config.Commits.Convention == "none" {
				return Skip("this workflow does not lint commit messages")
			}
			if in.CI {
				return Skip("a CI checkout is thrown away when the job ends, and nobody commits from one")
			}
			switch {
			case len(in.Local.HookManagers) > 0:
				return Skip("%s manages this repository's hooks", joinWords(in.Local.HookManagers))
			case in.Local.HookForeign:
				return Fail("something else's hook is at %s", in.Local.HookPath).
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "call flow from the hook that is already there",
						Steps:   []string{`add: exec flow check commit "$1"`},
					})
			case !in.Local.HookInstalled:
				return Fail("the commit-msg hook is not installed, so a bad message is only caught in CI").
					Fix(Remediation{Kind: RemedyAuto, Summary: "install it", Command: "flow hook install"})
			}
			return Pass("the commit-msg hook is at %s", in.Local.HookPath)
		},
	}
}

// authForge and authTracker are the same question of two providers, and they
// are two checks rather than one because the answers are independently
// useful: a forge token that works and a tracker token that does not is a
// specific, common and fixable situation.
func (in SetupInput) authForge() Check {
	return Check{
		ID:        CheckAuthForge,
		Title:     "The forge credential works",
		Category:  CategoryAuth,
		DependsOn: []string{CheckConfigSemantics},
		Run: func(ctx context.Context, env *Env) Result {
			return in.probe(ctx, TargetForge, env.Adapters.Forge, env.Config.Forge.Type, env.Config.Forge.Auth)
		},
	}
}

func (in SetupInput) authTracker() Check {
	return Check{
		ID:        CheckAuthTracker,
		Title:     "The tracker credential works",
		Category:  CategoryAuth,
		DependsOn: []string{CheckConfigSemantics},
		Run: func(ctx context.Context, env *Env) Result {
			return in.probe(ctx, TargetTracker, env.Adapters.Tracker, env.Config.Tracker.Type, env.Config.Tracker.Auth)
		},
	}
}

// prober is the part of an adapter an auth check uses. Taking the narrow
// interface is what lets the two checks be one function over a Forge and a
// Tracker, which have nothing else in common.
type prober interface {
	Kind() string
	Probe(ctx context.Context) (core.Permissions, error)
	Capabilities() core.CapabilitySet
}

// probe resolves a credential and reports what it turned out to allow.
//
// A credential that resolves but allows nothing is still a pass. What a token
// is allowed to do is the planner's business, item by item — the point of
// capability resolution (spec 05 §4) is that a missing permission becomes a
// follow-up on the one thing it blocks, not a red mark against the whole
// provider.
func (in SetupInput) probe(ctx context.Context, target string, adapter prober, kind string, auth config.Auth) Result {
	if err := in.Unavailable[target]; err != nil {
		return in.noCredential(target, err, auth)
	}
	if adapter == nil {
		return in.noCredential(target, nil, auth)
	}
	permissions, err := adapter.Probe(ctx)
	if err != nil {
		return Failed(fmt.Errorf("the %s credential was refused by %s: %w", target, kind, err)).
			Fix(Remediation{
				Kind:    RemedyInput,
				Summary: "check the credential is current and has not been revoked",
				Steps:   credentialSteps(auth),
			})
	}
	result := Pass("the %s credential is accepted by %s and holds %s", target, kind, permissions)
	for _, evidence := range permissions.Evidence {
		result = result.Saw(core.Redact(evidence), "")
	}
	return result.Saw("can act as "+strings.Join(Roles(target, adapter.Capabilities(), permissions), ", ")+" (spec 15 §3.1)", "")
}

// Credential roles (spec 15 §3.1, spec 20 §5).
const (
	RoleReader = "reader"
	RoleGate   = "gate"
	RoleActor  = "actor"
)

// roleNeeds is which declared capabilities a credential must be allowed for
// it to fill a role on each side. Reading them from the declaration rather
// than from scopes is what keeps roles one vocabulary across providers whose
// scopes have nothing in common.
var roleNeeds = map[string]map[string][]string{
	TargetForge: {
		RoleGate:  {core.CapCommitStatusWrite, core.CapPRComments},
		RoleActor: {core.CapAutoMerge},
	},
	TargetTracker: {
		RoleActor: {core.CapTicketMove, core.CapFollowUpTickets},
	},
}

// Roles is the roles a credential that was accepted can fill, given what the
// adapter declares each capability requires. A forge actor that may also
// change protection says so, because settings are the one act that needs an
// administrator.
func Roles(target string, declared core.CapabilitySet, permissions core.Permissions) []string {
	allowed := func(names ...string) bool {
		for _, name := range names {
			capability := declared.Capability(name)
			if !capability.Set || len(permissions.Missing(capability.Requires)) > 0 {
				return false
			}
		}
		return true
	}
	roles := []string{RoleReader}
	for _, role := range []string{RoleGate, RoleActor} {
		needs, asked := roleNeeds[target][role]
		if !asked || !allowed(needs...) {
			continue
		}
		if role == RoleActor && target == TargetForge && allowed(core.CapBranchProtection) {
			role = RoleActor + " (and settings)"
		}
		roles = append(roles, role)
	}
	return roles
}

// noCredential is the refusal when there is nothing to probe with.
func (in SetupInput) noCredential(target string, err error, auth config.Auth) Result {
	result := Fail("flow has no %s credential", target)
	if err != nil {
		result = result.Saw(core.Redact(err.Error()), "")
	}
	return result.Fix(Remediation{
		Kind:    RemedyInput,
		Summary: "give flow a credential for the " + target,
		Steps:   credentialSteps(auth),
	})
}

// credentialSteps says where flow looks, which is the only part of this a
// person cannot guess.
func credentialSteps(auth config.Auth) []string {
	steps := []string{"put a token in your personal configuration as a token_cmd, which keeps it off disk"}
	if auth.TokenEnv != "" {
		steps = append([]string{"export " + auth.TokenEnv + "=<token>"}, steps...)
	}
	return steps
}

// forgeRepo is reachability and the trunk in one check.
//
// They are one because they fail together and are fixed together: a slug that
// is wrong, a repository that was renamed and a credential that cannot see it
// all produce the same reply, and reporting "the repository is not there" and
// "its default branch is not what the workflow expects" as two red lines
// would describe one problem twice.
func (in SetupInput) forgeRepo() Check {
	// Bound once: the title names the work in the follow-up this check
	// can raise, so a copy of it beside the Run closure would be a second
	// answer to the same question.
	title := "The repository is there, and its trunk is the workflow's"
	return Check{
		ID:        CheckForgeRepo,
		Title:     title,
		Category:  CategoryForge,
		DependsOn: []string{CheckAuthForge, CheckLocalGit},
		Run: func(ctx context.Context, env *Env) Result {
			if env.Adapters.Forge == nil {
				return Failed(fmt.Errorf("there is no forge adapter to ask"))
			}
			info, err := env.Adapters.Forge.Repo(ctx)
			if err != nil {
				return Failed(fmt.Errorf("%s could not be read: %w", env.Config.Forge.Repo, err)).
					Fix(Remediation{
						Kind:    RemedyManual,
						Summary: "check forge.repo names a repository the credential can see",
					})
			}
			result := plannedResult(env, core.ItemForgeDefaultBranch, title)
			if result.State == StateSkip {
				// Nothing in the configuration asks for a particular trunk,
				// so reachability was the whole question.
				return Pass("%s is there, and its default branch is %s", env.Config.Forge.Repo, info.DefaultBranch).
					Saw("read as "+env.Adapters.Forge.Kind(), "")
			}
			return result.Saw(fmt.Sprintf("%s's default branch is %s", env.Config.Forge.Repo, info.DefaultBranch), "")
		},
	}
}

// trackerProject is the board, and is where a placeholder is reported.
//
// A board flow has not been told which one it is is not broken: it is
// unanswered, which is [StateInput], and `--fix` is what resolves it
// (spec 07 §3). Every other tracker check depends on this one, so an
// unanswered board blocks them rather than failing them twenty times over.
func (in SetupInput) trackerProject() Check {
	// Bound once: the title names the work in the follow-up this check
	// can raise, so a copy of it beside the Run closure would be a second
	// answer to the same question.
	title := "The board is identified"
	return Check{
		ID:        core.ItemTrackerProject,
		Title:     title,
		Category:  CategoryTracker,
		DependsOn: []string{CheckAuthTracker},
		Run: func(_ context.Context, env *Env) Result {
			for _, answer := range in.Placeholders {
				if answer.Placeholder.Path != core.ItemTrackerProject {
					continue
				}
				if !answer.Resolved() {
					return answer.Result()
				}
				return plannedResult(env, core.ItemTrackerProject, title).
					Saw(fmt.Sprintf("%s was %s this run", answer.Placeholder.Path, answer.How), "")
			}
			return plannedResult(env, core.ItemTrackerProject, title)
		},
	}
}

// deploysSource is skipped by most teams, because most teams record no
// deployments at all and never ask flow to read one.
//
// What switches it on is `deploys.source`, not `done_on`: a team that finishes
// work on the merge still holds itself to `max_lag` (spec 13 §2), and that
// audit reads the same records this check proves are readable. Gating it on
// `done_on: deployed` would skip the readability check for exactly the
// repositories whose live-health audit depends on it, flow's own among them.
func (in SetupInput) deploysSource() Check {
	// Bound once: the title names the work in the follow-up this check
	// can raise, so a copy of it beside the Run closure would be a second
	// answer to the same question.
	title := "Flow can read the deployment history"
	return Check{
		ID:        CheckDeploysSource,
		Title:     title,
		Category:  CategoryDeploys,
		DependsOn: []string{CheckAuthForge},
		Run: func(_ context.Context, env *Env) Result {
			if reads, why := ReadsDeployments(env.Config); !reads {
				return Skip("%s", why)
			}
			return plannedResult(env, core.ItemDeploysRecords, title)
		},
	}
}

// followUpsHealth is the check about flow's own asking. A follow-up is how
// flow admits it cannot do something, and a follow-up nobody acts on is a
// thing silently not happening — which is exactly what doctor exists to stop.
func (in SetupInput) followUpsHealth() Check {
	return Check{
		ID:        CheckFollowUpsHealth,
		Title:     "The work flow asked others to do is moving",
		Category:  CategoryFollowUps,
		DependsOn: []string{CheckConfigSemantics},
		Run: func(_ context.Context, env *Env) Result {
			result := in.pendingOwners(env)
			open := openFollowUps(env.Lock)
			if len(open) == 0 {
				return result
			}
			oldest, age := oldestFollowUp(open, env.Now())
			result.Message = fmt.Sprintf("%s, and %d %s open", result.Message,
				len(open), plural(len(open), "follow-up"))
			for _, followUp := range open {
				result = result.Saw(fmt.Sprintf("%s: %s", followUp.Check, followUp.Ticket), "")
			}
			if age >= StaleFollowUp {
				result.State = StateExternal
				result.Message = fmt.Sprintf("%s — %s has been open %s",
					result.Message, oldest.Ticket, config.Duration(age.Truncate(time.Hour)))
				result.Remediation = Remediation{
					Kind:    RemedyFollowUp,
					Summary: "somebody with the access needs to do the work, or the workflow should stop asking for it",
				}
			}
			return result
		},
	}
}

// StaleFollowUp is how long a follow-up may stay open before doctor stops
// calling it healthy. A month is long enough that a team on holiday does not
// come back to a red check, and short enough that a ticket nobody intends to
// do is noticed in the quarter it was filed.
const StaleFollowUp = 30 * 24 * time.Hour

// pendingOwners is the other half of this check: a follow-up flow cannot
// route is one that will sit unassigned, so the owners are checked whether or
// not there is anything open yet.
func (in SetupInput) pendingOwners(env *Env) Result {
	var pending []string
	for _, answer := range in.Placeholders {
		if !answer.Resolved() && strings.HasPrefix(answer.Placeholder.Path, "followups.owners.") {
			pending = append(pending, answer.Placeholder.Path)
		}
	}
	if len(pending) > 0 {
		return Fail("flow does not know who to give a follow-up to: %s", joinWords(pending)).
			Fix(Remediation{
				Kind:    RemedyInput,
				Summary: "say who holds the access",
				Command: "flow doctor --fix --set " + pending[0] + "=<handle>",
			})
	}
	if env.Lock == nil {
		return Pass("nothing has been asked of anybody")
	}
	return Pass("nothing is waiting on anybody")
}

// openFollowUps is the ones still outstanding, in the order they were filed.
func openFollowUps(lock *config.Lock) []config.FollowUp {
	if lock == nil {
		return nil
	}
	var open []config.FollowUp
	for _, followUp := range lock.FollowUps {
		if followUp.State == FollowUpVerified || followUp.State == FollowUpWithdrawn {
			continue
		}
		open = append(open, followUp)
	}
	slices.SortStableFunc(open, func(a, b config.FollowUp) int { return a.OpenedAt.Compare(b.OpenedAt) })
	return open
}

// oldestFollowUp is the first one filed and how long ago that was.
func oldestFollowUp(open []config.FollowUp, now time.Time) (config.FollowUp, time.Duration) {
	oldest := open[0]
	if now.IsZero() || oldest.OpenedAt.IsZero() {
		return oldest, 0
	}
	return oldest, now.Sub(oldest.OpenedAt)
}

// planned is a check whose whole question is "does the plan want to change
// this", which is most of them.
func planned(id, title, category string, dependsOn ...string) Check {
	return Check{
		ID:        id,
		Title:     title,
		Category:  category,
		DependsOn: dependsOn,
		Run:       func(_ context.Context, env *Env) Result { return plannedResult(env, id, title) },
	}
}

// plannedResult turns one plan item into one check result, and then holds it
// up against flow's memory: a human's word may settle it (spec 07 §8), and
// something that used to hold and no longer does is drift rather than a plain
// failure (spec 07 §7).
func plannedResult(env *Env, id, title string) Result {
	if env.PlanErr != nil {
		// Not a skip. A check that does not apply to this configuration and
		// a check flow was unable to make up its mind about are opposite
		// answers, and only the first of them may let a run report success.
		return Failed(env.PlanErr)
	}
	item, found := env.Plan.Find(id)
	if !found {
		return Skip("this configuration does not ask for it")
	}
	result := outcomeResult(item, title)
	result.ObservedHash = item.ObservedHash

	// A refuted item needs no guard here: [AttestInput.Wants] already holds
	// that a word settles only what flow cannot verify, and the refusal
	// above has made this one a thing flow verified (spec 07 §8).
	result = Attest(AttestInput{
		Check:      Check{ID: id, Title: strings.TrimSpace(result.Message)},
		Result:     result,
		Lock:       env.Lock,
		Inputs:     item.DesiredHash,
		ConfigHash: env.Plan.ConfigHash,
		Now:        env.Now(),
		TTL:        AttestationTTL(env.Config),
	})
	return Drift(DriftInput{
		Check:  id,
		Result: result,
		Lock:   env.Lock,
		Item:   item,
		Now:    env.Now(),
		Record: env.Fix,
	})
}

// followUpSummary is the line the ticket is titled after (spec 07 §4).
//
// It has to name the work. Every item flow cannot perform used to summarise
// as "somebody with the access has to do it", which made every such ticket in
// a repository identical in the issue list — two of them on flow's own repo,
// and no way to tell from the title which was the trunk's protection and
// which was the status check a merge must pass.
func followUpSummary(item PlanItem, title string) string {
	what := strings.TrimSpace(title)
	if what == "" {
		what = item.ID
	}
	if len(item.Missing) == 0 {
		return what + " — somebody else has to do it"
	}
	return fmt.Sprintf("%s — needs %s", what, joinWords(item.Missing))
}

// followUpSteps is the "exact steps" spec 07 §4 asks a follow-up to carry.
//
// The steps are the provider's own, from its declaration (spec 05 §1). Flow
// knows what it wanted and which permission it lacked, and it does not know
// what kind of thing a `deploys.record` is to the person reading the ticket,
// or which page it is on, or whether there is a page at all. Only the
// research behind the adapter knows that, and a ticket written without it
// says `create deploys.records "production"` at somebody who has no reason
// to have ever met either word.
//
// What flow adds is the two things the declaration cannot know: that this
// particular credential was short of this particular permission, and what
// the provider volunteered about this particular repository.
func followUpSteps(item PlanItem) []string {
	steps := make([]string, 0, len(item.Recipe.Steps)+len(item.Notes)+1)
	// What the provider volunteered comes first. A mechanism that is already
	// written and merely switched off is one click, and burying it under the
	// change flow would have made from scratch is burying the cheap answer
	// under the expensive one.
	steps = append(steps, noteSteps(item)...)
	if len(item.Missing) > 0 {
		steps = append(steps, fmt.Sprintf(
			"Give flow a %s credential with %s and it will do this itself; otherwise somebody who has it can follow the steps below.",
			item.Target, joinWords(item.Missing)))
	}
	steps = append(steps, item.Recipe.Steps...)
	// Flow's own words last, and only once. The reader has just been told
	// what to do in the provider's vocabulary; this is the same thing said
	// exactly, for somebody who knows flow's, and it is the only place in
	// the ticket it appears — spelling it out again under "what flow saw"
	// turned the shortest tickets into the same sentence twice.
	//
	// It is labelled as flow's vocabulary only where there is another
	// vocabulary to tell it apart from. An item that names no capability
	// has no declaration behind it and so no recipe, and announcing a
	// translation of the only sentence in the ticket is worse than the
	// sentence.
	for _, change := range item.Changes {
		instruction := changeInstruction(change)
		if len(item.Recipe.Steps) > 0 {
			instruction = "In flow's own words: " + instruction
		}
		steps = append(steps, instruction)
	}
	return steps
}

// refuted reports whether the provider said outright that the item is not
// true. One such note is enough; they are facts, not votes.
func refuted(item PlanItem) bool {
	for _, note := range item.Notes {
		if note.Refutes {
			return true
		}
	}
	return false
}

// withNotes adds what the provider volunteered to a finding's evidence.
//
// Only a finding: a note explains why something is not true, and a note on a
// check that passes is a fact about a mechanism nobody is relying on. The
// dormant ruleset on a protected trunk is exactly that.
func withNotes(result Result, item PlanItem) Result {
	for _, note := range item.Notes {
		result = result.Saw(note.Text, note.URL)
	}
	return result
}

// noteSteps are the steps the provider's own notes imply, in the order the
// provider gave them.
func noteSteps(item PlanItem) []string {
	var steps []string
	for _, note := range item.Notes {
		if note.Fix == "" {
			continue
		}
		step := note.Fix
		if note.URL != "" {
			step += " — " + note.URL
		}
		steps = append(steps, step)
	}
	return steps
}

// changeInstruction is one change written out with its values. The report's
// one-line form is deliberately terse; a person about to reproduce the change
// by hand needs the values, not a summary of them.
//
// What it does not need is the settings that already say what flow asked.
// [core.Change.Fields] is what decides that, for this and for the plan
// together: a follow-up ticket is a copy of a plan line that outlives the run
// it came from, and the two reading the same change differently is how
// somebody ends up checking four settings to find the fifth.
func changeInstruction(change core.Change) string {
	names := change.Fields()
	line := describeChange(change)
	if len(names) == 0 {
		return line
	}
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+change.Item.Fields[name])
	}
	return line + ", with " + strings.Join(pairs, ", ")
}

// outcomeResult is the mapping from what apply would do to what doctor says
// (spec 05 §4, spec 07 §2).
//
// Two of the six are worth stating. A compensated item passes, because the
// intent is enforced — by something other than what the configuration named,
// which the note says. An unsupported item is a skip rather than a failure:
// the provider cannot do it, nobody can make it, and a red mark every morning
// for a thing with no remedy is how a team learns to stop reading the report.
// The line still says so, so `--format json` carries it either way.
func outcomeResult(item PlanItem, title string) Result {
	switch item.Outcome {
	case OutcomeNoop:
		return Pass("%s", item.Why)

	case OutcomeCompensated:
		result := Pass("%s", item.Why)
		if item.Mechanism != "" {
			result = result.Saw("enforced by "+item.Mechanism+" instead of what the configuration named", "")
		}
		return result

	case OutcomeNative:
		result := withNotes(Fail("%s", item.Why), item)
		for _, change := range item.Changes {
			result = result.Saw(describeChange(change), "")
		}
		return result.Fix(Remediation{
			Kind:    RemedyAuto,
			Summary: "make the change",
			Command: "flow apply",
		})

	case OutcomeFollowUp:
		result := withNotes(Fail("%s", item.Why), item)
		if len(item.Missing) > 0 {
			result = result.Saw("the credential is missing "+joinWords(item.Missing), "")
		}
		// The changes are deliberately not repeated here. A native outcome
		// lists them as evidence because its remedy is a command and says
		// nothing about what the command will do; a follow-up's remedy is
		// the steps, and the last of those is the change written out in
		// full. Saying it in both places made the shortest ticket flow files
		// — one change, no fields — read as the same line under two
		// headings.
		return result.Fix(Remediation{
			Kind:    RemedyFollowUp,
			Summary: followUpSummary(item, title),
			Subject: item.Recipe.What,
			Venue:   item.Recipe.Where,
			Steps:   followUpSteps(item),
		})

	case OutcomeAttest:
		// A provider that has already answered no is not asking for a
		// word. Flow cannot make this change either, so the remedy is a
		// person's — but it is the switch, not the signature.
		if refuted(item) {
			return withNotes(Fail("%s", item.Why), item).Fix(Remediation{
				Kind:    RemedyManual,
				Summary: "switch it on; vouching for it will not make it true",
				Steps:   noteSteps(item),
			})
		}
		return Fail("%s", item.Why).Fix(Remediation{
			Kind:    RemedyAttest,
			Summary: "confirm it by hand, if it is true",
			Command: "flow attest " + item.ID,
		})

	case OutcomeUnsupported:
		return Skip("%s", item.Why)
	}
	return Failed(fmt.Errorf("%s planned an outcome flow does not know: %q", item.ID, item.Outcome))
}
