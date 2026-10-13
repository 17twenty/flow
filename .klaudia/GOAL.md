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
- [x] One test exists and passes per acceptance scenario id in `specs/19-acceptance-scenarios.md` (A01–A07, G01–G06, V01–V05, S01–S03).
- [ ] Every item in `.ralph/IMPLEMENTATION_PLAN.md` Phases 0–8 is checked off and its issue closed; "Spec issues" records any gap rather than a skip.

### Self-hosting proof
- [ ] `flow init --tracker github-projects --owner 17twenty --project <n> --force` reproduces `specs/17-dogfood.md` §4 `.flow.yaml` exactly; `flow plan` shows only native (ruleset + merge settings) and attest (built-in workflows) outcomes.
- [ ] `flow apply` created a `main` ruleset requiring `flow/policy` and the build (`forge.required_checks: [test]`), preserving pre-existing required contexts; native transitions attested.
- [x] `.github/workflows/flow.yml` landed via a PR that was itself gated by `flow/policy` ([#94](https://github.com/17twenty/flow/pull/94)); `.ralph/AGENTS.md` reads `CURRENT MODE: pr`.
- [x] At least one agent PR has been opened by the agent identity — the machine user `flow-bot-gh`, which replaced the App in specs/17-dogfood.md §Decisions — moved In progress → In review by `flow check pr` ([#72](https://github.com/17twenty/flow/issues/72) on [#94](https://github.com/17twenty/flow/pull/94)), approved by `@17twenty`, and squash-merged on a green gate.
- [x] Pinned "flow health" issue (marker `flow:health`) is updated in place by the daily doctor run — live as [#92](https://github.com/17twenty/flow/issues/92), rewritten in place, carrying its own 30-day run log; the README demo section links the live Project, that issue and recent agent PRs, and states the current streak with the date it was read (plan item 8.2).
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
- [x] Phase 7 — Stage B activation: the ruleset is on, the gate is on `main`, the first agent PR was human-approved and merged green, `CURRENT MODE: pr`
- [x] Phase 8 — Live health (plan Phase 5): 5.1–5.8 all landed
- [ ] Phase 9 — TUI (plan Phase 6): picker, apply confirmation and doctor watch done; init wizard to go
- [ ] Phase 10 — More adapters and hardening (plan Phase 7): Jira, Linear, Gitea, GitLab, credential approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): all scenarios passing — done; README demo and 14 green doctor days to go

### Status notes

*(updated after Stage B went live and the first gated pull request found the
column-vocabulary bug, commit `950a80d`)*

**Done.** Plan Phases 0–5 are complete (0.1–0.6, 1.1–1.14, 2.1–2.16, 3.1–3.12,
4.1–4.6, 5.1–5.8), their issues closed and their phase parents (#6, #13, #27,
#44, #64) closed with them. Every command and every flag in spec 02 is
implemented: with item 6.3 the last `ErrUnsupported` left the shipped
surface, and `Context.unimplemented` went with it.

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
   the build (or grants the bot admin so `flow apply` can), `flow apply`
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
Phase 7 settles the GitLab and Gitea adapters. *(The first is now settled by
item 7.0 — see the run below.)*

**What running it found (this run).** `flow doctor` was pointed at this
repository and the report was correct and the tickets behind it were not.
Issues #90 and #91 were both titled `flow: somebody with the access has to do
it (17twenty/flow)` — the obstacle, not the work — so the trunk's protection
and the status check a merge must pass were one line in the issue list, twice.
Every item flow cannot perform shared that summary, by construction. The
summary is now the check's own title with what is short appended, and the
steps carry the planned change with its fields in sorted order: a refused item
is the one outcome where flow knows the whole of a change and lacks only the
permission to make it, so a ticket that withholds it asks a person to
rediscover what flow already decided.

That exposed a hole in the lifecycle. Spec 07 §4 had no way back from a ticket
that was true when it was filed, so a build that learns to say something
better leaves a stale ticket behind a correct report. An open follow-up whose
title or body no longer matches what flow would write today is now rewritten
in place, under `--fix` only, reading `refreshed`. It is not a nudge and does
not touch `nudged_at` — a nudge asks somebody to act and is rationed to one a
week, while correcting flow's own words costs the reader nothing and must not
wait six days. `core.Tracker.UpdateFollowUp` takes the title as well as the
body, and still sends nothing when both already say this. Live: #90 and #91
now name their own work and carry their own steps, and a second `--fix` left
`updated_at` where it was.

Seven mutants, four of them in forty lines of new code that no test named at
all — which is the lesson of the run, now in `AGENTS.md`: grep a new helper's
name across `*_test.go` before calling the item done. The survivor worth
having was the title-only rewrite, which no test could see because every
existing test changed the title and the body together.

**The protection that was written and never switched on (this run).** The
previous run's fix stopped `apply` closing #90 and #91 by mistake, and the
reason they were wrong to close turned out to be the finding itself: somebody
had written the whole of the ruleset flow asks for and left its enforcement
`disabled`. GitHub creates every ruleset that way, and a disabled one is in no
branch's effective rules, so both protection endpoints are honestly silent and
the trunk reads exactly like a trunk nobody has thought about. The report went
on telling a person to write what they had already written.

The GitHub adapter now reads the rulesets that are *not* in force, keeps the
ones whose conditions would cover the branch, and carries them as
`core.Protection.Dormant`. None of it counts as protection — nothing in it is
enforced — but "somebody has to write this" and "somebody has to switch this
on" are a day's work and one click, and a report that cannot tell them apart
sends a person off to rebuild what they have. Only the rules that are not in
force are fetched in full, because an enforced one has already been answered
by the effective-rules endpoint; a credential that may not read the rules at
all yields no rules and no error, since the reading sharpens a remedy and
decides nothing.

They reach the report through a new, deliberately narrow channel:
`core.Observation.Notes`, things a provider knows that flow does not compare
it on. A note is outside the canonical encoding, so a ruleset appearing in a
repository can never read as drift or stale a plan somebody approved — proved
live, where the two protection hashes in `.flow.lock` did not move. It is
shown only on a check that fails, because on a passing one it is a fact about
a mechanism nobody is relying on, and in a follow-up it leads the steps, ahead
of the access flow is short of and the change flow would have made.

Live, #90 now opens with `switch the rule "Flow" on — it is already written,
and only its enforcement is disabled`, with the link. **That switch is the
whole of what is left before Stage B**: the ruleset exists, it requires
`flow/policy`, and its enforcement is off.

The mutant worth recording: removing the guard that refuses to read `**`
survived, because `path.Match` treats `**` as `*` and the fixture branch had
three segments, so the row matched nothing either way. A table row has to
differ in the *answer*, not only in the input.

### Stage B is live, and the first gated pull request found a real bug

`@17twenty` switched the `Flow` ruleset on, so `main` now refuses a push whose
commit has no `flow/policy` success. The close-out push for item 5.8 was
rejected, which is how Stage B announced itself. `.github/workflows/flow.yml`
and the repository secret `FLOW_PROJECT_TOKEN` (the missing half of Stage A
step 5) went in as [#94](https://github.com/17twenty/flow/pull/94), because
spec 17 §5 wants the gate to arrive through a pull request the gate itself
judges: a `pull_request` workflow runs from the branch it is on, so the first
gated pull request is the one carrying the gate. It is adapted from the
shipped template rather than copied — flow is what is in the checkout, so each
job builds the binary from the commit it is judging, and the toolchain comes
from `go.mod`. `.ralph/AGENTS.md` reads `CURRENT MODE: pr`.

The first run of it posted `flow/policy` and then exited 64:

```
error: could not move #72 to in_review
why:   17twenty/projects/2 has no such column
fix:   the columns of 17twenty/projects/2 are: Backlog, Ready, In progress,
       In review, Done
```

The remedy lists the column it says is missing, which is the shape of a
vocabulary mismatch rather than a configuration one. `core.ColumnID` is the
tracker's own identifier, and the GitHub Projects adapter matched a move
against status option ids. The engine has no option id to give it:
`workflow.columns` is written by hand (spec 04 §2), the lock maps no
configured column to an option, and a column flow never created has no id
anywhere in flow's world. So `columnTarget` sent the display name —
correctly, it is the only name flow has — and *every* flow-owned transition
was refused. The adapter now resolves a target as an option id first and a
display name second, folding case on the name, because `ColumnOf` reads a
column back by a folded name and an id flow cannot read back would not be
idempotent (commit `950a80d`).

Why nothing caught it, which is the lesson: the contract suite moved by
`ColumnInReview`, and that constant is `"opt-in-review"` — a fixture id, not
a configured name — so the one vocabulary it exercised was the one no caller
uses. The reference fake hid the rest by accepting whatever it was handed and
inventing a column for it. Both are corrected: `FakeTracker` refuses a column
its fixture does not list, and the contract now moves by display name as well
as by id. Reverting the adapter reproduces the live error word for word.

Pushing the fix to the same branch re-ran the gate from the fixed checkout,
and **#72 moved In progress → In review, performed by `flow check pr` in CI**
— the middle of the board moves for the first time. `flow/policy` is red on
`agent.human_review`, which is the gate working: `agents.authors` holds
`flow-bot-gh`, so a machine-written pull request wants a person. The `policy`
job itself exits 0, by design — the status carries the verdict, and a failing
job would report the same thing twice.

[#93](https://github.com/17twenty/flow/pull/93), the file proposal `flow apply`
opened for `forge.files`, was closed as superseded: a pull request based on
`main` cannot run the workflow, because `main` does not have it yet.

### #94 merged, and the audit was reading a fixture

`@17twenty` approved and squash-merged [#94](https://github.com/17twenty/flow/pull/94),
so the gate, the scheduled daily `doctor` and the `FLOW_PROJECT_TOKEN` secret
are all on `main`. Three acceptance criteria close with it: the workflow
landed through a pull request `flow/policy` judged, an agent pull request was
approved by a person and merged green, and the fourteen-day streak now has a
trigger to be counted from.

The first `doctor` run afterwards said something that could not be true:
`live.prs.reviewed 0/1 — 1 of 1 agent pull requests merged without a person
approving`, and `live.prs.linked 0/1`, about the one pull request a person
had just approved and which closes #72. GitHub's list endpoint carries
neither reviews nor `closingIssuesReferences`; spec 10 §2 reads each of them
separately, one call per number, and the adapter says so in a comment. The
fake did not: `coretest.FakeForge` held whole pull requests and handed them
back from `PullRequests`, so four call sites in the engine — the gate,
`check deploy`, and both halves of the live audit — read fields that only
ever arrive in a test. Nothing was stubbed and nothing was skipped. The suite
simply agreed with code that could not work.

The fake now strips what a listing cannot carry, `ForgeContractCases` holds
that as a case both the fake and the GitHub adapter must pass, and
`core.PullRequest` documents which of its fields a listing leaves empty. The
four callers re-read each pull request by number, which is one read per pull
request inside a window the caller already chose the size of. All five call
sites were mutation-tested; the one survivor was the gate reading the *other*
open work, which bought
`TestGateReadsTheOtherOpenWorkInFullBeforeMovingACard` — without it the gate
would walk a card backwards past a sibling pull request still in review.
Live, both checks now pass. It is [#96](https://github.com/17twenty/flow/issues/96),
open as [#95](https://github.com/17twenty/flow/pull/95) and waiting on a human
approval, which is the gate working.

**flow was the thing making the board untrue (this run).** You asked why
[#72](https://github.com/17twenty/flow/issues/72) had not been autoclosed. It
had — [#94](https://github.com/17twenty/flow/pull/94) closed it at 00:59 — but
at 01:15 the gate ran on the next pull request, which named #72 in a closing
reference because its own issue did not exist yet, and moved the ticket from
In progress back to In review. Three live checks had been reporting it since,
and the thing they were reporting was flow. Spec 04 §5 rule 2, "never move
work out of done", guarded the *column*, and the column is the one thing that
can be wrong: `issue_closed` is `owner: native`, so a ticket whose tracker
automation lagged was still in a working column for the next pull request to
drag forward. A closed ticket now refuses every event but its own closing,
which still applies under the ordinary rules and so still reaches done. It is
[#97](https://github.com/17twenty/flow/issues/97), open as
[#98](https://github.com/17twenty/flow/pull/98). #72's card was put back in
Done by hand, because that move belongs to GitHub's automation and flow will
not perform it.

**Where the exit code stands.** `flow doctor` exits 2 on one check and four
attestations. The check is `live.tickets.done_merged 1/67`: sixty-six tickets
reached Done in `direct` mode, before Stage B, with no pull request behind
them. It is literally true and it ages out of the fourteen-day window on its
own, so the streak cannot begin until roughly a fortnight after the last
direct-mode close — today. The four attestations are
`tracker.native.{changes_requested,issue_closed,pr_merged}` and
`tracker.expedite`, which `flow attest` settles for thirty days at a time and
which a person has to actually look at first.

### Archiving was a word flow did not know (this run)

[#98](https://github.com/17twenty/flow/pull/98) is merged. The next finding
came from the board flow is dogfooding on: spec 17 §3 asks this project to
auto-archive closed items, and nothing in flow had ever been told what
archiving is. GitHub Projects answers the same item two ways —
`ProjectV2.items` leaves an archived one out, `Issue.projectItems` returns it
with the column it was archived in — and flow selected `isArchived` in
neither, so one ticket was "not an item of the project" and "in column In
review" at the same time, depending on which question happened to be put.
`pr.ticket.in_project` failed for a pull request naming one and asked for the
issue to be added to a project that already holds it.

The move was worse. Asked to set the Status of an archived item, GitHub
answers `{"updateProjectV2ItemFieldValue": null}` with no `errors` at all and
changes nothing, and `setColumn` only ever looked for a GraphQL error — so
flow reported a move the board never made. A write that changed nothing is not
a write that worked: the id coming back is now the evidence the write landed,
and `Move` refuses an archived item before asking, because the refusal is the
one thing a person can act on. `engine.Transition` decides no move for an
archived ticket and says so ahead of every other reason; unlike a closed one
there is no event it still acts on, since restoring work to the board is a
person's decision and not something that happens to it.

The fake is why none of it was visible. It rendered every field of a board
item whatever the query asked for, so `isArchived` could have been missing
from both shipped queries with the whole suite green — the adapter decoding a
field the server had volunteered. It now reads the item selection out of the
document it was sent and answers with that and nothing else, which is what
GraphQL does, and the two mutants that survived the first draft (the queries
not selecting the field, and the fake answering one nobody asked for) each
bought a test. It is [#99](https://github.com/17twenty/flow/issues/99), open
as [#101](https://github.com/17twenty/flow/pull/101), green on `test`,
`policy` and `setup checks`, red only on `agent.human_review` — and the gate
moved #99 to In review from CI, which is the middle of the board working
unattended.

**Where the exit code stands.** `flow doctor` exits 2 on one check and four
attestations. The check is now `live.tickets.done_merged 3/69`: sixty-six
tickets reached Done in `direct` mode, before Stage B, with no pull request
behind them, and they age out of the fourteen-day window on their own. Every
other live check passes, including `live.prs.reviewed 3/3` and
`live.transitions.flow 67/67`. The four attestations are
`tracker.native.{changes_requested,issue_closed,pr_merged}` and
`tracker.expedite`, which `flow attest` settles for thirty days at a time and
which a person has to actually look at first.

### The same shape on every other provider (this run)

[#101](https://github.com/17twenty/flow/pull/101) is merged. Archiving is not
a GitHub Projects quirk — every provider flow is going to grow has the feature
under a different name — so the research was verified and written where the
next implementer looks first (`assets/research/tracker-forge-research.md`,
which spec 10 opens by telling you to read), with Decisions in specs 10 and 11
and the acceptance criteria of plan items 7.1–7.4 and their issues.

Jira is the hard one: an archived issue is read-only and drops out of JQL
search, while `GET /issue/{key}` returns it in full with **no archived flag of
any kind**. The flag has to be derived from the two readings disagreeing,
which flow can do only because it already asks both questions — and only
against a full listing, since absence from a narrowed one means the filter.
Linear is the routine one: connections exclude archived entities unless the
query passes `includeArchived: true`, and Linear auto-archives issues a while
after they close, so a Linear board archives flow's own tickets as a matter of
course. GitLab and Gitea archive the repository instead: read-only in every
function, 403 on a write.

Which found one more hole, left open as
[#102](https://github.com/17twenty/flow/issues/102):
`core.Repository.Archived` exists, the GitHub adapter fills it, and nothing
reads it. `flow apply` against an archived repository would fail item by item
with whatever each endpoint said, when the whole truth is one sentence. It is
the forge-side shape of #99, and the refusal belongs in the engine so both
Phase 7 forges inherit it with the flag. The notes are
[#103](https://github.com/17twenty/flow/issues/103), open as
[#104](https://github.com/17twenty/flow/pull/104), green on `test`, `policy`
and `setup checks`.

### A promise made in a commit message (this run — item 5.6, Phase 5 closed)

[#104](https://github.com/17twenty/flow/pull/104) is merged. `live.flags.cleanup`
is the last item of Phase 5, and with it every question spec 07 §6 asks of a
live repository has a check behind it.

A feature flag is a branch in the code somebody said they would come back for.
The promise is the whole problem: it is made in a commit message, read once by
a reviewer and then never again, and the thing that would make it true happens
weeks later if at all. Flags nobody took out are how a trunk-based codebase
ends up with long-lived branches in it after all. So flow turns the promise
into a ticket and audits the ticket: every `Flag-Added:` trailer merged inside
the window must have one carrying `flow:flag name=<name> repo=<owner/name>`,
and `doctor --fix` files the ones that are missing, exactly as 5.5 performs a
move somebody's automation dropped.

The reading that mattered is that the trailers are git's to find, not flow's.
Where a trailer block begins depends on `trailer.*` configuration, on the keys
a repository already uses, and on rules about blank lines that have changed
over git's life, so flow hands the message to the working copy's own
`git interpret-trailers --parse` rather than matching a line that looks like a
trailer. The difference is not academic: a `Flag-Added:` line with a paragraph
of prose after it *is* prose, and a regex would file a cleanup ticket for a
flag nobody added. `TestDoctorReadsTrailersWithGitItself` puts exactly that
message on the trunk and asserts no ticket appears for it — a fixture the two
readings agree on would prove nothing about which one ran. A run with no
working copy to parse with fails the check rather than reporting a quiet
fortnight, because a run that could not look has not established that nobody
added a flag.

Three smaller judgements. The marker names the flag *and* the repository, so
two commits announcing one flag are one promise and one ticket — which is what
makes `--fix` idempotent without flow remembering anything between runs —
while `new_basket` in somebody else's repository stays somebody else's. A
closed cleanup ticket counts as much as an open one: the check asks whether
the promise was written down, not whether it has been kept, since flow does
not read the code, and requiring the ticket to be open would fail the check
for precisely the teams that did the work. And flow files it and then never
touches it again: a follow-up (§4) is work flow can verify and may therefore
close, taking a flag out is not, and the ticket says in as many words that a
person closes this one — a ticket implying flow was watching would sit on a
board being quietly untrue.

Nineteen mutants were applied to the new code. Two survived the first draft
and bought a test each: the no-tracker branch (deleting the guard whole
compiled and passed, which means nothing had ever handed the check a run with
no board), and the ordering of the evidence. Live on this repository:
`live.flags.cleanup 0/0 nothing in the window added a feature flag` — counted
and said in those words, rather than passing "all 0 flags were fine". It is
[#70](https://github.com/17twenty/flow/issues/70), open as
[#105](https://github.com/17twenty/flow/pull/105), green on `test`, `policy`
and `setup checks`.

### The gate judged the title; the trunk gets the header (this run — issue #106)

[#105](https://github.com/17twenty/flow/pull/105) is merged and Phase 5 is
closed. This run fixed the bug that closing it exposed.

GitHub squashes with `squash_merge_commit_title: PR_TITLE`, which appends
` (#<number>)` to the title. The gate linted the title. So a 66-character
title passed `cc.header.length`, landed as a 73-character header, and
`live.prs.conventional` reported it a fortnight later — the gate and the audit
contradicting each other about the same commit, which is the one thing a tool
whose whole claim is "provably true" cannot do. Two commits on the trunk are
the evidence: `e628fb8` at 73 characters and `e4291ab` at 75.

The reading that settles it: the convention is a rule about the trunk's
history, and the trunk gets whatever the forge writes, so the gate must judge
that. Only the adapter knows what its forge will write — GitHub and Gitea
append the number, GitLab appends nothing — so `core.PullRequest` gained
`SquashHeader`, the forge contract requires it of every pull request an
adapter produces (from a listing as well as from its own endpoint: a number
and a title are all it is made of), and `MergeHeader()` falls back to the
title for a pull request nobody asked a forge about. GitHub's adapter
composes it for open pull requests too, because judging a header before the
commit exists is the gate's whole job.

Two smaller judgements. The two strings differ only at the end, so every rule
about the front of a header — type, scope, the breaking marker — reads the
same string either way and only the length rule changes its answer; nothing
else in the gate needed touching. And a verdict passed on a string the author
cannot see has to say so: the finding carries one line of evidence naming the
header that lands and the characters the forge added, because "73 characters"
is unanswerable to a person counting their own title to 66. When the two are
the same string it says nothing, which is what keeps that line from being
noise on every failing title.

Nine mutants, one survivor on the first pass — the fake remembering a
fixture's stale header instead of deriving a fresh one, which would have let a
test rename a pull request and still be handed the header the old name would
have landed. It bought
`TestFakeForgeDerivesTheSquashHeaderRatherThanRememberingIt`. Landed as
[#107](https://github.com/17twenty/flow/pull/107), approved by `@17twenty`,
squash-merged green — its own header is exactly 72 characters, counted by the
rule it added.

**The picker (item 6.2, this run).** The second of the four screens spec 12
allows. The decision was already in the engine and stays there: `Placeholders`
walks the ladder of spec 03 §1.1, reaches the rung where a provider offered
more than one answer, and calls an `engine.Chooser`. This item is only what a
terminal makes of that call — `ui.Picker`, a `tea.Model` over `bubbles/list`.

Three things decided the shape. Browsing, filtering and paging a list is a
solved problem and bubbles is on the dependency list precisely so that flow
does not solve it again, so the mechanism is the library's and what belongs
here is which candidate a key press means, what the rows are painted in, and
what the key line says. A key belongs to the filter before it belongs to the
screen — return applies a filter and escape abandons one — so the rule is
inherited by asking the list what state it is in rather than restated, which
is also what keeps the key line from naming keys that would do nothing. And a
person who declines to pick has not answered: escape reports
`core.ErrNeedsInput` naming the `--set` that would settle it, and the zero
screen has picked nothing, so an interrupt, a terminal closing and a model
that came back as something else all land on the safe reading without anybody
enumerating them.

The root offers a chooser only when it would draw a screen at all. A nil one
is a rung the engine already steps over, and what it falls through to — the
candidates named in the report with the `--set` that records an answer — is
exactly the plain equivalent spec 12 asks every screen for, so there was
nothing to write for the half of the world that has no terminal.

Eighteen mutants, five survivors on the first pass and four of them real: a
row that lied about its height (which fills a page with twice the candidates
it can show and overflows the terminal), the ellipsis being underlined as if
the filter had matched it, the key line offering keys the list had switched
off, and the filter input left in the library's colours on a screen asked for
none. Each bought a test. The fifth is equivalent — the zero value of a type
parameter is the zero value either way.

**The acceptance scenarios (item 8.1, this run).** Spec 19 is the list this
whole program is answerable to, and the one thing a list of promises must not
be able to do is lose an entry. The spec opens by saying each scenario becomes
a test named `TestScenario_<ID>_…`, so the names are the index:
`TestEveryAcceptanceScenarioHasATest` reads the spec and the source of every
test in the repository and refuses to let the two disagree in either
direction — a scenario nobody keeps, and a test claiming an id the spec does
not list, both fail. It also refuses to let its own reading rot: every bullet
in the scenario section must carry an id the expression recognises, so a
heading written a new way cannot silently drop a promise.

All twenty-one ids are kept. Most were already tested under other names, but
reading each test against the sentence it claims to keep found three promises
nobody was keeping in full. A04 asserted a *plan* after the attestations
rather than the second apply — and apply is the command somebody runs twice.
Making that assertion of the apply uncovered a real disagreement:
`Plan.ExitCode` has always counted a `compensated` item as nothing left to do,
because the intent is enforced by something other than the setting the
configuration named and no second run will change that, while
`Applied.ExitCode` counted it as outstanding. One world therefore had a plan
exiting 0 and an apply exiting 2, for ever. G02 did not assert the
`flow:policy` marker, which is how the next run finds that comment to edit
rather than adding another. V01 named the merge that never shipped without
asserting the evidence links to it, which is the difference between a report
and a number.
**The init wizard (item 6.1, this run).** The last of the four screens spec
12 allows, and the one with the sharpest acceptance criterion: its plain
equivalent has to produce an identical `.flow.yaml`. That is met by not having
a plain equivalent to keep in step. Every question the wizard asks is a flag
`flow init` already had, the answers go back onto the command, and the command
then takes the road it always took — so the screen is a way of filling the
flags in, not a second way of writing a configuration.

Four things decided the shape. The answers open already filled in with what
flow would have written had nobody been asked, and that same value is the
baseline the review's command line is shortened against, so "press enter all
the way through", `flow init --yes` and the `flow init …` the last screen
prints are necessarily the same file. A question this configuration does not
have is not asked — an owner is a GitHub Projects idea, an address is a Jira
one — and the heading counts the questions there are rather than the steps
there might have been. The wizard does not discover: spec 02 §2 says `init`
must succeed offline and is the command run before any configuration names a
tracker to hold a credential for, so the board is left as `${pending:project}`
and resolved later by `flow doctor --fix`, through the picker that landed last
run, in the command that has the credential. And the review shows the rendered
file itself, cut to the terminal rather than scrolled, with a preview that
failed shown in its place — a configuration flow would refuse to read is
something to learn before it is written.

Spec 12 §1 ends the wizard with two more questions, `done_on` and the deploy
source. Both were written as flags and neither could be used: `workflow.done_on`
has to agree with `workflow.transitions.deployed` and `deploys.source` with the
rest of its block, while `init` splices scalars into a preset rather than
restructuring one, so every value either repeated the preset's own answer or
produced a file flow refuses to read. A flag whose every effective value is an
error is not a flag. Both questions are therefore asked at `--preset`, where
each option says what it means for them, and the step carries a line saying so
— a question folded into another has to be visibly answered there, or it has
been quietly dropped. Two Decisions record it, in spec 02 as well as spec 12,
because the flag list is spec 02's.

Twenty-three mutants, all killed. The ones worth having the item for: a
revisited question that came back empty, a cursor that did not wrap, defaults
that overwrote an answer somebody had given, and a cancelled wizard that wrote
the file anyway.
**The README demo (item 8.2, this run).** The section links the board, the
pinned health issue, the agent's own pull requests and the workflow that gates
them, and sets spec 17 §7's expectations beside what the audit reads today —
seventeen merges, every one gated, linked, approved by a person and in
production inside two hours. The streak is stated with the date it was read
and a sentence saying the issue is the record, because a number in a README is
a claim and that issue is the thing that is true. What is checked
mechanically is the part that can rot unseen: every forge link in the section
is read against `.flow.yaml` and the board number in `.flow.lock`, so a flow
pointed at another repository fails the test that says the demo is of this
one.

**Where the exit code stands.** `./bin/flow doctor` against this repository
exits 2 on six checks, and none of them is a defect in flow:

- four attestations nobody has made yet — `tracker.native.pr_merged`,
  `tracker.native.issue_closed`, `tracker.native.changes_requested` and
  `tracker.expedite`. Spec 17 §5 puts these in a human's hands and
  `docs/cookbook.md` §6.1 (new this run) now gives the four exact commands.
- `live.tickets.done_merged`, 66 of 73 — every ticket closed in `direct` mode
  before Stage B, none of which had a pull request behind it. It ages out of
  the fourteen-day window on its own as PR-mode work replaces it.
- `live.prs.conventional` warns at 2 of 88: the two commits above. A warn does
  not set the exit code, and both age out of the window too.

So the fourteen-day clock has a shape now: the attestations are the only part
of it waiting on a decision rather than on the calendar, and the earliest the
streak can *start* is the morning the last direct-mode ticket leaves the
window.

**Last run** finished 6.3, doctor's inline display and `--watch`
([#76](https://github.com/17twenty/flow/issues/76)), which is what makes spec
02's command surface whole: every command and flag the spec names now does
what it says. Two screens, because a run being waited on and a run being
watched want opposite things from the terminal — the first leaves the report
in the scrollback and takes its display away, the second takes the alternate
buffer and leaves nothing. The run reports both moments of a check, reached
and answered, through a nil-able `engine.DoctorProgress` callback, since the
gap between them is the wait the display exists to fill. Mutation testing
found the defect worth having the item for: a clock wound by whatever
finished a round left a second chain of ticks behind every manual refresh, so
a window open all afternoon would have asked the forge faster and faster.

**Single best next step:** Phase 7, the other providers — Jira Cloud
([#79](https://github.com/17twenty/flow/issues/79)), Linear
([#80](https://github.com/17twenty/flow/issues/80)), Gitea/Forgejo
([#81](https://github.com/17twenty/flow/issues/81)) and GitLab
([#82](https://github.com/17twenty/flow/issues/82)). None of the four depends
on another, so they go up as separate pull requests. Then Phase 8's proof:
the acceptance scenarios whole ([#86](https://github.com/17twenty/flow/issues/86))
and the README demo ([#87](https://github.com/17twenty/flow/issues/87)).

The two issues this section named last run are both settled:
[#102](https://github.com/17twenty/flow/issues/102), an archived repository
read and then written to anyway, landed as
[#109](https://github.com/17twenty/flow/pull/109), and
[#100](https://github.com/17twenty/flow/issues/100) with it.

Spec 12 is now whole: the wizard, the picker, doctor's display and `--watch`,
and the apply confirmation are the four screens it allows, and there are no
others.

In parallel, and only `@17twenty` can do it: run the four `flow attest`
commands in `docs/cookbook.md` §6.1 after looking at the board's Workflows
panel (the **…** menu on <https://github.com/users/17twenty/projects/2>; it is
an overlay with no URL of its own), and land the resulting `.flow.lock`
through a pull request. Until then the scheduled daily
run cannot exit 0, and the streak cannot begin.

### The build was promised and never asked for (this run — issue #123)

`@17twenty` raised it against follow-up #91 a fortnight ago and it has sat
under **Spec issues** ever since, deferred until Phase 7 was in sight. It is
in sight. Spec 17 §1 and §5, `docs/cookbook.md` §5 and this file all promised
that `flow apply` creates a ruleset requiring `flow/policy` **and the build**,
and `engine.forgeRequiredStatus` only ever asked for `policy.status_context`
— because nothing in the configuration could say what the build is called.
The item is additive, so a repository that already required its build kept it,
which is exactly why nobody noticed: the gap only opens on a trunk with no
ruleset yet, where flow writes one the build is not required by and the
cookbook then tells the reader that adding it by hand means flow failed at its
own job.

`forge.required_checks` is the key, a list of status contexts validated like
any other configured list — no empty entry, no padding, no repetition. Flow's
own gate is still not written there; naming it anyway is a repetition and is
dropped rather than refused. Neither half implies the other, so the item
exists whenever either has anything in it: a team may require a build and run
no gate at all, and `strict_checks` follows the whole list rather than the
gate alone, because currency is a property of a required check whoever
required it.

The other half of the contradiction was a name. A ruleset matches a check run
by its own name, which for GitHub Actions is the job's; `ci / test` is a label
the web UI composes out of the workflow and the job, and no API reports it.
This repository publishes `test`, `policy`, `setup checks` and `daily health`,
so requiring `ci / test` would have required a check nothing publishes — a
trunk nobody could merge to. `.flow.yaml` now says `required_checks: [test]`,
and `flow plan` against the live repository proposes exactly one thing:
`forge.required_status` — `create "test"`, beside the `flow/policy` the
ruleset already has.

**Single best next step:** Phase 7's adapters, beginning with the Gitea/Forgejo
forge ([#81](https://github.com/17twenty/flow/issues/81)) — the contract suite
is already written, and `forge.required_checks` has settled what each new
adapter is asked to require. The cookbook still owes the other half of
`@17twenty`'s original report: a per-forge recipe for requiring a status check
by hand when the credential lacks admin, which is this repository's own
situation and the reason #90 and #91 are open. It is owed with #81 and #82, so
that each recipe is verified against the adapter that implements it rather
than guessed from the API documentation.
