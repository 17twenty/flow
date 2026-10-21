package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/17twenty/flow/assets"
	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
	"github.com/17twenty/flow/internal/engine"
)

// A promise with no implementation behind it and an implementation nothing
// promised are the same defect seen from two ends (issue #168). The specs are
// the authority: they name their checks, their rules, their flags and their
// files in lists a person maintains by hand, and the code registers them
// somewhere else entirely. Nothing compared the two until these tests, which
// read every list in both directions, so that neither half can move alone.

const (
	cliSpec     = ".ralph/specs/02-cli.md"
	configSpec  = ".ralph/specs/03-configuration.md"
	doctorSpec  = ".ralph/specs/07-doctor.md"
	policySpec  = ".ralph/specs/09-policy-gate.md"
	testingSpec = ".ralph/specs/14-testing.md"
	specDir     = ".ralph/specs"
)

// specName matches a dotted name as a spec writes one: in backticks, lower
// case, and with `<event>` where a family stands for one id per event.
var specName = regexp.MustCompile("`([a-z_]+(?:\\.[a-z_]+)+(?:\\.<event>)?)`")

// notBuilt matches what a row says when the spec asks for a check nobody has
// written yet. A gap with a ticket behind it is a decision; a gap with nothing
// behind it is the defect this file is about.
var notBuilt = regexp.MustCompile(`not built: \[#([0-9]+)]`)

// family folds the ids a check family produces back into the name the spec
// gives the family. One id per native transition is not one row per native
// transition: the spec names the shape and the configuration decides the
// members.
func family(id string) string {
	switch {
	case strings.HasPrefix(id, "tracker.native."):
		return "tracker.native.<event>"
	case strings.HasPrefix(id, engine.CheckLiveTransitionsPrefix):
		return strings.TrimSuffix(engine.CheckLiveTransitionsPrefix, ".")
	}
	return id
}

// everyCheckID is every check `flow doctor` can run, over every configuration
// flow ships plus its own. A check only exists when some configuration asks
// for it, so one configuration cannot answer what flow has.
func everyCheckID(t *testing.T) []string {
	t.Helper()
	presets, err := assets.Presets()
	if err != nil {
		t.Fatalf("the presets are configurations flow ships: %v", err)
	}
	names, err := assets.List("presets")
	if err != nil {
		t.Fatalf("listing the presets: %v", err)
	}

	var configs []*config.Config
	for _, name := range names {
		name = strings.TrimSuffix(filepath.Base(name), ".yaml")
		cfg, err := config.LoadPreset(presets, name)
		if err != nil {
			t.Fatalf("preset %s does not load: %v", name, err)
		}
		configs = append(configs, cfg)
	}
	own, err := config.Load(config.DefaultPath)
	if err != nil {
		t.Fatalf("flow's own configuration: %v", err)
	}
	configs = append(configs, own, deliveryBoardConfig(t))

	var ids []string
	for _, cfg := range configs {
		registry, err := engine.DoctorRegistry(engine.SetupInput{Config: cfg, ConfigPath: config.DefaultPath})
		if err != nil {
			t.Fatalf("DoctorRegistry: %v", err)
		}
		for _, check := range registry.Checks() {
			if id := family(check.ID); !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// readSpec is a spec file.
func readSpec(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

// section is a spec's heading and everything under it up to the next one.
func section(t *testing.T, path, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(readSpec(t, path), heading)
	if !ok {
		t.Fatalf("%s no longer has a %q, so this test is reading nothing", path, heading)
	}
	rest, _, _ = strings.Cut(rest, "\n## ")
	return rest
}

// tableIDs is the first column of every row of a markdown table, which is
// where each of these specs writes the name of the thing the row is about.
// Only the first column: the rest of a row names the keys and the data the
// thing reads, and a key is not a check.
func tableIDs(text string) map[string]string {
	rows := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cell, rest, _ := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		rows[strings.Trim(cell, "`")] = rest
	}
	return rows
}

// specChecks is every check id the doctor spec names — the setup list of §5,
// which is prose, and the first column of §6's table — and, for the ones the
// spec marks as not built, the issue that will build them.
func specChecks(t *testing.T) (ids []string, gaps map[string]string) {
	t.Helper()
	gaps = map[string]string{}
	add := func(id string) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, match := range specName.FindAllStringSubmatch(section(t, doctorSpec, "## 5. Setup checks"), -1) {
		add(match[1])
	}
	for id, rest := range tableIDs(section(t, doctorSpec, "## 6. Live-health audit")) {
		add(id)
		if match := notBuilt.FindStringSubmatch(rest); match != nil {
			gaps[id] = match[1]
		}
	}
	return ids, gaps
}

// TestEveryCheckTheDoctorSpecNamesIsACheckFlowRuns fails on a check named in
// the spec and registered nowhere — the shape of defect that let the README
// promise auto-merge for a fortnight while two specs reasoned about it.
//
// A check the spec marks as not built is a decision rather than a defect, and
// has to carry the issue that will close it.
func TestEveryCheckTheDoctorSpecNamesIsACheckFlowRuns(t *testing.T) {
	have := everyCheckID(t)
	named, gaps := specChecks(t)
	if len(named) == 0 {
		t.Fatal("read no check out of the doctor spec; this test is not testing anything")
	}
	for _, id := range named {
		switch {
		case slices.Contains(have, id) && gaps[id] != "":
			t.Errorf("%s says %q is not built, and flow runs it: the row is out of date", doctorSpec, id)
		case slices.Contains(have, id) || gaps[id] != "":
		default:
			t.Errorf("%s names the check %q, which no configuration flow ships can run and no issue is waiting to build", doctorSpec, id)
		}
	}
}

// TestEveryCheckFlowRunsIsACheckTheDoctorSpecNames is the other direction. A
// check that grades a repository and appears in no spec is a rule nobody
// agreed to.
func TestEveryCheckFlowRunsIsACheckTheDoctorSpecNames(t *testing.T) {
	named, _ := specChecks(t)
	for _, id := range everyCheckID(t) {
		if !slices.Contains(named, id) {
			t.Errorf("flow runs the check %q, which %s never names", id, doctorSpec)
		}
	}
}

// TestThePolicySpecAndTheGateAgreeOnTheRules reads the rule table of spec 09
// against the gate's own list, in both directions and in order. The order is
// the report's order, so a rule that moved in one and not the other is a
// report that no longer reads like the spec.
func TestThePolicySpecAndTheGateAgreeOnTheRules(t *testing.T) {
	var named []string
	for _, line := range strings.Split(section(t, policySpec, "## 2."), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		named = append(named, strings.Trim(cell, "`"))
	}
	if len(named) == 0 {
		t.Fatalf("read no rule out of %s; this test is not testing anything", policySpec)
	}
	if got := engine.PolicyRuleIDs(); !slices.Equal(got, named) {
		t.Errorf("the gate runs\n\t%v\nand %s names\n\t%v", got, policySpec, named)
	}
}

// commandHeading opens the part of spec 02 §2 that describes one command.
var commandHeading = regexp.MustCompile("(?m)^### `flow ([a-z][a-z -]*)")

// TestTheCLISpecIsTheWholeCommandSurface holds spec 02 to its own first
// sentence: it is the public contract, and changes update it in the same
// commit. Every flag it names has to exist, and every flag kong offers has to
// be named — a flag that appears in `--help` and in no spec is a surface
// nobody agreed to ship.
func TestTheCLISpecIsTheWholeCommandSurface(t *testing.T) {
	spec := readSpec(t, cliSpec)
	spec, _, _ = strings.Cut(spec, "\n## Decisions")

	// §1's table is the global flags, which every command repeats.
	global := []string{"--help"}
	for flag := range tableIDs(section(t, cliSpec, "## 1. Global flags")) {
		if _, long, ok := strings.Cut(flag, "--"); ok {
			global = append(global, "--"+long)
		}
	}
	for _, flag := range helpFlags(t, nil) {
		if !slices.Contains(global, flag) {
			t.Errorf("`flow --help` offers %s, which %s §1 does not name", flag, cliSpec)
		}
	}

	headings := commandHeading.FindAllStringSubmatchIndex(spec, -1)
	if len(headings) == 0 {
		t.Fatalf("read no command out of %s; this test is not testing anything", cliSpec)
	}
	for i, at := range headings {
		end := len(spec)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		body := spec[at[1]:end]

		// A section describes one command and may invoke another: the hook
		// section says what the hook runs, which is `flow check commit
		// --fix`. A flag inside somebody else's invocation is theirs.
		body = otherInvocation.ReplaceAllString(body, "")

		words := strings.Fields(spec[at[2]:at[3]])
		offered := helpFlags(t, words)
		command := strings.Join(words, " ")

		named := append([]string{"--help"}, flagToken.FindAllString(body, -1)...)
		for _, flag := range offered {
			// A negatable flag is one flag and prints as two; naming
			// either form names it.
			if positive, ok := strings.CutPrefix(flag, "--no-"); ok && slices.Contains(named, "--"+positive) {
				continue
			}
			if !slices.Contains(named, flag) && !slices.Contains(global, flag) {
				t.Errorf("`flow %s` offers %s, which %s does not name", command, flag, cliSpec)
			}
		}
		for _, flag := range named {
			if !slices.Contains(offered, flag) {
				t.Errorf("%s gives `flow %s` the flag %s, which it does not have", cliSpec, command, flag)
			}
		}
	}
}

// otherInvocation matches a whole command written in code in a spec, which is
// an example of running something rather than a claim about the flags of the
// section it sits in.
var otherInvocation = regexp.MustCompile("`flow [^`]*`")

// specPath matches a path to a file in this repository as a spec writes one:
// in backticks, under one of the directories flow's layout has, and with a
// file extension. A name with no extension is as likely to be a Go symbol
// (`internal/git.Trailers`) as a directory.
var specPath = regexp.MustCompile("`((?:internal|assets|docs|skills|bootstrap|dogfood|\\.ralph|\\.github)/[A-Za-z0-9_./-]+\\.[a-z]+)`")

// TestEveryFileTheSpecsNameExists walks every spec for the files it points a
// reader at. A path that moved and a paragraph that did not is the cheapest
// of these defects to make and the hardest to notice.
//
// Decisions are history — they record what was deleted and why — so only the
// prose above them is read.
func TestEveryFileTheSpecsNameExists(t *testing.T) {
	specs, err := filepath.Glob(filepath.Join(specDir, "*.md"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("found no specs to read: %v", err)
	}
	checked := 0
	for _, spec := range specs {
		text, _, _ := strings.Cut(readSpec(t, spec), "\n## Decisions")
		for _, match := range specPath.FindAllStringSubmatch(text, -1) {
			path := match[1]
			if strings.ContainsAny(path, "<>*") {
				continue
			}
			checked++
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s points at %s, which is not there", spec, path)
			}
		}
	}
	if checked == 0 {
		t.Error("the specs point at no file; this test is not testing anything")
	}
}

// fuzzTarget matches a fuzz test's declaration.
var fuzzTarget = regexp.MustCompile(`(?m)^func (Fuzz\w+)\(`)

// TestTheTestingSpecNamesEveryFuzzTarget reads spec 14's own list against the
// tree. A fuzz target is the one kind of test that runs for as long as
// somebody gives it, so which ones exist is a decision the spec records —
// and it had `FuzzConfigDecode`, which has never existed.
func TestTheTestingSpecNamesEveryFuzzTarget(t *testing.T) {
	var found []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != "." {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, match := range fuzzTarget.FindAllStringSubmatch(readSpec(t, path), -1) {
			if !slices.Contains(found, match[1]) {
				found = append(found, match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no fuzz target; this test is not testing anything")
	}

	spec := readSpec(t, testingSpec)
	spec, _, _ = strings.Cut(spec, "\n## Decisions")
	named := regexp.MustCompile("`(Fuzz\\w+)`").FindAllStringSubmatch(spec, -1)
	var names []string
	for _, match := range named {
		names = append(names, match[1])
	}
	for _, target := range found {
		if !slices.Contains(names, target) {
			t.Errorf("%s does not name %s, which the tree has", testingSpec, target)
		}
	}
	for _, name := range names {
		if !slices.Contains(found, name) {
			t.Errorf("%s names %s, which the tree does not have", testingSpec, name)
		}
	}
}

// TestTheConfigurationSpecShowsEveryKey holds spec 03 to being the
// configuration's documentation: its worked example is one flow accepts, and
// every key the schema has is named somewhere in it. A key flow decodes and no
// spec mentions is a surface that arrived without anybody agreeing to it —
// `workflow.blocked.require_comment` (issue #145) seen before it is read by
// nobody.
func TestTheConfigurationSpecShowsEveryKey(t *testing.T) {
	spec := readSpec(t, configSpec)
	_, rest, ok := strings.Cut(spec, "```yaml\n")
	if !ok {
		t.Fatalf("%s prints no configuration", configSpec)
	}
	example, _, _ := strings.Cut(rest, "```")

	cfg, err := config.Parse([]byte(example), configSpec)
	if err != nil {
		t.Fatalf("the configuration %s prints does not parse: %v", configSpec, err)
	}
	registry, err := core.LoadRegistry(os.DirFS("assets/capabilities"))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if err := config.Validate(cfg, configSpec, registry); err != nil {
		t.Fatalf("the configuration %s prints does not hang together: %v", configSpec, err)
	}

	for _, key := range schemaLeaves(readConfigSchema(t), "") {
		leaf := key
		if _, after, ok := strings.Cut(key, "."); ok {
			leaf = after[strings.LastIndex(after, ".")+1:]
		}
		if !strings.Contains(spec, leaf) {
			t.Errorf("%s never names %s, which flow reads", configSpec, key)
		}
	}
}

// schemaLeaves is every dotted key the configuration schema has.
func schemaLeaves(node map[string]any, prefix string) []string {
	properties, ok := node["properties"].(map[string]any)
	if !ok {
		return nil
	}
	var keys []string
	for name, raw := range properties {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		keys = append(keys, path)
		keys = append(keys, schemaLeaves(child, path)...)
		if items, ok := child["items"].(map[string]any); ok {
			keys = append(keys, schemaLeaves(items, path+"[]")...)
		}
	}
	return keys
}

// TestNoWorkflowRunsOnPullRequestTarget keeps spec 15 §1's first rule about
// CI. `pull_request_target` runs the trunk's workflow with the trunk's
// secrets against a fork's branch, and flow's gate needs none of it: it reads
// metadata, so a `pull_request` event is enough. This is the rule that would
// turn a repository flow configured into a credential anybody can have.
func TestNoWorkflowRunsOnPullRequestTarget(t *testing.T) {
	checked := 0
	for _, dir := range []string{".github/workflows", "dogfood/.github/workflows", "assets/templates/ci"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			checked++
			if strings.Contains(readSpec(t, path), "pull_request_target") {
				t.Errorf("%s runs on pull_request_target, which spec 15 §1 refuses", path)
			}
		}
	}
	if checked == 0 {
		t.Error("read no workflow; this test is not testing anything")
	}
}
