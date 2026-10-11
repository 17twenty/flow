package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/17twenty/flow/internal/core"
)

// VersionCmd prints version and build information (spec 02 §2).
type VersionCmd struct{}

// versionDoc is the --json payload for flow version.
type versionDoc struct {
	// Schema identifies the document kind and version.
	Schema string `json:"schema"`
	// Version is the release tag, or "dev" for an unreleased build.
	Version string `json:"version"`
	// Commit is the VCS revision the binary was built from, if known.
	Commit string `json:"commit,omitempty"`
	// Built is the commit timestamp, if known.
	Built string `json:"built,omitempty"`
	// Modified reports whether the working tree was dirty at build time.
	Modified bool `json:"modified"`
	// Module is the Go module path the binary was built from.
	Module string `json:"module,omitempty"`
	// Go is the toolchain version, for example "go1.26.4".
	Go string `json:"go"`
	// OS is the target operating system.
	OS string `json:"os"`
	// Arch is the target architecture.
	Arch string `json:"arch"`
}

// devVersion is the version reported when no release tag was injected.
const devVersion = "dev"

// buildDetails derives the version document from the ldflags-injected version
// and the embedded build info. The injected version always wins; otherwise the
// module version is used, and failing that the build is reported as "dev".
func buildDetails(injected string, info *debug.BuildInfo, ok bool) versionDoc {
	doc := versionDoc{
		Schema:  core.SchemaID("version"),
		Version: injected,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	if doc.Version == "" {
		doc.Version = devVersion
	}
	if !ok || info == nil {
		return doc
	}

	doc.Module = info.Main.Path
	doc.Go = info.GoVersion
	if doc.Version == devVersion && info.Main.Version != "" && info.Main.Version != "(devel)" {
		doc.Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			doc.Commit = s.Value
		case "vcs.time":
			doc.Built = s.Value
		case "vcs.modified":
			doc.Modified = s.Value == "true"
		case "GOOS":
			doc.OS = s.Value
		case "GOARCH":
			doc.Arch = s.Value
		}
	}
	return doc
}

// Run implements the version command.
func (c *VersionCmd) Run(app *Context) error {
	info, ok := debug.ReadBuildInfo()
	doc := buildDetails(version, info, ok)

	if app.CLI.JSON {
		return core.WriteJSON(app.Stdout, doc)
	}

	fmt.Fprintf(app.Stdout, "flow %s\n", doc.Version)
	if doc.Commit != "" {
		dirty := ""
		if doc.Modified {
			dirty = " (modified)"
		}
		fmt.Fprintf(app.Stdout, "commit: %s%s\n", doc.Commit, dirty)
	}
	if doc.Built != "" {
		fmt.Fprintf(app.Stdout, "built:  %s\n", doc.Built)
	}
	fmt.Fprintf(app.Stdout, "go:     %s %s/%s\n", doc.Go, doc.OS, doc.Arch)
	return nil
}
