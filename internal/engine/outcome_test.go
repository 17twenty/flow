package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// able is a capability flow could set, given the permissions it names.
func able(requires ...string) core.Capability {
	return core.Capability{Set: true, Verify: true, Requires: requires, Remediation: core.RemediationFollowUp}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		declared    core.Capability
		observed    bool
		attested    bool
		permissions core.Permissions
		want        engine.Outcome
		mechanism   string
		missing     []string
	}{
		{
			name:        "what is already true is left alone",
			declared:    able("admin"),
			observed:    true,
			permissions: core.Permissions{Tokens: []string{"admin"}},
			want:        engine.OutcomeNoop,
		},
		{
			name:        "what flow can set, and may, it sets",
			declared:    able("admin"),
			permissions: core.Permissions{Tokens: []string{"admin", "push"}},
			want:        engine.OutcomeNative,
		},
		{
			name:     "a capability needing nothing is still native",
			declared: core.Capability{Set: true, Verify: true, Remediation: core.RemediationFollowUp},
			want:     engine.OutcomeNative,
		},
		{
			name:        "what flow may not set falls to the remediation",
			declared:    able("admin"),
			permissions: core.Permissions{Tokens: []string{"push"}},
			want:        engine.OutcomeFollowUp,
			missing:     []string{"admin"},
		},
		{
			name: "what the provider cannot do is compensated when something else can",
			declared: core.Capability{
				Verify: false, Remediation: core.RemediationCompensate, CompensatedBy: core.CompensatePolicyWIP,
			},
			want:      engine.OutcomeCompensated,
			mechanism: core.CompensatePolicyWIP,
		},
		{
			name:     "what the provider cannot do and nothing covers goes to a human",
			declared: core.Capability{Verify: true, Remediation: core.RemediationFollowUp},
			want:     engine.OutcomeFollowUp,
		},
		{
			name:     "what flow cannot see is attested",
			declared: core.Capability{Verify: false, Remediation: core.RemediationAttest},
			want:     engine.OutcomeAttest,
		},
		{
			name:     "what nothing can be done about is unsupported",
			declared: core.Capability{Verify: false, Remediation: core.RemediationNone},
			want:     engine.OutcomeUnsupported,
		},
		{
			name:        "compensation is preferred to a follow-up even when permissions are missing",
			declared:    core.Capability{Set: true, Verify: true, Requires: []string{"admin"}, Remediation: core.RemediationCompensate, CompensatedBy: core.CompensateEngineTransitions},
			permissions: core.Permissions{Tokens: []string{"push"}},
			want:        engine.OutcomeCompensated,
			mechanism:   core.CompensateEngineTransitions,
		},
		{
			name:        "an undeclared capability is unsupported, whatever the credential holds",
			declared:    core.Capability{},
			permissions: core.Permissions{Tokens: []string{"admin"}},
			want:        engine.OutcomeUnsupported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			difference := engine.Difference{
				Capability: core.CapStatusOptions,
				Subject:    "In review",
				Declared:   test.declared,
				Observed:   test.observed,
				Attested:   test.attested,
			}

			got := engine.Resolve(difference, test.permissions)
			if got.Outcome != test.want {
				t.Errorf("outcome = %s, want %s (why: %s)", got.Outcome, test.want, got.Why)
			}
			if got.Mechanism != test.mechanism {
				t.Errorf("mechanism = %q, want %q", got.Mechanism, test.mechanism)
			}
			if !slices.Equal(got.Missing, test.missing) {
				t.Errorf("missing = %v, want %v", got.Missing, test.missing)
			}
			if got.Why == "" {
				t.Error("every outcome must say why, or a plan is a list of verdicts")
			}
			if got.Capability != core.CapStatusOptions || got.Subject != "In review" {
				t.Errorf("resolution lost what it was about: %+v", got)
			}
		})
	}
}

// What flow cannot read back is never assumed to be in place: the provider has
// not said so, and nobody else can say so for it except a human (spec 05 §4).
func TestUnverifiableIsNotEqualUntilAttested(t *testing.T) {
	declared := core.Capability{Verify: false, Remediation: core.RemediationAttest}

	unattested := engine.Resolve(engine.Difference{Declared: declared, Observed: true}, core.Permissions{})
	if unattested.Outcome != engine.OutcomeAttest {
		t.Errorf("outcome = %s, want %s: an observation flow could not make is not an observation", unattested.Outcome, engine.OutcomeAttest)
	}

	attested := engine.Resolve(engine.Difference{Declared: declared, Attested: true}, core.Permissions{})
	if attested.Outcome != engine.OutcomeNoop {
		t.Errorf("outcome = %s, want %s: a valid attestation stands in for the observation", attested.Outcome, engine.OutcomeNoop)
	}
	if !strings.Contains(attested.Why, "attested") {
		t.Errorf("why = %q, want it to say the noop rests on a human's word", attested.Why)
	}
}

// An attestation stands in for an observation flow cannot make. It does not
// overrule one it can.
func TestAttestationDoesNotOverruleAnObservation(t *testing.T) {
	declared := core.Capability{Set: true, Verify: true, Requires: []string{"admin"}, Remediation: core.RemediationFollowUp}

	got := engine.Resolve(engine.Difference{Declared: declared, Observed: false, Attested: true}, core.Permissions{Tokens: []string{"admin"}})
	if got.Outcome != engine.OutcomeNative {
		t.Errorf("outcome = %s, want %s: flow looked, and it is not true", got.Outcome, engine.OutcomeNative)
	}
}

func TestOutcomeNeedsHuman(t *testing.T) {
	for outcome, want := range map[engine.Outcome]bool{
		engine.OutcomeNoop:        false,
		engine.OutcomeNative:      false,
		engine.OutcomeCompensated: false,
		engine.OutcomeFollowUp:    true,
		engine.OutcomeAttest:      true,
		engine.OutcomeUnsupported: false,
	} {
		if got := outcome.NeedsHuman(); got != want {
			t.Errorf("%s.NeedsHuman() = %v, want %v", outcome, got, want)
		}
	}
}

// The resolution of a real declaration against a real credential is the thing
// flow's own plan rests on, so it is worth one test that reads like the board.
func TestResolveAgainstTheGitHubProjectsDeclaration(t *testing.T) {
	registry, err := core.LoadRegistry(testCapabilities(t))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	set, err := registry.Of("github-projects")
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	writer := core.Permissions{Tokens: []string{"project_write", "issues_write"}}
	reader := core.Permissions{Tokens: []string{"project_read"}}

	for _, test := range []struct {
		name        string
		capability  string
		permissions core.Permissions
		want        engine.Outcome
	}{
		{"columns a project token may rewrite", core.CapStatusOptions, writer, engine.OutcomeNative},
		{"columns a read-only token may not", core.CapStatusOptions, reader, engine.OutcomeFollowUp},
		{"column limits GitHub keeps per-view", core.CapWIPLimits, writer, engine.OutcomeCompensated},
		{"an automation that can only be watched", core.CapNativeTransitionPRMerged, writer, engine.OutcomeAttest},
		{"an automation GitHub does not have", core.CapNativeTransitionPROpenedDraft, writer, engine.OutcomeUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := engine.Resolve(engine.Difference{
				Capability: test.capability,
				Declared:   set.Capability(test.capability),
			}, test.permissions)
			if got.Outcome != test.want {
				t.Errorf("outcome = %s, want %s (why: %s)", got.Outcome, test.want, got.Why)
			}
		})
	}
}

func TestResolveNamesTheMissingPermission(t *testing.T) {
	declared := core.Capability{Set: true, Verify: true, Requires: []string{"admin", "statuses_write"}, Remediation: core.RemediationFollowUp}

	got := engine.Resolve(engine.Difference{Declared: declared}, core.Permissions{Tokens: []string{"statuses_write"}})
	if !slices.Equal(got.Missing, []string{"admin"}) {
		t.Errorf("missing = %v, want [admin]", got.Missing)
	}
	if !strings.Contains(got.Why, "admin") {
		t.Errorf("why = %q, want it to name the permission that was missing", got.Why)
	}
}
