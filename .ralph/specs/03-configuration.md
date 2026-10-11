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

## Decisions

- **§1 `version` is structural, not semantic (item 1.4).** `config.Parse` refuses
  anything but `version: 1` before any other check, and a missing `version` is
  reported at line 1. A file written for another schema version cannot be
  decoded into these structs at all, so deferring the check to
  `config.semantics` (§1.1) would mean reporting a cascade of unknown-key errors
  for keys that are perfectly valid in their own version.
- **§1 every problem is reported, not just the first (item 1.4).** Strict
  decoding collects unknown keys, type mismatches and invalid values across the
  whole file and reports them in file order. Fixing a policy file one error per
  run is the kind of friction that makes people stop running the tool.
- **§1 positions: line:column where the document parsed, line only where it did
  not (item 1.4).** Unknown keys, wrong types and invalid values are located to
  `path:line:column` by matching the YAML node tree against the decoder's
  errors. A YAML *syntax* error has no node tree to match, so flow reports the
  line the library gives and no column — and that line is the library's:
  yaml.v3 renders scanner errors from a one-based mark but parser errors from a
  zero-based one, so an unterminated `{…}` is reported one line early. Correcting
  it would mean guessing which kind of error a message came from.
- **§1 durations (item 1.4).** `d` is exactly 24h and `w` exactly 7d: flow
  measures elapsed time between a merge and a deployment, never calendar days,
  so daylight saving and leap seconds never enter into it. A bare number is
  refused — `max_lag: 2` is a question, not a value — with the single exception
  of `0`. Durations render back with days as the largest unit, so `1w` is read
  and written as `7d`; two spellings of one span is one too many.
- **§1 Parse applies no defaults (item 1.4).** It returns the file as written.
  Preset and default layering is precedence (§ top), and lands with the rest of
  precedence in plan item 1.8; until then, callers needing a default say so
  themselves (as `flow check commit` does with `commits.DefaultSettings`).
- **§2 placement is enforced at decode, not left to `config.semantics` (item
  1.5).** §1.1 lists the allowed placeholder paths among the semantic rules,
  but a `${pending:…}` anywhere else can never be resolved by anything: there
  is no discover, choose, create or ask step for `forge.repo`. `config.Parse`
  therefore refuses it with the rest of the file's problems, and
  `config.semantics` keeps the §1.1 rules that depend on observing the world.
- **§2 a placeholder is the whole value or it is an error (item 1.5).**
  `board-${pending:project}` is refused rather than concatenated: the resolver
  writes the value it found to the lock, and there is nowhere to record which
  part of a string it belongs to. Anything else resembling interpolation —
  `${env:HOME}`, `${project}`, an unclosed `${` — is refused in the same way,
  with the message saying `${pending:name}` is the only one that exists.
- **§2 the flow-mapping mistake is diagnosed, not relayed (item 1.5).** YAML
  cannot hold an unquoted `${…}` inside `{…}` and reports it as a missing
  brace, on the line the mapping opened on. When a syntax error coincides with
  a `${` inside a flow collection, flow replaces the parser's message with the
  real cause and points at the placeholder: put each key on its own line, or
  quote the value.
- **§2 self-referential anchors are walked once (item 1.5).** `&a [*a]` is four
  bytes of valid YAML that parses into a cyclic node tree; walking it for
  placeholders exhausted memory. The walk now keeps a record of the nodes it is
  inside and refuses to re-enter one.
- **§3 the lock is written in block style (item 1.7).** The example in §3 shows
  records as flow mappings (`{ hash: …, at: … }`). flow writes them one field
  per line instead: inside `{…}` a plain scalar may not contain `:`, so YAML
  quotes every `sha256:…` and every timestamp, and the compact form costs more
  noise than it saves. The example in `assets/examples/flow.lock` is now
  byte-for-byte what flow writes, and a test keeps it that way.
- **§3 the lock is rendered from an explicit node tree (item 1.7).** Key order,
  list order and quoting are flow's decision rather than the encoder's, so
  "deterministic" is a property of flow's code and not of a dependency's
  internal sort. Lists sort by their natural key (kind+id, check+ticket,
  check+by) and timestamps are UTC truncated to the second.
- **§3 a lock from another version is refused, not migrated (item 1.7).**
  `ParseLock` decodes as strictly as `.flow.yaml` does. The file says it is
  generated and not to be edited, so an unknown key means it was hand-edited or
  written by a build that knew something this one does not — guessing at either
  is worse than saying so.
- **§3 an observation whose clock went backwards is left alone (item 1.7).**
  Only a full TTL *forward* moves a timestamp, so a machine with a skewed clock
  cannot make the lock churn.
- **§4 one vocabulary for both personal files (item 1.8).** `user`, `ui`, `log`
  and `auth` are all a personal TOML may contain, and strict decoding refuses
  the rest with a message naming `.flow.yaml`. A `[commits]` table in someone's
  own file is them quietly holding themselves to a different standard than the
  team agreed, which is exactly what flow exists to prevent.
- **§4 `token_cmd` is refused inside the repository; `hosts` is not (item
  1.8).** `token_cmd` names a program flow executes, so it is only read from
  the user's own config (spec 15 §1). `hosts` is data — it can only narrow
  where a credential may be sent — so `.flow.local.toml` may set it.
- **§4 the tracked check is injected (item 1.8).** `config` may not import
  `internal/git` (spec 01 §2), so `LoadPersonal` takes a `Tracked` function and
  the caller supplies git. If git cannot answer, flow refuses to read the
  override rather than assuming it is personal; if there is no git to ask at
  all the file is read, which is safe because it may not carry a `token_cmd`
  either way.
- **§4 unset and false are different (item 1.8).** Boolean personal settings
  decode into pointers, so `ascii = false` in a user config survives a
  repository override that says nothing about it.
- **§4 TOML problems carry a position only when the library knows one (item
  1.8).** A syntax error is reported `path:line:col`; an unknown key is
  reported by its dotted name alone, because the decoder records which keys
  went unused but not where they were written.
- **§5 `NO_COLOR` follows the convention, not flow's boolean rules (item
  1.8).** Any non-empty value — including `0` — means no colour, and it beats
  `FLOW_NO_COLOR`. An unparseable `FLOW_*` boolean is ignored rather than
  guessed at, leaving the layer below in charge.
- **§5 credential resolution is its own piece of work (item 1.8).** This item
  defines where credential *sources* are configured and who may configure
  them. Reading the named variable, falling back to `FLOW_<PROVIDER>_TOKEN`
  and running `token_cmd` is plan item 1.14.
- **§6 the generator is a test, not a second main package (item 1.9).** `go
  generate ./internal/config` runs `go test . -run TestSchemaFileIsCurrent
  -update`, so the same code both writes `assets/schema/flow.v1.json` and
  fails when it is stale. A `cmd/schema-gen` would be a package outside the
  layout spec 01 §1 fixes, and a second renderer to keep in step.
- **§6 the schema is built in Go, not stored as a literal (item 1.9).** `flow
  schema` prints what `ConfigSchema` renders rather than the embedded file, and
  a test asserts the two are identical. The binary can then never print a
  schema it does not itself implement, and the committed file can never drift
  unnoticed.
- **§6 parity is by key, not by constraint (item 1.9).** The test insists every
  struct field is described and every described key exists, and that the JSON
  type matches the Go type. It does not try to derive patterns, enums or
  required lists from the structs: those are the things a schema says that Go
  cannot, and deriving them would delete the reason the schema is hand-written.
- **§6 `--kind` other than `config` is still `ErrUnsupported` (item 1.9).**
  Spec 02 §2 lists five kinds; §6 defines only the configuration schema. The
  plan, doctor and policy documents do not exist yet and the lock schema has no
  consumer, so flow says so and names the kind that works rather than printing
  a schema nobody wrote.
