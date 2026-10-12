package main

import (
	"time"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
	"github.com/17twenty/flow/internal/ui"
)

// AttestCmd records a named, expiring human confirmation for a check flow
// cannot verify through an API (spec 02 §2, spec 07 §8). Attesting a check that
// is API-verifiable is a usage error.
type AttestCmd struct {
	CheckID string `arg:"" name:"check-id" help:"Check to attest, e.g. tracker.native.pr_merged."`
	Note    string `required:"" help:"What was confirmed, and how."`
	By      string `help:"Who confirmed it. Defaults to the handle in user config." placeholder:"HANDLE"`
	Expires string `help:"How long the attestation stays valid. Default 30d, or attestation.default_ttl." placeholder:"DURATION"`
}

// Run implements the attest command.
//
// It looks before it writes. Which checks may be attested is a property of
// what the providers can be asked (spec 05 §4), so the plan is made first and
// the promise is refused unless that plan says nobody can answer this one —
// which is the difference between a record and a way to silence a real
// failure.
func (c *AttestCmd) Run(app *Context) error {
	s, err := app.open()
	if err != nil {
		return err
	}
	ttl, err := c.ttl(s.Config)
	if err != nil {
		return err
	}
	if err := s.connect(app); err != nil {
		return err
	}
	plan, err := s.plan(app, nil, nil)
	if err != nil {
		return err
	}
	attested, err := engine.RunAttest(engine.RecordInput{
		Check: c.CheckID,
		By:    c.by(s),
		Note:  c.Note,
		TTL:   ttl,
		Plan:  plan,
		Lock:  s.Lock,
		Now:   s.Clock.Now(),
	})
	if err != nil {
		return err
	}
	// Only the promise is written. The lock's ConfigHash is left as doctor
	// stamped it, because stamping it here would revive every older
	// attestation that had lapsed with it (spec 07 §8) on the word of
	// somebody who was vouching for one check.
	if err := s.saveLock(s.Lock); err != nil {
		return err
	}
	return app.render(s, func(r ui.Renderer) (string, error) { return r.Attested(attested) })
}

// by is who is vouching: whoever said so, else whoever this machine is
// configured as (spec 03 §4). Nobody at all is refused by the engine, which is
// where the rest of what makes a record worth keeping is decided.
func (c *AttestCmd) by(s *session) string {
	if c.By != "" {
		return c.By
	}
	return s.Settings.Handle
}

// ttl is how long this promise stands: what was asked for, else what the team
// agreed, else spec 02 §2's thirty days.
//
// The chain matters because `attestation.default_ttl` is a setting a
// team writes down (spec 03 §2), and a flag whose own default always won would
// make it a comment. The last two links are the engine's, so that the number
// doctor prints when it asks for an attestation is the number attest writes.
func (c *AttestCmd) ttl(cfg *config.Config) (time.Duration, error) {
	if c.Expires == "" {
		return engine.AttestationTTL(cfg), nil
	}
	ttl, err := config.ParseDuration(c.Expires)
	if err != nil {
		return 0, core.Errorf(core.ErrValidation, "read --expires %q", c.Expires).
			WithWhy("%v", err).
			WithFix("write it as a duration, such as 30d, 2w or 72h")
	}
	if ttl <= 0 {
		return 0, core.Errorf(core.ErrValidation, "record a promise that lasts %s", c.Expires).
			WithWhy("an attestation that has already expired vouches for nothing").
			WithFix("pass --expires with a positive duration, such as 30d")
	}
	return ttl, nil
}
