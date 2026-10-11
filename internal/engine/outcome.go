// Package engine turns a desired workflow into what flow will do about it:
// the plan, the apply, the doctor checks, the policy rules and the ticket
// transitions. It depends on core, config, commits, git and the adapter
// interfaces — never on a concrete adapter, which the root injects
// (spec 01 §2).
package engine

import (
	"fmt"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// Outcome is what flow will do about one difference between the workflow a
// team asked for and the one their provider is in (spec 05 §4).
type Outcome string

const (
	// OutcomeNoop means there is no difference: the provider already agrees.
	OutcomeNoop Outcome = "noop"
	// OutcomeNative means flow will make the change through the provider's
	// own API.
	OutcomeNative Outcome = "native"
	// OutcomeCompensated means flow cannot make the change, but enforces the
	// same intent by another mechanism it does control.
	OutcomeCompensated Outcome = "compensated"
	// OutcomeFollowUp means flow cannot do it and has filed it for a human.
	OutcomeFollowUp Outcome = "follow_up"
	// OutcomeAttest means only a human can confirm it, with flow attest.
	OutcomeAttest Outcome = "attest"
	// OutcomeUnsupported means nothing can be done about it here.
	OutcomeUnsupported Outcome = "unsupported"
)

// String renders the outcome as the word that appears in JSON and logs.
func (o Outcome) String() string { return string(o) }

// NeedsHuman reports whether the outcome leaves work for a person. Those are
// the outcomes doctor counts towards exit 2 (spec 07 §9).
func (o Outcome) NeedsHuman() bool { return o == OutcomeFollowUp || o == OutcomeAttest }

// Difference is one thing flow wants to be true, one thing the provider says
// is true, and everything needed to decide what happens next.
type Difference struct {
	// Capability is the registry name of the thing in question, such as
	// status_options (spec 05 §2).
	Capability string
	// Subject identifies what the difference is about, for messages and for
	// the attestation id: a column name, a branch, an environment.
	Subject string
	// Declared is the provider's declaration for this capability.
	Declared core.Capability
	// Observed reports whether what the provider has matches what the team
	// asked for. It is only meaningful when the capability can be verified.
	Observed bool
	// Attested reports whether an unexpired attestation covers this subject.
	Attested bool
}

// Resolution is what flow decided, and why, in terms a person can act on.
type Resolution struct {
	// Capability is the registry name the decision is about.
	Capability string `json:"capability"`
	// Subject is what it is about.
	Subject string `json:"subject,omitempty"`
	// Outcome is what flow will do.
	Outcome Outcome `json:"outcome"`
	// Mechanism names what enforces the intent instead, for a compensated
	// outcome: policy.wip, engine.transitions (spec 05 §1).
	Mechanism string `json:"mechanism,omitempty"`
	// Missing are the permission tokens that stopped flow doing it natively,
	// when that is what stopped it.
	Missing []string `json:"missing,omitempty"`
	// Why is one line explaining the outcome.
	Why string `json:"why"`
}

// Resolve decides what flow does about one difference. It is spec 05 §4's
// ladder, in order, and nothing else: the plan is only ever as honest as this
// function is literal.
//
// permissions is what the credential turned out to hold, from the adapter's
// Probe. A zero Permissions means nothing is granted, which is the safe
// reading: flow will not attempt a write it has no evidence it may make.
func Resolve(difference Difference, permissions core.Permissions) Resolution {
	resolution := Resolution{
		Capability: difference.Capability,
		Subject:    difference.Subject,
	}
	declared := difference.Declared

	switch {
	case satisfied(difference):
		resolution.Outcome = OutcomeNoop
		resolution.Why = alreadyTrue(difference)

	case declared.Set && permissions.Allows(declared.Requires):
		resolution.Outcome = OutcomeNative
		resolution.Why = "flow can set it through the provider's API"

	case declared.Remediation == core.RemediationCompensate:
		resolution.Outcome = OutcomeCompensated
		resolution.Mechanism = declared.CompensatedBy
		resolution.Why = fmt.Sprintf("the provider cannot be made to do this, so flow enforces the same intent through %s", declared.CompensatedBy)

	case declared.Remediation == core.RemediationFollowUp:
		resolution.Outcome = OutcomeFollowUp
		resolution.Missing = permissions.Missing(declared.Requires)
		resolution.Why = followUpWhy(declared, resolution.Missing)

	case declared.Remediation == core.RemediationAttest:
		resolution.Outcome = OutcomeAttest
		resolution.Why = "flow cannot see this through the API, so a person has to confirm it"

	default:
		resolution.Outcome = OutcomeUnsupported
		resolution.Missing = permissions.Missing(declared.Requires)
		resolution.Why = "the provider offers no way to do this and nothing else makes up for it"
	}
	return resolution
}

// satisfied reports whether the difference is no difference at all.
//
// A capability flow cannot verify is never taken to be equal on the provider's
// word — there is no word to take. An attestation stands in for the
// observation flow cannot make, and only for that: it never overrides an
// observation that says otherwise (spec 05 §4).
func satisfied(difference Difference) bool {
	if difference.Declared.Verify {
		return difference.Observed
	}
	return difference.Attested
}

// alreadyTrue explains a noop, which is not always the same reason.
func alreadyTrue(difference Difference) string {
	if !difference.Declared.Verify {
		return "flow cannot verify this, and a human has attested that it is true"
	}
	return "the provider already matches what was asked for"
}

// followUpWhy says what a person will have to do, and why flow could not.
func followUpWhy(declared core.Capability, missing []string) string {
	switch {
	case !declared.Set:
		return "the provider has no API for this, so a person has to do it"
	case len(missing) > 0:
		return fmt.Sprintf("the credential is missing %s, so a person with those permissions has to do it", strings.Join(missing, ", "))
	default:
		return "flow cannot do this itself, so a person has to"
	}
}
