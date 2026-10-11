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
