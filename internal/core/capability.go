package core

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Remediation says what flow does when it cannot make a capability true
// itself (spec 05 §1).
const (
	// RemediationNone means nothing is owed: either flow can do it, or the
	// difference does not matter.
	RemediationNone = "none"
	// RemediationFollowUp means flow files a ticket for a human.
	RemediationFollowUp = "follow_up"
	// RemediationCompensate means flow enforces the same intent by another
	// mechanism, named in CompensatedBy.
	RemediationCompensate = "compensate"
	// RemediationAttest means only a human can confirm it, with flow attest.
	RemediationAttest = "attest"
)

// The mechanisms a compensated capability may be compensated by (spec 05 §1).
const (
	// CompensatePolicyWIP enforces a work-in-progress limit in the gate
	// instead of in the tracker.
	CompensatePolicyWIP = "policy.wip"
	// CompensatePolicyExpedite enforces expedited work in the gate.
	CompensatePolicyExpedite = "policy.expedite"
	// CompensatePolicyDoR enforces the definition of ready in the gate.
	CompensatePolicyDoR = "policy.dor"
	// CompensateEngineTransitions performs a transition from flow instead of
	// configuring the tracker to perform it.
	CompensateEngineTransitions = "engine.transitions"
)

// remediations and compensators are the vocabularies above, for validation.
var (
	remediations = []string{RemediationNone, RemediationFollowUp, RemediationCompensate, RemediationAttest}
	compensators = []string{CompensatePolicyWIP, CompensatePolicyExpedite, CompensatePolicyDoR, CompensateEngineTransitions}
)

// Capability is what one provider can do about one thing flow cares about,
// and what flow does when it cannot (spec 05 §1).
type Capability struct {
	// Set is whether the provider's API can make this true.
	Set bool `yaml:"set"`
	// Verify is whether flow can read the current value back. A capability
	// that cannot be verified is never considered equal to the desired state
	// (spec 05 §4).
	Verify bool `yaml:"verify"`
	// Requires are the permission tokens Probe must report before flow will
	// attempt to set it.
	Requires []string `yaml:"requires,omitempty"`
	// Remediation is what flow does when it cannot set it.
	Remediation string `yaml:"remediation"`
	// CompensatedBy names the mechanism that enforces the same intent, and is
	// set exactly when Remediation is compensate.
	CompensatedBy string `yaml:"compensated_by,omitempty"`
	// Hazards are the ways using this API can hurt, for humans to read before
	// they approve a plan.
	Hazards []string `yaml:"hazards,omitempty"`
	// Notes is what the research found, in prose.
	Notes string `yaml:"notes,omitempty"`
}

// Limits is informational: what the provider's rate limiting looks like.
type Limits struct {
	// Rate describes the provider's request budget.
	Rate string `yaml:"rate,omitempty"`
}

// CapabilitySet is one provider's declaration: what it can do, what that
// costs, and when the research was done (spec 05 §1).
type CapabilitySet struct {
	// Adapter is the provider kind as written in .flow.yaml, such as
	// github-projects.
	Adapter string `yaml:"adapter"`
	// Kind is tracker, forge or deploys.
	Kind string `yaml:"kind"`
	// Version is the declaration format version.
	Version int `yaml:"version"`
	// ResearchedAt is the day the declaration was last checked against the
	// provider's documentation, as YYYY-MM-DD.
	ResearchedAt string `yaml:"researched_at"`
	// Sources are the documents the research rests on.
	Sources []string `yaml:"sources"`
	// Limits is informational rate limiting.
	Limits Limits `yaml:"limits,omitempty"`
	// Capabilities is every capability of the kind, keyed by registry name.
	Capabilities map[string]Capability `yaml:"capabilities"`
}

// Capability returns what the provider can do about name. An undeclared
// capability comes back as the zero Capability — cannot set, cannot verify,
// no remediation — which resolves to unsupported rather than to a guess.
func (s CapabilitySet) Capability(name string) Capability { return s.Capabilities[name] }

// Can reports whether the provider's API can set name.
func (s CapabilitySet) Can(name string) bool { return s.Capabilities[name].Set }

// CanVerify reports whether flow can read name back from the provider.
func (s CapabilitySet) CanVerify(name string) bool { return s.Capabilities[name].Verify }

// Names returns the declared capability names in registry order.
func (s CapabilitySet) Names() []string {
	names := CapabilityNames(s.Kind)
	return slices.DeleteFunc(names, func(name string) bool {
		_, ok := s.Capabilities[name]
		return !ok
	})
}

// Researched is the day the declaration was last checked. A declaration that
// failed validation cannot get here, so the date always parses.
func (s CapabilitySet) Researched() time.Time {
	day, _ := time.Parse(time.DateOnly, s.ResearchedAt)
	return day
}

// RequiredTokens is every permission token the declaration ever asks for,
// sorted. It is what a credential would have to hold for nothing here to be
// refused on permissions — which is what spec 05 §6's comparison assumes, so
// that two providers are judged on what they can do rather than on whose
// token happened to be in the environment.
func (s CapabilitySet) RequiredTokens() []string {
	seen := map[string]bool{}
	for _, capability := range s.Capabilities {
		for _, token := range capability.Requires {
			seen[token] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// Registry is every capability declaration flow ships, keyed by adapter.
type Registry map[string]CapabilitySet

// Adapters returns the declared adapter names, sorted.
func (r Registry) Adapters() []string { return slices.Sorted(maps.Keys(r)) }

// Of returns one adapter's declaration. The error names the adapters flow
// does know about, because the usual cause is a typo in .flow.yaml.
func (r Registry) Of(adapter string) (CapabilitySet, error) {
	set, ok := r[adapter]
	if !ok {
		return CapabilitySet{}, Errorf(ErrNotFound, "look up the %q adapter", adapter).
			WithWhy("flow ships no capability declaration for it").
			WithFix("use one of: %s", strings.Join(r.Adapters(), ", "))
	}
	return set, nil
}

// OfKind returns the declarations for one adapter kind, sorted by adapter.
func (r Registry) OfKind(kind string) []CapabilitySet {
	var out []CapabilitySet
	for _, adapter := range r.Adapters() {
		if set := r[adapter]; set.Kind == kind {
			out = append(out, set)
		}
	}
	return out
}

// LoadRegistry reads every *.yaml capability declaration in fsys — in
// production, the embedded assets/capabilities directory. Every declaration
// must be valid: flow's whole account of what it can do rests on these files,
// so a broken one is a broken build, not a degraded run (spec 05 §1).
func LoadRegistry(fsys fs.FS) (Registry, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, Errorf(ErrNotFound, "read the capability declarations").WithWhy("%s", err)
	}

	registry := Registry{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || path.Ext(name) != ".yaml" {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, Errorf(ErrNotFound, "read the capability declaration %s", name).WithWhy("%s", err)
		}
		set, err := ParseCapabilitySet(data, name)
		if err != nil {
			return nil, err
		}
		if existing, ok := registry[set.Adapter]; ok {
			return nil, Errorf(ErrValidation, "load the capability declarations").
				WithWhy("%s and another file both declare the adapter %q", name, existing.Adapter).
				WithFix("give each adapter exactly one declaration")
		}
		registry[set.Adapter] = set
	}
	if len(registry) == 0 {
		return nil, Errorf(ErrNotFound, "load the capability declarations").
			WithWhy("no .yaml declaration was found").
			WithFix("build flow with assets/capabilities populated")
	}
	return registry, nil
}

// ParseCapabilitySet decodes and validates one declaration. name is the file
// it came from, used in messages and required to match the adapter it
// declares, so that a file is always findable from its adapter name.
func ParseCapabilitySet(data []byte, name string) (CapabilitySet, error) {
	var set CapabilitySet
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&set); err != nil {
		return CapabilitySet{}, Errorf(ErrValidation, "read the capability declaration %s", name).
			WithWhy("%s", strings.TrimSpace(err.Error())).
			WithFix("the declaration format is in spec 05 §1")
	}

	if problems := set.validate(name); len(problems) > 0 {
		return CapabilitySet{}, Errorf(ErrValidation, "read the capability declaration %s", name).
			WithWhy("%s", strings.Join(problems, "; ")).
			WithFix("the declaration format is in spec 05 §1, the capability registry in §2")
	}
	return set, nil
}

// validate returns everything wrong with a declaration, so that a file is
// fixed in one pass rather than one problem per build.
func (s CapabilitySet) validate(file string) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch {
	case s.Adapter == "":
		add("no adapter is named")
	case file != "" && s.Adapter+".yaml" != file:
		add("the file is %s but the adapter is %q; name the file after the adapter", file, s.Adapter)
	}
	if !slices.Contains(AdapterKinds(), s.Kind) {
		add("kind is %q; it must be %s", s.Kind, strings.Join(AdapterKinds(), ", "))
	}
	if s.Version != 1 {
		add("version is %d; flow reads version 1", s.Version)
	}
	if _, err := time.Parse(time.DateOnly, s.ResearchedAt); err != nil {
		add("researched_at is %q; it must be a day, as YYYY-MM-DD", s.ResearchedAt)
	}

	known := CapabilityNames(s.Kind)
	for _, name := range slices.Sorted(maps.Keys(s.Capabilities)) {
		if !slices.Contains(known, name) {
			add("%q is not a capability of a %s; the registry is in spec 05 §2", name, s.Kind)
			continue
		}
		for _, problem := range s.Capabilities[name].validate() {
			add("%s %s", name, problem)
		}
	}
	for _, name := range known {
		if _, ok := s.Capabilities[name]; !ok {
			add("%q is not declared; every capability must be researched, even if only to say the provider has no API for it", name)
		}
	}
	return problems
}

// validate returns everything wrong with one capability, phrased to follow its
// name.
func (c Capability) validate() []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !slices.Contains(remediations, c.Remediation) {
		add("has remediation %q; it must be %s", c.Remediation, strings.Join(remediations, ", "))
	}
	switch {
	case c.Remediation == RemediationCompensate && c.CompensatedBy == "":
		add("is compensated but does not say by what; set compensated_by to %s", strings.Join(compensators, ", "))
	case c.Remediation != RemediationCompensate && c.CompensatedBy != "":
		add("sets compensated_by but its remediation is %q; only a compensated capability is compensated by something", c.Remediation)
	case c.CompensatedBy != "" && !slices.Contains(compensators, c.CompensatedBy):
		add("is compensated by %q, which is not a mechanism flow has; use %s", c.CompensatedBy, strings.Join(compensators, ", "))
	}
	if c.Remediation == RemediationAttest && c.Verify {
		add("asks for an attestation but says flow can verify it; spec 05 §4 only lets a promise stand in for an observation flow cannot make, so an attestation of this would never settle anything")
	}
	for _, token := range c.Requires {
		if strings.TrimSpace(token) == "" {
			add("requires an empty permission token")
		}
	}
	return problems
}
