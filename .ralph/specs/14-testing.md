# 14 — Testing

- `go test ./... -race -count=1` offline, < 60s. A test transport guard fails any
  non-loopback request. `XDG_CONFIG_HOME`, `FLOW_CACHE_DIR` → `t.TempDir()`.
- Git tests: temp repos, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`,
  fixed author/committer/dates; skip if git missing.
- Injected `core.Clock`; no sleeps. Stdlib `testing` + helpers in `coretest`.

| Layer | Scope |
|---|---|
| Unit | parser/linter, DoR, policy rules, outcome resolution, diff/canonical hashing, placeholder parsing, config validation, linkage, live-check evaluators (pure over fetched data) |
| Fuzz | `FuzzParse`, `FuzzConfigDecode`, `FuzzMarkerParse` |
| Golden | plan/doctor/policy renders (plain, md, JSON), init output per preset, lock writer |
| Contract | Forge/Tracker/Deploy contract suites vs HTTP fakes (`httptest`) built from documented API shapes |
| Engine | plan/apply/doctor/check flows vs in-memory fakes with fault injection |
| CLI | binary built in `TestMain` with `-tags flowtest`; fakes selectable via `FLOW_TEST_FAKES=1` only in that build (test asserts release builds can't) |
| Live | `//go:build live` + `FLOW_LIVE=1`; never in the loop |
| Dogfood | the flow repo itself (spec 17) — the final acceptance |

Coverage targets: commits 90%, config 85%, engine 80%, adapters 70%.

## Decisions

- **CI pins the Go toolchain to `go.mod`, not `stable`.** Both workflows used
  `go-version: stable`, which silently followed GitHub's runner images up to Go
  1.27.2 while local development stayed on the 1.26.4 named in `go.mod`. No
  released staticcheck (latest is v0.8.1) can read Go 1.27 export data, so every
  push since the first had a red `ci / test` — it failed in
  `internal/byteorder`, `math/bits` and friends with "export data version 5 is
  greater than maximum supported version 4", never in flow's own code. The
  module's `go` directive is now the single source of truth: `setup-go` uses
  `go-version-file: go.mod`, so CI and developers run the same compiler and a
  Go upgrade becomes a deliberate edit to `go.mod` rather than an ambient break
  arriving with a runner image. `AGENTS.md`'s "latest stable" is read as "keep
  `go.mod` current", not "let CI drift".
- **Third-party analysers are pinned; `govulncheck` is not.** `staticcheck` is
  pinned to `@v0.8.1` because it is a correctness gate whose verdict must be
  reproducible — an unpinned analyser can turn a green commit red with no change
  in the repo. `govulncheck` deliberately stays `@latest`: its value is knowing
  about vulnerabilities disclosed since the last run, so there pinning would
  defeat the point.

