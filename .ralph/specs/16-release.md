# 16 — Release (of flow itself) — delegated, verified

flow does not version or release itself with flow commands (non-goal). The
release workflow (`.github/workflows/release.yml`) uses external tools:

1. On push to `main`: run tests.
2. Compute next version from conventional commits with an external tool (svu or
   `git-cliff --bumped-version`). If there is a bump: tag `vX.Y.Z`, build
   (`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=…"`,
   linux/darwin/windows × amd64/arm64), publish GitHub Release with SHA256SUMS.
3. ALWAYS record a Deployment `environment: production`, `ref: <sha>`, with
   status `success` (payload `{"version": "vX.Y.Z" | "untagged"}`), after the
   release (or after tests for non-releasing merges): `go install …@main` and
   `@latest` both reach users from this point.

Doctor verifies the result (`live.deploys.lag` with `max_lag: 2h`).
`go install github.com/17twenty/flow@latest` must always work: `package main` at
root, no replace directives, no cgo, generated schema committed.

## Decisions

- **Templates install the release, not the module (issue #218).** Every
  shipped CI template downloads `flow_<tag>_linux_<arch>` for its pinned
  `FLOW_VERSION` and checks it against the release's `SHA256SUMS`, resolving
  `latest` to a tag first because the asset names carry it. Compiling flow on
  the runner with whatever Go it has was a second toolchain for every adopter
  to keep working. `go install …@latest` stays guaranteed for people.

- **`-X main.version` is what a scaffolded workflow installs (issue #186).**
  The release build injects the tag, `flow version` reports it, and the
  workflow `flow init` writes pins to it (spec 02 §2). So the ldflags above
  are not only how flow describes itself: they are how every repository flow
  configures gets a gate that installs a flow that exists. A release that
  forgets them produces binaries whose `init` falls back to `@latest`, which
  still works — the guarantee at the end of this section is what makes the
  fallback safe.
