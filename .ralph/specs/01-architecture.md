# 01 — Architecture, Layout, Dependencies, Standards

## 1. Repository layout (MUST)

```
github.com/17twenty/flow/
├── main.go            # package main: kong parse, signals, exit codes
├── cli.go             # root struct, global flags, adapter registry wiring
├── cmd_init.go        # one file per command, <150 lines, no business logic
├── cmd_plan.go
├── cmd_apply.go
├── cmd_attest.go
├── cmd_doctor.go
├── cmd_check.go       # check pr, check commit
├── cmd_hook.go
├── cmd_schema.go
├── cmd_version.go
├── fakes_flowtest.go  # //go:build flowtest — fake adapter registration for CLI tests
├── assets/
│   ├── embed.go       # package assets (embeds capabilities/, presets/, templates/, schema/)
│   └── …
├── internal/
│   ├── core/          # domain types, interfaces, errors, HTTP client, markers, redaction, clock
│   │   └── coretest/  # fakes + adapter contract suites (test helpers only)
│   ├── config/        # .flow.yaml, .flow.lock, TOML, env, precedence, placeholders, schema
│   ├── git/           # exec git: root, remotes, branch, log, hooks path
│   ├── commits/       # Conventional Commit parser + linter (no composer, no changelog)
│   ├── engine/        # desired state, plan, apply, doctor checks, policy rules, transitions
│   ├── forge/         # github.go (v1), gitea.go, gitlab.go (Phase 7)
│   ├── tracker/       # ghprojects.go (v1), jira.go, linear.go (Phase 7)
│   ├── deploys/       # forge deployments + commit-status sources
│   └── ui/            # lipgloss styles, plain/markdown/JSON renderers, bubbletea models
├── .github/           # flow's own dogfood config (spec 17)
├── .flow.yaml         # flow's own workflow (spec 17)
├── .flow.lock
└── .ralph/            # build loop specs and prompts
```

No other packages. New packages require a proposal in `IMPLEMENTATION_PLAN.md`
"Proposals" approved by a human.

## 2. Dependency direction

- `core` → stdlib (+ yaml for capability decoding).
- `forge`, `tracker`, `deploys` → `core` only. Never each other, never `engine`.
- `commits` → stdlib only.
- `config` → `core`.
- `git` → `core`.
- `engine` → `core`, `config`, `commits`, `git` and adapter INTERFACES (concrete
  adapters injected from `cli.go`).
- `ui` → `core`, `engine` result types. Never adapters.
- Root → everything.

## 3. Core interfaces (shape)

```go
type Forge interface {
    Kind() string
    Capabilities() CapabilitySet
    Probe(ctx context.Context) (Permissions, error)
    Repo(ctx context.Context) (RepoInfo, error)                       // default branch, merge settings
    PullRequest(ctx context.Context, n int) (PullRequest, error)      // incl. linked issues (native), reviews, head SHA, files summary
    PullRequests(ctx context.Context, q PRQuery) ([]PullRequest, error)
    Commits(ctx context.Context, q CommitQuery) ([]Commit, error)
    Protection(ctx context.Context, branch string) (Protection, error) // effective (classic + rulesets)
    SetProtection(ctx context.Context, branch string, p Protection) error // additive
    SetMergeSettings(ctx context.Context, s MergeSettings) error
    CommitStatuses(ctx context.Context, sha string) ([]CommitStatus, error)
    SetCommitStatus(ctx context.Context, sha string, s CommitStatus) error
    UpsertComment(ctx context.Context, pr int, m Marker, body string) error
    DeleteComment(ctx context.Context, pr int, m Marker) error
    ProposeFiles(ctx context.Context, p FileProposal) (PullRequest, error) // never writes default branch
}

type Tracker interface {
    Kind() string
    Capabilities() CapabilitySet
    Probe(ctx context.Context) (Permissions, error)
    Discover(ctx context.Context, what string, hint map[string]string) ([]Candidate, error)
    ParseRef(raw string) (Ref, error)                                 // URLs pasted by humans
    Observe(ctx context.Context, what string) (Observation, error)    // canonical, hashable
    Apply(ctx context.Context, c Change) (ApplyResult, error)
    Ticket(ctx context.Context, key string) (Ticket, error)
    Tickets(ctx context.Context, q TicketQuery) ([]Ticket, error)     // by column, updated window, keys
    Move(ctx context.Context, key string, to ColumnID) error          // engine-owned transitions only
    CreateFollowUp(ctx context.Context, f FollowUp) (Ticket, error)   // idempotent via marker
    FindFollowUp(ctx context.Context, m Marker) (*Ticket, error)
    Comment(ctx context.Context, key string, m Marker, body string) error
    Reopen(ctx context.Context, key string, reason string) error
    LinkedKeys(pr PullRequest) []string                               // linkage per P12 (spec 04 §3)
}

type DeploySource interface {
    Kind() string                                                     // "forge_deployments" | "commit_status"
    Deployments(ctx context.Context, q DeployQuery) ([]Deployment, error) // env, since
}
```

Adapters MUST pass `coretest.ForgeContract` / `TrackerContract` / `DeployContract`.

## 4. Dependencies (exhaustive allow-list)

| Module | Use |
|---|---|
| stdlib | everything possible |
| `github.com/alecthomas/kong` | CLI |
| `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2` | init wizard, pickers, doctor watch (v2 API: declarative `tea.View`, `tea.KeyPressMsg`) |
| `go.yaml.in/yaml/v3` | YAML (`gopkg.in/yaml.v3` is archived; MUST NOT be imported directly) |
| `github.com/BurntSushi/toml` | TOML user config |
| `golang.org/x/term` | TTY detection |
| `golang.org/x/sync/errgroup` | bounded concurrency |

Banned: SDKs for GitHub/Jira/Linear, cobra/viper, logrus/zap/zerolog, testify,
go-git, HTTP frameworks, reflection-based schema generators.
Tools via `go run …@latest`: staticcheck, govulncheck.

## 5. Coding standards

- Latest stable Go; `go.mod` `go`/`toolchain` set accordingly; no cgo.
- `context.Context` first on all I/O; root cancels on SIGINT/SIGTERM.
- `log/slog` only, through the redacting handler (spec 15 §2); logs to stderr.
- Sentinel errors in `core/errors.go`: `ErrNotFound`, `ErrPermission`,
  `ErrRateLimited`, `ErrConflict`, `ErrUnsupported`, `ErrNeedsInput`,
  `ErrValidation`, `ErrUnavailable`. HTTP 401/403→Permission, 404→NotFound,
  409/412→Conflict, 429/secondary→RateLimited, 5xx after retries→Unavailable.
- No global mutable state; injectable `core.Clock`.
- Bounded concurrency (default 4 per provider).
- `core.Client`: timeouts, retries (idempotent methods), rate-limit header
  parsing, ETag cache keyed by token fingerprint, User-Agent
  `flow/<version> (+https://github.com/17twenty/flow)`.
- Deterministic output ordering everywhere (plans, lock, JSON).
- JSON types in `core/jsonapi.go` with `"schema": "flow.<kind>/v1"`.
- Every exported identifier documented. Files < 800 lines.

## Decisions

- **§2 `git` → `core` (item 1.11).** §2 listed every package's dependencies but
  `git`'s. It takes `core`, like `config` does, so that a git that refuses can
  be reported in the same what/why/fix shape as everything else and mapped to
  an exit code. It takes nothing else, and never the network.
- **§1 `internal/git` is read-only (item 1.11).** Every command it runs answers
  a question; none changes a repository. Writing — the commit-msg hook (item
  1.13) — is the root package's job, through the ordinary file system, so there
  is no path by which reading state can alter it.
