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
- [x] GitHub forge and GitHub Projects adapters pass their contract suites against `httptest` fakes; Jira Cloud, Linear, Gitea/Forgejo and GitLab adapters pass theirs (plan Phase 7).
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
- [x] Phase 9 — TUI (plan Phase 6): the init wizard, the picker, the apply confirmation and doctor's display with `--watch`
- [x] Phase 10 — More adapters and hardening (plan Phase 7): Gitea/Forgejo, GitLab, Jira Cloud and Linear, plus `forge.required_checks`, the commit-status deploy source and credential host approval
- [ ] Phase 11 — Proof (plan Phase 8 + Stage C): the acceptance scenarios, the README demo and the agent skill are done; **every plan item but the human gate is ticked**, and 14 green doctor days are what is left

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

### The gate judged the title; the trunk gets the header (issue #106)

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

### The four screens spec 12 allows (items 6.2, 6.1, 6.3)

**The picker (item 6.2).** The second of the four screens spec 12
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

**The init wizard (item 6.1).** The last of the four screens spec
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

**Doctor's display and `--watch` (item 6.3).** The third screen
([#76](https://github.com/17twenty/flow/issues/76)) is what makes spec 02's
command surface whole: every command and flag the spec names now does
what it says. Two screens, because a run being waited on and a run being
watched want opposite things from the terminal — the first leaves the report
in the scrollback and takes its display away, the second takes the alternate
buffer and leaves nothing. The run reports both moments of a check, reached
and answered, through a nil-able `engine.DoctorProgress` callback, since the
gap between them is the wait the display exists to fill. Mutation testing
found the defect worth having the item for: a clock wound by whatever
finished a round left a second chain of ticks behind every manual refresh, so
a window open all afternoon would have asked the forge faster and faster.

Spec 12 is now whole: the wizard, the picker, doctor's display and `--watch`,
and the apply confirmation are the four screens it allows, and there are no
others.

### The proof, and the build nobody asked for (items 8.1, 8.2, issue #123)

**The acceptance scenarios (item 8.1).** Spec 19 is the list this
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

**The README demo (item 8.2).** The section links the board, the
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

**Where the exit code stands.** `./bin/flow doctor` against this repository
exits 2 on six checks, and none of them is a defect in flow:

- four attestations nobody has made yet — `tracker.native.pr_merged`,
  `tracker.native.issue_closed`, `tracker.native.changes_requested` and
  `tracker.expedite`. Spec 17 §5 puts these in a human's hands and
  `docs/cookbook.md` §6.1 gives the four exact commands.
- `live.tickets.done_merged`, 66 of 73 — every ticket closed in `direct` mode
  before Stage B, none of which had a pull request behind it. It ages out of
  the fourteen-day window on its own as PR-mode work replaces it.
- `live.prs.conventional` warns at 2 of 88: the two commits above. A warn does
  not set the exit code, and both age out of the window too.

So the fourteen-day clock has a shape now: the attestations are the only part
of it waiting on a decision rather than on the calendar, and the earliest the
streak can *start* is the morning the last direct-mode ticket leaves the
window.

The two issues named two runs ago are both settled:
[#102](https://github.com/17twenty/flow/issues/102), an archived repository
read and then written to anyway, landed as
[#109](https://github.com/17twenty/flow/pull/109), and
[#100](https://github.com/17twenty/flow/issues/100) with it.

**Single best next step:** Phase 7's adapters — the only code Phases 0–8
leave unwritten — beginning with the Gitea/Forgejo forge
([#81](https://github.com/17twenty/flow/issues/81)), because the contract
suite is already written and `forge.required_checks` has settled what each
new adapter is asked to require. None of the four — Jira Cloud
([#79](https://github.com/17twenty/flow/issues/79)), Linear
([#80](https://github.com/17twenty/flow/issues/80)), Gitea/Forgejo and GitLab
([#82](https://github.com/17twenty/flow/issues/82)) — depends on another, so
they go up as separate pull requests. The cookbook still owes the other half of
`@17twenty`'s original report: a per-forge recipe for requiring a status check
by hand when the credential lacks admin, which is this repository's own
situation and the reason #90 and #91 are open. It is owed with #81 and #82, so
that each recipe is verified against the adapter that implements it rather
than guessed from the API documentation.

In parallel, and only `@17twenty` can do it: run the four `flow attest`
commands in `docs/cookbook.md` §6.1 after looking at the board's Workflows
panel (the **…** menu on <https://github.com/users/17twenty/projects/2>; it is
an overlay with no URL of its own), and land the resulting `.flow.lock`
through a pull request. Until then the scheduled daily
run cannot exit 0, and the streak cannot begin.

### A second forge, and the things it would not say (item 7.3)

Everything flow had ever read, it had read from GitHub. The interfaces said
otherwise and the contract suite said otherwise, but a suite that only one
adapter has ever passed is a description of that adapter. Item 7.3 is the
first time something else answered the same questions, and what it proved is
mostly about flow rather than about Gitea.

**One adapter, two forges.** Forgejo is Gitea's fork and serves the same API
under the same path, so the kind is `gitea` for both and the flavour is a
detail of detection rather than of configuration. A second kind would have
doubled every declaration, every capability row and every recipe in the
cookbook to say the same things twice.

**`forge.url` is a question, not a default.** GitHub has one host; a Gitea
lives wherever somebody installed it. An empty `forge.url` is therefore
refused in the root — before the credential is resolved, because spec 15 §1
does not want a `token_cmd` that unlocks a vault run to produce a secret for a
provider flow was never going to speak to. That refusal is where the root also
stopped assuming: `client()` now takes the headers its provider wants instead
of GitHub's, and the switch that chooses an adapter has a kind it does not
know as its last arm rather than its default.

**Four answers the API made flow say out loud.** Spec 10 §3 had asked for one
of them to be confirmed at build time, and the other three were only visible
once something was actually written:

- `linked_issues_read` is **false**. Gitea makes the links from a body's
  closing keywords and publishes none of them — no field of the pull request
  names an issue, and no endpoint lists them — so the branch and title
  fallback stands. A pattern of flow's own here would have been a guess
  dressed as the forge's answer, which is worse than the fallback it silences.
- Protection is two readings, and only one needs an administrator. The branch
  carries what its rules come to and any credential may see that; the rule
  carries the settings the branch does not mention and only an administrator
  may read it. A refusal on the rule reads the same as its absence, because
  the alternative is that every read of a protected branch fails for the kind
  of bot account flow is ordinarily given. The rule is read under the name the
  branch gives for it, never the branch's own: a glob rule governs branches
  whose names it does not share.
- A comparison is asked twice. Gitea counts the commits one side has that the
  other does not and never names the relation; one count cannot tell
  `identical` from `behind`, so the question goes both ways round and flow
  names the relation itself — which is what the containment proofs behind
  `flow check deploy` depend on.
- A commit's statuses are a history, walked by `highestindex` and folded to
  the latest per context. Ordering by time would tie: two runs of the same
  workflow second finish in the same second, and the walk would keep whichever
  row the database happened to answer with.

**Checking the wire against the instance.** Two field names taken from the
documentation do not exist in the API, and Gitea answers an unknown key with a
200 and ignores it. Reading an instance's own schema is what found them; had
it not, `flow apply` would have reported protection nobody had. That is the
worst kind of bug this program can have, because flow's entire claim is that
what it says is true.

**The mutation pass, and the three that lived.** Fifteen mutants, twelve
killed. Each survivor was worth more than the twelve:

- The test for "a failed read does not become the answer for the rest of the
  run" asked a repository whose reference character is `#` — which is also
  what an unread repository falls to. The fixture could not tell the mutant
  apart. It now uses a repository whose issues live elsewhere, where the kept
  failure and the right answer differ.
- Nothing had ever asked flow *not* to look for a rule behind a branch that
  says it has none. Only an administrator may read one, so the question costs
  a refusal on every item of every plan.
- The third was not a gap but a redundancy: the read that decides whether to
  publish a status marked itself "do not cache", which `core.Client`
  guarantees already — it sends the tag it holds and takes a 304 as
  permission to reuse the body, never as permission to skip asking. Two
  mechanisms answering one question drift apart silently, so the line is gone
  and a test says why that is safe.

**The cookbook owes less than it did.** `docs/cookbook.md` §7 now carries the
by-hand recipe for requiring a status check on both GitHub and Gitea, which
was the other half of `@17twenty`'s original report — the half that is this
repository's own situation, and the reason
[#90](https://github.com/17twenty/flow/issues/90) and
[#91](https://github.com/17twenty/flow/issues/91) are open. Each recipe is
written against the adapter that implements it rather than from the API
documentation, which is exactly how the two non-existent field names were
caught.

[#131](https://github.com/17twenty/flow/pull/131) is open with it:
`test`, `policy` and `setup checks` green, `flow/policy` red on
`agent.human_review` alone, which is the gate working.

**Single best next step:** the GitLab forge
([#82](https://github.com/17twenty/flow/issues/82)), which is the last of the
three remaining Phase 7 adapters that is a forge and so reuses everything item
7.3 just generalised — the shared REST walk, the per-provider headers, and a
contract suite that has now been passed by two adapters rather than one. The
two trackers, Jira ([#79](https://github.com/17twenty/flow/issues/79)) and
Linear ([#80](https://github.com/17twenty/flow/issues/80)), depend on nothing
and can go up beside it.

The thing only `@17twenty` can do is unchanged, and it is now the only part of
the fourteen-day clock waiting on a decision rather than on the calendar: run
the four `flow attest` commands in `docs/cookbook.md` §6.1 and land the
resulting `.flow.lock` through a pull request.

### A skill, and the harness that proved it wrong (item 8.3, issue #134)

`@17twenty` asked for a skill a Claude-compatible harness could pick up and
work a ticket with: how to use flow, the bash, the credential. The interesting
half of that request is the second acceptance criterion — that another harness,
given the skill and no other context, can take a ticket and drive it to In
review. That is not a thing you can assert in a test, so it was run.

**What the skill is.** `skills/flow/SKILL.md` is spec 17 §6's loop with this
repository taken out of it: verify, take a ticket that meets the Definition of
Ready, branch, write commit messages the gate will accept, open a pull request
that links its ticket, wait for the gate and for a person. It states no
defaults of flow's as facts. Which column is takeable, what makes a ticket
ready, which commit types and scopes are allowed, what the status is called —
each is read out of the `.flow.yaml` in front of it, because a skill carrying
flow's own answers would be confidently wrong in every repository that
configured something else. What `.ralph/` knows about *this* repository stays
in `.ralph/`.

**What keeps it true.** The same thing that keeps the shipped templates true
(spec 02 §2): tests. Every `flow …` the skill writes in code must name a
command that answers `--help`; every flag beside one must appear in that
command's help; every dotted name in its prose must be a rule
`engine.PolicyRuleIDs()` publishes or a key the configuration schema has. The
first draft passed a mutant — a skill saying `flow release` — because it
reused the template test's filter, which skips a word that is no command of
flow's rather than failing on it.

**Then it was run.** A sub-agent got the skill, a path and a credential, and
was sent to issue #135 with nothing else: no plan, no `AGENTS.md`, no specs. It
came back with pull request [#137](https://github.com/17twenty/flow/pull/137) —
branched, fixed, tested, committed as the machine account with a conventional
message, `Closes #135` in the body, the `agent` label on it, every check green
but `agent.human_review`, and the card moved to In review by flow rather than
by hand. It did not approve its own work. The criterion is met.

**And it found four things wrong with the skill**, which is the point of
running one:

- The `flow doctor --json` reading asked for `.summary` and `.outcome`. Neither
  is a field of that document. It does not error — it prints
  `{"exit": null, "failing": []}` for a run that exited 2 with a real failing
  check, so an agent trusting the skill would have called a sick repository
  healthy. This one now has a test behind it: `skill_flowtest_test.go` runs
  `flow doctor --json` and resolves every field path the skill's jq takes
  against the document flow actually produced.
- The board query stopped at the first hundred items. This board holds a
  hundred and three, and #135 was on the second page — so the recipe reported
  the ticket it had just been sent to work as not being on the board, which
  reads exactly like `pr.ticket.in_project` failing. It pages now.
- The skill named an unresolved placeholder in `.flow.yaml` as a reason to stop
  work, without knowing that `.flow.lock` is where flow writes what it has
  since discovered. By the letter of it, the agent should have downed tools on
  a healthy repository.
- Committing as the account whose work it is was the last line of an appendix.
  It is not a nicety: `agents.authors` is a list of logins, and a commit
  authored as the human whose git config you inherited makes `agent.human_review`,
  the agent label and the whole review audit say something untrue.

It also reported, correctly, that the skill says nothing about *how to do the
work* — so §4 now tells an agent to go and read the repository's own
conventions first, which is the honest division: flow owns the workflow and
the repository owns everything else.

### A board flow does not write to (item 7.1)

Every tracker so far has been one flow configures. Jira is the first that is
not, and finding that out was most of the work.

The capability file said, from the research, that flow could set a project's
columns and its labels and read its WIP limits. None of the three survived
contact with the API. A status can be created — and a created status is still
not a column, because which statuses a project's workflow offers is the
workflow's business and which column of a board shows one is the board's, and
neither is writable in the public Cloud API. A label has no registry at all:
it exists the moment an issue carries one, so there is nothing to create and
nothing to read back. A WIP limit is on a board, and a project may have
several, so flow cannot say which board a limit it read would be on. The
declaration now says `set: false` everywhere, `Apply` refuses every change
with `ErrUnsupported`, and the plan for a Jira project files a follow-up for
whoever administers it rather than proposing work that could never land. That
is the capability model doing exactly what spec 05 wrote it for: the plan got
*smaller* and more honest, and nothing downstream had to learn about Jira.

What is left is reading, and Jira answers differently enough to be worth an
adapter of its own:

- **An archived issue has no flag anywhere.** Not on the issue, not in a
  field, not as an expansion; the request for one has been open for years. An
  archived issue is read-only, keeps its status, answers by key in full, and
  is in no search — and that disagreement between the two readings is the
  whole of the evidence, so that is what flow derives it from. The listing it
  is derived from is the whole project and nothing narrower, because absence
  from a windowed search means the window.
- **A move is a transition, not an assignment.** GitHub Projects takes any
  option id for any item; Jira will only make the moves the workflow offers
  from where the issue is now. A column the workflow will not reach from here
  is refused with both status names in the message, pointed at whoever
  administers the workflow. Closing is the same mechanism, with a resolution
  set on the way through only where the transition's own screen offers the
  field.
- **Linkage is Jira's convention and not the forge's.** What a pull request
  says it closes is an issue of the repository, which is nothing here. So the
  key is read out of the branch name, the title, and the header a squash will
  land — never a pattern somebody configured.
- **Basic is a pair, and only half of it is the secret.** A Jira API token
  authenticates against the email of the account that made it. The two are
  paired in the root, after the credential is resolved and before the client
  is built, so the HTTP client still knows one way to authenticate and the
  email never becomes a second secret.

**Taking the contract is what generalised it.** The tracker contract suite
said `#10` and `tracker.labels` — GitHub's word for a ticket and GitHub's
registry. A Jira issue is `FLOW-10` and a Jira project has no registry flow
can write to, so a contract spelling either its own way tests the fixture
rather than the product. The fixture now carries a key prefix every case
spells its keys through, and the desired item the adapter writes — or the
declaration that it writes none, which is held to refusing every change *and*
to saying `set: false`, since an adapter quietly refusing what it declares it
can do would make a plan propose work that never lands.

**Nineteen mutants, five survivors**, and the five are the interesting part.
Three were gaps: a resolution matched letter by letter where a team spells
`Won't Do` its own way, the header a squash lands never read for a key, and
flow's order of preference among resolutions — it would rather record `Won't
Do` than `Duplicate`, and a scheme holding both must not decide that by
whichever it lists first. Two were not gaps but redundancy. A key folded on
both sides of a comparison whose halves both come from Jira is a fold that
cannot matter, and three readings had the ETag store switched off for fear of
acting on a stale answer — but `core.Client` revalidates every cached GET, so
a body it reuses is one the provider has just agreed with, and on the JQL
search the flag was doing nothing at all, since only a GET is cached and a
search is a POST. A surviving mutant is always a finding; it is not always a
missing test.

### The last adapter, and the header with nothing in front of it (item 7.2)

**Linear is a board flow writes to.** `internal/tracker/linear.go` and
`linear_wire.go` are the fourth adapter and the last code of the plan. One
host serves every workspace, so `tracker.url` names nothing and
`tracker.project` holds a team key such as `ENG`: a Linear team owns the
workflow states that are flow's columns and the issues that are its tickets,
which is what makes a team the board. The whole tracker contract passes
against a GraphQL fake in Linear's own shapes — Relay cursor pages, errors at
HTTP 200, and an `Authorization` header that is the credential and nothing
else.

**That header cost a seam in `core`.** Every other provider flow speaks to
takes a scheme in front of the credential, and `""` already means "use the
default", so a Linear personal API key had nowhere to say "none at all".
`core.AuthSchemeNone` is what says it, and which kind of credential it is, is
in the credential itself: a token beginning `lin_oauth_` is an OAuth access
token and goes out as a Bearer token. Nobody is asked a second question about
a credential that names itself.

**And a second one, because Linear names failures somewhere else.** GitHub
writes `type` beside the message; Linear writes `extensions: {type, code}`
underneath it. `core.GraphQLError.Kind` now answers the first of those a
provider filled in, so an adapter maps the names its own provider uses and
never unwraps the envelope a second time. Both of Linear's are mapped — the
codes are precise and the types are the coarse ones under them — because a
refusal flow has no code for must not read as an outage worth retrying.

**Three things Linear would not let flow say.** A category is settled when a
state is created: `WorkflowStateUpdateInput` carries no type, so moving a
column into another category would mean archiving it and taking every issue
standing in it off the board. flow refuses with `ErrUnsupported` and says what
a person does instead, and the declaration now reads `status_categories: set:
false` for the same reason. An issue has no closed flag either — completed and
canceled are the two endings, and Linear dates both itself — so closing work
nobody will do means finding the leftmost cancelled column, and the column is
the closing. And an estimate is points, whose meaning each team settles for
itself, so `core.Ticket.Size` is left empty rather than multiplied by an hour
flow invented.

**Linkage is what Linear recorded, then what the branch says.**
`core.Tracker.LinkedKeys` is handed a pull request, no context and no error,
so it cannot ask: the attachment reading is the one flow has already done.
Every issue flow reads carries its attachments, and the addresses in them
answer first — Linear's own GitHub integration puts a pull request there. The
fallback is the identifier in the branch, the title or the header a squash
will land, which is Linear's convention and never a pattern somebody
configured.

**Fifty-six mutants over three passes, and the two that mattered were
redundancy.** Taking the leftmost column of a type compared positions inside a
loop over a list `states()` had already sorted by position — two mechanisms
answering one question, and the second one deleted. Reading a close reason
from the dates *and* from the state type was the same fault: the state type
now says why and the dates say when, so there is one answer to each. The rest
bought tests — a trashed issue read as gone, a follow-up remembered rather
than searched for twice, flow's own comment edited rather than written again,
a limit that is a limit. The fake needed fixing twice as well: it answered
`trashed: false` for an issue in the trash, which is the field's whole
purpose, and it wrote every edit at the same instant, so a date that moved
could not be told from one that did not.

**Every plan item but the human gate is now ticked.** 0.1–8.3 are done;
`- [ ] H.2` is the only unchecked line left in
`.ralph/IMPLEMENTATION_PLAN.md`, and it is the fourteen-day confirmation.

**Single best next step:** nothing in the plan — the code of v1 is written.
What remains is the clock and the one thing only `@17twenty` can do, which is
still the only part of it waiting on a decision rather than on the calendar:
run the four `flow attest` commands in `docs/cookbook.md` §6.1 and land the
resulting `.flow.lock` through a pull request. Until those four states are
vouched for, `flow doctor` exits 2 every day and the fourteen cannot start.

### A pull request flow's own gate refused (issue #152)

**The daily audit did what it was told, and flow said no.** A lock written on
a runner goes into the bin with the checkout, so in `--ci` the refreshed lock
is offered as a pull request instead (spec 07 §2). It opened one — #151 —
and flow's gate then judged flow's own pull request and refused it on
`pr.title.conventional` and `pr.ticket.linked`. The title was a constant,
`chore(flow): refresh .flow.lock`: this repository allows no `flow` scope and
does not let a `chore` merge without a ticket. Every morning flow would have
opened a pull request that flow then marked with a red cross, on the one
change nobody could act on.

**A constant cannot keep to a convention it has never read.**
`engine.Proposal` composes the header instead: a type that this team writes
*and* may merge without a ticket, the first scope the team allows out of the
ones the change can honestly claim, and the whole thing linted by the same
linter the gate lints it with — against the header a squash would land, with
room for the number the forge appends, because that was issue #106's lesson
and flow is not going to repeat it about its own writing.

**Both of flow's own pull requests go through it.** The lock refresh and the
files a workflow needs were two constants with one defect between them, which
is two mechanisms answering the same question. There is one now, with two
descriptions: `engine.LockProposal` and `engine.FilesProposal`, each naming
the types and scopes it may honestly claim.

**When nothing landable can be written, flow writes nothing.** A configuration
that lets no type through unlinked, or demands a scope it names none of, gets
no pull request and a message naming the key — `commits.types`,
`commits.unlinked_types`, `commits.scopes.allowed`, `commits.header_max` — in
the *reason* and not only in the remedy, because an apply reports an item's
failure by its message alone. This stays outside the grading: spec 07 §2
already settled that failing to propose the lock is not a finding about the
team's delivery.

**And this repository now allows what flow needs.** `commits.unlinked_types`
gained `chore`, which is what every shipped preset already has; the lock
refresh is titled `chore(config): refresh .flow.lock` and the files proposal
`ci(ci): add the files the workflow needs`. A root test reads the real
`.flow.yaml` rather than a fixture, so narrowing the configuration later
fails here rather than at six in the morning.

**Fourteen mutants, two survivors, both bought tests.** A remedy may only
name types `commits.types` actually allows — advising somebody to unlink
`docs` when the team never writes `docs` sends them to the wrong key — and
nothing had asked whether the doctor's refusal reached stderr at all.


### Work that cannot land, and the board that says it is moving (issue #156)

When #154 merged, #155 became unmergeable. Nothing reported it. No forge
sends mail about a conflict, and no check goes red — `test` and `flow/policy`
were computed on a head that has not changed and stay green there for ever.
The card stayed in **In review**, approved, with every doctor check passing.
That is the audit's one claim, that the board is true, failing quietly.

**flow asks the forge instead of guessing.** `core.PullRequest.Mergeability`
is `clean`, `conflicted`, `behind`, or nothing at all — and nothing at all is
the absence of a reading, not a reading that nothing is wrong. GitHub
computes it in the background and answers `null` until it has; treating
silence as a refusal would make the check fail hardest on the pull request
somebody opened a moment ago.

**Only `Forge.PullRequest` fills it, and only for an open one.** Every forge
goes on answering about work it has finished with, against a base that has
moved on, which for anything merged is a refusal — and "this merged pull
request conflicts" can only mislead whoever acts on it. A listing never
carries it either, including GitLab's and Gitea's, which do: an engine
written against a listing would answer "nothing is stuck" on GitHub and the
truth everywhere else, a difference between providers nothing would catch.

**`live.tickets.review_stuck` fails; `live.tickets.review_stale` warns.** The
first is a queue that has stopped and names which of the two reasons it is —
a conflict needs a resolution, a stale branch needs a push, and sending
somebody to resolve one that is not there wastes the half hour the check
saved. It asks only of approved work: falling behind the trunk is the
ordinary condition of every pull request in a queue the moment one of them
merges. The second is the column `progress_stale` never watched, warning
because a slow review is a fact about what people had time for.

**One patience, not one per column.** The five working days were a constant
in the engine; they are `health.stale_after` now, applied in `Env.Patience()`
alone so the number the report prints and the number it judged by cannot come
apart.

**Twenty-five mutants, three survivors, all three real.** A rule written as
two clauses is only tested by a fixture the clauses disagree about —
`ChangesRequested() || len(Approvals()) == 0` needed a *second* reviewer,
because `Approvals()` already drops one the same person overtook. The forge
fixture's merged pull request carried no mergeability, so the rule that drops
it for finished work could be deleted with the suite green. And GitHub's
`mergeable == nil` was load-bearing in a way the fake never showed: without
it, a state flow has no name for would have been read through a null pointer.


### A promise in the README that nothing kept (issue #167)

The README's bootstrap says "auto-merge on green gate". Spec 02 §2 reasons
about `check pr` exiting 0 so as not to stop auto-merge from seeing the red
tick it waits for, and spec 09 §1 puts the status out first because auto-merge
waits on it. Nothing implemented any of it. On #166 the gate went red, the
approval came at 02:15:32 and the status was green at 02:16:01 — twenty-nine
seconds, and then nothing, because nobody was listening. Four pull requests
sat approved and green at once.

**The gate arms what it has just judged, last.** After the status, the comment
and the transitions, because an armed pull request whose gate is already
passing merges on the spot: arming first would race flow against itself and
aim the remaining writes at a pull request the forge had closed. Nothing about
the arming can fail a gate. A forge that cannot do it, a repository with the
setting off, a draft, a credential that may not merge — each means this pull
request waits for a person, which is what it would have done anyway.

**A field flow does not ask for is a field flow would write `false` into.**
The merge settings are written whole, so `forge.auto_merge` had to be asked
for additively: GitHub's payload leaves the field out altogether, and Gitea
and GitLab, which have no such setting and let any pull request be armed, are
observed as permitting it rather than as refusing something they have never
refused anybody. Issue #169 is the same shape with nothing in front of it —
`delete_branch_on_merge` is in no desired item and is set to `false` by any
write that happens for some other reason.

**Three endpoints, one contract case.** GitHub has a mutation of its own and
answers the pending request in the round trip that fetches the node id, so it
never arms what is armed. Gitea has only the merge endpoint told to wait, and
shows the arrangement on no endpoint at all, so the second arming is forgiven
from its refusal rather than avoided. GitLab, asked to wait with nothing to
wait for, merges on the spot — so flow refuses to arm a project that does not
require its pipeline to pass.

**Twenty-seven mutants, five survivors, and one of them was a line that could
not be wrong.** Two were missing tests: the plan never asked a forge what it
already permitted, and nothing followed the field as far as the write. One was
a fake that accepted any merge method and remembered none — a promise made
with the wrong strategy lands a commit of the wrong shape hours after the last
person looked. And GitLab's newer `auto_merge` spelling reaches no
installation the deprecated one does not, for as long as flow speaks
`/api/v4`, so the write sends the old name and only the reading takes either.


### The workflow nobody had ever run (issues #186, #188, #189)

A greenfield repository, `17twenty/flow-demo-web`, was initialised with `flow
init` and had `flow apply` set its board up. Then its gate was asked to judge
its first pull request, and it could not take a single step.

**The template installed a version nobody had tagged.** `FLOW_VERSION:
v0.0.0`, written before any release existed and never looked at again, so
every job died at `go install`. This repository could not have told you: its
own workflow builds `./bin/flow` from the checkout, deliberately, so that a
change to the gate is judged by the gate it changes — and it therefore
installs no release at all. A build that knows its release now pins to it, and
a build with no release behind it leaves the `latest` spec 16 guarantees. The
pin is applied to the whole template tree, not the one file, because a
workflow reaches a repository two ways: `init` copies it in, and the
`forge.files` proposal carries the same bytes for `apply` to write when it is
missing.

**Then it ran flow outside a repository.** With the install fixed, the next
line of the log said so. The `gate` and `deploy` jobs had `setup-go`, an
install and nothing else; every flow command reads `.flow.yaml` out of the
working tree. The same blindness, twice: a repository whose every job has the
checkout for its own reasons cannot notice that it never asked for one.

**And the board had two of the same column.** A new GitHub project ships with
`Todo`, `In Progress` and `Done`. `flow apply` matched `Done`, and created `In
progress` one space-bar away from the option already there. The two halves of
flow already disagreed about this — a move resolves a column with `EqualFold`,
an observation finds one with `==` — so flow will happily move to a column it
has just made a duplicate of. It showed on the first real ticket: GitHub's own
automation put the work in `In Progress`, a column flow has never heard of,
and flow made no move at all. Whether two names are the same name turns out to
be the provider's question about that set, not one the engine can answer for
labels, file paths and status checks alike. That is issue #189, and it is the
next thing to build.

**What the greenfield test is actually for.** Three defects in an hour, none
of them reachable from this repository, all of them on the path every adopter
walks and this one never does. The demo now has a green `flow/policy` of its
own.

### The gate that could not land anything, and the line that lied (issues #190, #192)

Five pull requests were approved and green last week and every one of them
waited for somebody to press merge, although `flow check pr` has armed
auto-merge since #167 and this repository asked for it in #180.

**Arming a merge is a write to the trunk.** GitHub refuses
`enablePullRequestAutoMerge` to a job holding `contents: read`, which is what
every job of the gate workflow has held since it was written — here, in the
`dogfood/` copy, and in the template every adopter installs. The permission
is now the gate job's and no other job's: a job's `permissions:` block
replaces the file's whole set, which is what makes a grant that narrow
possible. A credential of its own was considered and refused, because a
feature that works only for people who first made a personal access token is
a feature that is off by default for everyone who did not read the manual.
The proof is the pull request that carried the fix: a `pull_request` run uses
the workflow from the head, so #191 armed itself.

**And the gate had been saying so, of the wrong thing.** For weeks every run
here printed `status not posted: this credential may not write here` three
seconds after posting the status. `Gate.ReadOnly` was one flag for four
writes, arming is the last of them, and its refusal reached back and
relabelled the first. The one refusal anybody could have acted on — arming —
was meanwhile the only outcome with no line at all.

Underneath both: a decision and an act are two facts, and the gate kept one
field for them. It printed a ticket moving whenever it worked out that one
should, so a refusal, a `--dry-run` and a card that really moved were
indistinguishable in the output. Every write now carries its own outcome.
Three callers were also re-asking a question `Transition` had already
answered — whether the column exists — and skipping the write in silence when
the answer was no; unreachable code whose only possible effect was the defect
above.

**Single best next step:** issue #189, the column flow duplicates. After it,
the four `flow attest` commands in `docs/cookbook.md` §6.1 and admin for the
machine user on the demo repository, which are the two things a person holds.
