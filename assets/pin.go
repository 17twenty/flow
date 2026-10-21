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

// releaseVersion matches a version `go install` can be given: a canonical
// semver tag, or the pseudo-version a build from a branch reports. Anything
// else — an empty string, "dev", a name somebody made up — is a build with no
// release behind it, which is not something a workflow can install.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

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
	if !releaseVersion.MatchString(version) {
		return tree, nil
	}
	want := []byte("FLOW_VERSION: " + version)
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
