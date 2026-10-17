package engine_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// The pull requests flow opens for itself are judged by flow's own gate.
// Composing the title and judging it are therefore tested together: a test
// that only checked the string would let the two drift apart again, which is
// the whole of the defect (issue #152).
//
// The header is the one a squash would land, number and all, because that is
// what the gate reads (issue #106) and what flow leaves room for.
func gateOn(t *testing.T, cfg *config.Config, title string) engine.PolicyResult {
	t.Helper()
	return engine.CheckPolicy(engine.PolicySnapshot{
		PR: core.PullRequest{
			Number:       99999,
			Title:        title,
			SquashHeader: fmt.Sprintf("%s (#%d)", title, 99999),
			Author:       "flow-bot-gh",
			State:        core.PRStateOpen,
			HeadSHA:      "abc1234",
		},
		Config: cfg,
		Now:    snapshot(t).Now,
	})
}

// proposals are the pull requests flow opens for its own work. Both are
// written by flow, judged by flow, and have no ticket behind them.
func proposals() map[string]engine.Proposal {
	return map[string]engine.Proposal{
		"the lock":  engine.LockProposal,
		"the files": engine.FilesProposal,
	}
}

func TestFlowsOwnProposalIsATitleTheGateAccepts(t *testing.T) {
	tests := []struct {
		name     string
		unlinked []string
		scopes   config.Scopes
		want     map[string]string
	}{
		{
			name:     "bookkeeping is a chore, and the workflow files are CI",
			unlinked: []string{"ci", "build", "docs", "chore"},
			scopes:   config.Scopes{Required: true, Allowed: []string{"cli", "config", "ci", "engine"}},
			want: map[string]string{
				"the lock":  "chore(config): refresh .flow.lock",
				"the files": "ci(ci): add the files the workflow needs",
			},
		},
		{
			name:     "a team that lets no chore through unlinked gets the next honest type",
			unlinked: []string{"build", "docs"},
			scopes:   config.Scopes{Required: true, Allowed: []string{"cli", "config", "engine"}},
			want: map[string]string{
				"the lock":  "build(config): refresh .flow.lock",
				"the files": "build(config): add the files the workflow needs",
			},
		},
		{
			name:     "no scope is asked for and none is given",
			unlinked: []string{"chore", "ci"},
			want: map[string]string{
				"the lock":  "chore: refresh .flow.lock",
				"the files": "ci: add the files the workflow needs",
			},
		},
		{
			name:     "a scope is demanded and the team names none",
			unlinked: []string{"chore", "ci"},
			scopes:   config.Scopes{Required: true},
			want: map[string]string{
				"the lock":  "chore(config): refresh .flow.lock",
				"the files": "ci(ci): add the files the workflow needs",
			},
		},
		{
			name:     "the team allows scopes and none of them fits",
			unlinked: []string{"chore", "ci"},
			scopes:   config.Scopes{Allowed: []string{"cli", "engine"}},
			want: map[string]string{
				"the lock":  "chore: refresh .flow.lock",
				"the files": "ci: add the files the workflow needs",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for what, proposal := range proposals() {
				cfg := preset(t, "lean-kanban")
				cfg.Commits.UnlinkedTypes = test.unlinked
				cfg.Commits.Scopes = test.scopes

				title, err := proposal.Title(cfg)
				if err != nil {
					t.Fatalf("flow cannot title its own pull request for %s: %v", what, err)
				}
				if title != test.want[what] {
					t.Errorf("the title for %s is %q, want %q", what, title, test.want[what])
				}

				// The gate is the point. A title flow writes and flow
				// refuses is a red cross on the trunk every morning.
				result := gateOn(t, cfg, title)
				for _, rule := range []string{engine.RulePRTitleConventional, engine.RulePRTicketLinked} {
					if f := finding(t, result, rule); !f.OK {
						t.Errorf("flow's own proposal of %s fails %s: %s", what, rule, f.Message)
					}
				}
			}
		})
	}
}

// Flow has nothing to say about a configuration that will not let it write
// its own bookkeeping, except which key said no. Opening a pull request
// anyway is what produced the defect; opening none and saying nothing is how
// somebody spends a morning wondering where the lock went.
func TestFlowsOwnProposalSaysWhichKeyRefusesIt(t *testing.T) {
	tests := []struct {
		name    string
		shape   func(*config.Config)
		mention string
	}{
		{
			name:    "every type flow could use needs a ticket",
			shape:   func(c *config.Config) { c.Commits.UnlinkedTypes = nil },
			mention: "commits.unlinked_types",
		},
		{
			name: "the only unlinked type is one flow cannot honestly claim",
			shape: func(c *config.Config) {
				c.Commits.UnlinkedTypes = []string{"feat"}
			},
			mention: "commits.unlinked_types",
		},
		{
			name: "a type flow would use is not a type the team writes at all",
			shape: func(c *config.Config) {
				c.Commits.Types = []string{"feat", "fix"}
				c.Commits.UnlinkedTypes = []string{"chore", "ci", "build", "docs"}
			},
			mention: "commits.types",
		},
		{
			name: "a scope is demanded and flow's own are not allowed",
			shape: func(c *config.Config) {
				c.Commits.UnlinkedTypes = []string{"chore", "ci"}
				c.Commits.Scopes = config.Scopes{Required: true, Allowed: []string{"cli", "engine"}}
			},
			mention: "commits.scopes.allowed",
		},
		{
			name: "the header is too short for the summary",
			shape: func(c *config.Config) {
				c.Commits.UnlinkedTypes = []string{"chore", "ci"}
				c.Commits.Scopes = config.Scopes{}
				c.Commits.HeaderMax = 20
			},
			mention: "commits.header_max",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for what, proposal := range proposals() {
				cfg := preset(t, "lean-kanban")
				test.shape(cfg)

				title, err := proposal.Title(cfg)
				if err == nil {
					t.Fatalf("flow titled %s %q, which this configuration does not accept", what, title)
				}
				var user *core.UserError
				if !errors.As(err, &user) {
					t.Fatalf("error %v is not a user error, so nobody is told what to change", err)
				}
				if !strings.Contains(user.Fix, test.mention) {
					t.Errorf("the fix for %s is %q, want it to name %s", what, user.Fix, test.mention)
				}
				// An apply reports a failure by its message alone, so the
				// key has to be in the reason as well as in the remedy.
				if !strings.Contains(err.Error(), test.mention) {
					t.Errorf("the message for %s is %q, want it to name %s", what, err, test.mention)
				}
				if !strings.Contains(err.Error(), proposal.Summary) {
					t.Errorf("the message %q does not say what flow could not open", err)
				}
			}
		})
	}
}

// A remedy that names types the team does not write is a remedy that sends
// somebody to the wrong key: adding `docs` to `commits.unlinked_types` does
// nothing while `commits.types` leaves it out. Flow asks for what it could
// actually use here.
func TestFlowsOwnProposalAsksOnlyForTypesTheTeamWrites(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Commits.Types = []string{"feat", "fix", "chore"}
	cfg.Commits.UnlinkedTypes = nil

	_, err := engine.LockProposal.Title(cfg)
	var user *core.UserError
	if !errors.As(err, &user) {
		t.Fatalf("a configuration with nothing unlinked was accepted: %v", err)
	}
	if !strings.Contains(user.Fix, "chore") {
		t.Errorf("the fix is %q, want it to name the type this team writes", user.Fix)
	}
	for _, unwritten := range []string{"docs", "build", "ci"} {
		if strings.Contains(user.Fix, unwritten) {
			t.Errorf("the fix is %q, want it not to name %q, which commits.types leaves out",
				user.Fix, unwritten)
		}
	}
}

// A squash appends the pull request's number, so a title that fits is not the
// same question as a header that fits. Flow asked the first question about
// somebody else's pull request once already (issue #106) and reported it a
// fortnight later; it is not going to ask it about its own.
func TestFlowsOwnProposalLeavesRoomForTheNumberTheForgeAppends(t *testing.T) {
	cfg := preset(t, "lean-kanban")
	cfg.Commits.UnlinkedTypes = []string{"chore"}
	cfg.Commits.Scopes = config.Scopes{}
	cfg.Commits.HeaderMax = len("chore: refresh .flow.lock") + 2

	title, err := engine.LockProposal.Title(cfg)
	if err == nil {
		t.Fatalf("flow wrote %q, which fits only until the forge numbers it", title)
	}
	var user *core.UserError
	if !errors.As(err, &user) || !strings.Contains(user.Fix, "commits.header_max") {
		t.Fatalf("the refusal does not say how much room a header needs: %v", err)
	}
	// The number it asks for is the room the header it tried really needs,
	// not the room the title alone would have taken.
	if !strings.Contains(user.Fix, fmt.Sprint(len("chore: refresh .flow.lock (#99999)"))) {
		t.Errorf("the fix is %q, want it to ask for room for the number too", user.Fix)
	}
}
