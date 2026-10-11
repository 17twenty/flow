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
