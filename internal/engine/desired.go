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

// Fields a desired item's contents carry. A field is only ever something the
// provider can report back, so that a comparison means something.
const (
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
	FieldStrictChecks         = "strict_checks"
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
	// NameSquash is the one squash-text item on the repository.
	NameSquash = "squash"
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
	// makes. Only [core.ItemForgeFiles] has any.
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
	desired.add(forgeSquashText(cfg))
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
		ID:         core.ItemTrackerProject,
		Capability: core.CapProject,
		Target:     TargetTracker,
		Resolve:    true,
	}
}

// trackerStatusOptions is one option per configured column.
//
// The option is its display name and nothing else. A column's category is
// flow's own mapping onto todo/in_progress/done (spec 04 §2) and no tracker
// researched can report it; a column's configured id is what the lock maps
// onto the tracker's id rather than something to compare; and the order the
// columns appear in on the board is cosmetic, where replacing an option set
// to reorder it can clear every item's status (spec 06 §4).
func trackerStatusOptions(cfg *config.Config) DesiredItem {
	items := make([]core.Item, 0, len(cfg.Workflow.Columns))
	for _, column := range cfg.Workflow.Columns {
		items = append(items, core.Item{Name: column.Name})
	}
	return DesiredItem{
		ID:         core.ItemTrackerStatusOptions,
		Capability: core.CapStatusOptions,
		Target:     TargetTracker,
		DependsOn:  []string{core.ItemTrackerProject},
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
		ID:         core.ItemTrackerLabels,
		Capability: core.CapLabels,
		Target:     TargetTracker,
		DependsOn:  []string{core.ItemTrackerProject},
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
			ID:         core.NativeItem(event),
			Capability: name,
			Target:     TargetTracker,
			DependsOn:  []string{core.ItemTrackerStatusOptions},
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
		ID:         core.ItemTrackerWIP,
		Capability: core.CapWIPLimits,
		Target:     TargetTracker,
		DependsOn:  []string{core.ItemTrackerStatusOptions},
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
		ID:         core.ItemTrackerExpedite,
		Capability: core.CapExpediteView,
		Target:     TargetTracker,
		DependsOn:  []string{core.ItemTrackerProject},
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
		ID:      core.ItemForgeDefaultBranch,
		Target:  TargetForge,
		Resolve: true,
	}
}

// forgeProtection is what the configuration implies about the trunk, and
// nothing more: it is protected, it is not rewritten or deleted, it keeps a
// linear history when the team squashes or rebases, and a branch is up to
// date with it before it may merge.
//
// That last one is what makes the gate's verdict true of the trunk rather
// than of the branch. A status is about the commit it was posted on, and a
// branch that has fallen behind is a different tree from the one that lands:
// two pull requests can each be green against the trunk they left and still
// be red together. So wherever flow requires a check of its own it also
// requires the branch to be current, and where it requires no check there is
// nothing for currency to be a property of (spec 10 §2).
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
		FieldStrictChecks:   strconv.FormatBool(len(requiredContexts(cfg)) > 0),
	}
	if cfg.Commits.MergeStrategy != "" && cfg.Commits.MergeStrategy != config.MergeCommit {
		fields[FieldRequireLinearHistory] = "true"
	}
	return DesiredItem{
		ID:         core.ItemForgeProtection,
		Capability: core.CapBranchProtection,
		Target:     TargetForge,
		DependsOn:  []string{core.ItemForgeDefaultBranch},
		Items:      []core.Item{{Name: NameProtection, Fields: fields}},
	}
}

// forgeRequiredStatus is what a merge must pass: flow's own gate, and the
// checks the team named beside it.
//
// The item is additive (spec 06 §4), so a context the trunk already requires
// and nobody wrote down survives a run. That is what makes it safe to name
// the build here: flow asks for it where it is missing and never takes
// anybody else's check away.
func forgeRequiredStatus(cfg *config.Config) DesiredItem {
	contexts := requiredContexts(cfg)
	if len(contexts) == 0 {
		return DesiredItem{}
	}
	items := make([]core.Item, 0, len(contexts))
	for _, context := range contexts {
		items = append(items, core.Item{Name: context})
	}
	return DesiredItem{
		ID:         core.ItemForgeRequiredStatus,
		Capability: core.CapRequiredStatusChecks,
		Target:     TargetForge,
		DependsOn:  []string{core.ItemForgeProtection},
		Additive:   true,
		Items:      items,
	}
}

// requiredContexts is every status the configuration asks of the trunk: the
// gate's own context, and forge.required_checks beside it.
//
// A team may require a build without running flow's gate, and a team may run
// flow's gate and require nothing else, so neither half implies the other.
// Naming the gate's own context again under forge.required_checks is a
// repetition, not a second check, and is quietly dropped: a forge asked for
// the same context twice is being asked something nobody meant. What order
// they end up in is not decided here — the desired state canonicalises its
// items, so the same set written two ways is the same plan and the same
// hash.
func requiredContexts(cfg *config.Config) []string {
	contexts := make([]string, 0, 1+len(cfg.Forge.RequiredChecks))
	seen := map[string]bool{}
	for _, context := range append([]string{cfg.Policy.StatusContext}, cfg.Forge.RequiredChecks...) {
		if context == "" || seen[context] {
			continue
		}
		seen[context] = true
		contexts = append(contexts, context)
	}
	return contexts
}

// forgeMergeSettings allows the one strategy the team configured and refuses
// the rest, so that one pull request lands as one commit of a known shape.
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
	return DesiredItem{
		ID:         core.ItemForgeMergeSettings,
		Capability: core.CapMergeSettings,
		Target:     TargetForge,
		Items:      []core.Item{{Name: NameMerge, Fields: fields}},
	}
}

// forgeSquashText is where a squash commit takes its words from: its header
// from the pull request title, so that the header the gate checked is the
// header that lands, and its body from the pull request body, so that the
// reasoning for a change is in the trunk and not only in the forge (spec 10
// §2).
//
// It is a separate item from the strategies because it is a separate
// capability: a forge can let a repository choose its merge styles and still
// compose the squash commit itself, and asking for both as one setting would
// make flow propose, every run, a change it has no way to make. Both items
// are written by the same call, the way protection and its required checks
// are.
func forgeSquashText(cfg *config.Config) DesiredItem {
	if cfg.Commits.MergeStrategy != config.MergeSquash {
		return DesiredItem{}
	}
	return DesiredItem{
		ID:         core.ItemForgeSquashText,
		Capability: core.CapSquashText,
		Target:     TargetForge,
		DependsOn:  []string{core.ItemForgeMergeSettings},
		Items: []core.Item{{Name: NameSquash, Fields: map[string]string{
			FieldSquashTitle:   core.SquashTitlePR,
			FieldSquashMessage: core.SquashMessagePRBody,
		}}},
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
		ID:         core.ItemForgeFiles,
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
// records no deployments wants nothing here. The condition is
// [ReadsDeployments], which the live-health audit asks as well: the plan and
// the audit must agree about which repositories have deployments, and two
// spellings of one test are two things to keep in step.
func deploysRecords(cfg *config.Config) DesiredItem {
	if reads, _ := ReadsDeployments(cfg); !reads {
		return DesiredItem{}
	}
	return DesiredItem{
		ID:         core.ItemDeploysRecords,
		Capability: core.CapDeployRecords,
		Target:     TargetDeploys,
		DependsOn:  []string{core.ItemForgeDefaultBranch},
		Items:      []core.Item{{Name: cfg.Deploys.Environment}},
	}
}
