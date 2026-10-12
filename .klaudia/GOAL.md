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
- [ ] Pinned "flow health" issue (marker `flow:health`) is updated in place by the daily doctor run (**done** — live as [#92](https://github.com/17twenty/flow/issues/92), rewritten in place, carrying its own 30-day run log); README demo section links the live Project, that issue and recent agent PRs, and states the current streak (plan item 8.2).
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
- [ ] Phase 8 — Live health (plan Phase 5): PR, ticket, transition and deploy audits, the `--fix` catch-up and the pinned health issue done (5.1–5.5, 5.7); flag cleanup (5.6) and `--ci` (5.8) remain
- [ ] Phase 9 — TUI (plan Phase 6): init wizard, picker, doctor watch, apply confirmation
- [ ] Phase 10 — More adapters and hardening (plan Phase 7): Jira, Linear, Gitea, GitLab, credential approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): all scenarios passing, README demo, 14 green doctor days

### Status notes

*(updated after plan item 5.5, the catch-up of the transitions flow owns,
commit `adec9ef`, CI green)*

**Done.** Plan Phases 0–4 are complete (0.1–0.6, 1.1–1.14, 2.1–2.16, 3.1–3.12,
4.1–4.6) and Phase 5 is five items of eight (5.1–5.5), their issues closed and
their phase parents (#6, #13, #27, #44) closed with them. Every command in spec 00 §5 is implemented; the only
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
- *The live-health audit of pull requests (this run, item 5.1).* The setup
  checks prove the rules are switched on; these four prove they were obeyed.
  Every pull request merged into the trunk inside `--window` is read for a
  ticket it named, for a `flow/policy` success that was already there when it
  merged, and for a person's approval if a machine wrote it; the trunk's own
  history is read for the convention the team chose. The four share one lazy
  reading of the window, so `--check` pays for nothing it will not look at and
  four checks are not four slightly different fortnights. A quiet fortnight is
  a pass that says "nothing merged", not "all 0 were fine". None of them
  offers a fix — last week's ungated merge cannot be gated now — so each
  points at the setup check that would stop the next one. The threshold spec
  07 §6 names finally has a key, `health.fail_below`, a whole number of per
  cent defaulting to 90, which settles the one open spec issue; `--window`
  stays a flag, because a daily job and a person checking by hand want
  different fortnights from the same repository. Live on this repository it
  reads `live.prs.conventional 64/64`.
- *The first live run (the run before).* `flow doctor` was pointed at this
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
3. *Phase 5 has three items left.* 5.1 (pull requests), 5.2 (deployments),
   5.3 (the board), 5.4 (native transitions) and 5.5 (the catch-up) landed, so
   every question spec 07 §6 asks of a live repository has a check behind it
   and the one finding flow can act on, it acts on. What remains is what
   doctor does with the answers: 5.6 (flag cleanup), 5.7 (`--report` and the
   pinned health issue) and 5.8 (`--ci` annotations). 5.7 is what the 14-day
   proof is read from, and the streak cannot start until H.1 makes doctor
   exit 0, so the clock does not favour reordering it.
4. *Phases 6–8:* the TUI, the remaining adapters, and the proof.

**The board half of the audit (item 5.3, just landed).** `live.tickets.*` asks
the tracker the question `live.prs.*` asks the forge, from the far side: every
ticket that reached Done had a merge behind it, work a team calls done is
running where `done_on` is `deployed`, everything waiting for review has
something to review, nothing in progress has gone quiet, and what is ready to
start really is. The board is read once for all five and bucketed through
`ColumnOf`, so a tracker's column ids stay its own business; each check finds
its column through the transition that lands work there and skips when the
workflow names none. Six mutants survived the first draft of the tests — the
key folding, the evidence ordering, the two-merge rule, the no-merge
exemption, the `done_on: deployed` column, and the undated tracker — and each
one now has a test that kills it.

Run live against this repository, it says something true and uncomfortable:
59 of 59 tickets reached Done with no merge behind them, because in `direct`
mode nothing opens a pull request. That is the finding, not a false positive,
and it is precisely what Stage B settles. The check stays literal and its
remediation points at `forge.protection`, the setup check that decides whether
work may reach the trunk that way at all.

**The catch-up (item 5.5, this run).** `live.transitions.native.<event>`
audits the moves flow declined to make and can only ever complain. This is the
other half: the moves flow owns, where the board being wrong never means flow
decided differently — the decision is deterministic — but that flow was never
asked. A CI run that did not start, a webhook the forge dropped, a credential
that expired on Friday evening. So `live.transitions.flow` is the one live
check whose remedy is flow's own command, and `doctor --fix` performs the
moves rather than printing steps for somebody.

It is one check for all events, where the native side is one per event:
`Transition` folds everything a ticket's work says into the single column it
belongs in, and asking per event would run that fold once per event and count
a ticket as many times as it had work. It is the same function over the same
snapshot the gate uses, so spec 04 §5 holds without being written twice —
which is the "delegate, never repeat" lesson applied where it matters most,
because a second implementation of the transition rules would have diverged
inside a release.

Three judgements worth recording. The grace runs from the *first* event that
asked for a move, not the last: a move owed since Monday should not wait
another quarter of an hour because somebody opened a second pull request, and
racing `flow check pr` costs nothing because rule 5 makes the move idempotent.
A caught-up ticket counts as held but keeps its evidence line, so a daily
`--fix` run does not exit non-zero for ever while the report still shows how
much flow had to put right. And this check alone reads the forge strictly: the
others only describe, so a pull request they miss costs a little accuracy,
whereas a reading missing the open half of a ticket's work is exactly the one
that would move it to Done too early.

Four mutants survived the first draft and each bought a test: ignoring tickets
closed with no pull request at all (closing is the one event that is not about
anybody's work); dating a deployment at `now` rather than at the grace
deadline, which would have raced `flow check deploy` on the one event where
the two most overlap; taking the latest asking event rather than the earliest;
and a half-read forge being acted on. Live on this repository the check reads
62/62 — the board is right, and now flow can say so about its own half of it.

### Item 5.7 — the pinned health issue (#71, commit 11109e2)

`flow doctor --report <file>` writes the run as GitHub-flavoured markdown, and
a `--fix` run keeps the same markdown in one pinned issue it rewrites every
morning. Live on this repository that issue is
[#92](https://github.com/17twenty/flow/issues/92), filed and then rewritten in
place by two consecutive runs.

The reading that mattered: spec 07 §10 says "updated in place", which costs
the history — and the history is the whole acceptance criterion, because spec
00 §3 asks for fourteen *consecutive* green days and an issue showing only
this morning proves one. So the issue carries its own log. Each run reads back
the table the last one wrote, adds today's row and keeps a month of them. The
streak is therefore provable from a fresh clone, a lost lock and a CI runner
that keeps nothing: the issue is the only thing that remembers, which is what
makes the final proof survive the machine it was produced on.

Four rules make the log mean something. One row per day, the last run of the
day winning, because a person may run doctor four times in an afternoon and
four rows for Tuesday would make "fourteen days" mean four afternoons. A day
with no run breaks the streak — a fortnight with a Sunday missing is thirteen
days of evidence and one day of not having looked. A row a person hand-edited
into something unparseable is dropped rather than guessed at, and a dropped
row is a gap like any other. And a `--check`-narrowed run publishes nothing at
all: it saw a fraction of the workflow, and recording it would put a day in
the streak nobody audited.

Two things it deliberately does not do. It does not render: the markdown comes
from `internal/ui` and the same bytes go to the file and to the issue, because
a second renderer in the engine would be a second answer (spec 01 §2). And it
never fails the run — the report has already said what is wrong, and a tracker
that will not take a copy of it is weather, not a verdict.

`core.Tracker` gained `UpdateFollowUp`, the missing half of `FindFollowUp`. It
re-reads the ticket and refuses a body whose marker is not really there (an
issue number can be reused, a person can edit a body, and the one thing worse
than failing to publish is publishing over somebody's bug report), and it
sends nothing when the bytes already match, so a quiet fortnight does not look
like news every morning. Nine mutants were tried; the only survivor was that
last no-op, which bought a contract assertion that the "last updated" date did
not move.

Raised by `@17twenty` against follow-up #91 and recorded under **Spec issues**
rather than fixed: the ruleset is promised `ci / test` in four documents and
flow only ever asks for `flow/policy`; and there is no cookbook recipe for
requiring a status check by hand when the credential lacks admin — which is
flow's own situation and the reason #90 and #91 are open. Both are owed before
Phase 7 settles the GitLab and Gitea adapters.

**Single best next step:** plan item 5.8 — `--ci` annotations and `--window`
(#72, specs 02 §2 and 07). It is the last thing standing between the daily run
and the scheduled job that spec 07 §10 describes: `--ci --fix --report` is one
command, and `--ci` is the only third of it still refused as unimplemented.
Taken ahead of 5.6 (flag cleanup, #70) for the same reason 5.7 was — Stage C
is a *scheduled* run, and a run that cannot annotate a CI job is a run whose
findings stop at a log nobody opens. 5.6 is a single self-contained check
(git trailers, a marker, a ticket filed under `--fix` exactly as 5.5 moves
one) and loses nothing by going last in the phase.
