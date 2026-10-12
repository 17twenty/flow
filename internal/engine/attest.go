package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Attesting (spec 02 §2, spec 07 §8) is one person saying "I looked, and it is
// true" about something no API will answer for. It is the smallest thing flow
// writes and the easiest one to make worthless, so the whole of this file is
// about the two questions that decide whether the record is worth anything:
// may this check be attested at all, and does the promise say who made it and
// what they saw.
//
// The plan is what answers the first. A check whose plan item resolves to
// [OutcomeAttest] is one the provider cannot be asked about (spec 05 §4); any
// other outcome means the answer is available, and an answer is better
// evidence than a promise. Deciding it here, from the same plan doctor reads,
// is what keeps `flow attest` and the report that asked for it talking about
// the same thing.

// AttestVersion is the schema `flow attest --json` declares (spec 02 §3).
const AttestVersion = "flow.attest/v1"

// RecordInput is everything one run of `flow attest` needs.
type RecordInput struct {
	// Check is the id being attested, which is the doctor check's and the
	// plan item's (spec 07 §8).
	Check string
	// By is who vouched for it.
	By string
	// Note is what they say they checked, and how.
	Note string
	// TTL is how long the promise stands. Zero is
	// [DefaultAttestationTTL].
	TTL time.Duration
	// Plan is what flow would do about this workflow, which is where the
	// answer to "can this be attested" comes from.
	Plan Plan
	// Lock is where the promise is written. It is modified in place; the
	// caller saves it.
	Lock *config.Lock
	// Now is the moment it was made.
	Now time.Time
}

// Attested is what one run recorded.
type Attested struct {
	// Check is what was attested.
	Check string
	// Why is what the plan says about that check, which is the thing the
	// person is vouching for.
	Why string
	// By, Note, At and ExpiresAt are the promise.
	By        string
	Note      string
	At        time.Time
	ExpiresAt time.Time
	// TTL is how long it stands, as a person would write it.
	TTL time.Duration
	// Inputs is the canonical hash of the desired item it covers, which is
	// what retires it when that item is rewritten (spec 07 §8).
	Inputs string
	// ReplacedBy and ReplacedAt are whoever vouched for this check before,
	// empty when nobody had. One check has one attestation, so a second
	// person's word takes the first's place rather than standing beside it.
	ReplacedBy string
	ReplacedAt time.Time
	// ReplacedStood reports that the promise this one displaced was still
	// valid, as opposed to expired or lapsed.
	ReplacedStood bool
}

// RunAttest records a human's word about a check flow cannot verify, and
// returns what it wrote. The lock is modified in place and not saved: writing
// it is the caller's, because the caller knows where it lives.
func RunAttest(in RecordInput) (Attested, error) {
	item, err := attestable(in.Plan, in.Check)
	if err != nil {
		return Attested{}, err
	}
	by := strings.TrimSpace(in.By)
	if by == "" {
		return Attested{}, core.Errorf(core.ErrValidation, "record who vouched for %s", item.ID).
			WithWhy("an attestation nobody signed is not a record, and flow does not know your handle").
			WithFix("pass --by <handle>, or set handle in your user config")
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		return Attested{}, core.Errorf(core.ErrValidation, "record what %s vouched for", by).
			WithWhy("--note is empty, so the record would not say what was checked").
			WithFix("pass --note with what you looked at and how")
	}

	record := NewAttestation(item.ID, by, note, item.DesiredHash, in.Now, in.TTL)
	attested := Attested{
		Check:     record.Check,
		Why:       item.Why,
		By:        record.By,
		Note:      record.Note,
		At:        record.At,
		ExpiresAt: record.ExpiresAt,
		TTL:       record.ExpiresAt.Sub(record.At),
		Inputs:    record.Inputs,
	}
	if previous := FindAttestation(in.Lock, item.ID, item.DesiredHash, in.Plan.ConfigHash, in.Now); previous.Found {
		attested.ReplacedBy = previous.Record.By
		attested.ReplacedAt = previous.Record.At
		attested.ReplacedStood = previous.Valid
	}
	RecordAttestation(in.Lock, record)
	return attested, nil
}

// attestable is the gate: the plan item this check is about, if the provider
// is one nobody can ask.
//
// Both refusals are usage errors rather than failures, because both are
// answered by typing something else: a different id, or a different command.
func attestable(plan Plan, id string) (PlanItem, error) {
	item, found := plan.Find(id)
	if !found {
		return PlanItem{}, core.Errorf(core.ErrValidation, "attest %s", id).
			WithWhy("this workflow has nothing called %s", id).
			WithFix("%s", attestableIDs(plan))
	}
	if item.Outcome != OutcomeAttest {
		return PlanItem{}, core.Errorf(core.ErrValidation, "attest %s", id).
			WithWhy("flow checks %s against the provider, and an answer is better evidence than a promise", id).
			WithFix("%s", insteadOf(item))
	}
	return item, nil
}

// insteadOf is what to do about a check that cannot be attested, which
// depends on what flow would do about it instead.
func insteadOf(item PlanItem) string {
	switch item.Outcome {
	case OutcomeNoop, OutcomeCompensated:
		return "it already holds — there is nothing to vouch for"
	case OutcomeNative:
		return "run flow apply to make it true"
	case OutcomeFollowUp:
		return "run flow doctor --fix to file it for somebody who has the access"
	case OutcomeUnsupported:
		return "nothing can be done about it here, and nothing needs to be"
	default:
		return "run flow doctor to see what it says"
	}
}

// attestableIDs is the fix line for an id flow does not have: the ones it
// does, which is a shorter answer than the whole report.
func attestableIDs(plan Plan) string {
	var ids []string
	for _, item := range plan.Items {
		if item.Outcome == OutcomeAttest {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return "nothing in this workflow is waiting on a human's word; run flow doctor to see what it is waiting on"
	}
	slices.Sort(ids)
	return "flow attest takes one of: " + strings.Join(ids, ", ")
}

// attestedDoc is the --json document, schema flow.attest/v1.
type attestedDoc struct {
	Schema    string    `json:"schema"`
	Check     string    `json:"check"`
	Why       string    `json:"why,omitempty"`
	By        string    `json:"by"`
	Note      string    `json:"note"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expires_at"`
	Expires   string    `json:"expires_in"`
	Inputs    string    `json:"inputs,omitempty"`
	Replaced  *replaced `json:"replaced,omitempty"`
}

// replaced is the promise this one took the place of.
type replaced struct {
	By    string    `json:"by"`
	At    time.Time `json:"at"`
	Stood bool      `json:"stood"`
}

// JSON renders the run's single document (spec 02 §3).
func (a Attested) JSON() ([]byte, error) {
	doc := attestedDoc{
		Schema:    AttestVersion,
		Check:     a.Check,
		Why:       a.Why,
		By:        a.By,
		Note:      a.Note,
		At:        a.At.UTC(),
		ExpiresAt: a.ExpiresAt.UTC(),
		Expires:   config.Duration(a.TTL).String(),
		Inputs:    a.Inputs,
	}
	if a.ReplacedBy != "" {
		doc.Replaced = &replaced{By: a.ReplacedBy, At: a.ReplacedAt.UTC(), Stood: a.ReplacedStood}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the attestation: %w", err)
	}
	return append(data, '\n'), nil
}

// Text renders what was recorded for a terminal: what it covers, who said so,
// and when flow will ask again.
func (a Attested) Text() string {
	var out strings.Builder
	fmt.Fprintf(&out, "attested %s\n", a.Check)
	if a.Why != "" {
		fmt.Fprintf(&out, "· %s\n", a.Why)
	}
	fmt.Fprintf(&out, "· %s vouched for it %s\n", a.By, humanTime(a.At))
	fmt.Fprintf(&out, "· %s\n", strconv.Quote(a.Note))
	if a.ReplacedBy != "" {
		stood := "which had already lapsed"
		if a.ReplacedStood {
			stood = "which still stood"
		}
		fmt.Fprintf(&out, "· replaces what %s said %s, %s\n", a.ReplacedBy, humanTime(a.ReplacedAt), stood)
	}
	fmt.Fprintf(&out, "flow will ask again %s, in %s\n", humanTime(a.ExpiresAt), config.Duration(a.TTL).String())
	return out.String()
}
