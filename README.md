# flow — lean trunk-based delivery, made provably true

flow makes a lean trunk-based workflow true and provably true across a forge and
a tracker. It **sets up** (`init/plan/apply`), **gates** (`check pr`) and
**verifies** (`doctor`). After setup nobody runs it by hand. And flow is built
with flow: its own repo, Project, gate and health report are the live demo.

```sh
go install github.com/17twenty/flow@latest
flow init && flow plan && flow apply && flow doctor
```

## What changed from v1
Removed: commit composer, changelog, metrics, start/next/context, flags commands,
reconcile/cron polling, user-configurable regex. Added: native-first transition
ownership per event, structured linkage (`closingIssuesReferences`), forge
deployment records against the app SHA, live-health audit in doctor,
`check deploy`, and a full dogfood/bootstrap design (GitHub + user-owned Project).

## Layout

| Path | Purpose |
|---|---|
| `main.go`, `cli.go`, `cmd_*.go` | CLI entry point, one file per command (spec 01 §1) |
| `internal/` | `core`, `config`, `git`, `commits`, `engine`, `forge`, `tracker`, `deploys`, `ui` |
| `assets/` | Embedded: capabilities, presets, templates, schema. Reference: docs, research, examples, testdata |
| `.flow.yaml`, `.github/` | flow's own workflow config, CI, issue forms, PR template (spec 17) |
| `.ralph/` | Build loop memory: `PROMPT.md`, `PROMPT_plan.md`, `AGENTS.md`, `IMPLEMENTATION_PLAN.md`, `specs/` |
| `skills/flow/SKILL.md` | Agent skill: how a harness works a ticket to a reviewed pull request |
| `loop.sh` | Loop runner (direct and PR modes) |
| `bootstrap/seed-issues.sh` | Creates phase + item issues and adds them to the Project |
| `dogfood/.github/workflows/flow.yml` | The gate workflow; added to `.github/` in bootstrap Stage B |

## Working it with an agent

[`skills/flow/SKILL.md`](skills/flow/SKILL.md) is a skill for Claude-compatible
harnesses: verify the repository, take a ticket that meets the Definition of
Ready, commit messages the gate will accept, open a pull request that links its
ticket, and then wait for a person — because `agent.human_review` is not a
check an agent may clear for itself. Copy it where your client looks for
skills:

```sh
mkdir -p .claude/skills/flow && curl -fsSLo .claude/skills/flow/SKILL.md \
  https://raw.githubusercontent.com/17twenty/flow/main/skills/flow/SKILL.md
```

It names no repository of its own: every rule it follows is read out of the
`.flow.yaml` in front of it. Tests hold it to flow's actual surface — each
command and flag it tells an agent to run, and each configuration key and
policy rule it names, has to exist.

## Bootstrap (summary — full detail in specs/17-dogfood.md §5)
1. **Stage A (human, ~30 min):** repo, user Project (Kanban template), built-in
   workflows, labels, agent GitHub App, `FLOW_PROJECT_TOKEN` secret, copy files
   (not `flow.yml` yet), `./bootstrap/seed-issues.sh 17twenty/flow <project#>`.
2. Loop in `direct` mode (Phases 0–4.3): `AGENT_CMD="claude -p" ./loop.sh`.
3. **Stage B (human, after 4.3):** `flow init/plan/apply/attest` on the repo,
   add `flow.yml` via a gated PR, set `CURRENT MODE: pr` in `.ralph/AGENTS.md`.
4. Loop in `pr` mode: the agent opens PRs; you review; auto-merge on green gate.
5. **Stage C:** v1 is done after 14 consecutive green daily doctor runs.

## Demo — flow runs on flow

This repository is flow's own live demo. The workflow below is not a diagram of
what flow could do: it is the one flow enforces here, on itself, every day.

| | |
|---|---|
| Board | <https://github.com/users/17twenty/projects/2> |
| Health | [#92 `flow health: 17twenty/flow`](https://github.com/17twenty/flow/issues/92) — rewritten in place by the daily run, carrying its own 30-day log |
| Agent pull requests | [`is:pr label:agent`](https://github.com/17twenty/flow/pulls?q=is%3Apr+label%3Aagent) — opened by `flow-bot-gh`, approved by `@17twenty`, merged on a green `flow/policy` |
| The gate | [`.github/workflows/flow.yml`](.github/workflows/flow.yml) — builds flow from the pull request's own checkout and judges it with that |

**Streak: 0 of 14 green daily runs** (as of 2026-10-13). The number lives in the
health issue, which is where to read it rather than here: this line is a date
and a claim, and that issue is the record. The clock has not started, and the
report says exactly why — `live.tickets.done_merged` counts 66 of 82 tickets
that reached Done with no merge behind them, every one of them closed in
`direct` mode before the gate existed, and four native behaviours are waiting
for a person to vouch for them (`docs/cookbook.md` §6.1). Neither is a defect,
and both are things the fourteen days are meant to be sensitive to.

What the audit reads today, against the expectations of
[spec 17 §7](.ralph/specs/17-dogfood.md):

| Check | Expected | Today |
|---|---|---|
| `live.prs.gated` | every merge carried `flow/policy` | ✓ all 17 |
| `live.prs.linked` | every merge named a ticket | ✓ all 17 |
| `live.prs.reviewed` | a person approved every agent pull request | ✓ all 17 |
| `live.deploys.lag` | shipped within 2h | ✓ all 17 |
| `live.transitions.native` | the tracker moved its own cards | ✓ 16 on `pr_merged`, 80 on `issue_closed` |
| `live.tickets.review_has_pr` | nothing waits for a review of nothing | ✓ both |
| `live.tickets.ready_dor` | everything Ready meets the Definition of Ready | ✓ nothing is Ready |

## Spec index
| # | Topic |
|---|---|
| 00 | Overview, scope, principles (native first, no user regex, verify behaviour) |
| 01 | Architecture, packages, dependency allow-list |
| 02 | CLI (complete) |
| 03 | Configuration (`.flow.yaml`, lock, TOML, env) |
| 04 | Workflow model, linkage, transition ownership, done_on |
| 05 | Capability model and outcomes |
| 06 | Plan and apply |
| 07 | Doctor: setup checks + live-health audit |
| 08 | Conventional commits (parser + linter) |
| 09 | Policy gate (`check pr`) |
| 10 | Forge adapters |
| 11 | Tracker adapters |
| 12 | TUI (minimal) |
| 13 | Deployment signals |
| 14 | Testing |
| 15 | Security |
| 16 | Release of flow (delegated, verified) |
| 17 | Dogfooding: flow builds flow |
| 18 | Serve mode (future constraints) |
| 19 | Acceptance scenarios |

RFC 2119 language. Specs win over examples; fix examples in the same change.
