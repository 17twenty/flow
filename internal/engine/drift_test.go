package engine_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

const (
	hashWas = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	hashNow = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// driftLock is a lock that remembers seeing something in a particular state.
func driftLock(check, hash string, when time.Time) *config.Lock {
	lock := config.NewLock("v0.3.0")
	lock.Observe(check, hash, when)
	return lock
}

// watched is a finding that carries an observed hash, which is what makes
// drift detectable at all.
func watched(result engine.Result, hash string) engine.Result {
	result.ObservedHash = hash
	return result
}

// TestSomethingThatWasRightAndIsNotNowIsDrift is the finding spec 07 §7
// exists for. It is a different fact from "this was never set up": somebody
// undid a decision the team made, probably without knowing it was one.
func TestSomethingThatWasRightAndIsNotNowIsDrift(t *testing.T) {
	t.Parallel()

	was := coretest.ContractNow.Add(-20 * 24 * time.Hour)
	lock := driftLock("tracker.status_options", hashWas, was)

	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.status_options",
		Result: watched(engine.Fail("the board is missing the Ready column"), hashNow),
		Lock:   lock,
		Now:    coretest.ContractNow,
		Record: true,
	})

	if got.State != engine.StateDrift {
		t.Fatalf("state = %q, want %q", got.State, engine.StateDrift)
	}
	if !strings.Contains(got.Message, "has changed since") {
		t.Errorf("message = %q, want it to say this used to hold", got.Message)
	}
	evidence := evidenceText(got)
	if !strings.Contains(evidence, "was 111111111111") {
		t.Errorf("evidence = %q, want the before", evidence)
	}
	if !strings.Contains(evidence, "now 222222222222") {
		t.Errorf("evidence = %q, want the after", evidence)
	}
	if !strings.Contains(evidence, was.UTC().Format("2006-01-02")) {
		t.Errorf("evidence = %q, want when it last held", evidence)
	}
}

// TestDriftDoesNotForgetTheGoodState is the one that makes a daily check
// worth running. If flow recorded the drifted hash, tomorrow's run would see
// "unchanged" and the drift would be reported once and then never again.
func TestDriftDoesNotForgetTheGoodState(t *testing.T) {
	t.Parallel()

	lock := driftLock("tracker.status_options", hashWas, coretest.ContractNow.Add(-20*24*time.Hour))
	in := engine.DriftInput{
		Check:  "tracker.status_options",
		Result: watched(engine.Fail("the board is missing the Ready column"), hashNow),
		Lock:   lock,
		Now:    coretest.ContractNow,
		Record: true,
	}

	if got := engine.Drift(in); got.State != engine.StateDrift {
		t.Fatalf("state = %q, want %q", got.State, engine.StateDrift)
	}
	if lock.Observed["tracker.status_options"].Hash != hashWas {
		t.Fatalf("the lock forgot the good state: %v", lock.Observed["tracker.status_options"])
	}

	in.Now = coretest.ContractNow.Add(24 * time.Hour)
	if got := engine.Drift(in); got.State != engine.StateDrift {
		t.Errorf("state on the second day = %q, want drift again", got.State)
	}
}

// TestACosmeticChangeIsAbsorbedSilently: a tracker that renamed an internal
// field id, a forge that started returning a list in another order. The thing
// the configuration asks for is still true, and reporting the churn would
// train people to ignore the findings that matter.
func TestACosmeticChangeIsAbsorbedSilently(t *testing.T) {
	t.Parallel()

	lock := driftLock("tracker.labels", hashWas, coretest.ContractNow.Add(-20*24*time.Hour))
	before := engine.Pass("every label the workflow uses is on the board")

	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.labels",
		Result: watched(before, hashNow),
		Lock:   lock,
		Now:    coretest.ContractNow,
		Record: true,
	})

	if got.State != engine.StatePass {
		t.Fatalf("state = %q, want the pass to stand", got.State)
	}
	if len(got.Evidence) != 0 {
		t.Errorf("evidence = %v, want silence", got.Evidence)
	}
	if lock.Observed["tracker.labels"].Hash != hashNow {
		t.Errorf("lock hash = %q, want the new shape absorbed so the next change is measured from it",
			lock.Observed["tracker.labels"].Hash)
	}
}

// TestAThingThatWasNeverRightIsNotDrift: flow will not invent a history it
// does not have. With nothing recorded there is no "before" to point at, and
// calling a fresh failure drift would be a lie about what happened.
func TestAThingThatWasNeverRightIsNotDrift(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.status_options",
		Result: watched(engine.Fail("the board is missing the Ready column"), hashNow),
		Lock:   lock,
		Now:    coretest.ContractNow,
		Record: true,
	})

	if got.State != engine.StateFail {
		t.Errorf("state = %q, want the failure as found", got.State)
	}
	if lock.Observed["tracker.status_options"].Hash != hashNow {
		t.Errorf("lock = %v, want the first sight remembered", lock.Observed)
	}
}

// TestAnUnchangedObservationIsLeftAlone, and its date is only rewritten once
// it has gone stale: rewriting every timestamp on every run puts a diff in
// front of a reviewer that says nothing (spec 03 §3).
func TestAnUnchangedObservationIsLeftAlone(t *testing.T) {
	t.Parallel()

	fresh := coretest.ContractNow.Add(-time.Hour)
	lock := driftLock("tracker.labels", hashWas, fresh)
	in := engine.DriftInput{
		Check:  "tracker.labels",
		Result: watched(engine.Pass("every label is on the board"), hashWas),
		Lock:   lock,
		Now:    coretest.ContractNow,
		Record: true,
	}

	if got := engine.Drift(in); got.State != engine.StatePass {
		t.Fatalf("state = %q, want pass", got.State)
	}
	if !lock.Observed["tracker.labels"].At.Equal(fresh) {
		t.Errorf("the date moved to %v for an hour-old observation", lock.Observed["tracker.labels"].At)
	}

	in.Now = fresh.Add(config.ObservationTTL + time.Hour)
	engine.Drift(in)
	if !lock.Observed["tracker.labels"].At.Equal(in.Now) {
		t.Errorf("date = %v, want a stale observation refreshed", lock.Observed["tracker.labels"].At)
	}
}

// TestDriftWritesNothingWithoutRecord: a read-only run still reports drift,
// it just does not update flow's memory.
func TestDriftWritesNothingWithoutRecord(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.labels",
		Result: watched(engine.Pass("every label is on the board"), hashNow),
		Lock:   lock,
		Now:    coretest.ContractNow,
	})

	if got.State != engine.StatePass {
		t.Errorf("state = %q, want pass", got.State)
	}
	if len(lock.Observed) != 0 {
		t.Errorf("lock = %v, want nothing written", lock.Observed)
	}
}

// TestDriftSaysWhatWouldPutItBack: the hashes say something changed, the
// plan's own changes say what to do about it.
func TestDriftSaysWhatWouldPutItBack(t *testing.T) {
	t.Parallel()

	lock := driftLock("tracker.status_options", hashWas, coretest.ContractNow.Add(-20*24*time.Hour))
	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.status_options",
		Result: watched(engine.Fail("the board is missing the Ready column"), hashNow),
		Lock:   lock,
		Item: engine.PlanItem{Changes: []core.Change{
			{What: "tracker.status_options", Op: core.ChangeCreate, Item: core.Item{Name: "Ready"}},
		}},
		Now:    coretest.ContractNow,
		Record: true,
	})

	if !strings.Contains(evidenceText(got), `create tracker.status_options "Ready"`) {
		t.Errorf("evidence = %q, want what would put it back", evidenceText(got))
	}
	if got.Remediation.Command != "flow doctor --fix" {
		t.Errorf("command = %q, want the one that fixes it", got.Remediation.Command)
	}
}

// A check that brought its own remedy has already said what the change is:
// a follow-up carries it as the last of its steps, a native outcome as
// evidence. Saying it again under "what flow saw" made the shortest ticket
// flow files the same sentence twice, which is what issue #194 was about.
func TestDriftDoesNotSayAgainWhatTheRemedyAlreadySays(t *testing.T) {
	t.Parallel()

	lock := driftLock("tracker.status_options", hashWas, coretest.ContractNow.Add(-20*24*time.Hour))
	own := engine.Fail("the board is missing the Ready column").Fix(engine.Remediation{
		Kind:    engine.RemedyFollowUp,
		Summary: "add the Ready column",
		Owner:   engine.OwnerTrackerAdmin,
		Steps:   []string{`create tracker.status_options "Ready"`},
	})

	got := engine.Drift(engine.DriftInput{
		Check:  "tracker.status_options",
		Result: watched(own, hashNow),
		Lock:   lock,
		Item: engine.PlanItem{Changes: []core.Change{
			{What: "tracker.status_options", Op: core.ChangeCreate, Item: core.Item{Name: "Ready"}},
		}},
		Now:    coretest.ContractNow,
		Record: true,
	})

	if strings.Contains(evidenceText(got), "to put it back") {
		t.Errorf("the change is in the ticket twice; evidence = %q", evidenceText(got))
	}
	// The drift itself still is: that is what this check adds, and nothing
	// else in the finding knows the thing used to be right.
	if !strings.Contains(evidenceText(got), "was ") || !strings.Contains(evidenceText(got), "now ") {
		t.Errorf("the finding no longer says what changed; evidence = %q", evidenceText(got))
	}
}

// TestDriftKeepsARemedyTheCheckAlreadyGave: a check that knows its own fix —
// because only a human can do it, say — is not overruled by the generic one.
func TestDriftKeepsARemedyTheCheckAlreadyGave(t *testing.T) {
	t.Parallel()

	lock := driftLock("tracker.wip", hashWas, coretest.ContractNow.Add(-20*24*time.Hour))
	own := engine.Fail("the column limit is gone").Fix(engine.Remediation{
		Kind: engine.RemedyFollowUp, Summary: "set the column limit again", Owner: engine.OwnerTrackerAdmin,
	})

	got := engine.Drift(engine.DriftInput{
		Check: "tracker.wip", Result: watched(own, hashNow), Lock: lock,
		Now: coretest.ContractNow, Record: true,
	})

	if got.Remediation.Kind != engine.RemedyFollowUp {
		t.Errorf("remedy = %q, want the check's own", got.Remediation.Kind)
	}
}

// TestACheckThatWatchesNothingIsPassedThrough: not every check has a hash,
// and the ones that do not cannot drift.
func TestACheckThatWatchesNothingIsPassedThrough(t *testing.T) {
	t.Parallel()

	lock := driftLock("local.remote", hashWas, coretest.ContractNow)
	got := engine.Drift(engine.DriftInput{
		Check: "local.remote", Result: engine.Fail("no git remote named origin"),
		Lock: lock, Now: coretest.ContractNow, Record: true,
	})

	if got.State != engine.StateFail {
		t.Errorf("state = %q, want the finding untouched", got.State)
	}
}

// --- attestations (spec 07 §8) ---

// attestCheck is something no API will answer.
var attestCheck = engine.Check{
	ID:       "tracker.expedite",
	Title:    "there is a saved view filtered to the expedite label",
	Category: engine.CategoryTracker,
}

// cannotTell is what a check says about something flow cannot verify.
func cannotTell() engine.Result {
	return engine.Fail("flow cannot see whether the expedite view exists").
		Fix(engine.Remediation{Kind: engine.RemedyAttest, Summary: "check it yourself and say so"})
}

// attested builds a lock holding one attestation.
func attested(a config.Attestation) *config.Lock {
	lock := config.NewLock("v0.3.0")
	lock.ConfigHash = "sha256:cfg"
	lock.Attestations = append(lock.Attestations, a)
	return lock
}

// TestAValidAttestationPasses: a human looked, and for something no API will
// answer that is the best evidence there is.
func TestAValidAttestationPasses(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty",
		"the Expedite view is saved on the board", "sha256:inputs",
		coretest.ContractNow.Add(-30*24*time.Hour), 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})

	if got.State != engine.StatePass {
		t.Fatalf("state = %q, want pass", got.State)
	}
	if !strings.Contains(got.Message, "attested by @17twenty") {
		t.Errorf("message = %q, want it to say whose word it is", got.Message)
	}
	if !strings.Contains(evidenceText(got), "the Expedite view is saved on the board") {
		t.Errorf("evidence = %q, want what they said", evidenceText(got))
	}
	if got.Remediation.Actionable() {
		t.Errorf("remedy = %+v, want nothing to do", got.Remediation)
	}
}

// TestAnAttestationAboutToExpireStillPassesButSaysSo: a week's notice is
// enough to renew it deliberately rather than in a hurry on the morning a
// check turns red.
func TestAnAttestationAboutToExpireStillPassesButSaysSo(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow.Add(-87*24*time.Hour), 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})

	if got.State != engine.StatePass {
		t.Fatalf("state = %q, want it to still pass", got.State)
	}
	if !strings.Contains(evidenceText(got), "expires in 3d") {
		t.Errorf("evidence = %q, want the notice", evidenceText(got))
	}
	if got.Remediation.Kind != engine.RemedyAttest {
		t.Errorf("remedy = %q, want the renewal offered", got.Remediation.Kind)
	}
	if !strings.Contains(got.Remediation.Command, "flow attest tracker.expedite") {
		t.Errorf("command = %q, want the one that renews it", got.Remediation.Command)
	}
}

// TestAnExpiredAttestationAsksAgain: a promise about a system is only as good
// as the day it was made.
func TestAnExpiredAttestationAsksAgain(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow.Add(-100*24*time.Hour), 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})

	if got.State != engine.StateAttest {
		t.Fatalf("state = %q, want %q", got.State, engine.StateAttest)
	}
	if !strings.Contains(got.Message, "has expired") {
		t.Errorf("message = %q, want it to say what happened", got.Message)
	}
	if got.Remediation.Owner != "@17twenty" {
		t.Errorf("owner = %q, want whoever vouched last time", got.Remediation.Owner)
	}
}

// TestAnAttestationLapsesWhenWhatItCoveredChanged: a promise about a workflow
// somebody has since rewritten is a promise about something else.
func TestAnAttestationLapsesWhenWhatItCoveredChanged(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow.Add(-time.Hour), 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:different", Now: coretest.ContractNow,
	})

	if got.State != engine.StateAttest {
		t.Fatalf("state = %q, want it to ask again", got.State)
	}
	if !strings.Contains(got.Message, "configuration it covered has changed") {
		t.Errorf("message = %q, want it to say why the promise lapsed", got.Message)
	}
}

// TestEditingAnUnrelatedPartOfTheConfigurationKeepsThePromise is why the
// attestation records what it covered rather than the whole file. Changing
// the allowed commit scopes must not throw away somebody's word about a
// saved view on the board.
func TestEditingAnUnrelatedPartOfTheConfigurationKeepsThePromise(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow.Add(-time.Hour), 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", ConfigHash: "sha256:a-totally-different-config",
		Now: coretest.ContractNow,
	})

	if got.State != engine.StatePass {
		t.Errorf("state = %q, want the promise to survive an unrelated edit", got.State)
	}
}

// TestAnOlderAttestationLapsesWithTheWholeConfiguration: one written before
// flow recorded what each covers has nothing finer to compare, so it takes
// the old, safe rule.
func TestAnOlderAttestationLapsesWithTheWholeConfiguration(t *testing.T) {
	t.Parallel()

	lock := attested(config.Attestation{
		Check: attestCheck.ID, By: "@17twenty", Note: "I checked",
		At: coretest.ContractNow.Add(-time.Hour), ExpiresAt: coretest.ContractNow.Add(89 * 24 * time.Hour),
	})

	same := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", ConfigHash: "sha256:cfg", Now: coretest.ContractNow,
	})
	if same.State != engine.StatePass {
		t.Errorf("state = %q, want it to stand while the configuration is unchanged", same.State)
	}

	changed := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: lock,
		Inputs: "sha256:inputs", ConfigHash: "sha256:rewritten", Now: coretest.ContractNow,
	})
	if changed.State != engine.StateAttest {
		t.Errorf("state = %q, want it to lapse", changed.State)
	}
}

// TestNobodyHasVouchedForItYet is the first run: the check says flow cannot
// tell, and the report says what would settle it.
func TestNobodyHasVouchedForItYet(t *testing.T) {
	t.Parallel()

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: config.NewLock("v0.3.0"),
		Inputs: "sha256:inputs", Now: coretest.ContractNow, TTL: 30 * 24 * time.Hour,
	})

	if got.State != engine.StateAttest {
		t.Fatalf("state = %q, want %q", got.State, engine.StateAttest)
	}
	if !strings.Contains(got.Message, "nobody has vouched for it") {
		t.Errorf("message = %q, want it to say so plainly", got.Message)
	}
	steps := strings.Join(got.Remediation.Steps, "\n")
	if !strings.Contains(steps, "attesting is not a way to silence it") {
		t.Errorf("steps = %q, want the one thing that would break this mechanism named", got.Remediation.Steps)
	}
	if !strings.Contains(steps, "30d") {
		t.Errorf("steps = %q, want how long their word will stand", got.Remediation.Steps)
	}
}

// TestAPromiseAboutAnAutomationNamesTheAuditThatWatchesIt: a native
// transition is the one case flow holds both halves of — nobody can ask the
// tracker whether the automation is on, and anybody can watch whether tickets
// moved. The person is being asked for the switch, so the steps point them at
// the fortnight of outcome flow can already show them.
func TestAPromiseAboutAnAutomationNamesTheAuditThatWatchesIt(t *testing.T) {
	t.Parallel()

	got := engine.Attest(engine.AttestInput{
		Check: engine.Check{
			ID:       core.NativeItemPrefix + "pr_merged",
			Title:    "the tracker moves a ticket when its work merges",
			Category: engine.CategoryTracker,
		},
		Result: cannotTell(), Lock: config.NewLock("v0.3.0"),
		Inputs: "sha256:inputs", Now: coretest.ContractNow, TTL: 30 * 24 * time.Hour,
	})

	steps := strings.Join(got.Remediation.Steps, "\n")
	if !strings.Contains(steps, engine.LiveTransitionCheck("pr_merged")) {
		t.Errorf("steps = %q, want the live check that watches the same automation named", got.Remediation.Steps)
	}
}

// TestAPromiseAboutSomethingNothingWatchesNamesNoAudit: every other
// attestation is a fact flow has no second reading of, and naming a check
// that does not exist in the report would send somebody looking for it.
func TestAPromiseAboutSomethingNothingWatchesNamesNoAudit(t *testing.T) {
	t.Parallel()

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: config.NewLock("v0.3.0"),
		Inputs: "sha256:inputs", Now: coretest.ContractNow, TTL: 30 * 24 * time.Hour,
	})

	steps := strings.Join(got.Remediation.Steps, "\n")
	if strings.Contains(steps, "in this report first") {
		t.Errorf("steps = %q, want no audit named for a fact nothing audits", got.Remediation.Steps)
	}
}

// TestAPromiseCannotOverruleSomethingFlowCanSee: if the API answers, the
// answer is better evidence than a promise. Otherwise attesting would be a
// way to swear away a real failure.
func TestAPromiseCannotOverruleSomethingFlowCanSee(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation("forge.protection", "@17twenty", "it is protected, honest",
		"sha256:inputs", coretest.ContractNow, 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: engine.Check{ID: "forge.protection", Title: "the trunk is protected"},
		Result: engine.Fail("main has no protection rule").
			Fix(engine.Remediation{Kind: engine.RemedyAuto, Command: "flow doctor --fix"}),
		Lock: lock, Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})

	if got.State != engine.StateFail {
		t.Errorf("state = %q, want the thing flow actually saw", got.State)
	}
}

// TestACheckThatHoldsIsNotAttested: a thing flow can verify does not need
// anybody's word, and the pass is reported on its own terms.
func TestACheckThatHoldsIsNotAttested(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow, 90*24*time.Hour))

	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: engine.Pass("the expedite view is there"),
		Lock: lock, Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})

	if strings.Contains(got.Message, "attested") {
		t.Errorf("message = %q, want the check's own answer", got.Message)
	}
}

// TestOneCheckHasOneAnswer: a second person vouching replaces the first.
// Two promises with different expiry dates is a question about which counts.
func TestOneCheckHasOneAnswer(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	engine.RecordAttestation(lock, engine.NewAttestation(attestCheck.ID, "@17twenty", "first",
		"sha256:inputs", coretest.ContractNow.Add(-24*time.Hour), 90*24*time.Hour))
	engine.RecordAttestation(lock, engine.NewAttestation(attestCheck.ID, "@priya", "second",
		"sha256:inputs", coretest.ContractNow, 90*24*time.Hour))

	if len(lock.Attestations) != 1 {
		t.Fatalf("attestations = %d, want one", len(lock.Attestations))
	}
	if lock.Attestations[0].By != "@priya" {
		t.Errorf("by = %q, want the most recent word", lock.Attestations[0].By)
	}
}

// TestAnAttestationRecordsWhatItCoveredAndWhenItDies is what `flow attest`
// writes, and it is the whole of why the mechanism is safe.
func TestAnAttestationRecordsWhatItCoveredAndWhenItDies(t *testing.T) {
	t.Parallel()

	made := engine.NewAttestation(attestCheck.ID, "@17twenty", "  the view is saved  ",
		"sha256:inputs", coretest.ContractNow, 0)

	if made.Note != "the view is saved" {
		t.Errorf("note = %q, want it trimmed", made.Note)
	}
	if made.Inputs != "sha256:inputs" {
		t.Errorf("inputs = %q, want what it covered", made.Inputs)
	}
	if want := coretest.ContractNow.Add(engine.DefaultAttestationTTL); !made.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v: a promise with no expiry is not a promise", made.ExpiresAt, want)
	}
}

// TestAnAttestationCanBeWithdrawn, which is what happens when somebody
// realises they were wrong, or when a check becomes verifiable.
func TestAnAttestationCanBeWithdrawn(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow, 90*24*time.Hour))

	if !engine.DropAttestation(lock, attestCheck.ID) {
		t.Fatal("DropAttestation found nothing to drop")
	}
	if len(lock.Attestations) != 0 {
		t.Errorf("attestations = %v, want none", lock.Attestations)
	}
	if engine.DropAttestation(lock, attestCheck.ID) {
		t.Error("DropAttestation claims to have dropped it twice")
	}
}

// TestAnAttestationRoundTripsThroughTheLock: what it covered must survive
// being written and read, or every attestation lapses on the next run.
func TestAnAttestationRoundTripsThroughTheLock(t *testing.T) {
	t.Parallel()

	lock := attested(engine.NewAttestation(attestCheck.ID, "@17twenty", "I checked",
		"sha256:inputs", coretest.ContractNow, 90*24*time.Hour))

	data, err := lock.Bytes()
	if err != nil {
		t.Fatalf("lock bytes: %v", err)
	}
	if !strings.Contains(string(data), "inputs: sha256:inputs") {
		t.Fatalf("the lock does not record what the attestation covered:\n%s", data)
	}

	read, err := config.ParseLock(data, ".flow.lock")
	if err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	got := engine.Attest(engine.AttestInput{
		Check: attestCheck, Result: cannotTell(), Lock: read,
		Inputs: "sha256:inputs", Now: coretest.ContractNow,
	})
	if got.State != engine.StatePass {
		t.Errorf("state after a round trip = %q, want pass", got.State)
	}
}

// evidenceText is a result's evidence as one searchable string.
func evidenceText(result engine.Result) string {
	lines := make([]string, 0, len(result.Evidence))
	for _, evidence := range result.Evidence {
		lines = append(lines, evidence.Text+" "+evidence.URL)
	}
	return strings.Join(lines, "\n")
}

// An unvouched check's remedy says what to look at before it asks for a word
// (issue #281). "Look at it yourself" said nothing about what, and a person who
// does not know what flow wanted can only vouch for what they assume. The
// provider's recipe says it in the reader's words; without one, flow says
// what it wanted in its own; with neither, the check's title is all there is.
func TestAnUnvouchedCheckSaysWhatToLookAt(t *testing.T) {
	t.Parallel()

	recipe := core.Recipe{
		What:  "A saved view on the board named Expedite.",
		Where: "On the board: the row of view tabs.",
		Steps: []string{"press + New view and name it Expedite"},
	}
	for name, tc := range map[string]struct {
		in    engine.AttestInput
		first string
		want  []string
	}{
		"in the provider's words": {
			in:    engine.AttestInput{Recipe: recipe, Asked: []string{`create "Expedite"`}},
			first: "what it is: A saved view on the board named Expedite.",
			want:  []string{"where it lives: On the board: the row of view tabs.", "press + New view and name it Expedite"},
		},
		"in flow's words": {
			in:    engine.AttestInput{Asked: []string{`create "Expedite" labels="incident"`}},
			first: `flow is asking for: create "Expedite" labels="incident"`,
		},
		"with nothing else to say": {
			in:    engine.AttestInput{},
			first: "look at it yourself: " + attestCheck.Title,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc.in.Check, tc.in.Result, tc.in.Lock, tc.in.Now = attestCheck, cannotTell(), config.NewLock("v0.3.0"), coretest.ContractNow
			steps := engine.Attest(tc.in).Remediation.Steps
			if len(steps) == 0 || steps[0] != tc.first {
				t.Fatalf("steps = %q, want them to open with %q", steps, tc.first)
			}
			for _, want := range tc.want {
				if !slices.Contains(steps, want) {
					t.Errorf("steps = %q, want %q among them", steps, want)
				}
			}
			if last := steps[len(steps)-1]; !strings.HasPrefix(last, "run flow attest "+attestCheck.ID) {
				t.Errorf("the last step is %q, want the command that settles it", last)
			}
			if name != "in the provider's words" && slices.ContainsFunc(steps, func(s string) bool { return strings.HasPrefix(s, "what it is:") }) {
				t.Errorf("steps = %q, a recipe nobody declared", steps)
			}
		})
	}
}
