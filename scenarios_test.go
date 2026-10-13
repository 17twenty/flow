package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The acceptance scenarios of spec 19 are the list this whole program is
// answerable to, and the one thing a list of promises must not be able to do
// is lose an entry. Spec 19 opens by saying each scenario becomes a test named
// `TestScenario_<ID>_…`, which makes the connection between a promise and the
// test that keeps it a fact about the source rather than a claim in a comment:
// this file reads the spec, reads the tests, and refuses to let them disagree.

// scenarioPath is the spec that lists what flow has promised to do.
const scenarioPath = ".ralph/specs/19-acceptance-scenarios.md"

// scenarioHeadingRe matches a scenario in spec 19: a bullet whose bold run
// opens with the id, whether or not the name of the scenario is inside the
// bold with it. The Decisions at the foot of that file are written the same
// way, which is why [scenarioIDs] stops reading before them — a decision about
// a scenario is not a second scenario.
var scenarioHeadingRe = regexp.MustCompile(`(?m)^- \*\*([AGVS][0-9]{2})(?: |\*\*)`)

// scenarioBulletRe matches every bullet in the scenario section, whether or
// not it opens with an id. [scenarioIDs] requires the two to agree: a regular
// expression that stopped recognising a heading would otherwise lose the
// promise silently, and a list of promises losing an entry is the one thing
// this file exists to stop.
var scenarioBulletRe = regexp.MustCompile(`(?m)^- \*\*.*`)

// scenarioTestRe matches the declaration of a test that claims a scenario.
var scenarioTestRe = regexp.MustCompile(`(?m)^func (TestScenario_([AGVS][0-9]{2})_[A-Za-z0-9_]+)\(`)

// TestEveryAcceptanceScenarioHasATest is the acceptance criterion of plan item
// 8.1 asserted on itself. A scenario spec 19 lists and nothing tests is a
// promise flow has quietly stopped keeping, and a scenario id that only a test
// knows about is a test nobody can trace back to a promise.
func TestEveryAcceptanceScenarioHasATest(t *testing.T) {
	promised := scenarioIDs(t)
	tested := scenarioTests(t)

	for _, id := range promised {
		if len(tested[id]) == 0 {
			t.Errorf("scenario %s is in %s and no test named TestScenario_%s_… keeps it", id, scenarioPath, id)
		}
	}
	for id, names := range tested {
		if !slices.Contains(promised, id) {
			t.Errorf("%v claims scenario %s, which %s does not list", names, id, scenarioPath)
		}
	}
}

// scenarioIDs is every scenario spec 19 lists, in the order it lists them.
func scenarioIDs(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatalf("the acceptance scenarios are what this test reads: %v", err)
	}
	text := string(source)
	// Everything from the Decisions heading on is commentary on the
	// scenarios above, written in the same shape.
	if cut := strings.Index(text, "\n## Decisions"); cut >= 0 {
		text = text[:cut]
	}
	var ids []string
	for _, match := range scenarioHeadingRe.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(ids, match[1]) {
			ids = append(ids, match[1])
		}
	}
	// A regular expression that matched nothing would make every assertion
	// below vacuous, and a spec that listed no scenarios would be the same
	// green run as a spec nobody had broken.
	if len(ids) == 0 {
		t.Fatalf("%s lists no scenarios, so this test proves nothing", scenarioPath)
	}
	for _, bullet := range scenarioBulletRe.FindAllString(text, -1) {
		if !scenarioHeadingRe.MatchString(bullet) {
			t.Errorf("%s lists %q, which does not open with a scenario id this test can recognise", scenarioPath, bullet)
		}
	}
	return ids
}

// scenarioTests is every test claiming a scenario, by id. It walks the source
// rather than the compiled test binaries because the scenarios are spread over
// four packages and two build tags, and a test only one tag can see is still a
// test that keeps its promise.
func scenarioTests(t *testing.T) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir():
			// Dot directories are the repository's own machinery: .git,
			// and the worktrees a second branch is checked out into,
			// whose tests are a different revision's business.
			if path != "." && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		case !strings.HasSuffix(path, "_test.go"):
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range scenarioTestRe.FindAllStringSubmatch(string(source), -1) {
			found[match[2]] = append(found[match[2]], match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tests: %v", err)
	}
	return found
}
