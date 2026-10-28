package assets_test

import (
	"io/fs"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

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

// The version a workflow installs must be one that was released: the templates
// download a release's binary, and a version with no release behind it is a
// workflow that fails at its first step (issue #267). A build that names a
// release pins to it; anything else falls back to the version spec 16 §2
// guarantees always resolves.
func TestTheWorkflowInstallsAVersionThatExists(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "a release", version: "v1.4.0", want: "v1.4.0"},
		{name: "a release candidate", version: "v2.0.0-rc.1", want: "v2.0.0-rc.1"},
		{name: "a build from a branch", version: "v0.0.0-20260101120000-abcdef123456", want: "latest"},
		{name: "a build from a commit after a release", version: "v0.96.3-0.20261027223121-82a2b6f93b4f", want: "latest"},
		{name: "a build from a commit after a pre-release", version: "v2.0.0-rc.1.0.20261027223121-82a2b6f93b4f", want: "latest"},
		{name: "a release built from a modified tree", version: "v1.4.0+dirty", want: "latest"},
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
		// Every CI template carries the pin, one per forge (spec 20 §7).
		ci := strings.HasPrefix(name, "ci/")
		switch {
		case ci && string(before) == string(after):
			t.Errorf("%s was not pinned", name)
		case ci:
		case string(before) != string(after):
			t.Errorf("%s changed because of the version", name)
		}
	}
}

// TestEveryInstallStepIsShell: the step that fetches the released binary is
// shell held inside YAML, where a stray quote is invisible until a runner
// tries it. Every CI template's copy is parsed by sh itself.
func TestEveryInstallStepIsShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	tree, err := assets.Templates("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ci/github-flow.yml", "ci/gitea-flow.yml", "ci/gitlab-flow.yml"} {
		raw, err := fs.ReadFile(tree, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not YAML: %v", name, err)
		}
		script := installScript(doc)
		if !strings.Contains(script, "SHA256SUMS") {
			t.Fatalf("%s has no install step that checks the release's sums", name)
		}
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: the install step is not valid shell: %v\n%s", name, err, out)
		}
	}
}

// installScript finds the install step in either shape: GitHub's workflow
// keeps it in env.FLOW_INSTALL, GitLab's in the hidden job's before_script.
func installScript(doc map[string]any) string {
	if env, ok := doc["env"].(map[string]any); ok {
		if script, ok := env["FLOW_INSTALL"].(string); ok {
			return script
		}
	}
	if job, ok := doc[".flow"].(map[string]any); ok {
		if steps, ok := job["before_script"].([]any); ok {
			for _, step := range steps {
				if script, ok := step.(string); ok && strings.Contains(script, "SHA256SUMS") {
					return script
				}
			}
		}
	}
	return ""
}
