package git_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Tracked decides whether a file is the team's or the individual's: a
// .flow.local.toml under version control is a personal override everyone is
// subject to, which flow refuses (spec 03 §4).
func TestTracked(t *testing.T) {
	f := newFixture(t)
	f.write(".flow.local.toml", "[ui]\nascii = true\n")
	f.write("untracked.toml", "")
	f.write("deep/nested/kept.txt", "x")
	f.add(".flow.local.toml", "deep/nested/kept.txt")
	f.commit("chore(repo): add files")
	repo := open(t, f)

	cases := []struct {
		path string
		want bool
	}{
		{".flow.local.toml", true},
		{"deep/nested/kept.txt", true},
		{"deep", true},
		{"untracked.toml", false},
		{"never-existed.toml", false},
		{"deep/nested/absent.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := repo.Tracked(ctx(t), tc.path)
			if err != nil {
				t.Fatalf("Tracked(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("Tracked(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	t.Run("absolute path", func(t *testing.T) {
		got, err := repo.Tracked(ctx(t), filepath.Join(f.dir, ".flow.local.toml"))
		if err != nil {
			t.Fatalf("Tracked: %v", err)
		}
		if !got {
			t.Error("an absolute path to a tracked file reported untracked")
		}
	})

	// A file that was deleted from the working tree is still the team's.
	t.Run("deleted but still tracked", func(t *testing.T) {
		if err := os.Remove(filepath.Join(f.dir, ".flow.local.toml")); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Tracked(ctx(t), ".flow.local.toml")
		if err != nil {
			t.Fatalf("Tracked: %v", err)
		}
		if !got {
			t.Error("deleting the file from the working tree made it untracked")
		}
	})
}

// A path outside the working tree has an answer — no — rather than a failure:
// the user's own config lives in their home directory, and asking about it
// must not break the run.
func TestTrackedOutsideTheRepository(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): one")
	repo := open(t, f)

	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Tracked(ctx(t), outside)
	if err != nil {
		t.Fatalf("Tracked outside the repository: %v", err)
	}
	if got {
		t.Error("a file outside the repository reported as tracked")
	}
}

func TestHooksPath(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)

	path, err := repo.HooksPath(ctx(t))
	if err != nil {
		t.Fatalf("HooksPath: %v", err)
	}
	if want := filepath.Join(f.dir, ".git", "hooks"); path != want {
		t.Errorf("hooks path = %q, want %q", path, want)
	}
	if _, set, err := repo.HooksPathConfigured(ctx(t)); err != nil || set {
		t.Errorf("core.hooksPath reported set=%v, err=%v", set, err)
	}
}

// A repository that has handed its hooks to lefthook or husky must report that
// directory, or flow installs a hook git will never run (spec 02 §2).
func TestHooksPathHonoursConfiguration(t *testing.T) {
	f := newFixture(t)
	f.git("config", "core.hooksPath", ".lefthook")
	repo := open(t, f)

	path, err := repo.HooksPath(ctx(t))
	if err != nil {
		t.Fatalf("HooksPath: %v", err)
	}
	if want := filepath.Join(f.dir, ".lefthook"); path != want {
		t.Errorf("hooks path = %q, want %q", path, want)
	}

	value, set, err := repo.HooksPathConfigured(ctx(t))
	if err != nil {
		t.Fatalf("HooksPathConfigured: %v", err)
	}
	if !set || value != ".lefthook" {
		t.Errorf("core.hooksPath = %q, %v; want \".lefthook\", true", value, set)
	}
}

func TestHooksPathAbsoluteConfiguration(t *testing.T) {
	f := newFixture(t)
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.git("config", "core.hooksPath", elsewhere)
	repo := open(t, f)

	path, err := repo.HooksPath(ctx(t))
	if err != nil {
		t.Fatalf("HooksPath: %v", err)
	}
	if path != elsewhere {
		t.Errorf("hooks path = %q, want %q", path, elsewhere)
	}
}
