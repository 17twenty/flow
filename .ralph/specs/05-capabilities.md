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
`linked_branches_read`, `native_transition_pr_opened_draft`, `native_transition_pr_ready_for_review`,
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

## Decisions

- **§1 a declaration must describe every capability of its kind (item 2.1).**
  `LoadRegistry` refuses a file that omits one. The alternative is a file that
  is silently incomplete, and an adapter that looks unsupported because nobody
  got round to the research rather than because the provider cannot do it.
  Saying `{ set: false, verify: false, remediation: none }` is a finding;
  saying nothing is not.
- **§1 an undeclared capability is the zero Capability (item 2.1).**
  `CapabilitySet.Capability` returns `{set:false, verify:false,
  remediation:"none"}` for a name it does not hold, which §4 resolves to
  `unsupported`. Querying an unresearched capability is a bug, and the safe
  reading of a bug is that flow may not touch the provider.
- **§1 the file is named after the adapter (item 2.1).** `github-projects.yaml`
  declares `adapter: github-projects`, and the loader enforces it, so the
  declaration behind an adapter named in `.flow.yaml` is always findable by
  name, and two files cannot claim one adapter.
- **§1 a broken declaration is a broken build (item 2.1).** The loader returns
  an error rather than skipping the file, and reports every problem in it at
  once. These files are flow's whole account of what it can do; running with a
  half-read one would make the plan a guess.
- **§1 `requires` tokens are free-form (item 2.1).** They are validated as
  non-empty strings and nothing more. §3 makes permission tokens a matter for
  each adapter's `Probe` — `admin`, `project_write`, `maintainer` — so a
  central registry of them would have to be rewritten for every new forge.
  Item 2.2 is where a token is matched against what `Probe` reported.
- **§2 the registry lives in `core`, the files are passed in (item 2.1).**
  `LoadRegistry` takes an `fs.FS`, and the root passes `assets.Capabilities()`.
  Spec 01 §2 puts `core` on stdlib and YAML only, so `core` importing `assets`
  would invert the dependency; it also lets tests load a declaration from
  `fstest.MapFS` instead of the shipped one.
- **§2 only six events have a `native_transition_*` name (item 2.1).** Spec 04
  §4's vocabulary has ten. `pr_opened` is defined as two other events and needs
  no name of its own; `pr_converted_to_draft`, `approved` and
  `pr_closed_unmerged` are not offered as built-in automation by any tracker
  researched. `core.NativeTransitionCapability` reports an event as unknown,
  and §1.1's `owner: native` check then rejects it with "use owner: flow".
- **§4 an attestation stands in for an observation, never over one (item
  2.2).** `verify: false` means flow has no way to look, so an attestation is
  what makes the difference equal. When `verify: true`, flow looked: an
  attestation claiming otherwise is ignored, and the ladder carries on to
  `native`. A human's word covers what flow cannot see, not what it can.
- **§4 the zero `Permissions` allows nothing (item 2.2).** Resolution takes
  what `Probe` reported; before a probe, or when one failed, there are no
  tokens, so `cap.requires` is unmet and the ladder falls through to the
  remediation. flow never attempts a write on the assumption that it probably
  may.
- **§3 `Permissions` is tokens plus evidence (item 2.2).** `Tokens` are the
  adapter's own vocabulary — the same words `requires` lists — and `Evidence`
  is what flow read to decide, one line each ("X-OAuth-Scopes: repo,
  project"). A refusal can then say what flow saw rather than only what it
  wanted. The tokens themselves stay per-adapter: §3 defines GitHub's, and
  each new forge brings its own.
- **§4 a resolution carries its own explanation (item 2.2).** Every outcome,
  including `noop`, comes back with one line of why, and a `follow_up` that
  was blocked on permissions names the tokens that were missing. The renderer
  in §5 therefore has nothing to work out for itself, and the JSON and the
  terminal cannot disagree about the reason.
- **§5 `noop` is `· up to date` (item 2.5).** The vocabulary names five
  outcomes and gives a glyph to each; the sixth, the one where there is
  nothing to do, it does not mention. Leaving the line out would make a plan
  that is entirely fine print as nothing, and a reader could not tell it from
  a plan that failed to run. `·` is deliberately the quietest mark on the
  page, and "up to date" is what a person calls it — `follow-up` likewise,
  against the JSON's `follow_up`.
- **§5 the engine owns the words, the UI owns the layout (item 2.5).**
  `Outcome.Symbol`, `Outcome.Label`, `PlanItem.Detail` and `Plan.Text` live
  beside the decision they describe, so the terminal, the markdown report
  (spec 12) and the CI annotation cannot disagree about what flow said it
  would do. What spec 12 adds is colour, width and arrangement.
- **§6 both sides are judged as a greenfield (item 2.6).** `Compare` plans the
  workflow against each declaration with no observations and no lock, so
  neither tracker is flattered by the state its board happens to be in. §6
  forbids a network, which means the tracker being compared against cannot be
  looked at — and comparing a board flow can see against one it cannot would
  answer "how far along are you" rather than "what would this cost".
- **§6 assumed permissions are the declaration's own `requires` (item 2.6).**
  `CapabilitySet.RequiredTokens` is the union of every token the file asks
  for, and that is the credential the comparison pretends to hold. §1's
  decision keeps permission tokens per-adapter, so there is no central list of
  what "full" means; the declaration is the only thing that knows. The effect
  is that a comparison is about what a provider can do, not about whose token
  is in the environment today.
- **§6 the table holds only what the workflow asks of a tracker (item 2.6).**
  `--tracker-override` changes the tracker; the repository and the deployment
  records are the same on both sides by construction, and rows that cannot
  differ are padding in a table whose whole purpose is the rows that do.
  `Compare` refuses a declaration that is not a tracker rather than comparing
  a board against a repository.
- **§6 the comparison takes declarations, not adapters (item 2.6).** `Compare`
  has no way to reach a provider, so "no network" is a property of the
  signature rather than a rule someone has to remember. It is also what makes
  the mode usable before adopting a tracker at all: flow ships the research,
  and the research is enough.
- **§3 a forge probes its repository; the project's trial read is the
  tracker's (item 3.7).** §3 describes GitHub's probe in one sentence, but
  spec 01 §2 forbids one adapter from knowing another exists, and
  `github-projects` is a separate adapter with a separate declaration and a
  separate credential check. `forge.GitHub.Probe` reads the repository;
  `tracker.GitHubProjects.Probe` (item 3.8) reads the project. Each reports
  the tokens its own declaration requires, which is the only way a resolution
  (§4) can compare like with like.
- **§3 a role and a scope are two limits, and both apply (item 3.7).** A
  maintainer whose classic token carries no scope permitting writes cannot
  write. So the probe reports the role's tokens always, and the write tokens
  only when the role may write *and* the token is permitted to use that. The
  alternative — reporting the role alone — produces a plan that says `native`
  and an apply that gets a 403, which is the exact failure §4 exists to
  prevent.
- **§3 a credential that reports no scopes is limited by its role alone
  (item 3.7).** `X-OAuth-Scopes` comes back from a classic token and from
  nothing else: a fine-grained token, a GitHub App and the Actions token send
  no such header, and their real grants are not discoverable from any
  response. An empty header is a classic token with no scopes, which is a
  different fact from no header, and the probe keeps them apart. Guessing in
  the second case would be wrong whichever way it guessed; taking the role is
  at least the limit flow can see.
- **§3 `public_repo` is read against visibility, not against `private`
  (item 3.7).** An internal repository on an enterprise is not private and is
  not public either, and a `public_repo` scope does not reach it. GitHub sends
  both fields; `visibility` is the one that can say so, so it wins where it is
  present and `private` answers for a server too old to send it.
- **§3 the implication inside a role is flow's, not GitHub's (item 3.7).**
  GitHub.com sends the whole implied set — an admin is also a maintainer, a
  pusher, a triager and a reader — but an older GitHub Enterprise Server sends
  only the three keys it knew about. Deriving the set from the strongest flag
  present costs nothing and means a probe against an old server does not
  silently report less than the credential holds.
- **§3 the scopes are reported as tokens in their own right (item 3.7).** §3
  names `repo`, `project` and `read:project` as permission tokens, so a
  declaration may require a scope by name. The probe therefore puts every
  scope it was told about into `Permissions.Tokens` verbatim, as well as using
  `repo`/`public_repo` to decide the write tokens. A declaration asking for
  `project` is then answered by the same mechanism as one asking for `admin`.
- **§3 probe and declaration share one vocabulary, and a test says so
  (item 3.7).** The tokens a GitHub write grants are exactly the words
  `assets/capabilities/github.yaml` requires, and
  `TestGitHubProbeSpeaksTheVocabularyTheDeclarationRequires` resolves every
  capability in the declaration against an admin's probe. A probe and a
  declaration that drift apart do not fail loudly — they resolve to
  `unsupported` (§4) and look like a provider limitation, which is the worst
  shape a bug can take here.
- **§4 a declaration may not ask for a promise about something flow can see
  (item 4.6).** `remediation: attest` together with `verify: true` is now a
  parse error in a capability declaration. The two say opposite things: an
  attestation stands in for an observation flow cannot make, so a promise
  about a capability flow verifies would be overruled by the next observation
  and settle nothing. Catching it where the declaration is read means no
  provider can ship a check nobody can ever satisfy.
- **§4 a compensated outcome is settled, not pending (item 4.6).** `flow plan`
  exits 2 for a change, a follow-up or an attestation (spec 02 §2), and a
  compensated item is none of them: the intent is enforced, by a mechanism
  other than the one the configuration named, and no amount of applying will
  change that. Counting it as pending would make a converged workflow exit 2
  for ever, and a command that can never report success is one nobody acts on.
  This is also what doctor already says about it.
- **§2 where a squash takes its words is a capability of its own (item
  7.3).** `merge_settings` used to mean both "which strategies may land a
  pull request" and "where the squash commit's header and body come from",
  and a declaration can only say one thing about a name. Gitea offers the
  first through its API and composes the commit itself, so a single name
  forced the Gitea declaration to claim either more than it can do — and
  propose, every run, a change flow has no way to make — or less, giving up
  the strategies to keep the text honest. `squash_text` splits them. GitHub
  (`squash_merge_commit_title`/`_message`) and GitLab (`squash_commit_template`)
  declare both; Gitea declares `squash_text` verify-only, so a repository whose
  squash body is not the pull request's gets one follow-up naming the file a
  person adds, rather than a change flow attempts and silently fails to make.
