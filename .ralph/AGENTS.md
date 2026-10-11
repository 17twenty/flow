# AGENTS.md — operational guide (< 150 lines)

CURRENT MODE: direct
<!-- Human switches to `pr` at bootstrap Stage B (specs/17-dogfood.md §5). -->

## Identity
- Act only as the GitHub App bot `17twenty-flow-agent[bot]` (GH_TOKEN = installation token).
- Never act as @17twenty. Never approve PRs.

## Toolchain
- Go: latest stable (`go version`). Module `github.com/17twenty/flow`, `package main` at root.

## Commands
- Build: `go build -o bin/flow .`   Run: `go run . <cmd>`
- Test: `go test ./... -race -count=1`   One: `go test ./internal/commits -run TestParse -v -count=1`
- Golden update: `go test ./internal/engine -run Golden -update`
- CLI tests build with: `go build -tags flowtest`
- Vet/static/vuln: `go vet ./...`; `go run honnef.co/go/tools/cmd/staticcheck@latest ./...`; `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
- Issues/Project: `gh issue view N`, `gh issue develop N --checkout`, `gh project item-list <n> --owner 17twenty --format json`

## Conventions (specs win)
- YAML `go.yaml.in/yaml/v3` (strict). TOML `github.com/BurntSushi/toml`.
- TUI `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2` (v2 API only).
- CLI kong; one `cmd_*.go` per command. slog via redacting handler. Git via exec.
- No network in tests; `httptest` fakes; transport guard.

## Learnings
- kong calls `os.Exit` for `--help`/parse errors; pass `kong.Exit(panic)` and recover in `run()` so it returns a code and stays testable.
- Test-only commands go in `testcmds_flowtest.go` via `kong.DynamicCommand`; `testcmds.go` (`!flowtest`) returns nil so the shipped surface stays spec 02 exactly.
- `go:embed schema` fails while `assets/schema/` holds only `.gitkeep` — use `all:schema`.
- staticcheck U1000 fires on helpers written before their callers. Write each helper with the plan item that uses it, not ahead of it.
- Signal tests: `signal.Notify` in the test first, so a signal landing before `run()` registers cannot kill the test process.
- GitHub push protection rejects token-shaped literals even in test fixtures. Assemble fake credentials from concatenated parts at run time.
