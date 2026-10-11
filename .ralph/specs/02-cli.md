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
