package main

import (
	"fmt"
	"os"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// ApplyCmd converges the forge and tracker on the desired state (spec 02 §2,
// spec 06). Exit 2 means follow-ups or inputs remain for a human.
type ApplyCmd struct {
	Plan        string `help:"Apply a saved plan; refused if it is stale." placeholder:"FILE"`
	AutoApprove bool   `name:"auto-approve" help:"Skip confirmation. Required when non-interactive."`
	Prune       bool   `help:"Remove options flow created that are no longer desired."`
	NoFollowups bool   `name:"no-followups" help:"Do not file follow-up tickets for unconfigurable settings."`
}

// Run implements the apply command.
func (c *ApplyCmd) Run(app *Context) error {
	s, err := app.open()
	if err != nil {
		return err
	}
	if !s.Config.Acts() {
		return core.Errorf(core.ErrValidation, "apply the plan").
			WithWhy("mode is %s, so flow may not change the forge or the tracker (spec 20 §8)", s.Config.Mode).
			WithFix("run flow plan to see what apply would do, or set mode: automate in .flow.yaml")
	}
	if err := s.connect(app); err != nil {
		return err
	}

	// The world is re-planned whatever happens: a saved plan is only worth
	// applying if it still describes what flow would decide now (spec 06 §3).
	plan, err := s.plan(app, nil, nil)
	if err != nil {
		return err
	}
	if c.Plan != "" {
		if plan, err = c.load(plan); err != nil {
			return err
		}
	}

	approved, err := c.approve(app, s, plan)
	if err != nil {
		return err
	}
	if !approved {
		fmt.Fprintln(app.Stdout, "nothing was applied")
		app.exit(plan.ExitCode())
		return nil
	}

	applied, err := engine.Apply(app.Ctx, engine.ApplyInput{
		Plan:     plan,
		Config:   s.Config,
		Desired:  s.Desired,
		Adapters: s.Adapters,
		Lock:     s.Lock,
		Clock:    s.Clock,
		Prune:    c.Prune,
		Save:     s.saveLock,
		Log:      app.Log,
	})
	if err != nil {
		return err
	}

	applied.FollowUps = engine.ApplyFollowUps(app.Ctx, engine.ApplyFollowUpInput{
		Applied: applied,
		Plan:    plan,
		Config:  s.Config,
		Lock:    s.Lock,
		Tracker: s.Adapters.Tracker,
		Repo:    s.Config.Forge.Repo,
		Clock:   s.Clock,
		File:    !c.NoFollowups,
		Log:     app.Log,
	})
	if len(applied.FollowUps) > 0 {
		if err := s.saveLock(s.Lock); err != nil {
			return err
		}
	}

	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Applied(applied) }); err != nil {
		return err
	}
	app.exit(applied.ExitCode())
	return nil
}

// load reads the plan a human approved and refuses it once the world has
// moved on (spec 02 §2, spec 06 §3).
func (c *ApplyCmd) load(fresh engine.Plan) (engine.Plan, error) {
	data, err := os.ReadFile(c.Plan)
	if err != nil {
		return engine.Plan{}, core.Errorf(core.ErrNotFound, "read the plan in %s", c.Plan).
			WithWhy("%v", err).
			WithFix("write one with flow plan --out %s", c.Plan)
	}
	loaded, err := engine.ParsePlan(data)
	if err != nil {
		return engine.Plan{}, err
	}
	if err := engine.Stale(loaded, fresh); err != nil {
		return engine.Plan{}, core.Errorf(err, "apply the plan in %s", c.Plan).
			WithWhy("%v", err).
			WithFix("look at it again with flow plan --out %s", c.Plan)
	}
	return loaded, nil
}

// approve settles whether flow may make these changes (spec 06 §3).
//
// A plan with nothing for flow to do needs no approval: there is nothing to
// agree to, and an apply that converged is worth running in a cron job
// without anybody having to promise in advance that it will find nothing.
func (c *ApplyCmd) approve(app *Context, s *session, plan engine.Plan) (bool, error) {
	changes := len(plan.Mutations())
	if changes == 0 || c.AutoApprove {
		return true, nil
	}
	if app.CLI.JSON || !app.interactive() {
		return false, core.Errorf(core.ErrValidation, "apply %d changes without asking", changes).
			WithWhy("nobody can answer: this run is not interactive").
			WithFix("pass --auto-approve, or run flow plan first and read it")
	}
	question := fmt.Sprintf("apply %d changes?", changes)
	if app.tui() {
		answer, err := onScreen(app, "ask for confirmation", app.renderer(s.Settings).Confirmation(plan, question))
		return answer.Approved(), err
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Plan(plan) }); err != nil {
		return false, err
	}
	return app.confirm(question)
}
