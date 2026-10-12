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

- **The fakes are the reference implementation, and sit the contract
  themselves (item 2.3).** `coretest.FakeForge`, `FakeTracker` and
  `FakeDeploys` are run through `ForgeContract`, `TrackerContract` and
  `DeployContract` in `coretest`'s own tests. A contract no implementation
  passes is a wish; holding the fakes to it means the engine tests run
  against adapters that behave exactly as the real ones must, and a contract
  that cannot be satisfied is found when it is written rather than in phase
  3.
- **A contract is given a fixture and a constructor (item 2.3).**
  `ForgeContract(t, func(t, ForgeFixture) core.Forge)`. The fixtures —
  `ContractForge`, `ContractTracker`, `ContractDeploys` — are exported, so a
  real adapter's test seeds its `httptest` server from the same world the
  in-memory fake is built from, and both are asked the same questions about
  the same repository. Without a shared world each suite would test a
  different thing and agree only by accident.
- **Faults are injected by method name (item 2.3).** `Fail`, `FailOnce`,
  `Heal` and `Calls` on an embedded `coretest.Faults`, keyed by the method's
  own name. It needs no generated mock and no interface per failure, and
  `FailOnce` is what makes "retried once, then succeeded" expressible. Every
  fake also refuses a cancelled context with `ErrUnavailable`, so the root's
  signal handling is testable offline.
- **The transport guard is automatic, not opt-in (item 2.16).** A guard a test
  has to install is a guard the one forgotten test does not have, and that test
  is exactly the one that reaches a real provider. `core.Client` therefore
  refuses any non-loopback host whenever `testing.Testing()` reports a test
  binary, with no flag to set and nothing to remember. `FLOW_LIVE=1` lifts it,
  which is the same switch the live suite is already gated on, and
  `ClientOptions.AllowHost` overrides it entirely — it is the hook that
  credential host binding (spec 15 §1, item 7.6) will use. The refusal is
  `ErrPermission` and
  is never retried: the answer would be the same three more times.
- **A new stdlib area moves the Go floor (item 2.16).** `govulncheck` had
  nothing to say while flow was offline; the first `net/http` import made ten
  advisories reachable in one commit, all fixed in go1.26.9. `go.mod`'s `go`
  directive moved from 1.26.4 to 1.26.9, which is the whole change: CI reads
  `go-version-file: go.mod`, so the runner and every developer follow. This is
  what "latest stable" in AGENTS.md means in practice — a deliberate edit to
  `go.mod` when a gate says so, never an ambient drift.
- **A contract suite is addressable case by case (item 3.1).** An adapter lands
  a few methods at a time, and a suite that can only be run whole cannot be run
  at all until the last plan item — which is exactly when its pressure is worth
  least. `coretest.ForgeContractCases` therefore names every case, and
  `ForgeContractSubset` runs the ones an adapter already claims. Naming a case
  that does not exist fails the test rather than passing quietly, so a renamed
  case cannot shrink a subset to nothing unnoticed, and the methods still to
  come refuse with `ErrUnsupported` rather than returning a plausible zero.
- **A real adapter is tested against the provider's own wire shapes (item
  3.1).** `internal/forge`'s fake serves GitHub's documented JSON over the same
  `coretest.ForgeFixture` the in-memory fake is built from, so both answer the
  same questions about the same repository. It paginates, it answers
  conditional requests with 304, it records the escaped path, and it refuses a
  request that does not pin the API version — the things an in-memory fake can
  never exercise and a live test must never be needed for.
- **An adapter that has landed runs the suite whole, not a subset (item
  3.12).** Subsets are for an adapter under construction: they let each plan
  item be held to the part of the contract it claimed. Once the last method
  lands, the call becomes `ForgeContract` / `TrackerContract` /
  `DeployContract`, so a case added to a contract later runs against the
  adapter without anybody remembering to add its name. The subset helpers stay
  for the adapters still to come (Gitea, GitLab, Jira, Linear).
- **The tracker contract is sat by both owner kinds (item 3.12).** A person's
  projects and an organisation's are reached through different fields of
  GitHub's schema and shown at different addresses. One suite, run twice, is
  what makes `owner_type` a tested setting rather than a configuration people
  discover is wrong in production.
- **Every GitHub fake refuses a request that does not pin the API version
  (item 3.12).** Only the forge's fake did, so the tracker and the deployment
  source could have stopped sending `X-GitHub-Api-Version` or `Accept` and
  nothing would have gone red — a silent promotion to whatever GitHub's REST
  means today. All three now answer 400 and 406, and all three count the
  `User-Agent`, which is how a repository's audit log says which tool acted.
  Deleting the header from an adapter fails most of its package.
- **The CLI fakes are chosen by replacing the adapter builder, not by
  threading a flag (item 4.2).** `fakes_flowtest.go` sets the package's
  `newAdapters` variable in its `init`, so the only difference between a test
  run and a real one is which function builds the providers. Every other part
  of a command — kong parsing, configuration, the lock, rendering, the exit
  code — is the shipped path. The file is not in a release build, so
  `FLOW_TEST_FAKES` means nothing to the shipped binary, and a `!flowtest`
  test asserts exactly that by watching a release build still ask for a
  credential it was not given.
- **Only the exact value `1` selects the fakes (item 4.2).** A variable
  somebody exported empty, or set to a word that reads like yes, must not
  quietly produce a fabricated world: the fakes answer every question
  confidently, so a run that reached them by accident would look like a
  passing one.
- **The fake world is per working tree and outlives one command (item 4.2).**
  Idempotence is the property these fakes exist to demonstrate, and providers
  rebuilt from a fixture on every invocation would converge for ever and
  prove nothing. The world is kept in a map keyed by the repository's path,
  so a second command in the same process sees what the first one wrote and
  two tests cannot see each other's board.
- **The fake forge's trunk holds what the working tree holds (item 4.2).** A
  fake whose default branch were always empty would have every run propose
  the same files again, and an apply that can never converge is not something
  scenario A04 can be written against. A repository in step with its remote
  is the ordinary case — somebody ran `flow init` and pushed what it wrote —
  so that is what the fake models, and a test wanting the proposal path
  deletes the file from the working tree to get it.
