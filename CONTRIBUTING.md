# Contributing to flow

flow is gated by flow. Everything below is what its own gate and daily audit
will hold your change to, so it is worth reading before the first pull request
rather than after the first red check.

## Build and test

Go is whatever `go.mod` names; CI reads the same file.

```sh
go build -o bin/flow .                 # the binary
go run . doctor                        # run it from source
go test ./... -race -count=1           # every package
go test -tags flowtest . -count=1      # the CLI, end to end, against fakes
go test ./internal/engine -run Golden -update   # after an intended output change
```

Before pushing, run what CI runs — `CI` is set on the runner and flow reads it,
so a suite that passes without it can fail there:

```sh
gofmt -l .
go vet ./... && go vet -tags flowtest ./...
CI=true go test ./... -count=1
CI=true go test -tags flowtest . -count=1
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
```

Tests never touch the network: adapters are tested against `httptest` fakes
that answer what the real provider answers, and a transport guard fails any
test that tries.

## Where things are

| | |
|---|---|
| `main.go`, `cli.go`, `cmd_*.go` | The CLI, one file per command |
| `internal/core` | The provider interfaces and shared types |
| `internal/engine` | Desired state, plan, apply, doctor, the gate, transitions |
| `internal/forge`, `internal/tracker`, `internal/deploys` | One adapter per provider |
| `internal/config`, `internal/commits`, `internal/git`, `internal/ui` | Configuration and lock, the commit linter, git, rendering |
| `assets/` | Embedded capabilities, presets, CI templates and schemas |
| `docs/design/` | The design record. Specs win over code and examples |

## Changing flow

1. **Start from a ticket.** Open an issue with acceptance criteria (the issue
   form asks for them). `feat`, `fix`, `perf`, `refactor`, `test`, `revert`
   and `style` pull requests must close one; `ci`, `build`, `docs` and `chore` may stand
   alone.
2. **Branch from `main`**, never from another open branch: two pull requests
   that each pass against the trunk they left can fail together.
3. **Title it as the squash commit will read**: a
   [conventional](docs/design/08-conventional-commits.md) header with a scope
   from `.flow.yaml`, and short enough that ` (#N)` still fits in 72
   characters.
4. **Write the decision down.** A change to behaviour updates the spec it
   belongs to, and a choice somebody could reasonably have made the other way
   gets a bullet under that spec's *Decisions*: what was seen, what changed,
   and why.
5. **Prove the tests would notice.** Break each new guard on purpose
   (`if false && …`, not a deletion) and see a test fail; a mutant that
   survives is a missing test, not a harmless change.
6. **Open the pull request** with `Closes #N`. `flow/policy` judges it with the
   flow in the pull request itself; [`docs/rules.md`](docs/rules.md) explains
   every finding. A maintainer reviews, and it merges as a squash once the
   branch is up to date and green.

Merging to `main` releases: CI tags the next version and publishes binaries.

## Working with an agent

Agents follow the same rules, plus the identity and operational detail in
[`AGENTS.md`](AGENTS.md). Agent work is labelled `agent`, and a person approves
it before it lands — `agent.human_review` cannot be cleared by the agent itself.
