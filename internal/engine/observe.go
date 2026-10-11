package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// Adapters are the providers flow was given for this run. Any of them may be
// nil: a team that records no deployments has no deploy source, and comparison
// mode (spec 05 §6) has no provider at all.
type Adapters struct {
	// Forge is the Git host.
	Forge core.Forge
	// Tracker is the ticket system.
	Tracker core.Tracker
	// Deploys is where deployment records come from.
	Deploys core.DeploySource
}

// Capabilities returns what the adapter behind a target declared, and the
// zero set when there is no adapter for it. An undeclared capability reads as
// unsupported, which is the safe answer (spec 05 §1).
func (a Adapters) Capabilities(target string) core.CapabilitySet {
	switch {
	case target == TargetForge && a.Forge != nil:
		return a.Forge.Capabilities()
	case target == TargetTracker && a.Tracker != nil:
		return a.Tracker.Capabilities()
	case target == TargetDeploys && a.Deploys != nil:
		return a.Deploys.Capabilities()
	}
	return core.CapabilitySet{}
}

// Kind returns the adapter's name for a target, empty when there is none.
func (a Adapters) Kind(target string) string {
	switch {
	case target == TargetForge && a.Forge != nil:
		return a.Forge.Kind()
	case target == TargetTracker && a.Tracker != nil:
		return a.Tracker.Kind()
	case target == TargetDeploys && a.Deploys != nil:
		return a.Deploys.Kind()
	}
	return ""
}

// Providers asks every adapter who it is, what it declared it can do and what
// its credential may do. It is the only part of planning that needs a
// credential, and the result is what makes [NewPlan] a pure function.
//
// A target with no adapter is absent from the map rather than present and
// empty, so that "flow was given no tracker" and "the tracker can do nothing"
// stay different answers. A deployment source has no credential of its own:
// it reads through the forge's.
func (a Adapters) Providers(ctx context.Context) (map[string]Provider, error) {
	providers := map[string]Provider{}
	if a.Forge != nil {
		allowed, err := a.Forge.Probe(ctx)
		if err != nil {
			return nil, fmt.Errorf("probe the forge: %w", err)
		}
		providers[TargetForge] = Provider{
			Kind:         a.Forge.Kind(),
			Capabilities: a.Forge.Capabilities(),
			Permissions:  allowed,
		}
	}
	if a.Tracker != nil {
		allowed, err := a.Tracker.Probe(ctx)
		if err != nil {
			return nil, fmt.Errorf("probe the tracker: %w", err)
		}
		providers[TargetTracker] = Provider{
			Kind:         a.Tracker.Kind(),
			Capabilities: a.Tracker.Capabilities(),
			Permissions:  allowed,
		}
	}
	if a.Deploys != nil {
		providers[TargetDeploys] = Provider{
			Kind:         a.Deploys.Kind(),
			Capabilities: a.Deploys.Capabilities(),
		}
	}
	return providers, nil
}

// Observe looks at every desired item and reports what the provider holds, in
// the canonical form a plan is diffed and hashed from (spec 06 §2).
//
// This is the one part of planning that is not pure. Everything downstream of
// it — the diff, the outcomes, the plan — is a function of what comes back
// here, which is why a plan can be written to a file and re-checked later.
func Observe(ctx context.Context, desired Desired, adapters Adapters) (map[string]core.Observation, error) {
	observer := observer{adapters: adapters}
	observations := map[string]core.Observation{}
	for _, item := range desired.Items {
		observation, err := observer.observe(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("observe %s: %w", item.ID, err)
		}
		observation.What = item.ID
		observations[item.ID] = observation.Canonical()
	}
	return observations, nil
}

// observer holds what one pass learns as it goes: the trunk's name is read
// once and everything else about the forge is about that branch.
type observer struct {
	adapters Adapters
	repo     *core.RepoInfo
}

func (o *observer) observe(ctx context.Context, item DesiredItem) (core.Observation, error) {
	switch item.Target {
	case TargetTracker:
		if o.adapters.Tracker == nil {
			return core.Observation{}, fmt.Errorf("%w: no tracker", core.ErrUnsupported)
		}
		return o.adapters.Tracker.Observe(ctx, item.ID)
	case TargetDeploys:
		return o.deploys(ctx, item)
	case TargetForge:
		return o.forge(ctx, item)
	}
	return core.Observation{}, fmt.Errorf("%w: %s belongs to no provider", core.ErrUnsupported, item.ID)
}

// repoInfo reads the repository once and remembers it: the trunk's name is
// what protection, required checks and the proposed files are all about.
func (o *observer) repoInfo(ctx context.Context) (core.RepoInfo, error) {
	if o.repo != nil {
		return *o.repo, nil
	}
	if o.adapters.Forge == nil {
		return core.RepoInfo{}, fmt.Errorf("%w: no forge", core.ErrUnsupported)
	}
	repo, err := o.adapters.Forge.Repo(ctx)
	if err != nil {
		return core.RepoInfo{}, err
	}
	o.repo = &repo
	return repo, nil
}

func (o *observer) forge(ctx context.Context, item DesiredItem) (core.Observation, error) {
	repo, err := o.repoInfo(ctx)
	if err != nil {
		return core.Observation{}, err
	}
	switch item.ID {
	case core.ItemForgeDefaultBranch:
		return core.Observation{
			Exists: repo.DefaultBranch != "",
			Items:  []core.Item{{ID: repo.DefaultBranch, Name: repo.DefaultBranch}},
		}, nil

	case core.ItemForgeProtection, core.ItemForgeRequiredStatus:
		protection, err := o.adapters.Forge.Protection(ctx, repo.DefaultBranch)
		if err != nil {
			return core.Observation{}, err
		}
		if item.ID == core.ItemForgeRequiredStatus {
			return requiredStatus(protection), nil
		}
		return protectionObservation(protection), nil

	case core.ItemForgeMergeSettings:
		return mergeObservation(repo.Merge), nil

	case core.ItemForgeFiles:
		return o.files(ctx, item, repo.DefaultBranch)
	}
	return core.Observation{}, fmt.Errorf("%w: nothing observes %s", core.ErrUnsupported, item.ID)
}

// protectionObservation transcribes the trunk's protection into the fields
// the desired item names.
func protectionObservation(p core.Protection) core.Observation {
	return core.Observation{
		Exists: p.Enabled,
		Items: []core.Item{{
			Name: NameProtection,
			Fields: map[string]string{
				FieldEnabled:              strconv.FormatBool(p.Enabled),
				FieldAllowForcePush:       strconv.FormatBool(p.AllowForcePush),
				FieldAllowDeletions:       strconv.FormatBool(p.AllowDeletions),
				FieldRequireLinearHistory: strconv.FormatBool(p.RequireLinearHistory),
			},
		}},
	}
}

// requiredStatus is one item per check the trunk already requires.
func requiredStatus(p core.Protection) core.Observation {
	items := make([]core.Item, 0, len(p.RequiredChecks))
	for _, context := range p.RequiredChecks {
		items = append(items, core.Item{Name: context})
	}
	return core.Observation{Exists: p.Enabled, Items: items}
}

// mergeObservation transcribes the repository's merge settings.
func mergeObservation(m core.MergeSettings) core.Observation {
	fields := map[string]string{
		FieldAllowSquash: strconv.FormatBool(m.AllowSquash),
		FieldAllowMerge:  strconv.FormatBool(m.AllowMerge),
		FieldAllowRebase: strconv.FormatBool(m.AllowRebase),
	}
	if m.SquashTitle != "" {
		fields[FieldSquashTitle] = m.SquashTitle
	}
	if m.SquashMessage != "" {
		fields[FieldSquashMessage] = m.SquashMessage
	}
	return core.Observation{Exists: true, Items: []core.Item{{Name: NameMerge, Fields: fields}}}
}

// files reads each proposed path on the trunk. A file that is not there is
// not an error and not an item: it is the difference the plan is about.
//
// What is read out of the workflow is which of flow's commands it runs, which
// is all spec 07 §5 asks of it.
func (o *observer) files(ctx context.Context, item DesiredItem, branch string) (core.Observation, error) {
	var items []core.Item
	for _, wanted := range item.Items {
		content, err := o.adapters.Forge.File(ctx, wanted.Name, branch)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			return core.Observation{}, err
		}
		found := core.Item{Name: wanted.Name}
		if runs := flowCommands(content); len(runs) > 0 {
			found = found.WithField(FieldRuns, strings.Join(runs, ", "))
		}
		items = append(items, found)
	}
	return core.Observation{Exists: len(items) > 0, Items: items}, nil
}

// flowCommands reports which of flow's commands a file invokes, in the order
// the desired state lists them so that the two can be compared as written.
func flowCommands(content []byte) []string {
	var found []string
	for _, command := range []string{"flow check pr", "flow check deploy"} {
		if strings.Contains(string(content), command) {
			found = append(found, command)
		}
	}
	return found
}

// deploys reports whether the environment has a deployment record flow can
// read. Nothing is ever written here: a deployment is somebody else's event,
// and flow only has to be able to see it (spec 13 §3).
func (o *observer) deploys(ctx context.Context, item DesiredItem) (core.Observation, error) {
	if o.adapters.Deploys == nil {
		return core.Observation{}, fmt.Errorf("%w: no deployment source", core.ErrUnsupported)
	}
	var items []core.Item
	for _, wanted := range item.Items {
		records, err := o.adapters.Deploys.Deployments(ctx, core.DeployQuery{Environment: wanted.Name, Limit: 1})
		if err != nil {
			return core.Observation{}, err
		}
		if len(records) == 0 {
			continue
		}
		items = append(items, core.Item{ID: records[0].ID, Name: wanted.Name})
	}
	return core.Observation{Exists: len(items) > 0, Items: items}, nil
}

// MergeSettingsFor turns the desired merge-settings item back into the value
// an adapter is asked to set. The desired state is the only description of
// what flow wants, so apply reads it rather than deriving it a second time.
func MergeSettingsFor(item core.Item) core.MergeSettings {
	return core.MergeSettings{
		AllowSquash:   item.Field(FieldAllowSquash) == "true",
		AllowMerge:    item.Field(FieldAllowMerge) == "true",
		AllowRebase:   item.Field(FieldAllowRebase) == "true",
		SquashTitle:   item.Field(FieldSquashTitle),
		SquashMessage: item.Field(FieldSquashMessage),
	}
}

// ProtectionFor turns the desired protection item and required checks back
// into the value an adapter is asked to set. It is additive by construction:
// the adapter merges it onto what the branch already has (spec 06 §4).
func ProtectionFor(branch string, protection core.Item, checks []core.Item) core.Protection {
	wanted := core.Protection{
		Branch:               branch,
		Enabled:              protection.Field(FieldEnabled) != "false",
		RequireLinearHistory: protection.Field(FieldRequireLinearHistory) == "true",
		AllowForcePush:       protection.Field(FieldAllowForcePush) == "true",
		AllowDeletions:       protection.Field(FieldAllowDeletions) == "true",
	}
	for _, check := range checks {
		wanted.RequiredChecks = append(wanted.RequiredChecks, check.Name)
	}
	slices.Sort(wanted.RequiredChecks)
	return wanted
}
