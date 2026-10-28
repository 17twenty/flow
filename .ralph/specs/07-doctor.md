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

Ticket in the tracker with title `flow: <summary> (<repo>)`, body = what the
thing is and where it lives (from the capability's `by_hand` recipe, spec 05
§1), why, exact steps, how flow will verify, marker
`flow:followup check=<id> repo=<repo>`,
labels `followups.labels`, assignee from `followups.owners`. Lifecycle:
create (after marker search) → `external` while unverified (nudge comment ≤ 1/7d)
→ verified: comment + close → closed-but-unverified: reopen with evidence →
closed as not planned by a person: leave closed, comment once, `warn` until the
configuration agrees (#265) → no longer required: comment + close.

## 5. Setup checks (v1 ids)

`config.syntax`, `config.semantics`, `config.secrets`, `lock.consistent`,
`local.git`, `local.hook` (skip if hooks disabled), `auth.forge`, `auth.tracker`,
`forge.repo`, `forge.protection`, `forge.required_status`, `forge.merge_settings`,
`forge.squash_text` (the squash commit says what the pull request said),
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
| `live.prs.current` | Is any PR that passed the gate waiting only on being behind its base? `--fix` brings it up to date (issue #234) | open PRs + gate status |
| `live.prs.closed_issues` | Did every merge close the issues it names? `--fix` closes any it left open | PR closing references + issue state |
| `live.trunk.built` | Did every merge start one of `forge.required_checks` on its merge commit? (spec 20 §6) | commit statuses + check runs |
| `live.deploys.recent` | Was there a successful deployment to `deploys.environment` within `max_age`? | deploy records |
| `live.deploys.lag` | Did every merged PR reach the environment within `max_lag`? (merge SHA ⊑ some deployed SHA, via forge compare API, results cached) | PRs + deploy records |
| `live.tickets.shared_board` | Is every repository whose tickets are on this board named in `tracker.shared_with`? | tracker |
| `live.tickets.done_merged` | Is every ticket moved to Done in the window linked to a merged PR (or closed as not planned)? | tracker + forge |
| `live.tickets.done_deployed` | (`done_on: deployed`) Is every Done ticket's PR deployed? | tracker + deploys |
| `live.tickets.review_has_pr` | Does every ticket in `in_review` have an open, ready PR? | tracker + forge |
| `live.tickets.review_stuck` | Any approved PR in `in_review` the forge would not take — conflicted, or behind a trunk the protection requires it to be level with? | tracker + forge |
| `live.tickets.review_stale` | Any ticket in `in_review` with no activity for > `health.stale_after` working days? (warn only) | tracker + forge |
| `live.tickets.progress_stale` | Any ticket in `in_progress` with no linked PR activity for > `health.stale_after` working days (default 5)? (warn only) | tracker + forge |
| `live.tickets.ready_dor` | Do tickets in `ready` pass DoR? | tracker |
| `live.tickets.blocked_explained` | (`workflow.blocked.require_comment`) Does every open blocked ticket carry a comment somebody other than flow wrote? | tracker |
| `live.transitions.flow` | For each flow-owned transition: is any ticket somewhere flow should have moved it from? (`--fix` catches it up) | tracker + forge + the transition engine |
| `live.transitions.native` | For each native transition: did the tracker move tickets within 15 min of the event? | tracker field history where available (GitHub Projects: item `updatedAt` + current status vs event time; Jira: changelog; Linear: issue history) |
| `live.wip` | Days in window where any WIP limit was exceeded (warn) | tracker snapshot now + PR timeline approximation |
| `live.flags.cleanup` | Every `Flag-Added: <name>` trailer merged in window has an open or done cleanup ticket (marker `flow:flag name=<name>`) — created by doctor `--fix` if missing | git log trailers (git's trailer format; parsed with `git interpret-trailers --parse`, no regex) |
| `live.flags.age` | Every open cleanup ticket whose flag is older than `health.flag_max_age` (90d) carries `health.flag_kept_label`, i.e. the flag went or the team said it stays | tracker (the marker's `added=` day, written from the commit) |
| `live.agents.rework` | How much agent work merged in the window, and how much of it was asked for changes at least once before it did (reported, never graded) | forge PRs merged in window + reviews |

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
A provider note marked `refutes` outranks any word: the check fails with a
manual remedy, and no attestation is asked for or honoured.

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
- **§4 the marker is asked before the lock is believed (item 2.10).** Every
  round starts with `FindFollowUp`, whatever the lock says. A lock that lost
  its record — a bad merge, a fresh clone of a branch that never had it — must
  not cause a second ticket for the same thing, and a lock that claims a ticket
  somebody deleted must not stop flow filing a real one. The lock is the thing
  most likely to be wrong, so it is the thing that gets corrected.
- **§4 a ticket found without a record is adopted silently (item 2.10).** It is
  written into the lock and not chased, because flow does not know how long it
  has really been outstanding; the nudge clock starts at the adoption. Chasing
  on sight would mean a comment on every fresh clone.
- **§4 the nudge is one comment, edited (item 2.10).** Six weeks of chasing is
  one comment that says "42d", not six comments. Because the comment is edited
  in place, the tracker cannot say when flow last chased without reading every
  comment back, so the lock records it: `followups[].nudged_at`, absent until
  there has been one. A key whose value is the zero time is a line in a
  reviewed file that says nothing.
- **§4 an open follow-up makes the check `external`, not `fail` (item 2.10).**
  The work is on somebody's plate with their name on it and a ticket to watch,
  which is a different fact from "broken" and is not something the person
  running doctor can act on. Reporting it as a failure every morning for a year
  is what makes people stop reading the report. It still does not satisfy, so
  it still costs the run its clean exit.
- **§4 "no longer required" is not "verified" (item 2.10).** Both end in a
  comment and a close, but a check that now holds closes as `completed` with
  what flow saw, and a check that stopped wanting a ticket — flow can do it
  itself now, or the configuration stopped asking — closes as `not_planned`
  saying which. Closing the second as completed would record work as done that
  nobody did.
- **§4 a follow-up closed without the work being done is reopened, with
  evidence (item 2.10).** This is the whole value of verifying rather than
  trusting the ticket: a closed follow-up nobody acted on is how a workflow
  quietly stops being true. The comment says what flow still sees and says how
  to stop being asked — change the configuration — because otherwise the reopen
  reads as flow arguing with a human.
- **§4 flow does not take a ticket back off whoever picked it up (item 2.10).**
  The assignee on the ticket wins over the owner the configuration routes to.
  Somebody reassigned it because they knew better than the configuration did,
  and re-assigning every morning would be a daily argument. The lock follows
  the ticket.
- **§4 an unfilled owner role falls back to the default, and an unfilled
  default is nobody (item 2.10).** Assigning a ticket to the literal string
  `${pending:owners.platform}` would be rejected by every real tracker; an
  unassigned ticket with the right label, in the right tracker, is still
  useful. Owners are resolved from the configuration first and from what
  doctor resolved into the lock second, which is what makes
  `--set followups.owners.platform=@sre` take effect.
- **§4 failing to file is not reported in place of what it was about (item
  2.10).** A tracker that will not answer leaves the check's own finding
  intact and carries the reason separately. Being unable to open a ticket
  about a problem is not a second problem to report instead of the first.
- **§4 a lock record for a follow-up nobody wants and nothing holds is dropped
  (item 2.10).** The lock is a statement of what is true, not a changelog of
  every ticket flow ever filed.
- **§7 drift needs a remembered good state, and keeps it (item 2.11).** The
  lock's `observed[check]` is written when a check holds, and that is the only
  "before" flow has. Two consequences. A failure with nothing recorded is not
  drift — it is a thing that was never set up, and calling it drift would be
  flow inventing a history it does not have. And a drifted check does *not*
  update the remembered hash: if it did, tomorrow's run would see "unchanged"
  and the drift would be reported once and then forgotten, which is the
  opposite of what a daily check is for. The memory is updated when the thing
  is right again.
- **§7 "cosmetic" means "still satisfies" (item 2.11).** A changed hash whose
  check still holds is absorbed silently and the new hash recorded, so the next
  real change is measured from what is there now. A tracker that renamed an
  internal field id or a forge that reordered a list is not news, and reporting
  the churn would train people to ignore the findings that matter.
- **§7 the "after" is the plan's own changes (item 2.11).** Spec §7 asks for
  before→after evidence "from cache when available"; flow has no observation
  cache and spec 01's cache is the HTTP ETag one. What flow does have is the
  plan item for the same check, whose changes are what would have to happen to
  put the thing back — which is the after a human can act on, rather than a
  second hash they cannot. The hashes are shown too, truncated to 12
  characters: enough to tell two states apart by eye, short enough for prose.
- **§8 an attestation records what it covered (item 2.11).**
  `attestations[].inputs` is the canonical hash of the desired item the check
  is about, taken when the attestation is made. "Invalidated when inputs'
  config hash changes" is read as *those* inputs: changing `commits.scopes`
  must not throw away somebody's word about a saved view on the board. An
  attestation with no `inputs` — written by an older build — has nothing finer
  to compare and lapses with the whole lock's ConfigHash, which is the old rule
  and the safe one.
- **§8 only a check that asks to be attested can be (item 2.11).** `Attest`
  applies a promise only when the finding's remedy is `attest`; if the API
  answers, the answer is better evidence than anybody's word. Without this
  gate, `flow attest forge.protection` would be a way to swear away a real
  failure, and the whole mechanism would be a silencer rather than a record.
- **§8 one check has one attestation (item 2.11).** A second person vouching
  replaces the first rather than stacking up: two promises with different
  expiry dates is a question about which one counts, and the answer would have
  to be invented.
- **§8 a promise with no expiry is not a promise (item 2.11).** When the
  configuration gives no `attestation.default_ttl`, flow uses thirty days —
  long enough not to be a chore, short enough that nobody is relying on
  something a departed colleague checked two years ago. (This entry said
  ninety until issue #220; the code, spec 02 §2 and spec 03 always said
  thirty, and `engine.DefaultAttestationTTL` is the one value.) The notice window is
  seven days, which is enough to renew deliberately rather than in a hurry on
  the morning a check turns red; an attestation inside it still passes and
  carries the renewal command.
- **§4 the marker search runs before every filing, and flow remembers its own
  (item 3.11).** GitHub's search index is seconds to minutes behind its
  writes, which is exactly the span of one doctor run: file a follow-up, ask
  the index whether one exists, be told no, file it again. So the adapter
  keeps what it filed in this process and answers from that first. The marker
  in the body stays the truth — the search is only a filter over it, and every
  candidate's body is checked — because an index that answers with too much is
  harmless and one that answers late is not.
- **§4 a follow-up is put on the board as it is filed (item 3.11).** Filing
  ends with `addProjectV2ItemById`, which is the only place flow adds an issue
  to a project. A follow-up that is not on the board is a ticket nobody is
  looking at, and the lifecycle in §4 assumes somebody will see it. A board
  flow has no number for yet is skipped rather than refused: setup files
  follow-ups before it has chosen one.
- **§4 a label or owner the forge has not got is left off, not fatal (item
  3.11).** `followups.labels` may name a label the apply that creates it has
  not reached, and `followups.owners` may name somebody who has left. Both are
  how people find flow's follow-ups rather than what makes one true, so an
  unlabelled, unassigned ticket somebody can see beats no ticket at all.
- **§4 nudging is one comment that keeps changing (item 3.11).** "nudge
  comment ≤ 1/7d" is read as one marked comment edited in place, not a weekly
  new one: six weeks of a thing being wrong should read as one conversation.
  The ticket's comments are read through every page before writing, since
  flow's is the oldest on a ticket that has been nudged for weeks.
- **§4 closing a closed ticket writes nothing (item 3.11).** Doctor runs
  daily, and a second close would move `closedAt` — which §6 reads to decide
  whether a follow-up is overdue, so repeating the close would make a stale
  follow-up look permanently fresh. Reopening an open ticket is likewise no
  work.
- **§4 the reason a ticket was reopened is a comment, not a field (item
  3.11).** GitHub records nothing about why an issue was reopened. Rather than
  drop the reason, the engine writes it where a human can read it; the adapter
  takes the reason and does not pretend to store it.
- **§5 a provider check asks the plan, not the provider (item 4.5).** Spec 06
  §1 makes `tracker.status_options` the check and `tracker.status_options` the
  plan item the same question, so the check reads the plan: a plan that needs
  no change is a pass, a native change is a failure flow can fix, a change
  flow is not allowed to make is a follow-up, and an unsupported one is a
  skip. Observing once and deciding once is what keeps doctor and apply from
  disagreeing about the same board. Only `auth.forge`, `auth.tracker` and
  `forge.repo` reach for a network, because reaching for one is their whole
  subject.
- **§5 a configuration that does not parse is a report, not a refusal (item
  4.5).** Every other command stops on an unreadable `.flow.yaml`, because it
  cannot do its job without one. Doctor's job *is* to say which part is wrong,
  so it records the parse error as `config.syntax`, the semantic error as
  `config.semantics`, and blocks what stands behind them rather than failing
  twenty checks about a file nobody could read. Loading stops there as well:
  planning a workflow just found incoherent means asking a provider flow never
  connected to, and the warning that produces blames the wrong thing.
- **§5 the credential scan is the redactor (item 4.5).** `config.secrets` asks
  whether [core.Redact] changes the file rather than keeping a second list of
  token shapes. The shapes flow knows how to keep out of a log are exactly the
  shapes it should refuse to find in a committed file, and one list cannot
  drift from another that does not exist. The refusal names the lines that
  changed and never what was on them.
- **§5 reachability and the trunk are one check (item 4.5).** A slug that is
  wrong, a repository that was renamed and a credential that cannot see it all
  produce the same reply from the forge, so `forge.repo` reports one problem
  once instead of marking both "the repository is not there" and "its default
  branch is not the workflow's".
- **§5 a credential that allows nothing still passes (item 4.5).** `auth.*`
  asks whether the token resolves and is accepted. What it is permitted to do
  is the planner's business item by item (spec 05 §4), so a missing permission
  becomes a follow-up on the one thing it blocks rather than a red mark
  against the whole provider.
- **§5 an unanswered board blocks the tracker checks (item 4.5).** Every
  tracker check depends on `tracker.project`, so a board flow has not been
  told which one it is reads `input` once and blocks the rest, rather than
  failing them each in turn about the same missing answer.
- **§5 a follow-up open for thirty days stops being healthy (item 4.5).**
  `followups.health` passes while flow's own asking is moving and reads
  `external` once the oldest open follow-up has been outstanding a month: long
  enough that a team on holiday does not come back to a red check, short
  enough that a ticket nobody intends to do is noticed in the quarter it was
  filed.
- **§8 the lock is stamped with the configuration doctor read (item 4.5).**
  `--fix` writes `config_hash` as it writes the lock, which is what makes a
  promise taken against the whole file lapse when the file is rewritten
  (spec 03 §3). A lock whose hash stayed empty would carry every attestation
  through a rewrite of the thing it was attested about. Editing the workflow
  is not itself a fault, so `lock.consistent` still passes and says so as
  evidence.
- **§9 the exit code is in the document as well as on the summary line (item
  4.5).** `--json` carries `exit_code`, derived from the results when the
  report is written rather than stored next to them, so the two cannot come to
  disagree. The summary line a person reads ends in `exit X`; something
  reading the document instead of the process's status deserves the same
  answer without re-deriving the verdict from twenty states.
- **§1 one candidate is not a choice (item 4.5).** Where a discoverable
  placeholder has exactly one candidate and `--fix` was not given, the check
  still reports `input` — nothing may be written without leave — but the
  remedy asks for the leave (`flow doctor --fix`) rather than for the answer
  (`--set <path>=<value>`). Flow is holding the id; telling a person to go and
  retype it puts a decision in front of them that has already been made.
- **§8 attest writes the promise and nothing else (item 4.6).** The lock's
  `config_hash` is left exactly as doctor stamped it. Stamping it here would
  re-validate every older attestation that had lapsed with the configuration —
  the ones with no `inputs`, which can only be retired by that hash moving —
  on the word of somebody who was vouching for a single check. Doctor may
  stamp it because doctor has looked at all of them; attest has looked at one.
- **§8 a promise names what it covered, and lapses with that (item 4.6).**
  Every attestation flow writes carries the canonical hash of the desired item
  it was made about, so rewriting one part of a workflow retires the promise
  about that part and leaves the rest standing. The coarse rule — lapse with
  the whole configuration — survives for attestations written before flow
  recorded this, which have no hash to compare.
- **§8 one check has one attestation (item 4.6).** A second person vouching
  takes the first's place rather than standing beside it, and the run says
  whose word it displaced and whether that word still stood. Two promises
  about one check would leave flow choosing between them with no rule to
  choose by.
- **§5 a plan flow could not make is not a skip (live fix, after item 4.6).**
  An empty plan and a plan that wants nothing are the same bytes, and the
  second of those is a skip, so the first live run of doctor against flow's
  own repository reported twenty checks as "this configuration does not ask
  for it" and exited 0 having seen nothing. The failure to observe is carried
  into `Env.PlanErr`, and every check that reads the plan reports it. The
  checks that read it first — `forge.repo` and `tracker.project` — say what
  went wrong, and everything behind them is blocked rather than told the same
  thing five more times.
- **§5 unless a placeholder is still pending.** A board nobody has identified
  is why there is no plan, `tracker.project` already reports it as `input`,
  and that already holds the run at exit 2. Repeating one unanswered question
  as twenty red lines would bury it, so the plan error is carried only when
  nothing is pending to explain it (scenario A05).
- **§3 resolving an answer rebuilds the providers that needed it.** An adapter
  is built from what flow knew when it connected, and for `github-projects`
  that includes the board's number. `--fix` writing a discovered number to the
  lock leaves the live tracker pointing at project zero, so the one run that
  found the answer was the one run that could not use it. The providers are
  rebuilt after resolution and before anything is asked.
- **§6 the window is read once, lazily, for all four checks (item 5.1).**
  `LiveChecks` returns a slice built together over one `audit`, so the
  fortnight's pull requests and the trunk's commits are fetched at most once a
  run. Four checks each asking the forge for the same fortnight would be four
  times the rate limit and, worse, four slightly different fortnights. Lazily,
  because `--check live.prs.conventional` must not pay for the pull requests it
  is never going to read.
- **§6 the window is about when work merged, not when it was touched
  (item 5.1).** The query is by update time, because that is what every forge
  indexes, but merging is not the last thing that can happen to a pull request
  — a comment afterwards moves it too. So the window is applied a second time,
  to `merged_at`, which is the date the audit is actually about.
- **§6 a quiet fortnight is not ill health (item 5.1).** Nothing looked at is a
  pass, and the check says "nothing merged in the window" rather than "all 0
  merged pull requests named a ticket". A dashboard that cannot tell a quiet
  fortnight from a healthy one is telling nobody anything.
- **§6 a live check never offers to fix what it found (item 5.1).** A pull
  request that merged ungated last week cannot be gated now. Every remediation
  is `manual` and names the setup check that would stop the next one —
  `forge.required_status`, `forge.protection`, `forge.merge_settings` — which
  is also what keeps `--fix` to the five things §6 allows.
- **§6 `live.prs.conventional` reads the trunk, not the pull request titles
  (item 5.1).** The title is only the commit header when the merge was a
  squash, and a merge that was not a squash is exactly the case where the
  header the gate checked is not the one that landed. It lints through
  `internal/commits`, so the commits git composed itself are exempt by the
  same rule the hook uses rather than by a second copy of it.
- **§6 `live.prs.reviewed` is critical whenever it runs (item 5.1).** A team
  that has not asked for a human review is not audited for one — there is no
  claim to hold them to, which is the answer the gate's `agent.human_review`
  rule gives too — so `require_human_review` decides whether the check exists,
  not whether it is critical. When it runs there is no warning band: one agent
  merge in twenty with nobody's approval is a failure.
- **§6 the audit and the gate read one definition of agent work (item 5.1).**
  `agentAuthored`, `humanApprover` and `unlinkedType` are shared with spec 09's
  rules rather than reimplemented. Two mechanisms answering the same question
  in different code drift apart silently, and the drift would show up as a
  report that disagrees with the gate about the same pull request.
- **§6 with no tracker the forge's own closing references are the link
  (item 5.1).** `LinkedKeys` is the tracker's, because only it knows its own
  convention, but a run that could not reach the tracker still knows which
  pull requests named an issue. Calling all of them unlinked would report the
  team's fortnight as broken when it was flow that could not look.
- **§6 the audit names things in its own order (item 5.1).** Merged pull
  requests are sorted by number before they are counted, whatever order the
  forge's index answered in. Item 5.7 edits this list into an issue in place,
  and evidence that reshuffles between runs is noise in every diff of it.
- **§6 the board is read once and bucketed, never queried column by column
  (item 5.3).** Five checks each asking the tracker for one column would be
  five walks of a board that pages, and the column a ticket is in has to be
  translated out of the tracker's own vocabulary anyway. One `Tickets` call
  feeds all five, and `ColumnOf` maps a Projects option id or a Jira column
  onto flow's `in_review` and `ready` through the lock, so renaming a column
  in the tracker does not change which question doctor is asking.
- **§6 a column flow cannot name is not audited (item 5.3).** Every ticket
  check finds its column through the transition that lands work there —
  `pr_ready_for_review` for review, `pr_opened_draft` for in progress, the
  Definition of Ready gate for ready — rather than by matching an id. A
  workflow that never says where review happens has no review column, and
  skipping is the honest answer: judging the whole board instead would report
  every ticket on it as missing a pull request.
- **§6 only the Done check has a window (item 5.3).** The other three are
  about the board as it stands. A ticket that has sat in review with nothing
  to review for a month is not less wrong for having arrived before the
  fortnight began, and "in review since before the window" is not a thing a
  tracker will tell you.
- **§6 a ticket's Done date is its close, falling back to its last edit
  (item 5.3).** A Done column holds everything a team has ever finished, so
  without a date the check would judge last year's work against this
  fortnight's merges. A board column is a view and not the issue's state,
  though, so Done also holds items the tracker still calls open and which have
  no close date at all: the last edit is the only thing there is, and
  exempting them would let the whole check be dodged by never closing
  anything.
- **§6 the two sides of the link are folded to one spelling (item 5.3).**
  Trackers differ on the case of their own keys — Jira writes `PROJ-12` on the
  board and accepts `proj-12` in a branch — so the index from ticket to pull
  request is built on a folded key. Without it a ticket with a merge behind it
  is reported as having none because somebody typed it in lower case.
- **§6 `done_merged` stays the literal question, and the remedy names the way
  out (item 5.3).** Work pushed straight to the trunk closes a ticket with no
  pull request to point at, and that is the finding rather than a false one:
  whether work *may* reach the trunk that way is `forge.protection`'s
  question, asked once of the repository, so the remediation points there.
  flow's own repository in `direct` mode fails this check, which is what
  spec 17 §5 Stage B exists to settle.
- **§6 `done_deployed` says nothing about a ticket with no merge at all
  (item 5.3).** That is `done_merged`'s question, and counting it twice would
  report one hand on the board as two failures, so a team fixing it would see
  only half the number move. A ticket that does have merges is running only
  when all of them are (spec 04 §5 rule 4): one half of a two-part change left
  behind on the trunk is exactly the state "done" is claiming is over.
- **§6 `progress_stale` warns and never fails (item 5.3).** Work goes quiet
  for reasons flow cannot see — leave, an incident, a dependency on somebody
  else — and a check that failed a repository for one of them is a check
  somebody turns off. Its bound is in working days, with Saturday and Sunday
  as the weekend in whatever zone the clock is in: flow does not know a team's
  calendar and will not ask for one, and the furthest this can be wrong is the
  two days of a different weekend, in the one check whose worst outcome is a
  warning.
- **§6 a pull request moving is the ticket moving (item 5.3).** The ticket
  itself is often untouched for a week while its branch is reviewed twice a
  day, so staleness reads the later of the two. A tracker that records no
  update time at all is not asked the question — the zero date would report
  every ticket on such a board as years stale — and a column of nothing but
  those says it has no date to judge by rather than claiming to be empty.
- **§6 the ready column is the one gated on the Definition of Ready
  (item 5.3).** The gate is what makes it the ready column: a team that
  renamed theirs has not stopped meaning it, and a team that gates no column
  on readiness has not asked the question of their board at all. The check
  uses the same `Judge` the gate does, so a board full of work that will be
  refused when somebody picks it up is the same failure found a day earlier,
  in the same words.
- **§6 the board is named in flow's own order (item 5.3).** Tickets are sorted
  by key before they are counted, whatever order the tracker answered in, for
  the reason item 5.1 sorted pull requests by number: item 5.7 edits this
  evidence into an issue in place, and a list that reshuffles between runs is
  noise in every diff of it.
- **§6 one check per native transition, not one for all of them
  (item 5.4).** The table names `live.transitions.native` and spec 04 §5 names
  `transitions.native.<event>`; the per-event reading wins, as
  `live.transitions.native.pr_merged`, mirroring the setup side's
  `tracker.native.<event>`. A tracker's built-in workflows are switched on one
  at a time — "Item closed" on, "Pull request merged" off is an ordinary state
  — and a single verdict could not say which, nor point at the attestation
  that covers it.
- **§6 the audit reads the board as it stands, not field history (item 5.4).**
  A Projects item's history is not in the API at all; Jira keeps a changelog
  and Linear an issue history. A check whose question changed shape per
  tracker would be three checks wearing one id. What every tracker does say is
  where a ticket is now, and that is enough: fifteen minutes after an event, a
  ticket that has not reached the column the event was supposed to take it to
  is a transition that did not happen.
- **§6 the grace period is the whole tolerance (item 5.4).** Spec §6's fifteen
  minutes is applied to the event, not to the move: an occurrence younger than
  that is not judged at all, so a merge two minutes old whose webhook is still
  in flight is invisible to doctor rather than a failure that clears itself.
- **§6 a forward transition is verified at-or-past, a back-move at-or-before
  (item 5.4).** The target column is where an event sends a ticket, not where
  it must stay: work that merged and then went further on has not been left
  behind. `changes_requested` is the one native transition that goes against
  the board's order, so its question is the mirror — a ticket further on than
  the column the review should have returned it to is the failure.
- **§6 flow verifies only the events it can date (item 5.4).** `pr_merged`,
  `issue_closed`, `pr_opened_draft` and `changes_requested` are dated exactly,
  from the merge, the close, the open and the review. Nothing records when a
  pull request was marked ready for review, so the last time anything happened
  to it stands in — always at or after the real moment, which makes the grace
  period generous rather than harsh. An event with no date at all is skipped
  with that as its reason: a vacuous "nothing in the window" would be a green
  tick for a question nobody asked, which is how an audit comes to mean
  nothing.
- **§6 the tracker is not blamed for a board it was not given (item 5.4).** A
  ticket in a status the workflow does not name is one flow cannot place
  (spec 04 §5 rule 3), and a ticket nobody added to the project comes back the
  same way — one rule for "flow does not know where this is" rather than two.
  Neither is counted, and when nothing else was, the message says so rather
  than claiming the fortnight was quiet.
- **§6 a ticket whose work is not finished is not a missed transition
  (item 5.4).** Spec 04 §5 rule 4 says a ticket is done when all of its work
  is, so an automation that left a ticket alone while a second pull request
  was still open did the right thing. Demanding the move would be asking the
  tracker to make the board lie.
- **§6 the remedy for a missed native transition is never a fix (item 5.4).**
  Last Tuesday's merge cannot be re-fired. The way out is to turn the built-in
  workflow back on and re-attest, or to take the event off the tracker —
  `owner: flow` — which puts it in `flow check pr` and lets doctor's catch-up
  repair what CI misses. The remediation names the `tracker.native.<event>`
  attestation this check is the evidence against.
- **§6 the deployment checks are switched on by `deploys.source`, not by
  `done_on` (item 5.2).** A team whose work is not finished until it is
  deployed has `live.tickets.done_deployed` for that. A library that finishes
  work on the merge still wants to know every merge was released, which is
  what spec 13 §2 gives `max_lag` for and what this repository uses. So the
  only exemption is having no records to read — `deploys.source: none`, or no
  `deploys.environment` to ask about — and `deploys.source` in the setup half
  is now asked on the same condition, because gating the readability check on
  `done_on` would skip it for exactly the repositories whose live audit
  depends on it.
- **§6 `recent` may fail and `lag` may not (item 5.2).** They divide the
  question: `recent` asks whether the pipeline is alive at all, which is one
  fact about an environment and a failure when the answer is no, and `lag`
  asks whether it is keeping up, which is a share of the merges and never
  worse than a warning. Nothing in the setup can make a release faster, and a
  check that could fail the daily run for slowness is one a team turns off.
- **§6 `recent` counts nothing (item 5.2).** It is a single fact about an
  environment rather than a sample of items, and "1/1" on a dashboard would
  say less than naming the commit and the day does.
- **§6 a merge inside its objective is set aside; one already running is
  credited (item 5.2).** A pull request that merged a minute ago has not
  missed a two-hour objective, so it is not judged — the same tolerance spec
  04 §5 gives a webhook still in flight. But being inside the objective only
  excuses what has not shipped yet: a team that deploys in minutes merges
  everything inside its own objective, and setting those aside first would
  leave the check counting nothing and passing for ever.
- **§6 both deployment checks read one history (item 5.2).** The records are
  read once, over the longer of `--window` and `max_age`: `lag` is about the
  merges of the window and `recent` about a gap a team may well allow to be
  longer than one. Two reads of overlapping spans would be two round trips for
  one history and, worse, two slightly different answers about what is
  running.
- **§6 the catch-up is found by a live check of its own, `live.transitions.flow`
  (item 5.5).** "Missed flow-owned transitions found by live checks" names no
  check in the table that finds them: the native family audits the moves flow
  declines to make, and the ticket family asks about columns rather than
  events. Adding one check is the smallest change that makes the sentence
  true, and it mirrors `live.transitions.native.<event>` from the other side.
- **§6 it is one check for every event, where the native side is one per
  event (item 5.5).** The native side can split because each event is a
  separate claim about a separate automation. Here there is one decision —
  `Transition` folds everything a ticket's work says about it into the single
  column the ticket belongs in — and one act that settles it. Splitting would
  run that fold once per event, count the same ticket as many times as it had
  work, and leave flow deciding one event at a time what the engine is
  careful to decide whole.
- **§6 the catch-up delegates to the transition engine rather than restating
  it (item 5.5).** It builds the same snapshot `flow check pr` builds and
  performs the same `TicketMove`. Everything spec 04 §5 says therefore holds
  without being written twice: a native transition is not flow's to perform,
  a ticket in a column the workflow does not name is not flow's to move out
  of, work that is still open does not finish a ticket, and nothing goes
  backwards but a review asking for changes. A second implementation of those
  rules would differ from the first within a release.
- **§6 the catch-up gets the same fifteen minutes the tracker gets, measured
  from the first event that asked for the move (item 5.5).** A pull request
  marked ready a minute ago has a CI run in flight that is about to make the
  move, and doctor should not race it. But where several pull requests ask for
  one move, the grace runs from the earliest: a move owed since Monday should
  not wait another quarter of an hour because somebody opened a second pull
  request. Racing costs nothing in any case — both runs perform the same move
  to the same column, which rule 5 makes idempotent.
- **§6 a caught-up ticket counts as held, and says so in evidence (item
  5.5).** After `--fix` the board is right, and a check that reported the
  board as wrong anyway would make a daily `--fix` run exit non-zero for ever.
  The evidence line stays either way — "flow moved it from In progress to In
  review" — so a report still shows how much the audit had to put right, which
  is the number that says whether `flow check pr` is running at all.
- **§6 the catch-up reads the forge strictly, and the other live checks do not
  (item 5.5).** Everything else in §6 only describes what it saw, so a pull
  request it could not read costs it a little accuracy. This one acts, and a
  ticket's column is decided by all of its work (spec 04 §5 rule 4) — so the
  reading that is missing the open half of a ticket's work is exactly the
  reading that moves it to Done too early. A partial answer is an error, not a
  smaller audit.
- **§6 a `deployed` transition with no records to read is an error, not a skip
  (item 5.5).** A workflow that finishes tickets on deployment and cannot see
  deployments has a last column nothing can ever reach. Skipping quietly would
  report a board that is permanently one move short as correct.

- **§10 the pinned issue is the record, and it carries its own log (item
  5.7).** The section says the report is "updated in place", which costs the
  history — and the history is the point, because spec 00 §3 asks for fourteen
  consecutive green days. So the issue holds a table of daily runs that each
  run reads back, adds today's row to, and writes out again. The streak is
  therefore provable from a fresh clone, a lost lock and a CI runner that
  keeps nothing: the issue is the only thing that remembers.
- **§10 a month of rows is kept (item 5.7).** `HealthRetention` is 30. Long
  enough to show a fortnight's streak and the week that preceded it, short
  enough that the issue stays one screen.
- **§10 one row per day, and the last run of the day wins (item 5.7).**
  Doctor runs daily but a person may run it four times in an afternoon, and
  four rows for Tuesday would make "fourteen consecutive days" mean four
  afternoons. The day's verdict is the run that finished it, because the
  afternoon's fix is what the team ended on.
- **§10 a day with no run breaks the streak (item 5.7).** A fortnight with a
  Sunday missing is thirteen days of evidence and one day of not having
  looked, which is not the same as green. So is a row a person hand-edited
  into something flow cannot parse: the row is dropped rather than guessed at,
  and a dropped row is a gap like any other.
- **§10 nothing is published without `--fix` (item 5.7).** Writing to somebody's
  tracker is a change to the world, and spec 02 §2 keeps those behind `--fix`.
  Not even the read happens: a preview has nothing to tell the reader that the
  report already on their screen has not told them.
- **§10 a narrowed run is not the morning report (item 5.7).** A run given
  `--check 'config.*'` saw a fraction of the workflow. Publishing it would
  overwrite a whole morning's report with a partial one and record a day in
  the streak that nobody audited. `--report` still writes the file, because
  that file is what the reader asked for by name.
- **§10 failing to publish is not a finding (item 5.7).** The report has
  already said what is wrong with the workflow. A tracker that will not take a
  copy of it is weather, not a verdict, so it is logged and the run exits on
  what it actually found (§4). Conversely a read that fails is never read as
  "there is no issue yet" — that would file a second health issue every
  morning the tracker had a bad minute.
- **§9 the markdown is rendered once, by `internal/ui` (item 5.7).** The engine
  does not render its own report: the shape a document takes on a web page
  belongs to the UI (spec 01 §2), and a second markdown writer in the engine
  would be a second answer. The root command renders once and hands the same
  bytes to the `--report` file and to the issue, so the two cannot disagree
  about what the run found.
- **§10 the title and the marker name the repository (item 5.7).** One tracker
  serves many repositories. `flow health: owner/name`, marker
  `flow:health repo=owner/name`, so two repositories reporting into one
  project keep separate streaks.
- **§4 the summary names the work, not the obstacle (item 2.10, revisited).**
  Every refused item summarised as "somebody with the access has to do it",
  so two follow-ups on one repository were the same line in the issue list —
  on flow's own repo, the trunk's protection and the status check a merge
  must pass, indistinguishable without opening either. The summary is now the
  check's own title with what is short appended: `<title> — needs admin`.
  Nothing else titles a follow-up, so the check title is the one place the
  work is named.
- **§4 "exact steps" means the change, fields and all (item 2.10, revisited).**
  A refused item is the one outcome where flow planned the whole change and
  was stopped only by permission. Writing "flow has no steps to offer for
  this one" when every field is in hand asks a person to rediscover what flow
  already decided, so the steps are the missing permission followed by each
  planned change with its fields in sorted order, and the report carries the
  same changes as evidence. Sorted, because a ticket rewritten by map order
  alone would look like news every morning.
- **§4 a follow-up is flow's own writing, so flow keeps it current (item
  2.10, revisited).** The lifecycle had no way back from a ticket that was
  true when filed: a finding whose summary or steps improve leaves a stale
  ticket behind a correct report. An open follow-up whose title or body no
  longer matches what flow would write today is rewritten in place, under
  `--fix` only, and the outcome reads `refreshed`. It is not a nudge and does
  not touch `nudged_at`: a nudge asks somebody to act and is rationed to one
  a week, while correcting flow's own words costs the reader nothing and must
  not wait six days. The tracker is asked only when the text really differs,
  so a steady finding writes nothing.
- **§4 a note belongs to a finding, and leads the steps (item 2.10,
  revisited).** What a provider volunteered about an item is shown on the
  checks that fail — `native` and `follow_up` — and on no check that passes: a
  note explains why something is not true, and on a pass it is a fact about a
  mechanism nobody is relying on, which is noise every morning. In a
  follow-up's steps the note comes first, ahead of the access flow is short of
  and the change flow would have made: a mechanism already written and merely
  switched off is one click, and printing the afternoon's work above it buries
  the cheap answer under the expensive one.
- **§2 in `--ci` the lock is proposed, never written (item 5.8).** A CI
  runner's checkout is deleted when the job ends, so a refreshed `.flow.lock`
  written there is a lock nobody will ever read; and the place it has to reach
  is the trunk, which is the one place flow does not write (spec 06 §4). So
  `--ci --fix` opens a pull request carrying it instead, under a fixed
  proposal id, so that a lock that goes on drifting updates one pull request
  rather than opening thirty mornings in a row. A lock that already matches
  is proposed not at all — a daily pull request against a file nobody changed
  is how a team learns to ignore them.
- **§10 running out of time is not weather (issue #197).** A provider that
  will not take the report or the lock proposal is weather and is logged; the
  run's own `--timeout` expiring while it files them is the run not
  finishing, and it exits as one (`ErrUnavailable`, "finish within"). Logging
  that too let flow's daily job go green for a week while its health issue
  stopped being rewritten. The daily job is given `--timeout 10m`: a
  fortnight's audit of a busy board does not fit in the interactive 2m.
- **§6 flow's own bookkeeping is not delivered work (issue #203).**
  `live.tickets.done_merged` leaves out a ticket carrying flow's follow-up or
  health marker and says how many it left out. A follow-up is settings work
  with no pull request by design, and flow closing it is its own evidence the
  work is done; counting it turned the audit red for a fortnight after every
  fix to a repository's setup. A flag cleanup ticket is still counted: it is
  flow's writing, but the work it asks for is a change to the code.
- **§6 a merge that closed nothing is caught up (issue #201).** A pull
  request the gate armed is merged for the Actions token, and GitHub lets
  such a merge cause no further events: `Closes #N` is read and the issue
  stays open, in review, with its work on the trunk. `live.prs.closed_issues`
  reads each merged pull request's closing references and, under `--fix`,
  closes what is still open with a comment naming the merge. It is flow's
  own catch-up, like `live.transitions.flow`, rather than a second credential
  for arming: the team chose a day's lag over another secret to hold.
- **§4 one fact, one follow-up (issue #202).** A follow-up is keyed by the
  doctor check it is reported under, and `flow apply` files under the same
  id (`engine.CheckOf`): the deployment records are the one item whose check
  id differs (`deploys.source`). A ticket an earlier flow filed under the
  item id is closed as not planned by the next round, with a comment naming
  the ticket that stands, and dropped from the lock.
- **§2 failing to propose the lock is not the run's verdict (item 5.8).** The
  report has already said everything doctor found about the workflow, and the
  lock is flow's own bookkeeping. A forge that will not take the proposal is
  logged and the exit code is left alone.
- **§5 the remedy is worded once for every reader (item 5.8).**
  `Remediation.Lines()` is where the fix, the command and the numbered steps
  become text; the terminal indents those lines under the finding and an
  annotation carries them on the end of its message. Two renderers wording
  the same advice would eventually word it differently.
- **§6 the trailers are read with git, never with a pattern (item 5.6).** Where
  a commit message's trailer block begins depends on `trailer.*` configuration,
  on the keys the repository already uses, and on rules about blank lines that
  have changed over git's life. Flow asks `git interpret-trailers --parse`
  through the working copy rather than matching a line that looks like a
  trailer — a `Flag-Added:` line with a paragraph of prose after it is prose,
  and a pattern match would file a cleanup ticket for a flag nobody added. A
  run with no working copy to parse with therefore fails the check, because a
  run that could not look has not established that nobody added a flag.
- **§6 the marker names the flag and the repository (item 5.6).** Two commits
  announcing the same flag are one promise and get one ticket, which is what
  makes `--fix` idempotent without flow remembering anything between runs.
  The repository is in the marker because one tracker serves many, and
  `new_basket` in somebody else's is not this repository's flag; without it
  the second repository would never get a ticket at all.
- **§6 flag names are compared exactly (item 5.6).** A flag name is an
  identifier in somebody's code. Folding case or punctuation would be flow
  deciding that two spellings are the same symbol, which it is not entitled to
  do. A trailer with no value after it announces nothing anybody could file a
  ticket about and is ignored; judging the trailer itself would be the commit
  linter's job, which spec 08 §4 took away from it.
- **§6 a closed cleanup ticket counts as much as an open one (item 5.6).** The
  check asks whether the promise was written down, not whether it has been
  kept: flow does not read the code and cannot tell whether the flag is still
  there. Requiring the ticket to be open would fail the check for exactly the
  teams that did the work.
- **§6 flow files the cleanup ticket and never touches it again (item 5.6).**
  A follow-up (§4) is work flow can verify, which is why flow may close it;
  taking a feature flag out is not, so flow will never know this one is done.
  The ticket says so in as many words — a ticket that implied flow was
  watching would sit on a board being quietly untrue. It carries the
  follow-up labels, because it answers the same question (which of these did
  flow open?) and a team with one label for flow's writing should not need a
  second to find half of it.
- **§6 a window that added no flags says which quiet it was (item 5.6).** "All
  0 flags have a cleanup ticket" and "nothing in the window added a feature
  flag" grade the same and read very differently. The second is what a reader
  needs to tell a healthy fortnight from a blind one, and it is also what
  stops the check asking the tracker a question about nothing.
- **§8 a promise about an automation names the audit that watches it (item
  4.6, revisited).** `tracker.native.*` is the one family flow holds both
  halves of: nobody can ask GitHub Projects whether a built-in workflow is
  switched on, and anybody can watch whether tickets actually moved. So the
  remediation steps point at `live.transitions.<event>` in the same report
  before asking for the promise. A person vouching from memory is the failure
  mode attestation has, and the fortnight of outcome is the cheapest thing
  that stops it. Every other attestation names no audit, because naming a
  check the report does not contain would send a reader looking for it.

- **§5 `local.hook` has nothing to ask of a CI checkout (the daily audit,
  revisited).** The hook is a file in a working copy; a runner's working copy
  lasts as long as the job and nobody commits from it, so installing one
  there would be installing it for nothing. This is the fact §2 already read
  about the lock, applied to the other thing flow would otherwise put in a
  throwaway tree. It matters more than it sounds: §10's whole point is a
  scheduled run whose exit code can be believed, and a check that cannot hold
  on a runner is a check that keeps every daily run red for ever. So `--ci`
  skips it, saying which it is — a skip with no reason reads as flow being
  coy about something it could have looked at.
- **§10 the daily audit needs the credential a person would use (the daily
  audit, revisited).** GitHub does not let a workflow grant its own Actions
  token the `administration` permission, so `secrets.GITHUB_TOKEN` answers
  403 to every read of branch protection and of the rulesets. flow reads both:
  the audit's subject is what protects the trunk. The alternative — treating
  a refused read as "nothing protects it" — is the failure mode this spec
  warns about throughout, a blind run that grades green, so the template and
  flow's own workflow hand the scheduled job a personal access token and say
  in the file why. A reader who finds a `403` in a job log should find the
  answer beside the token, not in a spec.
- **§2 the lock proposal keeps to the team's convention (issue #152).** The
  refreshed lock is offered as a pull request because a lock written on a
  runner is thrown away with the checkout, and that pull request is then
  judged by the gate like anybody else's. Its title is composed by
  `engine.LockProposal.Title` against `commits.types`,
  `commits.unlinked_types`, `commits.scopes` and `commits.header_max` rather
  than being a constant, and when the configuration leaves no landable title
  flow proposes nothing and logs which key refused it. This stays outside the
  grading: §2 already settled that failing to propose the lock is not a
  finding about the workflow, and a configuration flow cannot write
  bookkeeping under is the team's to change, not a fault in their delivery.

- **`live.prs.reviewed` excuses exactly what the gate excuses.** A pull
  request of a type in `agents.unreviewed_types` (spec 09 §2) is not counted
  and is not a finding: a gate that lets work land and an audit that reports
  it red a fortnight later are two readings of one rule, which is the
  disagreement issue #106 was about. One function answers for both. An excused
  pull request is still reported as a number, though — a fortnight of routine
  agent work says that it was let through unattended rather than that nothing
  a machine wrote merged, which would be untrue of a window flow read
  perfectly well.

- **`live.flags.age` reads the board, not the history (item #146).** A flag
  past ninety days was announced more than ninety days ago, so the commit is
  further back than any window worth reading every morning while the ticket is
  still right there. Reading the history far enough back to find the commit
  would cost every team a quarter of their trunk and a trailer parse per
  commit, every day, to find the few flags that are late — and would still
  miss a flag older than whatever span was chosen. The ticket is the record of
  the promise, so the ticket is what is audited.

- **The date is in the marker, written from the commit.** `flow:flag
  name=<n> repo=<r> added=<yyyy-mm-dd>` is stamped when the ticket is filed,
  from the day the announcing commit landed. That satisfies both halves of
  what the age has to be: it is the commit's date and not the ticket's, so
  closing, reopening, rewriting or re-filing the ticket does not wind the
  clock back; and it is machine-readable, so flow is not parsing its own
  paragraphs to find out. A search by `flow:flag name=<n> repo=<r>` still
  finds a stamped ticket, because a marker naming fewer fields matches more
  tickets (spec 11 §1) — which is what lets the stamp appear without
  orphaning the tickets filed before it existed. A ticket carrying no `added`
  is not counted: the only other date it has is its own, and that is the one
  date this check must not use.

- **Where two commits announce the same flag, the oldest is the one the
  ticket is stamped with.** The branch in the code is as old as it is, and
  mentioning it again does not make it younger.

- **Only flags past the date are counted.** One forgotten flag among fifty
  healthy ones is one of one, not an encouraging ninety-eight per cent, and a
  team whose flags are all current is `0/0` with a sentence saying which
  nothing it was.

- **A label is how a flag is allowed to stay, and flow never applies it.**
  The explanation belongs in the ticket's comments, where the team already
  writes and nobody has to keep to flow's vocabulary; what the check needs
  from it is the yes or no, and a label is the one field every tracker flow
  speaks to has, is already read on every ticket flow lists, and is one click
  for a person. It is in the labels `flow apply` asks the tracker for —
  a label nobody can apply is no way out of a finding — but putting it on
  anything is the team's word and never flow's.

- **There is no `--fix` arm.** Both ways out of this finding are a person's:
  flow cannot read the code, so it can neither take the flag out nor decide
  that it stays. The remedy names the two of them and the key to raise if the
  team has agreed a longer life for its flags.

- **Closed is gone.** The cleanup ticket says in as many words that a person
  closes it when the flag is out, so a closed ticket is the only account of
  the code flow has and it is the one the team wrote.
- **§6 a measurement is reported and never graded (issue #147).** The
  proposal asks for an agent's throughput and rework rate, and neither has a
  share that is right: a team landing forty agent pull requests a fortnight
  with a quarter of them sent back may be doing better work than one landing
  two with none. So `live.agents.rework` passes whatever it finds and cannot
  move the exit code. A check that could fail would need a threshold, and a
  threshold nobody can justify is a number flow made up.
- **§6 throughput and rework are one check, not two (issue #147).**
  Throughput is the rework rate's denominator: "a machine merged 12 pull
  requests" beside "3 of 12 were sent back" is the same sentence written
  twice, and the second cannot be read without the first. It is a check
  rather than a new kind of thing in the report because the report has one
  shape — counts, a message and evidence, rendered in text, markdown, JSON
  and in the issue the morning run edits in place. A measurement with a shape
  of its own would be rendered four more times to say less.
- **§6 rework is what happened, not what is still being asked (issue
  #147).** Every merged pull request has been approved by the time it lands,
  so `core.PullRequest.ChangesRequested`, which reads each reviewer's latest
  verdict, is false of all of them. The new `SentBack` asks whether anybody
  ever asked for changes, counting a review a later one overtook and one
  somebody dismissed. The approval that followed is what makes it rework
  rather than a refusal.
- **§6 a share of nothing is not a good score (issue #147).** A window with
  no agent work in it reports that, rather than 0% of nothing: a rate reads
  as a team doing well, and what happened is that nobody asked a machine for
  anything. A configuration naming no machine contributors skips the check
  outright — 0/0 beside a team of people reads as a reproach.
- **§6 a label is a request for help; the comment is what asks (issue #145).**
  `workflow.blocked.require_comment` was decoded, validated and read by
  nothing. `live.tickets.blocked_explained` reads it: a team that wrote the key
  down has said a label on its own is not enough, and flow holds them to that
  and to nothing else — the check skips for everybody else rather than
  teaching every team the same lesson about their board. It is graded like the
  rest of the board family rather than warn-only, because unlike a ticket that
  has gone quiet this is not a fact about what people had time for: the label
  went on deliberately and the explanation is the half of the gesture that was
  left out. Graded and not critical, because `health.fail_below` is where a
  team says how much of this they will carry, and one unexplained ticket in
  twenty is a reminder rather than a broken workflow.
- **§6 flow's own writing cannot answer the question flow is asking.** A
  comment is an explanation when somebody who is not flow wrote it, which is
  decided by the marker rather than by the author: the account flow writes as
  belongs to the team and may well be a person's. Flow nudges its own
  follow-ups, so a reading that counted a nudge would pass exactly the tickets
  that have been waiting longest. A ticket flow has written on and nobody else
  has says so in those words, because "nobody has said anything" and "only
  flow has said anything" need different things from whoever reads the report.
- **§6 what the comment must say is in the remedy, not in the rule.** Flow
  cannot judge whether a paragraph names why the work stopped, what would
  clear it and who is chasing it — so it asks for a comment and the
  remediation asks for the three. A rule that tried to grade the prose would
  be wrong about English, and a remedy that only said "add a comment" would
  collect the word "blocked" on every ticket.
- **§6 blocked work that is closed is not waiting on anybody.** A closed
  ticket keeps whatever labels it had, and asking a team to explain work they
  have already dropped is asking them to tidy history. The tracker's own
  blocked flag counts beside the label, because a board that uses a Jira flag
  or a Linear blocking relation and no label at all has still stopped the work.

- **§6 work that cannot land is a finding of its own (issue #156).** A pull
  request that conflicts, or that has fallen behind a trunk whose protection
  requires it to be level, goes wrong without anything going red: the checks
  that passed were computed on a head that has not moved and stay green there
  for ever, and no forge sends mail about it. `review_has_pr` does not see it
  — a conflicted pull request is open and ready — so the board says the work
  is moving when it stopped. `live.tickets.review_stuck` asks the forge
  instead of guessing, and fails rather than warns: unlike a slow review this
  is not a fact about what people had time for, it is a queue that has
  stopped.

- **§6 only approved work is asked whether it is stuck.** Falling behind the
  trunk is the ordinary condition of every pull request in a queue the moment
  one of them merges, so a check that failed for each of those would be noise
  about nothing. And a forge that reports one blocker at a time reports the
  approvals it is missing before anything else, so until the approval is
  there "what is in the way" is not a question it has answered. A draft is
  never among them whatever its reviews say.

- **§6 the review column has a patience too (issue #156).** `progress_stale`
  watched the column `pr_opened_draft` lands in, so the column work waits in
  afterwards had no staleness rule at all — the one place a queue silently
  grows. `live.tickets.review_stale` is the same check against the column
  `pr_ready_for_review` lands in, warning and never failing for the reason
  `progress_stale` does. Both date a ticket by the latest of its own update
  and its pull requests': a tracker's date does not move when somebody
  pushes, and a pull request's does not move when somebody writes on the card.

- **§6 one patience, not one per column (issue #156).** The five working days
  `progress_stale` counted were a constant in the engine; they are now
  `health.stale_after`, defaulting to 5 and applied in `Env.Patience()` alone
  so the number the report prints and the number it judged by cannot come
  apart. One key covers every column that has a patience: a team has one
  answer to "how long is too long", and two keys would mostly be set to the
  same thing and occasionally be set to different things by accident.

- **§6 the catch-up performs `branch_created`, and nothing else ever does
  (issue #144).** It is the one event with no event behind it: no forge
  announces a branch through the APIs flow uses, no tracker dates the link,
  and `flow check pr` cannot see it because the whole point is that no pull
  request exists yet. So the catch-up looks at tickets with no pull requests
  at all — which nothing else in the audit does — and dates the move at the
  grace deadline rather than waiting out a grace period against a date that
  does not exist. The grace is there to let an in-flight CI run settle a move
  first, and here there is no run to wait for. A board that dates nothing at
  all still has its work started.

- **§6 `live.tickets.shared_board` counts only the tickets that are not this
  repository's (issue #89).** Counting the whole board would drown a single
  unaccounted-for sibling in two hundred of this repository's tickets and let
  it pass the threshold. The question is not what share of the board is
  accounted for but whether anything on it is accounted for by nobody, so the
  check grades critically: one repository nothing names fails it.

- **§6 The three checks that ask a ticket what landed behind it pass over a
  named sibling's tickets entirely, and say how many they passed over.**
  `done_merged`, `done_deployed` and `review_has_pr` all compare a ticket
  against this repository's pull requests. Counting a sibling's ticket as held
  would claim flow had checked something it never looked at; counting it as
  broken is the cross-talk `tracker.shared_with` exists to stop. The count
  goes in the message because a tally of nought with nothing said reads as an
  empty board rather than a board somebody else is watching.

- **§6 `live.transitions.flow` will not move a named sibling's ticket, even
  under `--fix`.** Every reason to leave it where it is — an open pull request
  of theirs, a review they are waiting on — is invisible from here, so the
  only transition flow could reach would be one made in ignorance. Spec 04 §5
  rule 4 already says a ticket's column depends on all of its work; a
  repository that cannot see all of it has no business deciding.

- **§6 The remediation names the repositories it found, one line each, sorted.**
  "Add it to `tracker.shared_with`" is a sentence anybody could have written;
  the repository's name is the part the team does not have. Sorting is what
  makes two runs of the same board give the same instruction.
- **A check this spec names and nobody wrote carries the issue that will write
  it (issue #168).** §6's table is read by a test against the checks `flow
  doctor` actually registers, so a row with no code behind it fails the build
  — which is the point, since `live.wip` sat in that table unbuilt for the
  whole of v1. A gap that somebody has decided to leave open is marked in the
  row itself as `not built: [#N]`, and the test fails in the other direction
  too, the day the check is built and the annotation does not come off. A
  decision is a sentence with a ticket behind it; a shrug is what this
  replaces.
- **A family of checks is one row.** `tracker.native.<event>` and
  `live.transitions.native` produce one id per configured event, and the
  configuration decides the members, so the spec names the shape and the test
  folds the ids back into it before comparing.
- **§6 `live.wip` is registered whether or not any column carries a limit, and
  skips with a sentence when none does.** A check that is absent from the
  report is indistinguishable from a check nobody wrote — which is exactly how
  this one went missing for a whole phase — so the registry carries it always
  and the report carries its reason. Every other live check does the same, and
  a registration that read the configuration would be a second place where a
  check can disappear.

- **§6 The unit `live.wip` counts is a day, and the window is every UTC
  calendar day from `--window` to today inclusive.** Spec §6 asks for "days in
  window where any WIP limit was exceeded", so the denominator is days rather
  than tickets, and a day begins somewhere that does not move with whoever is
  reading the report. A day on which both limits were broken is one day: the
  question is how often the team was carrying too much, and counting breaches
  would make a team look worse the more limits it set.

- **§6 Today is read from the board; every earlier day is reconstructed from
  the pull requests.** The board carries one moment, the one it is read in —
  no tracker will say what a column held last Tuesday — so the last day of the
  window is a reading, bucketed by the same code the gate's WIP rules use, and
  the days before it are an approximation. The two are not two answers to one
  question: one is what is true now and the other is what can be inferred
  about then.

- **§6 The reconstruction counts a pull request for every day between its
  opening and its merge, in the column its state now puts it in.** The day
  work entered a column is precisely what nothing records, and the forge will
  only say whether a pull request is a draft *now* — so a draft is read as
  somebody's work in progress for its whole life, and anything else as waiting
  for review for its whole life. A pull request that spent a week as a draft
  and a day in review therefore reads as a week in review. That is an upper
  bound and not a measurement, which is why spec §6 marks the check warn only
  and why the approximation is stated in the check's own evidence rather than
  left for a reader to discover.

- **§6 A pull request abandoned mid-window is not counted.** The audit reads
  what merged in the window and what is open now, which is two listings two
  other checks already pay for; a third listing of every closed pull request
  would buy a reading nobody would act on. The days it was open are therefore
  counted one short, which is the right way round for a check that warns: it
  says less than happened rather than more.

- **§6 Expedited work is outside the limits here exactly as it is in
  `wip.review`.** The label is read from the pull request and from its linked
  tickets, because the gate reads both — and the same work being the agreed
  exception on Monday and a finding on Tuesday would be flow arguing with the
  configuration.
- **§8 a provider that answered no is not asking for somebody's word (issue #184).**
  An attestation is how flow takes a person's account of something no API will
  answer. Where the provider has said outright that the thing is not so, there
  is nothing left to take on trust: the check fails, the remedy is the switch
  rather than the signature, and a signature already in the lock does not
  quieten it. `core.Note.Refutes` is how an adapter says it saw that, and one
  such note is enough — they are facts, not votes.
- **§8 the rule needs no second guard (issue #184).** `AttestInput.Wants` already
  holds that a word settles only what flow could not verify. A refutation makes
  the item one flow did verify, so the ordinary path declines to apply the lock
  on its own; the first draft tested the same thing twice in two places, which
  is how two readings of one question drift apart.
- **§8 a refuted item keeps the provider's note as evidence (issue #184).** The
  finding is the note, so the line that would otherwise read "flow cannot see
  this" has to carry what flow saw and where the switch is. The remedy's steps
  are the note's own fix.
- **§4 a ticket says what the thing is before it says what to type (issue
  #194).** The body now leads with `## What this is` — the capability's
  `by_hand.what` and `by_hand.where`, filled in for this repository — and
  only then `## What to do`. The reader of a follow-up is by definition the
  person who does not have flow's vocabulary, and a list of instructions is
  no use to somebody who does not yet know what they are being asked to look
  at. Where there is no declaration to speak for a provider — a live audit is
  flow's own reading of the team's week — the remedy's summary carries the
  ticket as it always did.
- **§4 one refusal is said once (issue #194).** The planned change used to
  appear as a step *and* as evidence, so the shortest ticket flow files read
  as the same sentence under two headings. The steps are the account; the
  evidence of a refused item is the permission that was short and what the
  provider volunteered, which is what a reader does not already have.
- **A locked board the tracker does not show is kept, not replaced (#261).**
  Reading back the lock used to treat "the tracker has no such board" as
  "it was deleted" and start resolution again, and under `--fix` take the
  only other board the credential could see. GitHub gives the same answer
  for a board that was deleted and for one never shared with this
  credential, so a daily run on the bot of a repository whose board was not
  shared with it would have rebound that repository to another's board —
  flow's own — and remediated it. Now the lock is kept, nothing else is
  offered, and the check is a question naming the locked board: share it
  with the account flow runs as, or name another with `--set`, which is the
  one thing that may replace it. This is #255's rule applied to the
  tracker: a run that could not look is not evidence that something is gone.
- **A follow-up a person closed as not planned is left closed (#265).** The
  item 2.10 rule reopened any closed follow-up whose check still failed, with
  a comment saying the way to stop being asked is the configuration. That
  argued with an explicit answer: on flow-demo-web a reset closed every issue
  as not planned, and the next apply reopened the deploys follow-up. Now a
  ticket closed as not planned that flow did not close itself — flow records
  its own withdrawals as `closed` in the lock — is left closed. flow says
  once, on the ticket, how to write the decision into `.flow.yaml`; records
  the follow-up as `declined`, which `followups.health` does not count as
  waiting; and reports the check as a warning, *declined, not yet written
  down*, every run until the configuration stops asking. It is never a pass,
  because the configuration still asks for the work, and never a failure,
  because nobody is failing to do something they agreed to. Closing as
  completed when the work is not done is still reopened, and a person
  reopening a declined ticket makes it outstanding again. The decision ends
  up where it is reviewed, without flow overruling the person who made it.
- **A check that was not planned because a value is still to find out says
  so (#263).** Scenario A05 has doctor plan nothing while a placeholder is
  unanswered, so the one question is not buried under twenty failures. The
  checks reading the plan then found nothing in it and reported "this
  configuration does not ask for it" — about protection, required status
  and files the configuration plainly asks for. The environment now carries
  why there is no plan, and those checks are `blocked`, *not asked:
  tracker.project is still to find out*, as the tracker's own items already
  were. `forge.repo` passes on reachability, which is its own question,
  rather than holding every forge check behind a question that is the
  tracker's.
- **A board is taken without asking only if it is this repository's, or
  nobody's (#262).** Discovery took a lone candidate as the answer. With
  flow-demo-web's own board deleted, the only board on the account was
  flow's — linked to 17twenty/flow, 161 items — and doctor offered it as
  "the only answer there is", which `--fix` would have written down.
  Creating a board is always another answer. Candidates now carry the
  repositories the tracker links them to (`core.Candidate.Linked`; GitHub
  Projects reads `ProjectV2.repositories`). One linked to this repository is
  taken however many others there are; otherwise a lone board is taken only
  when it is linked to no repository at all. A lone board linked elsewhere
  is offered beside "or create a board for this repository", and nothing is
  written. A tracker with no notion of links leaves the old rule unchanged.
