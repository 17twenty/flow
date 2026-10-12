package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Placeholder resolution (spec 07 §3).
//
// A placeholder is the one thing `.flow.yaml` is allowed to not know:
// `${pending:project}` says "nobody can write this down until somebody has
// looked at the tracker". Doctor is what looks, and what it finds goes in the
// lock rather than back into the file a human wrote — the configuration is
// the team's intent, and an identifier the tracker made up is not part of it.
//
// The ladder is spec 07 §3's, in order: what the lock already knows, what the
// user passed on the command line, what the tracker offers when there is only
// one answer, what a human picks, what flow can create, and finally what to
// go and do by hand. Each rung either produces an identifier flow has read
// back from the provider, or hands over to the next.

// How a placeholder came to be resolved, for the lock's story and the line a
// human reads.
const (
	// HowLocked means the lock already had it, and it is still there.
	HowLocked = "locked"
	// HowSet means `--set` gave it.
	HowSet = "set"
	// HowDiscovered means the tracker offered exactly one answer.
	HowDiscovered = "discovered"
	// HowChosen means a human picked it from the candidates.
	HowChosen = "chosen"
	// HowCreated means flow made it.
	HowCreated = "created"
	// HowAsked means flow could not resolve it, and has said what to do.
	HowAsked = "asked"
)

// Placeholder is one value `.flow.yaml` does not know yet (spec 03 §2).
type Placeholder struct {
	// Path is where it sits in the configuration, e.g. tracker.project. It is
	// the key the lock records it under and the key `--set` uses.
	Path string
	// Name is the placeholder's own name, as written in the file.
	Name string
	// Discoverable reports whether this is something a provider can be asked
	// about. A board is; a colleague's handle is not.
	Discoverable bool
}

// Placeholders lists the values a configuration is still missing, in the order
// spec 03 §1.1 allows them. Nothing is reflected over: the five paths that may
// hold a placeholder are a short list, and walking it by hand means a new one
// has to be thought about rather than appearing by accident.
func Placeholders(cfg *config.Config) []Placeholder {
	if cfg == nil {
		return nil
	}
	var pending []Placeholder
	add := func(path string, value config.Pending, discoverable bool) {
		if value.IsPending() {
			pending = append(pending, Placeholder{Path: path, Name: value.Name, Discoverable: discoverable})
		}
	}
	add(core.ItemTrackerProject, cfg.Tracker.Project, true)
	add("followups.owners.default", cfg.FollowUps.Owners.Default, false)
	add("followups.owners.tracker_admin", cfg.FollowUps.Owners.TrackerAdmin, false)
	add("followups.owners.forge_admin", cfg.FollowUps.Owners.ForgeAdmin, false)
	add("followups.owners.platform", cfg.FollowUps.Owners.Platform, false)
	return pending
}

// Chooser is how a human picks between candidates: the picker of spec 12,
// which the engine must not know anything else about. Returning
// [core.ErrNeedsInput] means "not here, not now" — a terminal nobody is
// watching, or a user who pressed escape — and resolution falls through to
// asking rather than failing.
type Chooser func(ctx context.Context, p Placeholder, candidates []core.Candidate) (core.Candidate, error)

// ResolveInput is a round of placeholder resolution.
type ResolveInput struct {
	// Config is the workflow, which is where the placeholders are.
	Config *config.Config
	// Tracker is who to ask. It may be nil, which makes every discoverable
	// placeholder something to ask a human about.
	Tracker core.Tracker
	// Permissions is what the credential turned out to hold, which decides
	// whether flow may create what is missing (spec 05 §4).
	Permissions core.Permissions
	// Lock is what flow already knows, and where an answer is written.
	Lock *config.Lock
	// Set is `--set path=value`, keyed by configuration path.
	Set map[string]string
	// Fix permits flow to act on what it finds: to take the only candidate,
	// to ask the chooser, and to create. Without it resolution reports what
	// it would have done and writes nothing (spec 02 §2).
	Fix bool
	// Choose is the picker. Nil means there is nobody to ask.
	Choose Chooser
	// Log receives one line per resolution.
	Log *slog.Logger
}

// Answer is what became of one placeholder.
type Answer struct {
	// Placeholder is the value that was missing.
	Placeholder Placeholder
	// How it was resolved, or [HowAsked] when it was not.
	How string
	// Ref is the answer, empty when there is none yet.
	Ref core.Ref
	// Candidates are what the tracker offered, for a human to choose from.
	Candidates []core.Candidate
	// Steps are what to do by hand, when flow could not do it.
	Steps []string
	// Err is why resolution could not even be attempted: a provider that
	// would not answer. A placeholder nobody can resolve is not an error —
	// it is a question.
	Err error
}

// Resolved reports whether there is now an answer, verified and recorded.
func (r Answer) Resolved() bool { return r.Ref.ID != "" }

// ResolvePlaceholders walks the ladder for every placeholder in the configuration and
// writes what it verified into the lock.
//
// Nothing unverified is ever recorded. An identifier in the lock is what every
// later run addresses the provider by, and one that was guessed — from a URL
// somebody pasted with a typo, from a board that has since been deleted —
// turns every subsequent check into a mystery about a thing that is not there.
func ResolvePlaceholders(ctx context.Context, in ResolveInput) ([]Answer, error) {
	if in.Lock == nil {
		return nil, fmt.Errorf("%w: placeholder resolution was given no lock to record in", core.ErrValidation)
	}
	if in.Log == nil {
		in.Log = slog.New(slog.DiscardHandler)
	}
	pending := Placeholders(in.Config)
	resolutions := make([]Answer, 0, len(pending))
	for _, placeholder := range pending {
		if err := ctx.Err(); err != nil {
			return resolutions, err
		}
		resolution := resolve(ctx, in, placeholder)
		if resolution.Resolved() {
			in.Lock.Resolve(placeholder.Path, resolution.Ref.ID)
			in.Log.Info("flow resolved a placeholder",
				"path", placeholder.Path, "how", resolution.How, "id", resolution.Ref.ID)
		}
		resolutions = append(resolutions, resolution)
	}
	return resolutions, nil
}

// resolve walks the ladder for one placeholder.
func resolve(ctx context.Context, in ResolveInput, p Placeholder) Answer {
	for _, rung := range []func(context.Context, ResolveInput, Placeholder) (Answer, bool){
		fromLock, fromSet, fromDiscovery, fromCreation,
	} {
		if resolution, took := rung(ctx, in, p); took {
			return resolution
		}
	}
	return Answer{Placeholder: p, How: HowAsked, Steps: steps(in, p)}
}

// fromLock takes what flow already knows, having looked to see it is still
// there. A board that was deleted stops being the answer.
func fromLock(ctx context.Context, in ResolveInput, p Placeholder) (Answer, bool) {
	id := in.Lock.Resolved[p.Path]
	if id == "" {
		return Answer{}, false
	}
	ref := core.Ref{What: p.Path, ID: id}
	if !p.Discoverable {
		return Answer{Placeholder: p, How: HowLocked, Ref: ref}, true
	}
	verified, candidates, err := verify(ctx, in, p, ref)
	switch {
	case err != nil:
		// Not being able to look is not a reason to forget: the lock is what
		// a run with no network starts from, and dropping an identifier
		// because the provider was busy would turn a rate limit into a
		// question for a human.
		return Answer{Placeholder: p, How: HowLocked, Ref: ref, Err: err}, true
	case verified.ID != "":
		return Answer{Placeholder: p, How: HowLocked, Ref: verified}, true
	}
	// It is gone. Fall through to the rest of the ladder, carrying what the
	// tracker does have, so the next rung does not ask twice.
	if len(candidates) == 1 && in.Fix {
		return Answer{Placeholder: p, How: HowDiscovered, Ref: candidates[0].Ref()}, true
	}
	return Answer{}, false
}

// fromSet takes what the user put on the command line, which may be an
// identifier or a URL from the provider's own page (spec 07 §3).
func fromSet(ctx context.Context, in ResolveInput, p Placeholder) (Answer, bool) {
	raw := strings.TrimSpace(in.Set[p.Path])
	if raw == "" {
		return Answer{}, false
	}
	if !p.Discoverable {
		// A colleague's handle is not a thing to read back: there is no API
		// that says whether "@priya" is the right person to ask about board
		// administration. It is taken on the user's word.
		return Answer{Placeholder: p, How: HowSet, Ref: core.Ref{What: p.Path, ID: raw, Name: raw}}, true
	}
	ref := core.Ref{What: p.Path, ID: raw}
	if in.Tracker != nil && looksLikeURL(raw) {
		parsed, err := in.Tracker.ParseRef(raw)
		if err != nil {
			return Answer{Placeholder: p, How: HowAsked, Err: err, Steps: steps(in, p)}, true
		}
		parsed.What = p.Path
		ref = parsed
	}
	verified, _, err := verify(ctx, in, p, ref)
	switch {
	case err != nil:
		return Answer{Placeholder: p, How: HowAsked, Err: err, Steps: steps(in, p)}, true
	case verified.ID == "":
		return Answer{
			Placeholder: p,
			How:         HowAsked,
			Err: core.Errorf(core.ErrNotFound, "resolve %s to %q", p.Path, raw).
				WithWhy("the tracker has nothing by that name or id").
				WithFix("paste the URL from the tracker's own page"),
			Steps: steps(in, p),
		}, true
	}
	return Answer{Placeholder: p, How: HowSet, Ref: verified}, true
}

// fromDiscovery asks the tracker what it has. One answer is taken, but only
// when flow has been asked to act: a read-only `flow doctor` that silently
// picked a board would be writing to the lock without being asked.
func fromDiscovery(ctx context.Context, in ResolveInput, p Placeholder) (Answer, bool) {
	if !p.Discoverable || in.Tracker == nil {
		return Answer{}, false
	}
	candidates, err := in.Tracker.Discover(ctx, p.Path, hintFor(in.Config, p))
	if err != nil {
		return Answer{Placeholder: p, How: HowAsked, Err: err, Steps: steps(in, p)}, true
	}
	switch {
	case len(candidates) == 0:
		return Answer{}, false
	case len(candidates) == 1 && in.Fix:
		return Answer{Placeholder: p, How: HowDiscovered, Ref: refFor(p, candidates[0])}, true
	case in.Fix && in.Choose != nil:
		chosen, err := in.Choose(ctx, p, candidates)
		if err == nil {
			return Answer{Placeholder: p, How: HowChosen, Ref: refFor(p, chosen), Candidates: candidates}, true
		}
		if !isNeedsInput(err) {
			return Answer{Placeholder: p, How: HowAsked, Err: err, Candidates: candidates, Steps: steps(in, p)}, true
		}
	}
	// There is more than one, or nobody to ask, or flow was told only to
	// look. Either way the answer is a question, and the candidates are what
	// makes it answerable.
	return Answer{
		Placeholder: p,
		How:         HowAsked,
		Candidates:  candidates,
		Steps:       chooseSteps(p, candidates),
	}, true
}

// fromCreation makes the thing, when the provider's API can and the credential
// is allowed to. Most trackers cannot: a GitHub project, a Jira project and a
// Linear team are all things an administrator creates, and the declarations
// say so.
func fromCreation(ctx context.Context, in ResolveInput, p Placeholder) (Answer, bool) {
	if !in.Fix || !p.Discoverable || in.Tracker == nil {
		return Answer{}, false
	}
	capability, ok := creatable(in, p)
	if !ok {
		return Answer{}, false
	}
	if !in.Permissions.Allows(capability.Requires) {
		return Answer{}, false
	}
	name := newName(in.Config, p)
	if name == "" {
		return Answer{}, false
	}
	result, err := in.Tracker.Apply(ctx, core.Change{
		What: p.Path,
		Op:   core.ChangeCreate,
		Item: core.Item{Name: name},
	})
	if err != nil {
		return Answer{Placeholder: p, How: HowAsked, Err: err, Steps: steps(in, p)}, true
	}
	return Answer{
		Placeholder: p,
		How:         HowCreated,
		Ref:         core.Ref{What: p.Path, ID: result.Item.ID, Name: result.Item.Name},
	}, true
}

// verify reads the identifier back from the provider before it is written
// down. It returns the reference the provider described, which is richer than
// what was given: the name and the URL come from the tracker rather than from
// whoever typed the id.
func verify(ctx context.Context, in ResolveInput, p Placeholder, ref core.Ref) (core.Ref, []core.Candidate, error) {
	if in.Tracker == nil {
		return core.Ref{}, nil, core.Errorf(core.ErrUnsupported, "verify %s", p.Path).
			WithWhy("flow was given no tracker to ask").
			WithFix("check tracker.type in .flow.yaml")
	}
	candidates, err := in.Tracker.Discover(ctx, p.Path, hintFor(in.Config, p))
	if err != nil {
		return core.Ref{}, nil, err
	}
	for _, candidate := range candidates {
		if matches(candidate, ref) {
			return refFor(p, candidate), candidates, nil
		}
	}
	return core.Ref{}, candidates, nil
}

// matches reports whether a candidate is the thing a reference names. A
// reference carries whichever of the three a human had to hand.
func matches(candidate core.Candidate, ref core.Ref) bool {
	switch {
	case ref.ID != "" && candidate.ID == ref.ID:
		return true
	case ref.URL != "" && candidate.URL == ref.URL:
		return true
	case ref.ID != "" && candidate.Name == ref.ID:
		return true
	default:
		return false
	}
}

// refFor is a candidate as the reference recorded for a placeholder.
func refFor(p Placeholder, candidate core.Candidate) core.Ref {
	ref := candidate.Ref()
	ref.What = p.Path
	return ref
}

// creatable returns the capability that would have to allow creating this,
// and whether it does.
func creatable(in ResolveInput, p Placeholder) (core.Capability, bool) {
	if p.Path != core.ItemTrackerProject {
		return core.Capability{}, false
	}
	capability := in.Tracker.Capabilities().Capability(core.CapProject)
	return capability, capability.Set
}

// newName is what flow would call the thing it creates. A board for a
// repository is named after the repository: flow is not in a position to
// invent a better name, and one that matches the repo is the one a human
// would have typed.
func newName(cfg *config.Config, p Placeholder) string {
	if cfg == nil || p.Path != core.ItemTrackerProject {
		return ""
	}
	_, name, found := strings.Cut(cfg.Forge.Repo, "/")
	if !found {
		return cfg.Forge.Repo
	}
	return name
}

// hintFor is what flow already knows that narrows the search.
func hintFor(cfg *config.Config, p Placeholder) map[string]string {
	if cfg == nil || p.Path != core.ItemTrackerProject {
		return nil
	}
	hint := map[string]string{}
	if cfg.Tracker.Owner != "" {
		hint["owner"] = cfg.Tracker.Owner
	}
	if cfg.Tracker.OwnerType != "" {
		hint["owner_type"] = cfg.Tracker.OwnerType
	}
	if len(hint) == 0 {
		return nil
	}
	return hint
}

// steps is what to do by hand when flow could not resolve a placeholder.
func steps(in ResolveInput, p Placeholder) []string {
	if !p.Discoverable {
		return []string{
			fmt.Sprintf("decide who should receive follow-ups for %s", strings.TrimPrefix(p.Path, "followups.owners.")),
			fmt.Sprintf("run flow doctor --fix --set %s=<their handle>", p.Path),
		}
	}
	kind := "the tracker"
	if in.Tracker != nil {
		kind = in.Tracker.Kind()
	}
	return []string{
		fmt.Sprintf("open %s and find the board this repository's work belongs on", kind),
		fmt.Sprintf("run flow doctor --fix --set %s=<the board's URL>", p.Path),
		"or create the board first, if there is not one yet",
	}
}

// chooseSteps is what to do when the tracker offered answers and flow was not
// in a position to take one.
//
// One candidate is not a choice. Flow would have taken it and the only thing
// missing is leave to write it down, so asking somebody to "pick the one"
// puts a decision in front of them that has already been made — and tells
// them to type an id flow is holding.
func chooseSteps(p Placeholder, candidates []core.Candidate) []string {
	if len(candidates) == 1 {
		return []string{
			fmt.Sprintf("%s has one possible answer, and flow would take it:", p.Path),
			"  " + describe(candidates[0]),
			"run flow doctor --fix to write it down",
		}
	}
	chosen := make([]string, 0, len(candidates)+1)
	chosen = append(chosen, fmt.Sprintf("%s has %d answers; pick the one this repository's work belongs on:", p.Path, len(candidates)))
	for _, candidate := range candidates {
		chosen = append(chosen, "  "+describe(candidate))
	}
	return append(chosen, fmt.Sprintf("run flow doctor --fix --set %s=<id or URL>", p.Path))
}

// describe is one candidate on one line.
func describe(candidate core.Candidate) string {
	parts := []string{candidate.Name}
	if candidate.Name == "" {
		parts = []string{candidate.ID}
	}
	if candidate.Detail != "" {
		parts = append(parts, candidate.Detail)
	}
	if candidate.URL != "" {
		parts = append(parts, candidate.URL)
	}
	return strings.Join(parts, " — ")
}

// looksLikeURL reports whether a value is an address rather than an
// identifier. Only a scheme counts: a Jira project key and a Linear team key
// are bare words, and a GitHub project is a number.
func looksLikeURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

// isNeedsInput reports whether an error means "there is nobody to ask here",
// which is a fall-through rather than a failure.
func isNeedsInput(err error) bool { return errors.Is(err, core.ErrNeedsInput) }

// Result turns a resolution into the answer a doctor check gives (spec 07 §1).
func (r Answer) Result() Result {
	switch {
	case r.Err != nil && !r.Resolved():
		return Failed(r.Err)
	case r.Resolved():
		return Pass("%s is %s", r.Placeholder.Path, r.describe()).
			Fix(Remediation{Kind: RemedyNone})
	}
	remediation := Remediation{
		Kind:    RemedyInput,
		Summary: "tell flow which one it is",
		Command: "flow doctor --fix --set " + r.Placeholder.Path + "=<value>",
		Steps:   r.Steps,
	}
	if len(r.Candidates) == 1 {
		// Nothing is being asked of anybody: flow found one answer and was
		// only told to look. The remedy is leave to write it down.
		remediation.Summary = "let flow write down the only answer there is"
		remediation.Command = "flow doctor --fix"
	}
	result := Result{
		State:       StateInput,
		Message:     fmt.Sprintf("flow does not know what %s is", r.Placeholder.Path),
		Remediation: remediation,
	}
	for _, candidate := range r.Candidates {
		result = result.Saw(describe(candidate), candidate.URL)
	}
	return result
}

// describe is the resolved value as a person reads it.
func (r Answer) describe() string {
	if r.Ref.Name != "" && r.Ref.Name != r.Ref.ID {
		return fmt.Sprintf("%s (%s)", r.Ref.Name, r.Ref.ID)
	}
	return r.Ref.ID
}

// Pending returns the resolutions that still need a human.
func Pending(resolutions []Answer) []Answer {
	return slices.DeleteFunc(slices.Clone(resolutions), func(r Answer) bool { return r.Resolved() })
}

// Resolved reports whether any of these answers is one flow now knows, which
// is what tells a caller its providers were built against a configuration
// that has since gained a value.
func Resolved(resolutions []Answer) bool {
	return slices.ContainsFunc(resolutions, func(r Answer) bool { return r.Resolved() })
}
