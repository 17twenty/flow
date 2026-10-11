package commits

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule identifiers. These are a public contract: they appear in gate output,
// policy configuration and the seed corpus, so they must stay stable (spec 08 §2).
const (
	// RuleHeaderFormat covers the Conventional Commit grammar itself.
	RuleHeaderFormat = "cc.header.format"
	// RuleTypeAllowed requires a lowercase type drawn from commits.types.
	RuleTypeAllowed = "cc.type.allowed"
	// RuleScopeRequired requires a scope when the configuration demands one.
	RuleScopeRequired = "cc.scope.required"
	// RuleScopeAllowed requires every scope to come from commits.scopes.allowed.
	RuleScopeAllowed = "cc.scope.allowed"
	// RuleHeaderLength limits the header to commits.header_max runes.
	RuleHeaderLength = "cc.header.length"
	// RuleDescriptionCase prefers a lowercase first letter.
	RuleDescriptionCase = "cc.description.case"
	// RuleDescriptionPeriod rejects a trailing full stop.
	RuleDescriptionPeriod = "cc.description.period"
	// RuleBodyLeadingBlank requires a blank line between header and body.
	RuleBodyLeadingBlank = "cc.body.leading_blank"
	// RuleBreakingExplained requires "!" to be explained.
	RuleBreakingExplained = "cc.breaking.explained"
)

// AllRules lists every rule id in evaluation order, which is also the order
// violations are reported in, so output is deterministic (spec 01 §5).
func AllRules() []string {
	return []string{
		RuleHeaderFormat,
		RuleTypeAllowed,
		RuleScopeRequired,
		RuleScopeAllowed,
		RuleHeaderLength,
		RuleDescriptionCase,
		RuleDescriptionPeriod,
		RuleBodyLeadingBlank,
		RuleBreakingExplained,
	}
}

// Severity grades a violation. Warnings describe style; errors block.
type Severity int

const (
	// SeverityWarn marks a stylistic violation.
	SeverityWarn Severity = iota
	// SeverityError marks a violation that fails the check.
	SeverityError
)

// String returns the severity's lowercase name.
func (s Severity) String() string {
	if s == SeverityError {
		return "error"
	}
	return "warn"
}

// Violation is one rule failure against one message.
type Violation struct {
	// Rule is the stable rule id, for example "cc.type.allowed".
	Rule string `json:"rule"`
	// Severity is the grade this rule carries by default.
	Severity Severity `json:"-"`
	// Message states what is wrong, in one line.
	Message string `json:"message"`
	// Column is the 1-based rune column in the header the problem starts at,
	// or 1 for violations that concern the body.
	Column int `json:"column"`
	// Fix tells the author how to put it right.
	Fix string `json:"fix"`
}

// Subject distinguishes the two things flow lints. Under a squash merge the PR
// title becomes the commit header, so the gate lints titles while the optional
// local hook lints individual commits (spec 08).
type Subject int

const (
	// SubjectCommit is a commit message from a hook or git log.
	SubjectCommit Subject = iota
	// SubjectPullRequest is a pull request title, checked with its body.
	SubjectPullRequest
)

// DefaultHeaderMax is the header limit used when Settings leaves it at zero.
const DefaultHeaderMax = 72

// DefaultTypes is the commit type list spec 03 §1 ships as the default. It is
// repeated here, rather than imported, because this package must not depend on
// internal/config (spec 01 §2); config overrides it from .flow.yaml.
func DefaultTypes() []string {
	return []string{"feat", "fix", "perf", "refactor", "test", "docs", "build",
		"ci", "chore", "revert", "style"}
}

// DefaultSettings returns the configuration spec 03 §1 defaults to, for callers
// that have no .flow.yaml to read.
func DefaultSettings() Settings {
	return Settings{Types: DefaultTypes(), HeaderMax: DefaultHeaderMax}
}

// Settings mirrors the commits.* section of .flow.yaml. This package never
// reads configuration files: internal/config maps into this struct, keeping
// commits dependent on the standard library alone (spec 01 §2).
type Settings struct {
	// Types are the permitted commit types. Empty means any lowercase type.
	Types []string
	// HeaderMax limits the header in runes. Zero means no limit.
	HeaderMax int
	// ScopeRequired demands a scope on every header.
	ScopeRequired bool
	// AllowedScopes restricts scopes. Empty disables the check entirely.
	AllowedScopes []string
}

// Options tells the linter what it is checking and against what configuration.
type LintOptions struct {
	// Settings is the repository's commit configuration.
	Settings Settings
	// Subject selects commit or pull request semantics.
	Subject Subject
	// Body is the pull request description. It is consulted only when Subject
	// is SubjectPullRequest, to satisfy cc.breaking.explained.
	Body string
}

// Lint checks one parsed message and returns its violations in rule order.
//
// Messages git generates itself — merges, reverts and autosquash commits — are
// skipped: they cannot satisfy the grammar and no human chose their wording.
// When the header does not parse, only cc.header.format is reported, because
// every other rule would be guesswork (spec 08 §2).
func Lint(m Message, opts LintOptions) []Violation {
	if m.Kind.Generated() {
		return nil
	}
	if !m.Conventional() {
		return []Violation{{
			Rule:     RuleHeaderFormat,
			Severity: SeverityError,
			Message:  "header is not a Conventional Commit",
			Column:   1,
			Fix:      "write it as type(scope): description, for example feat(cli): add --json",
		}}
	}

	var out []Violation
	add := func(v Violation) { out = append(out, v) }

	checkType(m, opts.Settings, add)
	checkScope(m, opts.Settings, add)
	checkHeaderLength(m, opts.Settings, add)
	checkDescription(m, add)
	checkBody(m, opts, add)
	checkBreaking(m, opts, add)

	return out
}

// Errors returns only the error-severity violations in vs.
func Errors(vs []Violation) []Violation {
	var out []Violation
	for _, v := range vs {
		if v.Severity == SeverityError {
			out = append(out, v)
		}
	}
	return out
}

// checkType enforces cc.type.allowed: lowercase, and drawn from the configured
// list when one is set.
func checkType(m Message, s Settings, add func(Violation)) {
	lower := strings.ToLower(m.Type)
	if lower != m.Type {
		add(Violation{
			Rule:     RuleTypeAllowed,
			Severity: SeverityError,
			Message:  fmt.Sprintf("type %q must be lowercase", m.Type),
			Column:   m.TypeColumn,
			Fix:      fmt.Sprintf("write %q", lower),
		})
		return
	}
	if len(s.Types) == 0 || slices.Contains(s.Types, lower) {
		return
	}
	add(Violation{
		Rule:     RuleTypeAllowed,
		Severity: SeverityError,
		Message:  fmt.Sprintf("type %q is not one of the configured types", m.Type),
		Column:   m.TypeColumn,
		Fix:      "use one of: " + strings.Join(s.Types, ", "),
	})
}

// checkScope enforces cc.scope.required and cc.scope.allowed.
func checkScope(m Message, s Settings, add func(Violation)) {
	if s.ScopeRequired && !m.HasScope {
		add(Violation{
			Rule:     RuleScopeRequired,
			Severity: SeverityError,
			Message:  "a scope is required",
			Column:   m.TypeColumn + utf8.RuneCountInString(m.Type),
			Fix:      scopeFix("add a scope", s.AllowedScopes),
		})
	}
	if len(s.AllowedScopes) == 0 || !m.HasScope {
		return
	}
	for _, sc := range scopePositions(m) {
		if slices.Contains(s.AllowedScopes, sc.name) {
			continue
		}
		add(Violation{
			Rule:     RuleScopeAllowed,
			Severity: SeverityError,
			Message:  fmt.Sprintf("scope %q is not one of the configured scopes", sc.name),
			Column:   sc.column,
			Fix:      scopeFix("use", s.AllowedScopes),
		})
	}
}

// scopeFix builds a fix hint naming the allowed scopes, truncated so the
// message stays readable when a project allows many.
func scopeFix(verb string, allowed []string) string {
	if len(allowed) == 0 {
		return verb + " in parentheses after the type, for example feat(cli):"
	}
	const maxListed = 12
	if len(allowed) > maxListed {
		return fmt.Sprintf("%s one of: %s, … (%d allowed in total)",
			verb, strings.Join(allowed[:maxListed], ", "), len(allowed))
	}
	return verb + " one of: " + strings.Join(allowed, ", ")
}

// scopePosition is one comma-separated scope and where it sits in the header.
type scopePosition struct {
	name   string
	column int
}

// scopePositions locates each comma-separated scope within the header so that
// a violation points at the offending scope rather than the whole list.
func scopePositions(m Message) []scopePosition {
	var out []scopePosition
	cursor := 0
	for _, part := range strings.Split(m.Scope, ",") {
		name := strings.TrimSpace(part)
		if name != "" {
			offset := cursor + strings.Index(part, name)
			out = append(out, scopePosition{
				name:   name,
				column: m.ScopeColumn + utf8.RuneCountInString(m.Scope[:offset]),
			})
		}
		cursor += len(part) + 1 // the comma
	}
	return out
}

// checkHeaderLength enforces cc.header.length.
func checkHeaderLength(m Message, s Settings, add func(Violation)) {
	max := s.HeaderMax
	if max <= 0 {
		return
	}
	n := m.HeaderLen()
	if n <= max {
		return
	}
	add(Violation{
		Rule:     RuleHeaderLength,
		Severity: SeverityError,
		Message:  fmt.Sprintf("header is %d characters, limit is %d", n, max),
		Column:   max + 1,
		Fix:      fmt.Sprintf("shorten the description by %d characters", n-max),
	})
}

// checkDescription enforces cc.description.case and cc.description.period.
func checkDescription(m Message, add func(Violation)) {
	desc := m.Description

	if first, ok := shouldLowercase(desc); ok {
		add(Violation{
			Rule:     RuleDescriptionCase,
			Severity: SeverityWarn,
			Message:  fmt.Sprintf("description should start lowercase, not %q", first),
			Column:   m.DescriptionColumn,
			Fix:      fmt.Sprintf("write %q", unicode.ToLower(first)),
		})
	}

	trimmed := strings.TrimRight(desc, " \t")
	if strings.HasSuffix(trimmed, ".") {
		add(Violation{
			Rule:     RuleDescriptionPeriod,
			Severity: SeverityWarn,
			Message:  "description should not end with a full stop",
			Column:   m.DescriptionColumn + utf8.RuneCountInString(trimmed) - 1,
			Fix:      "remove the trailing full stop",
		})
	}
}

// shouldLowercase reports whether the description opens with a capital that is
// neither an acronym nor a backticked identifier, and returns that rune.
func shouldLowercase(desc string) (rune, bool) {
	d := strings.TrimLeft(desc, " \t")
	if d == "" {
		return 0, false
	}
	first, _ := utf8.DecodeRuneInString(d)
	if first == '`' || !unicode.IsUpper(first) {
		return first, false
	}
	// Two or more capitals in the opening word reads as an acronym (CSV, GPU,
	// I/O), which is left alone.
	word := d
	if i := strings.IndexAny(d, " \t"); i >= 0 {
		word = d[:i]
	}
	capitals := 0
	for _, r := range word {
		if unicode.IsUpper(r) {
			capitals++
		}
	}
	return first, capitals < 2
}

// checkBody enforces cc.body.leading_blank, which applies to commit messages
// only: a pull request title has no body attached to it.
func checkBody(m Message, opts LintOptions, add func(Violation)) {
	if opts.Subject != SubjectCommit || m.BodyLeadingBlank {
		return
	}
	add(Violation{
		Rule:     RuleBodyLeadingBlank,
		Severity: SeverityError,
		Message:  "body must be separated from the header by a blank line",
		Column:   1,
		Fix:      "insert a blank line after the header",
	})
}

// checkBreaking enforces cc.breaking.explained. A commit explains "!" with a
// BREAKING CHANGE footer or a body; a pull request explains it with a
// "Breaking change" section in its description.
func checkBreaking(m Message, opts LintOptions, add func(Violation)) {
	if !m.Breaking {
		return
	}
	explained := m.HasBreakingChange || m.HasBody
	fix := "add a BREAKING CHANGE: footer, or describe the break in the body"
	if opts.Subject == SubjectPullRequest {
		explained = hasBreakingSection(opts.Body)
		fix = `add a "Breaking change" section to the pull request description`
	}
	if explained {
		return
	}
	add(Violation{
		Rule:     RuleBreakingExplained,
		Severity: SeverityError,
		Message:  "the ! marks a breaking change that is never explained",
		Column:   m.BreakingColumn,
		Fix:      fix,
	})
}

// hasBreakingSection reports whether body carries a "Breaking change" heading
// or a BREAKING CHANGE footer. Headings may be markdown or bold, singular or
// plural, in any case.
func hasBreakingSection(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if _, ok := findBreakingChange(t); ok {
			return true
		}
		t = strings.Trim(t, "#*_ \t")
		t = strings.TrimSuffix(strings.TrimSpace(t), ":")
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "breaking change", "breaking changes":
			return true
		}
	}
	return false
}
