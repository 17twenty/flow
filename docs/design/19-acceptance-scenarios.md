# 19 — Acceptance Scenarios

Each becomes a test named `TestScenario_<ID>_…` (CLI or engine level, fakes only).
The final acceptance is spec 17 §5 Stage C on the real repo.

## Setup
- **A01 init offline** — git repo with origin `git@github.com:acme/api.git`, no network:
  `flow init --tracker github-projects --owner acme --yes` → `.flow.yaml` with
  modeline, `forge.repo: acme/api`, `tracker.project: ${pending:project}`;
  `.github/workflows/flow.yml`; exit 0. Running again without `--force` → 64.
- **A02 outside git** → 64 with `fix: git init`.
- **A03 plan on fresh GitHub Project** — fake project with default statuses
  Todo/In Progress/Done → plan: native status option changes (with hazard note),
  native ruleset + merge settings, attest for each native transition, compensated
  WIP; exit 2.
- **A04 apply idempotent** — second apply: no native changes, exit 0 (attestations
  still pending → 2 if not attested; with attestations → 0).
- **A05 placeholder discovery** — exactly one user project → `doctor --fix`
  resolves `tracker.project` into lock; two projects → state `input` with
  `--set project=<n>` hint, dependants blocked, exit 2.
- **A06 follow-up lifecycle** — forge token lacks admin → ruleset follow-up created
  (marker); later fake shows ruleset → doctor comments + closes; closed while
  still missing → reopened with evidence.
- **A07 drift** — status option renamed in fake after passing → `drift`, exit 2.

## Gate
- **G01 green PR** — draft PR `feat(engine): add x`, body `Closes #12`, issue #12
  Ready with AC and parent → status success; item moved to In progress; ready
  event → In review.
- **G02 failures explained** — title `Added x`, linked issue lacks AC → status
  failure, description ≤ 140 chars naming both rules; one `flow:policy` comment;
  re-run edits it; fixing both deletes it.
- **G03 unlinked chore** — `ci: cache modules`, no linked issue → success.
- **G04 agent needs human** — author `17twenty-flow-agent[bot]`, no approvals →
  failure `agent.human_review`; approval by `17twenty` → success.
- **G05 fork PR** — read-only token → result printed, nothing posted, exit 0.
- **G06 idempotent status** — same evaluation twice → one status post.

## Verify
- **V01 deploy lag** — 3 merged PRs; deployments cover 2 within 2h, 1 never →
  `live.deploys.lag` warn 2/3 with the missing PR linked.
- **V02 ungated merge** — a PR merged with no `flow/policy` success on its head →
  `live.prs.gated` fail (critical), exit 2.
- **V03 native transition broken** — merged PRs whose issues remain In review >15
  min → `live.transitions.native` fails for `pr_merged` with items listed.
- **V04 catch-up** — `pr_ready_for_review` event missed (no CI run) → doctor
  `--fix` moves the item to In review.
- **V05 flag cleanup** — merged commit with `Flag-Added: x` trailer and no
  cleanup ticket → `live.flags.cleanup` fail; `--fix` creates exactly one ticket
  (re-run creates none).

## Security
- **S01** tracked `.flow.local.toml` with `token_cmd` → refused, warning, 77 if no other credential.
- **S02** credential to unapproved host non-interactively → 77 with fix.
- **S03** redaction: tokens in errors/logs never appear in stderr/stdout.

## Decisions
- **G01 "status success" means the gate passes, not that the status is green
  (item 4.3).** The scenario opens with a *draft* pull request, and spec 09 §2
  publishes a draft as `pending` / "draft — not enforced" — judged in full,
  but not enforced against work somebody is still writing. The test asserts
  what the two agree on: every rule passes, the card moves to in progress, and
  the status turns `success` the moment the pull request is offered for review.
- **G04 the agent is recognised by `agents.authors` (item 4.3).** The preset
  ships `authors: []` and `labels: [agent]`, so the scenario's author-based
  reading needs the configuration to name the author. The test writes the
  login into `.flow.yaml` rather than labelling the pull request, because
  `agent.human_review` firing on a label makes dropping the label a way to pass
  the rule vacuously.
- **The scenario ids are checked against the tests, not against a list
  somebody maintains (item 8.1).** This file opens by saying each scenario
  becomes a test named `TestScenario_<ID>_…`, so the tests carry the id in
  their names and `TestEveryAcceptanceScenarioHasATest` reads both this file
  and the source of every test in the repository. A scenario nobody keeps and
  a test claiming an id this file does not list are both failures. Several
  scenarios are kept by more than one test, because a scenario is a sentence
  about a run and a test is an assertion about one thing: A04's two halves,
  A01's two refusals and V05's finding and its fix are each worth their own
  name.
- **A04's exit 0 is asserted of the apply, not only of a plan after it (item
  8.1).** The scenario's words are that the second apply has no native changes
  and exits 0, and a plan agreeing is a different statement: apply is the
  command somebody runs twice. Making the assertion of the apply is what found
  `Applied.ExitCode` disagreeing with `Plan.ExitCode` about a compensated item
  (spec 06 Decisions), and what made the test seed a deployment — a
  repository nothing has ever been deployed to owes a genuine follow-up, which
  is A06's business and not this one's.
