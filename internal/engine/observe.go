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
			return requiredStatus(protection, item), nil
		}
		observation := protectionObservation(protection)
		switch {
		case protection.Unreadable:
			observation = unreadProtection(observation, item)
		case protection.AdminOnly && len(protection.Immovable) > 0:
			observation = immovableProtection(observation, item, protection.Immovable)
		case protection.AdminOnly:
			observation.Notes = append(observation.Notes, core.Note{
				Text: "the trunk's protection is kept where only an admin can read it, so a run on any other credential cannot see it",
				Fix:  "flow apply moves it where anybody who can read the repository can read it",
			})
		}
		return observation, nil

	case core.ItemForgeMergeSettings:
		return mergeObservation(repo.Merge), nil

	case core.ItemForgeSquashText:
		return squashObservation(repo.Merge), nil

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
				FieldStrictChecks:         strconv.FormatBool(p.StrictChecks),
				FieldAdminOnly:            strconv.FormatBool(p.AdminOnly),
			},
		}},
		Notes: dormantNotes(p),
	}
}

// unreadProtection takes the fields a credential could not read as asked for,
// and says so (issue #255). The alternative is calling them undone, which is
// how a daily run on a non-admin credential reopened the follow-ups an admin's
// apply had just verified. What could be read — that the branch is protected
// at all — is still the observation's own.
func unreadProtection(observation core.Observation, item DesiredItem) core.Observation {
	if len(item.Items) == 0 || len(observation.Items) == 0 {
		return observation
	}
	for _, field := range []string{FieldAllowForcePush, FieldAllowDeletions, FieldRequireLinearHistory, FieldStrictChecks} {
		if wanted := item.Items[0].Field(field); wanted != "" {
			observation.Items[0] = observation.Items[0].WithField(field, wanted)
		}
	}
	observation.Notes = append(observation.Notes, core.Note{
		Text:       "this credential may see that the trunk is protected, and not how: what it could not read is taken as asked for",
		Fix:        "keep the trunk's protection in a ruleset, which anybody who can read the repository can read — flow apply run by an admin writes one",
		Unverified: true,
	})
	return observation
}

// immovableProtection is protection kept where only an admin can read it that
// flow cannot move without losing part of it (issue #258). Where it is kept is
// taken as asked for, because nothing flow does would make it better, and the
// note says what a person would have to carry across by hand.
func immovableProtection(observation core.Observation, item DesiredItem, immovable []string) core.Observation {
	if len(item.Items) == 0 || len(observation.Items) == 0 {
		return observation
	}
	if wanted := item.Items[0].Field(FieldAdminOnly); wanted != "" {
		observation.Items[0] = observation.Items[0].WithField(FieldAdminOnly, wanted)
	}
	observation.Notes = append(observation.Notes, core.Note{
		Text:       "the trunk's protection is kept where only an admin can read it, and says what flow cannot move: " + strings.Join(immovable, ", "),
		Fix:        "move the trunk's protection into a ruleset by hand, then flow apply moves nothing and a run on any credential can read it",
		Unverified: true,
	})
	return observation
}

// requiredStatus is one item per check the trunk already requires.
//
// A forge that requires contexts by pattern names the pattern and never the
// check, so each check the configuration wants that a pattern covers is
// observed under its own name as well — once, so that a forge reading a name
// as the pattern that matches only itself observes the trunk every other
// forge does. Without it a trunk requiring `flow/*`
// reads as not requiring `flow/policy`, and the plan asks to add what the
// forge already demands (issue #293).
func requiredStatus(p core.Protection, wanted DesiredItem) core.Observation {
	items := make([]core.Item, 0, len(p.RequiredChecks))
	for _, context := range p.RequiredChecks {
		items = append(items, core.Item{Name: context})
	}
	for _, want := range wanted.Items {
		if !slices.Contains(p.RequiredChecks, want.Name) && p.RequiresCheck(want.Name) {
			items = append(items, core.Item{Name: want.Name})
		}
	}
	return core.Observation{Exists: p.Enabled, Items: items, Notes: dormantNotes(p)}
}

// dormantNotes are the rules the forge holds for the trunk and is not
// enforcing, as the report's own words.
//
// Both protection items carry them, because a dormant ruleset is usually the
// explanation for both at once: the thing that would protect the branch and
// the thing that would require a check are one ruleset with a switch off.
func dormantNotes(p core.Protection) []core.Note {
	notes := make([]core.Note, 0, len(p.Dormant))
	for _, rule := range p.Dormant {
		notes = append(notes, core.Note{
			Text: fmt.Sprintf("the rule %q covers %s and is not in force: %s", rule.Name, p.Branch, rule.State),
			URL:  rule.URL,
			Fix:  fmt.Sprintf("switch the rule %q on — it is already written, and only its enforcement is %s", rule.Name, rule.State),
		})
	}
	if len(notes) == 0 {
		return nil
	}
	return notes
}

// mergeObservation transcribes the strategies the repository allows, and
// whether a pull request may be left to land itself.
//
// Auto-merge is always transcribed, including by a forge that has no setting
// for it: such a forge permits the arming unconditionally, so "true" is what
// it would say if it were asked. An observation that left the field out would
// read as a repository refusing something it has never refused anybody.
func mergeObservation(m core.MergeSettings) core.Observation {
	return core.Observation{Exists: true, Items: []core.Item{{Name: NameMerge, Fields: map[string]string{
		FieldAllowSquash:    strconv.FormatBool(m.AllowSquash),
		FieldAllowMerge:     strconv.FormatBool(m.AllowMerge),
		FieldAllowRebase:    strconv.FormatBool(m.AllowRebase),
		FieldAllowAutoMerge: strconv.FormatBool(m.AllowAutoMerge),
	}}}}
}

// squashObservation transcribes where the repository takes a squash commit's
// words from. A forge that composes the commit itself has nothing to say
// about one of them, and says nothing rather than agreeing: a field flow
// leaves out of the observation is one the comparison counts as a
// difference, which is what a forge that cannot be told should produce.
func squashObservation(m core.MergeSettings) core.Observation {
	fields := map[string]string{}
	if m.SquashTitle != "" {
		fields[FieldSquashTitle] = m.SquashTitle
	}
	if m.SquashMessage != "" {
		fields[FieldSquashMessage] = m.SquashMessage
	}
	return core.Observation{Exists: true, Items: []core.Item{{Name: NameSquash, Fields: fields}}}
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
		if runs := flowCommands(content, wanted.Field(FieldRuns)); len(runs) > 0 {
			found = found.WithField(FieldRuns, strings.Join(runs, ", "))
		}
		items = append(items, found)
	}
	return core.Observation{Exists: len(items) > 0, Items: items}, nil
}

// flowCommands reports which of the commands a file is wanted to run it
// actually runs, in the order they were asked for so that the two can be
// compared as written.
//
// Only the wanted commands are looked for, which is what makes the check the
// one spec 06 §1 describes: that the workflow runs flow's commands, not that
// it runs those and no others. A team whose CI also calls `flow check deploy`
// has added a step of its own, and the template flow itself ships carries
// both — reporting the extra one as a difference would have every plan
// propose the same update for ever.
func flowCommands(content []byte, wanted string) []string {
	var found []string
	for _, command := range strings.Split(wanted, ", ") {
		if command != "" && strings.Contains(string(content), command) {
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

// MergeSettingsFor turns the desired strategies and squash text back into
// the value an adapter is asked to set. The desired state is the only
// description of what flow wants, so apply reads it rather than deriving it a
// second time. They are two items because they are two capabilities, and one
// call because the forge has one setting: an adapter that is given only half
// of it would turn the other half off.
//
// The setting the repository has now is where the answer starts, and only the
// fields a desired item actually names are written over it. A merge setting
// is written whole — GitHub's repository PATCH carries every field of it,
// whatever flow meant to change — so a field no desired item mentions would
// otherwise be set to its zero value by a write made for some other reason. A
// plan that does not say it will turn "delete the branch after merging" off
// must not turn it off (spec 06 §4).
//
// Presence decides, not value: a desired item naming a field as "false" means
// false, and one not naming it at all means flow has no opinion about it.
// That is also what lets a setting be asked for additively — a configuration
// that wants nothing of one leaves it out, rather than asking a forge for a
// "no" it may have no way to say.
func MergeSettingsFor(current core.MergeSettings, strategies, text core.Item) core.MergeSettings {
	wanted := current
	overlayBool(strategies, FieldAllowSquash, &wanted.AllowSquash)
	overlayBool(strategies, FieldAllowMerge, &wanted.AllowMerge)
	overlayBool(strategies, FieldAllowRebase, &wanted.AllowRebase)
	overlayBool(strategies, FieldAllowAutoMerge, &wanted.AllowAutoMerge)
	overlayText(text, FieldSquashTitle, &wanted.SquashTitle)
	overlayText(text, FieldSquashMessage, &wanted.SquashMessage)
	return wanted
}

// overlayBool writes a flag onto the setting when the item names it, and
// leaves the setting alone when it does not.
func overlayBool(item core.Item, field string, target *bool) {
	if value, named := item.Fields[field]; named {
		*target = value == "true"
	}
}

// overlayText is [overlayBool] for a field whose value is a word rather than
// a flag.
func overlayText(item core.Item, field string, target *string) {
	if value, named := item.Fields[field]; named {
		*target = value
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
		StrictChecks:         protection.Field(FieldStrictChecks) == "true",
	}
	for _, check := range checks {
		wanted.RequiredChecks = append(wanted.RequiredChecks, check.Name)
	}
	slices.Sort(wanted.RequiredChecks)
	return wanted
}
