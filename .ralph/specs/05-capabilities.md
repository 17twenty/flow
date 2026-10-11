# 05 — Capability Model

## 1. Declarations

`assets/capabilities/<adapter>.yaml` (embedded):

```yaml
adapter: github-projects
kind: tracker
version: 1
researched_at: 2026-10-09
sources: [https://…]
limits: { rate: "…" }            # informational
capabilities:
  status_options:
    set: true
    verify: true
    requires: [project_write]    # permission tokens reported by Probe
    remediation: follow_up       # follow_up | compensate | attest | none
    compensated_by: ""           # policy.wip | policy.expedite | policy.dor | engine.transitions
    hazards: ["…"]
    notes: "…"
```

## 2. Registry (`core/capability_names.go`)

Tracker: `project`, `status_options` (columns/statuses), `status_categories`,
`wip_limits`, `expedite_view`, `labels`, `follow_up_tickets`, `ticket_move`,
`native_transition_pr_opened_draft`, `native_transition_pr_ready_for_review`,
`native_transition_changes_requested`, `native_transition_pr_merged`,
`native_transition_issue_closed`, `native_transition_deployed`.

Forge: `commit_status_write`, `branch_protection`, `required_status_checks`,
`merge_settings`, `file_proposal`, `pr_comments`, `deployments_read`,
`linked_issues_read`.

Deploys: `deploy_records`.

Tests assert: every name used in code exists in the registry; every declaration
file uses only registry names; `compensated_by` is set iff remediation is compensate.

## 3. Probing

`Probe` is read-only, ≤ 5 requests, returns permission tokens + evidence.
GitHub: repo `permissions` (admin/maintain/push), classic token scopes from
`X-OAuth-Scopes` (`repo`, `project`, `read:project`), and a trial read of the
Project (user- or org-owned) to confirm project access.

## 4. Outcome resolution (exact)

```
if observed == desired:                      noop
elif cap.set and probe ⊇ cap.requires:       native
elif cap.remediation == compensate:          compensated
elif cap.remediation == follow_up:           follow_up
elif cap.remediation == attest:              attest
else:                                        unsupported   (plan exit 1)
```
Unobservable (`verify: false`) ⇒ treat as not equal unless a valid attestation exists.
Compensated outcomes never create follow-ups.

## 5. Rendering

`✓ native` · `↻ compensated → <mechanism>` · `⚠ follow-up → <owner>` ·
`✎ attest → flow attest <id>` · `✗ unsupported`. JSON `flow.plan/v1`.

## 6. Comparison mode

`flow plan --tracker-override <kind>`: same desired state against another
tracker's declarations, full permissions assumed, no network. Side-by-side table.
