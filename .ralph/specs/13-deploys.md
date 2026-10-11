# 13 — Deployment Signals

flow never talks to clusters or deploy tools. It reads **deployment records the
forge holds about the app repository**, written by whatever deploys.

## 1. Contract with deployers

A deployer (release workflow, Argo CD, Flux, anything) MUST record, on the
**application repository at the application commit SHA**:
- preferred: a forge Deployment for environment `deploys.environment` with a
  `success` deployment status (`source: forge_deployments`), or
- a commit status with a configured context and state success
  (`source: commit_status`).

No message parsing, no image-tag parsing (P12).

## 2. Patterns (documented in `assets/docs/deploy-patterns.md`)

- **CLI/library (flow itself)**: the release workflow, after publishing a
  release for SHA X, creates Deployment(env=production, ref=X) + status success.
  `done_on: merged` + `max_lag` proves every merge was released (spec 17).
- **Single repo + GitOps**: Argo CD/Flux sync the app repo path; Argo CD
  notifications report on the synced revision (= app SHA).
- **Separate GitOps repo**: the bump step writes the app repo and SHA into
  structured fields on the Argo CD Application (annotations
  `flow.dev/app-repo`, `flow.dev/app-revision`); the notification template's
  repo URL and revision are templated from those annotations, so Argo CD reports
  against the app commit. (Field names in notification templates vary by Argo CD
  version — templates in `assets/templates/gitops/` must state the tested version.)

## 3. Reading

GitHub: `GET /deployments?environment=<env>` (newest first) + latest status per
deployment; GitLab: `/deployments?environment=&status=success`; commit_status:
statuses on default-branch commits matching contexts. "Deployed SHA at time T" =
newest successful record ≤ T. Ancestry (`merge_sha ⊑ deployed_sha`) via forge
compare API, cached per pair in `FLOW_CACHE_DIR`.

## 4. Doctor

`deploys.source` (setup), `live.deploys.recent`, `live.deploys.lag`,
`live.tickets.done_deployed` (spec 07 §6). Missing records → remediation
`manual` with the pattern for the detected setup, owner `followups.owners.platform`.
