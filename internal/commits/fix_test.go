package commits

import (
	"slices"
	"strings"
	"testing"
)

func TestFix(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		rules []string
	}{
		{
			name: "clean message is untouched",
			in:   "feat(cli): add --json\n\nbody text\n",
			want: "feat(cli): add --json\n\nbody text\n",
		},
		{
			name:  "uppercase type is lowercased",
			in:    "Feat(cli): add --json",
			want:  "feat(cli): add --json",
			rules: []string{RuleTypeAllowed},
		},
		{
			name:  "fully uppercase type is lowercased",
			in:    "FIX: stop the leak",
			want:  "fix: stop the leak",
			rules: []string{RuleTypeAllowed},
		},
		{
			name:  "leading whitespace is trimmed",
			in:    "  feat(cli): add --json",
			want:  "feat(cli): add --json",
			rules: []string{RuleHeaderFormat},
		},
		{
			name:  "trailing whitespace is trimmed",
			in:    "feat(cli): add --json   ",
			want:  "feat(cli): add --json",
			rules: []string{RuleHeaderFormat},
		},
		{
			name:  "trailing full stop is stripped",
			in:    "feat(cli): add --json.",
			want:  "feat(cli): add --json",
			rules: []string{RuleDescriptionPeriod},
		},
		{
			name:  "an ellipsis is stripped in one pass",
			in:    "feat(cli): add --json...",
			want:  "feat(cli): add --json",
			rules: []string{RuleDescriptionPeriod},
		},
		{
			name:  "missing blank line after header is inserted",
			in:    "feat(cli): add --json\nbody text",
			want:  "feat(cli): add --json\n\nbody text",
			rules: []string{RuleBodyLeadingBlank},
		},
		{
			name:  "every fix at once",
			in:    "  Feat(cli): add --json.  \nbody text\n",
			want:  "feat(cli): add --json\n\nbody text\n",
			rules: []string{RuleHeaderFormat, RuleTypeAllowed, RuleDescriptionPeriod, RuleBodyLeadingBlank},
		},
		{
			name: "a version number keeps its inner stops",
			in:   "chore(deps): bump kong to v1.16.1.",
			want: "chore(deps): bump kong to v1.16.1",

			rules: []string{RuleDescriptionPeriod},
		},
		{
			name: "description of only stops is left alone",
			in:   "feat(cli): ...",
			want: "feat(cli): ...",
		},
		{
			name: "a non-conventional header is only trimmed",
			in:   "  just some words  ",
			want: "just some words",

			rules: []string{RuleHeaderFormat},
		},
		{
			name: "git-generated messages are not rewritten",
			in:   "Merge branch 'main' into feature\nbody",
			want: "Merge branch 'main' into feature\n\nbody",

			rules: []string{RuleBodyLeadingBlank},
		},
		{
			name:  "blank line is inserted before an intervening comment",
			in:    "feat(cli): add --json\n# please enter a message\nbody text",
			want:  "feat(cli): add --json\n\n# please enter a message\nbody text",
			rules: []string{RuleBodyLeadingBlank},
		},
		{
			name: "comments above the header are preserved",
			in:   "# a leading comment\nFeat(cli): add --json",
			want: "# a leading comment\nfeat(cli): add --json",

			rules: []string{RuleTypeAllowed},
		},
		{
			name: "the scissors section is untouched",
			in:   "Feat(cli): add --json.\n# ------------------------ >8 ------------------------\nFeat: not a real header.\n",
			want: "feat(cli): add --json\n# ------------------------ >8 ------------------------\nFeat: not a real header.\n",

			rules: []string{RuleTypeAllowed, RuleDescriptionPeriod},
		},
		{
			name: "nothing but comments is left alone",
			in:   "# please enter a commit message\n# lines like this are ignored\n",
			want: "# please enter a commit message\n# lines like this are ignored\n",
		},
		{
			name:  "CRLF endings survive a fix",
			in:    "Feat(cli): add --json.\r\nbody text\r\n",
			want:  "feat(cli): add --json\r\n\r\nbody text\r\n",
			rules: []string{RuleTypeAllowed, RuleDescriptionPeriod, RuleBodyLeadingBlank},
		},
		{
			name: "an existing blank line is not doubled",
			in:   "feat(cli): add --json\n\n\nbody text",
			want: "feat(cli): add --json\n\n\nbody text",
		},
		{
			name:  "breaking marker is preserved while the type is fixed",
			in:    "Feat(cli)!: drop --legacy.",
			want:  "feat(cli)!: drop --legacy",
			rules: []string{RuleTypeAllowed, RuleDescriptionPeriod},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rules := Fix(tt.in)
			if got != tt.want {
				t.Errorf("Fix() message:\n got %q\nwant %q", got, tt.want)
			}
			if !slices.Equal(rules, tt.rules) {
				t.Errorf("Fix() rules = %v, want %v", rules, tt.rules)
			}
		})
	}
}

// TestFixIsIdempotent is the acceptance criterion for plan item 1.3: a second
// pass must change nothing and claim no further fixes.
func TestFixIsIdempotent(t *testing.T) {
	inputs := []string{
		"  Feat(cli): add --json.  \nbody text\n",
		"FIX: stop the leak...",
		"feat(cli): add --json\nbody",
		"Feat(cli): add --json.\r\nbody text\r\n",
		"# comment\n  Docs(readme): Explain the gate.\n# another\nbody",
		"feat(cli): ...",
		"",
		"\n\n\n",
		"Merge branch 'main'\nbody",
		"feat(cli)!: drop --legacy.\n\nBREAKING CHANGE: --legacy is gone.",
	}

	for _, in := range inputs {
		once, _ := Fix(in)
		twice, rules := Fix(once)
		if twice != once {
			t.Errorf("Fix not idempotent for %q:\n first %q\nsecond %q", in, once, twice)
		}
		if len(rules) != 0 {
			t.Errorf("Fix(%q) second pass reported rules %v, want none", in, rules)
		}
	}
}

// TestFixResolvesWhatItClaims checks the fixer against the linter: every rule
// Fix reports must actually stop being violated.
func TestFixResolvesWhatItClaims(t *testing.T) {
	inputs := []string{
		"  Feat(cli): add --json.  \nbody text\n",
		"FIX: stop the leak...",
		"Docs(readme): explain the gate\nbody",
		"Feat(cli)!: drop --legacy.\n\nBREAKING CHANGE: gone",
		"  Chore(deps): bump kong.  ",
	}
	settings := Settings{Types: DefaultTypes(), HeaderMax: DefaultHeaderMax}

	for _, in := range inputs {
		fixed, claimed := Fix(in)
		after := Lint(Parse(fixed), LintOptions{Settings: settings})
		for _, rule := range claimed {
			for _, v := range after {
				if v.Rule == rule {
					t.Errorf("Fix(%q) claimed to fix %s but %q still violates it",
						in, rule, fixed)
				}
			}
		}
	}
}

// TestFixNeverBreaksAParsingHeader guards the one thing a rewriter must never
// do: turn a message the linter accepted into one it rejects.
func TestFixNeverBreaksAParsingHeader(t *testing.T) {
	inputs := []string{
		"feat(cli): add --json",
		"fix: stop the leak",
		"feat(cli)!: drop --legacy",
		"chore(deps): bump kong to v1.16.1",
		"feat(a,b): two scopes",
		"feat(cli): ...",
		"refactor: move the thing.",
		"  Feat(cli): add --json.  ",
	}

	for _, in := range inputs {
		before := Parse(in)
		fixed, _ := Fix(in)
		after := Parse(fixed)
		if before.Conventional() && !after.Conventional() {
			t.Errorf("Fix(%q) = %q turned a conventional header unconventional", in, fixed)
		}
		if after.Conventional() && strings.TrimSpace(after.Description) == "" {
			t.Errorf("Fix(%q) = %q left an empty description", in, fixed)
		}
	}
}

func FuzzFix(f *testing.F) {
	for _, seed := range []string{
		"feat(cli): add --json",
		"  Feat(cli): add --json.  \nbody",
		"# c\n>8\nFeat: x.",
		"Merge branch 'x'",
		"\r\n\r\n",
		"feat(cli): ...",
		"a(:",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		fixed, rules := Fix(raw)

		// Idempotence must hold for every input, not just the tidy ones.
		again, more := Fix(fixed)
		if again != fixed {
			t.Fatalf("Fix not idempotent for %q: %q then %q", raw, fixed, again)
		}
		if len(more) != 0 {
			t.Fatalf("Fix(%q) second pass reported %v", raw, more)
		}

		// Every reported rule must be a real one.
		for _, r := range rules {
			if !slices.Contains(AllRules(), r) {
				t.Fatalf("Fix(%q) reported unknown rule %q", raw, r)
			}
		}

		// Fixing must never destroy a header that parsed.
		if before, after := Parse(raw), Parse(fixed); before.Conventional() && !after.Conventional() {
			t.Fatalf("Fix(%q) = %q broke a conventional header", raw, fixed)
		}
	})
}
