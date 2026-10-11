# 00 — Overview (v2: narrow scope, self-hosted)

## 1. What flow is

`flow` makes a lean, trunk-based delivery workflow **true** and **provably
true** across a Git forge and a ticket tracker. It does three things only:

1. **Set up** — `init`, `plan`, `apply`: configure forge and tracker (natively
   where APIs allow), and file tracked follow-ups for what they don't.
2. **Gate** — `check pr`: one CI step that publishes the `flow/policy` status on
   pull requests.
3. **Verify** — `doctor`: prove the setup is intact AND that the workflow is
   actually behaving (audit of recent PRs, tickets and deployments), using only
   read APIs: git log, forge, tracker.

After setup, nobody needs to run flow by hand. Developers use git, their forge
and their tracker. flow appears as a PR check and a scheduled health report.

## 2. What flow is NOT (explicit non-goals)

- Not a commit tool (no composer), changelog generator, release tool, metrics
  dashboard, or "what should I work on next" assistant. Use git-cliff/svu/
  release-please, DORA tooling, the tracker's own UI.
- Not a ticket mover where the tracker can move tickets itself. Native
  automation first; flow moves tickets only for transitions the tracker cannot
  do natively, and only as part of the CI step that already runs (§4 P11).
- Not a deployment tool. It reads deployment facts the forge already holds.
- Not a ticket tracker, CI system or flag service.

## 3. The proof: flow is built with flow

The flow repository (`github.com/17twenty/flow`) is managed by flow, using a
user-owned GitHub Project as its tracker, GitHub Deployments as its deploy
signal, and its own `flow/policy` gate on every PR — including PRs opened by the
build agent. Spec 17 defines this precisely. **v1 is done when `flow doctor` on
the flow repo exits 0 for 14 consecutive days** while development continues.

## 4. Principles

P1. **The tracker tracks work. Git tracks deployment. Flags track release.**
    Environments are never workflow statuses.
P2. **Declarative and idempotent.** Re-running converges; never duplicates.
P3. **Read-mostly.** After setup, flow's only writes are: the `flow/policy`
    status and its explanatory PR comment; engine-owned transitions (P11);
    follow-up tickets. Everything else is reads.
P4. **Honest about capability.** Every desired setting resolves to exactly one
    outcome: native, compensated, follow-up, attest, noop (spec 05).
P5. **Enforce in the forge, display in the tracker.**
P6. **Least surprise.** Mutations of shared configuration need confirmation.
P7. **Machine-readable.** Every command has `--json` and stable exit codes.
P8. **Small and flat.** Few packages, standard library first.
P9. **Repo config is untrusted input.** No command execution from repo files.
P10. **Help, don't nag.** Every failure says what, why (one line), and how to fix.
P11. **Native first.** Prefer the tracker's/forge's own automation and linkage
     data. flow fills only the gaps, and declares per transition who owns it.
P12. **No user-authored regex.** Linkage uses structured API data (e.g. GitHub
     `closingIssuesReferences`, deployment records). Where a convention is
     unavoidable, flow uses the tracker's own documented convention, fixed in the
     adapter (e.g. Jira issue keys in branch/PR title), never user-configured.
P13. **Verify behaviour, not just configuration.** Doctor audits what actually
     happened over a time window, not only whether settings look right.

## 5. Scope

### v1
- Commands: `init`, `plan`, `apply`, `attest`, `doctor`, `check pr`,
  `check deploy`, `check commit`, `hook install|uninstall`, `schema`, `version`.
- Forge: **GitHub** (incl. Deployments API). Gitea/Forgejo and GitLab: Phase 7.
- Tracker: **GitHub Projects v2 (user- and org-owned)** first — it's what flow
  dogfoods. Jira Cloud and Linear: Phase 7.
- Deploy signal: forge Deployments (preferred) or commit statuses, reported by
  whatever deploys (release workflow, Argo CD, Flux) against the **app commit**.

### Future
`flow serve` (spec 18), more adapters (Trello, ClickUp, Bitbucket, Azure DevOps).

## 6. Journeys

J1. **Adopt** — `flow init` → `flow plan` → `flow apply` → follow-ups land with
the right owners → `flow doctor` until green. One-time.
J2. **Daily work** — no flow commands. Branch, commit, PR with a conventional
title and a linked ticket; the `flow/policy` check passes or explains itself;
merge; the tracker moves the ticket natively; the release/deploy pipeline
records a deployment.
J3. **Verify** — scheduled `flow doctor --ci` posts a report; humans act on
warnings (e.g. "PR merged but never deployed").
J4. **Cross-team** — another team receives a follow-up ticket; doctor verifies
their change via API and closes it, or reopens it with evidence.

## 7. Glossary

| Term | Meaning |
|---|---|
| Adapter | Forge, Tracker or DeploySource implementation for one product. |
| Capability | Something flow may configure/verify; declared with set/verify flags. |
| Outcome | `native`, `compensated`, `follow_up`, `attest`, `noop`. |
| Owner (of a transition) | `native` (tracker automation) or `flow` (done by `check pr`). |
| Check | A doctor probe: setup check or live-health audit. |
| Follow-up | Tracked request for a human/team, materialised as a ticket. |
| Attestation | Named, expiring human confirmation for unverifiable settings. |
| Placeholder | `${pending:name}` value resolved by doctor. |
| Lock | `.flow.lock`: resolved IDs, observations, follow-ups, attestations. |
| Deployment record | Forge-held fact: (repo, sha, environment, state, time). |
