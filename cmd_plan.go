package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// PlanCmd shows what flow would configure, without changing anything
// (spec 02 §2, spec 06). Exit 0 means nothing to do, 2 means changes pending.
type PlanCmd struct {
	Only            []string `help:"Restrict the plan to these capabilities." placeholder:"CAPABILITY"`
	Target          []string `help:"Restrict the plan to these targets: forge, tracker, deploys." placeholder:"TARGET"`
	TrackerOverride string   `name:"tracker-override" help:"Compare against another tracker kind. Offline." placeholder:"KIND"`
	Out             string   `help:"Write the plan to a file for a later flow apply --plan." placeholder:"FILE"`
}

// Run implements the plan command.
func (c *PlanCmd) Run(app *Context) error {
	s, err := app.open()
	if err != nil {
		return err
	}
	if err := c.validate(s); err != nil {
		return err
	}
	if c.TrackerOverride != "" {
		return c.compare(app, s)
	}
	if err := s.connect(app); err != nil {
		return err
	}

	plan, err := s.plan(app, c.Only, c.Target)
	if err != nil {
		return err
	}
	if c.Out != "" {
		if err := writePlan(c.Out, plan); err != nil {
			return err
		}
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Plan(plan) }); err != nil {
		return err
	}
	app.exit(plan.ExitCode())
	return nil
}

// compare answers the same question about another tracker, from the two
// declarations alone (spec 05 §6). No provider is built and no credential is
// resolved: the answer is about what the two trackers can be made to do.
func (c *PlanCmd) compare(app *Context, s *session) error {
	left, err := s.Registry.Of(s.Config.Tracker.Type)
	if err != nil {
		return err
	}
	right, err := s.Registry.Of(c.TrackerOverride)
	if err != nil {
		return core.Errorf(core.ErrValidation, "compare against %s", c.TrackerOverride).
			WithWhy("flow ships no capability declaration for it").
			WithFix("name one of %s", strings.Join(s.Registry.Adapters(), ", "))
	}
	comparison, err := engine.Compare(engine.CompareInput{
		Config:  s.Config,
		Desired: s.Desired,
		Left:    left,
		Right:   right,
		Now:     s.Clock.Now(),
	})
	if err != nil {
		return err
	}
	if err := app.render(s, func(r ui.Renderer) (string, error) { return r.Comparison(comparison) }); err != nil {
		return err
	}
	if comparison.Differences() > 0 {
		app.exit(core.ExitActionRequired)
	}
	return nil
}

// validate refuses a filter that names nothing, rather than reporting that
// there is nothing to do: a typo in `--only` and a converged workflow would
// otherwise print the same empty plan and the same exit 0.
func (c *PlanCmd) validate(s *session) error {
	for _, target := range c.Target {
		if !slices.Contains([]string{engine.TargetForge, engine.TargetTracker, engine.TargetDeploys}, target) {
			return core.Errorf(core.ErrValidation, "plan the %s target", target).
				WithWhy("flow has no such target").
				WithFix("name one of %s, %s, %s", engine.TargetForge, engine.TargetTracker, engine.TargetDeploys)
		}
	}
	known := s.capabilityNames()
	for _, capability := range c.Only {
		if !slices.Contains(known, capability) {
			return core.Errorf(core.ErrValidation, "plan the %s capability", capability).
				WithWhy("none of this repository's providers declares it").
				WithFix("name one of %s", strings.Join(known, ", "))
		}
	}
	return nil
}

// writePlan saves the plan for a later `flow apply --plan` (spec 02 §2). It
// is the JSON document, which is what [engine.ParsePlan] reads back and what
// a human can diff.
func writePlan(path string, plan engine.Plan) error {
	data, err := plan.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write the plan to %s: %w", path, err)
	}
	return nil
}
