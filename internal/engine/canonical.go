package engine

import (
	"bytes"
	"cmp"
	"maps"
	"slices"
	"strconv"

	"github.com/17twenty/flow/internal/config"
	"github.com/17twenty/flow/internal/core"
)

// Canonical form. A hash is only worth recording if the same state always
// encodes to the same bytes, however the provider happened to order its
// answer, and if no value can be written in a way that reads as something
// else. So: items in a fixed order, fields sorted by name, every value
// quoted, one record per line (spec 06 §2).
//
// The encoding is deliberately legible. A golden file that nobody can read is
// a golden file nobody checks.

// HashObservation returns the canonical hash of what an adapter found, in the
// form the lock records: "sha256:…" (spec 03 §3).
func HashObservation(observation core.Observation) string {
	return config.Hash(CanonicalObservation(observation))
}

// CanonicalObservation returns the canonical encoding of an observation.
func CanonicalObservation(observation core.Observation) []byte {
	observation = observation.Canonical()
	var enc encoder
	enc.line("observation", observation.What)
	enc.line("exists", strconv.FormatBool(observation.Exists))
	enc.items(observation.Items)
	return enc.bytes()
}

// Hash returns the canonical hash of the whole desired state. A plan carries
// it so that applying a stale plan can be refused (spec 06 §2).
func (d Desired) Hash() string { return config.Hash(d.Canonical()) }

// Canonical returns the canonical encoding of the desired state.
func (d Desired) Canonical() []byte {
	var enc encoder
	enc.line("desired")
	for _, item := range d.Items {
		enc.buf.Write(item.Canonical())
	}
	return enc.bytes()
}

// Hash returns the canonical hash of one desired item, which is what a
// per-item staleness or drift check compares.
func (i DesiredItem) Hash() string { return config.Hash(i.Canonical()) }

// Canonical returns the canonical encoding of one desired item.
//
// A file is encoded as its path and the hash of its content, not the content
// itself: the bytes are what apply proposes, and reproducing them here would
// bury the encoding in CI workflows while telling the reader nothing a hash
// does not.
func (i DesiredItem) Canonical() []byte {
	var enc encoder
	enc.line("item", i.ID)
	enc.line("capability", i.Capability)
	enc.line("target", i.Target)
	for _, id := range i.DependsOn {
		enc.line("depends_on", id)
	}
	if i.Resolve {
		enc.line("resolve")
	}
	if i.Additive {
		enc.line("additive")
	}
	enc.items(i.Items)
	files := slices.SortedFunc(slices.Values(i.Files), func(a, b core.ProposedFile) int {
		return cmp.Compare(a.Path, b.Path)
	})
	for _, file := range files {
		enc.line("file", file.Path, config.Hash(file.Content))
	}
	return enc.bytes()
}

// canonicalItems returns items in the fixed order an observation puts them
// in, so that desired and observed are ordered by the same rule.
func canonicalItems(items []core.Item) []core.Item {
	if len(items) == 0 {
		return nil
	}
	return core.Observation{Items: items}.Canonical().Items
}

// encoder writes the canonical form: one record per line, every value quoted.
type encoder struct{ buf bytes.Buffer }

// line writes one record: a tag, then its values, each quoted.
//
// Quoting is what makes the encoding unambiguous. strconv.Quote escapes
// newlines and quotation marks, so no value can close its own record and open
// another: two different states cannot encode to the same bytes.
func (e *encoder) line(tag string, values ...string) {
	e.buf.WriteString(tag)
	for _, value := range values {
		e.buf.WriteByte(' ')
		e.buf.WriteString(strconv.Quote(value))
	}
	e.buf.WriteByte('\n')
}

// items writes items in canonical order, each followed by its fields sorted
// by name.
func (e *encoder) items(items []core.Item) {
	for _, item := range canonicalItems(items) {
		e.line("content", item.ID, item.Name)
		for _, field := range slices.Sorted(maps.Keys(item.Fields)) {
			e.line("field", field, item.Fields[field])
		}
	}
}

func (e *encoder) bytes() []byte { return e.buf.Bytes() }
