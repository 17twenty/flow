package config

import (
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/17twenty/flow/internal/core"
)

// PresetFile is the file one shipped preset lives in, relative to the embedded
// presets directory.
func PresetFile(name string) string { return name + ".yaml" }

// PresetSource returns a preset exactly as it ships, comments and all. This is
// what `flow init` writes: a preset is a starting point a team reads and
// edits, so the explanation beside each key has to survive being copied
// (spec 03 §7).
//
// fsys is the embedded presets directory, passed in by the root so that this
// package keeps its stdlib-and-core diet (spec 01 §2).
func PresetSource(fsys fs.FS, name string) ([]byte, error) {
	if !slices.Contains(PresetNames, name) {
		return nil, core.Errorf(core.ErrNotFound, "use the %q preset", name).
			WithWhy("flow ships no preset by that name").
			WithFix("use one of: %s", strings.Join(PresetNames, ", "))
	}
	data, err := fs.ReadFile(fsys, PresetFile(name))
	if err != nil {
		return nil, core.Errorf(core.ErrNotFound, "read the %q preset", name).
			WithWhy("%s", err).
			WithFix("this is a build problem: the preset should be embedded in the binary")
	}
	return data, nil
}

// LoadPreset reads and decodes one shipped preset. The result still holds its
// placeholders: a preset is not a usable configuration until a repository and
// a board are filled in (spec 03 §2).
func LoadPreset(fsys fs.FS, name string) (*Config, error) {
	data, err := PresetSource(fsys, name)
	if err != nil {
		return nil, err
	}
	return Parse(data, path.Join("presets", PresetFile(name)))
}
