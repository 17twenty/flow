package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// skillPath is the agent skill shipped for other harnesses (issue #134).
const skillPath = "skills/flow/SKILL.md"

func readSkill(t *testing.T) (frontmatter map[string]string, body string) {
	t.Helper()
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatalf("%s does not open with YAML frontmatter", skillPath)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatalf("%s opens frontmatter and never closes it", skillPath)
	}
	frontmatter = map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		frontmatter[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return frontmatter, rest[end+len("\n---\n"):]
}

// A skill is chosen by its description before it is read, so the description
// has to say when it applies and not merely what it is about.
func TestTheSkillSaysWhenItApplies(t *testing.T) {
	frontmatter, body := readSkill(t)
	if got := frontmatter["name"]; got != "flow" {
		t.Errorf("skill name is %q, want %q", got, "flow")
	}
	description := frontmatter["description"]
	if !strings.Contains(description, "Use when") {
		t.Errorf("the description never says when to use the skill: %q", description)
	}
	if !strings.Contains(description, ".flow.yaml") {
		t.Errorf("the description never names the thing that makes a repository flow's: %q", description)
	}
	if body == "" {
		t.Error("the skill is frontmatter and nothing else")
	}
}

// inlineCode matches a span of code written in the prose.
var inlineCode = regexp.MustCompile("`([^`\n]+)`")

// codeLines is every line of the skill that is code rather than prose: the
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

// Same obligation as the shipped templates (spec 02 §2): a snippet that tells
// an agent to run a command flow does not have is a promise only a test keeps.
// Every invocation is checked, including one naming no command at all — a
// skill saying `flow release` is exactly the rot this guards against.
func TestTheSkillInvokesRealCommands(t *testing.T) {
	_, body := readSkill(t)
	checked := 0
	for _, line := range codeLines(body) {
		for _, match := range invocation.FindAllStringSubmatch(line, -1) {
			words := strings.Fields(match[1])
			checked++
			if len(words) == 0 || !topLevelCommands[words[0]] {
				t.Errorf("the skill invokes `flow %s`, which is no command of flow's", match[1])
				continue
			}
			if code, _, stderr := exec(t, append(words, "--help")...); code != core.ExitOK {
				t.Errorf("the skill invokes `flow %s`, which exits %d: %s", match[1], code, stderr)
			}
		}
	}
	if checked == 0 {
		t.Error("the skill invokes no flow command; this test is not testing anything")
	}
}

// flagToken is a long flag as the skill writes one.
var flagToken = regexp.MustCompile(`--[a-z][a-z-]+`)

// Naming a command that exists is half of it: the flags beside it have to
// exist too, and kong answers --help before it judges anything else, so the
// previous test cannot see them.
func TestTheSkillNamesRealFlags(t *testing.T) {
	_, body := readSkill(t)
	help := map[string]string{}
	checked := 0
	for _, line := range codeLines(body) {
		match := invocation.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		words := strings.Fields(match[1])
		if len(words) == 0 || !topLevelCommands[words[0]] {
			continue
		}
		command := strings.Join(words, " ")
		if _, ok := help[command]; !ok {
			code, stdout, stderr := exec(t, append(words, "--help")...)
			if code != core.ExitOK {
				t.Fatalf("flow %s --help exits %d: %s", command, code, stderr)
			}
			help[command] = stdout
		}
		for _, flag := range flagToken.FindAllString(line, -1) {
			checked++
			if !strings.Contains(help[command], flag) {
				t.Errorf("the skill passes %s to `flow %s`, which has no such flag", flag, command)
			}
		}
	}
	if checked == 0 {
		t.Error("the skill passes no flag to any command; this test is not testing anything")
	}
}

// dottedName matches a configuration path or a policy rule id as the skill
// writes one in backticks: lower-case words joined by dots, with `[]` for the
// step into a list.
var dottedName = regexp.MustCompile("`([a-z_]+(?:\\[\\])?(?:\\.[a-z_]+(?:\\[\\])?)+)`")

// fence matches a fenced code block, which is example text rather than a claim
// about flow's vocabulary.
var fence = regexp.MustCompile("(?s)```.*?```")

// Every dotted name in the prose is either a rule flow publishes or a key flow
// reads. A skill that names a key the configuration does not have sends an
// agent to grep for nothing, which is the same rot as naming a command that
// does not exist — and it rots by itself, when flow renames something.
func TestTheSkillNamesRealRulesAndRealKeys(t *testing.T) {
	_, body := readSkill(t)
	prose := fence.ReplaceAllString(body, "")

	rules := map[string]bool{}
	for _, id := range engine.PolicyRuleIDs() {
		rules[id] = true
	}
	schema := readConfigSchema(t)

	rulesSeen, keysSeen := 0, 0
	for _, match := range dottedName.FindAllStringSubmatch(prose, -1) {
		name := match[1]
		switch {
		case rules[name]:
			rulesSeen++
		case schemaHas(schema, name):
			keysSeen++
		default:
			t.Errorf("the skill names %q, which is neither a policy rule nor a configuration key", name)
		}
	}
	if rulesSeen == 0 || keysSeen == 0 {
		t.Errorf("the skill named %d rules and %d keys; this test is not testing anything", rulesSeen, keysSeen)
	}
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

// A skill nobody is told about is a file. The README is where a reader of this
// repository finds out it exists.
func TestTheReadmeSaysWhereTheSkillIs(t *testing.T) {
	content, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), skillPath) {
		t.Errorf("the README never mentions %s", skillPath)
	}
}
