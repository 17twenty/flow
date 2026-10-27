package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// PlanVersion is the schema the plan's JSON declares (spec 05 §5, spec 02 §3).
// A reader that does not know this string must refuse the file rather than
// guess.
const PlanVersion = "flow.plan/v1"

// Provider is one target's adapter as the planner needs it: who it is, what
// its research says it can do, and what its credential turned out to hold.
//
// The planner takes these rather than the adapters themselves, which is what
// makes [NewPlan] pure — and what lets comparison mode (spec 05 §6) put
// another tracker's declarations in front of the same desired state with no
// network at all.
type Provider struct {
	// Kind is the adapter's name, e.g. github-projects.
	Kind string
	// Capabilities is its declaration (spec 05 §1).
	Capabilities core.CapabilitySet
	// Permissions is what Probe reported. The zero value allows nothing.
	Permissions core.Permissions
}

// PlanInput is everything a plan is made of. Nothing in here is read from a
// network, a clock or a disk: the caller has already looked.
type PlanInput struct {
	// Config is the workflow the team asked for.
	Config *config.Config
	// Desired is what that configuration asks of a provider.
	Desired Desired
	// Observations is what the providers hold, keyed by desired item id. A
	// missing entry reads as "nothing is there", which is what an item flow
	// could not look at should plan as.
	Observations map[string]core.Observation
	// Providers are the adapters behind each target, keyed by target.
	Providers map[string]Provider
	// Lock is what flow learned before, for attestations and for what flow
	// created and may therefore remove. It may be nil.
	Lock *config.Lock
	// ConfigHash is the hash of the configuration this plan is for.
	ConfigHash string
	// FlowVersion is the build making the plan.
	FlowVersion string
	// Now is the instant the plan is made at.
	Now time.Time
	// Only limits the plan to these capability names (`flow plan --only`).
	Only []string
	// Targets limits it to these targets (`flow plan --target`).
	Targets []string
}

// Plan is what flow will do, why, and what it was looking at when it decided
// (spec 06 §2). It is serialisable so that `flow apply --plan` can act on the
// one a human read, and refuse it once the world has moved on.
type Plan struct {
	// Schema is [PlanVersion]. It is both the plan file's own declaration
	// (spec 05 §5) and the `schema` every --json document carries
	// (spec 02 §3): one string, so a plan printed to a terminal and a plan
	// written to a file cannot disagree about what they are.
	Schema string `json:"schema"`
	// FlowVersion is the build that made it.
	FlowVersion string `json:"flow_version,omitempty"`
	// GeneratedAt is when it was made.
	GeneratedAt time.Time `json:"generated_at"`
	// Providers names the adapter behind each target.
	Providers map[string]string `json:"providers,omitempty"`
	// ConfigHash, DesiredHash and LockHash are what the plan was made from.
	// Apply compares them against the world it finds (spec 06 §3).
	ConfigHash  string `json:"config_hash,omitempty"`
	DesiredHash string `json:"desired_hash"`
	LockHash    string `json:"lock_hash,omitempty"`
	// Items is one entry per desired item, in dependency order.
	Items []PlanItem `json:"items"`
}

// PlanItem is one desired item, what the provider had, and what flow will do
// about the difference.
type PlanItem struct {
	// ID is the desired item's id, which is also the doctor check id and the
	// attestation id.
	ID string `json:"id"`
	// Target is tracker, forge or deploys.
	Target string `json:"target"`
	// Capability is the registry name the decision turned on.
	Capability string `json:"capability,omitempty"`
	// DependsOn are the items that must be settled first.
	DependsOn []string `json:"depends_on,omitempty"`
	// Outcome is what flow will do (spec 05 §4).
	Outcome Outcome `json:"outcome"`
	// Mechanism names what enforces the intent instead, for a compensated
	// outcome.
	Mechanism string `json:"mechanism,omitempty"`
	// Owner is who a follow-up is routed to (spec 07 §4).
	Owner string `json:"owner,omitempty"`
	// Missing are the permission tokens that stopped flow acting natively.
	Missing []string `json:"missing,omitempty"`
	// Why is one line explaining the outcome.
	Why string `json:"why"`
	// Exists reports whether the thing was there at all when flow looked.
	Exists bool `json:"exists"`
	// DesiredHash and ObservedHash are the canonical hashes the diff was
	// taken between, so that a stale plan can be told apart from a fresh one
	// item by item.
	DesiredHash  string `json:"desired_hash"`
	ObservedHash string `json:"observed_hash"`
	// Notes are what the provider volunteered about the item without flow
	// comparing it on them (spec 06 §1). They change what a report tells a
	// person to do and never what the plan decides, which is why they are
	// outside the canonical encoding the hashes are taken over.
	Notes []core.Note `json:"notes,omitempty"`
	// Changes are the mutations that would make the provider agree, in the
	// order apply makes them. A compensated, attested or unsupported item
	// still lists them: they are what would have to happen, and a human
	// reading the plan is owed that whether or not flow can do it.
	Changes []core.Change `json:"changes,omitempty"`
	// Hazards are the ways this provider's API can hurt, from its
	// declaration, for a human to read before approving.
	Hazards []string `json:"hazards,omitempty"`
	// Recipe is how a person makes this true by hand, from the provider's
	// declaration, with the names of this run filled in (spec 05 §1). It is
	// what a follow-up ticket is written out of, and it is carried on the
	// plan rather than looked up again later so that a plan read tomorrow
	// says what flow decided today.
	//
	// Only a follow-up carries one. Every other outcome is flow saying it
	// will see to this itself, or that it is already true, and instructions
	// for doing by hand a thing nobody has to do are the longest part of a
	// document whose whole job is to be read before it is approved.
	Recipe core.Recipe `json:"recipe,omitzero"`
}

// NewPlan diffs the desired state against what was observed and decides what
// flow does about each difference. It is spec 06 §2's "pure diff": given the
// same input it gives the same plan, on any machine, at any time.
func NewPlan(in PlanInput) (Plan, error) {
	if in.Config == nil {
		return Plan{}, fmt.Errorf("plan: no configuration was supplied")
	}
	plan := Plan{
		Schema:      PlanVersion,
		FlowVersion: in.FlowVersion,
		GeneratedAt: in.Now.UTC().Truncate(time.Second),
		ConfigHash:  in.ConfigHash,
		DesiredHash: in.Desired.Hash(),
	}
	if len(in.Providers) > 0 {
		plan.Providers = map[string]string{}
		for target, provider := range in.Providers {
			plan.Providers[target] = provider.Kind
		}
	}
	if in.Lock != nil {
		data, err := in.Lock.Bytes()
		if err != nil {
			return Plan{}, fmt.Errorf("plan: %w", err)
		}
		plan.LockHash = config.Hash(data)
	}

	for _, wanted := range in.Desired.Items {
		if !in.wants(wanted) {
			continue
		}
		plan.Items = append(plan.Items, in.item(wanted))
	}
	return plan, nil
}

// wants reports whether an item survives --target and --only. An empty filter
// selects everything, so the plain `flow plan` reads the same code path.
func (in PlanInput) wants(item DesiredItem) bool {
	if len(in.Targets) > 0 && !slices.Contains(in.Targets, item.Target) {
		return false
	}
	if len(in.Only) > 0 && !slices.Contains(in.Only, item.Capability) {
		return false
	}
	return true
}

// readOnly is the capability of an item that names none: the trunk is
// whatever the repository says it is. flow can always look, can never set it,
// and a person has to act if it is not there — which is exactly what the
// ladder does with `{set: false, verify: true, remediation: follow_up}`.
// Synthesising it here keeps spec 05 §4 the only place an outcome is decided.
var readOnly = core.Capability{Verify: true, Remediation: core.RemediationFollowUp}

// item plans one desired item.
func (in PlanInput) item(wanted DesiredItem) PlanItem {
	observation := in.Observations[wanted.ID]
	declared := readOnly
	if wanted.Capability != "" {
		declared = in.Providers[wanted.Target].Capabilities.Capability(wanted.Capability)
	}
	changes := in.changes(wanted, declared, observation)

	resolution := Resolve(Difference{
		Capability: wanted.Capability,
		Subject:    wanted.ID,
		Declared:   declared,
		Observed:   observation.Exists && len(changes) == 0,
		Attested:   in.attested(wanted.ID, wanted.Hash()),
	}, in.Providers[wanted.Target].Permissions)

	item := PlanItem{
		ID:           wanted.ID,
		Target:       wanted.Target,
		Capability:   wanted.Capability,
		DependsOn:    wanted.DependsOn,
		Outcome:      resolution.Outcome,
		Mechanism:    resolution.Mechanism,
		Missing:      resolution.Missing,
		Why:          resolution.Why,
		Exists:       observation.Exists,
		DesiredHash:  wanted.Hash(),
		ObservedHash: HashObservation(observation),
		Changes:      changes,
		Hazards:      declared.Hazards,
		Notes:        observation.Notes,
	}
	if item.Outcome == OutcomeFollowUp {
		item.Owner = followUpOwner(in.Config, wanted.Target)
		item.Recipe = in.recipe(declared, changes)
	}
	return item
}

// recipe is the declaration's by-hand instructions with this run's names in
// them.
//
// A recipe is written once, for every repository that will ever use the
// adapter, and read by one person holding one of them. "Add the option named
// {{name}}" is a sentence about the general case; "Add the option named
// Ready" is the sentence they need, and flow is the only thing that knows
// which it is. The substitution happens here, where the plan is made, so
// that the plan carries the words rather than the template: a plan is a
// record of what flow decided, and a template is a decision deferred.
func (in PlanInput) recipe(declared core.Capability, changes []core.Change) core.Recipe {
	if !declared.ByHand.Written() {
		return core.Recipe{}
	}
	names := make([]string, 0, len(changes))
	for _, change := range changes {
		if name := change.Item.Name; !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	fields := map[string]string{core.RecipeRepo: in.Config.Forge.Repo}
	// An item with nothing to change leaves the placeholder standing: the
	// board itself is an id to find rather than a thing to alter, and it has
	// no name of its own to offer. No declaration asks for one there, so a
	// `{{name}}` surviving into a ticket is a recipe written for an item it
	// does not belong to, and a visible brace is a better account of that
	// than a sentence with a hole where the word should be.
	if len(names) > 0 {
		fields[core.RecipeName] = joinWords(names)
	}
	return declared.ByHand.Expand(fields)
}

// changes is the diff itself: what would have to happen for the provider to
// agree with the configuration.
//
// An item is compared by name. A provider knows more about one of its own
// things than flow ever asked — a label's colour, an option's description —
// so an observed item matches when it says everything the desired one says,
// not when it says exactly the same things ([core.Item.Matches]).
//
// Whether two names are one name is the provider's question about that set,
// so the declaration answers it ([core.Capability.FoldCase]). A thing found
// under another case is the thing, and the change renames it in place —
// keeping its identifier, so whatever points at it follows — rather than
// adding a second one beside it.
//
// A resolve item has no contents: the work is finding it, which doctor does
// (spec 07 §3), so there is nothing here to change.
func (in PlanInput) changes(wanted DesiredItem, declared core.Capability, observation core.Observation) []core.Change {
	if wanted.Resolve {
		return nil
	}
	var changes []core.Change
	for _, want := range wanted.Items {
		found, ok := find(declared, observation, want.Name)
		switch {
		case !ok:
			changes = append(changes, core.Change{What: wanted.ID, Op: core.ChangeCreate, Item: want})
		case !found.Matches(want):
			want.ID = found.ID
			changes = append(changes, core.Change{
				What:    wanted.ID,
				Op:      core.ChangeUpdate,
				Item:    want,
				Differs: found.Differences(want),
			})
		}
	}
	return append(changes, in.deletions(wanted, declared, observation)...)
}

// deletions are the things the provider holds that the configuration does not
// ask for.
//
// What happens to one depends on whether flow owns the whole set. An additive
// item — labels, status options, required checks, proposed files — is shared
// with the people using the provider, so only what the lock records flow as
// having created is ever proposed for removal (spec 06 §4). The rest are one
// setting rather than a set: a work-in-progress limit on a column nobody
// configured is flow's own leftover, and leaving it would quietly enforce a
// rule nobody wrote down.
//
// Whether a proposed removal actually happens is `--prune`'s decision at
// apply time; the plan's job is to say what is there.
func (in PlanInput) deletions(wanted DesiredItem, declared core.Capability, observation core.Observation) []core.Change {
	var changes []core.Change
	for _, found := range observation.Items {
		if slices.ContainsFunc(wanted.Items, func(want core.Item) bool {
			match, ok := find(declared, observation, want.Name)
			return ok && match.ID == found.ID && match.Name == found.Name
		}) {
			continue
		}
		if wanted.Additive && !in.created(wanted.ID, found) {
			continue
		}
		changes = append(changes, core.Change{What: wanted.ID, Op: core.ChangeDelete, Item: found})
	}
	return changes
}

// find is the observed thing a desired one of that name is about: the one of
// exactly that name, or where the provider folds case in that set, the first
// one of that name in any case. Exact wins so that a provider already holding
// both spellings is matched to the one the configuration wrote.
func find(declared core.Capability, observation core.Observation, name string) (core.Item, bool) {
	if item, ok := observation.Find(name); ok || !declared.FoldCase {
		return item, ok
	}
	for _, item := range observation.Items {
		if strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	return core.Item{}, false
}

// created reports whether the lock records this thing as flow's own doing.
func (in PlanInput) created(id string, item core.Item) bool {
	return created(in.Lock, id, item)
}

// created reports whether the lock records this thing as flow's own doing. It
// is what spec 06 §4 turns on: nothing flow did not create is ever deleted,
// and both the plan and the apply ask the question the same way.
//
// A creation is matched by identifier when both sides have one, and by name
// otherwise. Not everything a provider holds has an id — a required status
// context is only ever its own name — and matching two empty ids would make
// one recorded creation stand for every nameless thing in the set.
func created(lock *config.Lock, id string, item core.Item) bool {
	if lock == nil {
		return false
	}
	kind := CreationKind(id)
	return slices.ContainsFunc(lock.Created, func(creation config.Creation) bool {
		return creation.Kind == kind && sameThing(creation, item)
	})
}

// sameThing reports whether a recorded creation and an observed thing are the
// same thing: by identifier when both have one, by name otherwise.
func sameThing(creation config.Creation, item core.Item) bool {
	if creation.ID != "" && item.ID != "" {
		return creation.ID == item.ID
	}
	return creation.Name == item.Name
}

// CreationKind is the lock's `created:` kind for one of the things a desired
// item is a set of: tracker.status_options holds tracker.status_option
// creations. One vocabulary, in the singular for one of them, named rather
// than derived: trimming the last letter made a required status check a
// `forge.required_statu` (issue #204).
func CreationKind(id string) string {
	if kind, ok := creationKinds[id]; ok {
		return kind
	}
	return id
}

// creationKinds names the creation kind of every desired item flow can add
// things to.
var creationKinds = map[string]string{
	core.ItemTrackerStatusOptions: "tracker.status_option",
	core.ItemTrackerLabels:        "tracker.label",
	core.ItemForgeRequiredStatus:  "forge.required_status",
	core.ItemForgeFiles:           "forge.file",
}

// attested reports whether a human's word still stands for this item.
//
// An attestation lapses when the thing it was made about changed: a promise
// about the workflow somebody has since rewritten is a promise about
// something else (spec 03 §3). That judgement is [FindAttestation]'s, and it
// is made here rather than repeated, so that the plan and the report cannot
// disagree about whether one promise still counts.
func (in PlanInput) attested(id, inputs string) bool {
	return FindAttestation(in.Lock, id, inputs, in.ConfigHash, in.Now).Valid
}

// followUpOwner routes a follow-up to whoever can actually act on it: the
// tracker's administrator for a board, the repository's for a forge setting,
// the platform for a deployment (spec 07 §4).
func followUpOwner(cfg *config.Config, target string) string {
	owners := cfg.FollowUps.Owners
	var owner config.Pending
	switch target {
	case TargetTracker:
		owner = owners.TrackerAdmin
	case TargetForge:
		owner = owners.ForgeAdmin
	case TargetDeploys:
		owner = owners.Platform
	}
	if owner.IsZero() {
		owner = owners.Default
	}
	return owner.String()
}

// Find returns the planned item with that id.
func (p Plan) Find(id string) (PlanItem, bool) {
	for _, item := range p.Items {
		if item.ID == id {
			return item, true
		}
	}
	return PlanItem{}, false
}

// IDs returns the ids of every planned item, in dependency order.
func (p Plan) IDs() []string {
	ids := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// Count returns how many items came out with that outcome.
func (p Plan) Count(outcome Outcome) int {
	n := 0
	for _, item := range p.Items {
		if item.Outcome == outcome {
			n++
		}
	}
	return n
}

// Changes returns every change the plan would make, in order.
func (p Plan) Changes() []core.Change {
	var changes []core.Change
	for _, item := range p.Items {
		changes = append(changes, item.Changes...)
	}
	return changes
}

// ExitCode is what `flow plan` exits with: 0 when there is nothing to do, 2
// when something is pending, 1 when the provider cannot be made to do what
// the configuration asks (spec 02 §2, spec 05 §4).
//
// Pending is spec 02 §2's three: a change, a follow-up, an attestation. A
// compensated item is none of them — the intent is enforced, by something
// other than what the configuration named — so it counts as settled, which
// is also what doctor says about it. A workflow whose every item is noop or
// compensated would otherwise exit 2 for ever, and a command that can never
// report success is a command nobody can act on.
func (p Plan) ExitCode() int {
	code := 0
	for _, item := range p.Items {
		switch item.Outcome {
		case OutcomeUnsupported:
			return 1
		case OutcomeNoop, OutcomeCompensated:
		default:
			code = 2
		}
	}
	return code
}

// StaleAgainst reports why this plan may no longer be applied against the
// world fresh describes, and whether it is stale at all.
//
// The three hashes answer three different questions: the configuration is
// what the team asked for, the desired state is what flow made of it, and
// each observation is what the provider held. A plan is a promise about all
// three, and applying it once any of them has moved would make a change
// nobody approved (spec 06 §2–3).
func (p Plan) StaleAgainst(fresh Plan) (string, bool) {
	switch {
	case p.Schema != fresh.Schema:
		return fmt.Sprintf("the plan is %s and this build writes %s", p.Schema, fresh.Schema), true
	case p.ConfigHash != fresh.ConfigHash:
		return "the configuration changed since the plan was made", true
	case p.DesiredHash != fresh.DesiredHash:
		return "what the configuration asks for changed since the plan was made", true
	}
	for _, item := range p.Items {
		now, ok := fresh.Find(item.ID)
		if !ok {
			return fmt.Sprintf("%s is no longer part of the plan", item.ID), true
		}
		if now.ObservedHash != item.ObservedHash {
			return fmt.Sprintf("%s changed at the provider since the plan was made", item.ID), true
		}
	}
	return "", false
}

// JSON renders the plan as `flow.plan/v1`, which is what `--out` writes and
// what `flow apply --plan` reads back.
func (p Plan) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the plan: %w", err)
	}
	return append(data, '\n'), nil
}

// ParsePlan decodes a plan written by [Plan.JSON]. A document that does not
// declare [PlanVersion] is refused rather than read as best it can be: the
// whole point of the file is that apply acts on exactly what was approved.
func ParsePlan(data []byte) (Plan, error) {
	var plan Plan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("read the plan: %w", err)
	}
	if plan.Schema != PlanVersion {
		return Plan{}, fmt.Errorf("read the plan: it declares %q, and this build understands %q", plan.Schema, PlanVersion)
	}
	return plan, nil
}
