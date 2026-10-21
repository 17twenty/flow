package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/commits"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// Every document flow ships makes claims about flow, and a claim in prose is
// the kind that rots quietly: a command is renamed, a flag goes, a key is
// never implemented, and the paragraph beside it goes on saying what used to
// be true. The skill has been read this way since it was written (issue #134);
// issue #168 is what happens when the README is not — it promised auto-merge
// for a fortnight, two specs reasoned about it, and no code did it.
//
// These tests read every shipped document the same way: the commands it
// invokes, the flags beside them, and the dotted names in its prose.

// proseDocs is every document flow ships to a reader. The specs are not here:
// they are the authority the code answers to, read by specs_test.go, and they
// reason about providers flow has no adapter for on purpose.
func proseDocs(t *testing.T) map[string]string {
	t.Helper()
	docs := map[string]string{}
	read := func(path string) {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		docs[path] = string(content)
	}
	read("README.md")
	for _, dir := range []string{"docs", "skills"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && filepath.Ext(path) == ".md" {
				read(path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	if len(docs) < 3 {
		t.Fatalf("found %d documents; this test is not testing anything", len(docs))
	}
	return docs
}

// inlineCode matches a span of code written in the prose.
var inlineCode = regexp.MustCompile("`([^`\n]+)`")

// fence matches a fenced code block, which is example text rather than a claim
// about flow's vocabulary.
var fence = regexp.MustCompile("(?s)```.*?```")

// flagToken is a long flag as a document writes one.
var flagToken = regexp.MustCompile(`--[a-z][a-z-]+`)

// dottedName matches a configuration path, a check id or a policy rule id as a
// document writes one in backticks: lower-case words joined by dots, with `[]`
// for the step into a list.
var dottedName = regexp.MustCompile("`([a-z_]+(?:\\[\\])?(?:\\.[a-z_]+(?:\\[\\])?)+)`")

// codeLines is every line of a document that is code rather than prose: the
// body of each fenced block, and each inline span. Only there does "flow x"
// mean an invocation — the prose says "flow plan" of the noun as often as of
// the command.
func codeLines(body string) []string {
	var lines []string
	for _, block := range fence.FindAllString(body, -1) {
		for _, line := range strings.Split(block, "\n") {
			// A shell comment is prose that happens to sit in a code block.
			code, _, _ := strings.Cut(line, " #")
			lines = append(lines, code)
		}
	}
	for _, span := range inlineCode.FindAllStringSubmatch(fence.ReplaceAllString(body, ""), -1) {
		lines = append(lines, span[1])
	}
	return lines
}

func readConfigSchema(t *testing.T) map[string]any {
	t.Helper()
	dir, err := assets.Schema()
	if err != nil {
		t.Fatal(err)
	}
	content, err := fs.ReadFile(dir, "flow.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

// schemaHas walks a dotted path through the configuration schema, stepping
// into `items` wherever the path says `[]`.
func schemaHas(schema map[string]any, path string) bool {
	node := schema
	for _, step := range strings.Split(path, ".") {
		list := strings.HasSuffix(step, "[]")
		step = strings.TrimSuffix(step, "[]")
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			return false
		}
		node, ok = properties[step].(map[string]any)
		if !ok {
			return false
		}
		if list {
			if node, ok = node["items"].(map[string]any); !ok {
				return false
			}
		}
	}
	return true
}

// negatable matches kong's rendering of a flag that can be turned off, which
// is one flag and prints as two.
var negatable = regexp.MustCompile(`--\[no-]([a-z][a-z-]+)`)

// flagColumn is how far into a line kong will start a flag: two spaces, and
// four more where there is no short form to sit in. Help text is wrapped into
// a column far to the right of it.
const flagColumn = 6

// helpWidth is narrow enough that kong wraps every description it prints.
// Without it the width comes from the environment, so the same help is one
// line on a developer's terminal and three on the runner — and a wrapped line
// can open with something that reads like a flag: `flow plan --help` says
// "for a later flow apply --plan.", which this test once believed was a flag
// of `flow plan`. Wrapping always, everywhere, is what makes the reading below
// answer the same thing in both places.
const helpWidth = "80"

// helpFlags is every flag a command accepts, read from kong's own help so that
// the grammar answers rather than a list in a test.
func helpFlags(t *testing.T, command []string) []string {
	t.Helper()
	t.Setenv("COLUMNS", helpWidth)
	code, stdout, stderr := exec(t, append(slices.Clone(command), "--help")...)
	if code != core.ExitOK {
		t.Fatalf("flow %s --help exits %d: %s", strings.Join(command, " "), code, stderr)
	}
	_, flags, ok := strings.Cut(stdout, "\nFlags:\n")
	if !ok {
		t.Fatalf("flow %s --help prints no flags", strings.Join(command, " "))
	}
	var names []string
	for _, line := range strings.Split(flags, "\n") {
		// Where the dash is says whether this line declares a flag or
		// continues the one above it.
		if dash := strings.IndexByte(line, '-'); dash < 0 || dash > flagColumn {
			continue
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			continue
		}
		if match := negatable.FindStringSubmatch(line); match != nil {
			names = append(names, "--"+match[1], "--no-"+match[1])
			continue
		}
		if _, flag, ok := strings.Cut(line, "--"); ok {
			name, _, _ := strings.Cut(flag, "=")
			name, _, _ = strings.Cut(name, " ")
			names = append(names, "--"+name)
		}
	}
	return names
}

// Same obligation the skill has had since it was written: a document that
// tells a reader to run a command flow does not have is a promise only a test
// keeps. Every invocation in every document is checked, including one naming
// no command at all.
func TestEveryDocumentInvokesRealCommands(t *testing.T) {
	checked := 0
	for path, text := range proseDocs(t) {
		for _, line := range codeLines(text) {
			for _, match := range invocations(line) {
				words := strings.Fields(match)
				checked++
				if len(words) == 0 || !topLevelCommands[words[0]] {
					t.Errorf("%s invokes `flow %s`, which is no command of flow's", path, match)
					continue
				}
				if code, _, stderr := exec(t, append(words, "--help")...); code != core.ExitOK {
					t.Errorf("%s invokes `flow %s`, which exits %d: %s", path, match, code, stderr)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no document invokes a flow command; this test is not testing anything")
	}
}

// invocations is every command a line of code invokes. A name followed by a
// colon is not one of them: `flow health: 17twenty/flow` is the title of the
// issue the daily run keeps, written in code because it is a name, and no
// command of flow's is ever followed by a colon.
func invocations(line string) []string {
	var found []string
	for _, match := range invocation.FindAllStringSubmatchIndex(line, -1) {
		if end := match[1]; end < len(line) && line[end] == ':' {
			continue
		}
		found = append(found, line[match[2]:match[3]])
	}
	return found
}

// Naming a command that exists is half of it: kong answers --help before it
// judges anything else, so the flags beside the command need asking for
// separately.
func TestEveryDocumentNamesRealFlags(t *testing.T) {
	flags := map[string][]string{}
	checked := 0
	for path, text := range proseDocs(t) {
		for _, line := range codeLines(text) {
			match := invocation.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			words := strings.Fields(match[1])
			if len(words) == 0 || !topLevelCommands[words[0]] {
				continue
			}
			command := strings.Join(words, " ")
			if _, ok := flags[command]; !ok {
				flags[command] = helpFlags(t, words)
			}
			for _, flag := range flagToken.FindAllString(line, -1) {
				checked++
				if !slices.Contains(flags[command], flag) {
					t.Errorf("%s passes %s to `flow %s`, which has no such flag", path, flag, command)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no document passes a flag to any command; this test is not testing anything")
	}
}

// Every dotted name in the prose is a rule flow publishes, a check it runs, or
// a key it reads. A document that names a key flow has never had sends a
// reader to grep for nothing — and it rots by itself, when flow renames
// something.
//
// Only names in flow's own namespaces are judged. A document about a forge
// writes `author.login` and `permissions.push`, which are GitHub's words, and
// `main.go`, which is a file; a name whose first word is none of flow's is
// somebody else's and flow has no opinion about it.
func TestEveryDocumentNamesRealRulesChecksAndKeys(t *testing.T) {
	rules := append(engine.PolicyRuleIDs(), commits.AllRules()...)
	checks := everyCheckID(t)
	schema := readConfigSchema(t)

	ours := map[string]bool{}
	for _, name := range slices.Concat(rules, checks) {
		head, _, _ := strings.Cut(name, ".")
		ours[head] = true
	}
	for key := range schema["properties"].(map[string]any) {
		ours[key] = true
	}

	var sawRules, sawChecks, sawKeys int
	for path, text := range proseDocs(t) {
		for _, match := range dottedName.FindAllStringSubmatch(fence.ReplaceAllString(text, ""), -1) {
			name := match[1]
			head, _, _ := strings.Cut(name, ".")
			switch {
			case !ours[head]:
			case slices.Contains(rules, name):
				sawRules++
			case slices.Contains(checks, family(name)):
				sawChecks++
			case schemaHas(schema, name):
				sawKeys++
			default:
				t.Errorf("%s names %q, which is no rule, no check and no configuration key", path, name)
			}
		}
	}
	if sawRules == 0 || sawChecks == 0 || sawKeys == 0 {
		t.Errorf("the documents named %d rules, %d checks and %d keys; this test is not testing anything",
			sawRules, sawChecks, sawKeys)
	}
}
