package assets_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/17twenty/flow/assets"
)

// workflowTemplate is the file whose version pin decides whether a repository
// flow has just set up can run its own gate.
const workflowTemplate = "ci/github-flow.yml"

// flowVersionOf returns the value of the workflow's FLOW_VERSION, which is the
// version its jobs install flow at.
func flowVersionOf(t *testing.T, tree fs.FS) string {
	t.Helper()
	content, err := fs.ReadFile(tree, workflowTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", workflowTemplate, err)
	}
	for line := range strings.Lines(string(content)) {
		name, value, found := strings.Cut(strings.TrimSpace(line), "FLOW_VERSION:")
		if !found || name != "" {
			continue
		}
		value, _, _ = strings.Cut(value, "#")
		return strings.TrimSpace(value)
	}
	t.Fatalf("%s names no FLOW_VERSION", workflowTemplate)
	return ""
}

// The version a workflow installs must be one the module proxy can resolve. A
// build that names a release pins to it; a build with no release behind it
// falls back to the version spec 16 §2 guarantees always resolves. Neither is
// ever a version nobody tagged.
func TestTheWorkflowInstallsAVersionThatExists(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "a release", version: "v1.4.0", want: "v1.4.0"},
		{name: "a release candidate", version: "v2.0.0-rc.1", want: "v2.0.0-rc.1"},
		{name: "a build from a branch", version: "v0.0.0-20260101120000-abcdef123456", want: "v0.0.0-20260101120000-abcdef123456"},
		{name: "an unreleased build", version: "dev", want: "latest"},
		{name: "a build that says nothing", version: "", want: "latest"},
		{name: "a name somebody made up", version: "nightly", want: "latest"},
		{name: "a tag go install cannot resolve", version: "v1", want: "latest"},
		{name: "a version nobody tagged it with", version: "1.4.0", want: "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree, err := assets.Templates(tc.version)
			if err != nil {
				t.Fatalf("templates: %v", err)
			}
			if got := flowVersionOf(t, tree); got != tc.want {
				t.Errorf("FLOW_VERSION for build %q = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

// Pinning the workflow must not rewrite anything else: the rest of the tree is
// scaffolding that has nothing to do with which flow is writing it.
func TestPinningTheWorkflowLeavesEveryOtherTemplateAlone(t *testing.T) {
	t.Parallel()
	shipped, err := assets.Templates("dev")
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	pinned, err := assets.Templates("v1.4.0")
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	paths, err := assets.List("templates")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no templates to compare")
	}
	for _, path := range paths {
		name := strings.TrimPrefix(path, "templates/")
		before, err := fs.ReadFile(shipped, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		after, err := fs.ReadFile(pinned, name)
		if err != nil {
			t.Fatalf("read pinned %s: %v", name, err)
		}
		switch {
		case name == workflowTemplate && string(before) == string(after):
			t.Errorf("%s was not pinned", name)
		case name == workflowTemplate:
		case string(before) != string(after):
			t.Errorf("%s changed because of the version", name)
		}
	}
}
