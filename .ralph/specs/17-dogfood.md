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
| Gate | `flow/policy` required status (plus `ci / test`) via ruleset on `main` |
| Merge | squash only; commit title = PR title; commit message = PR body |
| Done | `done_on: merged` (native "PR merged → Done", "Item closed → Done") |
| Deploy signal | GitHub Deployment `production` recorded by `release.yml` for every merge to `main` |
| Deploy SLO | `max_lag: 2h`, `max_age: 7d` |
| Builder | Ralph loop acting as a distinct identity (GitHub App bot `17twenty-flow-agent[bot]`) |
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
    changes_requested:   { to: in_progress, owner: native }
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
  unlinked_types: [ci, build, docs]
deploys: { source: forge_deployments, environment: production, max_lag: 2h, max_age: 7d }
policy:  { status_context: flow/policy, strictness: strict, comment: on_failure }
agents:  { authors: ['17twenty-flow-agent[bot]'], labels: [agent], require_human_review: true }
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
4. Create the GitHub App `17twenty-flow-agent` (contents RW, pull requests RW,
   issues RW, metadata R), install on the repo; store its credentials where the
   loop host can mint installation tokens.
5. Create classic PAT with `project` scope → repo secret `FLOW_PROJECT_TOKEN`.
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
flow apply       # creates ruleset requiring flow/policy + ci / test
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
   automatically when `flow/policy` and `ci / test` pass.

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
