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
- [x] User-owned GitHub Project "flow" (Kanban template) exists with Status options Backlog · Ready · In progress · In review · Done and the built-in workflows of spec 17 §3 enabled; labels `agent`, `blocked`, `incident`, `flow-followup`, `phase` created.
- [x] `bootstrap/seed-issues.sh 17twenty/flow <n>` run: a phase parent issue per plan phase, one sub-issue per plan item with its AC in the body, all on the Project; `.ralph/IMPLEMENTATION_PLAN.md` item lines carry `(#N)`.
- [ ] `AGENT_CMD=… ./loop.sh` completes one iteration in `direct` mode without stopping for a broken build.

### Binary and specs
- [x] `go build -o bin/flow .` succeeds; `flow --help` lists the Setup/Verify/CI/Meta groups of spec 02; no command returns `ErrUnsupported`.
- [x] Every command in spec 00 §5 (`init`, `plan`, `apply`, `attest`, `doctor`, `check pr|deploy|commit`, `hook install|uninstall`, `schema`, `version`) implemented with `--json` and the exit codes of spec 02 §4.
- [ ] GitHub forge and GitHub Projects adapters pass their contract suites against `httptest` fakes; Jira Cloud, Linear, Gitea/Forgejo and GitLab adapters pass theirs (plan Phase 7).
- [x] `go test ./... -race -count=1` green with no network access from tests and the transport guard active; no test skipped or weakened to pass.
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
- [x] Phase 3 — Offline core (plan Phase 1): commit parser/linter, config, lock, schema, git, UI, hooks
- [x] Phase 4 — Engine with fakes (plan Phase 2): capabilities, planner, apply, doctor registry, policy, transitions
- [x] Phase 5 — GitHub forge and GitHub Projects tracker (plan Phase 3)
- [x] Phase 6 — Commands (plan Phase 4): init, plan, apply, check pr, check deploy, doctor, attest
- [ ] Phase 7 — Stage B activation: flow configures and gates its own repo; loop switches to pr mode
- [ ] Phase 8 — Live health (plan Phase 5): PR/deploy/ticket/transition audits, report, health issue
- [ ] Phase 9 — TUI (plan Phase 6): init wizard, picker, doctor watch, apply confirmation
- [ ] Phase 10 — More adapters and hardening (plan Phase 7): Jira, Linear, Gitea, GitLab, credential approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): all scenarios passing, README demo, 14 green doctor days

### Status notes

*(updated after the first live `flow doctor` run on this repository, commit
`bd712ed`, CI green)*

**Done.** Plan Phases 0–4 are complete (0.1–0.6, 1.1–1.14, 2.1–2.16, 3.1–3.12,
4.1–4.6), their issues closed and their phase parents (#6, #13, #27, #44)
closed with them. Every command in spec 00 §5 is implemented; the only
`ErrUnsupported` left in the shipped surface is three `doctor` flags whose own
items are in Phases 5 and 6 (`--report` 5.7, `--ci` 5.8, `--watch` 6.3), which
are refused rather than parsed and ignored.

- *Offline core.* `internal/commits` parser, linter and safe autofix behind
  `flow check commit` and the git hooks; `internal/config` with strict YAML,
  placeholders, semantic validation, lock, TOML precedence, `token_cmd` trust
  rules, credential resolution, JSON Schema and `flow schema`; `internal/git`
  read-only over exec; `internal/ui` decorating the engine's own renderings
  line by line, so colour-off is plain byte for byte.
- *Engine.* Capability registry and outcome resolution, `coretest` fakes and
  the three contract suites, desired state and canonical hashing, planner,
  comparison mode, apply executor, doctor check registry, follow-up lifecycle,
  drift and attestations, Definition of Ready, the policy gate, the transition
  engine, markers and `core.Client`.
- *Adapters.* GitHub forge and GitHub Projects tracker, both running
  `coretest`'s contract suites whole against `httptest` fakes that refuse a
  request missing `X-GitHub-Api-Version` or `Accept`.
- *Commands.* `init` (editing a preset byte-by-byte so comments and alignment
  survive), `plan` and `apply` over an idempotent fake world, `check pr`,
  `check deploy`, `doctor`'s setup half, and `attest`.
- *The first live run (this run).* `flow doctor` was pointed at this
  repository with the bot's own credential, and exited 0 having observed none
  of it. Two faults, one behind the other, and neither reachable from the
  fakes. `--fix` resolved the board's number into the lock but the tracker had
  been built before that and went on pointing at project zero, so the run that
  found the answer was the one run that could not use it. Observing then
  failed and the failure was swallowed into an empty plan — and an empty plan
  and a plan that wants nothing are the same bytes, which every check reads as
  a skip, and a skip is a success. The providers are now rebuilt after
  resolution; the error is carried as `Env.PlanErr` and reported, except where
  a pending placeholder already explains it and already holds the run at exit
  2. The CLI fakes now refuse, with no board number, exactly the two questions
  `GitHubProjects` refuses, so the order flow resolves things in is testable.

**Bootstrap Stage A is done apart from the loop.** The user-owned Project 2
has Status options Backlog · Ready · In progress · In review · Done, all seven
built-in workflows enabled, the labels `agent`, `blocked`, `incident`,
`flow-followup` and `phase`, a phase parent issue per plan phase, and 74 plan
lines carrying `(#N)`.

**What currently verifies.** The spec's Verify command now runs end to end.
Everything up to the last line is green with zero tests skipped:

```sh
test -z "$(gofmt -l .)" && go vet ./... && go vet -tags flowtest ./... && \
  go build -o bin/flow . && go build -tags flowtest ./... && \
  go test ./... -race -count=1 && go test -tags flowtest ./... -race -count=1 && \
  go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./... && \
  go run golang.org/x/vuln/cmd/govulncheck@latest ./... && \
  ./bin/flow doctor --json | jq -e '.exit_code == 0'
```

The last line is `exit_code: 2`, and the report says exactly why — which is
the point of it. Flow's own repository is not yet converged: two follow-ups
are open on a person (#90 `forge.protection`, #91 `forge.required_status` —
the bot has `push`, not admin), `forge.files` wants
`.github/workflows/flow.yml` created, `tracker.labels` wants two labels, and
four native behaviours are waiting on an attestation. That is Stage B's work,
not a defect. `.flow.lock` is committed, and a second `--fix` leaves it byte
for byte identical.

**Still open, and why.**

1. *H.1 is the human gate, and it now has a concrete shape.* Stage B
   activation (spec 17 §5) is the four things doctor is already asking for:
   somebody with admin creates the `main` ruleset requiring `flow/policy` and
   `ci / test` (or grants the bot admin so `flow apply` can), `flow apply`
   lands `.github/workflows/flow.yml` and the two labels, the four native
   transitions are attested, and `.ralph/AGENTS.md` goes to `CURRENT MODE: pr`.
   Identity is not a blocker — the agent is the machine user `flow-bot-gh`, a
   repo collaborator with `push` and a WRITER on the Project, so
   `agents.authors: [flow-bot-gh]` makes `agent.human_review` satisfiable with
   `@17twenty` reviewing.
2. *Why the board shows Backlog → Done and nothing between.* Not a
   misconfiguration. Spec 04 §5 gives `pr_opened_draft` → In progress and
   `pr_ready_for_review` → In review to `owner: flow`, performed by
   `flow check pr`; in `direct` mode no pull request is ever opened, so those
   two transitions cannot fire. Done works because `pr_merged` and
   `issue_closed` are `owner: native` and GitHub's built-ins are enabled.
   Backlog → Ready is nobody's: flow judges readiness, it never promotes.
   Stage B is what makes the middle of the board move.
3. *Phases 5–8:* live health, TUI, the remaining adapters, and the proof.

**Single best next step:** plan item 5.1 — the `live.prs.*` audits (#65, spec
07 §6). Doctor's setup half answers "is the configuration what it should be";
Phase 5 is the half that answers "and is the team actually working this way",
which is the whole claim of the project and the thing the 14-day proof is
measured on. 5.1 is the first and the one the rest hang off: every pull
request merged inside `--window` is read for a linked ticket, a `flow/policy`
status, a Conventional Commit title and a review, and each becomes a check
registered alongside the setup ones. It needs nothing that is not already
there — the forge adapter pages pull requests, the gate already judges one,
and `engine.Check` already carries evidence and remediation — and it is what
makes `--report` (5.7) and the pinned health issue have anything to say.
