# 03 — Configuration

| File | Format | Committed | Purpose |
|---|---|---|---|
| `.flow.yaml` | YAML | yes | Desired workflow, adapters, policy. Schema-validated. |
| `.flow.lock` | YAML | yes | Resolved IDs, observations, follow-ups, attestations. Machine-written. |
| `$XDG_CONFIG_HOME/flow/config.toml` | TOML | no | Personal: identity, credential sources, UI, approved hosts. |
| `<root>/.flow.local.toml` | TOML | no (gitignored) | Personal per-repo overrides. |

Precedence: flags > env > `.flow.local.toml` > user TOML > `.flow.yaml` > preset > defaults.
Personal TOML can only override: `ui.*`, `auth.*`, `user.*`, `log.*`. Team policy
cannot be weakened locally.

## 1. `.flow.yaml` v1

Strict decoding (`KnownFields(true)`); unknown keys → 64 with line:col.
Durations accept Go syntax plus `d`/`w`. First line written by init:
`# yaml-language-server: $schema=https://raw.githubusercontent.com/17twenty/flow/main/assets/schema/flow.v1.json`

```yaml
version: 1
preset: lean-kanban

forge:
  type: github                    # github | gitea | gitlab
  repo: 17twenty/flow
  url: https://github.com         # GHES/self-managed: base web URL
  auth: { token_env: GITHUB_TOKEN }

tracker:
  type: github-projects           # github-projects | jira-cloud | linear
  owner: 17twenty                 # github-projects: user or org login
  owner_type: user                # user | org
  project: ${pending:project}     # github-projects: project number; jira: project key; linear: team key
  auth: { token_env: FLOW_PROJECT_TOKEN }
  # jira-cloud: url, auth.email_env ; linear: (none)

workflow:
  done_on: merged                 # merged | deployed   (spec 04 §6)
  columns:                        # ordered; ids stable; names = tracker display names
    - { id: backlog,     name: Backlog,     category: todo }
    - { id: ready,       name: Ready,       category: todo, gate: definition_of_ready }
    - { id: in_progress, name: In progress, category: in_progress, wip: { per_person: 2 } }
    - { id: in_review,   name: In review,   category: in_progress, wip: { limit: 3 } }
    - { id: done,        name: Done,        category: done }
  transitions:                    # who moves tickets for each event (spec 04 §5)
    pr_opened_draft:       { to: in_progress, owner: flow }
    pr_ready_for_review:   { to: in_review,   owner: flow }
    changes_requested:     { to: in_progress, owner: native }
    pr_merged:             { to: done,        owner: native }
    issue_closed:          { to: done,        owner: native }
    # deployed: { to: done, owner: flow }   # only with done_on: deployed (see preset deploy-done)
  expedite:
    labels: [incident]            # any-of; expedite items ignore WIP
    max: 1
  blocked:
    label: blocked
    require_comment: true

definition_of_ready:
  rules: [acceptance_criteria, parent_link, not_blocked]
  # optional: flag_if_user_facing, design_if_ui, max_size: 2d

commits:
  convention: conventional
  types: [feat, fix, perf, refactor, test, docs, build, ci, chore, revert, style]
  header_max: 72
  merge_strategy: squash          # gate checks the PR title as the future commit header
  scopes: { required: false, allowed: [] }
  unlinked_types: [ci, build, chore, docs]   # PR title types that may merge without a ticket

deploys:
  source: forge_deployments       # forge_deployments | commit_status | none
  environment: production         # name as recorded by the deployer
  # commit_status only: contexts: ['argocd/app-prod']   (exact names or prefix*)
  max_lag: 2h                     # doctor: merged → deployed SLO
  max_age: 7d                     # doctor: warn if no deployment seen in this window

policy:
  status_context: flow/policy
  strictness: standard            # relaxed | standard | strict
  comment: on_failure             # always | on_failure | never
  rules: {}                       # per-rule severity overrides: off | warn | error

agents:
  authors: ['17twenty-flow-agent[bot]']   # logins treated as agents
  labels: [agent]
  require_human_review: true

followups:
  labels: [flow-followup]
  owners:
    default: '@17twenty'
    tracker_admin: '@17twenty'
    forge_admin: '@17twenty'
    platform: '@17twenty'

attestation:
  default_ttl: 30d
```

### 1.1 Semantic validation (`flow doctor` check `config.semantics`)
- Column ids unique `^[a-z][a-z0-9_]*$`; exactly one `done` category column.
- Every transition `to` names a column; events from the vocabulary (spec 04 §4).
- `done_on: deployed` ⇔ `transitions.deployed` present and `deploys.source != none`.
- `done_on: merged` ⇒ `transitions.deployed` absent (deploy lag is verified, not tracked as status).
- Owner `native` only allowed for events the tracker capability file marks
  `native_transition_<event>: set|verify` true; otherwise error with hint
  "use owner: flow".
- Placeholders allowed only in: `tracker.project`, `followups.owners.*`.
- `auth.*_env` must be env var NAMES; heuristic secret detection on all strings.

## 2. Placeholders

Placeholders MUST appear as block-style plain scalars (or quoted strings); YAML flow
mappings `{…}` cannot contain an unquoted `${…}`. `flow init` always writes block style.

`${pending:<name>}` → `config.Pending`. Resolved values go to the lock
(`resolved:`), not back into `.flow.yaml`, unless the user picks "pin" in the
TUI (YAML node round-trip preserving comments). No other interpolation exists.

## 3. `.flow.lock` v1

Deterministic (sorted keys/lists, RFC3339 UTC, trailing newline), atomic
writes, header comment `# generated by flow — do not edit; use flow doctor --fix`.

```yaml
version: 1
flow_version: v0.3.0
config_hash: sha256:…
resolved:
  tracker.project: "3"
  tracker.project_id: PVT_kwHO…
  tracker.status_field_id: PVTSSF_…
  tracker.status_options.backlog: f75ad846
  tracker.status_options.ready: 61e4505c
  tracker.status_options.in_progress: 47fc9ee4
  tracker.status_options.in_review: df73e18b
  tracker.status_options.done: 98236657
  forge.default_branch: main
observed:
  tracker.status_options: { hash: sha256:…, at: 2026-10-09T08:00:00Z }
  forge.protection: { hash: sha256:…, at: 2026-10-09T08:00:00Z }
created:
  - { kind: tracker.status_option, id: df73e18b, name: In review, at: 2026-10-09T08:00:00Z }
followups:
  - { check: tracker.native.pr_merged, ticket: "#12", owner: '@17twenty', opened_at: …, state: open }
attestations:
  - { check: tracker.native.issue_closed, by: '@17twenty', note: 'Item closed → Done enabled', at: …, expires_at: … }
```
`observed.*.at` only changes when the hash changes or once per 24h.

## 4. TOML

User config:
```toml
[user]
handle = "@17twenty"

[ui]
theme = "auto"   # auto | dark | light
ascii = false

[auth.github]
token_cmd = ["gh", "auth", "token"]        # argv, no shell
hosts = ["api.github.com"]                 # approved hosts for this credential

[auth.github_projects]
token_cmd = ["gh", "auth", "token"]        # needs 'project' scope: gh auth refresh -s project
```
`token_cmd` is read only from user-owned TOML; a git-tracked `.flow.local.toml`
is refused (P9).

## 5. Environment variables

`FLOW_DIR`, `FLOW_CONFIG`, `FLOW_USER_CONFIG`, `FLOW_JSON`, `FLOW_NON_INTERACTIVE`,
`FLOW_LOG_LEVEL`, `FLOW_LOG_FORMAT`, `FLOW_NO_COLOR`, `NO_COLOR`, `FLOW_ASCII`,
`FLOW_TIMEOUT`, `FLOW_CACHE_DIR`, `CI`; credentials by the names configured in
`.flow.yaml` (`GITHUB_TOKEN`, `FLOW_PROJECT_TOKEN`, `JIRA_EMAIL`,
`JIRA_API_TOKEN`, `LINEAR_API_KEY`, …) with fallbacks `FLOW_<PROVIDER>_TOKEN`
then user `token_cmd`. CI event context: `GITHUB_EVENT_PATH`, `GITHUB_EVENT_NAME`,
`GITHUB_REPOSITORY`.

## 6. JSON Schema

Hand-written in `internal/config/schema.go`; `flow schema` prints it;
`go generate` writes `assets/schema/flow.v1.json` (committed). Parity test
between Go structs and schema (reflection in the test only). Staleness test.

## 7. Presets

`lean-kanban` (default; `done_on: merged` + deploy lag SLO), `deploy-done`
(`done_on: deployed`, flow owns the deployed transition), `minimal` (no Ready
gate, relaxed policy).
