# GOAL — flow v1, self-hosting

## Objective

Turn this specs-and-plan archive into `github.com/17twenty/flow`: a Go CLI that makes a lean
trunk-based workflow true and provably true across GitHub (forge) and GitHub Projects v2
(tracker), implementing specs `specs/00`–`19` and every item in `IMPLEMENTATION_PLAN.md`.
The repo currently has no commits, no Go code, and no GitHub Project. The work therefore starts
with bootstrap Stage A (restructure to the target layout per `README.md` §Layout, first commit
and push, GitHub Project + labels + agent App + secrets, seeded issues), proceeds through plan
Phases 0–8 building the binary, flips to self-hosting at Stage B (flow configures its own repo,
gates its own PRs with `flow/policy`, agent works in `pr` mode), and is complete when
`flow doctor` on the flow repo exits 0 for 14 consecutive daily runs while development continues
(spec 00 §3, spec 17 §5 Stage C). Native-first throughout: no user regex, no stubs, no skipped
tests, dependencies limited to spec 01 §4.

## Human prerequisites (ask for these up front; agent cannot do them)

- GitHub App `17twenty-flow-agent` created and installed on the repo (contents RW, pull requests
  RW, issues RW, metadata R); installation token available to the loop host as `GH_TOKEN`.
- Classic PAT with `project` scope stored as repo secret `FLOW_PROJECT_TOKEN`.
- Repo settings: Issues on, merge commits and rebase off, squash on with PR title/body.
- Approval to install the `gh` CLI on this machine (Homebrew) — needed for all Project/issue work.

## Acceptance criteria

### Bootstrap and repo shape
- [x] Target layout on `main`: `PROMPT.md`, `PROMPT_plan.md`, `AGENTS.md`, `IMPLEMENTATION_PLAN.md`, `specs/` under `.ralph/`; `loop.sh` at root; `assets/` at root; `dogfood/` contents merged into the repo root (except `.github/workflows/flow.yml`, added in Stage B); `bootstrap/` at root.
- [x] Initial commit(s) pushed to `origin main` with Conventional Commit messages and a scope from `.flow.yaml` `commits.scopes.allowed`.
- [ ] User-owned GitHub Project "flow" (Kanban template) exists with Status options Backlog · Ready · In progress · In review · Done and the built-in workflows of spec 17 §3 enabled; labels `agent`, `blocked`, `incident`, `flow-followup`, `phase` created.
- [ ] `bootstrap/seed-issues.sh 17twenty/flow <n>` run: a phase parent issue per plan phase, one sub-issue per plan item with its AC in the body, all on the Project; `.ralph/IMPLEMENTATION_PLAN.md` item lines carry `(#N)`.
- [ ] `AGENT_CMD=… ./loop.sh` completes one iteration in `direct` mode without stopping for a broken build.

### Binary and specs
- [ ] `go build -o bin/flow .` succeeds; `flow --help` lists the Setup/Verify/CI/Meta groups of spec 02; no command returns `ErrUnsupported`.
- [ ] Every command in spec 00 §5 (`init`, `plan`, `apply`, `attest`, `doctor`, `check pr|deploy|commit`, `hook install|uninstall`, `schema`, `version`) implemented with `--json` and the exit codes of spec 02 §4.
- [ ] GitHub forge and GitHub Projects adapters pass their contract suites against `httptest` fakes; Jira Cloud, Linear, Gitea/Forgejo and GitLab adapters pass theirs (plan Phase 7).
- [ ] `go test ./... -race -count=1` green with no network access from tests and the transport guard active; no test skipped or weakened to pass.
- [x] `gofmt -l .` empty; `go vet ./...`, `staticcheck ./...` and `govulncheck ./...` clean.
- [ ] One test exists and passes per acceptance scenario id in `specs/19-acceptance-scenarios.md` (A01–A07, G01–G06, V01–V05, S01–S03).
- [ ] Every item in `.ralph/IMPLEMENTATION_PLAN.md` Phases 0–8 is checked off and its issue closed; "Spec issues" records any gap rather than a skip.

### Self-hosting proof
- [ ] `flow init --tracker github-projects --owner 17twenty --project <n> --force` reproduces `specs/17-dogfood.md` §4 `.flow.yaml` exactly; `flow plan` shows only native (ruleset + merge settings) and attest (built-in workflows) outcomes.
- [ ] `flow apply` created a `main` ruleset requiring `flow/policy` and `ci / test`, preserving pre-existing required contexts; native transitions attested.
- [ ] `.github/workflows/flow.yml` landed via a PR that was itself gated by `flow/policy`; `.ralph/AGENTS.md` reads `CURRENT MODE: pr`.
- [ ] At least one agent PR has been opened by `17twenty-flow-agent[bot]`, moved In progress → In review by `flow check pr`, approved by `@17twenty`, and auto-merged on a green gate.
- [ ] Pinned "flow health" issue (marker `flow:health`) is updated in place by the daily doctor run; README demo section links the live Project, that issue and recent agent PRs, and states the current streak.
- [ ] `flow doctor` on `17twenty/flow` exits 0 on 14 consecutive daily runs with the live-health expectations of spec 17 §7 met, while the loop keeps shipping.

## Verify

```sh
cd /Users/nickglynn/Projects/nexgen/flow && \
  test -z "$(gofmt -l .)" && \
  go vet ./... && \
  go build -o bin/flow . && \
  go test ./... -race -count=1 && \
  go run honnef.co/go/tools/cmd/staticcheck@latest ./... && \
  go run golang.org/x/vuln/cmd/govulncheck@latest ./... && \
  ./bin/flow doctor --json | jq -e '.exit_code == 0'
```

Final proof (Stage C, not reproducible locally): the pinned `flow:health` issue on
`17twenty/flow` shows 14 consecutive green daily `flow doctor` runs.

## Progress

- [ ] Phase 1 — Bootstrap Stage A: repo layout, first push, Project, labels, seeded issues, loop runs
- [x] Phase 2 — Skeleton (plan Phase 0): module, kong root, sentinels, slog, embed, CI green
- [ ] Phase 3 — Offline core (plan Phase 1): commit parser/linter, config, lock, schema, git, UI, hooks
- [x] Phase 4 — Engine with fakes (plan Phase 2): capabilities, planner, apply, doctor registry, policy, transitions
- [ ] Phase 5 — GitHub forge and GitHub Projects tracker (plan Phase 3)
- [ ] Phase 6 — Commands (plan Phase 4): init, plan, apply, check pr, check deploy, doctor, attest
- [ ] Phase 7 — Stage B activation: flow configures and gates its own repo; loop switches to pr mode
- [ ] Phase 8 — Live health (plan Phase 5): PR/deploy/ticket/transition audits, report, health issue
- [ ] Phase 9 — TUI (plan Phase 6): init wizard, picker, doctor watch, apply confirmation
- [ ] Phase 10 — More adapters and hardening (plan Phase 7): Jira, Linear, Gitea, GitLab, credential approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): all scenarios passing, README demo, 14 green doctor days

### Status notes

**Done.** The repo is real: target layout per spec 01 §1 on `origin/main`, Go
module `github.com/17twenty/flow`, every commit so far CI-green. Plan Phase 0
(0.1–0.6), Phase 1 (1.1–1.14 except 1.12) and **all of Phase 2 (2.1–2.16)** are
complete, ticked in `.ralph/IMPLEMENTATION_PLAN.md`, and their issues closed
with the board item in Done.

- *Skeleton (plan Phase 0).* kong root parsing the whole spec 02 command tree,
  sentinels with exit-code mapping, `UserError` what/why/fix, the spec 15 §2
  redacting slog handler, `flow version`, embedded assets, CI.
- *Offline core (plan Phase 1).* `internal/commits` parser, linter and safe
  autofix with `flow check commit` and the git hooks; `internal/config` with
  strict YAML, placeholders, semantic validation, lock reader/writer, TOML
  precedence, `token_cmd` trust rules, credential resolution, JSON Schema and
  `flow schema`, embedded presets; `internal/git` read-only over exec.
- *Engine (plan Phase 2).* Capability registry and outcome resolution,
  `coretest` fakes and the three contract suites, desired state and canonical
  hashing, planner and plan JSON, comparison mode, apply executor, doctor check
  registry with placeholder resolution, follow-up lifecycle, drift and
  attestations, Definition of Ready, the policy gate with its status and
  comment renders, the transition engine, markers, and now `core.Client`.
- *`core.Client` (item 2.16, this run).* One `Do` over a `Request`: base-URL
  resolution, JSON in and out, per-attempt timeout, bounded concurrency,
  `Link` pagination, rate-limit headers, retries for idempotent methods and for
  anything the provider refused to perform, an ETag cache (memory and 0700 disk)
  keyed by a fingerprint of the credential, and the spec 14 transport guard that
  refuses any non-loopback host inside a test binary with nothing to remember.

**What currently verifies.** The spec's Verify command cannot pass until
`doctor` exists (plan item 4.5). Today's equivalent, all green:

```sh
test -z "$(gofmt -l .)" && go vet ./... && go build -o bin/flow . && \
  go test ./... -race -count=1 && go test -tags flowtest ./... -race -count=1 && \
  go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./... && \
  go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

**Still open, and why.**

1. *Plan item 1.12 — `internal/ui` styles and plain/markdown/JSON renderers.*
   The one gap in Phase 1. Nothing depends on it until the commands of Phase 4
   need somewhere to print to, so it was left behind the engine work; it must
   land before 4.2.
2. *Phases 3–8.* Adapters, commands, live health, TUI, extra adapters, proof.
3. *Blocked on the human* (the Phase 1 GitHub-side half, unchanged): create the
   GitHub App `17twenty-flow-agent` and expose an installation token as
   `GH_TOKEN`; store the classic PAT as `FLOW_PROJECT_TOKEN`; enable the spec
   17 §3 built-in Project workflows; run `bootstrap/seed-issues.sh`. The
   identity blocker recorded in spec 17 still stands: objects created with
   @17twenty's PAT are authored by @17twenty, who cannot approve them, so
   `agent.human_review` is unsatisfiable until the App exists.

**Single best next step:** plan item 3.1 — the GitHub forge adapter's read half
(`Repo`, `PullRequest`, `PullRequests`, `Commits`, `Compare`, `File`), specs 10
§2 and 01 §3, now unblocked by `core.Client`. Build it on `core.Client`
directly: paginate with `Response.Links["next"]` by passing that absolute URL
straight back as `Request.Path`, send the `Accept: application/vnd.github+json`
and `X-GitHub-Api-Version: 2022-11-28` headers as `ClientOptions.Header`, and
let the ETag cache do the conditional requests. Test it with an `httptest`
server seeded from `coretest.ContractForge` — the same world the in-memory fake
is built from — and run `coretest.ForgeContract` against the subset implemented
so far, so the real adapter and the fake answer the same questions identically.
Linked issues (3.3) need GraphQL and are a separate item; leave
`PullRequest.LinkedIssues` empty until then.
