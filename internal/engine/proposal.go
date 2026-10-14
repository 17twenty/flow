package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// The pull requests flow opens for itself (spec 06 §4, spec 07 §2).
//
// Two changes flow decides on belong in the trunk, and the trunk is the one
// place flow does not write: the files a workflow needs, and the refreshed
// lock. Both are offered as a pull request, and both are then judged by the
// gate like anybody else's — by flow, against the team's own configuration.
//
// Flow had been writing a constant title for each. On this repository the
// constant `chore(flow): refresh .flow.lock` has a scope that is not in
// `commits.scopes.allowed` and a type that is not in
// `commits.unlinked_types`, so every morning flow opened a pull request that
// flow then refused: a red cross on the trunk's gate, on the one change
// nobody could do anything about (issue #152, the same shape as #106).
//
// The title is therefore composed against the configuration and judged by the
// same linter the gate judges it with, and when no landable title can be
// written flow opens nothing and says which key refuses it.

// Proposal is a pull request flow opens for its own work.
type Proposal struct {
	// ID is the proposal's identity on the forge. Proposing under the same
	// id updates the same pull request rather than opening another.
	ID string
	// Summary is the description part of the header. The type and the scope
	// in front of it are the team's to decide, not flow's.
	Summary string
	// Types are the types this change may honestly claim, most honest first.
	// Flow has no ticket for its own bookkeeping — filing one every time the
	// lock drifts would be a board full of flow talking to itself — so the
	// type has to be one that may merge without one.
	Types []string
	// Scopes are the scopes it may honestly claim, most apt first. A scope
	// is optional in the grammar, so a team that allows none of them gets a
	// header without one rather than no pull request.
	Scopes []string
}

var (
	// LockProposal carries the refreshed lock. It is the configuration
	// package's own file, which is why `config` is the first scope.
	LockProposal = Proposal{
		ID:      "lock",
		Summary: "refresh " + config.LockPath,
		Types:   []string{"chore", "build", "ci", "docs"},
		Scopes:  []string{"config", "flow", "ci", "build", "deps"},
	}
	// FilesProposal carries the files the configured workflow needs, which
	// are the continuous integration the team has not written yet.
	FilesProposal = Proposal{
		ID:      "files",
		Summary: "add the files the workflow needs",
		Types:   []string{"ci", "chore", "build"},
		Scopes:  []string{"ci", "flow", "config", "build"},
	}
)

// proposalNumber stands in for the number the forge will append when it
// squashes. No pull request flow has seen is wider, and the header that lands
// is what the gate lints (spec 09 §2), so the title is judged with room for
// the number rather than on a string nobody will ever see.
const proposalNumber = 99999

// Title is the header this proposal would land as under cfg: a type that may
// merge without a ticket, a scope the team allows, and a header the team's own
// linter accepts.
//
// The error names the key that refuses it. There is nothing to grade here —
// spec 07 §2 settled that failing to propose the lock is not a finding about
// the workflow — but somebody has to be able to find out why no pull request
// arrived, and "flow could not write a title your configuration accepts" is
// only useful if it says which part of the configuration.
func (p Proposal) Title(cfg *config.Config) (string, error) {
	kind, err := p.kind(cfg)
	if err != nil {
		return "", err
	}
	title := kind + p.scope(cfg) + ": " + p.Summary
	if violations := p.violations(cfg, title); len(violations) > 0 {
		return "", p.refused(title, violations[0])
	}
	return title, nil
}

// kind picks a type the gate will let through without a ticket, from among
// the types this team writes at all.
func (p Proposal) kind(cfg *config.Config) (string, error) {
	settings := CommitSettings(cfg)
	var written []string
	for _, kind := range p.Types {
		if !slices.Contains(settings.Types, kind) {
			continue
		}
		written = append(written, kind)
		if _, unlinked := unlinkedType(cfg, kind+": "+p.Summary); unlinked {
			return kind, nil
		}
	}
	if len(written) == 0 {
		return "", p.errorf().
			WithWhy("commits.types has no type flow can honestly use for its own work").
			WithFix("allow one of %s in commits.types", strings.Join(p.Types, ", "))
	}
	return "", p.errorf().
		WithWhy("commits.unlinked_types lets no type flow can honestly use merge without a ticket").
		WithFix("add one of %s to commits.unlinked_types", strings.Join(written, ", "))
}

// scope is the scope part of the header, in brackets, and "" when the team
// allows none that fits. A scope flow is not sure of is a scope flow leaves
// out: the convention only judges one that is there — unless the team demands
// one, and then the absence is what the linter would report.
func (p Proposal) scope(cfg *config.Config) string {
	settings := CommitSettings(cfg)
	if len(settings.AllowedScopes) == 0 {
		if settings.ScopeRequired {
			return "(" + p.Scopes[0] + ")"
		}
		return ""
	}
	for _, scope := range p.Scopes {
		if slices.Contains(settings.AllowedScopes, scope) {
			return "(" + scope + ")"
		}
	}
	return ""
}

// violations lints the header the forge would land, not the title a reviewer
// sees. A squash appends the pull request's number, which is how a title that
// passed the gate landed as a commit that broke it (spec 09 §2).
func (p Proposal) violations(cfg *config.Config, title string) []commits.Violation {
	return commits.Lint(commits.Parse(landedHeader(title)), commits.LintOptions{
		Settings: CommitSettings(cfg),
		Subject:  commits.SubjectPullRequest,
	})
}

// landedHeader is the title with room for the number the forge appends.
func landedHeader(title string) string {
	return fmt.Sprintf("%s (#%d)", title, proposalNumber)
}

// refused turns the linter's verdict into the key that has to change for flow
// to be able to do its own bookkeeping.
//
// The key is in the reason and not only in the remedy, because an apply
// reports an item's failure by its message alone ([AppliedItem.Why]) and a
// reason that does not name the key is a morning spent guessing.
//
// Only two rules can fire here. The type and the scope were chosen from what
// the configuration allows, so a demanded scope the team named nothing for
// and the length are all that is left to go wrong — and the catch-all is
// there because a rule nobody has thought of yet must still say something.
func (p Proposal) refused(title string, violation commits.Violation) error {
	err := p.errorf()
	switch violation.Rule {
	case commits.RuleScopeRequired:
		return err.
			WithWhy("commits.scopes.required demands a scope and commits.scopes.allowed has none that fits").
			WithFix("allow one of %s in commits.scopes.allowed", strings.Join(p.Scopes, ", "))
	case commits.RuleHeaderLength:
		return err.
			WithWhy("commits.header_max: %s", violation.Message).
			WithFix("raise commits.header_max to %d", len([]rune(landedHeader(title))))
	}
	return err.
		WithWhy("%s", violation.Message).
		WithFix("flow cannot write a title %s accepts", violation.Rule)
}

// errorf starts the one message every refusal is a reason for.
func (p Proposal) errorf() *core.UserError {
	return core.Errorf(core.ErrValidation, "open flow's own pull request to %s", p.Summary)
}
