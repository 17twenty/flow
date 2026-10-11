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
`native_transition_pr_opened_draft`, `native_transition_pr_ready_for_review`,
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
