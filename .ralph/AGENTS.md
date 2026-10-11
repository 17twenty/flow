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
- (none yet)
