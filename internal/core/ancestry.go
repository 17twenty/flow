package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AncestryDir is the directory, under flow's cache directory, that holds
// remembered comparisons.
const AncestryDir = "ancestry"

// The lengths an object name may have: git's shortest useful abbreviation,
// and a SHA-256 name spelled out in full.
const (
	minObjectName = 7
	maxObjectName = 64
)

// Comparer is the one question an ancestry cache asks of a forge: whether one
// commit contains another. The deployment checks take this rather than the
// whole of [Forge], because proving a merge reached an environment needs
// nothing else (spec 13 §3).
type Comparer interface {
	Compare(ctx context.Context, base, head string) (Comparison, error)
}

// Ancestry is a [Comparer] that asks a provider once. The ancestry of two
// commits is settled for all time — a commit's parents never change — so the
// answer about two object names is kept on disk and reused by later runs,
// which is what stops a daily `flow doctor` spending a request per ticket per
// day proving again what it proved yesterday (spec 13 §3).
//
// Only object names are remembered. A branch or a tag is a name for whatever
// it points at today, so a comparison naming one is asked every time.
type Ancestry struct {
	from Comparer
	dir  string

	mu  sync.Mutex
	mem map[string]Comparison
}

// NewAncestry returns a cache in front of from, keeping answers in dir between
// runs. An empty dir keeps them for this process only, which is what a test
// and a one-shot command want.
func NewAncestry(from Comparer, dir string) (*Ancestry, error) {
	if from == nil {
		return nil, Errorf(ErrValidation, "remember ancestry answers").
			WithWhy("there is nothing to ask them of")
	}
	if dir != "" {
		if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
			return nil, Errorf(ErrUnavailable, "create the ancestry cache at %s", dir).
				WithWhy("%s", err).
				WithFix("set %s to a directory flow may write", CacheDirEnv)
		}
		// MkdirAll leaves an existing directory's mode alone, and umask
		// can loosen a new one, so say it again.
		if err := os.Chmod(dir, cacheDirPerm); err != nil {
			return nil, Errorf(ErrUnavailable, "secure the ancestry cache at %s", dir).
				WithWhy("%s", err)
		}
	}
	return &Ancestry{from: from, dir: dir, mem: map[string]Comparison{}}, nil
}

// Compare reports the ancestry between two commits, asking the provider only
// for a pair it has no answer for. A refusal is never remembered: the next run
// asks again.
func (a *Ancestry) Compare(ctx context.Context, base, head string) (Comparison, error) {
	key, settled := ancestryKey(base, head)
	if settled {
		if remembered, ok := a.load(key); ok {
			// The key is lower-cased, so hand back the names the
			// caller used rather than the ones it was filed under.
			remembered.Base, remembered.Head = base, head
			return remembered, nil
		}
	}
	comparison, err := a.from.Compare(ctx, base, head)
	if err != nil {
		return Comparison{}, err
	}
	if settled && comparison.Known() {
		a.store(key, comparison)
	}
	return comparison, nil
}

// load returns a remembered comparison, from this process if it has one and
// from the last run otherwise. A missing, unreadable or corrupt file is a
// miss, never an error: the question is simply asked again.
func (a *Ancestry) load(key string) (Comparison, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if remembered, ok := a.mem[key]; ok {
		return remembered, true
	}
	if a.dir == "" {
		return Comparison{}, false
	}
	raw, err := os.ReadFile(a.path(key))
	if err != nil {
		return Comparison{}, false
	}
	var remembered Comparison
	if err := json.Unmarshal(raw, &remembered); err != nil || !remembered.Known() {
		return Comparison{}, false
	}
	a.mem[key] = remembered
	return remembered, true
}

// store remembers a comparison. A cache that cannot write is not a failure the
// caller has to handle — the answer is still correct, it will just be asked
// for again — so the attempt is made and the outcome dropped.
func (a *Ancestry) store(key string, comparison Comparison) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mem[key] = comparison
	if a.dir == "" {
		return
	}
	raw, err := json.Marshal(comparison)
	if err != nil {
		return
	}
	_ = writeAtomic(a.dir, a.path(key), "a remembered comparison", raw)
}

// path is the file one key is stored in. Both halves of a key are object
// names, so the key is hexadecimal and dots: it is spelled out rather than
// hashed, because it names nothing private and a cache a human can read is a
// cache a human can clear.
func (a *Ancestry) path(key string) string {
	return filepath.Join(a.dir, key+".json")
}

// ancestryKey is the key two commits are remembered under, and whether they
// may be remembered at all.
func ancestryKey(base, head string) (string, bool) {
	if !objectName(base) || !objectName(head) {
		return "", false
	}
	return strings.ToLower(base) + "..." + strings.ToLower(head), true
}

// objectName reports whether ref is spelled the way git spells a commit: long
// enough to be an abbreviation rather than a word, and hexadecimal. A name
// that passes is one no later commit can take over.
func objectName(ref string) bool {
	if len(ref) < minObjectName || len(ref) > maxObjectName {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
