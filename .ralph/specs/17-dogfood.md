# 17 — Dogfooding: flow Is Built With flow

The flow repository is the reference installation and the final acceptance test.
Everything here is "an opinionated demo": the README links to the live Project,
the pinned health issue, and recent PRs as proof.

## 1. Topology

| Thing | Value |
|---|---|
| Repo | `github.com/17twenty/flow` (public) |
| Tracker | GitHub Project v2, **user-owned** by `17twenty`, named "flow", Board layout |
| Columns (Status options) | Backlog · Ready · In progress · In review · Done (GitHub's Kanban template defaults) |
| Linkage | PR body `Closes #N` (or Development sidebar) → `closingIssuesReferences` |
| Gate | `flow/policy` required status, plus the build's own `test` from `forge.required_checks`, via ruleset on `main` |
| Merge | squash only; commit title = PR title; commit message = PR body |
| Done | `done_on: merged` (native "PR merged → Done", "Item closed → Done") |
| Deploy signal | GitHub Deployment `production` recorded by `release.yml` for every merge to `main` |
| Deploy SLO | `max_lag: 2h`, `max_age: 7d` |
| Builder | Ralph loop acting as a distinct identity (machine user `flow-bot-gh`) |
| Human | `@17twenty`: shapes issues, reviews and approves agent PRs, owns follow-ups |

The agent MUST use a different GitHub identity from the human: a PR author cannot
approve their own PR, so `agent.human_review` would otherwise be unsatisfiable.

## 2. Files (provided in `dogfood/`, copied to the repo root)

- `.flow.yaml` — the configuration below.
- `.github/workflows/ci.yml` — fmt/vet/staticcheck/test/govulncheck/build.
- `.github/workflows/flow.yml` — `flow check pr` on PR events; daily doctor;
  doctor on `.flow.*` changes.
- `.github/workflows/release.yml` — spec 16.
- `.github/ISSUE_TEMPLATE/story.yml`, `bug.yml`, `config.yml` — issue forms with
  required "Acceptance criteria"; blank issues disabled.
- `.github/pull_request_template.md` — conventional title reminder, `Closes #`.
- `bootstrap/seed-issues.sh` — creates phase parent issues and one sub-issue per
  plan item from `IMPLEMENTATION_PLAN.md`, adds them to the Project in Ready /
  Backlog, writes the issue numbers back into the plan.

## 3. Project configuration (manual, attested once, then verified live)

Built-in workflows (Project → Workflows):
- Item added to project → **Backlog**
- Auto-add to project: `is:issue` from `17twenty/flow`
- Item reopened → **In progress**
- Code changes requested → **In progress**
- Pull request merged → **Done** (default on)
- Item closed → **Done** (default on)
- Auto-archive items: `is:closed updated:<@today-14d`

Transitions flow owns (no built-in exists): `pr_opened_draft → In progress`,
`pr_ready_for_review → In review`, performed by `flow check pr` in CI.

Views: "Board" (Status columns, column limits mirroring WIP for display),
"Expedite" (filter `label:incident`), "Agent" (filter `label:agent`).

## 4. `.flow.yaml` for the flow repo

```yaml
version: 1
preset: lean-kanban
forge:   { type: github, repo: 17twenty/flow, auth: { token_env: GITHUB_TOKEN } }
tracker:
  type: github-projects
  owner: 17twenty
  owner_type: user
  project: ${pending:project}     # resolved by flow doctor --fix (placeholders are never valid inside {…})
  auth: { token_env: FLOW_PROJECT_TOKEN }
workflow:
  done_on: merged
  columns:
    - { id: backlog,     name: Backlog,     category: todo }
    - { id: ready,       name: Ready,       category: todo, gate: definition_of_ready }
    - { id: in_progress, name: In progress, category: in_progress, wip: { per_person: 2 } }
    - { id: in_review,   name: In review,   category: in_progress, wip: { limit: 3 } }
    - { id: done,        name: Done,        category: done }
  transitions:
    pr_opened_draft:     { to: in_progress, owner: flow }
    pr_ready_for_review: { to: in_review,   owner: flow }
    pr_merged:           { to: done,        owner: native }
    issue_closed:        { to: done,        owner: native }
  expedite: { labels: [incident], max: 1 }
  blocked:  { label: blocked, require_comment: true }
definition_of_ready: { rules: [acceptance_criteria, parent_link, not_blocked] }
commits:
  convention: conventional
  types: [feat, fix, perf, refactor, test, docs, build, ci, chore, revert, style]
  header_max: 72
  merge_strategy: squash
  scopes: { required: true, allowed: [cli, core, config, git, commits, engine, forge, tracker, deploys, ui, assets, ci, docs, ralph, deps] }
  unlinked_types: [ci, build, docs, chore]
deploys: { source: forge_deployments, environment: production, max_lag: 2h, max_age: 7d }
policy:  { status_context: flow/policy, strictness: strict, comment: on_failure }
agents:  { authors: ['flow-bot-gh'], labels: [agent], require_human_review: true, unreviewed_types: [chore] }
followups:
  labels: [flow-followup]
  owners: { default: '@17twenty', tracker_admin: '@17twenty', forge_admin: '@17twenty', platform: '@17twenty' }
```

## 5. Bootstrap stages (chicken-and-egg)

**Stage A — manual scaffolding (human, ~30 min, before any code).**
1. Create repo; enable Issues; disable merge commits/rebase; enable squash with
   PR title/body.
2. Create user Project "flow" from the Kanban template; set workflows per §3.
3. Create labels: `agent`, `blocked`, `incident`, `flow-followup`, `phase`.
4. Create the machine user `flow-bot-gh`; invite it as a repo collaborator with
   Write; add it to the Project as a WRITER. Its classic PAT (`repo`, `project`,
   `workflow`) is the loop's only credential. See `docs/cookbook.md`.
5. Store that PAT as repo secret `FLOW_PROJECT_TOKEN`.
6. Copy `dogfood/*` into the repo (except `flow.yml`, which needs the binary —
   add it in Stage B). Commit to `main` (unprotected at this point).
7. Run `bootstrap/seed-issues.sh 17twenty/flow <project-number>`.

**Stage A loop mode (Phases 0–3): direct-to-main.** The loop commits to `main`
with conventional messages and `Closes #N` footers referencing the seeded issue
(native linkage closes the issue → Done). Ruleset not yet active.

**Stage B — activation (after plan item 3.x "check pr works").** Human runs:
```
go install github.com/17twenty/flow@main
flow init --tracker github-projects --owner 17twenty --project <n> --force   # must reproduce §4 exactly
flow plan        # expect: native = ruleset + merge settings; attest = built-in workflows; nothing else
flow apply       # creates ruleset requiring flow/policy + test (forge.required_checks)
flow attest tracker.native.pr_merged    --note "Project workflow on"   # etc. for each native transition
```
Then adds `.github/workflows/flow.yml` via a PR — which is itself gated by
`flow/policy`. From here the loop switches to **PR mode** (§6).

**Stage C — proof.** `flow doctor` daily run on the repo. v1 is DONE when the
pinned "flow health" issue shows exit 0 for 14 consecutive daily runs while the
loop keeps shipping.

## 6. Loop PR mode (Stage B onwards)

Each iteration, as the agent identity:
1. If an open agent PR has `changes requested`: address review comments on that
   branch, push, stop.
2. If an open agent PR awaits review: stop (WIP: one agent PR in review at a time;
   `loop.sh` waits for merge or changes requested before the next iteration).
3. Else pick the lowest-numbered open issue labelled `agent` in status Ready
   within the current phase (`gh project item-list` / GraphQL).
4. `gh issue develop <N> --checkout` (creates a branch linked to the issue).
5. Implement + backpressure (PROMPT.md §4).
6. Commit(s) with conventional messages; push.
7. `gh pr create --draft --title "<type>(<scope>): <description>" --body
   "<summary>\n\nCloses #<N>" --label agent` → `flow check pr` moves the item to
   In progress; when CI is green, `gh pr ready` → In review.
8. `gh pr merge --auto --squash`. The human reviews and approves; merge happens
   automatically when `flow/policy` and `test` pass.

## 7. What the demo proves (live-health expectations on the flow repo)

| Check | Expected |
|---|---|
| `live.prs.gated` | 100% of merged PRs had `flow/policy` success at merge |
| `live.prs.linked` | 100% (except ci/build/docs types) |
| `live.prs.reviewed` | 100% of agent PRs approved by `@17twenty` |
| `live.deploys.lag` | 100% within 2h |
| `live.transitions.native` | merged/closed → Done within 15 min |
| `live.tickets.review_has_pr` | 100% |
| `live.tickets.ready_dor` | 100% (issue forms guarantee AC) |

## Decisions

- **§1 builder identity: classic PAT for now, App deferred.** The topology table
  names a GitHub App bot `17twenty-flow-agent[bot]` as the builder, and §1 states
  the agent MUST use a different identity from the human. During v1 construction
  the agent authenticates with a classic PAT belonging to `@17twenty` instead.
  The reason is a product requirement, not convenience: flow must work across
  forges, and a GitHub App is a GitHub-shaped answer that GitLab and others do
  not share, so building against one would bake a GitHub assumption into the
  adapter boundary that spec 10 exists to keep thin. A PAT is the lowest common
  denominator every forge offers.

  This has a consequence that is **not yet resolved**: a PR opened with
  `@17twenty`'s credential is authored by `@17twenty`, who cannot approve it, so
  `agent.human_review` and `live.prs.reviewed` become unsatisfiable the moment
  the loop switches to `pr` mode (§5 Stage B). Until a second identity exists,
  Stage B and the Stage C acceptance run (`flow doctor` green for 14 days) cannot
  both hold. Git *authorship* is independent of the push credential, so commits
  continue to be authored as `17twenty-flow-agent[bot]`; only API-created objects
  carry the human's identity. Revisit before Stage B: either a second account, a
  machine user, or an App used solely as a credential source for the GitHub
  adapter while the adapter interface stays forge-neutral.

- **§2 `seed-issues.sh` drops the `gh` dependency.** The script was written
  against `gh project` and `gh issue`; the loop host has neither. It now uses
  `curl` + `jq` against REST for issues and GraphQL for Projects v2, with
  `GH_TOKEN` supplied by the environment. Bootstrap should need no more than the
  tools a CI image already carries, and `gh project` is itself a thin wrapper
  over the same GraphQL calls.
- **§5 Stage A seeds finished work closed.** The script originally matched only
  unticked (`- [ ]`) plan lines, so items completed before seeding would never
  appear on the board and the Project would misrepresent progress from its first
  day. It now seeds ticked items too and closes them immediately, letting the
  built-in "Item closed → Done" workflow place them. The board therefore shows
  8 items already in Done, which is the truth.
- **§3 the auto-add workflow races issue creation.** With "Auto-add to project"
  enabled, an issue can land in the Project before the script's explicit
  `addProjectV2ItemById`, which then fails `UNPROCESSABLE: Content already
  exists`. This killed the first seeding run part-way. The script now looks for
  an existing item first, adds only if absent, and re-queries if the add loses
  the race, so either ordering converges on exactly one item.
- **§5 seeding is resumable.** Plan markers are written through after every item
  rather than once at the end, and an item's identity is the `Plan item <id>.`
  line in its issue body rather than its title. A run that dies part-way is
  therefore re-runnable without duplicating: the interrupted first run left
  issue #32 unmarked, and the re-run adopted it instead of seeding a second copy.


- **§1 a deploy key is the push credential; it is not a second identity.** A
  repository deploy key (write-enabled) now authenticates every push, so the
  agent no longer pushes with the human's personal account key and the PAT no
  longer needs `contents:write`. It is scoped to this one repository, which is
  what spec 15 wants of a credential a loop holds.

  It does **not** close the Stage B blocker. A deploy key is an SSH transport
  credential: it cannot call REST or GraphQL, so it cannot open a pull
  request, and the pull request therefore still carries the identity of
  whatever token created it — `@17twenty`. The three ways out named above (a
  second account, a machine user, an App used only as a credential source)
  are still the only ways out.

  Worth recording because it is not obvious: dropping the `agent` label to
  dodge the check is not a fourth way out. §09's `agent.human_review` fires
  when the author is in `agents.authors` **or** a label is in
  `agents.labels`, so an unlabelled PR authored by `@17twenty` is simply not
  an agent PR, and the check passes while demonstrating nothing. A vacuous
  green is worse than a red one for a repository whose purpose is to be the
  proof.
- **§5 Stage B is deferred; the loop stays in direct mode for the rest of v1
  construction.** Phases 4–6 are built committing to `main`, and the switch
  to PR mode happens once, at the end, immediately before the Stage C
  acceptance run. The cost is accepted knowingly: `flow check pr` is
  exercised against its own tests and fixtures rather than against live agent
  pull requests until late, and the 14-day Stage C window cannot begin until
  the identity question is settled. The alternative — standing up a second
  identity now — was weighed and declined for v1 construction.

  Consequence for whoever picks this up: the identity decision is still owed,
  and it is owed *before* Stage C, not before Stage B. `agent.human_review`
  and `live.prs.reviewed` remain unsatisfiable until it is made.

- **§1 the identity question is settled: the builder is the machine user
  `flow-bot-gh`.** This supersedes the unresolved consequence of the two
  Decisions above. A second GitHub account now exists, is a repo collaborator
  with Write and a WRITER on the Project, and holds one classic PAT (`repo`,
  `project`, `workflow`). The agent authors commits and pull requests as that
  account; `@17twenty` reviews and approves them. `agent.human_review` and
  `live.prs.reviewed` are therefore satisfiable, and `agents.authors` in §4
  becomes `['flow-bot-gh']`.

  The forge-neutrality argument that deferred the GitHub App still stands and
  is what rules the App out permanently for v1: a machine user with a token is
  the lowest common denominator — GitHub, GitLab, Gitea and Bitbucket all have
  it — whereas an App is GitHub-shaped. Nothing about this choice reaches the
  adapter boundary: `forge.github` still sees a token in an env var.

- **§1 the machine user gets no SSH key; the token is the push credential.**
  The deploy key used during Phases 0–3 is revoked. A deploy key is bound to
  one repository and cannot call an API, so it could push but never open the
  pull request that Stage B is about — two credentials for one identity, one
  of which did half the job. The same token now authenticates git-over-HTTPS
  and REST/GraphQL alike, which is the shape a forge-neutral agent needs:
  `https://x-access-token:$TOKEN@host/owner/repo` works unchanged on GitLab
  and Gitea.

  Gotcha recorded because it cost real time twice, in two different forms:
  **on this host, SSH wins by default and does it silently.** `~/.ssh/config`
  sets `IdentityFile` for `github.com`, which beats `IdentitiesOnly=yes`
  (hence the `-F /dev/null` the deploy key needed), and `git config --global`
  carries `url.git@github.com:.insteadOf https://github.com/`, which rewrites
  an HTTPS remote back to SSH *after* the credential helper has been chosen.
  Both failure modes push successfully as the wrong identity and report
  nothing. The repo therefore pins `origin` to
  `https://x-access-token@github.com/17twenty/flow.git` — the userinfo prefix
  means `insteadOf` no longer matches — and sets `core.sshCommand` to a
  command that exits non-zero, so an accidental SSH push fails loudly instead
  of impersonating the human. Verify the credential rather than trusting it:
  compare `git credential fill` output against the token file.

- **§5 Stage B is unblocked, and waits only on `flow check pr`.** The previous
  Decision deferred Stage B to immediately before Stage C because no second
  identity existed. That reason is gone. The remaining preconditions are the
  ones the spec always named: plan item 4.3 (`flow check pr`) and the ruleset
  created by `flow apply`. The loop stays in `direct` mode until 4.3 lands and
  switches to `pr` mode then, which restores the original intent — agent pull
  requests exercised against live reviews for the whole of Phases 5–6, rather
  than for the few days before the acceptance window.

- **§3 the Project's collaborator list is separate from the repository's.**
  Adding `flow-bot-gh` to the repo granted it nothing on the user-owned
  Project: `projectV2(number: 2)` resolved to `null` for its token, which
  reads as "no such project" rather than "not allowed" and is easy to
  misdiagnose as a wrong project number. User-owned Projects v2 carry their
  own access list, set with the `updateProjectV2Collaborators` mutation (or
  Project → Settings → Manage access). This is a Stage A step; `docs/cookbook.md`
  carries the recipe.
- **§5 the first gated pull request is the one carrying the gate (item H.1).**
  Stage B's ordering looks circular — `main` requires `flow/policy`, and
  nothing produces `flow/policy` until `.github/workflows/flow.yml` is on the
  trunk — but it is not. A `pull_request` workflow runs from the pull
  request's own branch, so the file that adds the gate runs on the change that
  adds it, posts the status, and is merged by it. Nothing has to be waved
  through, and the repository is never in a state where a commit reached the
  trunk ungated.
- **§5 flow's own workflow builds flow, it does not install it (item H.1).**
  The shipped template does `go install github.com/17twenty/flow@<version>`,
  which is right for a team adopting flow and wrong for flow: a pull request
  would then be judged by a release made before it, and a change to the gate
  could not be tested by the gate. Here every job builds `./bin/flow` from the
  checkout, and the toolchain comes from `go-version-file: go.mod` rather than
  `stable`, for the reason CI already learned once — an unpinned toolchain
  turns every push red with no change to the repository.
- **§5 the repository secret was the missing half of Stage A step 5 (item
  H.1).** `FLOW_PROJECT_TOKEN` had never been stored, so the gate would have
  had a forge and no tracker and could not have judged `ticket.ready` at all.
  It is the bot's own PAT, the same one credential the loop uses everywhere
  else; a gate that reads the board needs the board's token wherever it runs.
- **§7 the README states the streak and the health issue holds it (item
  8.2).** A number in a README is a claim somebody has to remember to update,
  and the one thing this repository is for is not needing to be remembered. So
  the demo section states the streak *with the date it was read* and says in
  the same breath that the issue is the record — a dated claim is honestly
  stale, where an undated one is just wrong. Nothing generates the line:
  flow writes tickets and statuses, not the README of the repository it is
  configured for, and a `doctor` that edited the working tree would be a
  different and much larger promise.

  What is checked mechanically is the part that can rot without anybody
  noticing: every forge link in the section is read against `.flow.yaml` and
  the board number resolved in `.flow.lock`, so a flow pointed at another
  repository or another board fails the test that says the demo is of this
  one. The streak line itself is required to exist and to carry a date.

- **The required context is `test`, not `ci / test`** (item 7.0). A GitHub
  ruleset matches a check by the name the check run goes by, which for GitHub
  Actions is the job's name; `ci / test` is the label the web UI composes from
  the workflow and the job, and no API ever reports it. This repository's
  check runs are `test`, `policy`, `setup checks` and `daily health`, so
  `forge.required_checks: [test]` is what asks for the build. Naming the UI's
  label would have required a check nothing publishes — a trunk nobody could
  merge to.

- **§6 the loop's knowledge ships as a skill, and the skill knows no
  repository (item 8.3).** §6 is written as flow's own loop — `gh`, the agent
  label, this Project — and an agent arriving at somebody else's flow
  repository needs the same seven steps without any of those particulars.
  `skills/flow/SKILL.md` is that: verify, take a ticket, branch, commit, open a
  pull request that links its ticket, wait for the gate and for a person. It
  asserts nothing about a board it has not read. Every rule it follows — which
  column is takeable, what makes a ticket ready, which commit types and scopes
  are allowed, what the status is called — it reads out of the `.flow.yaml` in
  front of it, because a skill carrying flow's defaults as facts would be
  wrong in every repository that configured something else.

- **§6 the skill is held to flow's surface by tests, not by review (item
  8.3).** It is documentation that tells a machine what to run, which is the
  same promise spec 02 §2 makes about the shipped templates and the same way
  of breaking it: a command that was renamed, a flag that was never added, a
  configuration key that does not exist. So each `flow …` the skill writes in
  code must name a command that answers `--help`, each flag beside it must
  appear in that command's help, and each dotted name in its prose must be a
  rule `PolicyRuleIDs()` publishes or a key in the configuration schema. A
  skill nobody tests rots silently, and it rots into an agent confidently
  running nothing.

- **§6 the skill tells an agent to wait, and never how to clear
  `agent.human_review` (item 8.3).** It is the one rule in spec 09 §2 whose
  subject is the reader, and an agent that treats a red gate as an obstacle
  will find two ways past it — approving its own pull request, or dropping the
  agent label so the rule stops applying and the check passes proving nothing.
  Both are named in the skill as things never to do, with the reason, because
  a prohibition without its reason is a puzzle.

- **§6 a skill is assessed by running it, not by reading it (item 8.3).** The
  acceptance criterion was that a harness given the skill and nothing else
  could work a ticket; it was met by doing exactly that, and the run is the
  item's evidence. Four defects came out of one pass, and the one that
  mattered most — a `flow doctor --json` reading whose field names do not
  exist, so that a repository with a failing check reports nothing failing —
  is invisible to a reviewer and silent to the agent, which is the worst pair
  of properties a line of documentation can have. A skill is read by something
  with no judgement about whether what it is doing is absurd, so the standard
  is the one for code: run it, and keep the parts that can rot under test.

- **§5 the scheduled audit runs as the bot, not as the workflow (Stage C,
  revisited).** Step 5 stores the machine user's PAT as `FLOW_PROJECT_TOKEN`
  for the Project, and the daily `flow doctor` job hands it to the forge as
  well. Not for the writing — `--fix` never touches the forge — but for the
  reading: `secrets.GITHUB_TOKEN` may not be granted `administration`, so it
  cannot see the ruleset that Stage B switched on, and an audit of the gate
  that cannot see the gate is the one thing §7's fourteen green runs must not
  be able to mean. The first scheduled runs failed this way and said so
  loudly, which is the behaviour to keep.

- **A transition this repository does not perform is not in this repository's
  configuration (§4, revisited).** §4 named `changes_requested` with
  `owner: native`, and `flow doctor` therefore asked a person to vouch for the
  Projects built-in "Code changes requested". GraphQL lists a project's
  configured workflows by name with an `enabled` flag, and on Project 2 that
  one is not among them: seven are on, and it is not one of the seven. The
  attestation would have been a false statement, and `live.transitions.native
  .changes_requested` had never caught it because no reviewer in any window
  had asked for changes. The transition is removed from §4 rather than
  attested. The presets keep it — a team whose board does perform it should
  say so — but what this repository claims is now only what its board does.
  (The alternative, `owner: flow`, is what `docs/delivery-board.md` proposes
  and is a change of behaviour, not a correction; it is not made here.)
- **§4 is this repository's configuration, not `flow init`'s output
  (revisited).** It has diverged from the `lean-kanban` preset three times
  now — `chore` is an unlinked type, `agents.unreviewed_types` is set, and
  `changes_requested` is gone — each for a reason recorded here. §4 tracks the
  file; the acceptance criterion about reproducing it with `flow init --force`
  is about the shape `init` writes, not about a file a live repository has
  since edited.
