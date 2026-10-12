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
- [x] 1.6 Semantic validation and secret heuristics — specs: 03 §1.1 — deps: 1.4, 2.1 (#19)
  - note: config/semantics.go. Validate(cfg, path, registry) reports every contradiction at once as ParseError problems keyed by config path; the capability registry is what decides whether `owner: native` is honest, and may be nil when a caller cannot supply it. Secret heuristics run at parse time over the document, so a credential is located at its line wherever it was written. Policy rule ids wait for the rule registry (2.13).
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
- [x] 1.10 Presets embedded and valid — specs: 03 §7 — deps: 1.6 (#23)
  - note: config/preset.go reads presets from an fs.FS the root supplies. PresetSource returns the file verbatim so init copies the comments; LoadPreset decodes it. minimal now has no Ready gate at all, per §7. Goldens per preset under internal/config/testdata/presets, written by -update.
  - AC: lean-kanban, deploy-done, minimal decode + validate; golden
- [x] 1.11 internal/git — specs: 01 §1, 10 §1 — deps: 0.2 (#24)
  - note: read-only exec git. Remote parsing drops credentials from the URL; an unborn HEAD is an empty log rather than an error; detached HEAD reports no branch; trailers come from git itself, which does not treat BREAKING CHANGE as one. Forge detection's probing half stays with the adapters (3.1, 3.7).
  - AC: root, remote parsing (SSH/HTTPS/subgroups), log range, hooks path, ls-files tracked, interpret-trailers parsing
- [ ] 1.12 UI styles and plain/markdown/JSON renderers — specs: 12 — deps: 0.3 (#25)
  - AC: golden renders for sample plan and doctor results; ASCII + NO_COLOR
- [x] 1.13 flow hook install/uninstall — specs: 02 §2, 08 — deps: 1.3, 1.11 (#26)
  - note: cmd_hook.go; writes through a temp file, recognises its own work by a marker, defers to lefthook/husky/pre-commit with exit 2. The shipped templates called `flow hook run`, a command that never existed — rewritten to spec 02's `flow check commit --fix`, prepare-commit-msg deleted, and a test now runs every template's invocation with --help.
  - AC: respects core.hooksPath; refuses foreign hooks; prints lefthook/husky/pre-commit snippets
- [x] 1.14 Credential resolution: env name, FLOW_<PROVIDER>_TOKEN, token_cmd — specs: 03 §5, 15 §1 — deps: 1.8 (#88)
  - note: config/credential.go. `Credential` hides its secret behind Secret(), and formats and logs as its source, so it cannot leak through %v or an error. token_cmd is argv with no shell, EOF on stdin, 30s, one line of stdout; only the exit status and a redacted first line of stderr reach the user. Resolution is cached per provider and single-flighted; failures are not cached. Host binding stays with 7.6.
  - AC: resolution order per provider with a table test; token_cmd run as argv with no shell, with a timeout, and only from the user's own config
  - AC: the resolved token is never logged or returned in an error; absent credential names the variable to set

## Phase 2 — Engine with fakes

- [x] 2.1 Capability registry and loader — specs: 05 §1–2 — deps: 0.5 (#28)
  - note: core/capability_names.go holds the registry as constants; core/capability.go decodes strictly from an fs.FS the root supplies, so core keeps off assets. A declaration must cover every capability of its kind, is named after its adapter, and reports all its problems at once; an undeclared capability reads as the zero Capability, which 2.2 resolves to unsupported. Permission tokens stay free-form until Probe (3.7).
  - AC: all assets/capabilities load; names ⊆ registry; compensated_by iff compensate
- [x] 2.2 Outcome resolution — specs: 05 §4 — deps: 2.1 (#29)
  - note: engine/outcome.go is spec 05 §4's ladder literally, plus core.Permissions (tokens + evidence). An attestation substitutes for an observation flow cannot make and never overrules one it can; the zero Permissions allows nothing; every Resolution carries its own why, and names missing tokens.
  - AC: exhaustive table over set/verify/remediation/permission/attestation
- [x] 2.3 coretest fakes and contract suites — specs: 14 — deps: 0.2 (#30)
  - note: core gained the domain types and the three adapter interfaces alongside the fakes, since nothing could be faked before they existed. Forge took Compare, File and Comment; Clock is Now plus Sleep; an observation is a list of core.Item so desired and observed share one shape. Contracts take a fixture and a constructor, and the fakes sit them.
  - AC: FakeForge/FakeTracker/FakeDeploys with fault injection; contracts pass against fakes
- [x] 2.4 Desired state and canonical observation hashing — specs: 06 §1–2 — deps: 1.10, 2.2 (#31)
  - note: engine/desired.go derives spec 06 §1's table from the configuration rather than carrying it; engine/canonical.go is a legible quoted encoding whose hash is what the lock records. A desired field is only ever something the provider can report back. Resolve items have no contents; Additive items allow the provider more than was asked for. Goldens per preset under internal/engine/testdata/desired.
  - AC: pure, golden; hash stable under reordering
- [x] 2.5 Planner and plan JSON — specs: 06 §2, 05 §5 — deps: 2.4, 2.3 (#32)
  - note: engine/observe.go asks each adapter what it holds for every desired item; engine/plan.go is the pure diff, taking providers as {kind, capabilities, permissions} rather than adapters so the same configuration can be planned against another tracker with no network. Contents compare by name and a provider may know more than flow asked. Every item lists its changes whatever the outcome, since those are the content of a follow-up or an attestation. engine/render.go holds spec 05 §5's words; noop renders as `· up to date`. A status option's board position left the desired state: reordering a GitHub Projects option set can clear every item's status.
  - AC: golden plain/JSON for lean-kanban vs GitHub-Projects-like and Jira-like fakes
- [x] 2.6 Comparison mode — specs: 05 §6 — deps: 2.5 (#33)
  - note: engine/compare.go takes two capability declarations rather than adapters, so "no network" is a property of the signature. Both sides are planned as a greenfield with the union of the declaration's own `requires` assumed, which makes the answer about the provider instead of about today's board or today's token. Only tracker items are held: nothing else changes when the tracker does. Rows the two disagree about are marked `≠`. CLI wiring is item 4.2.
  - AC: no network; side-by-side golden
- [x] 2.7 Apply executor — specs: 06 §3–4 — deps: 2.5, 1.7 (#34)
  - note: engine/apply.go walks the plan in its own order, writing the lock after every mutation that lands, so an apply that stops half way has told the truth about the half that happened. A tracker item is a list of changes made one at a time; a forge item is one setting written whole, because protection, the required checks and the merge strategies are each one value and the missing files are one pull request. A failure stops only the items that depend on it. Removal is the only thing that cannot be undone by running again, so every refusal lives in one place. Found and fixed a real bug: a nameless recorded creation matched every other nameless observed thing, so apply left a spurious delete behind.
  - AC: partial failure leaves lock consistent; prune refuses in-use options
- [x] 2.8 Doctor check registry, states, exit aggregation — specs: 07 §1–2, §9 — deps: 2.3 (#35)
  - note: engine/doctor.go and engine/doctor_render.go are the frame the §5 and §6 checks hang in. A check is blocked when something it depends on ran and did not hold, which carries down a chain of any length because blocked does not satisfy either; a dependency nobody selected does not block. The exit code is a property of the states: a failure is actionable when it carries a remedy and hard when it does not, and a run of nothing but errors is 69 or 77 only when the errors agree and nothing else is wrong. A check that panics or answers nothing is an error on that check alone, and the run carries on. `fail_below` is a field on the environment: spec 03 has no key for it yet (see Spec issues).
  - AC: blocked propagation; exit aggregation table; renders golden
- [x] 2.9 Placeholder resolution — specs: 07 §3 — deps: 2.8, 1.5 (#36)
  - note: engine/placeholder.go walks one ladder per placeholder — lock, --set, discover, create, ask — and writes only what a provider read back. The lock and --set come before discovery because auto-taking the only candidate would overrule a human who had just named the board; --set needs no --fix, because what --fix licenses is flow choosing on their behalf. A tracker that will not answer costs flow nothing it already knew: the locked identifier is kept with the reason beside it, and only a tracker that answers and does not list it retires one. The five paths are walked by hand, and only tracker.project is discoverable. Answers go in the lock; the ${pending:…} string in .flow.yaml stays as written.
  - AC: discover/choose/create/ask paths; --set; verified before lock write
- [x] 2.10 Follow-up lifecycle — specs: 07 §4 — deps: 2.8 (#37)
  - note: engine/followup.go is one loop run per check per doctor run: file, chase, verify, reopen, withdraw. It starts from FindFollowUp rather than from the lock, because the lock is the thing most likely to be wrong — a lost record must not cause a second ticket and a claimed-but-deleted one must not stop a real one. An open follow-up turns the check external: somebody else's move, with their name on it, which is a different fact from broken. Verified closes as completed with what flow saw; no-longer-wanted closes as not_planned saying why. The weekly chase is one comment edited in place, so the lock carries nudged_at. core.Tracker gained Close, the missing half of Reopen (spec 11 decision).
  - AC: create, external, verified-close, reopen-on-false-close, no-longer-required, lost-lock recovery by marker
- [x] 2.11 Drift and attestations — specs: 07 §7–8 — deps: 2.8, 1.7 (#38)
  - note: engine/drift.go. Drift is the difference between "never right" and "was right and somebody changed it", and the lock's observed hash is the only before flow has — so a failure with nothing recorded is not drift, and a drifted check deliberately does not overwrite the remembered good state, or the finding would be reported once and then forgotten. A changed hash that still satisfies is absorbed silently. The after is the plan item's own changes. Attestations record the hash of the desired item they covered, so an unrelated edit does not retire them; only a check whose remedy is attest can be attested, which stops `flow attest` being a silencer. One check, one attestation, ninety days by default, seven days' notice.
  - AC: drift evidence; cosmetic absorption; expiry and invalidation
- [x] 2.12 Definition of Ready rules — specs: 04 §7 — deps: 1.4 (#39)
  - note: engine/ready.go. Six pure functions of a ticket and the configuration — nothing asks the network, because the same ticket must get the same answer in `flow check pr` on a laptop, in CI and in doctor's live audit. A rule whose trigger is absent is skipped and satisfied; max_size is on only when a limit is set; a rule this build does not know fails closed, because ignoring it would let work through a gate the team believes is closed. The acceptance-criteria rule distinguishes a missing heading from an empty one, which is the ticket the rule exists to catch.
  - AC: table tests per rule incl. issue-form generated bodies
- [x] 2.13 Policy rules, status description, comment render — specs: 09 §2 — deps: 1.2, 2.12 (#40)
  - note: engine/policy.go and engine/policy_render.go. Nine rules, each a pure function of a snapshot somebody else gathered — the gate must give one pull request the same verdict on a laptop, in CI and in doctor's live audit, so nothing here may ask the network. Strictness is a baseline that `policy.rules` overrides, and `off` means the question is not asked at all rather than answered kindly. Two clauses needed a reading: the review and in-progress columns are the ones the `pr_ready_for_review` and `pr_opened_draft` transitions name, not ids flow does not own; and "entered review before the limit was exceeded" is a place in the arrival order, which a tracker that will not timestamp arrivals cannot give, so the exemption then does not apply. Expedited work is outside the WIP rules entirely — it is the team's own agreed exception. A draft is judged in full and only its status softens to pending. The description's count is never trimmed, only its list.
  - AC: per-rule tables; strictness mapping; 140-char truncation golden; determinism
- [x] 2.14 Transition engine — specs: 04 §4–5 — deps: 1.4 (#41)
  - note: engine/transition.go. It reads state, not events: a webhook action is a claim about a moment that has already passed, and doctor's catch-up has no payload at all, so `flow check pr` and `flow doctor --fix` are the same function and cannot disagree about where a ticket belongs. Each linked pull request is judged from the ticket's real column and the furthest-along answer wins — rule 4 says the review column when *any* pull request is ready, and a sequential fold would let a review asking for changes on the second undo the first. The losers are reported as overruled, not dropped. Natively owned events are reported and not performed, which is safe precisely because the fold starts at the column the tracker's automation has already left the ticket in. Done is the one column a single pull request cannot deliver on its own, and an abandoned one is not work that is still open.
  - AC: owner routing; forward-only; back moves; multi-PR; never out of done
- [x] 2.15 Markers — specs: 11 §1 — deps: 0.2 (#42)
  - AC: both styles round-trip; FuzzMarkerParse
- [x] 2.16 core.Client — specs: 01 §5 — deps: 0.3 (#43)
  - note: core/client.go, httpmeta.go, httpcache.go. One `Do` over a `Request`, because every adapter wants the same four things and a method per verb only multiplies them. Retries are for methods HTTP calls idempotent, plus anything the provider refused to perform — a 5xx on a POST may mean the write landed and the answer was lost, a 429 means it did not happen at all. The ETag cache is keyed by a fingerprint of the token, never the token: an ETag is only valid for the credential that obtained it, so one token must never be handed another's body. A 403 is read as throttling rather than refusal when the budget is spent, a Retry-After was sent, or the message says so. The transport guard is automatic — `testing.Testing()` and nothing to remember — so the one test that forgot its fake fails instead of reaching a real provider.
  - AC: retries, ETag cache keyed by token fingerprint, rate-limit headers, transport guard

## Phase 3 — GitHub forge and GitHub Projects tracker

- [x] 3.1 GitHub: repo, PRs, reviews, commits, compare — specs: 10 §2 — deps: 2.16, 2.3 (#45)
  - note: internal/forge/{github,github_wire}.go. The read half of the forge contract now passes against GitHub's own REST shapes, served by a fake that paginates and answers conditional requests — the two things an in-memory fake can never exercise. The contract suite became addressable case by case (`ForgeContractSubset`) so that an adapter can be held to the part of it that has landed. The methods of 3.2–3.7 refuse with ErrUnsupported and name what they wanted.
  - AC: pagination via Link; conditional requests; contract suite subset passes
- [x] 3.2 GitHub: commit statuses and marker comments — specs: 10 §2, 09 §2 — deps: 3.1, 2.15 (#46)
  - note: GitHub's statuses endpoint is a history and its comments are everybody's, so the adapter turns both into the state flow means: the latest verdict per context, and the one comment carrying flow's marker. Neither is written when the provider already says exactly that, so a gate that runs every minute leaves one status and one comment rather than a thousand. Mutations of one repository are serialised, which is what spec 10 §2's note about secondary limits asks for.
  - AC: status idempotency; comment upsert/delete by marker
- [x] 3.3 GitHub: linked issues via closingIssuesReferences — specs: 04 §3, 10 §2 — deps: 3.1 (#47)
  - note: the first GraphQL query flow makes, sent to the endpoint beside the REST root rather than under it. A ticket in another repository comes back qualified, one in this repository does not, and the walk follows the cursor because the table's `first: 20` is a page size and not a ceiling. GraphQL answers 200 when it fails, so the errors array is read as the refusal it is, with the sentinel its type means. The adapter asks only when its declaration says it can verify the link.
  - AC: cross-repo references returned repo-qualified
- [x] 3.4 GitHub: rulesets/protection read, additive required check, merge settings — specs: 10 §2, 06 §4 — deps: 3.1 (#48)
  - note: GitHub protects a branch two ways, so flow reads both and folds them into one answer: a check required by either is required, and a rule that forbids force-pushing wins over a classic protection that permits it. The classic PUT replaces the whole of a branch's protection, so the merge happens in the adapter against what the branch says now — which is where additivity lives. Protection either mechanism already provides is not written again, taking protection off is refused, and merge strategies are written whole and read back so a pinned repository is a conflict rather than a silent no-op.
  - AC: existing required contexts preserved
- [x] 3.5 GitHub: file proposal via PR — specs: 06 §4 — deps: 3.1 (#49)
  - note: refs and contents, as spec 10 §2 says: branch off the trunk once, write each file quoting the blob it replaces, then open the pull request from that branch or patch the one already open. A file the branch already holds is not written again, so a proposal left unmerged does not grow a commit per apply. The trunk is never written, and a proposal flow could not offer — no id, title, files or path — is refused before anything is sent.
  - AC: re-run updates the same PR
- [x] 3.6 GitHub: deployments read — specs: 13 §3 — deps: 3.1 (#50)
  - note: internal/deploys/{github,github_wire}.go, the first adapter outside the forge. GitHub keeps a deployment's state beside the record rather than on it, so the state is the newest status that is not "inactive" — the word it writes on a record a later deployment superseded, which would otherwise erase every past success. The environment and the commit are the provider's filters; the window is the adapter's, since the endpoint has no "since". Ancestry caching is core.Ancestry, in front of a core.Comparer, because the engine needs it and may not import an adapter: a pair of object names is remembered for good, a branch or a tag never. core.Paginate and core.Explain came out of forge so that deploys did not have to copy them.
  - AC: newest successful per env; ancestry via compare cached
- [x] 3.7 GitHub probe — specs: 05 §3 — deps: 3.1 (#51)
  - note: one request, the repository itself: the role is in the body, the classic token's scopes are in a header nothing else sends. A role and a scope are separate limits and both apply, so the write tokens are granted only where the role may write and the token is permitted to use that — `public_repo` against the repository's visibility, not its `private` flag, because an internal repository is neither. A credential that sends no scope header at all is limited by its role alone; guessing a fine-grained token's grants from nothing would only ever be wrong. The trial read of the project is the tracker's probe (3.8), since a forge adapter that reached into a board would have to know a tracker exists. `notYet` is gone: the forge adapter has no stubs left and the whole contract suite runs against it.
  - AC: admin/maintain/push and token scopes mapped; project access trial read
- [x] 3.8 GitHub Projects: resolve/discover/observe (user and org) — specs: 11 §2 — deps: 2.16 (#52)
  - note: internal/tracker/{githubprojects,githubprojects_wire}.go, the first adapter with no REST API at all — a board is read entirely in GraphQL, so the envelope moved to core.GraphQL and only the reading of GitHub's error types stayed here. A project is its number everywhere flow records it, and the address discovery offers is one flow composed rather than one GitHub sent, so what it offers is what ParseRef accepts. The board and its columns are one query, because the columns are the options of its own Status field. A column is its name and its id: a colour in the observation would make recolouring one read as drift. Reading the board is proved by reading it and writing to it needs the `project` scope as well. Both owner kinds sit the contract, since a person's projects and an organisation's are different fields of the schema and different addresses.
  - AC: ParseRef for project URLs; canonical observation of Status options
- [x] 3.9 GitHub Projects: items, Move, ticket model incl. parent — specs: 11 §2 — deps: 3.8 (#53)
  - note: a ticket is an issue and its column is where this board holds it, so an issue on no board is still a ticket and simply sits nowhere — which is what the Definition of Ready has to be able to say about one. Reading a ticket is one query: the issue→item lookup a move needs is the same document the ticket is built from. A key is `#7` here and `owner/repo#7` elsewhere, and nothing else is a key — a bare number would resolve somebody else's reference against this board, silently. The ticket's id is the issue's, not the item's: an item id says where an issue is pinned rather than what it is, and changes when somebody takes it off the board and puts it back. A ticket changed when either half of it did, since editing the issue does not touch the item and moving the item does not touch the issue. A move reads before it writes, refuses a column the board has not got by name, and will not add an issue to the board — the board's own automations run on what is added, and a transition is not where that is decided.
  - AC: idempotent Move; issue→item lookup
- [x] 3.10 GitHub Projects: status option edits with snapshot/restore — specs: 06 §4, 11 §2 — deps: 3.9 (#54)
  - note: GitHub cannot edit one option of a single-select field — the set is written whole, and an option it does not recognise is a new one with a new id that every item pointing at the old one loses. So flow sends each column back with its own id, which is what that input's `id` is for, and which keeps the board's built-in automations hooked up too. The snapshot-restore-verify dance happens regardless, because it is right either way: read every item's column, rewrite, put back by name whatever was cleared, read the board again to prove it. Restoring by name and not by id is the point — an id is what the rewrite may have changed — and a rename carries its items with it. Colour and description are read so they can be sent back but stay out of the observation, or recolouring a column by hand would read as drift and be undone daily. The snapshot covers drafts and PRs, not just tickets, since a rewrite clears their columns identically. Deleting a column with items in it is refused before any write: unlike a cleared value there is nothing that would put them back. The >500 retreat is taken here and not in the planner, because how big a board is has nothing to do with what the configuration asks for; it comes back as `ApplyResult.FollowUp`, which is neither a success the lock may record nor a failure that stops the item.
  - AC: values restored by name; >500 items → follow_up outcome
- [x] 3.11 GitHub Projects: labels, follow-ups, comments — specs: 07 §4 — deps: 3.9, 3.2 (#55)
  - note: a follow-up is searched for before it is filed, every time, and flow also remembers what it filed in this process — GitHub's search index lags its writes by seconds to minutes, which is exactly the window one doctor run spans, so the index alone would file a second ticket for the same check every day. The marker in the body is the truth and the search is only a filter over it: an index that answers with too much is harmless, one that answers late is not. Filing ends with `addProjectV2ItemById`, the one place flow puts an issue on a board, because a follow-up nobody can see on the board is a follow-up nobody will do. A label the repository has not got and an assignee GitHub does not know are left off rather than failing the filing: they are how people find a follow-up, not what makes one true. Nudging is one comment that keeps changing — the comments are read through every page and the marker decides which, if any, is flow's, because flow's is the oldest one on a ticket that has been nudged for weeks. Closing a closed ticket writes nothing, since doctor runs daily and would otherwise move the moment the work was finished, which is what §6 reads to decide whether a follow-up is overdue. Labels are the one thing this adapter reads over REST, because GraphQL has no complete set of label mutations; a label change sends only what it names, so a rename leaves the colour and description a human chose exactly as they were.
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

- **`fail_below` is named in spec 07 §6 but has no key in spec 03** (found by
  item 2.8). The live-health threshold is the one number in §6 a team would
  want to set, and `.flow.yaml` has nowhere to write it. Item 5.1 must either
  add it to spec 03 §1 — `health: { fail_below: 0.9, window: 14d }` would be
  the obvious shape, next to the `--window` flag that already exists — or
  record that it is a flag only. Until then the engine takes it as a parameter
  and the caller defaults it to 0.9.

## Notes
