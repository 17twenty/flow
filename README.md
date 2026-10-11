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
| `loop.sh` | Loop runner (direct and PR modes) |
| `bootstrap/seed-issues.sh` | Creates phase + item issues and adds them to the Project |
| `dogfood/.github/workflows/flow.yml` | The gate workflow; added to `.github/` in bootstrap Stage B |

## Bootstrap (summary — full detail in specs/17-dogfood.md §5)
1. **Stage A (human, ~30 min):** repo, user Project (Kanban template), built-in
   workflows, labels, agent GitHub App, `FLOW_PROJECT_TOKEN` secret, copy files
   (not `flow.yml` yet), `./bootstrap/seed-issues.sh 17twenty/flow <project#>`.
2. Loop in `direct` mode (Phases 0–4.3): `AGENT_CMD="claude -p" ./loop.sh`.
3. **Stage B (human, after 4.3):** `flow init/plan/apply/attest` on the repo,
   add `flow.yml` via a gated PR, set `CURRENT MODE: pr` in `.ralph/AGENTS.md`.
4. Loop in `pr` mode: the agent opens PRs; you review; auto-merge on green gate.
5. **Stage C:** v1 is done after 14 consecutive green daily doctor runs.

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
