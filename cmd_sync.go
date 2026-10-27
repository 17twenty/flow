package main

import (
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// SyncCmd performs the catch-ups flow owns and nothing else (spec 20 §2).
//
// It is `doctor --fix` narrowed to the live checks whose remedy is flow's own
// act, on a window of hours rather than a fortnight: cheap enough to run after
// every gate, every hour and on every push, so that an event the forge never
// delivered costs an hour rather than a day. It makes no plan, publishes no
// report, proposes no lock and files no follow-up — those are the audit's,
// and an audit run every hour is a rate limit spent on nothing.
type SyncCmd struct {
	Window Duration `default:"48h" help:"How far back to look for work flow still owes."`
}

// Run implements the sync command.
func (c *SyncCmd) Run(app *Context) error {
	s, err := app.inspect()
	if err != nil {
		return err
	}
	if s.ConfigErr != nil {
		return s.ConfigErr
	}
	if s.SemanticsErr != nil {
		return s.SemanticsErr
	}
	doctor := &DoctorCmd{Window: c.Window, Check: engine.SyncChecks}
	setup, err := doctor.world(app, s)
	if err != nil {
		return err
	}
	// A provider flow could not reach is not something sync can settle or
	// report on usefully: there is no audit here to carry the finding, so the
	// run fails and says why.
	for _, target := range []string{engine.TargetForge, engine.TargetTracker} {
		if unavailable := setup.Unavailable[target]; unavailable != nil {
			return unavailable
		}
	}
	registry, err := engine.DoctorRegistry(setup)
	if err != nil {
		return err
	}
	report, err := engine.RunDoctor(app.Ctx, engine.DoctorInput{
		Registry: registry,
		Env: &engine.Env{
			Config:    s.Config,
			Lock:      s.Lock,
			Desired:   s.Desired,
			Adapters:  s.Adapters,
			Ancestry:  doctor.ancestry(app, s),
			Trailers:  s.Repo,
			Clock:     s.Clock,
			Window:    c.Window.Std(),
			FailBelow: failBelow(s.Config),
			// Below automate, sync says what it would settle and settles
			// nothing (spec 20 §8).
			Fix: s.Config.Acts(),
			Log: app.Log,
		},
		Only:        engine.SyncChecks,
		FlowVersion: version,
	})
	if err != nil {
		return err
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Report(report) }); err != nil {
		return err
	}
	app.exit(report.Exit())
	return nil
}
