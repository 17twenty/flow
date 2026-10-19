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
  required_checks: [test]         # contexts the trunk requires besides flow's own gate; additive
  auth: { token_env: GITHUB_TOKEN }

tracker:
  type: github-projects           # github-projects | jira-cloud | linear
  owner: 17twenty                 # github-projects: user or org login
  owner_type: user                # user | org
  project: ${pending:project}     # github-projects: project number; jira: project key; linear: team key
  auth: { token_env: FLOW_PROJECT_TOKEN }
  shared_with: []                 # github-projects: other repos whose work is on this board
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
  unreviewed_types: []                    # types a machine may land unapproved

followups:
  labels: [flow-followup]
  owners:
    default: '@17twenty'
    tracker_admin: '@17twenty'
    forge_admin: '@17twenty'
    platform: '@17twenty'

attestation:
  default_ttl: 30d

health:
  fail_below: 90                  # doctor: a live check below this % fails rather than warns
  flag_max_age: 90d               # doctor: how long a feature flag may stand before it is asked about
  flag_kept_label: flag-kept      # the label on a cleanup ticket that says the flag stays on purpose
  stale_after: 5                  # doctor: working days work may sit in a column before the audit says so
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

[auth.github-projects]
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
- **§5 the provider is the adapter kind, and the fallback is spelled from it
  (item 1.14).** `FLOW_<PROVIDER>_TOKEN` takes `<PROVIDER>` from `forge.type`
  or `tracker.type` exactly as written in `.flow.yaml`, upper-cased with every
  character that is not a letter or digit turned into `_`: `github-projects`
  becomes `FLOW_GITHUB_PROJECTS_TOKEN`. The same string keys the user's
  `[auth.<provider>]` table, so one name serves all three sources.
- **§5 an empty variable falls through to the next source (item 1.14).** A
  variable set to the empty string, or to nothing but whitespace, is treated as
  unset rather than as an empty credential. `GITHUB_TOKEN=` left over from a
  shell profile should not mask a working `token_cmd`. Values are trimmed of
  surrounding whitespace, since a trailing newline is what `export
  GITHUB_TOKEN=$(cat token)` usually produces.
- **§5 no credential at all exits 77, not 2 (item 1.14).** Spec 02 §4's ACTION
  REQUIRED is for work flow has finished and filed for a human; a command that
  cannot authenticate has not started. Absent credentials therefore wrap
  `core.ErrPermission`, and the message names the variable to export and the
  `token_cmd` that would have been run.
- **§5 `token_cmd` must print one line within 30 seconds (item 1.14).** It is
  run as argv with no shell, with an immediate EOF on stdin so a helper that
  asks a question fails instead of hanging, and is given 30s — enough for a
  vault to prompt for a fingerprint, short enough that CI does not sit on it.
  Output is trimmed; empty output is `ErrPermission`; more than one line is
  `ErrValidation`, because a token is one line and the second line is usually
  a warning the user cannot see. A helper that is missing or not executable is
  a configuration error (64); one that runs and fails, or times out, is
  `ErrUnavailable` (69). Only the exit status and the first line of stderr,
  passed through `core.Redact`, appear in the error: a failing helper often
  quotes the secret back.
- **§5 a resolved credential is kept for the life of the process (item 1.14).**
  Concurrent callers for one provider wait for each other, so a vault is
  unlocked once rather than once per API call. Failures are not remembered, so
  a helper that was merely locked can succeed on the next attempt.
- **§5 `auth.*_env` holding a secret is refused at resolution too (item
  1.14).** `config.semantics` (§1.1) is where the full heuristics live, but the
  resolver will not read a variable whose *name* is itself a credential shape
  — `core.Redact` changing the string is the test. Nothing is gained by
  deferring a check that stops flow sending a pasted token to a provider, and
  the error never repeats the value back.
- **§1.1 semantic problems name a key, not a line (item 1.6).** Decoding
  problems carry `line:col` because they are about the document; semantic ones
  are about the decoded configuration — `workflow.transitions.pr_merged.owner`
  is more useful than a line number, and the same check runs over presets and
  merged state where there is no single line to point at. They reuse
  `ParseError`, so tooling reads them the same way and the exit code is still
  64.
- **§1.1 the secret heuristic runs at parse time, over the document (item
  1.6).** Every scalar is checked with `core.Redact`: if redaction changes the
  value, it is a credential shape, and the file says so at the line it is on.
  Doing it on the document rather than the decoded struct catches a secret
  wherever it was written — including keys the struct has no field for — and
  needs no reflection to walk. `config.semantics` still reports it, because
  doctor parses the file to check it. The error never repeats the value.
- **§1.1 a nil capability registry skips only what it is needed for (item
  1.6).** `config.Validate` takes the registry so it can tell whether a
  tracker really has the automation an `owner: native` transition assumes.
  Callers that cannot supply one — a command that only wants the commit
  convention — pass nil and still get every other check.
- **§1.1 a key that means nothing for the chosen adapter is an error (item
  1.6).** `tracker.owner` under Linear, `tracker.url` under Linear,
  `forge.auth.email_env`, `deploys.contexts` under `forge_deployments`:
  ignoring them would let someone believe a setting had an effect. The same
  reading as §1's strict decoding, applied to combinations rather than keys.
- **§1.1 contradictions are refused as well as unknown values (item 1.6).**
  A column gated on `definition_of_ready` while `definition_of_ready.rules` is
  empty, a `wip` block with no limit in it, `expedite.max` with no labels,
  `blocked.require_comment` with no label, `agents.require_human_review` with
  no authors or labels, `commits.scopes.required` with an empty allow-list, an
  `unlinked_type` that is not a type, and any `deploys` key under
  `source: none` all describe a rule that cannot fire. A policy that silently
  does nothing is the thing flow exists to stop.
- **§1 `done_on: merged` still allows a deploys source (item 1.6).** Only
  `transitions.deployed` is forbidden. Deployment lag is verified under
  `merged` (spec 04 §6), so flow still needs somewhere to read deployments
  from; the lean-kanban preset does exactly this.
- **§1.1 `tracker.owner_type` is required for github-projects (item 1.6).**
  GitHub addresses `user { projectV2 }` and `organization { projectV2 }`
  differently, and guessing costs a wasted API round trip and a confusing
  permission error. flow asks instead.
- **§1.1 `commits.header_max` must be between 20 and 200 (item 1.6).** Below
  20 there is no room for a type, a scope and a verb; above 200 it is not a
  limit. The spec gives no bound, and an unbounded one would accept a value
  that silently fails every commit.
- **§1.1 policy rule ids are not checked here (item 1.6).** `policy.rules`
  keys are validated against the gate's rule registry in spec 09 §2 (item
  2.13), which is where that registry exists; only the severity vocabulary is
  checked now. Checking ids against a hard-coded list in two places is how
  they drift.
- **§7 `minimal` has no Ready gate at all (item 1.10).** The sentence is "no
  Ready gate, relaxed policy", and the literal reading is the simplest: the
  `ready` column carries no `gate:` key. `definition_of_ready.rules` keeps
  `not_blocked`, because spec 04 §7 has `check pr` read the definition of
  ready whether or not a column gates on it — the preset starts a team with
  one rule, not with none.
- **§7 a preset is copied with its comments (item 1.10).** `PresetSource`
  returns the file verbatim and `flow init` writes that, rather than
  re-rendering the decoded config. The comment beside each key is the
  documentation a team actually reads, and a round trip through the structs
  would delete all of it. `LoadPreset` decodes the same bytes for the code
  paths that need values.
- **§7 presets are read from an `fs.FS` the caller supplies (item 1.10).** As
  with the capability registry, `config` does not import `assets`: the root
  passes `assets.Presets()`. A preset name outside `PresetNames` is refused
  before the file system is touched, so a typo names the presets that exist.
- **§7 a preset still holds its placeholders (item 1.10).** `tracker.project`
  and every `followups.owners.*` are `${pending:…}` in all three files, and
  `forge.repo` is `owner/name`: a preset cannot know a board number or a
  team's handles, and `flow init` fills them in. A golden file per preset pins
  what each one decodes to, so changing a preset is a change somebody looked
  at.
- **§1 `health.fail_below` is a whole number of per cent (item 5.1).** Spec 07
  §6 names the threshold but §1 had nowhere to write it. It is an integer
  because that is how a team says it out loud — "ninety per cent" — and
  because `0.9` and `90` in the same key is a mistake nobody would catch.
  Absent means 90; `config.semantics` refuses anything outside 1 to 100, since
  zero is how an absent key reads and a threshold nothing can fall below is
  not one anybody meant.
- **§1 how far back the audit looks is not configuration (item 5.1).**
  `health` holds how strictly the answer is graded, which is a property of the
  team; `--window` stays a flag, because a daily job and somebody checking by
  hand want different fortnights from the same repository on the same day.
- **§4 a tracked override is left unread, not fatal (item 7.6).** Scenario S01
  settles what "refused" means: the *file* is refused, not the run. A
  `.flow.local.toml` that is tracked is the team's, and a file anybody can
  commit must not be able to stop everybody else working — so flow leaves it
  unread, says so once on stderr, and carries on with the layers it does
  trust. The run then fails for want of a credential (77) only if that file
  was carrying the only one, which is the failure it would have had if the
  file had never existed. The same applies when git cannot be asked whether
  the file is tracked: a file flow cannot tell apart from the team's is not
  personal, and not reading it is the whole remedy.
- **§4 the user's own file is still worth stopping for (item 7.6).** An
  *untracked* `.flow.local.toml` naming a `token_cmd` remains an error, not a
  warning. Nobody but its owner can have written it, so flow is telling a
  person about their own mistake and naming the key, where the tracked case is
  flow declining to be used as a lever by somebody else.
- **§4 the auth table is keyed by the provider exactly (item 7.6).** The kind
  as `.flow.yaml` spells it — `github-projects`, with a hyphen. The shipped
  example said `[auth.github_projects]`, which is read as a provider nothing
  asks about and is therefore silently never used; it is corrected. Because
  the mistake is invisible until a credential or a host is missing, both of
  those messages now point at a table that names the provider with the wrong
  punctuation.

- **`forge.required_checks` is what the trunk must pass besides flow's own
  gate** (item 7.0, settling the Spec issues entry raised by `@17twenty`
  against follow-up #91). Four documents promised that `flow apply` creates a
  ruleset requiring `flow/policy` *and the build*, and nothing in the
  configuration could say what the build is called, so `forge.required_status`
  only ever asked for `policy.status_context`. The key is a list of status
  contexts, validated like any other configured list: no empty entry, no
  padding (a context is matched exactly, so the spaces around it are part of
  it), no repetition. Flow's own gate is still flow's business and is not
  written here; naming it anyway is a repetition and is dropped rather than
  refused, because a forge asked for the same context twice is being asked
  something nobody meant. The item stays additive (spec 06 §4), which is what
  makes the key safe: flow asks for the contexts it was given where they are
  missing, and never takes away a check somebody else required.

- **A team may require a build without running flow's gate.** Neither half of
  `forge.required_status` implies the other, so the item exists whenever
  either `policy.status_context` or `forge.required_checks` has anything in
  it, and `strict_checks` follows the whole list rather than the gate alone.
  Currency is a property of a required check — a status is about the commit it
  was posted on — and it is no less so when the check is the team's.

- **The order the contexts are written in is not a difference.** The desired
  state canonicalises its items, so the same set written two ways is the same
  plan and the same hash, and a team that alphabetises its list does not
  thereby propose a change.

- **`agents.unreviewed_types` is validated against `commits.types`.** A type
  that cannot be written cannot be excused, which is the same rule
  `commits.unlinked_types` is held to and the same message. An empty
  `commits.types` is the convention's own set rather than an empty one, so a
  team that never narrowed the list may still excuse a routine type. The key
  is absent from every shipped preset: the exemption exists only where a
  person wrote it down.

- **`health.flag_max_age` and `health.flag_kept_label` are the team's, and
  they live beside `fail_below` (item #146).** How long a flag may stand is an
  agreement a team makes once and keeps to, which is the same kind of thing as
  how strictly its audit is graded and not the same kind of thing as
  `--window`, which is a property of one run. The label is configurable for
  the reason every other label flow reads is: it is a word in the team's
  vocabulary, and flow asks the tracker for it by whatever name they gave it.

- **A negative `flag_max_age` is refused and an absent one is 90 days.** Zero
  is how an absent key reads, so it cannot also mean "every flag is too old
  the day it is announced"; the default is written down once, in the engine,
  for the reason `fail_below`'s is.
- **§1 `health.stale_after` is a whole number of working days (issue #156).**
  Spec 07 §6 counted five of them in the engine, as a constant, and the
  review column had no patience at all. The key is optional, defaults to 5,
  must not be negative and has no upper bound: a team that waits a quarter has
  said so deliberately, and the checks that read it only ever warn.

- **§1 `tracker.shared_with` names the other repositories whose work is on
  this board (issue #89).** flow runs in one repository and can only see that
  repository's pull requests, so a sibling's ticket reaching Done is
  indistinguishable from work that never went through the trunk. The key is a
  list of `owner/name`, optional, and says that those tickets are audited by
  the flow running there. Deriving it instead — auditing only tickets whose
  repository matches `forge.repo` and saying nothing about the rest — was
  rejected: a repository nobody runs flow in would then escape silently, and
  nothing in the configuration would record the arrangement for the next
  person to read.

- **§1 A ticket from a repository `shared_with` does not name is still a
  finding.** The key is how a team says "somebody else is watching that". An
  unnamed sibling is either a repository running no flow at all or a line
  somebody forgot to write, and both are worth a report.

- **§1.1 `tracker.shared_with` is refused for a tracker that records no
  repository.** Jira and Linear tickets belong to a project rather than to a
  repository, so flow could never tell a sibling's work from this
  repository's and the key would quietly do nothing. Naming this repository
  in its own list, naming the same sibling twice, or writing something that is
  not `owner/name` are errors for the same reason: each is a line that cannot
  mean what it says.
