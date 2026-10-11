# 16 — Release (of flow itself) — delegated, verified

flow does not version or release itself with flow commands (non-goal). The
release workflow (`dogfood/.github/workflows/release.yml`) uses external tools:

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
