package engine

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Drift and attestations (spec 07 §7–8).
//
// These are the two answers doctor gives about things it cannot settle by
// looking once.
//
// Drift is the difference between "this was never right" and "this was right
// and somebody changed it". The second is the interesting one: it means a
// decision the team made has been undone, probably by somebody who did not
// know it was a decision, and it is the only finding that can point at a
// before. Flow can tell them apart because it wrote down what it saw the last
// time the thing held.
//
// An attestation is a human's word about something no API will answer —
// whether a saved view exists, whether a deployment pipeline really gates on
// the branch. It expires, because a promise about a system is only as good as
// the day it was made, and it lapses when the part of the configuration it
// was about changes, because then it is a promise about something else.

// HashShown is how much of a canonical hash goes in front of a human. Twelve
// hex characters is enough to tell two states apart by eye and short enough
// to sit in a line of prose.
const HashShown = 12

// AttestationWindow is how long before an attestation expires that flow
// starts saying so. A week is enough notice to renew it deliberately rather
// than in a hurry on the morning a check turns red.
const AttestationWindow = 7 * 24 * time.Hour

// DriftInput is one check's finding, held up against what flow saw last time.
type DriftInput struct {
	// Check is the check id, which is also the key its observation is
	// recorded under.
	Check string
	// Result is what the check found this run. Its ObservedHash is what makes
	// drift detectable; a check that reports none is passed through.
	Result Result
	// Lock is flow's memory of what the thing looked like when it last held.
	Lock *config.Lock
	// Item is the plan's item for this check, when there is one. Its changes
	// are the "after" a human can act on: what would have to happen to put
	// the thing back.
	Item PlanItem
	// Now is this run's instant.
	Now time.Time
	// Record permits writing what was seen into the lock. A read-only run
	// still reports drift; it just does not update flow's memory.
	Record bool
}

// Drift decides whether a check's finding is drift, and remembers what it saw.
//
// The rule is spec 07 §7's: something that passed before, looks different
// now, and no longer satisfies what the configuration asks, has drifted. A
// change that still satisfies is absorbed silently — a tracker that renamed
// an internal field id, a forge that started returning a list in another
// order. Reporting those would train people to ignore the ones that matter.
func Drift(in DriftInput) Result {
	result := in.Result
	if result.ObservedHash == "" || in.Lock == nil {
		return result
	}
	previous, known := in.Lock.Observed[in.Check]

	switch {
	case !known:
		// Nothing to compare against. A failure here is a thing that was
		// never set up, not a thing somebody broke, and saying "drift" about
		// it would be flow inventing a history it does not have.
		in.remember(result.ObservedHash)
		return result

	case previous.Hash == result.ObservedHash:
		// Unchanged. The timestamp is refreshed only when it has gone stale,
		// because rewriting every date on every run puts a diff in front of a
		// reviewer that says nothing (spec 03 §3).
		if result.State.Satisfies() && in.Now.Sub(previous.At) >= config.ObservationTTL {
			in.remember(result.ObservedHash)
		}
		return result

	case result.State.Satisfies():
		// It looks different and it is still right: cosmetic. Absorb it, so
		// that the next real change is measured from what is there now.
		in.remember(result.ObservedHash)
		return result
	}

	// It was right, it is not now. Flow's memory of the good state is
	// deliberately not updated: if it were, tomorrow's run would see
	// "unchanged" and the drift would be reported once and then forgotten,
	// which is the opposite of what a daily check is for.
	return in.drifted(result, previous)
}

// drifted is the finding, with the before and the after on it.
func (in DriftInput) drifted(result Result, previous config.Observation) Result {
	result.State = StateDrift
	was := humanTime(previous.At)
	result.Message = fmt.Sprintf("%s — this held %s and has changed since", result.Message, was)
	result.Evidence = append(result.Evidence,
		Evidence{Text: fmt.Sprintf("was %s, last confirmed %s", short(previous.Hash), was)},
		Evidence{Text: "now " + short(result.ObservedHash)},
	)
	for _, change := range in.Item.Changes {
		result.Evidence = append(result.Evidence, Evidence{Text: "to put it back: " + describeChange(change)})
	}
	if !result.Remediation.Actionable() {
		result.Remediation = Remediation{
			Kind:    RemedyAuto,
			Summary: "put it back the way the configuration asks",
			Command: "flow doctor --fix",
		}
	}
	return result
}

// remember writes what flow saw into the lock, when it is allowed to.
func (in DriftInput) remember(hash string) {
	if !in.Record || in.Lock == nil || hash == "" {
		return
	}
	in.Lock.Observe(in.Check, hash, in.Now)
}

// describeChange is one mutation in the words of the thing it is about.
func describeChange(change core.Change) string {
	name := change.Item.Name
	if name == "" {
		name = change.Item.ID
	}
	if name == "" {
		return fmt.Sprintf("%s %s", change.Op, change.What)
	}
	return fmt.Sprintf("%s %s %q", change.Op, change.What, name)
}

// short is a hash as a human reads it.
func short(hash string) string {
	hash = strings.TrimPrefix(hash, "sha256:")
	if len(hash) > HashShown {
		return hash[:HashShown]
	}
	return hash
}

// humanTime is a moment in the words a report uses.
func humanTime(at time.Time) string {
	if at.IsZero() {
		return "at some point"
	}
	return "on " + at.UTC().Format("2006-01-02")
}

// AttestInput is one check whose answer only a human can give.
type AttestInput struct {
	// Check is the check being attested. Its id is what the attestation is
	// recorded under, so renaming one throws away every promise about it.
	Check Check
	// Result is what the check found, which for an attested check is usually
	// "flow cannot tell".
	Result Result
	// Lock holds the attestations.
	Lock *config.Lock
	// Inputs is the canonical hash of the part of the configuration this
	// check is about — the desired item's hash. An attestation made against
	// different inputs is a promise about something else.
	Inputs string
	// ConfigHash is the whole configuration's hash, which retires
	// attestations written before flow recorded their inputs.
	ConfigHash string
	// Now is this run's instant.
	Now time.Time
	// TTL is how long a fresh attestation would stand, for the line that asks
	// for one.
	TTL time.Duration
}

// Attestation is what the lock holds about one check, judged against now.
type Attestation struct {
	// Record is the attestation itself, zero when there is none.
	Record config.Attestation
	// Found reports whether there was one at all.
	Found bool
	// Valid reports whether it still stands.
	Valid bool
	// Expired reports that its time ran out.
	Expired bool
	// Expiring reports that it stands but not for much longer.
	Expiring bool
	// Lapsed reports that the configuration it was made against has changed,
	// which retires it however long it had left.
	Lapsed bool
	// Left is how long it has, zero once it has none.
	Left time.Duration
}

// FindAttestation judges what the lock holds about a check.
func FindAttestation(lock *config.Lock, check, inputs, configHash string, now time.Time) Attestation {
	if lock == nil {
		return Attestation{}
	}
	at := slices.IndexFunc(lock.Attestations, func(a config.Attestation) bool { return a.Check == check })
	if at < 0 {
		return Attestation{}
	}
	record := lock.Attestations[at]
	judged := Attestation{Record: record, Found: true}

	switch {
	case record.Inputs != "" && inputs != "" && record.Inputs != inputs:
		// The attestation names the inputs it was about, and they are not
		// these. Somebody rewrote the part of the workflow this promise
		// covered.
		judged.Lapsed = true
	case record.Inputs == "" && configHash != "" && lock.ConfigHash != "" && configHash != lock.ConfigHash:
		// An attestation from before flow recorded what each one covered.
		// There is nothing finer to compare, so it lapses with the whole
		// configuration, which is the old rule and the safe one.
		judged.Lapsed = true
	case !now.Before(record.ExpiresAt):
		judged.Expired = true
	default:
		judged.Valid = true
		judged.Left = record.ExpiresAt.Sub(now)
		judged.Expiring = judged.Left < AttestationWindow
	}
	return judged
}

// Wants reports whether a human's word is what would settle this check: it
// did not hold, and the check itself says looking harder will not help.
//
// The gate matters. A check flow can verify is never attested, however loudly
// somebody vouches for it: if the API answers, the answer is better evidence
// than a promise, and letting a promise win would make `flow attest` a way to
// swear away a real failure.
func (in AttestInput) Wants() bool {
	return !in.Result.State.Satisfies() && in.Result.Remediation.Kind == RemedyAttest
}

// Attest applies a human's word to a check that cannot be settled by looking.
func Attest(in AttestInput) Result {
	result := in.Result
	if !in.Wants() {
		return result
	}
	judged := FindAttestation(in.Lock, in.Check.ID, in.Inputs, in.ConfigHash, in.Now)
	if judged.Valid {
		pass := Pass("%s — attested by %s on %s",
			in.Check.Title, judged.Record.By, judged.Record.At.UTC().Format("2006-01-02"))
		pass.ObservedHash = result.ObservedHash
		if note := strings.TrimSpace(judged.Record.Note); note != "" {
			pass = pass.Saw(note, "")
		}
		if judged.Expiring {
			pass = pass.Saw(fmt.Sprintf("expires in %s — renew it before it does",
				config.Duration(judged.Left.Truncate(time.Hour)).String()), "")
			pass.Remediation = Remediation{
				Kind:    RemedyAttest,
				Summary: "renew the attestation before it expires",
				Command: in.command(),
				Owner:   judged.Record.By,
			}
		}
		return pass
	}

	result.State = StateAttest
	result.Message = in.why(judged)
	result.Remediation = Remediation{
		Kind:    RemedyAttest,
		Summary: "check it yourself and say so",
		Command: in.command(),
		Steps:   in.steps(),
	}
	if judged.Found {
		result.Remediation.Owner = judged.Record.By
	}
	return result
}

// why is the one line explaining what happened to the promise.
func (in AttestInput) why(judged Attestation) string {
	switch {
	case judged.Lapsed:
		return fmt.Sprintf("%s — %s vouched for this, but the configuration it covered has changed since",
			in.Check.Title, judged.Record.By)
	case judged.Expired:
		return fmt.Sprintf("%s — %s vouched for this on %s and it has expired",
			in.Check.Title, judged.Record.By, judged.Record.At.UTC().Format("2006-01-02"))
	default:
		return fmt.Sprintf("%s — flow cannot check this itself and nobody has vouched for it",
			in.Check.Title)
	}
}

// command is the one that settles it.
func (in AttestInput) command() string {
	return fmt.Sprintf("flow attest %s --note %q", in.Check.ID, "what you checked, and how")
}

// steps say what attesting means, because signing something you did not check
// is the one way this mechanism fails.
func (in AttestInput) steps() []string {
	stands := "as long as the attestation policy allows"
	if in.TTL > 0 {
		stands = config.Duration(in.TTL).String()
	}
	return []string{
		"look at it yourself: " + in.Check.Title,
		"if it is not true, make it true first — attesting is not a way to silence it",
		fmt.Sprintf("run flow attest %s --note \"…\"; your word stands for %s", in.Check.ID, stands),
	}
}

// NewAttestation is the record `flow attest` writes. The inputs hash is taken
// now, so that the promise retires by itself when the thing it was about is
// rewritten.
func NewAttestation(check, by, note, inputs string, now time.Time, ttl time.Duration) config.Attestation {
	if ttl <= 0 {
		ttl = DefaultAttestationTTL
	}
	return config.Attestation{
		Check:     check,
		By:        by,
		Note:      strings.TrimSpace(note),
		At:        now,
		ExpiresAt: now.Add(ttl),
		Inputs:    inputs,
	}
}

// DefaultAttestationTTL is how long a human's word stands when the
// configuration does not say. Ninety days is long enough not to be a chore
// and short enough that nobody is relying on something a departed colleague
// checked two years ago.
const DefaultAttestationTTL = 90 * 24 * time.Hour

// RecordAttestation puts an attestation in the lock, replacing whatever stood
// for that check. One check has one answer: a second person vouching for the
// same thing replaces the first rather than stacking up, because two promises
// with different expiry dates is a question about which one counts.
func RecordAttestation(lock *config.Lock, a config.Attestation) {
	if lock == nil {
		return
	}
	for i, existing := range lock.Attestations {
		if existing.Check == a.Check {
			lock.Attestations[i] = a
			return
		}
	}
	lock.Attestations = append(lock.Attestations, a)
}

// DropAttestation removes a check's attestation, which is what `flow attest
// --remove` does and what happens when a check becomes verifiable.
func DropAttestation(lock *config.Lock, check string) bool {
	if lock == nil {
		return false
	}
	at := slices.IndexFunc(lock.Attestations, func(a config.Attestation) bool { return a.Check == check })
	if at < 0 {
		return false
	}
	lock.Attestations = append(lock.Attestations[:at], lock.Attestations[at+1:]...)
	return true
}
