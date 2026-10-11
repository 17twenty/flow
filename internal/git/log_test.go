package git_test

import (
	"errors"
	"testing"
	"time"

	"github.com/17twenty/flow/internal/core"
)

func TestLogReadsCommits(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): add --json\n\nThe body explains why.\n\nRefs: #12\n")
	f.commit("fix(core): stop the leak")
	repo := open(t, f)

	commits, err := repo.Log(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}

	// Newest first, as git reports them.
	if commits[0].Subject != "fix(core): stop the leak" {
		t.Errorf("first subject = %q", commits[0].Subject)
	}

	oldest := commits[1]
	if oldest.Subject != "feat(cli): add --json" {
		t.Errorf("subject = %q", oldest.Subject)
	}
	if oldest.Message != "feat(cli): add --json\n\nThe body explains why.\n\nRefs: #12" {
		t.Errorf("message = %q", oldest.Message)
	}
	if oldest.Author.Name != "Ada Lovelace" || oldest.Author.Email != "ada@example.com" {
		t.Errorf("author = %+v", oldest.Author)
	}
	if oldest.Committer.Name != "Flow Bot" || oldest.Committer.Email != "bot@example.com" {
		t.Errorf("committer = %+v", oldest.Committer)
	}
	if want := time.Date(2024, 5, 1, 10, 1, 0, 0, time.UTC); !oldest.Author.When.Equal(want) {
		t.Errorf("authored at %s, want %s", oldest.Author.When, want)
	}
	if len(oldest.Hash) != 40 || oldest.Short() != oldest.Hash[:7] {
		t.Errorf("hash = %q, short = %q", oldest.Hash, oldest.Short())
	}
	if len(oldest.Parents) != 0 || oldest.Merge() {
		t.Errorf("the root commit has parents %v", oldest.Parents)
	}
}

func TestLogRange(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): base")
	f.git("checkout", "--quiet", "-b", "work")
	f.commit("feat(cli): one")
	f.commit("feat(cli): two")
	repo := open(t, f)

	commits, err := repo.Log(ctx(t), "main..work")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want the 2 that are not on main", len(commits))
	}
	for _, c := range commits {
		if c.Subject == "feat(cli): base" {
			t.Error("the range included a commit that is already on main")
		}
	}
}

// A merge commit is recognisable, because the commit convention does not apply
// to messages git wrote itself (spec 08 §2).
func TestLogMerge(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): base")
	f.git("checkout", "--quiet", "-b", "side")
	f.commit("feat(cli): side")
	f.git("checkout", "--quiet", "main")
	f.commit("feat(cli): main")
	f.git("-c", "user.name=Flow Bot", "-c", "user.email=bot@example.com",
		"merge", "--quiet", "--no-ff", "-m", "Merge branch 'side'", "side")
	repo := open(t, f)

	commits, err := repo.Log(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	merge := commits[0]
	if !merge.Merge() || len(merge.Parents) != 2 {
		t.Fatalf("the merge has %d parents: %+v", len(merge.Parents), merge)
	}
	if merge.Parents[0] == merge.Parents[1] {
		t.Error("both parents are the same commit")
	}
}

// A message containing the separators flow uses to read git's output must come
// back intact, or a commit could rewrite the record of the one before it.
func TestLogMessageWithSeparators(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): odd\n\nA unit separator: \x1f and a newline.\n\nBODY-END\n")
	repo := open(t, f)

	commits, err := repo.Log(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("got %d commits, want 1", len(commits))
	}
	if want := "feat(cli): odd\n\nA unit separator: \x1f and a newline.\n\nBODY-END"; commits[0].Message != want {
		t.Errorf("message = %q, want %q", commits[0].Message, want)
	}
}

// Having just been created is not a failure.
func TestLogEmptyRepository(t *testing.T) {
	f := newFixture(t)
	repo := open(t, f)

	commits, err := repo.Log(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("Log on an empty repository: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("got %d commits from an empty repository", len(commits))
	}
}

// Log takes revisions. An option smuggled in as one would let a caller change
// what the command does.
func TestLogRefusesOptions(t *testing.T) {
	f := newFixture(t)
	f.commit("feat(cli): one")
	repo := open(t, f)

	for _, rev := range []string{"--all", "-n1", "--output=/tmp/x"} {
		t.Run(rev, func(t *testing.T) {
			_, err := repo.Log(ctx(t), rev)
			if err == nil {
				t.Fatalf("Log(%q) was accepted", rev)
			}
			if !errors.Is(err, core.ErrValidation) {
				t.Errorf("error is %v, want it to wrap ErrValidation", err)
			}
		})
	}
}
