# 09 — The Policy Gate (`flow check pr`)

## 1. Where it runs

A CI job on pull request events (GitHub: `pull_request` types opened, edited,
synchronize, reopened, ready_for_review, converted_to_draft; and
`pull_request_review` submitted). It reads the PR number from the event payload.
The template (`assets/templates/ci/github-flow.yml`) uses `pull_request`, never
`pull_request_target`, and checks out nothing from the PR (flow needs only APIs).
Fork PRs: token is read-only → flow prints the result and exits 0 without
posting; the maintainers' re-run (or scheduled doctor) catches up.

## 2. Snapshot → rules → status

Snapshot: PR (title, body, draft, author, labels, head SHA, reviews), linked
tickets (spec 04 §3) with status and DoR inputs, WIP counts, config, now.
Evaluation is pure and deterministic.

| id | default | Pass |
|---|---|---|
| `pr.ticket.linked` | error | ≥1 linked ticket, unless title type ∈ `commits.unlinked_types` |
| `pr.ticket.in_project` | error | (github-projects) every linked issue is a project item |
| `ticket.ready` | error | every linked ticket passes DoR now, OR is already in in_progress/in_review (DoR was the entry gate; don't re-litigate edits) |
| `pr.title.conventional` | error | title passes `cc.header.format`, `cc.type.allowed`, `cc.scope.*`, `cc.header.length` |
| `pr.breaking.explained` | error | title has `!` ⇒ body has a "Breaking change" section |
| `agent.human_review` | error | agent-authored PR (author in `agents.authors` or label in `agents.labels`) has ≥1 approval from a non-agent |
| `wip.review` | warn | in_review count ≤ limit, or this PR is approved, or it entered review before the limit was exceeded |
| `wip.in_progress` | warn | author's in_progress count ≤ per_person |
| `ticket.not_blocked` | warn | no linked ticket blocked |

Strictness: relaxed (all → warn except `pr.ticket.linked`, `agent.human_review`),
standard (table), strict (warn → error). `policy.rules` overrides per id.

Status: `failure` if any error fails; else `success`. Draft PRs: `pending`
"draft — not enforced". Description ≤ 140 chars, e.g.
`✗ 2 errors — ticket.ready, pr.title.conventional`. Skip posting if the latest
flow status on the SHA already has the same state+description.

Comment: one PR comment (marker `flow:policy`) with a table of failing/warning
rules and exact fixes; edited in place; deleted when clean (`comment: on_failure`).

## 3. Transitions in the same run

After posting, apply flow-owned transitions for this PR's linked tickets per
spec 04 §5 (`--no-transitions` to skip).

## 4. Tests

Per-rule tables; strictness mapping; description truncation golden; comment
golden; determinism; fork/read-only path; draft path.
