package commits_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/commits"
)

// lintSettings is a representative configuration for the unit tests below.
func lintSettings() commits.Settings {
	return commits.Settings{
		Types:         []string{"feat", "fix", "docs", "chore", "refactor"},
		HeaderMax:     72,
		ScopeRequired: false,
		AllowedScopes: nil,
	}
}

// lint parses and lints in one step.
func lint(t *testing.T, text string, opts commits.LintOptions) []commits.Violation {
	t.Helper()
	return commits.Lint(commits.Parse(text), opts)
}

// ruleIDs returns the rule ids in vs, in order.
func ruleIDs(vs []commits.Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Rule)
	}
	return out
}

func TestLintRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		text     string
		settings func() commits.Settings
		want     []string
		severity commits.Severity
	}{
		{
			name: "clean header produces nothing",
			text: "feat(cli): add the --json flag",
			want: nil,
		},
		{
			name:     "non-conventional reports only the format rule",
			text:     "Added CSV export",
			want:     []string{commits.RuleHeaderFormat},
			severity: commits.SeverityError,
		},
		{
			name:     "unknown type",
			text:     "feature(cli): add the --json flag",
			want:     []string{commits.RuleTypeAllowed},
			severity: commits.SeverityError,
		},
		{
			name:     "uppercase type",
			text:     "Feat(cli): add the --json flag",
			want:     []string{commits.RuleTypeAllowed},
			severity: commits.SeverityError,
		},
		{
			name: "scope required but absent",
			text: "feat: add the --json flag",
			settings: func() commits.Settings {
				s := lintSettings()
				s.ScopeRequired = true
				return s
			},
			want:     []string{commits.RuleScopeRequired},
			severity: commits.SeverityError,
		},
		{
			name: "scope not in the allowed list",
			text: "feat(payments): add the --json flag",
			settings: func() commits.Settings {
				s := lintSettings()
				s.AllowedScopes = []string{"cli", "core"}
				return s
			},
			want:     []string{commits.RuleScopeAllowed},
			severity: commits.SeverityError,
		},
		{
			name:     "header too long",
			text:     "feat(cli): " + strings.Repeat("x", 70),
			want:     []string{commits.RuleHeaderLength},
			severity: commits.SeverityError,
		},
		{
			name:     "capitalised description",
			text:     "feat(cli): Add the --json flag",
			want:     []string{commits.RuleDescriptionCase},
			severity: commits.SeverityWarn,
		},
		{
			name:     "trailing full stop",
			text:     "feat(cli): add the --json flag.",
			want:     []string{commits.RuleDescriptionPeriod},
			severity: commits.SeverityWarn,
		},
		{
			name:     "body glued to the header",
			text:     "feat(cli): add the --json flag\nIt prints one document.",
			want:     []string{commits.RuleBodyLeadingBlank},
			severity: commits.SeverityError,
		},
		{
			name:     "unexplained breaking change",
			text:     "feat(cli)!: drop the --format flag",
			want:     []string{commits.RuleBreakingExplained},
			severity: commits.SeverityError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settings := lintSettings()
			if tc.settings != nil {
				settings = tc.settings()
			}
			got := lint(t, tc.text, commits.LintOptions{Settings: settings})
			if !slices.Equal(ruleIDs(got), tc.want) {
				t.Fatalf("rules = %v, want %v", ruleIDs(got), tc.want)
			}
			for _, v := range got {
				if v.Severity != tc.severity {
					t.Errorf("%s severity = %s, want %s", v.Rule, v.Severity, tc.severity)
				}
			}
		})
	}
}

// Every violation must carry a column and a fix, since the gate renders both
// and principle P10 requires the "how to fix" line.
func TestLintViolationsAreActionable(t *testing.T) {
	t.Parallel()
	for _, s := range readCorpus(t, "invalid.txt") {
		for _, v := range commits.Lint(commits.Parse(s.text), commits.LintOptions{Settings: s.settings}) {
			if v.Column < 1 {
				t.Errorf("invalid.txt:%d %s has column %d, want >= 1", s.line, v.Rule, v.Column)
			}
			if strings.TrimSpace(v.Message) == "" {
				t.Errorf("invalid.txt:%d %s has no message", s.line, v.Rule)
			}
			if strings.TrimSpace(v.Fix) == "" {
				t.Errorf("invalid.txt:%d %s has no fix text", s.line, v.Rule)
			}
		}
	}
}

// Columns must point at the offending text, not merely be non-zero.
func TestLintColumns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		text     string
		settings func() commits.Settings
		rule     string
		want     int
	}{
		{
			name: "type starts at column 1",
			text: "Feat(cli): add thing",
			rule: commits.RuleTypeAllowed, want: 1,
		},
		{
			name: "description case points at the first letter",
			text: "feat(cli): Add thing",
			rule: commits.RuleDescriptionCase, want: 12,
		},
		{
			name: "trailing period points at the full stop",
			text: "feat(cli): add thing.",
			rule: commits.RuleDescriptionPeriod, want: 21,
		},
		{
			name: "breaking points at the bang",
			text: "feat(cli)!: drop thing",
			rule: commits.RuleBreakingExplained, want: 10,
		},
		{
			name: "header length points just past the limit",
			text: "feat(cli): " + strings.Repeat("x", 70),
			rule: commits.RuleHeaderLength, want: 73,
		},
		{
			name: "scope required points where a scope would go",
			text: "feat: add thing",
			settings: func() commits.Settings {
				s := lintSettings()
				s.ScopeRequired = true
				return s
			},
			rule: commits.RuleScopeRequired, want: 5,
		},
		{
			name: "allowed scope points at the offending scope",
			text: "feat(cli,payments): add thing",
			settings: func() commits.Settings {
				s := lintSettings()
				s.AllowedScopes = []string{"cli", "core"}
				return s
			},
			rule: commits.RuleScopeAllowed, want: 10,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settings := lintSettings()
			if tc.settings != nil {
				settings = tc.settings()
			}
			for _, v := range lint(t, tc.text, commits.LintOptions{Settings: settings}) {
				if v.Rule != tc.rule {
					continue
				}
				if v.Column != tc.want {
					t.Errorf("%s column = %d, want %d (header %q)", v.Rule, v.Column, tc.want, tc.text)
				}
				return
			}
			t.Fatalf("%s never fired for %q", tc.rule, tc.text)
		})
	}
}

// Only the offending scope of a multi-scope header is reported.
func TestLintReportsEachDisallowedScope(t *testing.T) {
	t.Parallel()
	s := lintSettings()
	s.AllowedScopes = []string{"cli", "core"}
	got := lint(t, "refactor(cli,payments,billing): tidy", commits.LintOptions{Settings: s})
	if len(got) != 2 {
		t.Fatalf("got %d violations, want one per disallowed scope: %v", len(got), ruleIDs(got))
	}
	for _, v := range got {
		if !strings.Contains(v.Message, "payments") && !strings.Contains(v.Message, "billing") {
			t.Errorf("violation names the wrong scope: %s", v.Message)
		}
		if strings.Contains(v.Message, `"cli"`) {
			t.Errorf("an allowed scope was reported: %s", v.Message)
		}
	}
}

// Acronyms and backticked identifiers are legitimate ways to open a
// description, so the case rule must leave them alone.
func TestLintDescriptionCaseExceptions(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"feat(cli): CSV export for invoices",
		"fix(core): I/O timeout on slow disks",
		"fix(ui): `useQuota` returns a stale value",
		"docs(cli): GPU quota limits explained",
	} {
		if got := lint(t, text, commits.LintOptions{Settings: lintSettings()}); len(got) != 0 {
			t.Errorf("Lint(%q) = %v, want no violations", text, ruleIDs(got))
		}
	}
}

// git writes merge, revert and autosquash messages itself; linting them would
// report problems no human can fix.
func TestLintSkipsGeneratedMessages(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"Merge branch 'main' into feature",
		"Merge pull request #12 from 17twenty/x",
		`Revert "feat(billing): add CSV export"`,
		"fixup! feat(billing): add CSV export",
		"squash! feat(billing): add CSV export",
		"amend! feat(billing): add CSV export",
	} {
		if got := lint(t, text, commits.LintOptions{Settings: lintSettings()}); got != nil {
			t.Errorf("Lint(%q) = %v, want nothing for a generated message", text, ruleIDs(got))
		}
	}
}

// A broken grammar makes every other rule guesswork, so only the format rule
// is reported even when other problems are visible (spec 08 §2).
func TestLintStopsAtHeaderFormat(t *testing.T) {
	t.Parallel()
	got := lint(t, "Added a CSV export.\nNo blank line either.", commits.LintOptions{Settings: lintSettings()})
	if !slices.Equal(ruleIDs(got), []string{commits.RuleHeaderFormat}) {
		t.Errorf("rules = %v, want only %s", ruleIDs(got), commits.RuleHeaderFormat)
	}
}

func TestLintPullRequestSubject(t *testing.T) {
	t.Parallel()
	t.Run("breaking explained by a section", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{
			"## Breaking change\n\nThe --format flag is gone.",
			"### Breaking changes\n\nGone.",
			"**Breaking change:**\n\nGone.",
			"BREAKING CHANGE: the --format flag is gone.",
			"breaking change\n\nGone.",
		} {
			got := commits.Lint(commits.Parse("feat(cli)!: drop the --format flag"),
				commits.LintOptions{Settings: lintSettings(), Subject: commits.SubjectPullRequest, Body: body})
			if len(got) != 0 {
				t.Errorf("body %q gave %v, want no violations", body, ruleIDs(got))
			}
		}
	})

	t.Run("breaking unexplained", func(t *testing.T) {
		t.Parallel()
		got := commits.Lint(commits.Parse("feat(cli)!: drop the --format flag"),
			commits.LintOptions{Settings: lintSettings(), Subject: commits.SubjectPullRequest,
				Body: "Just a normal description of the change."})
		if !slices.Equal(ruleIDs(got), []string{commits.RuleBreakingExplained}) {
			t.Errorf("rules = %v, want %s", ruleIDs(got), commits.RuleBreakingExplained)
		}
	})

	// A pull request title has no body attached, so the blank-line rule cannot
	// apply to it (spec 08 §2 marks the rule commit-only).
	t.Run("leading blank rule does not apply", func(t *testing.T) {
		t.Parallel()
		got := commits.Lint(commits.Parse("feat(cli): add thing\nglued body"),
			commits.LintOptions{Settings: lintSettings(), Subject: commits.SubjectPullRequest})
		if slices.Contains(ruleIDs(got), commits.RuleBodyLeadingBlank) {
			t.Errorf("rules = %v, want no %s for a pull request", ruleIDs(got), commits.RuleBodyLeadingBlank)
		}
	})

	// A commit explains "!" with a body or footer, unlike a pull request.
	t.Run("commit breaking explained by body", func(t *testing.T) {
		t.Parallel()
		for _, text := range []string{
			"feat(cli)!: drop --format\n\nIt was never documented.",
			"feat(cli)!: drop --format\n\nBREAKING CHANGE: use --json instead.",
		} {
			if got := lint(t, text, commits.LintOptions{Settings: lintSettings()}); len(got) != 0 {
				t.Errorf("Lint(%q) = %v, want no violations", text, ruleIDs(got))
			}
		}
	})
}

// Violations arrive in AllRules order whatever the input, so output and
// golden files stay stable (spec 01 §5).
func TestLintOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	s := lintSettings()
	s.ScopeRequired = true
	text := "Feat: Add a very long description that runs past the configured limit of seventy-two."
	got := ruleIDs(lint(t, text, commits.LintOptions{Settings: s}))
	if len(got) < 3 {
		t.Fatalf("expected several violations, got %v", got)
	}
	order := commits.AllRules()
	for i := 1; i < len(got); i++ {
		if slices.Index(order, got[i-1]) > slices.Index(order, got[i]) {
			t.Fatalf("violations out of rule order: %v", got)
		}
	}
}

// Empty configuration must not crash or invent rules: an unset Types list
// allows any lowercase type, and an unset HeaderMax imposes no limit.
func TestLintZeroSettings(t *testing.T) {
	t.Parallel()
	text := "anything(scope): " + strings.Repeat("x", 200)
	if got := lint(t, text, commits.LintOptions{}); len(got) != 0 {
		t.Errorf("Lint with zero settings = %v, want no violations", ruleIDs(got))
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	// A capitalised description that also ends in a full stop: two warnings.
	warnOnly := lint(t, "feat(cli): Add thing.", commits.LintOptions{Settings: lintSettings()})
	if len(warnOnly) != 2 {
		t.Fatalf("expected two warnings, got %v", ruleIDs(warnOnly))
	}
	if got := commits.Errors(warnOnly); len(got) != 0 {
		t.Errorf("Errors() = %v, want none", ruleIDs(got))
	}

	mixed := lint(t, "feature(cli): Add thing.", commits.LintOptions{Settings: lintSettings()})
	errs := commits.Errors(mixed)
	if !slices.Equal(ruleIDs(errs), []string{commits.RuleTypeAllowed}) {
		t.Errorf("Errors() = %v, want %s", ruleIDs(errs), commits.RuleTypeAllowed)
	}
}

func TestSeverityString(t *testing.T) {
	t.Parallel()
	if got := commits.SeverityError.String(); got != "error" {
		t.Errorf("SeverityError = %q, want %q", got, "error")
	}
	if got := commits.SeverityWarn.String(); got != "warn" {
		t.Errorf("SeverityWarn = %q, want %q", got, "warn")
	}
}

// AllRules is a published contract; it must list each id exactly once.
func TestAllRulesIsComplete(t *testing.T) {
	t.Parallel()
	rules := commits.AllRules()
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r] {
			t.Errorf("AllRules lists %q twice", r)
		}
		seen[r] = true
		if !strings.HasPrefix(r, "cc.") {
			t.Errorf("rule %q does not use the cc. prefix", r)
		}
	}
	if len(rules) != 9 {
		t.Errorf("AllRules has %d entries, want the 9 of spec 08 §2", len(rules))
	}
}
