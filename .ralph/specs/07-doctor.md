# 07 — Doctor: Setup Checks and Live-Health Audit

Doctor answers two questions:
1. **Is it set up as declared?** (setup checks — configuration state)
2. **Is it actually working?** (live-health audit — what happened in the window)

Both are read-only unless `--fix`.

## 1. Check model

```go
type Check struct {
    ID        string   // stable dotted id
    Title     string
    Category  string   // config | local | auth | forge | tracker | deploys | live | followups | meta
    DependsOn []string
    Run       func(ctx context.Context, env *Env) Result
}
type Result struct {
    State        State        // pass fail input blocked external attest drift skip error
    Message      string
    Evidence     []Evidence   // {Text, URL}; live checks list offending items with links
    Remediation  Remediation  // {Kind: auto|input|followup|attest|manual|none, Summary, Command, Steps, Owner}
    ObservedHash string
    Counts       *Counts      // live checks: {Pass, Total}
}
```

## 2. States and exit

| State | Glyph | Exit |
|---|---|---|
| pass | ✓ | 0 |
| fail | ✗ | 2 (actionable) or 1 (hard) |
| warn | ⚠ | 2 — live checks below threshold but above `fail_below` |
| input | ? | 2 |
| blocked | ⏸ | 0 (dependency carries signal) |
| external | ⏳ | 2 |
| attest | ✎ | 2 |
| drift | ≠ | 2 |
| skip | · | 0 |
| error | ! | 1 (or 69/77 when it is the only failure kind) |

## 3. Placeholders

Resolution order: discover (one candidate → auto in `--fix`) → choose (picker /
`--set`) → create (if capability allows) → ask (steps; accept pasted URLs via
`ParseRef`). Verified by a read before writing to the lock.

## 4. Follow-ups

Ticket in the tracker with title `flow: <summary> (<repo>)`, body = why, exact
steps, how flow will verify, marker `flow:followup check=<id> repo=<repo>`,
labels `followups.labels`, assignee from `followups.owners`. Lifecycle:
create (after marker search) → `external` while unverified (nudge comment ≤ 1/7d)
→ verified: comment + close → closed-but-unverified: reopen with evidence →
no longer required: comment + close.

## 5. Setup checks (v1 ids)

`config.syntax`, `config.semantics`, `config.secrets`, `lock.consistent`,
`local.git`, `local.hook` (skip if hooks disabled), `auth.forge`, `auth.tracker`,
`forge.repo`, `forge.protection`, `forge.required_status`, `forge.merge_settings`,
`forge.files` (CI workflow present on default branch and runs `flow check pr`;
`flow check deploy` when done_on: deployed), `tracker.project`,
`tracker.status_options`, `tracker.labels`, `tracker.native.<event>` (per native
transition — config verify where the API allows, else attestation),
`tracker.wip` (compensated → pass with note), `tracker.expedite`,
`deploys.source` (deployment records readable for the environment),
`followups.health`.

## 6. Live-health audit (P13)

Window `--window` (default 14d). Each check lists failing items as evidence with
links. Thresholds: `pass` at 100%; `warn` below; `fail` below `fail_below`
(default 90%) or for any item marked critical.

| id | Question | Data |
|---|---|---|
| `live.prs.linked` | Did every merged PR link ≥1 ticket? (exempt: PR title type in `commits.unlinked_types`) | forge PRs merged in window + LinkedKeys |
| `live.prs.gated` | Did every merged PR have a `flow/policy` success status on its final head SHA at merge time? (critical) | commit statuses |
| `live.prs.conventional` | Is every squash commit on the default branch a valid conventional header? | git log / forge commits |
| `live.prs.reviewed` | Did agent-authored PRs get a human approval? (critical when `require_human_review`) | reviews |
| `live.deploys.recent` | Was there a successful deployment to `deploys.environment` within `max_age`? | deploy records |
| `live.deploys.lag` | Did every merged PR reach the environment within `max_lag`? (merge SHA ⊑ some deployed SHA, via forge compare API, results cached) | PRs + deploy records |
| `live.tickets.done_merged` | Is every ticket moved to Done in the window linked to a merged PR (or closed as not planned)? | tracker + forge |
| `live.tickets.done_deployed` | (`done_on: deployed`) Is every Done ticket's PR deployed? | tracker + deploys |
| `live.tickets.review_has_pr` | Does every ticket in `in_review` have an open, ready PR? | tracker + forge |
| `live.tickets.progress_stale` | Any ticket in `in_progress` with no linked PR activity for > 5 working days? (warn only) | tracker + forge |
| `live.tickets.ready_dor` | Do tickets in `ready` pass DoR? | tracker |
| `live.transitions.native` | For each native transition: did the tracker move tickets within 15 min of the event? | tracker field history where available (GitHub Projects: item `updatedAt` + current status vs event time; Jira: changelog; Linear: issue history) |
| `live.wip` | Days in window where any WIP limit was exceeded (warn) | tracker snapshot now + PR timeline approximation |
| `live.flags.cleanup` | Every `Flag-Added: <name>` trailer merged in window has an open or done cleanup ticket (marker `flow:flag name=<name>`) — created by doctor `--fix` if missing | git log trailers (git's trailer format; parsed with `git interpret-trailers --parse`, no regex) |

Doctor `--fix` performs only: placeholder resolution, native apply items,
follow-up lifecycle, flag cleanup ticket creation, and **catch-up** of missed
flow-owned transitions found by live checks. Never edits code or history.

## 7. Drift

Setup checks with `ObservedHash`: previously passing + now not matching desired
→ `drift` with before→after evidence (from cache when available). Cosmetic hash
changes that still satisfy desired are absorbed silently.

## 8. Attestations

`flow attest <id> --note …` → lock; valid ⇒ pass with "attested by …"; expiring
< 7d ⇒ pass + note; expired ⇒ `attest`; invalidated when inputs' config hash changes.

## 9. Rendering

Grouped by category; live checks show `n/m` and up to 5 evidence items (rest
summarised, full list in `--json` and `--report`). Summary line:
`N actions for you · M waiting on others · K attestations due · exit X`.
`--report` writes GitHub-flavoured markdown suitable for an issue comment.

## 10. Where it runs

Locally on demand; on PRs touching `.flow.yaml`/`.flow.lock` (`--ci`, setup
checks only via `--check 'config.*,lock.*,forge.*,tracker.*'`); daily scheduled
(`--ci --fix --report`), posting the report to a pinned "flow health" issue
(marker `flow:health`) — updated in place.
