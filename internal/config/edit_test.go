package config

import (
	"errors"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/17twenty/flow/internal/core"
)

// TestSetChangesOneTokenAndNothingElse is the property the whole file exists
// for: a preset a team reads is still the preset they read after flow fills
// it in. Comments, alignment, blank lines and flow style all survive, because
// every byte outside the value is copied rather than re-encoded.
func TestSetChangesOneTokenAndNothingElse(t *testing.T) {
	const src = `# yaml-language-server: $schema=flow.v1.json
version: 1

forge:
  type: github                    # github | gitea | gitlab
  repo: owner/name                # from git remote
  auth: { token_env: GITHUB_TOKEN }

tracker:
  project: ${pending:project}     # the board
`
	for _, tc := range []struct {
		name, path, value, want string
	}{
		{
			name:  "a value with a comment beside it",
			path:  "forge.repo",
			value: "acme/api",
			want:  "  repo: acme/api                  # from git remote",
		},
		{
			name:  "a value inside a flow mapping",
			path:  "forge.auth.token_env",
			value: "FLOW_FORGE_TOKEN",
			want:  "  auth: { token_env: FLOW_FORGE_TOKEN }",
		},
		{
			name:  "a placeholder becoming a value",
			path:  "tracker.project",
			value: "7",
			want:  `  project: "7"                    # the board`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Set([]byte(src), tc.path, tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tc.want) {
				t.Errorf("the edited line is wrong:\n got %s\nwant %s", lineHolding(string(got), tc.path), tc.want)
			}
			assertOnlyLineChanged(t, src, string(got))
			assertReads(t, got, tc.path, tc.value)
		})
	}
}

// TestSetKeepsAValueAStringEvenWhenYAMLWouldNot covers the renderings ladder.
// A project number stored as text must come back as text and not as an
// integer, a value with a colon in it must not become a mapping, and a value
// with a comma in it must survive a flow mapping, where a comma ends the
// entry.
func TestSetKeepsAValueAStringEvenWhenYAMLWouldNot(t *testing.T) {
	const src = `version: 1
tracker:
  project: ${pending:project}
  auth: { token_env: T }
`
	for _, tc := range []struct {
		name, path, value string
	}{
		{name: "a bare number", path: "tracker.project", value: "7"},
		{name: "a word YAML reads as true", path: "tracker.project", value: "yes"},
		{name: "a word YAML reads as null", path: "tracker.project", value: "~"},
		{name: "a colon", path: "tracker.project", value: "PROJ: the board"},
		{name: "a hash", path: "tracker.project", value: "board #3"},
		{name: "a comma in a flow mapping", path: "tracker.auth.token_env", value: "A,B"},
		{name: "a brace in a flow mapping", path: "tracker.auth.token_env", value: "A}B"},
		{name: "a quote", path: "tracker.project", value: `say "hi"`},
		{name: "a backslash", path: "tracker.project", value: `a\b`},
		{name: "a leading space", path: "tracker.project", value: " padded"},
		{name: "empty", path: "tracker.project", value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Set([]byte(src), tc.path, tc.value)
			if err != nil {
				t.Fatalf("Set(%q) = %v", tc.value, err)
			}
			assertReads(t, got, tc.path, tc.value)
		})
	}
}

// TestSetInsertsAKeyTheSectionDoesNotHave covers `--tracker-url`, which names
// a key no preset ships: the Jira and Gitea hosts are the only ones that need
// it, so the presets leave it out and init adds it.
func TestSetInsertsAKeyTheSectionDoesNotHave(t *testing.T) {
	const src = `version: 1

tracker:
  type: jira-cloud
  owner: acme          # the site
  auth:
    token_env: T

workflow:
  done_on: merged
`
	got, err := Set([]byte(src), "tracker.url", "https://acme.atlassian.net")
	if err != nil {
		t.Fatal(err)
	}
	assertReads(t, got, "tracker.url", "https://acme.atlassian.net")

	want := `version: 1

tracker:
  type: jira-cloud
  owner: acme          # the site
  auth:
    token_env: T
  url: https://acme.atlassian.net

workflow:
  done_on: merged
`
	if string(got) != want {
		t.Errorf("inserted in the wrong place:\n got %q\nwant %q", got, want)
	}
}

// TestSetRefusesWhatItCannotDoSafely. Every refusal here is a case where
// guessing would corrupt somebody's configuration, and a corrupted .flow.yaml
// is found by whoever next runs doctor rather than by whoever ran init.
func TestSetRefusesWhatItCannotDoSafely(t *testing.T) {
	const src = `version: 1
forge:
  type: github
  auth: { token_env: T }
workflow:
  columns:
    - { id: backlog }
`
	for _, tc := range []struct {
		name, path, value string
		want              error
	}{
		{name: "a section that is not there", path: "tracker.project", value: "7", want: core.ErrNotFound},
		{name: "a key under a scalar", path: "forge.type.name", value: "x", want: core.ErrNotFound},
		{name: "a key that holds a list", path: "workflow.columns", value: "x", want: core.ErrConflict},
		{name: "a key that holds a mapping", path: "forge.auth", value: "x", want: core.ErrConflict},
		{name: "inserting into a flow mapping", path: "forge.auth.email_env", value: "E", want: core.ErrConflict},
		{name: "an empty path", path: "", value: "x", want: core.ErrValidation},
		{name: "a doubled dot", path: "forge..type", value: "x", want: core.ErrValidation},
		{name: "a trailing dot", path: "forge.", value: "x", want: core.ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Set([]byte(src), tc.path, tc.value)
			if err == nil {
				t.Fatalf("Set(%q) wrote %q instead of refusing", tc.path, got)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Set(%q) = %v, want %v", tc.path, err, tc.want)
			}
		})
	}
}

// TestSetRefusesADocumentItCannotRead. init writes over a preset flow ships,
// so this is a corrupt build rather than a corrupt input — but a value spliced
// into a document that did not parse would be spliced at a guessed offset.
func TestSetRefusesADocumentItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{name: "not YAML at all", src: "forge: {\n"},
		{name: "empty", src: ""},
		{name: "a list at the top", src: "- one\n- two\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Set([]byte(tc.src), "forge.repo", "a/b"); err == nil {
				t.Fatalf("Set on %q wrote %q", tc.src, got)
			} else if !errors.Is(err, core.ErrValidation) {
				t.Errorf("Set on %q = %v, want %v", tc.src, err, core.ErrValidation)
			}
		})
	}
}

// TestSetIsRepeatable: init makes several substitutions one after another, so
// the output of one has to be a document the next can edit, and setting the
// same value twice has to be the same file.
func TestSetIsRepeatable(t *testing.T) {
	const src = `version: 1
forge:
  type: github      # forge
  repo: owner/name  # repo
tracker:
  owner: owner      # login
`
	once, err := Set([]byte(src), "forge.repo", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Set(once, "tracker.owner", "acme")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Set(twice, "tracker.owner", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(twice) {
		t.Errorf("setting the same value twice changed the file:\n got %q\nwant %q", again, twice)
	}
	want := `version: 1
forge:
  type: github      # forge
  repo: acme/api    # repo
tracker:
  owner: acme       # login
`
	if string(twice) != want {
		t.Errorf("got %q, want %q", twice, want)
	}
}

// TestSetEditsEveryPresetItShips is the test that matters for init: the paths
// init fills in must be findable and editable in all three presets, so that
// adding a preset without one of them fails here and not in somebody's repo.
func TestSetEditsEveryPresetItShips(t *testing.T) {
	fsys := presetsFS(t).FS()
	paths := []string{"forge.type", "forge.repo", "forge.url", "tracker.type", "tracker.owner", "tracker.owner_type", "tracker.project"}
	for _, name := range PresetNames {
		t.Run(name, func(t *testing.T) {
			src, err := PresetSource(fsys, name)
			if err != nil {
				t.Fatal(err)
			}
			edited := src
			for _, path := range paths {
				edited, err = Set(edited, path, "flow-test-"+path)
				if err != nil {
					t.Fatalf("Set(%s): %v", path, err)
				}
			}
			if _, err := Set(edited, "tracker.url", "https://example.test"); err != nil {
				t.Fatalf("insert tracker.url: %v", err)
			}
			if lines, was := strings.Count(string(edited), "\n"), strings.Count(string(src), "\n"); lines != was {
				t.Errorf("the file grew from %d lines to %d", was, lines)
			}
			if comments, was := strings.Count(string(edited), "#"), strings.Count(string(src), "#"); comments != was {
				t.Errorf("the file went from %d comment markers to %d", was, comments)
			}
		})
	}
}

// TestSetRealignsCommentsAndNothingElse pins the one piece of tidying Set
// does. A preset is written in columns, so a shorter value is padded back out
// to leave its explanation where it was. Punctuation is not an explanation:
// the brace closing a flow mapping stays beside its value.
func TestSetRealignsCommentsAndNothingElse(t *testing.T) {
	const src = `version: 1
forge:
  repo: a-rather-long-name/here   # from git remote
  auth: { token_env: A_LONG_TOKEN_NAME }
  url: https://github.com
`
	got, err := Set([]byte(src), "forge.repo", "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  repo: a/b                       # from git remote"; !strings.Contains(string(got), want) {
		t.Errorf("the comment moved:\n got %s\nwant %s", lineHolding(string(got), "forge.repo"), want)
	}

	got, err = Set([]byte(src), "forge.auth.token_env", "T")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  auth: { token_env: T }"; !strings.Contains(string(got), want) {
		t.Errorf("the brace was pushed away:\n got %s\nwant %s", lineHolding(string(got), "forge.auth.token_env"), want)
	}

	got, err = Set([]byte(src), "forge.url", "https://git.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  url: https://git.example.test\n"; !strings.Contains(string(got), want) {
		t.Errorf("a value with no comment was padded:\n got %q", got)
	}
}

// lineHolding finds the line a path's value was written on, for a readable
// failure message.
func lineHolding(document, path string) string {
	name := path[strings.LastIndex(path, ".")+1:]
	for _, line := range strings.Split(document, "\n") {
		if strings.Contains(line, name+":") {
			return line
		}
	}
	return "(not found)"
}

// assertOnlyLineChanged checks that exactly one line differs.
func assertOnlyLineChanged(t *testing.T, before, after string) {
	t.Helper()
	was, now := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(was) != len(now) {
		t.Fatalf("the document went from %d lines to %d", len(was), len(now))
	}
	changed := 0
	for i := range was {
		if was[i] != now[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want 1", changed)
	}
}

// assertReads decodes the document and checks the path holds value as a
// string, which is the only thing a caller of Set asked for.
func assertReads(t *testing.T, document []byte, path, value string) {
	t.Helper()
	var into map[string]any
	if err := yaml.Unmarshal(document, &into); err != nil {
		t.Fatalf("the edited document does not parse: %v\n%s", err, document)
	}
	var at any = into
	for _, segment := range strings.Split(path, ".") {
		mapping, ok := at.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q is not a mapping", path, segment)
		}
		at, ok = mapping[segment]
		if !ok {
			t.Fatalf("%s: no %q in the edited document", path, segment)
		}
	}
	got, ok := at.(string)
	if !ok {
		t.Fatalf("%s decoded as %T (%v), want the string %q", path, at, at, value)
	}
	if got != value {
		t.Errorf("%s = %q, want %q", path, got, value)
	}
}
