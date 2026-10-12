package engine_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// What `flow attest` is for, and what it must never become (spec 02 §2, spec
// 07 §8). The tests below are mostly about the refusals: recording a promise
// is three lines, and every way the mechanism fails is a promise that should
// not have been accepted.

// attestPlan is a workflow with one of each outcome in it, which is what
// decides whether a check may be attested at all.
func attestPlan() engine.Plan {
	return engine.Plan{
		Schema:     engine.PlanVersion,
		ConfigHash: "sha256:config",
		Items: []engine.PlanItem{
			{
				ID:          "tracker.native.pr_merged",
				Target:      "tracker",
				Outcome:     engine.OutcomeAttest,
				Why:         "this board moves cards itself and will not say so",
				DesiredHash: "sha256:merged",
			},
			{
				ID:          "tracker.expedite",
				Target:      "tracker",
				Outcome:     engine.OutcomeAttest,
				Why:         "nobody can be asked about the expedite lane",
				DesiredHash: "sha256:expedite",
			},
			{
				ID:          "forge.protection",
				Target:      "forge",
				Outcome:     engine.OutcomeNative,
				Why:         "the ruleset does not match",
				DesiredHash: "sha256:protection",
			},
			{
				ID:          "deploys.records",
				Target:      "deploys",
				Outcome:     engine.OutcomeFollowUp,
				Why:         "flow may not enable this",
				DesiredHash: "sha256:deploys",
			},
		},
	}
}

// attestInput is a well-formed run against that plan.
func attestInput(lock *config.Lock) engine.RecordInput {
	return engine.RecordInput{
		Check: "tracker.native.pr_merged",
		By:    "@17twenty",
		Note:  "watched a PR merge and the card move to Done",
		TTL:   30 * 24 * time.Hour,
		Plan:  attestPlan(),
		Lock:  lock,
		Now:   coretest.ContractNow,
	}
}

// A promise about a check flow cannot verify is recorded whole: who, what,
// when it dies, and which version of the thing it covered.
func TestAttestingWritesWhoSaidSoAndWhatTheySaw(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	attested, err := engine.RunAttest(attestInput(lock))
	if err != nil {
		t.Fatalf("attest refused a check nobody can be asked about: %v", err)
	}

	if len(lock.Attestations) != 1 {
		t.Fatalf("the lock holds %d attestations, want one", len(lock.Attestations))
	}
	record := lock.Attestations[0]
	if record.Check != "tracker.native.pr_merged" || record.By != "@17twenty" {
		t.Errorf("the lock records %+v, want the check and the person", record)
	}
	if record.Inputs != "sha256:merged" {
		t.Errorf("inputs = %q, want the hash of the item it covered: it cannot retire itself without one", record.Inputs)
	}
	if want := coretest.ContractNow.Add(30 * 24 * time.Hour); !record.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v", record.ExpiresAt, want)
	}
	if attested.Why != "this board moves cards itself and will not say so" {
		t.Errorf("why = %q, want what the plan says about the check", attested.Why)
	}
	if attested.TTL != 30*24*time.Hour {
		t.Errorf("ttl = %v, want how long it was asked to stand", attested.TTL)
	}
	if attested.ReplacedBy != "" {
		t.Errorf("it claims to have replaced %q, and there was nothing there", attested.ReplacedBy)
	}

	// The point of writing it is that the next run reads it as settling the
	// check rather than asking again.
	judged := engine.FindAttestation(lock, record.Check, "sha256:merged", "sha256:config", coretest.ContractNow)
	if !judged.Valid {
		t.Errorf("the promise flow just wrote does not stand: %+v", judged)
	}
}

// The gate: a check the provider answers is never attestable, because an
// answer is better evidence than a promise. Both refusals are usage errors —
// exit 64 — because both are settled by typing something else.
func TestAttestingRefusesWhatTheProviderCanBeAsked(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		check string
		fix   string
	}{
		{"native", "forge.protection", "flow apply"},
		{"follow-up", "deploys.records", "flow doctor --fix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lock := config.NewLock("v0.3.0")
			in := attestInput(lock)
			in.Check = tc.check

			_, err := engine.RunAttest(in)
			if err == nil {
				t.Fatalf("attest accepted %s, which flow checks itself", tc.check)
			}
			if code := core.ExitCode(err); code != core.ExitUsage {
				t.Errorf("exit %d, want %d (spec 02 §2)", code, core.ExitUsage)
			}
			var user *core.UserError
			if !errors.As(err, &user) {
				t.Fatalf("error is not a user error: %v", err)
			}
			if !strings.Contains(user.Fix, tc.fix) {
				t.Errorf("fix = %q, want it to name %s: a refusal that does not say what to do instead is a dead end", user.Fix, tc.fix)
			}
			if len(lock.Attestations) != 0 {
				t.Errorf("a refused attestation was written down anyway: %+v", lock.Attestations)
			}
		})
	}
}

// An id this workflow does not have is a typo, and the fix line is the ids it
// does have — a shorter answer than the whole report.
func TestAttestingAnUnknownCheckSaysWhichOnesThereAre(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	in := attestInput(lock)
	in.Check = "tracker.native.pr_merge"

	_, err := engine.RunAttest(in)
	if err == nil {
		t.Fatal("attest accepted a check that does not exist")
	}
	if code := core.ExitCode(err); code != core.ExitUsage {
		t.Errorf("exit %d, want %d", code, core.ExitUsage)
	}
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("error is not a user error: %v", err)
	}
	for _, want := range []string{"tracker.expedite", "tracker.native.pr_merged"} {
		if !strings.Contains(user.Fix, want) {
			t.Errorf("fix = %q, want it to offer %s", user.Fix, want)
		}
	}
	if strings.Contains(user.Fix, "forge.protection") {
		t.Errorf("fix = %q, want it to offer only what can be attested", user.Fix)
	}
}

// A promise nobody signed, or one that does not say what was looked at, is
// not a record. Both are refused before anything is written.
func TestAnUnsignedOrSilentPromiseIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		by    string
		note  string
		wants string
	}{
		{"nobody vouched", "   ", "I checked", "--by"},
		{"nothing was said", "@17twenty", "  ", "--note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lock := config.NewLock("v0.3.0")
			in := attestInput(lock)
			in.By, in.Note = tc.by, tc.note

			_, err := engine.RunAttest(in)
			if err == nil {
				t.Fatal("attest recorded a promise that says nothing")
			}
			if code := core.ExitCode(err); code != core.ExitUsage {
				t.Errorf("exit %d, want %d", code, core.ExitUsage)
			}
			var user *core.UserError
			if !errors.As(err, &user) || !strings.Contains(user.Fix, tc.wants) {
				t.Errorf("fix does not name %s: %v", tc.wants, err)
			}
			if len(lock.Attestations) != 0 {
				t.Errorf("it was written anyway: %+v", lock.Attestations)
			}
		})
	}
}

// One check has one answer, so a second person vouching takes the first's
// place — and the run says whose word it displaced, because quietly
// overwriting somebody else's promise is how this stops being a record.
func TestASecondWordReplacesTheFirstAndSaysSo(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	first := attestInput(lock)
	if _, err := engine.RunAttest(first); err != nil {
		t.Fatalf("the first attestation was refused: %v", err)
	}

	second := attestInput(lock)
	second.By = "@priya"
	second.Note = "looked again after the board was rebuilt"
	second.Now = coretest.ContractNow.Add(24 * time.Hour)

	attested, err := engine.RunAttest(second)
	if err != nil {
		t.Fatalf("the second attestation was refused: %v", err)
	}
	if len(lock.Attestations) != 1 {
		t.Fatalf("the lock holds %d attestations for one check, want one", len(lock.Attestations))
	}
	if lock.Attestations[0].By != "@priya" {
		t.Errorf("by = %q, want the most recent word", lock.Attestations[0].By)
	}
	if attested.ReplacedBy != "@17twenty" {
		t.Errorf("replaced = %q, want the promise it displaced", attested.ReplacedBy)
	}
	if !attested.ReplacedStood {
		t.Error("it reports displacing a lapsed promise; the one it displaced was still valid")
	}
	if !attested.ReplacedAt.Equal(coretest.ContractNow) {
		t.Errorf("replaced at %v, want when the first was made", attested.ReplacedAt)
	}
	if !strings.Contains(attested.Text(), "@17twenty") {
		t.Errorf("the rendering does not say whose word was replaced:\n%s", attested.Text())
	}
}

// Taking the place of a promise that had already lapsed is not the same event,
// and the run must not describe it as though somebody's good word was
// overruled.
func TestReplacingALapsedPromiseIsNotTheSameAsOverrulingOne(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	first := attestInput(lock)
	if _, err := engine.RunAttest(first); err != nil {
		t.Fatalf("the first attestation was refused: %v", err)
	}

	second := attestInput(lock)
	second.By = "@priya"
	second.Now = coretest.ContractNow.Add(90 * 24 * time.Hour)

	attested, err := engine.RunAttest(second)
	if err != nil {
		t.Fatalf("the second attestation was refused: %v", err)
	}
	if attested.ReplacedBy != "@17twenty" {
		t.Errorf("replaced = %q, want the promise it displaced", attested.ReplacedBy)
	}
	if attested.ReplacedStood {
		t.Error("it reports overruling a promise that stood; that one had expired a month earlier")
	}
	if text := attested.Text(); !strings.Contains(text, "lapsed") {
		t.Errorf("the rendering does not say the promise it replaced had lapsed:\n%s", text)
	}
}

// The default is what the engine says it is, so that the number doctor prints
// when it asks for an attestation is the number attest writes (spec 02 §2,
// spec 03 §2).
func TestHowLongAPromiseStands(t *testing.T) {
	t.Parallel()

	if got := engine.AttestationTTL(nil); got != engine.DefaultAttestationTTL {
		t.Errorf("with no configuration at all the ttl is %v, want %v", got, engine.DefaultAttestationTTL)
	}
	if got := engine.AttestationTTL(&config.Config{}); got != engine.DefaultAttestationTTL {
		t.Errorf("with nothing agreed the ttl is %v, want %v", got, engine.DefaultAttestationTTL)
	}
	if want := 30 * 24 * time.Hour; engine.DefaultAttestationTTL != want {
		t.Errorf("the default is %v, want %v (spec 02 §2)", engine.DefaultAttestationTTL, want)
	}

	agreed := &config.Config{}
	agreed.Attestation.DefaultTTL = config.Duration(14 * 24 * time.Hour)
	if got := engine.AttestationTTL(agreed); got != 14*24*time.Hour {
		t.Errorf("ttl = %v, want what the team agreed: a setting the code ignores is a comment", got)
	}
}

// The --json document is one object carrying its schema (spec 02 §3).
func TestTheAttestationDocumentSaysWhatItIs(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	if _, err := engine.RunAttest(attestInput(lock)); err != nil {
		t.Fatalf("the first attestation was refused: %v", err)
	}
	second := attestInput(lock)
	second.By = "@priya"
	attested, err := engine.RunAttest(second)
	if err != nil {
		t.Fatalf("attest refused: %v", err)
	}

	data, err := attested.JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc struct {
		Schema    string    `json:"schema"`
		Check     string    `json:"check"`
		By        string    `json:"by"`
		Note      string    `json:"note"`
		ExpiresAt time.Time `json:"expires_at"`
		Expires   string    `json:"expires_in"`
		Inputs    string    `json:"inputs"`
		Replaced  *struct {
			By    string `json:"by"`
			Stood bool   `json:"stood"`
		} `json:"replaced"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the document is not one JSON object (%v):\n%s", err, data)
	}
	if doc.Schema != engine.AttestVersion {
		t.Errorf("schema = %q, want %q", doc.Schema, engine.AttestVersion)
	}
	if doc.Check != "tracker.native.pr_merged" || doc.By != "@priya" || doc.Inputs != "sha256:merged" {
		t.Errorf("the document does not say what was promised: %+v", doc)
	}
	if doc.Expires != "30d" {
		t.Errorf("expires_in = %q, want it written as a person would write it", doc.Expires)
	}
	if doc.Replaced == nil || doc.Replaced.By != "@17twenty" || !doc.Replaced.Stood {
		t.Errorf("the document does not record the promise it displaced: %+v", doc.Replaced)
	}
}
