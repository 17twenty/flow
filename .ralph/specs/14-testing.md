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
- **§4 a fake may not answer what the real adapter would refuse (live fix,
  after item 4.6).** `coretest.FakeTracker` holds its board in memory and so
  does not care whether anybody knows its number, while `GitHubProjects`
  refuses to query a board whose number is not set. The CLI fakes therefore
  wrap it: with no number, `tracker.project` and `tracker.status_options` —
  the two questions the real adapter answers by querying the board, and only
  those — refuse in the same words. Everything else passes through, because
  repository labels, the per-view settings the adapter declines to verify,
  the native workflows and discovery all answer without a number there too.
  Without the wrapper the order flow resolves things in was untestable, and
  the bug that order hid shipped.
- **A fake is wrong when it is more generous than the adapter, not only when
  it is less (bug fix after item 5.8).** `coretest.FakeForge` held whole pull
  requests and handed them out from `PullRequests`, so every caller read
  reviews and closing issues that no forge puts in a listing. Nothing was
  stubbed and nothing was skipped; the suite simply agreed with code that
  could not work, and the live audit went on to report that nobody had
  approved or linked the first pull request the gate ever passed. The fake now
  strips what a listing cannot carry, and `ForgeContractCases` holds the rule
  as a case of its own, so an adapter and a fake that disagree about it fail
  rather than diverge quietly.
- **§2 a fake answers the fields the document selected, and no others.** The
  GitHub Projects fake rendered every field of a board item whatever the query
  asked for, so `isArchived` could have been left out of both shipped queries
  with the whole suite green: the adapter would decode a field the server had
  volunteered. The fake now reads the item selection out of the document it
  was sent and drops what is not named, which is what GraphQL does and what
  makes a missing field a test failure rather than a live one.
- **A contract case reads the adapter's declaration and asserts honest
  silence, rather than skipping (item 7.3).** A behaviour no provider offers
  is still a contract: the adapter must not invent it. So where a capability
  is declared absent, the case asserts the absence — a forge that cannot read
  the link between a pull request and a ticket must report no closing issues,
  so the tracker fallback runs instead of being silenced by a guess, and a
  forge that cannot be told where a squash takes its words must report the
  same answer after being asked as before. A skipped case would let either
  adapter quietly answer from a pattern of its own, which is the one thing
  the suite exists to catch.
- **A contract case must ask only what the adapter declares it can do (item
  7.3).** "A listing is a summary and says so" proved the point by requiring
  both the reviews and the closing issues a reading adds, and Gitea publishes
  no link between a pull request and a ticket at all — so the second adapter
  to take the contract failed a case the first had written in its own image.
  The reviews stand for every forge; the closing issues are now asked for
  only where `linked_issues_read` is declared, exactly as the case about the
  linkage itself already did. A capability the contract ignores is a
  capability the declaration cannot mean anything by.
- **A contract case must be written in the vocabulary of the adapter under
  test (item 7.1).** The tracker contract said `#10` and `tracker.labels`,
  which are GitHub's words for a ticket and for a registry flow can write to.
  A Jira issue is `FLOW-10` and a Jira project has no registry flow can write
  to at all, so a contract spelling either one its own way tests the fixture
  rather than the product. The fixture now carries both: a key prefix every
  case spells its keys through, and the desired item the adapter writes — or
  the declaration that it writes none, which is held to refusing every change
  with `ErrUnsupported` and to saying `set: false` in its capabilities, since
  an adapter silently refusing what it declares it can do would make a plan
  propose work that never lands (spec 05 §1).
- **An advisory moves the floor the same way a new import does (item 2.16,
  revisited, issue #177).** GO-2026-6809 made `crypto/x509` reachable through
  every `fmt.Fprintf` in the tree and turned `test` red on `main` and on every
  open pull request at once, with nobody having changed a line. The remedy is
  the same single edit: `go.mod`'s `go` directive to the release that fixes
  it, so the runner, every developer and `govulncheck` move together. Nothing
  is pinned a second time anywhere, which is what makes a one-line change
  enough — and it is why the first symptom of an advisory is a red gate on
  somebody's unrelated work rather than a notification nobody reads.
