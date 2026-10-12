# AGENTS.md — operational guide (< 150 lines)

CURRENT MODE: direct
<!-- Human switches to `pr` at bootstrap Stage B (specs/17-dogfood.md §5). -->

## Identity
- Auth: classic PAT (`GH_TOKEN`). The GitHub App is deferred — flow must stay forge-neutral (specs/17-dogfood.md Decisions).
- Author commits as `17twenty-flow-agent[bot]`: `git -c user.name=... -c user.email=...`. Authorship is independent of the push credential.
- Never approve PRs. API-created objects currently carry @17twenty — blocks `agent.human_review`; resolve before Stage B.

## Toolchain
- Go: the version in `go.mod` (CI uses `go-version-file`). Bump `go.mod` to upgrade; never let CI drift to `stable`.
- Module `github.com/17twenty/flow`, `package main` at root.

## Commands
- Build: `go build -o bin/flow .`   Run: `go run . <cmd>`
- Test: `go test ./... -race -count=1`   One: `go test ./internal/commits -run TestParse -v -count=1`
- Golden update: `go test ./internal/engine -run Golden -update`
- CLI tests build with: `go build -tags flowtest`
- Vet/static/vuln: `go vet ./...`; `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...`; `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
- No `gh` on this machine: use `curl` against `api.github.com` with `GH_TOKEN`.

## Conventions (specs win)
- YAML `go.yaml.in/yaml/v3` (strict). TOML `github.com/BurntSushi/toml`.
- TUI `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2` (v2 API only).
- CLI kong; one `cmd_*.go` per command. slog via redacting handler. Git via exec.
- No network in tests; `httptest` fakes; transport guard.

## Learnings
- kong calls `os.Exit` for `--help`/parse errors; pass `kong.Exit(panic)` and recover in `run()` so it returns a code and stays testable.
- Test-only commands go in `testcmds_flowtest.go` via `kong.DynamicCommand`; `testcmds.go` (`!flowtest`) returns nil so the shipped surface stays spec 02 exactly.
- `go:embed schema` fails while `assets/schema/` holds only `.gitkeep` — use `all:schema`.
- Never use a real credential as a test fixture, even a doomed one: push protection rejects the whole push. Build credential-shaped fixtures from pieces (`"gh" + "p_" + strings.Repeat(…)`) so the file holds no scannable literal.
- staticcheck U1000 fires on helpers written before their callers. Write each helper with the plan item that uses it, not ahead of it.
- Signal tests: `signal.Notify` in the test first, so a signal landing before `run()` registers cannot kill the test process.
- GitHub push protection rejects token-shaped literals even in test fixtures. Assemble fake credentials from concatenated parts at run time.
- `invalid.txt` samples split cleanly: only those expecting `cc.header.format` must fail to parse; the rest must parse so the linter can judge them.
- The commit corpus is the linter's contract: `# expect:` sets are matched exactly (no extras), and `TestCorpusCoversEveryRule` fails if a rule has no sample. Add a sample with the rule.
- `internal/commits` must not import `internal/config` (spec 01 §2). Config maps into `commits.Settings` instead.
- Unpinned analysers break CI with no repo change: `staticcheck@latest` cannot read Go 1.27 export data, so `go-version: stable` turned every push red. Pin the toolchain to `go.mod` and the analyser to a version.
- Verify CI from the API after pushing — a green local tree proves nothing about the runner's Go version.
- Fuzz every rewriter for idempotence, not just for panics: `FuzzFix` found two real bugs in minutes that table tests missed.
- A commit-message rewriter must never change which lines git treats as comments or scissors — trimming `" #x"` to `"#x"` deletes the line.
- Mixing whitespace definitions (`TrimRight` cutset vs `unicode.IsSpace`) breaks idempotence. Pick one per concern.
- `run()` takes stdin as a parameter so commands reading `-` stay testable in-process.
- yaml.v3 locates errors by line and never by column. Decode into a `yaml.Node` first, index key/value nodes by line, and match the decoder's messages against it to recover the column.
- A custom `UnmarshalYAML` must return a `*yaml.TypeError` with a `line N:` prefix, or yaml aborts the decode at that value and drops the position.
- Walking a `yaml.Node` tree must guard against cycles: `&a [*a]` parses into a self-referential node and ate all the memory. Keep a set of nodes currently open.
- Generated YAML: build a `yaml.Node` tree instead of marshalling a struct. Key order, list order and quoting then belong to flow, not to the encoder's internal sort.
- Spec filenames are `05-capabilities.md` and `06-plan-apply.md`. Appending a Decision with `cat >> .ralph/specs/06-*.md` creates a new file when the glob misses, instead of failing. `ls .ralph/specs/` first.
- `govulncheck` was clean only because nothing imported `net/http`. The first real HTTP client turned ten stdlib advisories reachable at once. Expect a `go.mod` bump with the first use of a new stdlib area, and run govulncheck before claiming the item is done.
- `http.Header` literals in tests do not work: `Get`/`Values` canonicalise, so `http.Header{"X-RateLimit-Limit": …}` is never found (canonical is `X-Ratelimit-Limit`). Build test headers with `Set`.
- A transport guard must refuse without retrying. The retry loop happily asked the guard four times before the test noticed.
- A paging test only proves paging if the thing searched for is beyond page 1. Seed the decoys first, the target last, or a read-only-the-first-page mutant survives.
- A surviving mutant is a gap in the tests, never a harmless mutation. `ghLabelDescriptionOf` returning `&""` survived: nothing asserted that a rename leaves a description alone.
- Mutate with `if false && (…)`, not by deleting the line: a deletion that orphans an import is a build failure, which is not the same as a test failure.
- `git checkout` silently does nothing for an untracked file. `cp` a backup before mutating and `grep` to confirm the restore.
- A mutant can survive because the platform, not the test, masks it: lipgloss declines a pipe on Unix, so deleting flow's own `term.IsTerminal` guard broke nothing on macOS. Inject the seam and test flow's decision, not the library's.
- `core.Client.Do` returns `nil, err` on a failing status, so an adapter that needs GitHub's per-error `code` must read the body before writing the error.
- `engine.AppliedChange` has a named field `Change`: it is `change.Change.Item.Name`.
- macOS's case-insensitive filesystem masks cache-key casing bugs. Assert the key, not the file.
