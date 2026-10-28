package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// DoctorCmd proves the setup is intact and that the workflow is actually
// behaving, using read APIs only (spec 02 §2, spec 07).
type DoctorCmd struct {
	Fix       bool     `help:"Apply automatic remediations, resolve placeholders, file follow-ups."`
	Set       []string `help:"Supply a placeholder value, as name=value." placeholder:"K=V"`
	Check     []string `help:"Run only checks matching these globs." placeholder:"GLOB"`
	Window    Duration `default:"14d" help:"How far back the live-health audit looks."`
	CI        bool     `name:"ci" help:"Plain output plus CI annotations."`
	Report    string   `help:"Write a Markdown report to this file." placeholder:"FILE"`
	WriteLock bool     `name:"write-lock" default:"true" negatable:"" help:"Refresh .flow.lock; in --ci mode propose it as a PR instead."`
	Watch     bool     `help:"Re-run continuously in a terminal UI."`
}

// Run implements the doctor command.
func (c *DoctorCmd) Run(app *Context) error {
	app.CI = app.CI || c.CI
	c.observeMode(app)
	if c.Watch {
		return c.watch(app)
	}
	s, report, err := c.once(app)
	if err != nil {
		return err
	}
	if err := c.publish(app, s, report); err != nil {
		return err
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Report(report) }); err != nil {
		return err
	}
	if err := c.annotate(app, s, report); err != nil {
		return err
	}
	app.exit(report.Exit())
	return nil
}

// observeMode drops --fix below automate (spec 20 §8): doctor still reads and
// reports everything, writes --report, and changes nothing anywhere — no
// follow-up, no health issue, no lock, no catch-up. A configuration doctor
// cannot read leaves --fix alone, because doctor's job then is to say why.
func (c *DoctorCmd) observeMode(app *Context) {
	if !c.Fix {
		return
	}
	s, err := app.inspect()
	if err != nil || s.Config == nil || s.Config.Acts() {
		return
	}
	app.Log.Warn("mode is " + s.Config.Mode + ", so doctor reports and changes nothing; --fix is ignored")
	c.Fix = false
}

// once is a whole run: the world as it stands, every check asked, and the
// lifecycles a finding starts. It is what `flow doctor` does between reading
// the flags and printing, and what the watch screen asks for again every
// minute — which is why the working copy is read afresh each time rather than
// once at the top. Somebody fixing a repository with the screen open is
// editing `.flow.yaml`, and a session that answered out of the file as it was
// when the window opened would never go green.
func (c *DoctorCmd) once(app *Context) (*session, engine.Report, error) {
	s, err := app.inspect()
	if err != nil {
		return nil, engine.Report{}, err
	}
	setup, err := c.world(app, s)
	if err != nil {
		return s, engine.Report{}, err
	}
	registry, err := engine.DoctorRegistry(setup)
	if err != nil {
		return s, engine.Report{}, err
	}
	report, err := c.ask(app, s, engine.DoctorInput{
		Registry:    registry,
		Env:         c.env(app, s, setup),
		Only:        c.Check,
		FlowVersion: version,
	})
	if err != nil {
		return s, report, err
	}
	if err := c.settle(app, s, &report); err != nil {
		return s, report, err
	}
	return s, report, nil
}

// ask runs the checks, drawing a progress display for a terminal that can
// take one (spec 12 §3).
//
// A doctor run reads a fortnight of pull requests, a board, a branch's
// protection and a deployment history before it prints a word, because the
// report is one document and half a document is not one. The display is what
// fills that silence; it is inline rather than full-screen, so the report
// lands in the scrollback underneath it where the rest of the session is.
//
// A run inside the watch screen draws none of this. That screen already has
// the terminal and says where the run has got to itself.
func (c *DoctorCmd) ask(app *Context, s *session, in engine.DoctorInput) (engine.Report, error) {
	if c.Watch || !app.tui() {
		return engine.RunDoctor(app.Ctx, in)
	}
	var report engine.Report
	var err error
	drawn := app.whileWorking(app.renderer(s.Settings).Progress(), func(ctx context.Context, say func(tea.Msg)) {
		in.Progress = func(step engine.DoctorStep) { say(step) }
		report, err = engine.RunDoctor(ctx, in)
	})
	if errors.Is(drawn, errInterrupted) {
		return report, errInterrupted
	}
	if drawn != nil {
		// The run is what the command came for and it has already finished.
		// A terminal that could not be drawn on is not a finding about the
		// workflow, and the report is about to be printed to it anyway.
		app.Log.Warn("doctor could not draw the run", "error", drawn)
	}
	return report, err
}

// watch draws the checks and keeps asking them (spec 12 §3). It is the half
// hour somebody spends putting a repository right, where the answer changes
// under them and they want to see it change.
func (c *DoctorCmd) watch(app *Context) error {
	s, err := app.inspect()
	if err != nil {
		return err
	}
	if err := c.watchable(app); err != nil {
		return err
	}
	screen := app.renderer(s.Settings).Watch(&watchRunner{app: app, cmd: c})
	final, err := onScreen(app, "watch the checks", screen)
	if err != nil {
		return err
	}
	report := final.Report()
	if !report.GeneratedAt.IsZero() {
		app.exit(report.Exit())
		return nil
	}
	// Nothing ever answered, so there is no verdict to exit on. Whatever
	// stopped the first run is the reason, and exiting 0 on it would report a
	// healthy repository that nobody managed to look at. A window closed
	// before the first run came back is nobody's failure.
	return final.Failing()
}

// watchable refuses the combinations that would accept --watch and then not
// honour it. A flag taken and ignored is worse than one that is missing,
// because the run then looks like it did what was asked.
func (c *DoctorCmd) watchable(app *Context) error {
	if c.Report != "" {
		return core.Errorf(core.ErrValidation, "watch the checks and write a report").
			WithWhy("a watch session has no last run to write down: it keeps asking until you close it").
			WithFix("run flow doctor --report %s on its own, or drop --report", c.Report)
	}
	if !app.tui() {
		return core.Errorf(core.ErrValidation, "watch the checks").
			WithWhy("there is no terminal to draw on").
			WithFix("run flow doctor on its own for a report that can be read anywhere")
	}
	return nil
}

// watchRunner is what the watch screen acts through: the same two commands a
// person would have typed, run again from inside the screen.
//
// It delegates rather than repeating. What an attestation may be taken
// against, how long it lasts and whose word it is are the attest command's
// rules (spec 07 §8), and a screen with its own copy of them would be a
// second place for them to be wrong.
type watchRunner struct {
	app *Context
	cmd *DoctorCmd
}

// Doctor implements [ui.Runner].
func (w *watchRunner) Doctor(fix bool) (engine.Report, error) {
	run := *w.cmd
	run.Fix = fix
	_, report, err := run.once(w.app)
	return report, err
}

// Attest implements [ui.Runner].
//
// The command prints what it recorded, and the screen is holding the
// terminal, so this one is run silently: anything written to stdout would
// land on top of the display. What it wrote is in the lock, and the run that
// follows is what says it took.
func (w *watchRunner) Attest(check, note string) error {
	quiet := *w.app
	quiet.Stdout = io.Discard
	return (&AttestCmd{CheckID: check, Note: note}).Run(&quiet)
}

// annotate offers the findings to the CI job flow is running inside, after
// the report rather than instead of it: the log is the whole of what was
// found, and the annotations are the few lines of it somebody is going to see
// without opening the job at all (spec 02 §2).
//
// They go to stderr, because stdout carries the one document the command
// printed and `--json --ci` has to stay a document a machine can parse. The
// runner reads both streams, so being on the quieter one costs nothing.
func (c *DoctorCmd) annotate(app *Context, s *session, report engine.Report) error {
	if !app.CI {
		return nil
	}
	return ui.Fprint(app.Stderr, app.renderer(s.Settings).Annotations(report))
}

// publish puts the report where it will be read again: the file --report
// names, and the one pinned issue flow keeps in the tracker (spec 07 §10).
//
// Neither is rendered here. The markdown is the UI's shape of the engine's
// words, so the file, the issue and a future CI comment cannot disagree
// about what the run found.
func (c *DoctorCmd) publish(app *Context, s *session, report engine.Report) error {
	if c.Report == "" && !c.Fix {
		return nil
	}
	body, err := ui.New(ui.Options{Format: ui.FormatMarkdown}).Report(report)
	if err != nil {
		return err
	}
	if c.Report != "" {
		if err := os.WriteFile(c.Report, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write the report to %s: %w", c.Report, err)
		}
	}
	out := engine.PublishHealth(app.Ctx, engine.HealthInput{
		Report:   report,
		Rendered: body,
		Config:   s.Config,
		Tracker:  s.Adapters.Tracker,
		Repo:     repoOf(s.Config),
		Clock:    s.Clock,
		Fix:      c.Fix,
		Narrowed: len(c.Check) > 0,
		Log:      app.Log,
	})
	if out.Err != nil {
		if outOfTime(app, out.Err) {
			return fmt.Errorf("publish the health report: %w", out.Err)
		}
		// The report has already said what is wrong with the workflow. Not
		// being able to file a copy of it is not a finding about the
		// workflow, and must not become the run's verdict (spec 07 §4).
		app.Log.Warn("doctor could not publish the health report", "error", out.Err)
	}
	return nil
}

// outOfTime reports whether an error is the run's own deadline rather than a
// provider's weather. A tracker that will not take a copy of the report is
// weather; a run that ran out of the time it was given did not finish, and
// a daily job that exits as if it had is a job going green on a morning the
// report was never filed (spec 07 Decisions).
func outOfTime(app *Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(app.Ctx.Err(), context.DeadlineExceeded)
}

// repoOf is the repository a report is about, empty when there is no
// configuration to say.
func repoOf(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Forge.Repo
}

// world is everything the checks are not asked to find out for themselves:
// the working copy, the credentials, and the placeholders resolved.
//
// A configuration that did not parse stops here. There is nothing to connect
// to, nothing to plan and nothing to resolve, and the two checks that can
// still be answered are answered from what the failure said.
func (c *DoctorCmd) world(app *Context, s *session) (engine.SetupInput, error) {
	setup := engine.SetupInput{
		Config:       s.Config,
		ConfigPath:   s.ConfigPath,
		Source:       s.Source,
		ConfigErr:    s.ConfigErr,
		SemanticsErr: s.SemanticsErr,
		CI:           app.CI,
		Unavailable:  map[string]error{},
	}
	if s.ConfigErr != nil || s.SemanticsErr != nil {
		return setup, nil
	}
	setup.Local = c.local(app, s)

	if err := s.connect(app); err != nil {
		// A credential flow could not resolve is `auth.forge` and
		// `auth.tracker` failing, not doctor failing: the whole point of the
		// run is to say which part of the setup is not there.
		setup.Unavailable[engine.TargetForge] = err
		setup.Unavailable[engine.TargetTracker] = err
		return setup, nil
	}

	answers, err := c.resolve(app, s)
	if err != nil {
		return setup, err
	}
	setup.Placeholders = answers

	// An adapter is built from what flow knew when it connected, and for
	// github-projects that includes the board's number. Resolving the
	// placeholder that holds it leaves the tracker pointing at nothing, so
	// the providers are rebuilt before anything asks them a question — or
	// the run that found the answer would be the one run that could not use
	// it (spec 07 §3).
	if c.Fix && engine.Resolved(answers) {
		if err := s.connect(app); err != nil {
			setup.Unavailable[engine.TargetTracker] = err
		}
	}
	return setup, nil
}

// env is the world the checks read, with the plan already made.
//
// The plan is made here rather than in a check because every provider check
// reads it: observing once and deciding once is what keeps doctor and apply
// from disagreeing about the same board (spec 06 §2).
func (c *DoctorCmd) env(app *Context, s *session, setup engine.SetupInput) *engine.Env {
	env := &engine.Env{
		Config:    s.Config,
		Lock:      s.Lock,
		Desired:   s.Desired,
		Adapters:  s.Adapters,
		Ancestry:  c.ancestry(app, s),
		Trailers:  s.Repo,
		Clock:     s.Clock,
		Window:    c.Window.Std(),
		FailBelow: failBelow(s.Config),
		Fix:       c.Fix,
		Set:       parseSet(c.Set),
		Log:       app.Log,
	}
	if len(setup.Unavailable) > 0 || s.Config == nil {
		return env
	}
	plan, err := s.plan(app, nil, nil)
	if err != nil {
		// Observing is the only part that can fail here. What must not
		// happen is the run going quiet: an empty plan is indistinguishable
		// from a plan that wants nothing, and that is a skip, so a doctor
		// that could not see the providers at all would report every one of
		// them as "this configuration does not ask for it" and exit 0.
		//
		// Unless something is still being asked. A placeholder nobody has
		// answered is why there is no plan, the check for it already says so
		// and already holds the run at exit 2, and repeating that one
		// question as twenty failures would bury it (scenario A05).
		app.Log.Warn("doctor could not look at the providers", "error", err)
		pending := engine.Pending(setup.Placeholders)
		if len(pending) == 0 {
			env.PlanErr = err
			return env
		}
		names := make([]string, 0, len(pending))
		for _, answer := range pending {
			names = append(names, answer.Placeholder.Path)
		}
		verb := " is"
		if len(names) > 1 {
			verb = " are"
		}
		env.Unplanned = strings.Join(names, ", ") + verb + " still to find out"
		return env
	}
	env.Plan = plan
	return env
}

// ancestry is what the live-health audit asks whether a merge is running
// with. The answers are the same ones `flow check deploy` caches, by the same
// pair and in the same place, so a daily audit of a fortnight's merges costs
// the forge one comparison per pair ever rather than one per day (spec 13 §3).
//
// A machine with nowhere to keep them, or a run with no forge to ask, gets
// nothing and the checks fall back to asking the forge directly. A cache is
// not worth failing a report over.
func (c *DoctorCmd) ancestry(app *Context, s *session) core.Comparer {
	if s.Adapters.Forge == nil {
		return nil
	}
	cache, err := ancestry(s.Adapters.Forge)
	if err != nil {
		app.Log.Warn("doctor could not cache what it compares", "error", err)
		return nil
	}
	return cache
}

// local is what flow can see in the working copy without a network.
func (c *DoctorCmd) local(app *Context, s *session) engine.Local {
	local := engine.Local{}
	remote, err := s.Repo.Remote(app.Ctx, "origin")
	if err != nil {
		local.Err = err
		return local
	}
	local.Remote, local.Repo = remote.URL, remote.Slug()
	if branch, err := s.Repo.Branch(app.Ctx); err == nil {
		local.Branch = branch
	}

	managers, err := detectHookManagers(app, s.Repo)
	if err == nil {
		for _, manager := range managers {
			local.HookManagers = append(local.HookManagers, manager.Name)
		}
	}
	dir, err := s.Repo.HooksPath(app.Ctx)
	if err != nil {
		return local
	}
	local.HookPath = filepath.Join(dir, hookName)
	content, err := os.ReadFile(local.HookPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
	case ours(content):
		local.HookInstalled = true
	default:
		local.HookForeign = true
	}
	return local
}

// resolve answers the values `.flow.yaml` does not know yet (spec 07 §3).
// Without --fix nothing is written: the answers are reported as what flow
// would have done.
func (c *DoctorCmd) resolve(app *Context, s *session) ([]engine.Answer, error) {
	pending := engine.Placeholders(s.Config)
	if len(pending) == 0 {
		return nil, nil
	}
	var permissions core.Permissions
	if s.Adapters.Tracker != nil {
		if probed, err := s.Adapters.Tracker.Probe(app.Ctx); err == nil {
			permissions = probed
		}
	}
	answers, err := engine.ResolvePlaceholders(app.Ctx, engine.ResolveInput{
		Config:      s.Config,
		Tracker:     s.Adapters.Tracker,
		Permissions: permissions,
		Lock:        s.Lock,
		Set:         parseSet(c.Set),
		Fix:         c.Fix,
		Choose:      app.chooser(s),
		Log:         app.Log,
	})
	if err != nil {
		return nil, err
	}
	if c.Fix && app.keepsWhatItLearns() && len(engine.Pending(answers)) < len(answers) {
		if err := s.saveLock(s.Lock); err != nil {
			return nil, err
		}
	}
	return answers, nil
}

// settle runs the lifecycles that turn a finding into somebody's work: a
// follow-up for what flow is not allowed to do, and the lock refreshed with
// what flow saw.
func (c *DoctorCmd) settle(app *Context, s *session, report *engine.Report) error {
	if s.Config == nil {
		return nil
	}
	outcomes := engine.ReconcileFollowUps(app.Ctx, *report, engine.FollowUpInput{
		Config:  s.Config,
		Lock:    s.Lock,
		Tracker: s.Adapters.Tracker,
		Repo:    s.Config.Forge.Repo,
		Clock:   s.Clock,
		Fix:     c.Fix,
		Log:     app.Log,
	})
	report.Absorb(outcomes)
	if !c.Fix || !c.WriteLock {
		return nil
	}
	// The lock being written now describes this configuration. Stamping it is
	// what makes an attestation taken against an older `.flow.yaml` lapse
	// (spec 03 §3): a lock whose hash stayed empty would keep every promise
	// alive through a rewrite of the thing it was promised about.
	s.Lock.ConfigHash = s.ConfigHash
	if !app.keepsWhatItLearns() {
		return c.proposeLock(app, s)
	}
	return s.saveLock(s.Lock)
}

// proposeLock offers the refreshed lock as a pull request instead of writing
// it (spec 02 §2's --write-lock).
//
// A CI runner's checkout is thrown away when the job ends, so a lock written
// there is a lock nobody will ever read; and the place it has to reach is the
// trunk, which is the one place flow does not write (spec 06 §4). The
// proposal is made under the same id every day, so a lock that goes on
// drifting updates one pull request rather than opening thirty.
//
// Not being able to propose it is not a finding about the workflow, and must
// not become the run's verdict: the report has already said everything doctor
// found, and the lock is flow's own bookkeeping.
func (c *DoctorCmd) proposeLock(app *Context, s *session) error {
	data, err := s.Lock.Bytes()
	if err != nil {
		return core.Errorf(core.ErrValidation, "write %s", config.LockPath).WithWhy("%s", err)
	}
	if current, err := os.ReadFile(s.LockPath); err == nil && bytes.Equal(current, data) {
		return nil
	}
	if s.Adapters.Forge == nil {
		app.Log.Warn("doctor has no forge to propose the refreshed lock to")
		return nil
	}
	// The title is composed against this team's own convention and judged by
	// the linter the gate will judge it with. A pull request flow's own gate
	// refuses is worse than no pull request: it is a red cross on the trunk
	// every morning, on the one change nobody can do anything about.
	title, err := engine.LockProposal.Title(s.Config)
	if err != nil {
		// The fix is the whole value of the message, and it is not in
		// Error(): it is the key somebody has to change.
		var user *core.UserError
		fix := ""
		if errors.As(err, &user) {
			fix = user.Fix
		}
		app.Log.Warn("doctor cannot write a lock proposal this configuration would accept",
			"error", err, "fix", fix)
		return nil
	}
	pr, err := s.Adapters.Forge.ProposeFiles(app.Ctx, core.FileProposal{
		ID:    engine.LockProposal.ID,
		Title: title,
		Body:  lockProposalBody(),
		Files: []core.ProposedFile{{Path: config.LockPath, Content: data}},
	})
	if err != nil {
		if outOfTime(app, err) {
			return fmt.Errorf("propose the refreshed lock: %w", err)
		}
		app.Log.Warn("doctor could not propose the refreshed lock", "error", err)
		return nil
	}
	app.Log.Info("doctor proposed the refreshed lock", "pull_request", pr.Number)
	return nil
}

// lockProposalBody explains the pull request to whoever has to approve it.
func lockProposalBody() string {
	return "`" + config.LockPath + "` records what flow last saw and what it was told " +
		"to take somebody's word for (spec 03 §3).\n\n" +
		"This run read the providers and found it out of date. Merging this keeps the next run's " +
		"drift detection honest: a lock that is never refreshed reports yesterday's repository for ever.\n"
}

// failBelow is how strictly the live-health audit grades what it finds, as a
// ratio. A configuration that did not say takes the engine's default.
func failBelow(cfg *config.Config) float64 {
	if cfg == nil {
		return 0
	}
	return cfg.Health.Threshold()
}

// parseSet turns `--set name=value` into the map the engine takes.
func parseSet(pairs []string) map[string]string {
	if len(pairs) == 0 {
		return nil
	}
	set := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		set[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return set
}
