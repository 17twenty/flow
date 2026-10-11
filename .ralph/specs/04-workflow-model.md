# 04 — Workflow Model

Human description: `assets/docs/lean-kanban-workflow.md`.

## 1. One delivery workflow

Every ticket type (story, task, bug) uses the same columns. Urgency = the
expedite label, never a separate board. flow does not configure product/epic
boards; it only reads parent links for DoR.

## 2. Columns

`{id, name, category, gate?, wip?}`; mapped to tracker statuses (GitHub Projects
Status options, Jira statuses, Linear workflow states) and resolved into the lock.

## 3. Linkage: PR ↔ tickets (P12 — no user regex)

`Tracker.LinkedKeys(pr)` returns ticket keys using structured data first:

| Tracker | Primary (structured) | Fallback (tracker's own documented convention, fixed in adapter) |
|---|---|---|
| GitHub Projects | PR `closingIssuesReferences` (GraphQL) — set by closing keywords in the PR body or the PR's "Development" sidebar link | none: unlinked PR = no ticket |
| Jira Cloud | — (dev-info API is not public) | Jira issue key in branch name or PR title — the convention Jira itself uses to link development work |
| Linear | Linear attachment for the PR URL (created by Linear's Git integration) | Linear identifier in branch name or PR title — Linear's own convention |

Rules:
- The fallback key format is the tracker's format (`[A-Z][A-Z0-9]+-\d+` for
  Jira/Linear) compiled once in the adapter; it is not configurable.
- Keys are validated by fetching the ticket; unknown keys are reported, not trusted.
- For GitHub Projects, a linked issue must also be an item in the configured
  project (else rule `pr.ticket.in_project` fails with fix "add the issue to the project").

## 4. Event vocabulary

| Event | Source |
|---|---|
| `pr_opened_draft` | PR opened as draft |
| `pr_opened` | PR opened ready for review |
| `pr_ready_for_review` | draft → ready |
| `pr_converted_to_draft` | ready → draft |
| `changes_requested` | review with CHANGES_REQUESTED |
| `approved` | required approvals met |
| `pr_merged` | merged |
| `pr_closed_unmerged` | closed without merge |
| `issue_closed` | linked ticket closed (by merge with closing keyword, or manually) |
| `deployed` | deployment record success for env, SHA ⊒ merge SHA (spec 13) |

`pr_opened` is treated as `pr_opened_draft` followed by `pr_ready_for_review`
(so both transitions apply in order).

## 5. Transition ownership (P11)

`workflow.transitions.<event> = {to, owner}`.
- `owner: native` — the tracker's automation does it. flow configures it during
  apply when the API allows (capability `native_transition_<event>`), otherwise
  files a follow-up or requires attestation; doctor verifies it actually happens
  (live check `transitions.native.<event>`, spec 07 §6).
- `owner: flow` — performed by `flow check pr` (PR events) or
  `flow check deploy` (deployment events) in CI, and caught up by scheduled
  `flow doctor --fix` for anything missed (e.g. a CI outage).

Engine rules for flow-owned transitions:
1. Forward-only by column order, except `changes_requested` / `pr_converted_to_draft`
   which may move back to `in_progress`.
2. Never move out of `done`.
3. Skip tickets whose status isn't a configured column (warn).
4. Multiple PRs per ticket: `in_review` when any linked PR is ready; `done`
   only when all linked PRs are merged (and deployed if `done_on: deployed`).
5. Idempotent: read current status first; skip if at/after target.
6. No transition comments (noise). Transitions are visible in tracker history.

GitHub Projects defaults (research): a new project enables "Item closed → Done"
and "Pull request merged → Done" automatically. Hence the lean-kanban preset
assigns `pr_merged` and `issue_closed` to `native`, and assigns the PR-opened
transitions (which GitHub's built-ins don't offer) to `flow`.

## 6. Done: merged vs deployed

- `done_on: merged` (default). Done means merged. Deployment is a **verified
  guarantee**, not a status: doctor's `deploys.lag` check proves every merged PR
  reached `deploys.environment` within `deploys.max_lag`. Works with native
  tracker automation only. Recommended when every merge deploys automatically.
- `done_on: deployed`. Done means running in the environment. Requires
  `transitions.deployed.owner: flow` AND that native automation does NOT set Done
  on merge/close (apply disables or follow-ups them; doctor verifies).
  With GitHub closing keywords the issue still closes on merge — in this mode
  the native "Item closed" workflow must target `in_review` (or be disabled), and
  flow moves the item to Done on deployment.

## 7. Definition of Ready

Rules (pure functions over `core.Ticket`):

| Rule | Pass condition |
|---|---|
| `acceptance_criteria` | Body contains a heading "Acceptance criteria" (case-insensitive, any level) followed by ≥1 non-empty line/list item. The issue form template (spec 17) guarantees this heading. |
| `parent_link` | Ticket has a parent (GitHub sub-issue parent; Jira parent/epic; Linear parent or project). |
| `not_blocked` | Not labelled/flagged blocked. |
| `flag_if_user_facing` | Label `user-facing` ⇒ label `flag:<name>` present. |
| `design_if_ui` | Label `ui` ⇒ a link under a "Design" heading. |
| `max_size: <dur>` | If the tracker has a size/estimate field: within limit. |

DoR is evaluated by `check pr` (rule `ticket.ready`) and in doctor live health.

## 8. WIP

Counts from the tracker (column membership), cross-checked with forge state
for `in_review` (open non-draft PRs linked to tickets); the max is used.
Enforced by policy rules `wip.*` (spec 09); never blocks an approved PR.
- **§7 `max_size` is a key, not a name in `rules` (item 2.12).** Spec §7's
  table lists it among the rules, but it needs a duration and the other five
  need nothing, so the configuration models it as
  `definition_of_ready.max_size` beside `rules`. It is on exactly when the
  limit is greater than zero, and a disabled rule does not appear in the
  verdict at all rather than appearing as a skip.
- **§7 a rule that cannot apply is satisfied, not failed (item 2.12).** A
  ticket that is not labelled `user-facing` has no opinion about feature flags;
  a tracker with no estimate field cannot answer `max_size`. Flow does not hold
  work up for a question nobody asked. The verdict records the skip so the
  difference between "it passed" and "it did not come up" stays visible.
- **§7 the trigger labels are fixed, not configured (item 2.12).**
  `user-facing` and `ui` are named by this spec, and flow creates both as part
  of the desired state. A rule whose trigger label could be renamed per
  repository is a rule nobody can reason about across repositories — and a team
  could never switch the rule on, because the label it reads would not exist
  yet. The *blocked* label is the exception and stays configurable, because
  `workflow.blocked.label` already names it for the rest of the workflow.
- **§7 a heading is matched at any level, without regard to case, ignoring
  trailing `#` and `:` (item 2.12).** "## Acceptance Criteria:" and "###
  acceptance criteria" are the same heading to everybody except a parser.
  `#hashtag` is not a heading and neither is seven hashes.
- **§7 an empty heading fails, and says so differently (item 2.12).** The issue
  form flow proposes puts the "Acceptance criteria" heading in for you, so a
  heading with nothing under it means somebody accepted the template and wrote
  nothing — exactly the ticket the rule exists to catch. "You have no heading"
  and "you left it empty" are different mistakes, and the person fixing the
  ticket is told which they made. The next heading of any level ends the
  section.
- **§7 all three markdown link spellings count under "Design" (item 2.12).** A
  bare URL, `[text](url)` and `[text][1]`. Flow is not a markdown linter, and a
  reference whose definition is missing is a rendering mistake rather than a
  missing design.
- **§7 a rule this build does not know fails closed (item 2.12).** Configuration
  validation rejects an unknown rule, so reaching the engine with one means a
  binary older than the file. Ignoring it silently would let work through a
  gate the team believes is closed, so the verdict refuses and names the
  problem as flow's own rather than the ticket's.
- **§7 no rules means no Definition of Ready (item 2.12).** Every ticket is
  ready. A team that has not agreed what ready means should not have flow
  inventing one for them.
