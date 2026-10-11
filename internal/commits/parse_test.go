package commits_test

import (
	"strings"
	"testing"

	"github.com/17twenty/flow/internal/commits"
)

func TestParseHeaderGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		in           string
		wantType     string
		wantScope    string
		wantScopes   []string
		wantBreaking bool
		wantDesc     string
	}{
		{
			name:     "type and description only",
			in:       "docs: clarify GPU quota limits in README",
			wantType: "docs", wantDesc: "clarify GPU quota limits in README",
		},
		{
			name:     "single scope",
			in:       "feat(billing): add CSV export for invoices",
			wantType: "feat", wantScope: "billing", wantScopes: []string{"billing"},
			wantDesc: "add CSV export for invoices",
		},
		{
			name:     "breaking marker with scope",
			in:       "feat(api)!: drop v1 volume endpoints",
			wantType: "feat", wantScope: "api", wantScopes: []string{"api"},
			wantBreaking: true, wantDesc: "drop v1 volume endpoints",
		},
		{
			name:     "breaking marker without scope",
			in:       "refactor!: collapse the adapter interfaces",
			wantType: "refactor", wantBreaking: true,
			wantDesc: "collapse the adapter interfaces",
		},
		{
			name:     "comma-separated scopes",
			in:       "refactor(core,api): extract retry policy",
			wantType: "refactor", wantScope: "core,api",
			wantScopes: []string{"core", "api"}, wantDesc: "extract retry policy",
		},
		{
			name:     "scopes with spaces after commas",
			in:       "fix(core, api, ui): align timeouts",
			wantType: "fix", wantScope: "core, api, ui",
			wantScopes: []string{"core", "api", "ui"}, wantDesc: "align timeouts",
		},
		{
			name:     "scope punctuation the grammar allows",
			in:       "chore(ci/release.v2_beta-1): bump runner",
			wantType: "chore", wantScope: "ci/release.v2_beta-1",
			wantScopes: []string{"ci/release.v2_beta-1"}, wantDesc: "bump runner",
		},
		{
			name:     "uppercase type still parses; case is the linter's business",
			in:       "Feat(billing): add CSV export",
			wantType: "Feat", wantScope: "billing", wantScopes: []string{"billing"},
			wantDesc: "add CSV export",
		},
		{
			name:     "conventional revert is an ordinary type",
			in:       "revert: feat(billing): add CSV export for invoices",
			wantType: "revert", wantDesc: "feat(billing): add CSV export for invoices",
		},
		{
			name:     "description keeps its backticks and unicode",
			in:       "fix(ui): correct `useQuota` hook — again",
			wantType: "fix", wantScope: "ui", wantScopes: []string{"ui"},
			wantDesc: "correct `useQuota` hook — again",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := commits.Parse(tc.in)
			if !m.Conventional() {
				t.Fatalf("Parse(%q) did not parse as conventional", tc.in)
			}
			if m.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", m.Type, tc.wantType)
			}
			if m.Scope != tc.wantScope {
				t.Errorf("Scope = %q, want %q", m.Scope, tc.wantScope)
			}
			if strings.Join(m.Scopes, "|") != strings.Join(tc.wantScopes, "|") {
				t.Errorf("Scopes = %v, want %v", m.Scopes, tc.wantScopes)
			}
			if m.Breaking != tc.wantBreaking {
				t.Errorf("Breaking = %v, want %v", m.Breaking, tc.wantBreaking)
			}
			if m.Description != tc.wantDesc {
				t.Errorf("Description = %q, want %q", m.Description, tc.wantDesc)
			}
			if m.HasScope != (tc.wantScope != "") {
				t.Errorf("HasScope = %v, want %v", m.HasScope, tc.wantScope != "")
			}
		})
	}
}

// Input that breaks the grammar must yield an empty Type rather than an error,
// so the linter can report cc.header.format and stop (spec 08 §1–2).
func TestParseNonConventional(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in string }{
		{"prose", "Added CSV export"},
		{"missing colon", "feat(billing) add CSV export"},
		{"no space after colon", "feat(billing):add CSV export"},
		{"empty description", "feat(billing): "},
		{"whitespace description", "feat:    "},
		{"empty scope", "feat(): add CSV export"},
		{"unterminated scope", "feat(billing: add CSV export"},
		{"scope with illegal character", "feat(bil!ing): add CSV export"},
		{"scope with non-ascii", "feat(billingé): add CSV export"},
		{"digit in type", "feat2(billing): add CSV export"},
		{"empty type", "(billing): add CSV export"},
		{"bang before scope", "feat!(billing): add CSV export"},
		{"empty message", ""},
		{"only whitespace", "   \n\n  "},
		{"only comments", "# a comment\n# another"},
		{"colon alone", ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := commits.Parse(tc.in)
			if m.Conventional() {
				t.Errorf("Parse(%q) parsed as %q/%q, want not conventional",
					tc.in, m.Type, m.Description)
			}
		})
	}
}

func TestParseColumns(t *testing.T) {
	t.Parallel()
	m := commits.Parse("feat(api)!: drop v1 volume endpoints")
	// f=1 e=2 a=3 t=4 (=5 a=6 ... )=9 !=10 :=11 space=12 d=13
	for name, got := range map[string]struct{ have, want int }{
		"TypeColumn":        {m.TypeColumn, 1},
		"ScopeColumn":       {m.ScopeColumn, 6},
		"BreakingColumn":    {m.BreakingColumn, 10},
		"DescriptionColumn": {m.DescriptionColumn, 13},
	} {
		if got.have != got.want {
			t.Errorf("%s = %d, want %d", name, got.have, got.want)
		}
	}
}

// Header length is measured in runes, not bytes, because that is what
// commits.header_max limits and what a terminal shows.
func TestParseHeaderLenCountsRunes(t *testing.T) {
	t.Parallel()
	m := commits.Parse("fix(ui): rèsumé uploads — again")
	if !m.Conventional() {
		t.Fatal("expected a conventional header")
	}
	if got, want := m.HeaderLen(), 31; got != want {
		t.Errorf("HeaderLen = %d, want %d runes", got, want)
	}
	if got := len(m.Header); got <= m.HeaderLen() {
		t.Errorf("expected the byte length (%d) to exceed the rune length (%d)", got, m.HeaderLen())
	}
	if m.DescriptionColumn != 10 {
		t.Errorf("DescriptionColumn = %d, want 10", m.DescriptionColumn)
	}
}

func TestParseBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		in               string
		wantBody         string
		wantHasBody      bool
		wantLeadingBlank bool
	}{
		{
			name:             "header only",
			in:               "docs: clarify quota limits",
			wantHasBody:      false,
			wantLeadingBlank: true,
		},
		{
			name:             "blank line then body",
			in:               "fix: handle empty VM list\n\nThe dashboard crashed.",
			wantBody:         "The dashboard crashed.",
			wantHasBody:      true,
			wantLeadingBlank: true,
		},
		{
			name:             "body glued to the header",
			in:               "fix: handle empty VM list\nThe dashboard crashed.",
			wantBody:         "The dashboard crashed.",
			wantHasBody:      true,
			wantLeadingBlank: false,
		},
		{
			name:             "trailing blank lines are dropped",
			in:               "fix: handle empty VM list\n\nBody here.\n\n\n",
			wantBody:         "Body here.",
			wantHasBody:      true,
			wantLeadingBlank: true,
		},
		{
			name:             "internal blank lines are kept",
			in:               "fix: x\n\nfirst para\n\nsecond para",
			wantBody:         "first para\n\nsecond para",
			wantHasBody:      true,
			wantLeadingBlank: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := commits.Parse(tc.in)
			if m.Body != tc.wantBody {
				t.Errorf("Body = %q, want %q", m.Body, tc.wantBody)
			}
			if m.HasBody != tc.wantHasBody {
				t.Errorf("HasBody = %v, want %v", m.HasBody, tc.wantHasBody)
			}
			if m.BodyLeadingBlank != tc.wantLeadingBlank {
				t.Errorf("BodyLeadingBlank = %v, want %v", m.BodyLeadingBlank, tc.wantLeadingBlank)
			}
		})
	}
}

func TestParseBreakingChangeFooter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		wantText string
		wantHas  bool
	}{
		{
			name:     "spelled with a space",
			in:       "feat(api)!: drop v1\n\nBREAKING CHANGE: /v1/volumes is removed.",
			wantText: "/v1/volumes is removed.",
			wantHas:  true,
		},
		{
			name:     "spelled with a hyphen",
			in:       "feat(api)!: drop v1\n\nBREAKING-CHANGE: /v1/volumes is removed.",
			wantText: "/v1/volumes is removed.",
			wantHas:  true,
		},
		{
			name:    "absent",
			in:      "feat(api)!: drop v1\n\nJust a body.",
			wantHas: false,
		},
		{
			name:    "lowercase is not the footer",
			in:      "feat(api)!: drop v1\n\nbreaking change: nope",
			wantHas: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := commits.Parse(tc.in)
			if m.HasBreakingChange != tc.wantHas {
				t.Fatalf("HasBreakingChange = %v, want %v", m.HasBreakingChange, tc.wantHas)
			}
			if m.BreakingChange != tc.wantText {
				t.Errorf("BreakingChange = %q, want %q", m.BreakingChange, tc.wantText)
			}
		})
	}
}

func TestParseStripsComments(t *testing.T) {
	t.Parallel()
	in := "# Please enter the commit message for your changes.\n" +
		"feat(billing): add CSV export\n" +
		"#\n" +
		"# On branch main\n" +
		"\n" +
		"Body text.\n" +
		"# Changes to be committed:\n"
	m := commits.Parse(in)
	if m.Header != "feat(billing): add CSV export" {
		t.Errorf("Header = %q", m.Header)
	}
	if m.Body != "Body text." {
		t.Errorf("Body = %q, want the comments removed", m.Body)
	}
}

// core.commentChar is configurable, so a repo using ';' must still have its
// comments stripped — and a '#' line must then be ordinary text.
func TestParseCustomCommentChar(t *testing.T) {
	t.Parallel()
	in := "; a comment\nfix(ui): tidy\n\n# not a comment here\n; also a comment"
	m := commits.ParseWithOptions(in, commits.ParseOptions{CommentChar: ';'})
	if m.Header != "fix(ui): tidy" {
		t.Errorf("Header = %q", m.Header)
	}
	if m.Body != "# not a comment here" {
		t.Errorf("Body = %q, want the '#' line kept", m.Body)
	}
}

func TestParseScissors(t *testing.T) {
	t.Parallel()
	in := "feat(core): add thing\n\nReal body.\n" +
		"# ------------------------ >8 ------------------------\n" +
		"# Do not modify or remove the line above.\n" +
		"diff --git a/x b/x\n" +
		"+feat(fake): this must not become the body\n"
	m := commits.Parse(in)
	if m.Header != "feat(core): add thing" {
		t.Errorf("Header = %q", m.Header)
	}
	if m.Body != "Real body." {
		t.Errorf("Body = %q, want everything after the scissors dropped", m.Body)
	}
}

// The scissors line follows the configured comment character too.
func TestParseScissorsCustomCommentChar(t *testing.T) {
	t.Parallel()
	in := "fix: x\n\nBody.\n; ------------------------ >8 ------------------------\ndiff --git a/x b/x"
	m := commits.ParseWithOptions(in, commits.ParseOptions{CommentChar: ';'})
	if m.Body != "Body." {
		t.Errorf("Body = %q, want the scissors section dropped", m.Body)
	}
}

// A comment line that merely mentions a diff marker is not a scissors line.
func TestParseNotScissors(t *testing.T) {
	t.Parallel()
	in := "fix: x\n\nBody.\n# see >8 below for why\nMore body."
	m := commits.Parse(in)
	if want := "Body.\nMore body."; m.Body != want {
		t.Errorf("Body = %q, want %q", m.Body, want)
	}
}

func TestParseKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want commits.Kind
	}{
		{"merge branch", "Merge branch 'main' into feature", commits.KindMerge},
		{"merge pull request", "Merge pull request #12 from 17twenty/x", commits.KindMerge},
		{"generated revert", `Revert "feat(billing): add CSV export"`, commits.KindRevert},
		{"fixup", "fixup! feat(billing): add CSV export", commits.KindFixup},
		{"squash", "squash! feat(billing): add CSV export", commits.KindSquash},
		{"amend", "amend! feat(billing): add CSV export", commits.KindAmend},
		{"ordinary commit", "feat(billing): add CSV export", commits.KindNormal},
		{"conventional revert type", "revert: feat(billing): add CSV", commits.KindNormal},
		{"merged word is not a merge", "Merged everything", commits.KindNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := commits.Parse(tc.in)
			if m.Kind != tc.want {
				t.Errorf("Kind = %v, want %v", m.Kind, tc.want)
			}
			if got, want := m.Kind.Generated(), tc.want != commits.KindNormal; got != want {
				t.Errorf("Generated() = %v, want %v", got, want)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	t.Parallel()
	for kind, want := range map[commits.Kind]string{
		commits.KindNormal: "normal", commits.KindMerge: "merge",
		commits.KindRevert: "revert", commits.KindFixup: "fixup",
		commits.KindSquash: "squash", commits.KindAmend: "amend",
	} {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

// Editors and Windows checkouts deliver CRLF; it must not reach the fields.
func TestParseCRLF(t *testing.T) {
	t.Parallel()
	m := commits.Parse("feat(billing): add CSV export\r\n\r\nBody line.\r\n")
	if m.Header != "feat(billing): add CSV export" {
		t.Errorf("Header = %q, want no carriage return", m.Header)
	}
	if m.Body != "Body line." {
		t.Errorf("Body = %q, want no carriage return", m.Body)
	}
}

// Raw must survive untouched so the autofix can rewrite from the original.
func TestParseKeepsRaw(t *testing.T) {
	t.Parallel()
	in := "# comment\nfeat: x\r\n\r\nbody\n"
	if got := commits.Parse(in).Raw; got != in {
		t.Errorf("Raw = %q, want the input unchanged", got)
	}
}
