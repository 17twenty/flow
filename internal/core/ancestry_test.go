package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// countingComparer answers from a table and counts what it was asked, so that
// a test can tell a remembered answer from a fresh one.
type countingComparer struct {
	mu      sync.Mutex
	asked   []string
	answer  Comparison
	failing error
}

func (c *countingComparer) Compare(_ context.Context, base, head string) (Comparison, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, base+"..."+head)
	if c.failing != nil {
		return Comparison{}, c.failing
	}
	answer := c.answer
	answer.Base, answer.Head = base, head
	return answer, nil
}

func (c *countingComparer) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.asked)
}

// Two object names and a branch, as the deployment checks compare them.
const (
	ancestorSHA = "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1"
	descendSHA  = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
)

func newTestAncestry(t *testing.T, from Comparer, dir string) *Ancestry {
	t.Helper()
	ancestry, err := NewAncestry(from, dir)
	if err != nil {
		t.Fatalf("NewAncestry: %v", err)
	}
	return ancestry
}

func TestAncestryAsksOnceForOnePair(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: CompareAhead, AheadBy: 2}}
	ancestry := newTestAncestry(t, from, t.TempDir())

	first, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	second, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA)
	if err != nil {
		t.Fatalf("Compare again: %v", err)
	}
	if first != second {
		t.Errorf("the remembered answer is %+v, want the one that was given: %+v", second, first)
	}
	if !second.Contains() {
		t.Error("the remembered answer lost that the head contains the base")
	}
	if from.calls() != 1 {
		t.Errorf("the provider was asked %d times about one pair of commits, want 1", from.calls())
	}
}

func TestAncestryOutlivesTheRun(t *testing.T) {
	dir := t.TempDir()
	first := &countingComparer{answer: Comparison{Status: CompareIdentical}}
	if _, err := newTestAncestry(t, first, dir).Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}

	next := &countingComparer{failing: errors.New("the next run must not ask")}
	answer, err := newTestAncestry(t, next, dir).Compare(t.Context(), ancestorSHA, descendSHA)
	if err != nil {
		t.Fatalf("Compare in a later run: %v", err)
	}
	if answer.Status != CompareIdentical {
		t.Errorf("the later run read %q, want %q", answer.Status, CompareIdentical)
	}
	if next.calls() != 0 {
		t.Error("the later run asked the provider again; the answer about two commits never changes")
	}
}

func TestAncestryKeepsAnswersToItself(t *testing.T) {
	dir := t.TempDir()
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	if _, err := newTestAncestry(t, from, dir).Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != cacheDirPerm.Perm() {
		t.Errorf("the cache directory is %v, want %v", info.Mode().Perm(), cacheDirPerm.Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the cache holds %d files, want 1 per pair", len(entries))
	}
	if name := entries[0].Name(); name != strings.ToLower(ancestorSHA)+"..."+strings.ToLower(descendSHA)+".json" {
		t.Errorf("the pair is filed as %q, want it named after the two commits", name)
	}
	file, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if file.Mode().Perm() != cacheFilePerm.Perm() {
		t.Errorf("a remembered comparison is %v, want %v", file.Mode().Perm(), cacheFilePerm.Perm())
	}
}

func TestAncestryNeverRemembersAMovingRef(t *testing.T) {
	refs := []struct {
		name string
		base string
		head string
	}{
		{"a branch", "main", descendSHA},
		{"a tag", ancestorSHA, "v1.2.0"},
		{"a word that is not hexadecimal", "release", descendSHA},
		{"an abbreviation too short to be a commit", "c1c1c1", descendSHA},
		{"nothing at all", "", descendSHA},
	}
	for _, tc := range refs {
		t.Run(tc.name, func(t *testing.T) {
			from := &countingComparer{answer: Comparison{Status: CompareAhead}}
			ancestry := newTestAncestry(t, from, t.TempDir())
			for range 2 {
				if _, err := ancestry.Compare(t.Context(), tc.base, tc.head); err != nil {
					t.Fatalf("Compare: %v", err)
				}
			}
			if from.calls() != 2 {
				t.Errorf("the provider was asked %d times, want 2: %s names whatever it points at today", from.calls(), tc.name)
			}
		})
	}
}

func TestAncestryKeepsThePairsApart(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	ancestry := newTestAncestry(t, from, t.TempDir())
	if _, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}
	reversed, err := ancestry.Compare(t.Context(), descendSHA, ancestorSHA)
	if err != nil {
		t.Fatalf("Compare the other way round: %v", err)
	}
	if from.calls() != 2 {
		t.Fatal("the reversed pair was answered from the first one; ancestry is not symmetric")
	}
	if reversed.Base != descendSHA || reversed.Head != ancestorSHA {
		t.Errorf("the reversed answer is about %s...%s", reversed.Base, reversed.Head)
	}
}

func TestAncestryAnswersInTheCallersSpelling(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	// No directory: a case-insensitive file system would answer the
	// second question from the first one's file whatever the key said,
	// and the key is what is under test.
	ancestry := newTestAncestry(t, from, "")
	upper := strings.ToUpper(ancestorSHA)
	if _, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}
	answer, err := ancestry.Compare(t.Context(), upper, descendSHA)
	if err != nil {
		t.Fatalf("Compare in upper case: %v", err)
	}
	if from.calls() != 1 {
		t.Error("the same commit spelled in upper case was asked about again")
	}
	if answer.Base != upper {
		t.Errorf("the answer is about %q, want the name the caller used, %q", answer.Base, upper)
	}
}

func TestAncestryDoesNotRememberWhatItCouldNotAnswer(t *testing.T) {
	refused := errors.New("the provider refused")
	from := &countingComparer{failing: refused}
	ancestry := newTestAncestry(t, from, t.TempDir())
	if _, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA); !errors.Is(err, refused) {
		t.Fatalf("Compare = %v, want the refusal", err)
	}
	from.mu.Lock()
	from.failing = nil
	from.answer = Comparison{Status: CompareAhead}
	from.mu.Unlock()

	answer, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA)
	if err != nil {
		t.Fatalf("Compare after the provider recovered: %v", err)
	}
	if answer.Status != CompareAhead {
		t.Errorf("Compare = %q, want the provider asked again rather than a remembered refusal", answer.Status)
	}
}

func TestAncestryDoesNotRememberAnAncestryItCannotRead(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: "sideways"}}
	dir := t.TempDir()
	if _, err := newTestAncestry(t, from, dir).Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("an ancestry flow does not understand was written to the cache: %v", entries)
	}
}

func TestAncestryTreatsACorruptFileAsAMiss(t *testing.T) {
	dir := t.TempDir()
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	ancestry := newTestAncestry(t, from, dir)
	if _, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
		t.Fatalf("Compare: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("{not json"), cacheFilePerm); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	next := &countingComparer{answer: Comparison{Status: CompareBehind}}
	answer, err := newTestAncestry(t, next, dir).Compare(t.Context(), ancestorSHA, descendSHA)
	if err != nil {
		t.Fatalf("Compare with a corrupt cache: %v", err)
	}
	if answer.Status != CompareBehind {
		t.Errorf("Compare = %q, want the question asked again", answer.Status)
	}
}

func TestAncestryWithoutADirectoryRemembersOnlyThisProcess(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	ancestry := newTestAncestry(t, from, "")
	for range 3 {
		if _, err := ancestry.Compare(t.Context(), ancestorSHA, descendSHA); err != nil {
			t.Fatalf("Compare: %v", err)
		}
	}
	if from.calls() != 1 {
		t.Errorf("the provider was asked %d times, want 1", from.calls())
	}
}

func TestAncestryIsUsableFromSeveralGoroutines(t *testing.T) {
	from := &countingComparer{answer: Comparison{Status: CompareAhead}}
	ancestry := newTestAncestry(t, from, t.TempDir())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := strings.Repeat("ab", 19) + "c" + string(rune('0'+i))
			if _, err := ancestry.Compare(t.Context(), base, descendSHA); err != nil {
				t.Errorf("Compare: %v", err)
			}
		}()
	}
	wg.Wait()
	if from.calls() != 8 {
		t.Errorf("the provider was asked %d times about 8 pairs", from.calls())
	}
}

func TestNewAncestryRefusesWhatItCannotCache(t *testing.T) {
	if _, err := NewAncestry(nil, t.TempDir()); !errors.Is(err, ErrValidation) {
		t.Errorf("NewAncestry without a provider = %v, want ErrValidation", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), cacheFilePerm); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewAncestry(&countingComparer{}, file); !errors.Is(err, ErrUnavailable) {
		t.Errorf("NewAncestry in a file = %v, want ErrUnavailable", err)
	}
}
