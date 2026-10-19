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
| `agent.human_review` | error | agent-authored PR (author in `agents.authors` or label in `agents.labels`) has ≥1 approval from a non-agent, unless its landed type ∈ `agents.unreviewed_types` |
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

## Decisions

- **§2 the snapshot is given, never gathered (item 2.13).** Every rule is a
  pure function of `engine.PolicySnapshot`. Asking the forge for the pull
  request, the tracker for its tickets and the board for its counts is the
  command's work. The gate must give the same verdict on a laptop, in CI and
  in doctor's live audit, and a rule that could reach the network could
  disagree with itself between the three.
- **§2 a rule set to `off` is not evaluated and does not appear (item 2.13).**
  Off is the team saying they do not want the question asked. Reporting it as
  a pass, or as a skip, would be flow answering it anyway.
- **§2 an unknown strictness is read as standard (item 2.13).** Configuration
  validation rejects one, so reaching the engine with it means a binary older
  than the file. The baseline is the honest answer to "what would flow have
  done before this word existed". This is the opposite of spec 04 §7's unknown
  *rule*, which fails closed: a missing rule would open a gate the team
  believes is shut, whereas a missing strictness only changes how loudly the
  rules that do exist complain.
- **§2 the review and in-progress columns are the ones the transitions name
  (item 2.13).** `wip.review` reads the column `pr_ready_for_review` lands in
  and `wip.in_progress` the column `pr_opened_draft` lands in. Matching column
  ids called "in_review" and "in_progress" would be flow owning names the team
  owns; the events already say which column is which.
- **§2 "it entered review before the limit was exceeded" is a place in the
  arrival order (item 2.13).** The column's items are sorted by when they
  arrived, and a pull request is let off when all of its tickets are inside the
  first `limit` of them. A tracker that will not say when an item arrived gives
  no order, so the exemption does not apply at all: a pull request is let off
  on evidence or not at all.
- **§2 expedited work is outside the WIP rules entirely (item 2.13).** Items
  carrying a `workflow.expedite` label are not counted, and a pull request
  whose own ticket carries one skips both rules. Expedite is the team's agreed
  way past a limit (spec 04 §8); warning about it would be telling them off
  for following their own process.
- **§2 `ticket.ready` stops at the first in-progress column, and everything
  past it (item 2.13).** The table says "already in in_progress/in_review"; the
  rule is implemented as "at or past the first column of category
  `in_progress`", which also covers done. The Definition of Ready is the entry
  gate to starting work, and a ticket that has gone through it has gone through
  it. The snapshot carries the *configured* column id, resolved through the
  lock by the caller: the engine never maps a tracker column itself (spec 04 §2).
- **§2 a key the tracker cannot produce is evidence, not a ticket (item
  2.13).** Spec 04 §3 has unknown keys reported rather than trusted, so an
  unresolvable key does not satisfy `pr.ticket.linked`; it is listed underneath
  the failure so the author can see which key flow could not find.
- **§2 `pr.title.conventional` reports five of the linter's rules (item
  2.13).** `cc.header.format`, `cc.type.allowed`, `cc.scope.required`,
  `cc.scope.allowed` and `cc.header.length`, exactly as the table says. The
  description's case and full stop are style and belong to the optional commit
  hook; the body rules have no body to read on a pull request title; and
  `cc.breaking.explained` is its own policy rule.
- **§2 a draft is judged in full and reported as pending (item 2.13).** The
  rules still run and the comment still says what is wrong — that is what the
  author wants while they are still writing. Only the commit status softens, to
  `pending` and `draft — not enforced`. A red cross on work somebody is still
  thinking about teaches people to ignore red crosses.
- **§2 the description's count is never trimmed, only its list (item 2.13).**
  When the rule ids do not fit in 140 characters they are dropped from the end
  and `, +N more` is appended; the leading count stays true. A description
  saying "2 errors" when there were nine would be a lie told to save eleven
  characters. When even one id will not fit, the line is the count alone.
- **§2 `comment: never` removes the comment rather than leaving it (item
  2.13).** A team that turns comments off is saying they do not want flow
  writing on their pull requests. Honouring that only for new comments would
  leave the ones flow already wrote arguing with a status that has moved on.
  `on_failure` removes it when nothing is wrong, which is the same rule.
- **§2 the comment lists only what did not hold (item 2.13).** Somebody reading
  it has a pull request to fix. Nine rows with two crosses in them makes them
  hunt for the crosses.
- **§1 the gathering lives in the engine, not in the command (item 4.3).** The
  rules are pure and the `cmd_*.go` files hold no business logic, so the one
  thing left — taking the snapshot, publishing the verdict, moving the cards —
  is `internal/engine/gate.go`. It is the same shape as `Observe`/`Apply`:
  above the adapters, below the command, taking `engine.Adapters` so it never
  learns which providers it has.
- **§1 the order is status, then comment, then transitions (item 4.3).** The
  status is what branch protection and auto-merge wait on, so it goes out
  before anything slower can fail. A run that died writing a comment has still
  told the machinery what it needed to know.
- **§1 read-only is discovered by being refused, never predicted (item 4.3).**
  The forge is the only authority on what a credential may do. A gate that
  read a scope list would either skip a post it could have made or claim it
  was allowed and fail. One refusal is enough: having been refused the status,
  flow does not then ask the board, because that would turn one refusal into a
  transition that half happened.
- **§2 membership is read from the board listing, not asked a second time
  (item 4.3).** `pr.ticket.in_project` compares the linked ticket against every
  item of the project, because an issue the tracker will happily produce may
  still not be an item. Asking the tracker instead would mean trusting an empty
  column to mean "not on the board", and an item with no status set has an
  empty column too.
- **§2 the board gives no arrival order (item 4.3).** `WIPItem.Since` is left
  zero. No tracker flow speaks to records when an item entered a column, and
  filling it with the last-edited time would let an item somebody merely
  touched jump the queue.
- **§3 the pull requests consulted are the open ones, plus this one (item
  4.3).** Spec 04 §5 rule 4 wants all of a ticket's work considered, and open
  work is what a ticket is still waiting on. The gate never runs on a closed
  pull request, the merge is `check deploy`'s to react to, and walking every
  pull request the repository ever had would put an unbounded read in a CI job.
- **§3 a branch whose protection cannot be read asks for no approvals (item
  4.3).** The pull request's own approvals are then what counts. The reading
  that invents a requirement nobody configured is the one that blocks work for
  a reason no one can find.
- **§2 `pr.ticket.in_project` tells archived from absent (issue #99).** Both
  fail the rule, because the board shows neither and no transition can move
  either, and the rule is about what the board shows. What differs is the
  remedy, and it is the whole value of the finding: an issue nobody added has
  to be added, and one somebody archived has to be restored — adding it again
  is not a thing the project will do, since it already holds the item.
- **§2 `pr.title.conventional` judges the header a squash would land, not the
  title (issue #106).** The convention is a rule about the trunk's history and
  the trunk gets whatever the forge writes: with `squash_merge_commit_title:
  PR_TITLE` GitHub appends ` (#<number>)`, so a 66-character title lands as a
  73-character header. Linting the title therefore passed work that broke
  `cc.header.length` the moment it merged, and the live audit of spec 07 §6
  reported it a fortnight later — the gate contradicting the audit about the
  same commit. The rule now lints `core.PullRequest.MergeHeader()`. The two
  strings differ only at the end, so every rule about the front of a header
  reads the same thing either way and only the length rule changes its answer.
  A finding adds one line of evidence naming the header that lands and the
  characters the forge added, because a person counting their own title to 66
  cannot otherwise make sense of "73 characters"; when the two are the same
  string, nothing is said.

- **A team may excuse a class of change from `agent.human_review`, and never a
  pull request.** `agents.unreviewed_types` lists the commit types a machine
  may land with nobody approving them; an agent pull request whose landed
  header has one of those types passes the rule as skipped, saying which type
  excused it. Everything else about the rule is unchanged, including its place
  in `policyAlwaysError`: relaxed strictness still cannot turn it into a
  warning. The alternative designs all fail on the same point — the signal has
  to be one an agent cannot give itself. `agents.require_human_review: false`
  and a `policy.rules` override are repo-wide and permanent; a label or any
  other per-pull-request marker is applied with the same push access the agent
  already has, and dropping the `agent` label is worse still, because the pull
  request stops being agent work and the rule passes vacuously. A type written
  in `.flow.yaml` is authority a person granted, because every change to that
  file is itself reviewed.

- **The permission cannot be self-granted, so three kinds of change can never
  use it.** A pull request that changes `.flow.yaml`, or the workflow that
  runs the gate (`engine.GateFiles`), is changing what the gate will ask and
  is never exempt however routine its type. A breaking change is never
  routine, whatever its type. And a pull request flow cannot see the whole of
  — no file named, or a file list shorter than the forge's own count — is
  never exempt either, because the file that grants the permission might be
  the one that was not listed. When a listed type is refused for one of these
  reasons the finding says so in its evidence: a person who wrote `chore` into
  the configuration and then watched a chore fail would otherwise have nothing
  to read.

- **The type is read from the header a merge would land.** `MergeHeader()`
  again, for the reason above it: the gate and the audit must agree about the
  same commit, and the audit only ever sees what landed.
