package assets

import (
	"bytes"
	"io/fs"
	"regexp"
	"testing/fstest"
)

// versionPin is the line the shipped workflow carries, and the only thing in
// the template tree that depends on which flow is writing it. The shipped
// bytes name the version spec 16 §2 guarantees always resolves, so a template
// read straight out of the binary is already a workflow that runs.
const versionPin = "FLOW_VERSION: latest"

// releaseVersion matches a version a release could have been tagged with:
// canonical semver, a pre-release such as v2.0.0-rc.1 allowed, and no build
// metadata — Go appends `+dirty` to a build from a modified tree, and no tag
// carries it.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// pseudoVersion is the tail Go gives a build from a commit no tag names: a
// fourteen-digit timestamp and a twelve-character commit (issue #267). It
// looks like a pre-release, and nothing was ever released under it.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

// Latest is what a workflow installs when the build writing it is not a
// release: the version spec 16 §2 guarantees always resolves.
const Latest = "latest"

// Pin is the version a workflow written by this build installs. The
// templates download a released binary rather than compiling one, so only a
// tagged release can be pinned: a build from a branch, an unreleased "dev"
// or a modified tree pins to [Latest].
func Pin(version string) string {
	if !releaseVersion.MatchString(version) || pseudoVersion.MatchString(version) {
		return Latest
	}
	return version
}

// pinned returns the template tree with the workflow's version naming the
// build that asked for it. A build with no release behind it has nothing to
// pin to and leaves the shipped line alone, so what flow writes is always a
// version that exists.
//
// The whole tree is rewritten rather than the one file, so that every way of
// reading a template — flow init copying it into the working tree, and the
// forge.files proposal that flow apply writes when it is missing — is reading
// the same bytes. Two readings of one file are two things to keep in step.
func pinned(tree fs.FS, version string) (fs.FS, error) {
	pin := Pin(version)
	if pin == Latest {
		return tree, nil
	}
	want := []byte("FLOW_VERSION: " + pin)
	out := fstest.MapFS{}
	err := fs.WalkDir(tree, ".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		}
		content, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		out[path] = &fstest.MapFile{Data: bytes.ReplaceAll(content, []byte(versionPin), want), Mode: 0o644}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
