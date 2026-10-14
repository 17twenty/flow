//go:build flowtest

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// jqField matches a field path the way a jq program in the skill writes one:
// `.exit_code`, `.checks[]`, `.result.state`.
var jqField = regexp.MustCompile(`\.[a-z_]+(?:\[\])?(?:\.[a-z_]+(?:\[\])?)*`)

// The skill tells an agent to read a `flow doctor --json` document and decide
// from it whether the repository is somebody else's problem or its own. A
// field path that does not exist answers nothing at all rather than failing:
// the first draft of this skill asked for `.summary` and `.outcome`, and a run
// that exited 2 with a real failing check printed a confident
// `{"exit": null, "failing": []}`. So the paths are checked against a document
// flow actually produced.
func TestTheSkillReadsFieldsTheDoctorDocumentHas(t *testing.T) {
	_, body := readSkill(t)

	var reading string
	for _, block := range fence.FindAllString(body, -1) {
		if strings.Contains(block, "flow doctor --json") {
			reading = block
		}
	}
	if reading == "" {
		t.Fatal("the skill never shows an agent how to read a doctor document")
	}

	_, stdout, stderr := execAt(t, fakeRepo(t), "--json", "doctor")
	var document any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("doctor --json is not one JSON document (%v):\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	checked := 0
	for _, path := range jqField.FindAllString(reading, -1) {
		steps := strings.Split(strings.TrimPrefix(path, "."), ".")
		checked++
		if !resolvesSomewhere(document, steps) {
			t.Errorf("the skill reads %s out of a doctor document, which has no such field", path)
		}
	}
	if checked < 3 {
		t.Errorf("only %d field paths were checked; this test is not testing anything", checked)
	}
}

// resolvesSomewhere is true when the path reads something from this node or
// from anything under it. The skill's paths are relative as often as not —
// `.result.state` is read inside `.checks[]` — so where a path is rooted is
// the jq program's business and not this test's.
func resolvesSomewhere(node any, steps []string) bool {
	if resolves(node, steps) {
		return true
	}
	switch value := node.(type) {
	case map[string]any:
		for _, child := range value {
			if resolvesSomewhere(child, steps) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if resolvesSomewhere(child, steps) {
				return true
			}
		}
	}
	return false
}

// resolves walks the path from this node exactly, stepping into a list where
// the path says `[]`.
func resolves(node any, steps []string) bool {
	for _, step := range steps {
		list := strings.HasSuffix(step, "[]")
		object, ok := node.(map[string]any)
		if !ok {
			return false
		}
		if node, ok = object[strings.TrimSuffix(step, "[]")]; !ok {
			return false
		}
		if list {
			items, ok := node.([]any)
			if !ok || len(items) == 0 {
				return false
			}
			node = items[0]
		}
	}
	return node != nil
}
