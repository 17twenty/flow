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

## Decisions

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
- **§8 the board/forge reconciliation happens before the gate sees it (item
  2.13).** Spec §8 takes the counts from the tracker, cross-checks `in_review`
  against the forge and uses the larger. Both halves need the network, so the
  command does the reconciling and hands the gate one list per column. The
  gate's rules stay pure, which is what lets `flow check pr` and doctor's live
  audit agree (spec 09 decisions).
- **§4–5 the engine reads state, not events (item 2.14).** §4 names the events
  by their source — "PR opened as draft", "draft → ready" — which reads like a
  stream of webhooks. A payload's action is a claim about a moment that has
  already passed by the time flow sees it, and doctor's catch-up of missed
  transitions (spec 07 §6) has no payload at all. So the engine asks which
  event describes a pull request's state *now*, and looks that event up. It
  makes `flow check pr` in CI and doctor's catch-up literally the same
  function, which is the only way they cannot disagree about where a ticket
  belongs — the same argument the policy gate makes (spec 09 decisions).
- **§4 `pr_opened` is a source, not a transition anybody configures (item
  2.14).** §4 defines it as `pr_opened_draft` followed by
  `pr_ready_for_review`, so a `workflow.transitions.pr_opened` would be a key
  flow silently ignored. Configuration validation now refuses it and names the
  two to use instead. Both of the pair really are applied in order, so a
  workflow that configures only the first still starts the work.
- **§4 the events describe a pull request as it stands, not as it was (item
  2.14).** A pull request closed without merging never asks for the review
  column on its way out, because it is not in review and never will be again;
  replaying a history would strand the ticket wherever the last configured
  event in it pointed. The exception is §4's own: open and ready is the pair
  above.
- **§4 `approved` waits for somebody, even where the branch requires nobody
  (item 2.14).** "Required approvals met" is vacuously true on a repository
  that requires none, which would fire the event the moment a pull request
  opened. A repository requiring no approvals has not said nobody need look at
  the work — it has said it does not enforce it — so the event waits for at
  least one approval, which is what `approved` means to everybody who reads it.
- **§4 a draft is a conversion only when the ticket has gone past it (item
  2.14).** Nothing on a pull request records that it was once ready, and the
  two events differ only in being allowed to move work backwards. A draft pull
  request whose ticket is already past the column drafts belong in can only
  have come about one way, so the ticket's own position is the evidence — and
  a pull request opened as a draft is never reported as one that was put back.
- **§5 the pull request furthest along the board decides (item 2.14).** Rule 4
  says the review column when *any* linked pull request is ready, so each pull
  request is judged from the ticket's real column and the furthest-along
  answer wins. Folding them one after another would let a review asking for
  changes on the second pull request undo the first one being in review. The
  ones that lose are reported as overruled rather than dropped: they were not
  wrong about their own pull request, they are behind the work as a whole.
  Being already in the target column counts as an opinion — that is what holds
  a ticket in review.
- **§5 a back-move goes where the configuration sends it (item 2.14).** Rule 1
  says `changes_requested` and `pr_converted_to_draft` "may move back to
  `in_progress`". The engine decides *who* may go backwards; which column they
  land in is the team's, exactly as it is for every other event. A workflow
  that configures neither gets no back-moves, which is the honest reading of a
  team that did not ask for one.
- **§5 a pull request closed without merging is not unfinished work (item
  2.14).** Rule 4 holds Done until all linked pull requests are merged. An
  abandoned one never will be, and counting it would leave the ticket short of
  Done for ever. Done waits for the work that is still open.
- **§3 a linked ticket names its repository only when it is in another one
  (item 3.3).** `closingIssuesReferences` returns every reference with the
  repository it belongs to. A ticket in the repository being spoken for comes
  back as `#7`, which is how everybody writes it and what the tracker's own
  keys look like; a ticket anywhere else comes back as `owner/repo#7`, which is
  how GitHub itself writes a reference it has to qualify. Without the
  qualification `#7` there and `#7` here are the same string and different
  tickets. Repository names are compared case-insensitively, because GitHub's
  are.
- **§3 an adapter that cannot verify the link reports no tickets rather than
  none (item 3.3).** Linkage is a declared capability (`linked_issues_read`).
  When the declaration says flow cannot read it back — Gitea, at the time of
  writing — the adapter leaves the list empty and never asks, which is the
  state this section already has a fallback for. The alternative, asking
  anyway and reporting an empty answer, would be indistinguishable from a pull
  request that really closes nothing.
- **§6 the environment is matched without regard to case, and then reported by
  the configured name (item 4.4).** The name arrives from whatever started the
  run and the configured one was typed by a person, so `Production` and
  `production` are one environment. Having decided which environment a
  deployment is in, flow uses the workflow's own spelling for every question
  it asks afterwards and for what it reports, so a capital letter changes
  neither the outcome nor the document.
- **§6 the walk for merges is bounded by `deploys.max_lag` (item 4.4).** A
  deployment could in principle be running any merge in the repository's
  history. Walking all of it on every deployment would turn one CI job into a
  full audit, so the run looks back as far as the lag the team agreed to and
  no further: a merge that took longer than its own objective to reach the
  environment is something doctor reports and catches up (spec 07 §6). A
  workflow that set no objective gets no bound, because flow has no number of
  its own to put there.
- **§6 only tickets linked to a merge this deployment is running are
  considered (item 4.4).** The board holds work the deployment has nothing to
  do with, and asking the tracker about all of it would be the same full audit
  by another route. Open pull requests are still read, because §5 rule 4 needs
  to see a ticket's unfinished work before calling it done — but they are
  evidence about those tickets, not candidates of their own.
- **§5 `owner: flow` means flow performs it eventually, not only on the event
  (item 5.5).** The gate performs a transition when it is told about the
  event; `flow doctor --fix` performs it when nobody did. The decision is the
  same function over the same snapshot in both, so the two can never disagree
  about where a ticket belongs — the only difference is what prompted the
  question. That is what makes a missed CI run a delay rather than a hole in
  the board, and it is why rule 5's idempotence is load-bearing rather than
  merely tidy: two things now make these moves.
- **§5 rule 2 is about the work, not about the column: flow makes no move on a
  closed ticket except the one its closing asks for (found live).** Rule 2
  says flow never moves work out of done, and read as a rule about the column
  it has a hole in it, because the column is exactly the thing that can be
  wrong. `issue_closed` is `owner: native` in the preset, so when the
  tracker's automation does not fire — which is what the live check
  `transitions.native.issue_closed` exists to notice — the ticket stays in
  whatever column it was in while a pull request still names it. Opening a
  second pull request against it then asked for `pr_ready_for_review`, and
  flow, judging the move from the column, performed it. It happened to this
  repository: #72 was closed and merged at 00:59 and flow moved it back to In
  review at 01:15. So a closed ticket is refused every event but
  `issue_closed` (`ReasonClosed`), which still applies under the ordinary
  rules and so still reaches done where the workflow gives flow that event.
  The linked pull requests are considered and refused rather than skipped, so
  a dry run still says what it declined. The alternative — trusting the column
  — makes flow the thing that makes the board untrue, which is the one outcome
  the whole model exists to prevent.
- **§5 flow decides no move for an archived ticket (issue #99).** Archiving is
  a decision about the whole item rather than about one event, so it answers
  every event the same way, ahead of every other reason (`ReasonArchived`
  leads `heldOrder`). The tracker will not write to an archived item at all,
  so a move decided here would be a move flow reported and never made — the
  same failure as rule 2's, arriving from the other direction. Unlike a closed
  ticket, there is no event it still acts on: closing is finished work
  reaching its own column, whereas archiving is the board declining to show
  the work, and restoring it is a person's decision, not an event.
