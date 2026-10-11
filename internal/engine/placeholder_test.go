package engine_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// pendingConfig is a configuration that knows nothing it does not have to:
// the shipped preset, straight from the file, with all five placeholders
// still pending.
func pendingConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := preset(t, "lean-kanban")
	cfg.Forge.Repo = "acme/widget"
	return cfg
}

// resolveTracker is a tracker that can be asked about boards and can make
// one, which is the most permissive case: a test that wants less takes it
// away.
func resolveTracker(t *testing.T, candidates ...core.Candidate) *coretest.FakeTracker {
	t.Helper()
	fixture := coretest.ContractTracker()
	fixture.Kind = "fake-tracker"
	if candidates != nil {
		fixture.Candidates = map[string][]core.Candidate{engine.ItemTrackerProject: candidates}
	}
	return coretest.NewFakeTracker(fixture)
}

// resolve runs a round of resolution and insists it got as far as answering.
func resolve(t *testing.T, in engine.ResolveInput) ([]engine.Answer, *config.Lock) {
	t.Helper()
	if in.Config == nil {
		in.Config = pendingConfig(t)
	}
	if in.Lock == nil {
		in.Lock = config.NewLock("v0.3.0")
	}
	answers, err := engine.ResolvePlaceholders(t.Context(), in)
	if err != nil {
		t.Fatalf("resolve placeholders: %v", err)
	}
	return answers, in.Lock
}

// answer is the one resolution for a path.
func answer(t *testing.T, answers []engine.Answer, path string) engine.Answer {
	t.Helper()
	for _, a := range answers {
		if a.Placeholder.Path == path {
			return a
		}
	}
	t.Fatalf("no answer for %s; got %d answers", path, len(answers))
	return engine.Answer{}
}

// TestPlaceholdersAreTheOnesTheConfigurationLeftOpen pins the short list. A
// placeholder that stopped being found here would be one doctor silently
// stopped asking about, and `.flow.yaml` would keep a `${pending:...}` string
// as if it were a value.
func TestPlaceholdersAreTheOnesTheConfigurationLeftOpen(t *testing.T) {
	t.Parallel()

	found := engine.Placeholders(pendingConfig(t))
	paths := make([]string, 0, len(found))
	for _, p := range found {
		paths = append(paths, p.Path)
	}
	want := []string{
		"tracker.project",
		"followups.owners.default",
		"followups.owners.tracker_admin",
		"followups.owners.forge_admin",
		"followups.owners.platform",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("placeholders = %q, want %q", paths, want)
	}
	if !found[0].Discoverable {
		t.Error("tracker.project should be discoverable: a tracker can be asked what boards it has")
	}
	for _, p := range found[1:] {
		if p.Discoverable {
			t.Errorf("%s should not be discoverable: no API says who should receive a follow-up", p.Path)
		}
	}
	if name := found[0].Name; name != "project" {
		t.Errorf("placeholder name = %q, want %q", name, "project")
	}
}

// TestPlaceholdersOfAConfigurationThatKnowsEverything is the other end: a
// configuration with nothing pending has nothing to resolve.
func TestPlaceholdersOfAConfigurationThatKnowsEverything(t *testing.T) {
	t.Parallel()

	cfg := pendingConfig(t)
	cfg.Tracker.Project = config.Known("PVT_1")
	cfg.FollowUps.Owners.Default = config.Known("@dev")
	cfg.FollowUps.Owners.TrackerAdmin = config.Known("@dev")
	cfg.FollowUps.Owners.ForgeAdmin = config.Known("@dev")
	cfg.FollowUps.Owners.Platform = config.Known("@dev")

	if found := engine.Placeholders(cfg); len(found) != 0 {
		t.Fatalf("placeholders = %v, want none", found)
	}
	if found := engine.Placeholders(nil); found != nil {
		t.Errorf("placeholders of no configuration = %v, want nil", found)
	}
}

// TestResolveTakesTheOnlyCandidate is the happy path of spec 07 §3: one
// answer and permission to act means there is nothing to ask a human.
func TestResolveTakesTheOnlyCandidate(t *testing.T) {
	t.Parallel()

	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}
	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t, only),
		Fix:     true,
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowDiscovered {
		t.Errorf("how = %q, want %q", got.How, engine.HowDiscovered)
	}
	if got.Ref.ID != "PVT_1" {
		t.Errorf("ref id = %q, want PVT_1", got.Ref.ID)
	}
	if got.Ref.Name != "Delivery" {
		t.Errorf("ref name = %q, want Delivery: the name comes from the tracker", got.Ref.Name)
	}
	if got.Ref.What != engine.ItemTrackerProject {
		t.Errorf("ref what = %q, want %q", got.Ref.What, engine.ItemTrackerProject)
	}
	if lock.Resolved[engine.ItemTrackerProject] != "PVT_1" {
		t.Errorf("lock resolved = %v, want tracker.project=PVT_1", lock.Resolved)
	}
}

// TestResolveWithoutFixChangesNothing is spec 02 §2: a run that was only
// asked to look reports what it would have taken and writes nothing. Doctor
// without --fix must be safe to run from anywhere.
func TestResolveWithoutFixChangesNothing(t *testing.T) {
	t.Parallel()

	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}
	answers, lock := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t, only)})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Errorf("how = %q, want %q", got.How, engine.HowAsked)
	}
	if got.Resolved() {
		t.Errorf("resolved without --fix: %+v", got.Ref)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock was written without --fix: %v", lock.Resolved)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("candidates = %v, want the one it would have taken", got.Candidates)
	}
	if !strings.Contains(strings.Join(got.Steps, "\n"), "Delivery") {
		t.Errorf("steps = %q, want the candidate named so a human can act on it", got.Steps)
	}
}

// TestResolveAsksWhenThereIsMoreThanOneAnswer: flow does not guess which
// board a team's work belongs on. Two candidates is a question, and the
// question carries both so it can be answered without going and looking.
func TestResolveAsksWhenThereIsMoreThanOneAnswer(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t), Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Errorf("how = %q, want %q", got.How, engine.HowAsked)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock was written from a guess: %v", lock.Resolved)
	}
	steps := strings.Join(got.Steps, "\n")
	for _, want := range []string{"Delivery", "Research", "--set tracker.project="} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps = %q, want them to mention %q", got.Steps, want)
		}
	}
}

// TestResolveLetsAHumanChoose is the picker rung: when there is somebody at
// the terminal, two candidates is a question that can be answered now.
func TestResolveLetsAHumanChoose(t *testing.T) {
	t.Parallel()

	var asked engine.Placeholder
	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Fix:     true,
		Choose: func(_ context.Context, p engine.Placeholder, candidates []core.Candidate) (core.Candidate, error) {
			asked = p
			return candidates[1], nil
		},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowChosen {
		t.Fatalf("how = %q, want %q", got.How, engine.HowChosen)
	}
	if got.Ref.ID != "PVT_2" {
		t.Errorf("ref id = %q, want the one that was chosen", got.Ref.ID)
	}
	if asked.Path != engine.ItemTrackerProject {
		t.Errorf("chooser was asked about %q, want %q", asked.Path, engine.ItemTrackerProject)
	}
	if lock.Resolved[engine.ItemTrackerProject] != "PVT_2" {
		t.Errorf("lock resolved = %v, want the chosen board", lock.Resolved)
	}
}

// TestNobodyAtTheTerminalFallsThroughToTheQuestion: a picker that cannot ask
// — no TTY, or a user who pressed escape — is not a failure. It means the
// answer is the written-down question.
func TestNobodyAtTheTerminalFallsThroughToTheQuestion(t *testing.T) {
	t.Parallel()

	answers, _ := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Fix:     true,
		Choose: func(context.Context, engine.Placeholder, []core.Candidate) (core.Candidate, error) {
			return core.Candidate{}, core.Errorf(core.ErrNeedsInput, "ask which board").
				WithWhy("there is nobody at this terminal")
		},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Fatalf("how = %q, want %q", got.How, engine.HowAsked)
	}
	if got.Err != nil {
		t.Errorf("err = %v, want none: having nobody to ask is a question, not a fault", got.Err)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("candidates = %v, want both carried into the question", got.Candidates)
	}
}

// TestAPickerThatBrokeIsAFailure distinguishes the two: a chooser that could
// not ask falls through, a chooser that went wrong is reported.
func TestAPickerThatBrokeIsAFailure(t *testing.T) {
	t.Parallel()

	broken := errors.New("the terminal went away mid-draw")
	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Fix:     true,
		Choose: func(context.Context, engine.Placeholder, []core.Candidate) (core.Candidate, error) {
			return core.Candidate{}, broken
		},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if !errors.Is(got.Err, broken) {
		t.Errorf("err = %v, want the picker's own failure", got.Err)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock was written after the picker broke: %v", lock.Resolved)
	}
}

// TestResolveAcceptsAPastedURL is what a human actually has to hand: the
// address of the page they are looking at, not an opaque node id.
func TestResolveAcceptsAPastedURL(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Set:     map[string]string{engine.ItemTrackerProject: " https://tracker.test/projects/2 "},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowSet {
		t.Fatalf("how = %q, want %q (err: %v)", got.How, engine.HowSet, got.Err)
	}
	if got.Ref.ID != "PVT_2" {
		t.Errorf("ref id = %q, want the URL resolved to an id", got.Ref.ID)
	}
	if got.Ref.Name != "Research" {
		t.Errorf("ref name = %q, want the tracker's own name for it", got.Ref.Name)
	}
	if lock.Resolved[engine.ItemTrackerProject] != "PVT_2" {
		t.Errorf("lock resolved = %v, want the pasted board", lock.Resolved)
	}
}

// TestSetIsAcceptedWithoutFix: naming the board is the human's decision, not
// flow's, so recording it does not need permission to act. The permission
// --fix grants is to choose on a human's behalf.
func TestSetIsAcceptedWithoutFix(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Set:     map[string]string{engine.ItemTrackerProject: "PVT_1"},
	})

	if got := answer(t, answers, engine.ItemTrackerProject); got.How != engine.HowSet {
		t.Fatalf("how = %q, want %q (err: %v)", got.How, engine.HowSet, got.Err)
	}
	if lock.Resolved[engine.ItemTrackerProject] != "PVT_1" {
		t.Errorf("lock resolved = %v, want what the user named", lock.Resolved)
	}
}

// TestResolveRefusesAValueTheTrackerDoesNotHave is the whole point of
// verifying before recording. An id with a typo in it, written to the lock,
// turns every later run into a mystery about a board that is not there.
func TestResolveRefusesAValueTheTrackerDoesNotHave(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Fix:     true,
		Set:     map[string]string{engine.ItemTrackerProject: "PVT_999"},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.Resolved() {
		t.Fatalf("recorded an unverified id: %+v", got.Ref)
	}
	if !errors.Is(got.Err, core.ErrNotFound) {
		t.Errorf("err = %v, want it to say the tracker has no such thing", got.Err)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock = %v, want nothing written", lock.Resolved)
	}
}

// TestAPastedURLTheTrackerCannotReadIsReported: a URL from the wrong site is
// the user's mistake, and saying so beats silently treating it as an id.
func TestAPastedURLTheTrackerCannotReadIsReported(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Fix:     true,
		Set:     map[string]string{engine.ItemTrackerProject: "https://example.invalid/not-a-board"},
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if !errors.Is(got.Err, core.ErrValidation) {
		t.Errorf("err = %v, want the tracker's complaint about the address", got.Err)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock = %v, want nothing written", lock.Resolved)
	}
}

// TestWhatTheLockKnowsIsCheckedAndKept: the usual case is that the answer is
// already known. It is still read back, because a board can be deleted.
func TestWhatTheLockKnowsIsCheckedAndKept(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	lock.Resolve(engine.ItemTrackerProject, "PVT_2")

	answers, _ := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t), Lock: lock})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowLocked {
		t.Fatalf("how = %q, want %q", got.How, engine.HowLocked)
	}
	if got.Ref.Name != "Research" {
		t.Errorf("ref name = %q, want the name read back from the tracker", got.Ref.Name)
	}
}

// TestTheLockSurvivesATrackerThatWouldNotAnswer: a rate limit is not news
// about the board. Forgetting an identifier because the provider was busy
// would turn a transient failure into a question for a human, and would make
// `flow doctor` on a train destroy what it could not confirm.
func TestTheLockSurvivesATrackerThatWouldNotAnswer(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	lock.Resolve(engine.ItemTrackerProject, "PVT_2")
	tracker := resolveTracker(t)
	busy := core.Errorf(core.ErrRateLimited, "list the boards").WithWhy("the tracker is rate limiting")
	tracker.Fail("Discover", busy)

	answers, after := resolve(t, engine.ResolveInput{Tracker: tracker, Lock: lock, Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowLocked || got.Ref.ID != "PVT_2" {
		t.Fatalf("how = %q, ref = %+v, want the locked answer kept", got.How, got.Ref)
	}
	if !errors.Is(got.Err, core.ErrRateLimited) {
		t.Errorf("err = %v, want the reason it could not be confirmed", got.Err)
	}
	if after.Resolved[engine.ItemTrackerProject] != "PVT_2" {
		t.Errorf("lock resolved = %v, want it left alone", after.Resolved)
	}
}

// TestALockedBoardThatIsGoneIsNotTheAnswer: the other side of reading back.
// When the tracker answers and the board is not among what it has, the
// identifier has stopped being true and resolution starts again.
func TestALockedBoardThatIsGoneIsNotTheAnswer(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	lock.Resolve(engine.ItemTrackerProject, "PVT_DELETED")

	answers, _ := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t), Lock: lock, Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Fatalf("how = %q, want the ladder to have started again", got.How)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("candidates = %v, want what the tracker does have", got.Candidates)
	}
}

// TestADeletedBoardWithOneReplacementIsTakenStraightAway: having already
// asked the tracker what it has, resolution does not ask again.
func TestADeletedBoardWithOneReplacementIsTakenStraightAway(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	lock.Resolve(engine.ItemTrackerProject, "PVT_DELETED")
	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}

	answers, after := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t, only), Lock: lock, Fix: true,
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowDiscovered || got.Ref.ID != "PVT_1" {
		t.Fatalf("how = %q, ref = %+v, want the one board that is left", got.How, got.Ref)
	}
	if after.Resolved[engine.ItemTrackerProject] != "PVT_1" {
		t.Errorf("lock resolved = %v, want the replacement", after.Resolved)
	}
}

// TestResolveCreatesTheBoardWhenItCan is the rung most trackers never reach:
// github-projects, Jira and Linear all declare project creation as something
// an administrator does. A tracker that declares it can is taken at its word.
func TestResolveCreatesTheBoardWhenItCan(t *testing.T) {
	t.Parallel()

	empty := coretest.ContractTracker()
	empty.Candidates = map[string][]core.Candidate{}
	tracker := coretest.NewFakeTracker(empty)

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker:     tracker,
		Permissions: core.Permissions{Tokens: []string{"project_write", "repo", "issues_write", "project_read"}},
		Fix:         true,
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowCreated {
		t.Fatalf("how = %q, want %q (err: %v)", got.How, engine.HowCreated, got.Err)
	}
	if got.Ref.Name != "widget" {
		t.Errorf("created board name = %q, want it named after the repository", got.Ref.Name)
	}
	if got.Ref.ID == "" {
		t.Error("created board has no id: the identifier must come from the tracker")
	}
	if lock.Resolved[engine.ItemTrackerProject] != got.Ref.ID {
		t.Errorf("lock resolved = %v, want the created board's id", lock.Resolved)
	}
}

// TestCreationNeedsThePermissionItDeclared: a capability that says it needs a
// token is not attempted without it. The credential is checked before the
// request, so the failure is a sentence rather than a 403.
func TestCreationNeedsThePermissionItDeclared(t *testing.T) {
	t.Parallel()

	empty := coretest.ContractTracker()
	empty.Candidates = map[string][]core.Candidate{}
	caps := empty.Capabilities
	project := caps.Capability(core.CapProject)
	project.Requires = []string{"project_admin"}
	caps.Capabilities[core.CapProject] = project
	empty.Capabilities = caps

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker:     coretest.NewFakeTracker(empty),
		Permissions: core.Permissions{Tokens: []string{"project_read"}},
		Fix:         true,
	})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Fatalf("how = %q, want it to have asked rather than tried", got.How)
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock = %v, want nothing written", lock.Resolved)
	}
}

// TestATrackerThatCannotCreateIsNotAskedTo: the real declarations say
// set: false for a project, and resolution respects that rather than
// discovering it from a rejected request.
func TestATrackerThatCannotCreateIsNotAskedTo(t *testing.T) {
	t.Parallel()

	empty := coretest.ContractTracker()
	empty.Candidates = map[string][]core.Candidate{}
	empty.Capabilities = coretest.NoCapabilities("fake-tracker", core.KindTracker)
	tracker := coretest.NewFakeTracker(empty)

	answers, _ := resolve(t, engine.ResolveInput{Tracker: tracker, Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject)
	if got.How != engine.HowAsked {
		t.Fatalf("how = %q, want %q", got.How, engine.HowAsked)
	}
	steps := strings.Join(got.Steps, "\n")
	if !strings.Contains(steps, "fake-tracker") {
		t.Errorf("steps = %q, want them to name the tracker to go and open", got.Steps)
	}
	if !strings.Contains(steps, "--set tracker.project=") {
		t.Errorf("steps = %q, want the command that finishes the job", got.Steps)
	}
}

// TestOwnersAreTakenOnTrust: there is no API that says whether @priya is the
// right person to ask about board administration, so a handle is recorded as
// typed. Reading it back is not possible and pretending otherwise would mean
// never resolving it.
func TestOwnersAreTakenOnTrust(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{
		Tracker: resolveTracker(t),
		Set:     map[string]string{"followups.owners.default": "@priya"},
	})

	got := answer(t, answers, "followups.owners.default")
	if got.How != engine.HowSet || got.Ref.ID != "@priya" {
		t.Fatalf("how = %q, ref = %+v, want the handle as typed", got.How, got.Ref)
	}
	if lock.Resolved["followups.owners.default"] != "@priya" {
		t.Errorf("lock resolved = %v, want the handle recorded", lock.Resolved)
	}
	unset := answer(t, answers, "followups.owners.platform")
	if unset.How != engine.HowAsked {
		t.Errorf("how = %q, want the ones nobody named to still be questions", unset.How)
	}
	if !strings.Contains(strings.Join(unset.Steps, "\n"), "--set followups.owners.platform=") {
		t.Errorf("steps = %q, want the command that answers it", unset.Steps)
	}
}

// TestAKnownOwnerIsNotAskedAboutAgain: an owner already in the lock is
// answered from it, without a tracker being involved at all.
func TestAKnownOwnerIsNotAskedAboutAgain(t *testing.T) {
	t.Parallel()

	lock := config.NewLock("v0.3.0")
	lock.Resolve("followups.owners.platform", "@sre")

	answers, _ := resolve(t, engine.ResolveInput{Lock: lock})

	got := answer(t, answers, "followups.owners.platform")
	if got.How != engine.HowLocked || got.Ref.ID != "@sre" {
		t.Fatalf("how = %q, ref = %+v, want it answered from the lock", got.How, got.Ref)
	}
}

// TestResolveWithoutATrackerStillAsks: `flow doctor` in a repository whose
// tracker is misconfigured must still produce the list of what it needs,
// rather than failing before it has anything to say.
func TestResolveWithoutATrackerStillAsks(t *testing.T) {
	t.Parallel()

	answers, lock := resolve(t, engine.ResolveInput{Fix: true})

	if len(answers) != 5 {
		t.Fatalf("answers = %d, want all five placeholders accounted for", len(answers))
	}
	for _, got := range answers {
		if got.How != engine.HowAsked {
			t.Errorf("%s: how = %q, want %q", got.Placeholder.Path, got.How, engine.HowAsked)
		}
		if len(got.Steps) == 0 {
			t.Errorf("%s: no steps; an unanswerable question is useless without them", got.Placeholder.Path)
		}
	}
	if len(lock.Resolved) != 0 {
		t.Errorf("lock = %v, want nothing written", lock.Resolved)
	}
}

// TestResolveNeedsSomewhereToRecord: resolution without a lock would do the
// work and throw the answer away, which is worse than refusing.
func TestResolveNeedsSomewhereToRecord(t *testing.T) {
	t.Parallel()

	_, err := engine.ResolvePlaceholders(t.Context(), engine.ResolveInput{Config: pendingConfig(t)})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}

// TestResolveStopsWhenTheContextIsDone: every rung talks to a provider, and a
// cancelled run stops talking.
func TestResolveStopsWhenTheContextIsDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := engine.ResolvePlaceholders(ctx, engine.ResolveInput{
		Config:  pendingConfig(t),
		Lock:    config.NewLock("v0.3.0"),
		Tracker: resolveTracker(t),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestPendingIsWhatStillNeedsAHuman is what doctor counts to decide whether
// anybody has to do anything.
func TestPendingIsWhatStillNeedsAHuman(t *testing.T) {
	t.Parallel()

	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}
	answers, _ := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t, only), Fix: true})

	still := engine.Pending(answers)
	if len(still) != 4 {
		t.Fatalf("pending = %d, want the four owners", len(still))
	}
	for _, got := range still {
		if got.Resolved() {
			t.Errorf("%s is resolved but counted as pending", got.Placeholder.Path)
		}
	}
	if len(answers) != 5 {
		t.Errorf("answers = %d, want Pending to have left its input alone", len(answers))
	}
}

// TestAnswerBecomesADoctorResult: resolution is reported through the same
// check machinery as everything else, so a placeholder is a check that needs
// input rather than one that failed.
func TestAnswerBecomesADoctorResult(t *testing.T) {
	t.Parallel()

	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}
	answers, _ := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t, only), Fix: true})

	done := answer(t, answers, engine.ItemTrackerProject).Result()
	if done.State != engine.StatePass {
		t.Errorf("state = %q, want pass", done.State)
	}
	if !strings.Contains(done.Message, "Delivery") || !strings.Contains(done.Message, "PVT_1") {
		t.Errorf("message = %q, want it to say both what and which", done.Message)
	}

	open := answer(t, answers, "followups.owners.default").Result()
	if open.State != engine.StateInput {
		t.Fatalf("state = %q, want %q", open.State, engine.StateInput)
	}
	if open.Remediation.Kind != engine.RemedyInput {
		t.Errorf("remedy kind = %q, want %q", open.Remediation.Kind, engine.RemedyInput)
	}
	if !strings.Contains(open.Remediation.Command, "--set followups.owners.default=") {
		t.Errorf("command = %q, want the one that answers it", open.Remediation.Command)
	}
}

// TestAnAnswerThatCouldNotBeAttemptedIsAnError separates "nobody has decided"
// from "flow could not ask": the first is a question for a human, the second
// is a fault to report.
func TestAnAnswerThatCouldNotBeAttemptedIsAnError(t *testing.T) {
	t.Parallel()

	tracker := resolveTracker(t)
	tracker.Fail("Discover", core.Errorf(core.ErrPermission, "list the boards").
		WithWhy("the token cannot read projects"))

	answers, _ := resolve(t, engine.ResolveInput{Tracker: tracker, Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject).Result()
	if got.State != engine.StateError {
		t.Fatalf("state = %q, want %q", got.State, engine.StateError)
	}
	if got.Err == nil {
		t.Error("no error carried: the report must say why it could not look")
	}
}

// TestCandidatesBecomeEvidence: the question a human reads should carry what
// flow saw, with addresses they can open.
func TestCandidatesBecomeEvidence(t *testing.T) {
	t.Parallel()

	answers, _ := resolve(t, engine.ResolveInput{Tracker: resolveTracker(t), Fix: true})

	got := answer(t, answers, engine.ItemTrackerProject).Result()
	if len(got.Evidence) != 2 {
		t.Fatalf("evidence = %v, want one line per candidate", got.Evidence)
	}
	if got.Evidence[0].URL != "https://tracker.test/projects/1" {
		t.Errorf("evidence url = %q, want the board's own address", got.Evidence[0].URL)
	}
	if !strings.Contains(got.Evidence[0].Text, "12 open items") {
		t.Errorf("evidence text = %q, want the detail that tells them apart", got.Evidence[0].Text)
	}
}

// TestResolutionIsRecordedInTheLockNotTheConfiguration: `.flow.yaml` is the
// team's intent, and an identifier the tracker made up is not part of it.
// Writing it back would make a reviewed file change without review.
func TestResolutionIsRecordedInTheLockNotTheConfiguration(t *testing.T) {
	t.Parallel()

	cfg := pendingConfig(t)
	only := core.Candidate{ID: "PVT_1", Name: "Delivery", URL: "https://tracker.test/projects/1"}

	resolve(t, engine.ResolveInput{Config: cfg, Tracker: resolveTracker(t, only), Fix: true})

	if !cfg.Tracker.Project.IsPending() {
		t.Errorf("tracker.project = %q, want the configuration left as written", cfg.Tracker.Project)
	}
}
