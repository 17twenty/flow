# AGENTS.md — working on flow as an agent

flow builds flow: this repository runs its own gate, board and daily health
report, so work lands the way flow says work should. People start at
`CONTRIBUTING.md`; this file is the operational detail an agent needs on top.
The design record the code answers to is `docs/design/`.

## Landing work
- `main` is protected by a ruleset: a push is **rejected** unless `flow/policy` already passed on that exact commit. Branch and open a pull request.
- Branch `<type>/<topic>` from `main` (never from another open branch), push it, then open the pull request with `Closes #N` in the body and the `agent` label. `.github/workflows/flow.yml` builds flow from the pull request's own checkout and runs `flow check pr`, which posts `flow/policy` and moves the ticket.
- `flow/policy` stays red until `@17twenty` approves: `agents.authors` holds `flow-bot-gh`, so `agent.human_review` wants a human. That is the gate working, not a fault. Ask, then wait.
- The squash header is the title plus ` (#N)`, and must fit `commits.header_max` (72). Merges are squash, and the branch must be up to date with `main`.
- Before pushing, run the whole suite as CI does: `gofmt -l .`, `go vet ./...` and with `-tags flowtest`, `CI=true go test ./...`, `CI=true go test -tags flowtest .`, and staticcheck.

## Identity
- The agent is the machine user **`flow-bot-gh`** (id 341252896), a repo collaborator with `push` and a WRITER on the Project. The human is `@17twenty`. See `docs/cookbook.md`.
- One credential for everything: its classic PAT in `.flow-bot-token` (gitignored, `PAT_TOKEN=`). API *and* git push. No SSH key — a token over HTTPS is what every forge offers (docs/design/17-dogfood.md Decisions).
- `git config --global` has `url.git@github.com:.insteadOf https://github.com/`, which silently rewrites an HTTPS remote back to SSH and pushes as the human. `origin` is therefore `https://x-access-token@github.com/...` (no prefix match, no rewrite) and `core.sshCommand` is a command that fails loudly. Check with `git ls-remote --get-url origin`.
- Author commits as `Flow Bot <341252896+flow-bot-gh@users.noreply.github.com>`: `git -c user.name=... -c user.email=...`. The noreply form is what makes GitHub attribute the commit to the account.
- Never approve PRs — `@17twenty` does that. The agent authors, the human reviews: `agent.human_review` is now satisfiable.

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
- Spec filenames are `05-capabilities.md` and `06-plan-apply.md`. Appending a Decision with `cat >> docs/design/06-*.md` creates a new file when the glob misses, instead of failing. `ls docs/design/` first.
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
- GraphQL payloads go to `curl -d @file`, never `-d "$(jq ...)"`. An inline command substitution produced a parse error pointing at valid syntax; the identical query from a file succeeded. The bug was the shell, so debugging the query was the wrong layer.
- A guard that only avoids work still changes what a run *says*: dropping doctor's "stop loading once the configuration is incoherent" made it plan against providers it never connected to and warn about them. Mutate the cheap-looking guards too, and assert on stderr as well as the report.
- Two anonymous structs with the same fields are not the same type to `str.replace`, and a method returning one is unreadable. Name the type the moment a second copy appears.
- A python3 heredoc is not a shell heredoc: `\n` inside `'''...'''` becomes a real newline and welds a Go string literal open. Escape it as `\\n`.
- A user-owned Project v2 has its own collaborator list, separate from the repo's. A token without access gets `projectV2: null` — "not found", not "forbidden" — which reads like a wrong project number.
- An acceptance scenario can disagree with the spec it tests (G01 says "status success" of a *draft*, spec 09 says `pending`). The spec wins, the test asserts what they agree on, and the disagreement is a Decision — never a quietly weakened assertion.
- A reference fake can be too well-behaved to reach a branch: `coretest.FakeTracker`'s board is every ticket it holds, so `InProject` was always true. Wrap it and override the one method (`boardlessTracker`, `deletelessForge`) rather than teaching the reference implementation to misbehave.
- Make a verdict fail by breaking the *rule's* input, not by moving the card: a ticket in Backlog still meets the Definition of Ready, so "put it somewhere else" proves nothing. Delete the acceptance criteria instead.
- `core.UserError.Fix` is not in `Error()`. Asserting "the message says to pass --pr" must read the field, or a `WithWhy` that replaces the message silently passes.
- Two mechanisms answering the same question in different code drift apart silently: `PlanInput.attested` re-derived what `FindAttestation` already judged, and only mutation testing showed the plan ignoring the inputs hash. Delegate, never repeat.
- A reference fake world can make a whole family of items unreachable — the CLI fake tracker never observed `tracker.native.*` — so when a test expectation surprises you, compare the fake's behaviour against the real adapter before changing the test.
- Mutating an *absence* means adding the line the code deliberately omits. `attest` not stamping the lock's `config_hash` had no test at all until the stamp was added and nothing failed.
- Read files in small targeted slices (`sed -n`, `grep -n -A`). Large tool output gets elided, and an elided read is a guess.
- Run the thing against the real repo as soon as it builds. The first live `flow doctor` found two bugs no test had reached: a report of twenty skips exiting 0, and `--fix` resolving the board's number into a lock the already-built adapter never re-read.
- An empty result and "nothing was wanted" must not be the same value. A skip is a success, so any code path that turns "could not look" into an empty collection turns a blind run green.
- Grep every new helper's name across `*_test.go` before calling an item done. `followUpSummary`, `followUpSteps` and `changeInstruction` appeared in no test at all, so four mutants were waiting in forty lines of new code.
- A title and a body are two fields, and a test that changes both proves nothing about either. Make one change at a time: the title-only rewrite was the mutant that survived.
- "Flow did not attempt it" and "flow found nothing left to do" are the same empty slice, and the second closes tickets. Any code that judges work from a result must first ask whether the work was tried.
- A mutant survives a test whose fixture cannot tell it apart: `path.Match` treats `**` as `*`, so the "two stars" row matched nothing either way. A row must differ in the *answer*, not only in the input.
- A contract suite written from the fixture's own constants tests the fixture, not the product. `Tracker.Move` took the column's display name everywhere in the engine and only an option id in the adapter; the suite moved by `ColumnInReview = "opt-in-review"`, so both halves agreed with the test and with nothing else. Write at least one contract case in the vocabulary the *caller* has.
- A fake that is *more* generous than the adapter is as wrong as one that is less: `FakeForge.PullRequests` returned whole pull requests, so four call sites read reviews and closing issues that no list endpoint carries, and the live audit reported nobody had approved or linked the one gated PR. Ask of every field on a listing: which endpoint put it there?
- A rule about a fact, written as a rule about the thing that *displays* the fact, has a hole in it exactly where the display is wrong. "Never move work out of done" guarded the column, so a closed ticket whose column lagged (because `issue_closed` is `owner: native`) was dragged back to In review by the next PR to name it — flow making the board untrue. Guard the fact; the column is the symptom.
- A fake that answers fields the query never selected makes a missing field in the query invisible: both GitHub Projects item queries could have dropped `isArchived` with the whole suite green. Render a GraphQL fake's answer from the document it was sent, not from the fixture.
- Two readings of the same item are allowed to disagree, and the adapter has to say so: `ProjectV2.items` hides an archived item and `Issue.projectItems` returns it. Where a provider offers two ways to ask, ask both in one test before trusting either.
- To prove flow delegated the parsing, pick a fixture the two readings disagree about: `Flag-Added: x` followed by a paragraph of prose is prose to `git interpret-trailers` and a match to any regex. A fixture both readings agree on proves nothing about which one ran.
- The runner sets `CI`, and flow reads it: a suite that only says "both ends are terminals" is answered no on the runner and yes everywhere else. Run `CI=true go test ./...` before pushing anything that asks a question.
- A gate judges what the forge will *write*, never what the author typed. GitHub squashes a 66-character title into a 73-character header by appending ` (#N)`, so `pr.title.conventional` passed work that broke `cc.header.length` the moment it landed — the gate and the live audit disagreeing about the same commit. Ask the adapter what it will write (`core.PullRequest.SquashHeader`) and lint that; a verdict passed on a string the author cannot see must say so in its evidence.
- Green is a claim about a tree, not about a branch: two pull requests each passing against the trunk they left can be red together, and `main` broke that way. A gate is only evidence about what lands if the branch was up to date when it ran — `strict_required_status_checks_policy` — and until it is, rebase onto the trunk and re-run before asking for a merge.
- A repeating timer must have exactly one chain of ticks, wound where it is read and nowhere else. Winding it again from whatever finished a round looked identical in every test and left a second chain behind each manual refresh, so a window open all afternoon asked a forge faster and faster. Mutate the *scheduling*, not just the work.
- A flag whose every usable value is the one it already had is not a flag. `--done-on` and `--deploy-source` were accepted, written and tested, and the first test that set each *on its own* showed both were unusable: the keys they set have to agree with the rest of their block, and `init` splices scalars rather than restructuring a preset. Test a new flag alone, not alongside the preset that makes it a no-op.
- A mutation harness left running is a second author. An interrupted run leaves its last mutant in the tree, and the next run then reports every mutant KILLED against the failures that mutant is causing. Check `Jobs` and `grep` for `true ||` / `false &&` before believing anything a worktree tells you.
- A screen cannot be read back from the terminal: a bubbletea program erases its frames as it leaves, so stdout after the run holds only what was printed afterwards. Assert what the screen *did* (the file it wrote, the answers it collected) and assert what it *says* through the model's own View, not by scraping ANSI out of the buffer.
- Accepting both sides of a conflict is right when in doubt and wrong when left there. Three pull requests writing to the same narrative file left two statements of what to do next, four paragraphs each claiming to be "this run", and two welded together with no blank line between them. A file that records a decision can hold only one answer to the same question: after a batch of merges, read the tail of the run record as a reader would, not as a diff.
- Moving an adapter onto an interface moves its tests; it does not delete them. The paging, window and dedupe tests that proved an HTTP walk now belong to the adapter still doing the walking, and the caller must instead prove what it *asks for*. Find the test in the receiving package before dropping the one you are holding — and delete the local fallback the collaborator's contract already guarantees, or it becomes an untestable branch.
- Anything a tool writes for its own gate to judge must be composed against the gate's rules, never written down as a constant. `chore(flow): refresh .flow.lock` was a string in two files; this repository allows neither that scope nor that type unlinked, so flow opened a pull request every morning that flow then refused. A constant cannot keep to a convention it has never read — and the fix is cheap only because the linter was already there to ask.
- A rule written as two clauses is only tested by a fixture the two clauses disagree about. `pr.ChangesRequested() || len(pr.Approvals()) == 0` looked covered by a reviewer who approved and then asked for changes — but `Approvals()` already drops an approval the same person overtook, so the first clause could be deleted with the suite green. It took a *second* reviewer, so that an approval survives beside the refusal, to tell the two apart.
- A fixture of what a provider holds must include the answers that are wrong to pass on. The merged pull request in the forge fixture carried no mergeability, so the adapter rule "drop it for anything already finished" could be deleted and nothing moved. A provider that keeps answering about work it has finished with is exactly what that rule exists for; the fixture has to say so.
- An event with no moment behind it is a state, and a date invented for it will be a date somebody's grace period waits out. `branch_created` has no announcement and no timestamp anywhere: the tracker records the link and never when it was made. Dating it from the ticket's `updated_at` looked right in every test with a dated fixture and silently never started work on a board that dates nothing. Ask what the field *is* before reusing the nearest one that is populated.
- A test that reads a rendering is reading a width too. kong wraps `--help` to the environment's terminal, so a wrapped description opening with `--plan.` was a flag of `flow plan` on the runner and nothing at all here. Pin the width in the test and parse by column; a rendering read at whatever width the machine happened to have is a different test on every machine.
- Ask why a ticket was closed, not only whether. "Not planned" is a person's answer, and flow's own withdrawals close with the same reason: tell them apart by what flow recorded itself, never by the ticket alone. Reopening every closed follow-up argued with people who had already decided.
- A lifecycle round that does nothing must still report the state it holds. `Report.Absorb` skips `FollowUpNone`, so a declined follow-up that said nothing on its second run fell back to the check's raw failure. Quiet is not the same as unchanged.
- A screen tested on its own proves the screen, not the program driving it. The doctor progress screen quit when sent `Finished`, and its test sent it by hand; the command never sent it, so every interactive `flow doctor` hung — and ignored ctrl-c, which a raw-mode terminal delivers as a key, not a signal. Nothing automated ever draws a screen (CI, pipes, `--json` all skip it), so drive the real command on a faked terminal (`terminals(t, …)`) for every screen, both left alone and interrupted, and run it yourself in a real terminal before calling a screen done.
