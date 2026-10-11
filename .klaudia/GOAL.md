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
- [ ] Phase 4 — Engine with fakes (plan Phase 2): capabilities, planner, apply, doctor registry, policy, transitions
- [ ] Phase 5 — GitHub forge and GitHub Projects tracker (plan Phase 3)
- [ ] Phase 6 — Commands (plan Phase 4): init, plan, apply, check pr, check deploy, doctor, attest
- [ ] Phase 7 — Stage B activation: flow configures and gates its own repo; loop switches to pr mode
- [ ] Phase 8 — Live health (plan Phase 5): PR/deploy/ticket/transition audits, report, health issue
- [ ] Phase 9 — TUI (plan Phase 6): init wizard, picker, doctor watch, apply confirmation
- [ ] Phase 10 — More adapters and hardening (plan Phase 7): Jira, Linear, Gitea, GitLab, credential approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): all scenarios passing, README demo, 14 green doctor days

### Status notes

**Done.** The repo is real: target layout per spec 01 §1 on `origin/main`, Go
module `github.com/17twenty/flow`. Plan items 0.1–0.5 and 1.1 are complete and
ticked in `.ralph/IMPLEMENTATION_PLAN.md`.

- *Skeleton (plan Phase 0).* The binary parses the whole spec 02 command tree;
  unimplemented commands fail as `ErrUnsupported` (exit 1), the only stub the
  plan permits. Core has sentinels with exit-code mapping, `UserError`
  what/why/fix rendering, and the spec 15 §2 redacting slog handler.
  `flow version` is fully implemented in human and `--json` form. SIGINT (130)
  and `--timeout` (69) are tested against the real signal plumbing via a
  `flowtest`-tagged blocking command.
- *Parser (plan 1.1).* `internal/commits` implements the spec 08 §1 grammar and
  never errors. It handles CRLF, `core.commentChar`, scissors, merge/revert/
  autosquash forms and the `BREAKING CHANGE` footer, and reports rune-accurate
  columns for linter diagnostics. `FuzzParse` is seeded from
  `assets/testdata/commits`; a 45s run did 26M execs with no panics.

**What currently verifies.** The spec's Verify command cannot pass until
`doctor` exists. Today's equivalent, all green:

```sh
test -z "$(gofmt -l .)" && go vet ./... && go build -o bin/flow . && \
  go test ./... -race -count=1 && go test -tags flowtest ./... -race -count=1 && \
  go run honnef.co/go/tools/cmd/staticcheck@latest ./... && \
  go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

**Phase 2 is ticked with one caveat**: plan item 0.6 ("CI green on main") now has
a workflow and pushed commits, so GitHub Actions should be running it — nobody
has confirmed the run is green. Every step of `ci.yml` passes locally.

**Phase 1 stays unticked.** Its local half is done (layout, module, first push);
its GitHub-side half (Project, labels, App, `FLOW_PROJECT_TOKEN`, running
`seed-issues.sh`, one `loop.sh` iteration) is blocked on the Human prerequisites
above, and its three unticked acceptance criteria track exactly that.

**Blocked on the human** — nothing else can satisfy these:
1. Create the GitHub App `17twenty-flow-agent`, install it, expose an
   installation token as `GH_TOKEN` on the loop host.
2. Create the classic PAT with `project` scope as repo secret `FLOW_PROJECT_TOKEN`.
3. Create the user Project "flow" and the five labels; set repo merge settings.
4. Approve installing `gh` (Homebrew) so the agent can seed issues and drive the
   Project. Until then `bootstrap/seed-issues.sh` cannot run.

**Single best next step:** plan item 1.2 — the linter rules in
`internal/commits` (spec 08 §2), implementing every `cc.*` rule id with
severity, message, column and fix text. It must also rewrite
`assets/testdata/commits/{valid,invalid}.txt` to v2 rule ids **in the same
commit** and record a Decision in spec 08: the seeds still expect the retired
`cc.footer.*`, `cc.trailer.*`, `cc.body.wrap` and `cc.description.imperative`
rules, and carry `Refs:`/`Flag-Added:`/`Migration:` footers from the earlier,
wider design. `TestCorpusInvalidSamplesMatchHeaderFormat` already pins which
samples must fail the grammar, so keep it passing through the rewrite. Then 1.3
(`check commit` + safe autofix) gives the first genuinely implemented command.


