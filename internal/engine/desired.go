package engine

import (
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Targets a desired item belongs to, which is what `flow plan --target`
// selects on (spec 02 §2).
const (
	// TargetTracker is the ticket system.
	TargetTracker = "tracker"
	// TargetForge is the Git host.
	TargetForge = "forge"
	// TargetDeploys is the source of deployment records.
	TargetDeploys = "deploys"
)

// Desired item ids (spec 06 §1). They are also the doctor check ids (spec 07
// §5) and the keys of the lock's `observed:` map (spec 03 §3), because they
// name the same things: one vocabulary for one subject.
const (
	// ItemTrackerProject is the board itself.
	ItemTrackerProject = "tracker.project"
	// ItemTrackerStatusOptions is the set of columns on it.
	ItemTrackerStatusOptions = "tracker.status_options"
	// ItemTrackerLabels is the labels flow's rules read.
	ItemTrackerLabels = "tracker.labels"
	// ItemTrackerWIP is the work-in-progress limits.
	ItemTrackerWIP = "tracker.wip"
	// ItemTrackerExpedite is the view that shows expedited work.
	ItemTrackerExpedite = "tracker.expedite"
	// ItemForgeDefaultBranch is the trunk.
	ItemForgeDefaultBranch = "forge.default_branch"
	// ItemForgeProtection is the protection on it.
	ItemForgeProtection = "forge.protection"
	// ItemForgeRequiredStatus is the checks a merge must pass.
	ItemForgeRequiredStatus = "forge.required_status"
	// ItemForgeMergeSettings is how a pull request is allowed to land.
	ItemForgeMergeSettings = "forge.merge_settings"
	// ItemForgeFiles is the files flow proposes to the repository.
	ItemForgeFiles = "forge.files"
	// ItemDeploysRecords is the deployment history flow reads.
	ItemDeploysRecords = "deploys.records"
)

// NativeItemPrefix begins the id of the item for one transition the tracker's
// own automation is to perform: tracker.native.pr_merged.
const NativeItemPrefix = "tracker.native."

// NativeItem returns the desired item id for a native transition on event.
func NativeItem(event string) string { return NativeItemPrefix + event }

// Fields a desired item's contents carry. A field is only ever something the
// provider can report back, so that a comparison means something.
const (
	// FieldPosition is a column's place in board order.
	FieldPosition = "position"
	// FieldTo is the column a transition lands in.
	FieldTo = "to"
	// FieldLimit and FieldPerPerson are a column's work-in-progress limits.
	FieldLimit     = "limit"
	FieldPerPerson = "per_person"
	// FieldLabels is the labels a view filters on.
	FieldLabels = "labels"
	// FieldRuns is the flow commands a workflow file invokes.
	FieldRuns = "runs"
)

// Protection fields, named after [core.Protection]'s own members so that an
// adapter's observation is a transcription rather than a translation.
const (
	FieldEnabled              = "enabled"
	FieldAllowForcePush       = "allow_force_push"
	FieldAllowDeletions       = "allow_deletions"
	FieldRequireLinearHistory = "require_linear_history"
)

// Merge settings fields, named after [core.MergeSettings]'s members.
const (
	FieldAllowSquash   = "allow_squash"
	FieldAllowMerge    = "allow_merge"
	FieldAllowRebase   = "allow_rebase"
	FieldSquashTitle   = "squash_title"
	FieldSquashMessage = "squash_message"
)

// Singleton item names, for the things a provider has exactly one of.
const (
	// NameProtection is the one protection item on the trunk.
	NameProtection = "protection"
	// NameMerge is the one merge-settings item on the repository.
	NameMerge = "merge"
	// NameExpedite is the view expedited work appears in.
	NameExpedite = "Expedite"
)

// ReadyLabels are the labels flow's Definition of Ready rules read, whose
// names spec 04 §7 fixes. They are wanted whether or not the rules that read
// them are switched on: a team cannot start labelling work `user-facing`
// before the label exists, and so could never switch the rule on.
var ReadyLabels = []string{"ui", "user-facing"}

// proposedFiles are the files flow proposes to a GitHub repository: the
// template that holds the content, the path it is proposed at, and whether it
// is the workflow that has to run flow's commands (spec 02 §2, spec 17 §2).
var proposedFiles = []struct {
	Template string
	Path     string
	Workflow bool
}{
	{Template: "ci/github-flow.yml", Path: ".github/workflows/flow.yml", Workflow: true},
	{Template: "pull_request_template.md", Path: ".github/pull_request_template.md"},
	{Template: "issue-forms/story.yml", Path: ".github/ISSUE_TEMPLATE/story.yml"},
}

// DesiredItem is one thing flow wants to be true about a provider: which
// capability it needs to make it so, what has to be settled first, and the
// contents the provider is compared against (spec 06 §1).
type DesiredItem struct {
	// ID names the item, e.g. tracker.status_options.
	ID string
	// Capability is the registry name of what making it true requires, empty
	// for an item that only has to be found.
	Capability string
	// Target is tracker, forge or deploys.
	Target string
	// DependsOn are the ids that must be settled before this one can be. An
	// item never appears in [Desired.Items] before something it depends on.
	DependsOn []string
	// Resolve marks an item whose work is finding an identifier rather than
	// changing anything: which board flow is to use, which branch is the
	// trunk. It has no contents — what matters is that the thing is there.
	Resolve bool
	// Additive marks an item the provider may hold more of than flow asked
	// for without that being a difference: a label flow does not know about
	// is somebody else's label, not drift (spec 06 §4).
	Additive bool
	// Items are the contents flow wants, in canonical order. A field is only
	// ever something the provider can report back.
	Items []core.Item
	// Files are the file contents behind the items, for the proposal apply
	// makes. Only [ItemForgeFiles] has any.
	Files []core.ProposedFile
}

// Desired is the workflow a team's configuration asks for, in the only terms
// a provider can be compared against (spec 06 §1). It is a pure function of
// the configuration and the shipped templates: nothing here looks at a
// network, a clock or a lock.
type Desired struct {
	// Items are the desired items, in dependency order.
	Items []DesiredItem
}

// NewDesired derives the desired state from a configuration. templates holds
// the file templates flow ships, which the root supplies from assets.
func NewDesired(cfg *config.Config, templates fs.FS) (Desired, error) {
	if cfg == nil {
		return Desired{}, fmt.Errorf("desired state: no configuration was supplied")
	}
	var desired Desired

	desired.add(trackerProject())
	desired.add(trackerStatusOptions(cfg))
	desired.add(trackerLabels(cfg))
	for _, item := range trackerNative(cfg) {
		desired.add(item)
	}
	desired.add(trackerWIP(cfg))
	desired.add(trackerExpedite(cfg))

	desired.add(forgeDefaultBranch())
	desired.add(forgeProtection(cfg))
	desired.add(forgeRequiredStatus(cfg))
	desired.add(forgeMergeSettings(cfg))
	files, err := forgeFiles(cfg, templates)
	if err != nil {
		return Desired{}, err
	}
	desired.add(files)

	desired.add(deploysRecords(cfg))
	return desired, nil
}

// add appends an item unless it is empty. An item with nothing to say — no
// contents and nothing to resolve — is one the configuration did not ask for,
// such as the expedite view of a team that does not expedite.
func (d *Desired) add(item DesiredItem) {
	if item.ID == "" || (!item.Resolve && len(item.Items) == 0) {
		return
	}
	item.Items = canonicalItems(item.Items)
	d.Items = append(d.Items, item)
}

// Find returns the item with that id, and whether it is wanted at all.
func (d Desired) Find(id string) (DesiredItem, bool) {
	for _, item := range d.Items {
		if item.ID == id {
			return item, true
		}
	}
	return DesiredItem{}, false
}

// IDs returns the ids of every desired item, in dependency order.
func (d Desired) IDs() []string {
	ids := make([]string, 0, len(d.Items))
	for _, item := range d.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// Target returns the items belonging to one target.
func (d Desired) Target(target string) []DesiredItem {
	var items []DesiredItem
	for _, item := range d.Items {
		if item.Target == target {
			items = append(items, item)
		}
	}
	return items
}

// trackerProject is the board itself: an id to find, not a thing to change.
// Which board that is may still be a placeholder, which doctor resolves into
// the lock rather than into `.flow.yaml` (spec 07 §3).
func trackerProject() DesiredItem {
	return DesiredItem{
		ID:         ItemTrackerProject,
		Capability: core.CapProject,
		Target:     TargetTracker,
		Resolve:    true,
	}
}

// trackerStatusOptions is one option per configured column.
//
// The option carries its place in board order and nothing else: a column's
// category is flow's own mapping onto todo/in_progress/done (spec 04 §2) and
// no tracker researched can report it, and a column's configured id is what
// the lock maps onto the tracker's id rather than something to compare.
func trackerStatusOptions(cfg *config.Config) DesiredItem {
	items := make([]core.Item, 0, len(cfg.Workflow.Columns))
	for i, column := range cfg.Workflow.Columns {
		items = append(items, core.Item{
			Name:   column.Name,
			Fields: map[string]string{FieldPosition: strconv.Itoa(i)},
		})
	}
	return DesiredItem{
		ID:         ItemTrackerStatusOptions,
		Capability: core.CapStatusOptions,
		Target:     TargetTracker,
		DependsOn:  []string{ItemTrackerProject},
		Additive:   true,
		Items:      items,
	}
}

// trackerLabels is every label flow's own rules read, which is every label the
// configuration names plus the fixed vocabulary of the Definition of Ready.
// flow has no opinion about a label's colour or description, so the items
// carry none.
func trackerLabels(cfg *config.Config) DesiredItem {
	var names []string
	add := func(labels ...string) {
		for _, label := range labels {
			if label != "" && !slices.Contains(names, label) {
				names = append(names, label)
			}
		}
	}
	add(cfg.Workflow.Blocked.Label)
	add(cfg.Workflow.Expedite.Labels...)
	add(cfg.FollowUps.Labels...)
	add(cfg.Agents.Labels...)
	add(ReadyLabels...)

	items := make([]core.Item, 0, len(names))
	for _, name := range names {
		items = append(items, core.Item{Name: name})
	}
	return DesiredItem{
		ID:         ItemTrackerLabels,
		Capability: core.CapLabels,
		Target:     TargetTracker,
		DependsOn:  []string{ItemTrackerProject},
		Additive:   true,
		Items:      items,
	}
}

// trackerNative is one item per transition the team asked the tracker's own
// automation to perform (P11, spec 04 §5).
//
// An event with no capability name of its own still becomes an item, under the
// name it would have had. Resolution reads an undeclared capability as the
// zero one and says unsupported (spec 05 §4), which is the honest answer:
// configuration that asks a tracker for automation it does not offer is
// refused by config.Validate, and a plan built from it anyway must not look
// as though it would work.
func trackerNative(cfg *config.Config) []DesiredItem {
	var items []DesiredItem
	for _, event := range config.Events {
		transition, ok := cfg.Workflow.Transitions[event]
		if !ok || transition.Owner != config.OwnerNative {
			continue
		}
		name := core.NativeTransitionPrefix + event
		if known, ok := core.NativeTransitionCapability(event); ok {
			name = known
		}
		to := transition.To
		if column, ok := cfg.Workflow.Column(transition.To); ok {
			to = column.Name
		}
		items = append(items, DesiredItem{
			ID:         NativeItem(event),
			Capability: name,
			Target:     TargetTracker,
			DependsOn:  []string{ItemTrackerStatusOptions},
			Items:      []core.Item{{Name: event, Fields: map[string]string{FieldTo: to}}},
		})
	}
	return items
}

// trackerWIP is one item per column the team limits.
func trackerWIP(cfg *config.Config) DesiredItem {
	var items []core.Item
	for _, column := range cfg.Workflow.Columns {
		if column.WIP == nil {
			continue
		}
		fields := map[string]string{}
		if column.WIP.Limit > 0 {
			fields[FieldLimit] = strconv.Itoa(column.WIP.Limit)
		}
		if column.WIP.PerPerson > 0 {
			fields[FieldPerPerson] = strconv.Itoa(column.WIP.PerPerson)
		}
		if len(fields) == 0 {
			continue
		}
		items = append(items, core.Item{Name: column.Name, Fields: fields})
	}
	return DesiredItem{
		ID:         ItemTrackerWIP,
		Capability: core.CapWIPLimits,
		Target:     TargetTracker,
		DependsOn:  []string{ItemTrackerStatusOptions},
		Items:      items,
	}
}

// trackerExpedite is the view that shows the work allowed to jump the queue.
// How much of it is allowed at once is flow's own cap, enforced by the policy
// gate, so it is not part of what the view must look like.
func trackerExpedite(cfg *config.Config) DesiredItem {
	labels := slices.Clone(cfg.Workflow.Expedite.Labels)
	if len(labels) == 0 {
		return DesiredItem{}
	}
	slices.Sort(labels)
	return DesiredItem{
		ID:         ItemTrackerExpedite,
		Capability: core.CapExpediteView,
		Target:     TargetTracker,
		DependsOn:  []string{ItemTrackerProject},
		Items: []core.Item{{
			Name:   NameExpedite,
			Fields: map[string]string{FieldLabels: strings.Join(labels, ",")},
		}},
	}
}

// forgeDefaultBranch is the trunk: a name to find, which everything else on
// the forge is about.
func forgeDefaultBranch() DesiredItem {
	return DesiredItem{
		ID:      ItemForgeDefaultBranch,
		Target:  TargetForge,
		Resolve: true,
	}
}

// forgeProtection is what the configuration implies about the trunk, and
// nothing more: it is protected, it is not rewritten or deleted, and it keeps
// a linear history when the team squashes or rebases.
//
// How many approvals a pull request needs is deliberately absent. Spec 09's
// gate is what insists on a human approval of agent work, and a required
// approval count in protection would hold every other pull request to a rule
// nobody wrote down (spec 06 §4: flow's protection changes are additive, so
// a team that wants one keeps it).
func forgeProtection(cfg *config.Config) DesiredItem {
	fields := map[string]string{
		FieldEnabled:        "true",
		FieldAllowForcePush: "false",
		FieldAllowDeletions: "false",
	}
	if cfg.Commits.MergeStrategy != "" && cfg.Commits.MergeStrategy != config.MergeCommit {
		fields[FieldRequireLinearHistory] = "true"
	}
	return DesiredItem{
		ID:         ItemForgeProtection,
		Capability: core.CapBranchProtection,
		Target:     TargetForge,
		DependsOn:  []string{ItemForgeDefaultBranch},
		Items:      []core.Item{{Name: NameProtection, Fields: fields}},
	}
}

// forgeRequiredStatus is the gate's own status context, which a merge must
// pass. Whatever else the trunk requires is the team's business.
func forgeRequiredStatus(cfg *config.Config) DesiredItem {
	if cfg.Policy.StatusContext == "" {
		return DesiredItem{}
	}
	return DesiredItem{
		ID:         ItemForgeRequiredStatus,
		Capability: core.CapRequiredStatusChecks,
		Target:     TargetForge,
		DependsOn:  []string{ItemForgeProtection},
		Additive:   true,
		Items:      []core.Item{{Name: cfg.Policy.StatusContext}},
	}
}

// forgeMergeSettings allows the one strategy the team configured and refuses
// the rest, so that the header the gate checked is the header that lands. A
// squash takes its header from the pull request title and its body from the
// pull request body (spec 10 §2).
func forgeMergeSettings(cfg *config.Config) DesiredItem {
	strategy := cfg.Commits.MergeStrategy
	if strategy == "" {
		return DesiredItem{}
	}
	fields := map[string]string{
		FieldAllowSquash: strconv.FormatBool(strategy == config.MergeSquash),
		FieldAllowMerge:  strconv.FormatBool(strategy == config.MergeCommit),
		FieldAllowRebase: strconv.FormatBool(strategy == config.MergeRebase),
	}
	if strategy == config.MergeSquash {
		fields[FieldSquashTitle] = core.SquashTitlePR
		fields[FieldSquashMessage] = core.SquashMessagePRBody
	}
	return DesiredItem{
		ID:         ItemForgeMergeSettings,
		Capability: core.CapMergeSettings,
		Target:     TargetForge,
		Items:      []core.Item{{Name: NameMerge, Fields: fields}},
	}
}

// forgeFiles is the files flow proposes to the repository. What is wanted of
// the workflow file is that it runs flow's commands, not that it matches the
// shipped template byte for byte: a team adds steps of its own to its CI, and
// calling that drift would make the plan nag forever.
//
// Only GitHub's layout is shipped, so only a GitHub repository is proposed to.
func forgeFiles(cfg *config.Config, templates fs.FS) (DesiredItem, error) {
	if cfg.Forge.Type != config.ForgeGitHub {
		return DesiredItem{}, nil
	}
	if templates == nil {
		return DesiredItem{}, fmt.Errorf("desired state: no file templates were supplied")
	}
	runs := []string{"flow check pr"}
	if cfg.Workflow.DoneOn == config.DoneOnDeployed {
		runs = append(runs, "flow check deploy")
	}

	item := DesiredItem{
		ID:         ItemForgeFiles,
		Capability: core.CapFileProposal,
		Target:     TargetForge,
		Additive:   true,
	}
	for _, file := range proposedFiles {
		content, err := fs.ReadFile(templates, file.Template)
		if err != nil {
			return DesiredItem{}, fmt.Errorf("desired state: read template %s: %w", file.Template, err)
		}
		wanted := core.Item{Name: file.Path}
		if file.Workflow {
			wanted = wanted.WithField(FieldRuns, strings.Join(runs, ", "))
		}
		item.Items = append(item.Items, wanted)
		item.Files = append(item.Files, core.ProposedFile{Path: file.Path, Content: content})
	}
	return item, nil
}

// deploysRecords is the environment whose deployments flow reads. A team that
// records no deployments wants nothing here.
func deploysRecords(cfg *config.Config) DesiredItem {
	if cfg.Deploys.Source == "" || cfg.Deploys.Source == config.DeploySourceNone || cfg.Deploys.Environment == "" {
		return DesiredItem{}
	}
	return DesiredItem{
		ID:         ItemDeploysRecords,
		Capability: core.CapDeployRecords,
		Target:     TargetDeploys,
		DependsOn:  []string{ItemForgeDefaultBranch},
		Items:      []core.Item{{Name: cfg.Deploys.Environment}},
	}
}
