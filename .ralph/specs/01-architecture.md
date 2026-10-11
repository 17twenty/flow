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
- **§3 the shapes are indicative, and three methods were added to `Forge`
  (item 2.3).** `Compare(base, head)` because spec 13 §3 proves a merge
  reached an environment by ancestry and nothing else could answer it;
  `File(path, ref)` because doctor's `forge.files` check has to read the
  workflow on the default branch to see that it runs `flow check pr`; and
  `Comment(number, marker)` because `DeleteComment` already has to find a
  comment by its marker, and a gate that cannot read its own last comment
  cannot tell an unchanged verdict from a new one (spec 09 §2). Nothing was
  removed.
- **§5 `core.Clock` is `Now` and `Sleep` (item 2.3).** A live check is a pure
  function of what it fetched and what time it is, so the time has to be an
  input; and the retry budget in `core.Client` is the other thing that must
  not really wait in a test. `coretest.FakeClock` advances instead of
  sleeping and records what it was asked to wait for, so a backoff can be
  asserted rather than endured.
- **§3 an observation is a list of items, not a shape per desired item (item
  2.3).** `core.Item{ID, Name, Fields map[string]string}` describes a status
  option, a label and a WIP limit equally well, so `Observe` and the desired
  state have one shape and the planner can diff them without knowing what
  they are. The alternative — a Go type per row of spec 06 §1 — would make
  every new desired item a change to the planner as well as to the adapter.
  `Item.Equal` treats a missing id as "not yet created", because a desired
  item cannot know the id the provider will give it.
- **§2 the config → commits mapping lives in engine (item 2.13).**
  `internal/commits` depends on the standard library alone and must not read
  `.flow.yaml`, so something has to turn `commits.*` into `commits.Settings`.
  `engine.CommitSettings` does it, because engine already depends on both and
  the policy gate is the first caller that needs it. The defaults are filled in
  there too, so that an incomplete configuration lints by the same convention
  spec 03 §1 ships.
- **§5 `core.Client` is one `Do` over a `Request`, not a method per verb (item
  2.16).** Every adapter needs the same four things — the URL resolved against
  a base, a JSON body in, a JSON body out, and the provider's metadata — and
  anything shaped `Get/Post/Patch` either multiplies by the number of verbs or
  hides the headers the adapters actually need. `Response` therefore carries
  the parsed `Link` relations and the rate-limit numbers alongside the body, so
  pagination and throttling are the client's job once rather than each
  adapter's job again.
- **§5 retries are for idempotent methods, and for anything the provider
  refused to perform (item 2.16).** A 5xx on a POST may mean the write landed
  and the answer was lost, so it is never replayed; a 429 or a secondary rate
  limit means the provider did not act at all, so it is replayed whatever the
  method. `Request.Retry` lets an adapter say `RetryAlways` for a write it
  knows to be idempotent at the provider, or `RetryNever` for one it does not.
  The backoff is injectable so that a test asserts the wait instead of
  enduring it.
- **§5 the ETag cache is keyed by a fingerprint of the credential (item
  2.16).** An ETag is only valid for the token that obtained it — a token that
  may see less of a resource must never be handed another token's body — so
  the key is `sha256("flow-token-v1\x00"+token)[:8]`, the method and the full
  URL. The fingerprint is safe to log and appears in the client's debug
  records; the token itself never reaches the key, the file name or the disk.
  Only GET is cached, and only when the response carried an ETag.
- **§5 the status mapping has three additions (item 2.16).** Spec 01 §5 names
  401/403, 404, 409/412, 429 and 5xx. `410 Gone` joins 404, because a resource
  that was deleted is one that is not there; `405` and `501` map to
  `ErrUnsupported`, because the provider is saying this adapter asked for
  something it does not do; every other 4xx maps to `ErrValidation`, which is
  what a 400 or GitHub's 422 is. A 403 is read as rate limiting rather than
  refusal when the budget is spent, a `Retry-After` was sent, or the message
  says so — otherwise the two are indistinguishable from the status alone.
- **§5 a cancelled context is returned as itself (item 2.16).** Everything else
  is wrapped in a `UserError` carrying a sentinel, but `context.Canceled` is
  how the root tells an interrupt (exit 130) from an outage (exit 69), so
  wrapping it as `ErrUnavailable` would turn Ctrl-C into a reported failure.
  A deadline, by contrast, is an outage and says which timeout elapsed.
- **§5 bounded concurrency belongs to the client (item 2.16).** "Default 4 per
  provider" is a property of the connection to a provider, not of any one
  caller, so the limit is a buffered channel inside `Client` and an adapter
  cannot forget it. It needs no dependency; `golang.org/x/sync/errgroup` stays
  for the places that fan out over several providers.
- **§2 pagination and error restatement belong to `core` (item 3.6).**
  Adapters may not import each other, so the Link-following walk and the
  helper that restates a provider's error as the thing flow was trying to do —
  both written for `internal/forge` — would have had to be copied verbatim
  into `internal/deploys`. They are properties of `core.Response.Links` and of
  `core.UserError`, not of any provider, so they became `core.Paginate` and
  `core.Explain` and the forge adapter now uses them. What is really GitHub's —
  its media type, its API version, the shape of a repository path — stays with
  each adapter that speaks GitHub, where a second provider will not inherit it.
