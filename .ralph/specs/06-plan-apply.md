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
- **§1 a status option's board position is not desired (item 2.5).** Item 2.4
  asked for each column at its configured index. §4's hazard is that setting
  a GitHub Projects option set replaces it, which can clear every item's
  status — so flow would propose that destructive write to move a column one
  place left. The set of column names is what the workflow depends on; the
  order they are drawn in is the board's own business, and a team that drags
  one is not drifting.
- **§2 the plan is pure, and the adapters are not passed to it (item 2.5).**
  `NewPlan` takes the desired state, the observations, and each provider as
  `{kind, capabilities, permissions}`. Given the same input it gives the same
  plan on any machine at any time, which is what makes the golden files worth
  pinning and what lets spec 05 §6's comparison put another tracker's
  declarations in front of one unchanged configuration with no network at all.
- **§2 contents are compared by name, and the provider may know more (item
  2.5).** An observed thing matches when it says everything the desired one
  says, not when it says exactly the same things: a label flow asked for by
  name has a colour and a description flow never mentioned, and calling that
  a difference would propose an update on every run for ever. An update
  carries the provider's own id, because that is what apply needs to address
  it.
- **§2 every item lists its changes, whatever flow will do about them (item
  2.5).** A compensated, attested, follow-up or unsupported item still shows
  the creates, updates and deletes that would make the provider agree. Those
  changes are the content of the follow-up issue and of the thing a human is
  being asked to attest to; hiding them would leave the plan saying somebody
  must act without saying what the act is.
- **§2 an item with no capability is resolved as read-only (item 2.5).**
  `forge.default_branch` names none (§1), so the planner hands the ladder a
  synthesised `{set: false, verify: true, remediation: follow_up}` rather than
  deciding its outcome itself. Spec 05 §4 stays the one place an outcome is
  reached, and a trunk that is not there comes out as a follow-up instead of a
  special case.
- **§3 a plan goes stale against three hashes and every observation (item
  2.5).** `flow apply --plan` compares the configuration hash, the desired
  hash, and each item's observed hash against a plan made now, and refuses on
  the first difference, naming it. The three answer different questions — what
  the team asked for, what flow made of it, what the provider held — and
  applying a plan once any of them has moved would make a change nobody
  approved. The lock hash is recorded but not compared: an attestation made
  since the plan can only make flow do less.
- **§4 a removal is proposed, never assumed (item 2.5).** The plan lists a
  delete for anything observed that the configuration does not ask for —
  subject to §4's rule that an additive set only yields what the lock records
  as flow's own creation — and `--prune` decides at apply time whether it
  happens. A plan that hid unwanted leftovers until somebody passed a flag
  would be answering a different question from the one `flow plan` is asked.
