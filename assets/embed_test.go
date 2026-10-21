package assets_test

import (
	"io/fs"
	"slices"
	"testing"

	"github.com/17twenty/flow/assets"
)

// Every directory flow relies on at runtime must actually be in the binary.
func TestEmbeddedDirectories(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"capabilities", "presets", "templates", "schema"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			files, err := assets.List(dir)
			if err != nil {
				t.Fatalf("List(%q): %v", dir, err)
			}
			if len(files) == 0 {
				t.Errorf("no files embedded under %q", dir)
			}
		})
	}
}

// The capability files named by spec 05 and the presets named by spec 03 §7
// must all be present, so a missing provider fails the build rather than a user.
func TestExpectedAssetsPresent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		want []string
	}{
		{"capabilities", []string{
			"deploys.yaml", "gitea.yaml", "github-projects.yaml", "github.yaml",
			"gitlab.yaml", "jira-cloud.yaml", "linear.yaml",
		}},
		{"presets", []string{"deploy-done.yaml", "lean-kanban.yaml", "minimal.yaml"}},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()
			sub, err := fs.Sub(assets.FS, tc.dir)
			if err != nil {
				t.Fatalf("Sub(%q): %v", tc.dir, err)
			}
			entries, err := fs.ReadDir(sub, ".")
			if err != nil {
				t.Fatalf("ReadDir(%q): %v", tc.dir, err)
			}
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			for _, want := range tc.want {
				if !slices.Contains(got, want) {
					t.Errorf("%s/%s not embedded (have %v)", tc.dir, want, got)
				}
			}
		})
	}
}

// The Sub accessors must open the same content as the raw FS.
func TestAccessors(t *testing.T) {
	t.Parallel()
	accessors := map[string]func() (fs.FS, error){
		"capabilities": assets.Capabilities,
		"presets":      assets.Presets,
		"templates":    func() (fs.FS, error) { return assets.Templates("dev") },
		"schema":       assets.Schema,
	}
	for dir, accessor := range accessors {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			sub, err := accessor()
			if err != nil {
				t.Fatalf("accessor for %q: %v", dir, err)
			}
			if _, err := fs.ReadDir(sub, "."); err != nil {
				t.Errorf("ReadDir on %q sub-FS: %v", dir, err)
			}
		})
	}
}

// Reference material must stay out of the binary.
func TestReferenceMaterialNotEmbedded(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"docs", "research", "examples", "testdata"} {
		if _, err := fs.Stat(assets.FS, dir); err == nil {
			t.Errorf("%q is embedded but should be reference-only", dir)
		}
	}
}
