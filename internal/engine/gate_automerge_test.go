package engine_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// What the gate arranges after it has judged: a pull request left to land
// itself once everything it is waiting for has gone green (spec 09 §1).

// armingWorld is the gate's ordinary world with everything an arming needs:
// a team that asked for it, a repository that permits it, and a pull request
// that is open and out of draft.
func armingWorld(t *testing.T, adjust ...func(*coretest.ForgeFixture)) *gateWorld {
	t.Helper()
	world := gateFixture(t)
	world.cfg.Forge.AutoMerge = true
	fx := coretest.ForgeFixture{
		PullRequests: []core.PullRequest{world.pullRequest(t, 4)},
		Repo:         core.RepoInfo{Owner: "acme", Name: "widgets", Merge: core.MergeSettings{AllowAutoMerge: true}},
		Capabilities: coretest.FullCapabilities("fake-forge", core.KindForge),
	}
	for _, change := range adjust {
		change(&fx)
	}
	world.forge = coretest.NewFakeForge(fx)
	return world
}

// TestTheGateLeavesAPullRequestSetToLandItself is the whole feature: an
// approval given while nobody is watching is not a queue that stops.
func TestTheGateLeavesAPullRequestSetToLandItself(t *testing.T) {
	world := armingWorld(t)

	gate := world.run(t)
	if gate.ArmOutcome != engine.GatePosted {
		t.Fatalf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GatePosted)
	}
	if pr := world.pullRequest(t, 4); !pr.AutoMerge {
		t.Error("#4 is not set to merge itself, so an approval arriving tonight waits until somebody looks")
	}
	if text := gate.Text(); !strings.Contains(text, "auto-merge armed") {
		t.Errorf("the text rendering says nothing about the arrangement:\n%s", text)
	}
}

// TestTheGateArmsNothingTheTeamDidNotAskFor: merging unattended is a decision
// about who is accountable, so the absence of a configuration is a no.
func TestTheGateArmsNothingTheTeamDidNotAskFor(t *testing.T) {
	world := armingWorld(t)
	world.cfg.Forge.AutoMerge = false

	gate := world.run(t)
	if gate.ArmOutcome != engine.GateNone {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateNone)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("the gate asked the forge %d times for something nobody configured", calls)
	}
	if text := gate.Text(); strings.Contains(text, "auto-merge") {
		t.Errorf("the text rendering talks about an arrangement nobody asked for:\n%s", text)
	}
}

// TestTheGateDoesNotArmADraft: a draft is work in progress, and a promise to
// land it the moment its checks pass is a promise about something nobody has
// finished writing.
func TestTheGateDoesNotArmADraft(t *testing.T) {
	world := armingWorld(t)
	pr := world.pullRequest(t, 4)
	pr.Draft = true
	world.forge.SetPullRequest(pr)

	gate := world.run(t)
	if gate.ArmOutcome != engine.GateNone {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateNone)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("the gate asked the forge %d times to promise to land a draft", calls)
	}
}

// TestTheGateDoesNotArmWhatIsAlreadyArmed: the gate runs on every push, and a
// call that changes nothing is still a call somebody's rate limit pays for.
func TestTheGateDoesNotArmWhatIsAlreadyArmed(t *testing.T) {
	world := armingWorld(t)
	pr := world.pullRequest(t, 4)
	pr.AutoMerge = true
	world.forge.SetPullRequest(pr)

	gate := world.run(t)
	if gate.ArmOutcome != engine.GateUnchanged {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateUnchanged)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("the gate armed an armed pull request %d times", calls)
	}
	if text := gate.Text(); strings.Contains(text, "auto-merge") {
		t.Errorf("the text repeats an arrangement nobody changed, in every log this run appears in:\n%s", text)
	}
}

// TestADryRunPromisesNothing: a dry run that armed a pull request would be
// the one dry run that merged something.
func TestADryRunPromisesNothing(t *testing.T) {
	world := armingWorld(t)

	gate := world.run(t, func(in *engine.GateInput) { in.DryRun = true })
	if gate.ArmOutcome != engine.GateDryRun {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateDryRun)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("a dry run armed the pull request %d times", calls)
	}
}

// TestAForgeThatWouldNotArmDoesNotFailTheGate: the verdict is about the
// change, not about what flow managed to arrange afterwards. A repository
// with auto-merge switched off is a reason this pull request waits for a
// person, which is what it would have done anyway.
func TestAForgeThatWouldNotArmDoesNotFailTheGate(t *testing.T) {
	world := armingWorld(t, func(fx *coretest.ForgeFixture) { fx.Repo.Merge.AllowAutoMerge = false })

	gate := world.run(t)
	if !gate.Pass() {
		t.Fatalf("a refused arming failed a pull request that broke no rule: %+v", gate.Result.Failures())
	}
	if gate.ArmOutcome != engine.GateRefused {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateRefused)
	}
	if text := gate.Text(); !strings.Contains(text, "would not have it") {
		t.Errorf("the text rendering does not say the forge refused:\n%s", text)
	}
}

// TestAForgeWithNoAutoMergeSaysSoRatherThanFailing: a capability-gated step
// that failed the gate would make the policy mean one thing on GitHub and
// another on a forge that has never had the feature.
func TestAForgeWithNoAutoMergeSaysSoRatherThanFailing(t *testing.T) {
	world := armingWorld(t, func(fx *coretest.ForgeFixture) {
		fx.Capabilities.Capabilities[core.CapAutoMerge] = core.Capability{}
	})

	gate := world.run(t)
	if !gate.Pass() {
		t.Fatalf("a forge that cannot arm failed the gate: %+v", gate.Result.Failures())
	}
	if gate.ArmOutcome != engine.GateUnsupported {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateUnsupported)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("the gate asked a forge that declares it cannot, %d times", calls)
	}
	if text := gate.Text(); !strings.Contains(text, "cannot do it") {
		t.Errorf("the text rendering does not say why nothing was arranged:\n%s", text)
	}
}

// TestACredentialThatMayNotWriteArmsNothing: the status was refused, so the
// arming would be too, and asking anyway buys a second refusal in the log.
func TestACredentialThatMayNotWriteArmsNothing(t *testing.T) {
	world := armingWorld(t)
	world.forge.Fail("SetCommitStatus", core.Errorf(core.ErrPermission, "set a status").WithWhy("read-only"))

	gate := world.run(t)
	if gate.ArmOutcome != engine.GateReadOnly {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateReadOnly)
	}
	if calls := world.forge.Calls("ArmAutoMerge"); calls != 0 {
		t.Errorf("a read-only run armed the pull request %d times", calls)
	}
}

// TestAnArmingRefusedForWantOfPermissionSaysSoOfTheArmingAlone: the refusal
// can also arrive here first, on a credential allowed to publish a status and
// not to merge, which is every repository whose gate holds `contents: read`
// (issue #190). The run has to say what it could not do — and nothing else.
// This is the shape that made the arming permission invisible for weeks: the
// refusal marked the whole run read-only, and the line that got printed was
// about a status the run had already posted (issue #192).
func TestAnArmingRefusedForWantOfPermissionSaysSoOfTheArmingAlone(t *testing.T) {
	world := armingWorld(t)
	world.forge.Fail("ArmAutoMerge", core.Errorf(core.ErrPermission, "arm").WithWhy("read-only"))

	gate := world.run(t)
	if gate.ArmOutcome != engine.GateReadOnly {
		t.Errorf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GateReadOnly)
	}
	if gate.StatusOutcome != engine.GatePosted {
		t.Errorf("the status outcome is %q, want %q: the status went out before the arming was refused",
			gate.StatusOutcome, engine.GatePosted)
	}
	if gate.ReadOnly {
		t.Error("a credential that may not merge was reported as one that may not write at all")
	}
	text := gate.Text()
	if !strings.Contains(text, "auto-merge not armed: this credential may not arrange a merge here") {
		t.Errorf("the run says %q, and nowhere in it that the arming was refused", text)
	}
	if strings.Contains(text, "status not posted") {
		t.Errorf("the run says %q, which is a report of something that did not happen", text)
	}
	if !gate.Pass() {
		t.Errorf("a credential that may not merge failed a pull request that broke no rule: %+v", gate.Result.Failures())
	}
}

// TestAnArmingThatFailsForNoGoodReasonIsReported: a provider that is simply
// unwell is not a pull request that will wait for a person, and a run that
// swallowed it would report a gate it never finished.
func TestAnArmingThatFailsForNoGoodReasonIsReported(t *testing.T) {
	world := armingWorld(t)
	world.forge.Fail("ArmAutoMerge", core.Errorf(core.ErrUnavailable, "arm").WithWhy("the forge is down"))

	in := world.input()
	in.Config.Forge.AutoMerge = true
	if _, err := engine.RunGate(context.Background(), in); err == nil {
		t.Fatal("a forge that was failing armed nothing and said nothing")
	}
}

// TestTheGateSaysInItsJSONWhatItArranged: a run that arranged nothing has to
// say so where a machine reads it. The gate's own exit code is about the
// change and never about the arming, so the document is the only place a CI
// job, or somebody reading one a week later, can find out that the queue is
// still waiting for a person.
func TestTheGateSaysInItsJSONWhatItArranged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		world func(*testing.T) *gateWorld
		want  string
	}{
		{"armed", func(t *testing.T) *gateWorld { return armingWorld(t) }, engine.GatePosted},
		{"the forge would not have it", func(t *testing.T) *gateWorld {
			return armingWorld(t, func(fx *coretest.ForgeFixture) { fx.Repo.Merge.AllowAutoMerge = false })
		}, engine.GateRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.world(t).run(t).JSON()
			if err != nil {
				t.Fatalf("render the gate: %v", err)
			}
			var doc struct {
				AutoMerge *string `json:"auto_merge"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("the gate's JSON does not parse: %v\n%s", err, raw)
			}
			if doc.AutoMerge == nil {
				t.Fatalf("the document says nothing about auto-merge:\n%s", raw)
			}
			if *doc.AutoMerge != tc.want {
				t.Errorf("auto_merge is %q, want %q", *doc.AutoMerge, tc.want)
			}
		})
	}
}

// TestTheGateArmsLastSoItDoesNotRaceItself.
//
// An armed pull request whose gate is already passing merges on the spot. If
// the arming went out first, the branch would be gone and the status, the
// comment and the transitions still to come would be aimed at a pull request
// the forge had closed.
func TestTheGateArmsLastSoItDoesNotRaceItself(t *testing.T) {
	world := armingWorld(t)
	world.tracker.SetTicket(unreadyTicket("#12", "Ready")) // so there is a comment to write
	var order []string
	forge := &orderedForge{FakeForge: world.forge, order: &order}
	tracker := &orderedTracker{FakeTracker: world.tracker, order: &order}

	in := world.input()
	in.Adapters = engine.Adapters{Forge: forge, Tracker: tracker}
	gate, err := engine.RunGate(context.Background(), in)
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	if gate.ArmOutcome != engine.GatePosted {
		t.Fatalf("the arming outcome is %q, want %q", gate.ArmOutcome, engine.GatePosted)
	}
	if len(order) == 0 || order[len(order)-1] != "ArmAutoMerge" {
		t.Errorf("the gate wrote %v; the arming has to be the last of them, or flow races its own merge", order)
	}
	for _, want := range []string{"SetCommitStatus", "UpsertComment", "Move"} {
		if !slices.Contains(order, want) {
			t.Errorf("the gate never called %s, so the order %v proves nothing about it", want, order)
		}
	}
}

// orderedForge records the order of the writes the gate makes, which is the
// one thing about them the fake's call counts cannot say.
type orderedForge struct {
	*coretest.FakeForge
	order *[]string
}

func (f *orderedForge) SetCommitStatus(ctx context.Context, sha string, s core.CommitStatus) error {
	*f.order = append(*f.order, "SetCommitStatus")
	return f.FakeForge.SetCommitStatus(ctx, sha, s)
}

func (f *orderedForge) UpsertComment(ctx context.Context, number int, m core.Marker, body string) error {
	*f.order = append(*f.order, "UpsertComment")
	return f.FakeForge.UpsertComment(ctx, number, m, body)
}

func (f *orderedForge) ArmAutoMerge(ctx context.Context, number int, method string) error {
	*f.order = append(*f.order, "ArmAutoMerge")
	return f.FakeForge.ArmAutoMerge(ctx, number, method)
}

// orderedTracker records the board's half of the same order.
type orderedTracker struct {
	*coretest.FakeTracker
	order *[]string
}

func (t *orderedTracker) Move(ctx context.Context, key string, column core.ColumnID) error {
	*t.order = append(*t.order, "Move")
	return t.FakeTracker.Move(ctx, key, column)
}
