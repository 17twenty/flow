# 02 — CLI Surface

Kong-based. This file is the public contract; changes update it in the same commit.

## 1. Global flags

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-C, --dir` | `FLOW_DIR` | `.` | Repo dir (walks up to git root). |
| `--config` | `FLOW_CONFIG` | `<root>/.flow.yaml` | Team config. |
| `--user-config` | `FLOW_USER_CONFIG` | `$XDG_CONFIG_HOME/flow/config.toml` | User config. |
| `--json` | `FLOW_JSON` | false | One JSON document on stdout; no TUI, no ANSI. |
| `--non-interactive` | `FLOW_NON_INTERACTIVE` | auto | Auto-true if not a TTY or `CI` is set. |
| `--log-level` | `FLOW_LOG_LEVEL` | warn | debug/info/warn/error (stderr). |
| `--log-format` | `FLOW_LOG_FORMAT` | text | text/json. |
| `--no-color` | `NO_COLOR`, `FLOW_NO_COLOR` | false | |
| `--ascii` | `FLOW_ASCII` | false | ASCII glyph fallback. |
| `--timeout` | `FLOW_TIMEOUT` | 2m | Overall deadline. |

## 2. Commands (complete list)

Help groups: **Setup** (init, plan, apply, attest), **Verify** (doctor),
**CI** (check pr, check deploy, check commit), **Meta** (hook, schema, version).

### `flow init`
Writes `.flow.yaml` (+ CI workflow, PR template, optional issue form, optional
hook). Flags: `--preset` (lean-kanban), `--forge`, `--tracker`, `--tracker-url`,
`--project`, `--owner` (GitHub Projects owner login), `--force`, `--no-hooks`,
`--no-templates`, `--yes`.
MUST: refuse outside git (64, hint `git init`); refuse existing config without
`--force`; succeed offline; never write secrets; never write to the default
branch remotely (local files only — the user commits them via PR).

### `flow plan`
Read-only. `--only <capability>…`, `--target forge|tracker|deploys…`,
`--tracker-override <kind>` (comparison, no network), `--out <file>`.
Exit 0 nothing to do · 2 changes/follow-ups/attestations pending · 1 error.

### `flow apply`
`--plan <file>` (refuse if stale), `--auto-approve` (required non-interactively),
`--prune`, `--no-followups`. Exit 0 converged · 2 follow-ups/inputs remain · 1 error.

### `flow attest <check-id>`
`--note` (required), `--by` (default user config handle), `--expires` (default 30d).
Refuses for API-verifiable checks (64).

### `flow doctor`
Setup checks + live-health audit (spec 07).
`--fix` (auto remediations, placeholder resolution, follow-ups), `--set k=v…`,
`--check <glob>…`, `--window <dur>` (live-health window, default 14d),
`--ci` (plain + CI annotations), `--report <file.md>`, `--write-lock`
(default true locally; in `--ci` it proposes a lock-refresh PR instead),
`--watch` (TUI). Exit 0 all pass · 2 actionable · 1 hard failure.

### `flow check pr`
The CI gate (spec 09). Runs in CI on pull request events.
Flags: `--pr <n>` (default: from CI event payload, e.g. `GITHUB_EVENT_PATH`),
`--dry-run` (evaluate and print; post nothing), `--no-transitions`.
Behaviour: evaluate policy → post `flow/policy` status → upsert/delete
explanation comment → perform flow-owned transitions for linked tickets
(spec 04 §5). Exit 0 always when the status was posted successfully (the
STATUS carries pass/fail; the CI job must not double-report), 1 if evaluation or
posting failed, 77/69 for auth/availability. `--exit-code` flag makes it exit 1
on policy failure (useful locally).

### `flow check deploy`
Runs in CI on deployment events (GitHub: `deployment_status`). Reads the
deployment from the event payload (or `--sha`, `--environment`), and if
`done_on: deployed`, performs the flow-owned `deployed` transition for tickets
linked to PRs merged at or before that SHA (spec 04 §6). Otherwise a no-op that
exits 0. `--dry-run` supported.

### `flow check commit [<file>|-]`
Lint one commit message (spec 08). Used by the optional hook. Exit 1 on errors.
`--fix` applies safe autofixes in place.

### `flow hook install|uninstall`
Optional local commit-msg hook calling `flow check commit --fix "$1"`.
Detects hook managers (lefthook, husky, pre-commit) and prints snippets instead.

### `flow schema [--kind config|lock|plan|doctor|policy]`
### `flow version`

## 3. Output

Human → stdout; logs → stderr; `--json` → exactly one JSON doc with
`"schema":"flow.<kind>/v1"`. Errors: `error:` / `why:` / `fix:` lines (P10).
Secrets never printed.

## 4. Exit codes

0 OK · 1 FAIL · 2 ACTION REQUIRED · 64 USAGE · 69 UNAVAILABLE · 77 NOPERM ·
130 INTERRUPTED. Kong parse errors → 64.

## Decisions

- **§2 `check commit` JSON kind is `flow.commit/v1`.** §3 requires every `--json`
  document to carry `flow.<kind>/v1`, but §2 names kinds only for `flow schema`.
  `commit` is the kind for this command: it describes one commit message, not
  the check that read it.
- **§2 `check commit --fix` with stdin writes to stdout.** "In place" has no
  meaning for a message read from `-`. The fixed text goes to stdout so a hook
  can pipe it onward. Under `--json` it is suppressed instead, because §3 allows
  exactly one document on stdout; the `fixed` field reports what changed.
- **§2 violations go to stdout, the P10 explanation to stderr.** The violations
  are the command's human output, which §3 puts on stdout. The `error:`/`why:`/
  `fix:` summary is the failure explanation and goes to stderr with every other
  command's. A clean message prints nothing at all: this backs a commit-msg
  hook, where silence is success.
- **§4 a lint failure exits 1 without a sentinel.** §2 fixes `check commit` at
  exit 1 on errors. None of spec 01 §5's sentinels fits — the message is neither
  missing, conflicting nor unsupported, and `ErrValidation` would map to 64. The
  command returns a `core.UserError` with no wrapped sentinel, which
  `core.ExitCode` maps to `ExitFail` by default.
- **§2 the shipped hook templates called a command that does not exist (item
  1.13).** `assets/templates/hooks/*` and `hook-managers/*` invoked `flow hook
  run commit-msg`, which no spec defines and the CLI has never had. §2 fixes
  the hook as `flow check commit --fix "$1"`, so the templates now say that,
  and `assets/templates/hooks/prepare-commit-msg` is deleted: it invented a
  second hook and a ticket-prefill feature no spec asks for. A test walks every
  template and runs each `flow …` it finds with `--help`, so a snippet can
  never again promise a command flow does not have.
- **§2 a hook manager means action required, not failure (item 1.13).** When
  lefthook, husky or pre-commit owns the repository's hooks, `flow hook install`
  prints the snippet on stdout and exits 2: flow did its half, a person must
  paste the other half. §4's ACTION REQUIRED is exactly that state. `--force`
  is the way past, and writes `.git/hooks` regardless.
- **§2 flow only ever touches a hook it wrote (item 1.13).** Its own hook
  carries an "installed by flow" marker. Installing over a marked hook upgrades
  it, installing over an unmarked one is refused unless `--force`, and
  uninstall refuses an unmarked hook outright with no override: deleting
  somebody's hook is not a thing flow should be able to do by accident.
  Installing an identical hook reports "unchanged" and rewrites nothing.
- **§2 `init` fills the preset in place rather than generating a file (item
  4.1).** A preset is mostly explanation — every key has a comment beside it
  saying what the alternatives are — and spec 03 §7 ships it so a team can read
  and edit it. Decoding to a struct and marshalling back loses every comment;
  decoding to a `yaml.Node` and marshalling back keeps the comments but loses
  their alignment, reflows the flow mappings and re-indents the sequences. So
  `config.Set` parses the document only to find out where a value is, splices
  those bytes, and verifies the result by re-reading it: the path must now hold
  the new value and no other value in the document may have changed. A value is
  accepted only once YAML tags it `!!str`, so a project number stays the string
  it was configured as. A shorter value with a comment beside it is padded back
  out to the comment's column — only a comment, never the `}` closing a flow
  mapping.
- **§2 `init` detects a forge only from hosts it can know without asking (item
  4.1).** Spec 10 §1 detects a forge past `github.com` by probing
  `/api/v3/meta`, `/api/v4/version` and `/api/v1/version`, and §2 requires init
  to succeed offline. The two cannot both hold, so init resolves `github.com`,
  `gitlab.com` and `codeberg.org` from the host alone and asks for `--forge`
  otherwise. The refusal is `ErrNeedsInput` (exit 2), not usage (64): the flag
  is needed by some repositories and not by others, so leaving it out is not a
  malformed invocation. The probing wizard is spec 12's job (item 6.1).
- **§2 `init` rebuilds `forge.url` from the remote's host (item 4.1).** A clone
  URL may carry a credential (spec 15 §2) and names a transport, while
  `forge.url` is the forge's web address, so the host is the only part copied.
- **§2 `--force` is about `.flow.yaml` (item 4.1).** The refusal to
  reconfigure is checked before anything is written, and it does not look at
  what the existing file says: a configuration that happens to match the one
  init would write is still one somebody committed. The templates are
  scaffolding rather than configuration, so an existing one is kept whether or
  not `--force` was given — reverting somebody's pull request template is not
  what starting the configuration again means.
- **§2 `init` validates before it writes (item 4.1).** The configuration is
  parsed and checked against the capability registry first, so no combination
  of flags can leave behind a file flow itself refuses to read.
- **§2 the commit-msg hook cannot fail `init` (item 4.1).** The gate on the
  pull request is the authority (spec 08) and the hook is its fast local echo,
  so a repository with a hook manager in charge, or with somebody else's
  commit-msg hook, is reported as `deferred` and left alone. `init` still
  succeeds: a repository is configured whether or not it got a hook.
- **§2 `check pr` exits 0 whenever the status was published (item 4.3).** The
  status carries the verdict; a failing job as well would report the same thing
  twice and stop auto-merge from ever seeing the red tick it is waiting for.
  `--exit-code` is the local exception, for somebody running the gate at their
  desk with no commit status to look at.
- **§2 `--pr` defaults to the CI event, and asks when it cannot tell (item
  4.3).** The payload GitHub writes is read for both shapes: a `pull_request`
  event carries the number at the top level, a `pull_request_review` only
  inside the pull request. Anything else — no variable, an unreadable file,
  JSON about something else — is `ErrNeedsInput` and exit 2. A gate that
  guessed a number would judge somebody else's work.
- **§2 the status links to the run, or to nothing (item 4.3).**
  `GITHUB_SERVER_URL`, `GITHUB_REPOSITORY` and `GITHUB_RUN_ID` are taken
  together: with any of them missing the status gets no target URL, because a
  status pointing at the wrong page is worse than one pointing nowhere.
- **§2 `check deploy` prefers its flags to the event payload (item 4.4).** The
  payload is whatever event happened to start the run and a flag was typed on
  purpose, so `--sha` and `--environment` win. Somebody re-running a job by
  hand is correcting it, and a stale `GITHUB_EVENT_PATH` left in the
  environment must not quietly decide what they just said.
- **§2 a workflow that does not track deployments needs no credential, and no
  payload either (item 4.4).** `workflow.done_on: merged` is the default, so
  for most repositories `check deploy` has nothing to do; it discovers that
  from configuration alone, before resolving a token or reading the event. A
  payload it could not read is therefore not an error in that case — the
  complaint is only worth making to somebody who was relying on the answer.
- **§2 a flag whose own item has not landed is refused, not ignored (item
  4.5).** `doctor --ci`, `--report` and `--watch` belong to items 5.8, 5.7 and
  6.3, and until those land the command exits `ErrUnsupported` when asked for
  them. Parsing a flag and doing nothing with it is worse than not having it:
  the run then looks like it did what was asked. The flags are declared now
  rather than added later so that the shipped surface is spec 02's exactly,
  and `TestUnimplementedCommandsExitFail` holds each one until its item.
- **§2 `flow attest` makes a plan before it writes a word (item 4.6).** Which
  checks may be attested is not a list in the command: it is a property of
  what the providers can be asked (spec 05 §4), and the plan already decides
  it. An id whose plan item resolves to anything but `attest` is refused with
  64, and the fix line says what to do instead — `flow apply` for a native
  change, `flow doctor --fix` for a follow-up, nothing at all for something
  that already holds. Deciding it from the same plan doctor reads is what
  stops the two commands disagreeing about one board.
- **§2 `--expires` is the last of three, not the first (item 4.6).** The flag
  has no kong default. What is asked for wins, else `attestation.default_ttl`
  from the configuration (spec 03 §2), else thirty days. A flag default would
  have silently beaten the team's own setting on every run and made it a
  comment. Both ends read the chain through one function, so the duration
  doctor prints when it asks for an attestation is the duration attest writes.
- **§2 a promise with nobody's name on it, no words in it, or no time left in
  it is refused (item 4.6).** All three are exit 64 rather than a failure,
  because all three are answered by typing something else. `--by` falls back
  to the handle in the user config; when there is none, the refusal says so
  rather than recording an unsigned promise.
