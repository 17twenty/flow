package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/core/coretest"
	"github.com/17twenty/flow/internal/engine"
)

// answering builds a check that reports a fixed result, for the tests that are
// about the frame rather than about any question.
func answering(id, category string, result engine.Result, dependsOn ...string) engine.Check {
	return engine.Check{
		ID:        id,
		Title:     "whether " + id,
		Category:  category,
		DependsOn: dependsOn,
		Run:       func(context.Context, *engine.Env) engine.Result { return result },
	}
}

// doctorEnv is the smallest world a check can be asked in.
func doctorEnv(t *testing.T) *engine.Env {
	t.Helper()
	return &engine.Env{
		Clock:     coretest.NewClock(time.Date(2025, 3, 4, 9, 0, 0, 0, time.UTC)),
		Window:    14 * 24 * time.Hour,
		FailBelow: 0.9,
	}
}

// doctorRun asks the checks and fails the test if the run could not be made.
func doctorRun(t *testing.T, env *engine.Env, only []string, checks ...engine.Check) engine.Report {
	t.Helper()
	registry, err := engine.NewRegistry(checks...)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}
	report, err := engine.RunDoctor(t.Context(), engine.DoctorInput{
		Registry: registry, Env: env, Only: only, FlowVersion: "v0.1.0-test",
	})
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	return report
}

// state is the state a report gave one check, for a terse assertion.
func state(t *testing.T, report engine.Report, id string) engine.State {
	t.Helper()
	checked, ok := report.Find(id)
	if !ok {
		t.Fatalf("the report has no %s: %+v", id, report.Checks)
	}
	return checked.Result.State
}

// TestBlockedCarriesDownAChain is spec 07 §2's blocked propagation: the one
// thing that is wrong is reported once, and everything standing behind it says
// so rather than guessing.
func TestBlockedCarriesDownAChain(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("config.syntax", engine.CategoryConfig, engine.Fail(".flow.yaml does not parse")),
		answering("tracker.project", engine.CategoryTracker, engine.Pass("the board is there"), "config.syntax"),
		answering("tracker.status_options", engine.CategoryTracker, engine.Pass("the columns are right"), "tracker.project"),
		answering("tracker.wip", engine.CategoryTracker, engine.Pass("the limits are set"), "tracker.status_options"),
		answering("local.git", engine.CategoryLocal, engine.Pass("this is a repository")),
	)

	for _, id := range []string{"tracker.project", "tracker.status_options", "tracker.wip"} {
		if got := state(t, report, id); got != engine.StateBlocked {
			t.Errorf("%s is %s, want blocked", id, got)
		}
	}
	if got := state(t, report, "local.git"); got != engine.StatePass {
		t.Errorf("local.git is %s, want pass: nothing it needs failed", got)
	}

	blocked, _ := report.Find("tracker.wip")
	if blocked.BlockedBy != "tracker.status_options" {
		t.Errorf("tracker.wip is blocked by %q, want the check it depends on", blocked.BlockedBy)
	}
}

// TestBlockedIsNotAFailure states the reason blocked exits 0: the dependency
// carries the signal, and counting both would say one problem twice.
func TestBlockedIsNotAFailure(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("auth.forge", engine.CategoryAuth, engine.Skip("no forge is configured")),
		answering("forge.repo", engine.CategoryForge, engine.Pass("the repository is there"), "auth.forge"),
	)
	if got := state(t, report, "forge.repo"); got != engine.StatePass {
		t.Fatalf("forge.repo is %s, want pass: a skipped dependency has nothing to say about it", got)
	}
	if got := report.Exit(); got != core.ExitOK {
		t.Errorf("exit %d, want %d", got, core.ExitOK)
	}
}

// TestADependencyThatWasNotRunDoesNotBlock: --check is the user narrowing the
// question, not flow losing confidence in the answer.
func TestADependencyThatWasNotRunDoesNotBlock(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), []string{"forge.*"},
		answering("auth.forge", engine.CategoryAuth, engine.Fail("the token is not set")),
		answering("forge.repo", engine.CategoryForge, engine.Pass("the repository is there"), "auth.forge"),
		answering("forge.protection", engine.CategoryForge, engine.Pass("main is protected"), "forge.repo"),
	)
	if len(report.Checks) != 2 {
		t.Fatalf("the report has %d checks, want the two that were asked for", len(report.Checks))
	}
	if got := state(t, report, "forge.repo"); got != engine.StatePass {
		t.Errorf("forge.repo is %s, want pass", got)
	}
}

// TestSelectChoosesByGlob covers spec 02 §2's --check, including the pattern
// the CI run uses.
func TestSelectChoosesByGlob(t *testing.T) {
	registry, err := engine.NewRegistry(
		answering("config.syntax", engine.CategoryConfig, engine.Pass("")),
		answering("config.secrets", engine.CategoryConfig, engine.Pass("")),
		answering("lock.consistent", engine.CategoryConfig, engine.Pass("")),
		answering("live.prs.linked", engine.CategoryLive, engine.Pass("")),
		answering("live.prs.gated", engine.CategoryLive, engine.Pass("")),
		answering("live.wip", engine.CategoryLive, engine.Pass("")),
	)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}

	for _, tc := range []struct {
		name  string
		globs []string
		want  []string
	}{
		{"everything", nil, []string{"config.syntax", "config.secrets", "lock.consistent", "live.prs.linked", "live.prs.gated", "live.wip"}},
		{"one prefix", []string{"config.*"}, []string{"config.syntax", "config.secrets"}},
		{"the ci set", []string{"config.*", "lock.*"}, []string{"config.syntax", "config.secrets", "lock.consistent"}},
		{"two dots", []string{"live.prs.*"}, []string{"live.prs.linked", "live.prs.gated"}},
		{"an exact id", []string{"live.wip"}, []string{"live.wip"}},
		{"overlapping globs choose a check once", []string{"config.*", "config.syntax"}, []string{"config.syntax", "config.secrets"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chosen, err := registry.Select(tc.globs)
			if err != nil {
				t.Fatalf("select %v: %v", tc.globs, err)
			}
			var ids []string
			for _, check := range chosen {
				ids = append(ids, check.ID)
			}
			if fmt.Sprint(ids) != fmt.Sprint(tc.want) {
				t.Errorf("select %v chose %v, want %v", tc.globs, ids, tc.want)
			}
		})
	}
}

// TestSelectRefusesAGlobThatMatchesNothing: running nothing and exiting 0 is
// indistinguishable from health, which is the one answer doctor must never
// give by accident.
func TestSelectRefusesAGlobThatMatchesNothing(t *testing.T) {
	registry, err := engine.NewRegistry(answering("config.syntax", engine.CategoryConfig, engine.Pass("")))
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}
	_, err = registry.Select([]string{"config.*", "trackr.*"})
	if !errors.Is(err, core.ErrValidation) {
		t.Fatalf("select a glob that matches nothing: %v, want a validation error", err)
	}
	if got := core.ExitCode(err); got != core.ExitUsage {
		t.Errorf("it exits %d, want %d", got, core.ExitUsage)
	}
}

// TestRegistryRefusesWhatItCouldNotRun. A registry is built at startup, and a
// doctor that falls over half way through has told a worse story than one that
// would not start.
func TestRegistryRefusesWhatItCouldNotRun(t *testing.T) {
	ok := answering("config.syntax", engine.CategoryConfig, engine.Pass(""))
	for _, tc := range []struct {
		name   string
		checks []engine.Check
	}{
		{"no id", []engine.Check{answering("", engine.CategoryConfig, engine.Pass(""))}},
		{"no title", []engine.Check{{ID: "a.b", Category: engine.CategoryConfig, Run: ok.Run}}},
		{"nothing to run", []engine.Check{{ID: "a.b", Title: "a", Category: engine.CategoryConfig}}},
		{"a category nobody renders", []engine.Check{answering("a.b", "invented", engine.Pass(""))}},
		{"the same id twice", []engine.Check{ok, ok}},
		{"a dependency that is not a check", []engine.Check{answering("a.b", engine.CategoryConfig, engine.Pass(""), "nowhere")}},
		{"a check that depends on itself", []engine.Check{answering("a.b", engine.CategoryConfig, engine.Pass(""), "a.b")}},
		{"two checks in a circle", []engine.Check{
			answering("a.b", engine.CategoryConfig, engine.Pass(""), "c.d"),
			answering("c.d", engine.CategoryConfig, engine.Pass(""), "a.b"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := engine.NewRegistry(tc.checks...); !errors.Is(err, core.ErrValidation) {
				t.Fatalf("the registry was built: %v, want a validation error", err)
			}
		})
	}
}

// TestOrderPutsDependenciesFirst: registration order is already dependency
// order when whoever wrote the list was careful, and sorting anyway means
// nobody has to be.
func TestOrderPutsDependenciesFirst(t *testing.T) {
	registry, err := engine.NewRegistry(
		answering("tracker.wip", engine.CategoryTracker, engine.Pass(""), "tracker.status_options"),
		answering("tracker.status_options", engine.CategoryTracker, engine.Pass(""), "tracker.project"),
		answering("tracker.project", engine.CategoryTracker, engine.Pass(""), "auth.tracker"),
		answering("auth.tracker", engine.CategoryAuth, engine.Pass("")),
	)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}
	ordered, err := registry.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	var ids []string
	for _, check := range ordered {
		ids = append(ids, check.ID)
	}
	want := "[auth.tracker tracker.project tracker.status_options tracker.wip]"
	if fmt.Sprint(ids) != want {
		t.Errorf("the order is %v, want %s", ids, want)
	}
}

// TestExitAggregation is spec 07 §2's table, state by state and in the
// combinations that make it interesting.
func TestExitAggregation(t *testing.T) {
	remedied := func(result engine.Result) engine.Result {
		return result.Fix(engine.Remediation{Kind: engine.RemedyAuto, Summary: "flow apply"})
	}
	for _, tc := range []struct {
		name    string
		results []engine.Result
		want    int
	}{
		{"nothing at all", nil, core.ExitOK},
		{"all pass", []engine.Result{engine.Pass("yes"), engine.Pass("yes")}, core.ExitOK},
		{"a skip", []engine.Result{engine.Pass("yes"), engine.Skip("not configured")}, core.ExitOK},
		{"an actionable failure", []engine.Result{remedied(engine.Fail("no"))}, core.ExitActionRequired},
		{"a hard failure", []engine.Result{engine.Fail("no")}, core.ExitFail},
		{"a failure with an explicitly empty remedy", []engine.Result{
			engine.Fail("no").Fix(engine.Remediation{Kind: engine.RemedyNone}),
		}, core.ExitFail},
		{"a warning", []engine.Result{{State: engine.StateWarn, Message: "8 of 10"}}, core.ExitActionRequired},
		{"needs input", []engine.Result{{State: engine.StateInput}}, core.ExitActionRequired},
		{"waiting on somebody", []engine.Result{{State: engine.StateExternal}}, core.ExitActionRequired},
		{"an attestation due", []engine.Result{{State: engine.StateAttest}}, core.ExitActionRequired},
		{"drift", []engine.Result{{State: engine.StateDrift}}, core.ExitActionRequired},
		{"a hard failure outranks an actionable one", []engine.Result{
			remedied(engine.Fail("no")), engine.Fail("also no"),
		}, core.ExitFail},
		{"an error alone is a failure", []engine.Result{engine.Failed(errors.New("who knows"))}, core.ExitFail},
		{"nothing but unavailable is weather", []engine.Result{
			engine.Failed(fmt.Errorf("github: %w", core.ErrUnavailable)),
			engine.Failed(fmt.Errorf("the project board: %w", core.ErrRateLimited)),
			engine.Pass("yes"),
		}, core.ExitUnavailable},
		{"nothing but permission is the token", []engine.Result{
			engine.Failed(fmt.Errorf("protection: %w", core.ErrPermission)),
			engine.Pass("yes"),
		}, core.ExitNoPerm},
		{"unavailable and permission together are a failure", []engine.Result{
			engine.Failed(fmt.Errorf("github: %w", core.ErrUnavailable)),
			engine.Failed(fmt.Errorf("protection: %w", core.ErrPermission)),
		}, core.ExitFail},
		{"a real problem behind an unreachable provider is not weather", []engine.Result{
			engine.Failed(fmt.Errorf("github: %w", core.ErrUnavailable)),
			remedied(engine.Fail("the columns are wrong")),
		}, core.ExitFail},
		{"an attestation behind an unreachable provider is not weather", []engine.Result{
			engine.Failed(fmt.Errorf("github: %w", core.ErrUnavailable)),
			{State: engine.StateAttest},
		}, core.ExitFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := make([]engine.Check, 0, len(tc.results))
			for i, result := range tc.results {
				checks = append(checks, answering(fmt.Sprintf("meta.check%d", i), engine.CategoryMeta, result))
			}
			report := doctorRun(t, doctorEnv(t), nil, checks...)
			if got := report.Exit(); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
			// And the document says so too. Something reading `--json`
			// instead of the process's status must not have to re-derive
			// the verdict from the states.
			if got := exitCodeOf(t, report); got != tc.want {
				t.Errorf("the document says exit %d, want %d", got, tc.want)
			}
		})
	}
}

// exitCodeOf is the exit code as the JSON document carries it.
func exitCodeOf(t *testing.T, report engine.Report) int {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal the report: %v", err)
	}
	var document struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("read the report back: %v", err)
	}
	return document.ExitCode
}

// TestSummaryCountsWhoMustAct is spec 07 §9's last line.
func TestSummaryCountsWhoMustAct(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("config.syntax", engine.CategoryConfig, engine.Pass("it parses")),
		answering("config.secrets", engine.CategoryConfig, engine.Fail("a token is written in the file").
			Fix(engine.Remediation{Kind: engine.RemedyManual, Summary: "move it to the environment"})),
		answering("tracker.wip", engine.CategoryTracker, engine.Result{State: engine.StateExternal, Message: "asked in FLOW-12"}),
		answering("tracker.expedite", engine.CategoryTracker, engine.Result{State: engine.StateAttest}),
		answering("forge.protection", engine.CategoryForge, engine.Result{State: engine.StateDrift}),
		answering("tracker.project", engine.CategoryTracker, engine.Result{State: engine.StateInput}),
		answering("live.wip", engine.CategoryLive, engine.Result{State: engine.StateWarn, Counts: &engine.Counts{Pass: 13, Total: 14}}),
	)
	want := "4 actions for you · 1 waiting on others · 1 attestation due · exit 2"
	if got := report.Summary(); got != want {
		t.Errorf("the summary is %q, want %q", got, want)
	}
}

// TestSummaryIsSingularForOne, because a tool that says "1 actions" is a tool
// nobody trusts with their branch protection.
func TestSummaryIsSingularForOne(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("config.syntax", engine.CategoryConfig, engine.Fail("no").
			Fix(engine.Remediation{Kind: engine.RemedyAuto, Summary: "flow apply"})),
	)
	want := "1 action for you · 0 waiting on others · 0 attestations due · exit 2"
	if got := report.Summary(); got != want {
		t.Errorf("the summary is %q, want %q", got, want)
	}
}

// TestGradeAtTheThresholds is spec 07 §6's bands.
func TestGradeAtTheThresholds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		counts   engine.Counts
		critical bool
		want     engine.State
	}{
		{"everything held", engine.Counts{Pass: 10, Total: 10}, false, engine.StatePass},
		{"nothing to look at", engine.Counts{}, false, engine.StatePass},
		{"one short", engine.Counts{Pass: 9, Total: 10}, false, engine.StateWarn},
		{"at the threshold", engine.Counts{Pass: 90, Total: 100}, false, engine.StateWarn},
		{"below the threshold", engine.Counts{Pass: 89, Total: 100}, false, engine.StateFail},
		{"critical has no warning band", engine.Counts{Pass: 9, Total: 10}, true, engine.StateFail},
		{"critical and perfect still passes", engine.Counts{Pass: 10, Total: 10}, true, engine.StatePass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := engine.Grade(tc.counts, 0.9, tc.critical); got != tc.want {
				t.Errorf("grade %v is %s, want %s", tc.counts, got, tc.want)
			}
		})
	}
}

// TestACheckThatPanicsIsAnError: a doctor that dies on one unexpected nil says
// nothing about the twenty things that were fine.
func TestACheckThatPanicsIsAnError(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		engine.Check{
			ID: "forge.repo", Title: "whether the repository is there", Category: engine.CategoryForge,
			Run: func(context.Context, *engine.Env) engine.Result { panic("nil pointer somewhere") },
		},
		answering("local.git", engine.CategoryLocal, engine.Pass("this is a repository")),
	)
	if got := state(t, report, "forge.repo"); got != engine.StateError {
		t.Fatalf("a check that panicked is %s, want error", got)
	}
	if got := state(t, report, "local.git"); got != engine.StatePass {
		t.Errorf("the next check is %s, want pass: the run carried on", got)
	}
}

// TestACheckThatAnswersNothingIsAnError. The zero State is not a state, and
// letting it read as a pass would make a half-written check look like health.
func TestACheckThatAnswersNothingIsAnError(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		engine.Check{
			ID: "meta.silent", Title: "a check that forgot", Category: engine.CategoryMeta,
			Run: func(context.Context, *engine.Env) engine.Result { return engine.Result{} },
		},
	)
	if got := state(t, report, "meta.silent"); got != engine.StateError {
		t.Errorf("a check that answered nothing is %s, want error", got)
	}
}

// TestAChecksSeesWhatRanBeforeIt, so that followups.health does not have to
// ask the tracker everything a second time.
func TestAChecksSeesWhatRanBeforeIt(t *testing.T) {
	var saw engine.State
	report := doctorRun(t, doctorEnv(t), nil,
		answering("tracker.project", engine.CategoryTracker, engine.Pass("the board is there")),
		engine.Check{
			ID: "followups.health", Title: "whether the follow-ups are moving",
			Category: engine.CategoryFollowUps, DependsOn: []string{"tracker.project"},
			Run: func(_ context.Context, env *engine.Env) engine.Result {
				result, ok := env.Result("tracker.project")
				if !ok {
					return engine.Fail("the earlier result was not there")
				}
				saw = result.State
				return engine.Pass("nothing is stuck")
			},
		},
	)
	if saw != engine.StatePass {
		t.Errorf("the later check saw %q, want the pass that ran before it", saw)
	}
	if got := state(t, report, "followups.health"); got != engine.StatePass {
		t.Errorf("followups.health is %s, want pass", got)
	}
}

// TestRunStopsWhenTheContextIsCancelled, because a doctor nobody is waiting
// for should not keep calling a provider.
func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	registry, err := engine.NewRegistry(
		answering("config.syntax", engine.CategoryConfig, engine.Pass("it parses")),
		answering("config.secrets", engine.CategoryConfig, engine.Pass("no secrets")),
	)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := engine.RunDoctor(ctx, engine.DoctorInput{Registry: registry, Env: doctorEnv(t)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run a cancelled doctor: %v, want a cancellation", err)
	}
	if len(report.Checks) != 0 {
		t.Errorf("it asked %d checks, want none", len(report.Checks))
	}
}

// TestRunRefusesWithoutARegistryOrAnEnv.
func TestRunRefusesWithoutARegistryOrAnEnv(t *testing.T) {
	if _, err := engine.RunDoctor(t.Context(), engine.DoctorInput{Env: doctorEnv(t)}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("run with no registry: %v, want a validation error", err)
	}
	registry, err := engine.NewRegistry()
	if err != nil {
		t.Fatalf("build an empty registry: %v", err)
	}
	if _, err := engine.RunDoctor(t.Context(), engine.DoctorInput{Registry: registry}); !errors.Is(err, core.ErrValidation) {
		t.Errorf("run with no environment: %v, want a validation error", err)
	}
}

// TestReportErrNamesTheCheck, so that a caller which only wants to know what
// went wrong does not have to walk the report.
func TestReportErrNamesTheCheck(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("auth.tracker", engine.CategoryAuth, engine.Failed(fmt.Errorf("the project board: %w", core.ErrRateLimited))),
	)
	err := report.Err()
	if err == nil || !errors.Is(err, core.ErrRateLimited) {
		t.Fatalf("the report's error is %v, want the rate limit", err)
	}
	if want := "auth.tracker: the project board: rate limited"; err.Error() != want {
		t.Errorf("the error reads %q, want %q", err, want)
	}
}

// TestDoctorGolden pins what a run of every state looks like, in plain text
// and as the JSON document `--json` prints.
//
// Run go test ./internal/engine -run TestDoctorGolden -update to rewrite them.
func TestDoctorGolden(t *testing.T) {
	env := doctorEnv(t)
	report := doctorRun(t, env, nil, everyState()...)

	rendered, err := report.JSON()
	if err != nil {
		t.Fatalf("render the report as JSON: %v", err)
	}
	goldenFile(t, filepath.Join("doctor", "report.txt"), []byte(report.Text()))
	goldenFile(t, filepath.Join("doctor", "report.json"), rendered)

	var panes strings.Builder
	for _, checked := range report.Checks {
		panes.WriteString(checked.Detail())
		panes.WriteString("\n")
	}
	goldenFile(t, filepath.Join("doctor", "detail.txt"), []byte(panes.String()))
}

// TestAPaneShowsEverythingTheCheckSaw states the one way a detail pane
// differs from the report it was opened out of. A report caps the evidence
// because scrolling a hundred pull requests past a reader tells them nothing;
// a pane somebody opened on one check, and can scroll, is the case that cap
// was protecting them from.
func TestAPaneShowsEverythingTheCheckSaw(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("live.prs.gated", engine.CategoryLive, liveFail()))
	checked, ok := report.Find("live.prs.gated")
	if !ok {
		t.Fatal("the report has no live.prs.gated")
	}
	if len(checked.Result.Evidence) <= engine.EvidenceShown {
		t.Fatalf("the fixture has %d pieces of evidence, which the report would not have capped",
			len(checked.Result.Evidence))
	}

	pane := checked.Detail()
	for _, item := range checked.Result.Evidence {
		if !strings.Contains(pane, item.Text) {
			t.Errorf("the pane leaves out %q:\n%s", item.Text, pane)
		}
	}
	if strings.Contains(pane, "and 2 more") {
		t.Errorf("the pane counted evidence it was showing:\n%s", pane)
	}
	if !strings.Contains(report.Text(), "and 2 more") {
		t.Errorf("the report stopped capping its evidence:\n%s", report.Text())
	}
}

// TestAPaneNamesTheQuestionItAnswers states that the pane carries the check's
// title. A report is a list of findings and the id is enough to tell them
// apart; a pane is one finding on its own, and "forge.protection" is not a
// sentence anybody can act on.
func TestAPaneNamesTheQuestionItAnswers(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("forge.protection", engine.CategoryForge, engine.Pass("main is protected")))
	checked, _ := report.Find("forge.protection")

	pane := checked.Detail()
	if !strings.Contains(pane, checked.Title) {
		t.Errorf("the pane does not say what was asked (%q):\n%s", checked.Title, pane)
	}
	if !strings.Contains(pane, checked.Line()) {
		t.Errorf("the pane does not lead with the finding (%q):\n%s", checked.Line(), pane)
	}
}

// everyState is one check in each state, with the evidence and remedies the
// renderer has to lay out.
func everyState() []engine.Check {
	return []engine.Check{
		answering("config.syntax", engine.CategoryConfig, engine.Pass(".flow.yaml parses")),
		answering("config.secrets", engine.CategoryConfig,
			engine.Fail("a credential is written into .flow.yaml").
				Fix(engine.Remediation{
					Kind:    engine.RemedyManual,
					Summary: "move it to the environment and rotate it",
					Steps:   []string{"rotate the token at the provider", "set GITHUB_TOKEN", "delete the line"},
				})),
		answering("local.hook", engine.CategoryLocal, engine.Skip("hooks are off in this configuration")),
		answering("auth.tracker", engine.CategoryAuth,
			engine.Failed(fmt.Errorf("the project board: %w", core.ErrRateLimited))),
		answering("tracker.project", engine.CategoryTracker,
			engine.Result{
				State:       engine.StateInput,
				Message:     "flow does not know which board this is",
				Remediation: engine.Remediation{Kind: engine.RemedyInput, Summary: "choose a project", Command: "flow doctor --fix"},
			}),
		answering("tracker.status_options", engine.CategoryTracker,
			engine.Result{State: engine.StateBlocked, Message: "not asked"}, "tracker.project"),
		answering("tracker.wip", engine.CategoryTracker,
			engine.Result{
				State:       engine.StateExternal,
				Message:     "a work-in-progress limit needs a board admin",
				Evidence:    []engine.Evidence{{Text: "FLOW-12 flow: set the WIP limits (17twenty/flow)", URL: "https://example.test/FLOW-12"}},
				Remediation: engine.Remediation{Kind: engine.RemedyFollowUp, Summary: "waiting on the follow-up", Owner: "@board-admin"},
			}),
		answering("forge.protection", engine.CategoryForge,
			engine.Result{
				State:        engine.StateDrift,
				Message:      "main no longer requires a linear history",
				ObservedHash: "sha256:9f2c",
				Remediation:  engine.Remediation{Kind: engine.RemedyAuto, Summary: "put it back", Command: "flow apply"},
			}),
		answering("forge.merge_settings", engine.CategoryForge,
			engine.Result{
				State:       engine.StateAttest,
				Message:     "flow cannot read the merge settings with this token",
				Remediation: engine.Remediation{Kind: engine.RemedyAttest, Summary: "say it is so", Command: "flow attest forge.merge_settings"},
			}),
		answering("live.prs.linked", engine.CategoryLive,
			engine.Result{
				State:   engine.StateWarn,
				Message: "merged pull requests linking a ticket",
				Counts:  &engine.Counts{Pass: 8, Total: 10},
				Evidence: []engine.Evidence{
					{Text: "#101 fix: stop the retry storm", URL: "https://example.test/pr/101"},
					{Text: "#104 chore: tidy the Makefile", URL: "https://example.test/pr/104"},
				},
			}),
		answering("live.prs.gated", engine.CategoryLive, liveFail()),
		answering("followups.health", engine.CategoryFollowUps, engine.Pass("no follow-up has been open longer than a week")),
	}
}

// liveFail is a live check with more evidence than a terminal shows.
func liveFail() engine.Result {
	result := engine.Result{
		State:   engine.StateFail,
		Message: "merged pull requests carrying a flow/policy status",
		Counts:  &engine.Counts{Pass: 2, Total: 9},
		Remediation: engine.Remediation{
			Kind:    engine.RemedyManual,
			Summary: "the gate is not running on every pull request",
		},
	}
	for i := 110; i < 117; i++ {
		result = result.Saw(fmt.Sprintf("#%d merged with no status", i), fmt.Sprintf("https://example.test/pr/%d", i))
	}
	return result
}

// TestARunSaysWhereItHasGotTo states what a progress display is given: each
// check twice, once as it is reached and once with its answer, and a count it
// can draw a proportion from. The first of the pair is the one that matters —
// the gap between reaching a live check and hearing back from it is where a
// run spends its time, and a screen told only about answers would be silent
// for exactly that wait.
func TestARunSaysWhereItHasGotTo(t *testing.T) {
	registry, err := engine.NewRegistry(
		answering("config.syntax", engine.CategoryConfig, engine.Pass("it parses")),
		answering("local.git", engine.CategoryLocal, engine.Fail("this is not a repository")),
		answering("local.hooks", engine.CategoryLocal, engine.Pass("the hooks are in"), "local.git"),
	)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}

	var steps []engine.DoctorStep
	report, err := engine.RunDoctor(t.Context(), engine.DoctorInput{
		Registry: registry,
		Env:      doctorEnv(t),
		Progress: func(step engine.DoctorStep) { steps = append(steps, step) },
	})
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}

	if len(steps) != 2*len(report.Checks) {
		t.Fatalf("%d steps for %d checks, want two each: %+v", len(steps), len(report.Checks), steps)
	}
	for i, checked := range report.Checks {
		asking, answered := steps[2*i], steps[2*i+1]
		if asking.Check.ID != checked.ID || answered.Check.ID != checked.ID {
			t.Fatalf("step %d is about %q and %q, want %q", i, asking.Check.ID, answered.Check.ID, checked.ID)
		}
		if asking.Answered() {
			t.Errorf("%s was reported as answered before it was asked: %+v", checked.ID, asking.Result)
		}
		if !answered.Answered() {
			t.Errorf("%s answered %q and the step says it did not", checked.ID, checked.Result.State)
		}
		if answered.Result.State != checked.Result.State {
			t.Errorf("%s answered %q to the screen and %q to the report",
				checked.ID, answered.Result.State, checked.Result.State)
		}
		if asking.Asked != i || answered.Asked != i+1 {
			t.Errorf("%s was asked %d/%d and answered %d/%d, want %d then %d",
				checked.ID, asking.Asked, asking.Total, answered.Asked, answered.Total, i, i+1)
		}
		if asking.Total != len(report.Checks) || answered.Total != len(report.Checks) {
			t.Errorf("%s says the run has %d checks, want %d", checked.ID, asking.Total, len(report.Checks))
		}
	}
}

// TestABlockedCheckIsStillReached states that a check nobody asked is still
// a step. It appears in the report, so a screen that drew only the checks it was
// told about would be missing rows the report has — and the reason a check was
// not asked is one of the more useful things on the screen.
func TestABlockedCheckIsStillReached(t *testing.T) {
	registry, err := engine.NewRegistry(
		answering("local.git", engine.CategoryLocal, engine.Fail("this is not a repository")),
		answering("local.hooks", engine.CategoryLocal, engine.Pass("the hooks are in"), "local.git"),
	)
	if err != nil {
		t.Fatalf("build the registry: %v", err)
	}

	var answered []engine.DoctorStep
	if _, err := engine.RunDoctor(t.Context(), engine.DoctorInput{
		Registry: registry,
		Env:      doctorEnv(t),
		Progress: func(step engine.DoctorStep) {
			if step.Answered() {
				answered = append(answered, step)
			}
		},
	}); err != nil {
		t.Fatalf("run doctor: %v", err)
	}

	if len(answered) != 2 {
		t.Fatalf("%d checks answered, want 2: %+v", len(answered), answered)
	}
	if got := answered[1].Result.State; got != engine.StateBlocked {
		t.Errorf("local.hooks answered %q to the screen, want blocked", got)
	}
}

// TestNobodyWatchingIsNotAFailure states that the hook is optional. Every
// command but a drawn one passes no progress at all, and a run that dialled a
// nil function would be every run flow makes in CI.
func TestNobodyWatchingIsNotAFailure(t *testing.T) {
	report := doctorRun(t, doctorEnv(t), nil,
		answering("config.syntax", engine.CategoryConfig, engine.Pass("it parses")))
	if len(report.Checks) != 1 {
		t.Fatalf("%d checks, want 1", len(report.Checks))
	}
}
