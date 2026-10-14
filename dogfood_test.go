package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// `dogfood/` holds the files a repository adopting flow copies to its root
// (spec 17 §2), and `.github/workflows/flow.yml` is one of them. A copy is a
// claim that it is what this repository runs, and a claim of that kind rots
// silently: the gate in `.github/workflows/` was rewritten for Stage B — it
// builds the binary from the checkout it is judging and pins the toolchain to
// `go.mod` — while the copy went on describing the draft that installed flow
// from `main` on `go-version: stable`, the exact drift that turned this
// repository's CI red. Nothing failed, because nothing compared them.
//
// This test is that comparison. Every file under `dogfood/` must exist at the
// repository root and be byte-for-byte what is there, so that copying the
// directory is the same act as adopting this repository's workflow. A
// difference that is deliberate cannot be left in the file: either it belongs
// in both copies, or it is recorded as a Decision in spec 17 and this test is
// changed on purpose, with the reason, by the commit that makes it true.

// dogfoodDir is the directory of files that are copied to a repository root.
const dogfoodDir = "dogfood"

// TestDogfoodCopiesAreWhatTheRepositoryRuns fails when a file in `dogfood/`
// has drifted from the one it is a copy of, or names a file the repository
// does not have at all.
func TestDogfoodCopiesAreWhatTheRepositoryRuns(t *testing.T) {
	var copies []string
	err := filepath.WalkDir(dogfoodDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		copies = append(copies, path)
		return nil
	})
	if err != nil {
		t.Fatalf("%s is what this test reads: %v", dogfoodDir, err)
	}
	if len(copies) == 0 {
		t.Fatalf("%s holds no files, so nothing is being offered to copy (spec 17 §2)", dogfoodDir)
	}

	for _, copied := range copies {
		relative, err := filepath.Rel(dogfoodDir, copied)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", dogfoodDir, copied, err)
		}
		t.Run(filepath.ToSlash(relative), func(t *testing.T) {
			want, err := os.ReadFile(relative)
			if err != nil {
				t.Fatalf("%s offers %s, but this repository has no such file to copy it from: %v", dogfoodDir, relative, err)
			}
			got, err := os.ReadFile(copied)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", copied, err)
			}
			if string(got) != string(want) {
				t.Errorf("%s has drifted from %s: a repository copying it would not get the workflow this one runs.\n"+
					"Copy %s over it, or — if the difference is deliberate — record it as a Decision in .ralph/specs/17-dogfood.md and change this test with the reason.\n"+
					"first difference at byte %d", copied, relative, relative, firstDifference(string(got), string(want)))
			}
		})
	}
}

// firstDifference is the index of the first byte at which two files disagree,
// so the failure says where to look rather than printing two whole workflows.
func firstDifference(got, want string) int {
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return min(len(got), len(want))
}
