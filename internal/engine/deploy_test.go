package engine_test

import (
	"context"
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

// What one deployment finishes. The decision about a ticket is the transition
// engine's and is held by transition_test.go; what is tested here is the
// gathering in front of it — which merges the environment is running, and what
// flow declines to act on when it cannot prove that.

// The commits of the fixture, newest first: a merge that landed after the
// deployment, the deployed commit itself, and the two merges it is running.
const (
	mergeLate   = "f0000000000000000000000000000000000000ff"
	deployedSHA = "d0000000000000000000000000000000000000dd"
	mergeTwo    = "b0000000000000000000000000000000000000bb"
	mergeOne    = "a0000000000000000000000000000000000000aa"
)

// deployNow is the moment every deployment in these tests happens at.
var deployNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// deployWorld is a repository whose work is finished by a deployment.
type deployWorld struct {
	forge   *coretest.FakeForge
	tracker *coretest.FakeTracker
	deploys *coretest.FakeDeploys
	cfg     *config.Config
}

// deployWorldFixture is the ordinary case: production is running two merges, a
// third merged after it went out, and every ticket is in review waiting to be
// finished.
func deployWorldFixture(t *testing.T) *deployWorld {
	t.Helper()
	cfg := preset(t, "deploy-done")
	return &deployWorld{
		cfg: cfg,
		forge: coretest.NewFakeForge(coretest.ForgeFixture{
			PullRequests: []core.PullRequest{
				deployPR(1, mergeOne, "#11", deployNow.Add(-30*time.Minute)),
				deployPR(2, mergeTwo, "#12", deployNow.Add(-20*time.Minute)),
				deployPR(3, mergeLate, "#13", deployNow.Add(-10*time.Minute)),
			},
			Commits: []core.Commit{
				{SHA: mergeLate}, {SHA: deployedSHA}, {SHA: mergeTwo}, {SHA: mergeOne},
			},
		}),
		tracker: coretest.NewFakeTracker(coretest.TrackerFixture{
			Tickets: []core.Ticket{
				deployTicket("#11"), deployTicket("#12"), deployTicket("#13"),
			},
		}),
		deploys: coretest.NewFakeDeploys(coretest.DeployFixture{
			Deployments: []core.Deployment{{
				ID:          "7",
				Environment: "production",
				SHA:         deployedSHA,
				State:       core.DeploySuccess,
				UpdatedAt:   deployNow,
			}},
		}),
	}
}

// deployPR is one merged pull request closing one ticket.
func deployPR(number int, merge, issue string, at time.Time) core.PullRequest {
	return core.PullRequest{
		Number:        number,
		Title:         "feat(engine): finish on deployment",
		Author:        "nick",
		State:         core.PRStateMerged,
		MergeSHA:      merge,
		BaseRef:       "main",
		ClosingIssues: []string{issue},
		UpdatedAt:     at,
		MergedAt:      at,
	}
}

// deployTicket is a ticket whose pull request has merged and which is waiting
// in review for the deployment that finishes it.
func deployTicket(key string) core.Ticket {
	return core.Ticket{
		Key:      key,
		Title:    "finish on deployment",
		Body:     "## Acceptance criteria\n\n- it is running\n",
		Parent:   "#1",
		Status:   core.Status{ID: "In review", Name: "In review"},
		ColumnID: core.ColumnID("In review"),
	}
}

// input is the run's input for this world.
func (w *deployWorld) input() engine.DeployInput {
	return engine.DeployInput{
		SHA:         deployedSHA,
		Environment: "production",
		Config:      w.cfg,
		Adapters:    engine.Adapters{Forge: w.forge, Tracker: w.tracker, Deploys: w.deploys},
		Now:         deployNow,
	}
}

// run reacts to the deployment, failing the test when it returns an error.
func (w *deployWorld) run(t *testing.T, adjust ...func(*engine.DeployInput)) engine.Deploy {
	t.Helper()
	in := w.input()
	for _, change := range adjust {
		change(&in)
	}
	deploy, err := engine.RunDeploy(context.Background(), in)
	if err != nil {
		t.Fatalf("react to the deployment: %v", err)
	}
	return deploy
}

// column is where the board has a ticket now.
func (w *deployWorld) column(t *testing.T, key string) string {
	t.Helper()
	ticket, err := w.tracker.Ticket(context.Background(), key)
	if err != nil {
		t.Fatalf("read ticket %s: %v", key, err)
	}
	return ticket.Status.Name
}

// moveOf is the decision about one ticket.
func moveOf(t *testing.T, deploy engine.Deploy, key string) engine.TicketMove {
	t.Helper()
	for _, move := range deploy.Moves {
		if move.Key == key {
			return move
		}
	}
	t.Fatalf("no decision about %s in %+v", key, deploy.Moves)
	return engine.TicketMove{}
}

// reasons is every reason the decision about one ticket recorded.
func reasons(move engine.TicketMove) []string {
	var out []string
	for _, step := range move.Steps {
		out = append(out, step.Reason)
	}
	return out
}

// TestDeployIsNotAStatusWhenDoneMeansMerged is spec 04 §6: the default
// workflow proves deployment as lag rather than tracking it as a column, so
// there is nothing to do — and nothing to do must cost nothing, which is why
// this answer is reached without a forge, a tracker or a credential.
func TestDeployIsNotAStatusWhenDoneMeansMerged(t *testing.T) {
	deploy, err := engine.RunDeploy(context.Background(), engine.DeployInput{
		SHA:    deployedSHA,
		Config: preset(t, "lean-kanban"),
	})
	if err != nil {
		t.Fatalf("react to the deployment: %v", err)
	}
	if deploy.Watched || deploy.Acted() {
		t.Fatalf("deploy = %+v, want unwatched and inert", deploy)
	}
	if !strings.Contains(deploy.Why, "workflow.done_on is merged") {
		t.Fatalf("why = %q, want it to name workflow.done_on", deploy.Why)
	}
	if deploy.Environment != "production" {
		t.Fatalf("environment = %q, want the configured one", deploy.Environment)
	}
}

// TestDeployIgnoresAnotherEnvironment keeps a review app out of the board. A
// repository deploys to more environments than the one the workflow is about,
// and reaching staging does not finish anything. Nothing is asked of any
// provider, which is why this run is given none.
func TestDeployIgnoresAnotherEnvironment(t *testing.T) {
	world := deployWorldFixture(t)
	deploy, err := engine.RunDeploy(context.Background(), engine.DeployInput{
		SHA:         deployedSHA,
		Environment: "staging",
		Config:      world.cfg,
		Now:         deployNow,
	})
	if err != nil {
		t.Fatalf("react to the deployment: %v", err)
	}
	if !deploy.Watched || deploy.Acted() {
		t.Fatalf("deploy = %+v, want watched and inert", deploy)
	}
	for _, want := range []string{"staging", "production"} {
		if !strings.Contains(deploy.Why, want) {
			t.Fatalf("why = %q, want it to name %s", deploy.Why, want)
		}
	}
}

// TestDeployMatchesTheEnvironmentByNameNotByCase is the other half of that
// guard. The environment arrives from whatever started the run and the
// configured one was typed by a person, so "Production" and "production" are
// one environment: a deployment that finished the work must not be ignored
// over a capital letter, when the same name in the other case would have been
// acted on.
func TestDeployMatchesTheEnvironmentByNameNotByCase(t *testing.T) {
	world := deployWorldFixture(t)
	deploy := world.run(t, func(in *engine.DeployInput) { in.Environment = "Production" })

	if !deploy.Confirmed || !deploy.Acted() {
		t.Fatalf("deploy = %+v, want the deployment recognised as production's", deploy)
	}
	if got := world.column(t, "#11"); got != "Done" {
		t.Fatalf("#11 is in %q, want Done", got)
	}
	if deploy.Environment != "production" {
		t.Fatalf("environment = %q, want the name the workflow calls it", deploy.Environment)
	}
}

// TestDeployNeedsToKnowWhatWasDeployed refuses to guess. "The environment is
// running something" moves no board.
func TestDeployNeedsToKnowWhatWasDeployed(t *testing.T) {
	world := deployWorldFixture(t)
	_, err := engine.RunDeploy(context.Background(), without(world.input(), "sha"))
	if !errors.Is(err, core.ErrNeedsInput) {
		t.Fatalf("RunDeploy returned %v, want ErrNeedsInput", err)
	}
}

// without returns the input with one part of it missing, which is how the
// refusals below are spelled.
func without(in engine.DeployInput, part string) engine.DeployInput {
	switch part {
	case "sha":
		in.SHA = ""
	case "config":
		in.Config = nil
	case "forge":
		in.Adapters.Forge = nil
	case "tracker":
		in.Adapters.Tracker = nil
	case "deploys":
		in.Adapters.Deploys = nil
	}
	return in
}

// TestDeployNeedsSomewhereToReadAndWrite refuses a repository that cannot
// answer the question. A deployment needs all three providers: the record to
// confirm it, the forge to say what it contains, and the board to finish.
func TestDeployNeedsSomewhereToReadAndWrite(t *testing.T) {
	world := deployWorldFixture(t)
	for _, part := range []string{"config", "forge", "tracker", "deploys"} {
		t.Run("no "+part, func(t *testing.T) {
			_, err := engine.RunDeploy(context.Background(), without(world.input(), part))
			if !errors.Is(err, core.ErrValidation) {
				t.Fatalf("RunDeploy returned %v, want ErrValidation", err)
			}
		})
	}
}

// TestDeployBelievesTheRecordAndNotTheEvent is spec 13 §1. The event payload
// claims the environment is running a commit and `--sha` claims it with
// nothing at all behind it; the record is the contract, so a deployment that
// failed, or never finished, finishes no work.
func TestDeployBelievesTheRecordAndNotTheEvent(t *testing.T) {
	for _, state := range []string{core.DeployFailure, core.DeployPending, core.DeployInProgress} {
		t.Run(state, func(t *testing.T) {
			world := deployWorldFixture(t)
			world.deploys = coretest.NewFakeDeploys(coretest.DeployFixture{
				Deployments: []core.Deployment{{
					Environment: "production", SHA: deployedSHA, State: state,
				}},
			})
			deploy := world.run(t)
			if deploy.Confirmed || deploy.Acted() {
				t.Fatalf("deploy = %+v, want unconfirmed and inert", deploy)
			}
			if !strings.Contains(deploy.Why, "no successful deployment") {
				t.Fatalf("why = %q, want it to say the record is missing", deploy.Why)
			}
			if n := world.tracker.Calls("Move"); n != 0 {
				t.Fatalf("Move called %d times, want the board left alone", n)
			}
		})
	}
}

// TestDeployFinishesTheWorkItIsRunning is the whole point: the tickets whose
// merges are in the environment reach the done column, and nothing else does.
func TestDeployFinishesTheWorkItIsRunning(t *testing.T) {
	world := deployWorldFixture(t)
	deploy := world.run(t)

	if !deploy.Confirmed || !deploy.Acted() {
		t.Fatalf("deploy = %+v, want confirmed and acted on", deploy)
	}
	running := []int{}
	for _, pr := range deploy.PRs {
		running = append(running, pr.Number)
	}
	// The forge answers newest first, and the order it answered in is the
	// order the document reports.
	if len(running) != 2 || running[0] != 2 || running[1] != 1 {
		t.Fatalf("running = %v, want the two merges production holds", running)
	}
	for _, key := range []string{"#11", "#12"} {
		move := moveOf(t, deploy, key)
		if move.To != "done" || move.Event != config.EventDeployed {
			t.Fatalf("%s moved %+v, want done on deployed", key, move)
		}
		if got := world.column(t, key); got != "Done" {
			t.Fatalf("%s is in %q, want Done", key, got)
		}
	}
	if got := world.column(t, "#13"); got != "In review" {
		t.Fatalf("#13 is in %q, want it left in review", got)
	}
}

// TestDeployLeavesAMergeItIsNotRunning is spec 13 §3: a merge that landed
// after the deployed commit is not in it, however recently it merged, and the
// ticket it closes is not finished by somebody else's deployment.
func TestDeployLeavesAMergeItIsNotRunning(t *testing.T) {
	world := deployWorldFixture(t)
	deploy := world.run(t)

	for _, pr := range deploy.PRs {
		if pr.Number == 3 {
			t.Fatalf("PRs = %+v, want #3 left out: it merged after the deployment", deploy.PRs)
		}
	}
	for _, move := range deploy.Moves {
		if move.Key == "#13" {
			t.Fatalf("moves = %+v, want no decision about a ticket this deployment is not running", deploy.Moves)
		}
	}
}

// TestDeployCannotProveAMergeWithNoMergeCommit is the forge saying it merged
// a pull request without saying what the merge produced. There is nothing to
// compare, so there is nothing to claim — and nothing to ask the forge either.
func TestDeployCannotProveAMergeWithNoMergeCommit(t *testing.T) {
	world := deployWorldFixture(t)
	world.forge = coretest.NewFakeForge(coretest.ForgeFixture{
		PullRequests: []core.PullRequest{deployPR(1, "", "#11", deployNow.Add(-time.Minute))},
		Commits:      []core.Commit{{SHA: deployedSHA}},
	})
	deploy := world.run(t)

	if len(deploy.PRs) != 0 || deploy.Acted() {
		t.Fatalf("deploy = %+v, want nothing proven running", deploy)
	}
	if n := world.forge.Calls("Compare"); n != 0 {
		t.Fatalf("Compare called %d times, want no comparison with nothing to compare", n)
	}
}

// TestDeployWaitsForTheRestOfTheWork is spec 04 §5 rule 4. A ticket with a
// second pull request still open is not finished by the first one shipping,
// which is why the open pull requests are read even though none of them can
// ever be a candidate.
func TestDeployWaitsForTheRestOfTheWork(t *testing.T) {
	world := deployWorldFixture(t)
	world.forge.SetPullRequest(core.PullRequest{
		Number:        4,
		Title:         "feat(engine): the other half",
		Author:        "nick",
		State:         core.PRStateOpen,
		HeadSHA:       "cafe123",
		BaseRef:       "main",
		ClosingIssues: []string{"#11"},
		UpdatedAt:     deployNow,
	})
	deploy := world.run(t)

	move := moveOf(t, deploy, "#11")
	if move.Moves() {
		t.Fatalf("#11 moved %+v, want it held while #4 is open", move)
	}
	if !slicesContain(reasons(move), engine.ReasonOpenWork) {
		t.Fatalf("reasons = %v, want %s", reasons(move), engine.ReasonOpenWork)
	}
	if got := world.column(t, "#11"); got != "In review" {
		t.Fatalf("#11 is in %q, want it left in review", got)
	}
	if got := world.column(t, "#12"); got != "Done" {
		t.Fatalf("#12 is in %q, want the finished work finished", got)
	}
}

// slicesContain reports whether a list holds a value.
func slicesContain(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestDeployOnlyLooksBackAsFarAsTheObjective is spec 07 §6's division of
// labour: a merge that took longer to ship than `deploys.max_lag` allows is
// something doctor reports and catches up, not something a CI job reacting to
// one deployment walks the whole history to find.
func TestDeployOnlyLooksBackAsFarAsTheObjective(t *testing.T) {
	world := deployWorldFixture(t)
	world.forge = coretest.NewFakeForge(coretest.ForgeFixture{
		PullRequests: []core.PullRequest{deployPR(1, mergeOne, "#11", deployNow.Add(-3*time.Hour))},
		Commits:      []core.Commit{{SHA: deployedSHA}, {SHA: mergeOne}},
	})

	deploy := world.run(t)
	if len(deploy.PRs) != 0 || deploy.Acted() {
		t.Fatalf("deploy = %+v, want the long-overdue merge left to doctor", deploy)
	}

	// The same world with no moment to measure from has no window either:
	// flow has no number of its own to put there.
	unbounded := world.run(t, func(in *engine.DeployInput) { in.Now = time.Time{} })
	if len(unbounded.PRs) != 1 {
		t.Fatalf("PRs = %+v, want the merge found when there is no window", unbounded.PRs)
	}
}

// TestDeployDecidesWithoutMovingOnADryRun reports the whole decision and
// leaves the board exactly as it was.
func TestDeployDecidesWithoutMovingOnADryRun(t *testing.T) {
	world := deployWorldFixture(t)
	deploy := world.run(t, func(in *engine.DeployInput) { in.DryRun = true })

	if !deploy.DryRun || !deploy.Acted() {
		t.Fatalf("deploy = %+v, want a dry run that still decided", deploy)
	}
	if move := moveOf(t, deploy, "#11"); !move.Moves() {
		t.Fatalf("#11 decided %+v, want the move it would have made", move)
	}
	if n := world.tracker.Calls("Move"); n != 0 {
		t.Fatalf("Move called %d times, want the board untouched", n)
	}
	if got := world.column(t, "#11"); got != "In review" {
		t.Fatalf("#11 is in %q, want it where it was", got)
	}
}

// countingComparer answers every ancestry the same way and says how often it
// was asked. It stands in for [core.Ancestry], which the command puts in front
// of the forge so that a pair is only asked about once (spec 13 §3).
type countingComparer struct {
	status string
	calls  int
}

func (c *countingComparer) Compare(context.Context, string, string) (core.Comparison, error) {
	c.calls++
	return core.Comparison{Status: c.status}, nil
}

// TestDeployAsksWhoeverItWasGiven is what the ancestry cache is wired through.
// The comparer in the input answers instead of the forge, and the forge is not
// asked at all.
func TestDeployAsksWhoeverItWasGiven(t *testing.T) {
	world := deployWorldFixture(t)
	comparer := &countingComparer{status: core.CompareIdentical}
	deploy := world.run(t, func(in *engine.DeployInput) { in.Ancestry = comparer })

	if len(deploy.PRs) != 3 {
		t.Fatalf("PRs = %+v, want every merge the given comparer called running", deploy.PRs)
	}
	if comparer.calls != 3 {
		t.Fatalf("the comparer was asked %d times, want once per merge", comparer.calls)
	}
	if n := world.forge.Calls("Compare"); n != 0 {
		t.Fatalf("Compare called %d times on the forge, want it asked through the cache only", n)
	}
}

// TestDeployRefusesAnAncestryItCannotRead is spec 13's rule for a word flow
// does not know, as §2's comparison already has it: guessing would either hide
// a deployment or invent one.
func TestDeployRefusesAnAncestryItCannotRead(t *testing.T) {
	world := deployWorldFixture(t)
	_, err := engine.RunDeploy(context.Background(), func() engine.DeployInput {
		in := world.input()
		in.Ancestry = &countingComparer{status: "unrelated"}
		return in
	}())
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("RunDeploy returned %v, want ErrUnavailable", err)
	}
}

// TestDeployAsksAboutEachTicketOnce is two pull requests closing one ticket.
// The board is asked once and decided once: a ticket does not get two
// opinions because its work came in two parts.
func TestDeployAsksAboutEachTicketOnce(t *testing.T) {
	world := deployWorldFixture(t)
	world.forge = coretest.NewFakeForge(coretest.ForgeFixture{
		PullRequests: []core.PullRequest{
			deployPR(1, mergeOne, "#11", deployNow.Add(-30*time.Minute)),
			deployPR(2, mergeTwo, "#11", deployNow.Add(-20*time.Minute)),
		},
		Commits: []core.Commit{{SHA: deployedSHA}, {SHA: mergeTwo}, {SHA: mergeOne}},
	})
	deploy := world.run(t)

	if len(deploy.Moves) != 1 {
		t.Fatalf("moves = %+v, want one decision about one ticket", deploy.Moves)
	}
	if n := world.tracker.Calls("Ticket"); n != 1 {
		t.Fatalf("Ticket called %d times, want once", n)
	}
	if got := world.column(t, "#11"); got != "Done" {
		t.Fatalf("#11 is in %q, want Done", got)
	}
}

// TestDeployExplainsWhatItCouldNotRead keeps a provider's refusal attached to
// the question flow was asking.
func TestDeployExplainsWhatItCouldNotRead(t *testing.T) {
	refused := core.Errorf(core.ErrUnavailable, "answer").WithWhy("the provider is down")
	cases := map[string]func(*deployWorld){
		"the deployments": func(w *deployWorld) { w.deploys.Fail("Deployments", refused) },
		"the merges":      func(w *deployWorld) { w.forge.Fail("PullRequests", refused) },
		"the ticket":      func(w *deployWorld) { w.tracker.Fail("Ticket", refused) },
		"the move":        func(w *deployWorld) { w.tracker.Fail("Move", refused) },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			world := deployWorldFixture(t)
			breakIt(world)
			if _, err := engine.RunDeploy(context.Background(), world.input()); !errors.Is(err, core.ErrUnavailable) {
				t.Fatalf("RunDeploy returned %v, want ErrUnavailable", err)
			}
		})
	}
}

// TestDeployJSONIsOneDocument pins the wire format of `check deploy --json`
// (spec 02 §3): one document, its schema named, and lists that are empty
// rather than absent.
func TestDeployJSONIsOneDocument(t *testing.T) {
	world := deployWorldFixture(t)
	deploy := world.run(t)
	raw, err := deploy.JSON()
	if err != nil {
		t.Fatalf("render JSON: %v", err)
	}
	var doc struct {
		Schema      string `json:"schema"`
		SHA         string `json:"sha"`
		Environment string `json:"environment"`
		Watched     bool   `json:"watched"`
		Confirmed   bool   `json:"confirmed"`
		Acted       bool   `json:"acted"`
		PRs         []struct {
			Number   int    `json:"pr"`
			MergeSHA string `json:"merge_sha"`
		} `json:"prs"`
		Moves []struct {
			Key   string `json:"ticket"`
			To    string `json:"to"`
			Event string `json:"event"`
		} `json:"moves"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if doc.Schema != engine.DeployVersion {
		t.Fatalf("schema = %q, want %q", doc.Schema, engine.DeployVersion)
	}
	if doc.SHA != deployedSHA || doc.Environment != "production" {
		t.Fatalf("doc = %+v, want it to name the deployment", doc)
	}
	if !doc.Watched || !doc.Confirmed || !doc.Acted {
		t.Fatalf("doc = %+v, want a run that acted", doc)
	}
	if len(doc.PRs) != 2 || doc.PRs[0].MergeSHA != mergeTwo {
		t.Fatalf("prs = %+v, want the merges it is running", doc.PRs)
	}
	if len(doc.Moves) != 2 || doc.Moves[0].To != "done" || doc.Moves[0].Event != config.EventDeployed {
		t.Fatalf("moves = %+v, want the transitions it made", doc.Moves)
	}

	// A run with nothing to say still says it in the same shape.
	idle, err := engine.RunDeploy(context.Background(), engine.DeployInput{SHA: deployedSHA, Config: preset(t, "lean-kanban")})
	if err != nil {
		t.Fatalf("react to the deployment: %v", err)
	}
	quiet, err := idle.JSON()
	if err != nil {
		t.Fatalf("render JSON: %v", err)
	}
	for _, want := range []string{`"prs": []`, `"moves": []`, `"acted": false`} {
		if !strings.Contains(string(quiet), want) {
			t.Fatalf("quiet document %s, want it to contain %s", quiet, want)
		}
	}
}

// TestDeployTextSaysWhatItFinished is the terminal rendering: what went out,
// what it was running, and where that took the board.
func TestDeployTextSaysWhatItFinished(t *testing.T) {
	world := deployWorldFixture(t)
	text := world.run(t).Text()
	for _, want := range []string{"deployment d000000 → production", "#1 is running", "→ #11  in_review → done (deployed)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text = %q, want it to contain %q", text, want)
		}
	}

	dry := world.run(t, func(in *engine.DeployInput) { in.DryRun = true }).Text()
	if !strings.Contains(dry, "dry run: nothing was moved") {
		t.Fatalf("dry run text = %q, want it to say nothing moved", dry)
	}
}

// TestDeployTextSaysWhyItDidNothing is the common case, and the one a person
// reading a CI log most needs explained.
func TestDeployTextSaysWhyItDidNothing(t *testing.T) {
	deploy, err := engine.RunDeploy(context.Background(), engine.DeployInput{
		SHA:    deployedSHA,
		Config: preset(t, "lean-kanban"),
	})
	if err != nil {
		t.Fatalf("react to the deployment: %v", err)
	}
	text := deploy.Text()
	if !strings.HasPrefix(text, "deployment d000000 → production\n") {
		t.Fatalf("text = %q, want it to name the deployment first", text)
	}
	if !strings.Contains(text, "nothing to do: workflow.done_on is merged") {
		t.Fatalf("text = %q, want it to say why", text)
	}
}

// TestDeployTextSaysWhyATicketStayed explains a ticket this deployment
// finished work on that is still not done.
func TestDeployTextSaysWhyATicketStayed(t *testing.T) {
	world := deployWorldFixture(t)
	world.forge.SetPullRequest(core.PullRequest{
		Number:        4,
		State:         core.PRStateOpen,
		BaseRef:       "main",
		ClosingIssues: []string{"#11"},
		UpdatedAt:     deployNow,
	})
	text := world.run(t).Text()
	if !strings.Contains(text, "#4") || !strings.Contains(text, "#11") {
		t.Fatalf("text = %q, want it to say which open pull request holds #11", text)
	}
}
