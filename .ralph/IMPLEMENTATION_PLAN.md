# IMPLEMENTATION_PLAN.md

Line format (parsed by `bootstrap/seed-issues.sh` — keep it exact):
`- [ ] <phase>.<n> <title> — specs: <refs> — deps: <ids>` then `  - AC: …` lines.
After seeding, each item line ends with `(#N)` = its GitHub issue.
Human-only steps use ids `H.<n>` (never seeded).

---

## Phase 0 — Skeleton

- [x] 0.1 Module and kong root with every v2 command registered — specs: 01 §1, 02 — deps: —   (#7)
  - note: kong root, all v2 commands registered, stubs return ErrUnsupported
  - AC: `go install .` produces `flow`; `flow --help` shows groups Setup/Verify/CI/Meta
  - AC: unimplemented commands return ErrUnsupported (exit 1) — the only permitted temporary stub
  - AC: kong parse errors exit 64; SIGINT exits 130
- [x] 0.2 Sentinel errors and exit-code mapping — specs: 01 §5, 02 §4 — deps: 0.1   (#8)
  - note: core/errors.go sentinels + ExitCode, UserError what/why/fix
  - AC: table test maps every sentinel (and wrapped) to its exit code
- [x] 0.3 slog setup with redacting handler — specs: 15 §2 — deps: 0.2   (#9)
  - note: core/redact.go handler; kinds decided in spec 15 Decisions
  - AC: table test covers every key/value/URL pattern in spec 15 §2
- [x] 0.4 flow version — specs: 02 §2 — deps: 0.1   (#10)
  - note: buildDetails from ReadBuildInfo; -X main.version overrides
  - AC: human and --json (`flow.version/v1`) from ReadBuildInfo; ldflags override
- [x] 0.5 assets package with go:embed — specs: 01 §1 — deps: 0.1   (#11)
  - note: assets/embed.go embeds capabilities, presets, templates, schema
  - AC: embeds capabilities/, presets/, templates/, schema/; test lists files
- [x] 0.6 CI green on main — specs: 14, 17 §2 — deps: 0.1   (#12)
  - note: was red since the first push; `stable` drifted to Go 1.27 and broke staticcheck. Pinned to go.mod + staticcheck v0.8.1. Green on d15bb852.
  - AC: `.github/workflows/ci.yml` passes on main

## Phase 1 — Offline core

- [x] 1.1 Conventional commit header parser — specs: 08 §1 — deps: 0.2   (#14)
  - note: internal/commits/parse.go; never errors, 26M fuzz execs clean
  - AC: table tests (scopes, `!`, comment char, scissors, merge/revert/fixup)
  - AC: FuzzParse seeded from assets/testdata/commits; no panics
- [x] 1.2 Linter rules and v2 seed data — specs: 08 §2, §4 — deps: 1.1   (#15)
  - note: all 9 rules; corpus rewritten to v2 ids, decisions in spec 08
  - AC: every v2 rule id implemented with column + fix text
  - AC: seed files rewritten to v2 rule ids; valid.txt → zero errors; invalid.txt → exact expected sets
- [x] 1.3 Safe autofix and `flow check commit` — specs: 08 §3, 02 §2 — deps: 1.2 (#16)  
  - note: commits/fix.go + cmd_check.go; FuzzFix clean over 171M execs. Settings are spec 03 defaults until config lands in 1.4.
  - AC: idempotent fixes; CLI exit codes; --json output
- [x] 1.4 Config types with strict YAML decoding — specs: 03 §1 — deps: 0.2 (#17)
  - note: config/{config,duration,parse}.go; every problem located line:col, decisions in spec 03. Parse applies no defaults — layering lands with precedence in 1.8.
  - AC: unknown keys error with line:col; durations with d/w
- [x] 1.5 Placeholders — specs: 03 §2 — deps: 1.4 (#18)
  - note: config/placeholder.go; Pending type at tracker.project + followups.owners.*, placement enforced in Parse, flow-mapping mistake diagnosed. FuzzParse found a cyclic-alias memory bomb.
  - AC: Pending values only at allowed paths; block-style requirement documented in error
- [ ] 1.6 Semantic validation and secret heuristics — specs: 03 §1.1 — deps: 1.4, 2.1 (#19)
  - AC: one test per rule incl. native-owner capability check
- [x] 1.7 Lockfile reader/writer — specs: 03 §3 — deps: 1.4 (#20)
  - note: config/lock.go; rendered from an explicit node tree so determinism is ours, atomic write via temp+rename, churn rule only moves a timestamp a full TTL forward. assets/examples/flow.lock is now byte-for-byte what flow writes.
  - AC: deterministic golden; atomic write; observed timestamp churn rule
- [x] 1.8 TOML config, precedence, token_cmd trust rules — specs: 03 §4–5, 15 §1 — deps: 1.4 (#21)
  - note: config/personal.go; strict TOML limited to user/ui/log/auth, token_cmd refused outside the user's own config, tracked override refused via an injected Tracked func (config must not import git). Preset layering of .flow.yaml waits for 1.10; credential resolution split out as 1.14.
  - AC: precedence table test; tracked .flow.local.toml refused
- [x] 1.9 JSON Schema and `flow schema` — specs: 03 §6 — deps: 1.4 (#22)
  - note: config/schema.go built in Go; `flow schema` prints it and a test proves it equals the committed assets/schema/flow.v1.json. Parity walks Config and the schema in both directions (reflection in the test only). Kinds other than config stay ErrUnsupported until those documents exist.
  - AC: struct↔schema parity test; go generate writes assets/schema/flow.v1.json; staleness test
- [ ] 1.10 Presets embedded and valid — specs: 03 §7 — deps: 1.6 (#23)
  - AC: lean-kanban, deploy-done, minimal decode + validate; golden
- [x] 1.11 internal/git — specs: 01 §1, 10 §1 — deps: 0.2 (#24)
  - note: read-only exec git. Remote parsing drops credentials from the URL; an unborn HEAD is an empty log rather than an error; detached HEAD reports no branch; trailers come from git itself, which does not treat BREAKING CHANGE as one. Forge detection's probing half stays with the adapters (3.1, 3.7).
  - AC: root, remote parsing (SSH/HTTPS/subgroups), log range, hooks path, ls-files tracked, interpret-trailers parsing
- [ ] 1.12 UI styles and plain/markdown/JSON renderers — specs: 12 — deps: 0.3 (#25)
  - AC: golden renders for sample plan and doctor results; ASCII + NO_COLOR
- [x] 1.13 flow hook install/uninstall — specs: 02 §2, 08 — deps: 1.3, 1.11 (#26)
  - note: cmd_hook.go; writes through a temp file, recognises its own work by a marker, defers to lefthook/husky/pre-commit with exit 2. The shipped templates called `flow hook run`, a command that never existed — rewritten to spec 02's `flow check commit --fix`, prepare-commit-msg deleted, and a test now runs every template's invocation with --help.
  - AC: respects core.hooksPath; refuses foreign hooks; prints lefthook/husky/pre-commit snippets
- [ ] 1.14 Credential resolution: env name, FLOW_<PROVIDER>_TOKEN, token_cmd — specs: 03 §5, 15 §1 — deps: 1.8 (#88)
  - AC: resolution order per provider with a table test; token_cmd run as argv with no shell, with a timeout, and only from the user's own config
  - AC: the resolved token is never logged or returned in an error; absent credential names the variable to set

## Phase 2 — Engine with fakes

- [ ] 2.1 Capability registry and loader — specs: 05 §1–2 — deps: 0.5 (#28)
  - AC: all assets/capabilities load; names ⊆ registry; compensated_by iff compensate
- [ ] 2.2 Outcome resolution — specs: 05 §4 — deps: 2.1 (#29)
  - AC: exhaustive table over set/verify/remediation/permission/attestation
- [ ] 2.3 coretest fakes and contract suites — specs: 14 — deps: 0.2 (#30)
  - AC: FakeForge/FakeTracker/FakeDeploys with fault injection; contracts pass against fakes
- [ ] 2.4 Desired state and canonical observation hashing — specs: 06 §1–2 — deps: 1.10, 2.2 (#31)
  - AC: pure, golden; hash stable under reordering
- [ ] 2.5 Planner and plan JSON — specs: 06 §2, 05 §5 — deps: 2.4, 2.3 (#32)
  - AC: golden plain/JSON for lean-kanban vs GitHub-Projects-like and Jira-like fakes
- [ ] 2.6 Comparison mode — specs: 05 §6 — deps: 2.5 (#33)
  - AC: no network; side-by-side golden
- [ ] 2.7 Apply executor — specs: 06 §3–4 — deps: 2.5, 1.7 (#34)
  - AC: partial failure leaves lock consistent; prune refuses in-use options
- [ ] 2.8 Doctor check registry, states, exit aggregation — specs: 07 §1–2, §9 — deps: 2.3 (#35)
  - AC: blocked propagation; exit aggregation table; renders golden
- [ ] 2.9 Placeholder resolution — specs: 07 §3 — deps: 2.8, 1.5 (#36)
  - AC: discover/choose/create/ask paths; --set; verified before lock write
- [ ] 2.10 Follow-up lifecycle — specs: 07 §4 — deps: 2.8 (#37)
  - AC: create, external, verified-close, reopen-on-false-close, no-longer-required, lost-lock recovery by marker
- [ ] 2.11 Drift and attestations — specs: 07 §7–8 — deps: 2.8, 1.7 (#38)
  - AC: drift evidence; cosmetic absorption; expiry and invalidation
- [ ] 2.12 Definition of Ready rules — specs: 04 §7 — deps: 1.4 (#39)
  - AC: table tests per rule incl. issue-form generated bodies
- [ ] 2.13 Policy rules, status description, comment render — specs: 09 §2 — deps: 1.2, 2.12 (#40)
  - AC: per-rule tables; strictness mapping; 140-char truncation golden; determinism
- [ ] 2.14 Transition engine — specs: 04 §4–5 — deps: 1.4 (#41)
  - AC: owner routing; forward-only; back moves; multi-PR; never out of done
- [ ] 2.15 Markers — specs: 11 §1 — deps: 0.2 (#42)
  - AC: both styles round-trip; FuzzMarkerParse
- [ ] 2.16 core.Client — specs: 01 §5 — deps: 0.3 (#43)
  - AC: retries, ETag cache keyed by token fingerprint, rate-limit headers, transport guard

## Phase 3 — GitHub forge and GitHub Projects tracker

- [ ] 3.1 GitHub: repo, PRs, reviews, commits, compare — specs: 10 §2 — deps: 2.16, 2.3 (#45)
  - AC: pagination via Link; conditional requests; contract suite subset passes
- [ ] 3.2 GitHub: commit statuses and marker comments — specs: 10 §2, 09 §2 — deps: 3.1, 2.15 (#46)
  - AC: status idempotency; comment upsert/delete by marker
- [ ] 3.3 GitHub: linked issues via closingIssuesReferences — specs: 04 §3, 10 §2 — deps: 3.1 (#47)
  - AC: cross-repo references returned repo-qualified
- [ ] 3.4 GitHub: rulesets/protection read, additive required check, merge settings — specs: 10 §2, 06 §4 — deps: 3.1 (#48)
  - AC: existing required contexts preserved
- [ ] 3.5 GitHub: file proposal via PR — specs: 06 §4 — deps: 3.1 (#49)
  - AC: re-run updates the same PR
- [ ] 3.6 GitHub: deployments read — specs: 13 §3 — deps: 3.1 (#50)
  - AC: newest successful per env; ancestry via compare cached
- [ ] 3.7 GitHub probe — specs: 05 §3 — deps: 3.1 (#51)
  - AC: admin/maintain/push and token scopes mapped; project access trial read
- [ ] 3.8 GitHub Projects: resolve/discover/observe (user and org) — specs: 11 §2 — deps: 2.16 (#52)
  - AC: ParseRef for project URLs; canonical observation of Status options
- [ ] 3.9 GitHub Projects: items, Move, ticket model incl. parent — specs: 11 §2 — deps: 3.8 (#53)
  - AC: idempotent Move; issue→item lookup
- [ ] 3.10 GitHub Projects: status option edits with snapshot/restore — specs: 06 §4, 11 §2 — deps: 3.9 (#54)
  - AC: values restored by name; >500 items → follow_up outcome
- [ ] 3.11 GitHub Projects: labels, follow-ups, comments — specs: 07 §4 — deps: 3.9, 3.2 (#55)
  - AC: follow-up created once (marker), added to project
- [ ] 3.12 Contract suites green for github + github-projects — specs: 14 — deps: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 3.10, 3.11 (#56)
  - AC: all contract tests pass against HTTP fakes

## Phase 4 — Commands (activation happens after 4.3)

- [ ] 4.1 flow init (non-interactive) — specs: 02 §2 — deps: 1.10, 1.11, 1.13 (#58)
  - AC: scenarios A01, A02
- [ ] 4.2 flow plan and flow apply — specs: 02 §2, 06 — deps: 2.7, 3.12 (#59)
  - AC: scenarios A03, A04
- [ ] 4.3 flow check pr — specs: 09, 02 §2 — deps: 2.13, 2.14, 3.12 (#60)
  - AC: scenarios G01–G06
- [ ] 4.4 flow check deploy — specs: 02 §2, 04 §6, 13 — deps: 2.14, 3.6 (#61)
  - AC: done_on deployed moves linked tickets; done_on merged no-op exit 0
- [ ] 4.5 flow doctor setup checks — specs: 07 §5 — deps: 2.9, 2.10, 2.11, 3.12 (#62)
  - AC: scenarios A05–A07
- [ ] 4.6 flow attest — specs: 02 §2, 07 §8 — deps: 2.11 (#63)
  - AC: refuses API-verifiable checks with 64

- [ ] H.1 HUMAN: bootstrap Stage B activation (specs/17-dogfood.md §5); set AGENTS.md `CURRENT MODE: pr`

## Phase 5 — Live health

- [ ] 5.1 live.prs.* audits (linked, gated, conventional, reviewed) — specs: 07 §6 — deps: 4.5 (#65)
  - AC: scenarios V02; evidence links; thresholds and critical flags
- [ ] 5.2 live.deploys.* (recent, lag) — specs: 07 §6, 13 — deps: 4.5, 3.6 (#66)
  - AC: scenario V01
- [ ] 5.3 live.tickets.* audits — specs: 07 §6 — deps: 4.5 (#67)
  - AC: done_merged, done_deployed, review_has_pr, progress_stale, ready_dor
- [ ] 5.4 live.transitions.native verification — specs: 07 §6, 04 §5 — deps: 5.3 (#68)
  - AC: scenario V03
- [ ] 5.5 Catch-up of missed flow-owned transitions in doctor --fix — specs: 07 §6 — deps: 5.3, 2.14 (#69)
  - AC: scenario V04
- [ ] 5.6 live.flags.cleanup via git trailers — specs: 07 §6 — deps: 5.1, 1.11 (#70)
  - AC: scenario V05
- [ ] 5.7 --report markdown and pinned health issue — specs: 07 §9–10 — deps: 5.1 (#71)
  - AC: one issue (marker flow:health) updated in place
- [ ] 5.8 --ci annotations and --window — specs: 02 §2, 07 — deps: 5.1 (#72)
  - AC: GitHub annotations for fail/warn

## Phase 6 — TUI

- [ ] 6.1 Init wizard — specs: 12 — deps: 4.1 (#74)
  - AC: plain equivalent produces identical config
- [ ] 6.2 Picker for placeholders — specs: 12 — deps: 4.5 (#75)
  - AC: Update-driven model tests
- [ ] 6.3 Doctor inline spinners and --watch — specs: 12 — deps: 5.7 (#76)
  - AC: golden renders; key bindings tested via Update
- [ ] 6.4 Apply confirmation — specs: 12 — deps: 4.2 (#77)
  - AC: non-TTY without --auto-approve exits 64

## Phase 7 — More adapters and hardening

- [ ] 7.1 Jira Cloud tracker — specs: 11 §3 — deps: 3.12 (#79)
  - AC: contract suite; key fallback linkage; board configuration verify
- [ ] 7.2 Linear tracker — specs: 11 §4 — deps: 3.12 (#80)
  - AC: contract suite; attachment-first linkage
- [ ] 7.3 Gitea/Forgejo forge — specs: 10 §3 — deps: 3.12 (#81)
  - AC: contract suite; commit_status deploy source
- [ ] 7.4 GitLab forge — specs: 10 §4 — deps: 3.12 (#82)
  - AC: both gate strategies; closes_issues linkage; deployments read
- [ ] 7.5 commit_status deploy source — specs: 13 — deps: 3.2 (#83)
  - AC: context exact and prefix* matching
- [ ] 7.6 Credential host approval — specs: 15 §1 — deps: 1.8 (#84)
  - AC: scenarios S01–S03

## Phase 8 — Proof

- [ ] 8.1 All acceptance scenarios present and passing — specs: 19 — deps: 5.8, 6.4, 7.6 (#86)
  - AC: one test per scenario id; gaps recorded under Spec issues, never skipped
- [ ] 8.2 README demo section links live Project, health issue, recent agent PRs — specs: 17 §7 — deps: 5.7 (#87)
  - AC: README states current doctor streak from the health issue

- [ ] H.2 HUMAN: confirm 14 consecutive green daily doctor runs → tag v1.0.0

---

## Proposals

## Spec issues

## Notes
