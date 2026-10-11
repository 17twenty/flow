// Package assets holds the files flow ships inside its binary: provider
// capability declarations, configuration presets, file templates and the JSON
// schema. Reference material under docs/, research/, examples/ and testdata/ is
// deliberately not embedded.
package assets

import (
	"embed"
	"io/fs"
)

// FS holds every embedded asset. Paths are rooted at the assets directory, so a
// capability file is read as "capabilities/github.yaml".
//
//go:embed capabilities presets templates schema
var FS embed.FS

// Capabilities returns the provider capability declarations (spec 05).
func Capabilities() (fs.FS, error) { return fs.Sub(FS, "capabilities") }

// Presets returns the configuration presets (spec 03 §7).
func Presets() (fs.FS, error) { return fs.Sub(FS, "presets") }

// Templates returns the file templates written by flow init (spec 02 §2).
func Templates() (fs.FS, error) { return fs.Sub(FS, "templates") }

// Schema returns the generated JSON Schema documents (spec 03 §6).
func Schema() (fs.FS, error) { return fs.Sub(FS, "schema") }

// List returns every embedded file path under dir, sorted, so that callers and
// tests can enumerate assets without walking the embed.FS themselves.
func List(dir string) ([]string, error) {
	var out []string
	err := fs.WalkDir(FS, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
