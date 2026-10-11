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

## Decisions

- **§2 only a pass and a skip let the checks behind one run (item 2.8).** A
  check is blocked when something it depends on ran and did not hold. A skip
  does not block: a check that does not apply to this configuration has
  nothing to say about anything downstream of it, and treating it as a failure
  would hide every forge check behind a workflow the team switched off. Because
  a blocked check is itself recorded as blocked, and blocked does not satisfy,
  this carries down a chain of any length with no second rule.
- **§2 a dependency that was not run does not block (item 2.8).**
  `--check 'forge.*'` is the user narrowing the question, so a check is asked
  even though `auth.forge` was not in the selection. Refusing to look because
  of a check nobody asked for would answer a different question and report it
  as if flow had found something.
- **§2 a failure is actionable when it carries a remediation (item 2.8).** The
  table gives `fail` two exit codes and does not say what tells them apart.
  What tells them apart is whether flow has anything to suggest: a failure
  with a remedy is somebody's afternoon and exits 2, and a failure with
  `none` or no remedy at all is flow saying something is wrong that it cannot
  help with, which is a hard failure and exits 1.
- **§2 errors are only weather when nothing else is wrong, and only when they
  agree (item 2.8).** A run whose sole failing kind is `error` exits 69 or 77
  when every error maps to the same one of those codes, and 1 otherwise. A
  real failure standing behind an unreachable provider must not be reported as
  a rate limit, and "unavailable here, forbidden there" is not one condition.
  Working this out needs the error itself, so `Result` carries an `Err`
  alongside the state the table lists.
- **§1 the zero state is not a state, and a check that panics is an error
  (item 2.8).** A half-written check that returns nothing would otherwise read
  as a pass, and a doctor that dies on one unexpected nil says nothing about
  the twenty things that were fine. Both become `error` on that check alone
  and the run carries on, because the point of doctor is to say everything
  that is true about the repository in one pass.
- **§1 the registry refuses at startup what it could not run (item 2.8).** A
  check with no id, no title, nothing to run, a category nothing renders, a
  duplicate id, a dependency that is not a check, or a cycle is refused when
  the registry is built. A doctor that falls over half way through has told a
  worse story than one that would not start. Order is a topological sort of
  registration order, so a dependency added later cannot quietly put a check
  in front of what it depends on.
- **§9 `--check` refuses a pattern that matches nothing (item 2.8).** Running
  no checks and exiting 0 is indistinguishable from health, which is the one
  answer doctor must never give by accident. A typo in `--check` is a usage
  error (64).
- **§9 "N actions for you" counts errors (item 2.8).** The summary line has
  three counts and an exit code, and `error` fits none of them. It is counted
  as an action: not knowing whether the trunk is protected is something for
  the person running doctor to look into, and leaving it out would print "0
  actions for you · exit 69".
- **§9 the report groups by the category order of §1 (item 2.8).** config,
  local, auth, forge, tracker, deploys, live, followups, meta — which is
  roughly the order things have to be true in, so a reader meets the cause
  before the consequences. Within a category, checks are in the order they ran.
- **§6 `fail_below` has no key in the configuration yet (item 2.8).** The
  threshold is a field on the doctor environment, defaulted by the caller, and
  grading is one function: everything passing is a pass, below the threshold is
  a failure, in between is a warning, and a check marked critical has no
  warning band at all. Where the number is written down in `.flow.yaml` is for
  the item that adds the live checks (5.1); spec 03 does not have it yet, and
  this is recorded as a spec issue rather than invented here.
- **§3 the lock and `--set` are consulted before discovery (item 2.9).** The
  written order is discover → choose → create → ask, but discovery auto-takes a
  single candidate under `--fix`, which would silently overrule a user who had
  just said which board it was. An identifier flow already verified, and one a
  human typed on the command line, are both better answers than one flow picked
  for them, so the ladder is: lock → set → discover → create → ask. Discovery
  keeps its position relative to the rungs the spec names.
- **§3 everything is read back from the provider before it is recorded (item
  2.9).** Including an id the user passed with `--set`: a typo in a pasted
  value, written to the lock, turns every later run into a mystery about a
  board that is not there. The reference recorded is the one the provider
  described, so the name and URL in the lock are the tracker's own rather than
  whatever was typed.
- **§3 creation is its own read-back (item 2.9).** The item a provider returns
  from a create call is that provider describing what it just made, so a second
  look would only be a second chance to fail. Nothing unverified is recorded by
  this route either.
- **§3 a provider that will not answer does not cost flow what it knows (item
  2.9).** When the lock has an identifier and the read-back fails — rate limit,
  outage, a token that lost a scope — the locked answer is kept and the reason
  is carried on the answer. Forgetting it would turn a transient failure into a
  question for a human and make `flow doctor` destructive when offline. Only a
  tracker that answers, and does not list the thing, retires an identifier.
- **§3 `--set` does not require `--fix` (item 2.9).** Naming the board is the
  human's decision already made; what `--fix` licenses is flow choosing on
  their behalf — taking the only candidate, asking the picker, creating. So a
  plain `flow doctor --set tracker.project=…` records the answer, and a plain
  `flow doctor` with one candidate reports what it would have taken and writes
  nothing.
- **§3 a picker that cannot ask is a fall-through, not a failure (item 2.9).**
  `core.ErrNeedsInput` from the chooser means no TTY, or a user who pressed
  escape; resolution carries on to the written-down question. Any other error
  is the picker going wrong, and is reported.
- **§3 the five placeholder paths are walked by hand (item 2.9).**
  `tracker.project` and the four `followups.owners.*`. No reflection: the list
  is short, and walking it explicitly means a sixth has to be thought about
  rather than appearing by accident. Only `tracker.project` is discoverable —
  no API says whether a handle is the right person to ask about board
  administration, so owners are recorded as typed.
- **§3 a created board is named after the repository (item 2.9).** Flow is not
  in a position to invent a better name, and the repository's own is the one a
  human would have typed. Creation is attempted only when the declaration says
  `set: true` and the probe reports the tokens it requires, so the common case
  — github-projects, Jira and Linear all declare project creation as an
  administrator's job — never reaches a request that would be rejected.
- **§3 answers go in the lock, never back into `.flow.yaml` (item 2.9).** The
  configuration is the team's intent and an identifier the tracker made up is
  not part of it; writing it back would change a reviewed file without review.
  The `${pending:…}` string stays as written.
- **§3 an unresolved placeholder is `input`, not `fail` (item 2.9).** Nobody
  has decided yet, which is a question for a human and exit 2. A provider that
  could not be asked at all is `error`: that is a fault to report, not a
  decision to make.
