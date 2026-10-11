# 06 — Plan and Apply

## 1. Desired items (lean-kanban, GitHub + GitHub Projects)

| id | capability | depends_on |
|---|---|---|
| `tracker.project` | project (resolve) | — |
| `tracker.status_options` | status_options | tracker.project |
| `tracker.labels` | labels (blocked, incident, flow-followup, agent, user-facing, ui) | tracker.project |
| `tracker.native.<event>` for each `owner: native` transition | native_transition_<event> | tracker.status_options |
| `tracker.wip` | wip_limits | tracker.status_options |
| `tracker.expedite` | expedite_view | tracker.project |
| `forge.default_branch` | (resolve) | — |
| `forge.protection` | branch_protection | forge.default_branch |
| `forge.required_status` | required_status_checks | forge.protection |
| `forge.merge_settings` | merge_settings (squash only, title = PR title) | — |
| `forge.files` | file_proposal (CI workflow, PR template, issue form) | — |
| `deploys.records` | deploy_records (verify only) | forge.default_branch |

## 2. Plan

Pure diff of canonical desired vs canonical observed per item → outcome
(spec 05 §4) + changes + owner + hazards. Serialisable (`--out`), carrying
config/lock/observation hashes for staleness checks.

## 3. Apply

Re-plan or load plan (stale → 1). Confirm. Execute `native` items in dependency
order; failures block dependants only. Create/update follow-ups. Write lock after
each successful mutation (atomic) and at the end. Summary.

## 4. Safety (MUST)

- Never write to the default branch; files are proposed via a PR from branch
  `flow/setup-<id>` titled `chore(flow): …`, updated in place on re-run.
- Protection changes are additive (keep existing required contexts/settings).
- Never delete statuses/options not recorded in lock `created:`; `--prune`
  refuses options currently in use.
- GitHub Projects option edits replace the option set and can clear item values:
  snapshot item→option name, mutate, restore by name, verify; >500 items →
  follow_up instead.
- Every mutating call logged at info (method, path, status, resulting id).

## Decisions

- **§1 a desired item's fields are only what the provider can report back
  (item 2.4).** A column's category is flow's own mapping onto
  todo/in_progress/done and no tracker researched can be asked for it; a
  column's configured id is what the lock maps onto the tracker's id. Neither
  is part of the desired contents, because a field an observation cannot carry
  turns every comparison into a difference that can never be resolved. What
  flow knows and the provider cannot say stays in `.flow.yaml`.
- **§1 the table is derived from the configuration, not transcribed (item
  2.4).** `NewDesired` reads the columns, transitions, labels and merge
  strategy a team actually wrote, so the §1 table is what the lean-kanban
  preset happens to produce rather than a list flow carries around. A team
  that does not expedite, does not limit work in progress, deploys nothing or
  publishes no status context is asked for none of those items.
- **§1 `user-facing` and `ui` are wanted whether or not their rules are on
  (item 2.4).** Spec 04 §7 fixes those two label names, and lean-kanban
  enables neither rule yet §1 still lists both labels. A team cannot start
  labelling work `user-facing` before the label exists, and so could never
  switch the rule on. Every other label is derived from the configuration that
  names it.
- **§1 a resolve item has no contents (item 2.4).** `tracker.project` and
  `forge.default_branch` are identifiers to find, not states to make true, so
  they carry `Resolve` and nothing to compare: what matters is that the thing
  is there and the lock records which one it is (spec 07 §3). Only
  `forge.default_branch` has no capability name at all — finding the trunk is
  reading the repository, which every forge can do.
- **§1 an item says whether more than was asked for is a difference (item
  2.4).** Labels, status options, required checks and proposed files are
  `Additive`: a label flow does not know about is somebody else's label, and
  §4 forbids deleting anything the lock does not record as flow's. Protection,
  merge settings and WIP limits are compared as a whole, because they are one
  setting rather than a set.
- **§1 protection asks for no approval count (item 2.4).** The configuration
  implies that the trunk is protected, is not force-pushed or deleted, and
  keeps a linear history when the team squashes or rebases. It does not imply
  a number of reviews: spec 09's gate is what insists on a human approval of
  agent work, and a required count in protection would hold every other pull
  request to a rule nobody wrote down. Protection changes are additive, so a
  team that wants one keeps it.
- **§1 `forge.files` wants its commands run, not its bytes copied (item
  2.4).** Spec 07 §5 defines the check as the workflow being present and
  running `flow check pr` (and `flow check deploy` when done means deployed),
  so that is the desired content. Comparing the file against the shipped
  template would call every step a team added to its own CI drift, and the
  plan would nag for ever. The template bytes are still carried, because they
  are what the first proposal writes.
- **§1 only GitHub is proposed to (item 2.4).** `.github/workflows/flow.yml`
  and its neighbours are GitHub's layout, and flow ships no other. A forge
  flow has no templates for is asked for no files rather than offered
  GitHub's.
- **§2 the canonical form is legible (item 2.4).** An observation and a
  desired item encode to one quoted record per line — items in a fixed order,
  fields sorted by name — and the hash is the hash of those bytes. Quoting is
  what makes it unambiguous: `strconv.Quote` escapes newlines, so no value can
  close its own record and forge another. A golden file nobody can read is a
  golden file nobody checks, and the first question about a hash that changed
  is always "what changed".
- **§2 a proposed file is hashed, not quoted into the canonical form (item
  2.4).** The file line carries the path and the hash of the content. The
  bytes are what apply proposes; repeating them in the encoding would bury it
  in CI workflow while telling a reader nothing the hash does not.
