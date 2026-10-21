package main

import (
	"os"
	"strings"
	"testing"
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
