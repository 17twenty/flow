# 10 — Forge Adapters

Read `assets/research/tracker-forge-research.md` first. Thin REST/GraphQL clients
over `core.Client`; no SDKs.

## 1. Detection (init)

Parse `git remote get-url origin` (SSH/HTTPS, subgroups). `github.com` → github.
Other hosts: probe `/api/v3/meta` (GHES), `/api/v4/version` (GitLab),
`/api/v1/version` (Gitea; `/api/forgejo/v1/version` → flavor forgejo). Else ask.

## 2. GitHub (v1)

Headers: `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`.

| Need | Endpoint |
|---|---|
| Repo, permissions, merge settings | `GET/PATCH /repos/{o}/{r}` (`allow_squash_merge`, `allow_merge_commit:false`, `allow_rebase_merge:false`, `squash_merge_commit_title: PR_TITLE`, `squash_merge_commit_message: PR_BODY`) |
| PR | `GET /repos/{o}/{r}/pulls/{n}`; reviews `/pulls/{n}/reviews` |
| PRs in window | `GET /repos/{o}/{r}/pulls?state=closed&sort=updated&direction=desc` (stop at window) |
| Linked issues | GraphQL `repository{pullRequest(number){closingIssuesReferences(first:20){nodes{number repository{nameWithOwner}}}}}` |
| Commits | `GET /repos/{o}/{r}/commits?sha=&since=` |
| Compare (ancestry) | `GET /repos/{o}/{r}/compare/{base}...{head}` → `status` ∈ {ahead, identical} means base ⊑ head |
| Statuses | `GET /repos/{o}/{r}/commits/{sha}/statuses`; `POST /repos/{o}/{r}/statuses/{sha}` |
| Protection | `GET /repos/{o}/{r}/rules/branches/{b}` (effective rulesets); classic `GET/PUT /repos/{o}/{r}/branches/{b}/protection`; add context `POST …/protection/required_status_checks/contexts` |
| Comments | `GET/POST/PATCH/DELETE /repos/{o}/{r}/issues/{n}/comments[/{id}]` |
| Deployments | `GET /repos/{o}/{r}/deployments?environment=&sha=`; statuses `GET /repos/{o}/{r}/deployments/{id}/statuses` |
| File proposal | refs + contents API on branch `flow/setup-<id>`, then `POST /pulls` |

Notes: commit statuses need only "Commit statuses: write"; Checks API needs a
GitHub App (not used in v1). Conditional requests (ETag) for REST polling; 304s
don't count against the primary limit; ETags are token-scoped. `GITHUB_TOKEN`:
1,000 req/h per repo. Serialize mutating calls per repo. Pagination via `Link`.

## 3. Gitea/Forgejo (Phase 7)

`/api/v1`: statuses `POST /repos/{o}/{r}/statuses/{sha}`; branch protection with
`enable_status_check` + `status_check_contexts` (merges refused until success);
PR linked issues: parse none — Gitea links via closing keywords in PR body;
expose them only if the API returns references at build time, else linkage =
`unsupported` → tracker fallback rules. Deployments: none → `deploys.source:
commit_status`.

## 4. GitLab (Phase 7)

`/api/v4`. Gate: Ultimate external status checks (results via API) or commit
status + "pipelines must succeed" (pass `pipeline_id` of the MR pipeline to avoid
duplicate-pipeline hazards). MR closing issues: `GET
/projects/:id/merge_requests/:iid/closes_issues` (structured). Deployments:
`GET /projects/:id/deployments?environment=&status=success`.

## Decisions

- **§1 detection is split: parsing now, probing with the adapters (item
  1.11).** `internal/git.ParseRemote` turns a remote URL into host, owner and
  repository; deciding what a non-`github.com` host *is* needs `core.Client` and
  belongs with the adapters that do the probing (items 3.1, 3.7). Nothing in
  `internal/git` touches the network.
- **§1 the scp-like form is recognised by git's own rule (item 1.11).** A colon
  that comes before any slash makes `[user@]host:path`; anything else is a URL
  or a path on this machine. Reimplementing a cleverer rule would disagree with
  git about somebody's remote, which is the one thing the parser must never do.
- **§1 owner is everything above the repository (item 1.11).** GitLab nests
  groups arbitrarily deep, so `Owner` is the whole path above the final
  segment — `acme/platform/tooling` — and `Repo` is the last segment with one
  `.git` suffix removed. A URL that names a host but no owner is refused rather
  than half-parsed: every forge flow speaks to addresses repositories as
  `owner/name`.
- **§1 a remote URL never carries a credential out of the parser (item
  1.11).** `https://<token>@host/owner/repo` is a working clone URL, so
  `Remote.URL` is rebuilt without any userinfo and `Remote.User` is populated
  only for ssh, where the user names an account rather than hides a secret.
  Spec 15 §2's redactor cannot help here: a bare token in the userinfo position
  matches no known shape once it is somebody's custom forge token.
- **§1 a local path is parsed, not refused (item 1.11).** `/srv/git/flow.git`
  and `file://…` remotes give a `Remote` with no host, which `Local()` reports.
  Detection then has something honest to say — flow cannot talk to it — instead
  of the parser failing on a repository that is otherwise fine.
- **§2 GitHub has no merged state, so flow filters what GitHub cannot express
  (item 3.1).** `state=closed` returns merged and abandoned pull requests
  alike; a merged one is a closed one that carries `merged_at`. The adapter
  asks GitHub for the widest state it understands and then drops what the
  query did not want, rather than pretending the provider can answer a question
  it has no word for. The same reading gives `MergeSHA`: it is
  `merge_commit_sha` only once `merged_at` is set, because GitHub leaves that
  field populated on a pull request that was closed unmerged.
- **§2 pagination follows the Link header and is bounded (item 3.1).** The walk
  follows `rel="next"` as an absolute URL, carrying the cursor, the window and
  the page size the provider chose, and never counts pages of its own. It stops
  at 500 pages with `ErrUnavailable`: a provider whose Link header points at
  itself is a bug flow must survive rather than loop on. The walk also stops as
  soon as the caller has what it asked for — a limit, or the first item older
  than the window on a list sorted newest first — so a one-page question costs
  one request.
- **§2 a ref's slashes stay slashes in the URL (item 3.1).** `release/1.0` is
  written into the path as it appears; escaping it to `release%2F1.0` makes
  GitHub look for a branch of that literal name. Every other character is
  escaped, so a segment can still never change the URL's shape.
- **§2 a review nobody submitted is not a verdict (item 3.1).** GitHub returns
  `PENDING` reviews — ones being written — from the reviews endpoint. They are
  dropped rather than mapped, because the policy gate counts approvals and a
  review in progress approves nothing. `DISMISSED` is kept: it is a verdict
  that was withdrawn, and spec 04 §4's rule that a later review replaces an
  earlier one needs to see it.
- **§2 the file summary is counts from the pull request and one page of paths
  (item 3.1).** `changed_files`, `additions` and `deletions` come from the pull
  request itself and are exact; the paths come from one page of
  `/pulls/{n}/files`, sorted. GitHub caps that list at 3,000 files and no rule
  flow has needs more than a summary, so paging the whole diff would buy
  nothing. The list endpoint carries no counts at all, so a pull request read
  from a list summarises no files until it is read on its own.
- **§2 a file is read raw, not as an envelope (item 3.1).** `Accept:
  application/vnd.github.raw` returns the bytes; the JSON envelope would mean
  base64 and a 1 MB ceiling on a field flow would have to decode itself.
- **§2 an unattributed commit still has an author (item 3.1).** The author is
  the GitHub login when the commit is attributed to an account and the name on
  the commit otherwise, so that a commit pushed from an unknown email is
  reported by whoever wrote it rather than by nobody.
- **§2 an ancestry flow cannot read is unavailable, not false (item 3.1).**
  Compare's `status` is one of four words. Anything else is refused with
  `ErrUnavailable` rather than read as "not contained", because silently
  answering no would make a deploy audit claim a merge never shipped.
- **§2 provider errors are restated, never re-classified (item 3.1).** Every
  call wraps its failure in what flow was trying to do — "read pull request #2
  of acme/widget" — while keeping the sentinel `core.Client` derived from the
  status, so the exit code and the fix stay the provider's and only the
  sentence is flow's.
- **§2 a commit's statuses are a state, not a history (item 3.2).**
  `GET /commits/{sha}/statuses` returns every status ever published, newest
  first, including the ones since replaced. `CommitStatuses` keeps the first it
  sees from each context and drops the rest, because a caller asking what the
  checks say means the verdict, not the argument that led to it. The combined
  endpoint would collapse them too, but spec 10 §2 names this one and it is the
  only one that can be paged.
- **§2 a verdict already published is not published again (item 3.2).** GitHub
  appends; it never replaces. A gate that runs on every push would otherwise
  write the same sentence onto a commit until the status list is unreadable.
  `SetCommitStatus` reads the newest status from its own context first and
  returns without writing when the state, description and target URL all match
  — spec 09 §2's "skip posting" rule, kept by the adapter so that every caller
  gets it and no caller has to remember it. The read stops at the first status
  from that context and is never served from the ETag cache: a verdict is a
  thing flow is about to change.
- **§2 GitHub's own ceiling on a description is the adapter's to keep (item
  3.2).** 140 characters is GitHub's limit and another forge has another one, so
  the adapter cuts a longer description on a rune boundary and marks the cut
  with an ellipsis rather than being refused by the provider. A state GitHub
  does not have, and a status with no context, are refused before anything is
  sent: there is no honest way to publish either.
- **§2 flow edits its own comment and nobody else's (item 3.2).** The comment
  endpoints are addressed as issues, because a pull request is one to GitHub.
  flow lists them, takes the first whose body carries a marker the wanted one
  matches, and edits that; a comment with no marker, or with another kind of
  marker, is somebody else's writing and is left alone. The marker goes in an
  HTML comment, which GitHub keeps and shows nobody. Writing the body it
  already has is not an edit, so a re-run does not bump the comment's
  timestamp or re-notify everybody watching.
- **§2 deleting a comment that is not there is success (item 3.2).** The caller
  asked for it to be gone. A 404 from the delete itself — somebody got there
  first — is the same outcome and is reported the same way.
- **§2 mutations of one repository are serialised (item 3.2).** GitHub's
  secondary rate limit counts concurrent writes to one repository and answers a
  burst with a refusal that costs more than the concurrency bought. The adapter
  holds one lock across each mutation, read-before-write included, so two
  concurrent runs of the gate cannot both find no comment and both write one.
- **§2 the GraphQL endpoint is derived from the REST root, not resolved
  against it (item 3.3).** github.com answers REST at the root and GraphQL at
  `/graphql` beside it; GitHub Enterprise Server puts REST under `/api/v3` and
  GraphQL at `/api/graphql` — a sibling, not a child. Resolving `graphql`
  against the client's base URL, which is how every other path is addressed,
  would send an Enterprise query to `/api/v3/graphql` and get a 404. The
  endpoint is worked out once when the adapter is built.
- **§2 a GraphQL answer carrying errors is a refusal, whatever data came with
  it (item 3.3).** GitHub answers 200 whether the query worked or not and puts
  the reason in an `errors` array, so the HTTP status cannot be trusted to
  decide. The adapter refuses on any error rather than using the partial data
  beside it: a linkage read that quietly returned half the tickets would make
  the gate pass a pull request whose other ticket was never checked. The error
  type chooses the sentinel — `NOT_FOUND`, `FORBIDDEN`/`INSUFFICIENT_SCOPES`/
  `UNAUTHORIZED`, `RATE_LIMITED` — and a type flow has no name for is
  unavailable, because the query is flow's own and was not the user's to get
  wrong.
- **§2 a GraphQL query is retried like a read (item 3.3).** GraphQL is sent
  with POST, which the default policy must assume could have happened twice.
  A query changes nothing, so the adapter asks for it to be retried anyway;
  mutations, when flow grows any, will not.
- **§2 `first: 20` in the linked-issues query is a page size, not a ceiling
  (item 3.3).** The table fixes how many references one page carries; the
  adapter follows `pageInfo.endCursor` until there are none left, bounded by
  the same page limit as the REST walks. A pull request that closes a
  twenty-first ticket is still closing it, and silently dropping it would make
  the gate judge work it could not see.
- **§2 a branch is as protected as the strictest thing said about it (item
  3.4).** GitHub has two mechanisms — classic branch protection and rulesets —
  and a repository may use either, both or neither. `Protection` reads both and
  folds them together: a check required by either is required, the larger
  approval count wins, and a rule that forbids force-pushing forbids it
  whatever classic protection permits. Reading one mechanism would report a
  protected trunk as unprotected for every team that moved to rulesets.
- **§2 rules only ever forbid (item 3.4).** The rules endpoint lists what is in
  force and says nothing about what is not, so a branch with rules starts out
  permitting force-pushes and deletion and is restricted by the rules that are
  there. A branch with no rules and no classic protection is not protected at
  all, which is a different answer from "protected and permissive".
- **§2 protection is merged here because GitHub's PUT replaces (item 3.4).**
  The classic endpoint takes the whole of a branch's protection in one body and
  every field is required, so sending what flow wants would delete the required
  checks, the approval count and the flags nobody mentioned. The adapter reads
  what the branch has, merges flow's intent onto it, and writes the result —
  which is where spec 06 §4's additivity actually lives. The additive contexts
  endpoint in §2's table is not used: it cannot protect a branch that is not
  protected, and a merged PUT is additive by construction, so one call does
  what two would.
- **§2 protection a branch already provides is not written again (item 3.4).**
  If the effective protection — either mechanism — already says everything
  flow wants, nothing is sent. A check required by a ruleset is required; a
  team that moved to rulesets should not find flow writing a classic
  protection beside them on every apply.
- **§2 flow never takes protection off (item 3.4).** `SetProtection` with
  protection that is not enabled is refused rather than translated into a
  delete. Everything else flow does to a repository can be undone by running
  it again; removing the trunk's protection cannot.
- **§2 merge strategies are written whole and read back (item 3.4).** Spec 06
  §4 makes them one setting, so the PATCH carries all of them: the strategies
  a team did not ask for are the ones it wants turned off. Disabling all three
  is refused before anything is sent, because GitHub refuses it too and its
  own message does not say which strategy to put back. The reply is compared
  with what was asked for, so an organisation policy that pins the strategies
  is reported as a conflict instead of passing as a write that did nothing.
- **§2 a proposal is committed with the contents API, one file at a time (item
  3.5).** §2's table names refs and contents, so that is what the adapter uses:
  a branch off the trunk, then a write per file. The git data API would make
  one commit of the lot, at the cost of blobs, a tree and a commit object to go
  wrong; the branch is squashed when it merges, so the tidier history is one
  nobody ever reads. Every write quotes the blob it replaces, which is what
  stops flow from overwriting an edit somebody made to the proposal.
- **§2 a file the branch already holds is not written again (item 3.5).** The
  current content is read before each write and an identical one is skipped.
  `flow apply` is meant to be safe to run on a timer, and a commit per run
  would turn a proposal nobody has merged into a branch nobody can read.
- **§2 the proposal's branch is created once and never reset (item 3.5).** A
  branch that is already there carries the last proposal and may carry a commit
  somebody added to the pull request; forcing it back to the trunk would throw
  that away. flow writes the files it was given onto whatever is there, which
  is what "updated in place" means.
- **§2 the pull request is found by its head branch (item 3.5).** `flow/setup-
  <id>` is the proposal's identity, so the open pull request from that branch is
  the one to update — title and body are patched and no second one is opened.
  Looking it up by the lock would make a proposal unfindable to anybody holding
  a different lock, and GitHub refuses a second pull request from one branch
  anyway.
- **§2 a ruleset that is written and not enforced is read, and is not
  protection (item 3.4, revisited).** GitHub creates every ruleset with its
  enforcement `disabled`, and `evaluate` reports without blocking, so a
  repository can hold a complete, correct ruleset that stops nothing. A
  disabled ruleset is in no branch's effective rules, which means both
  protection endpoints are honestly silent about it and the trunk reads
  exactly like a trunk nobody has thought about. None of it is counted as
  protection — nothing in it is enforced — but it is carried on
  `core.Protection.Dormant`, because the remedy differs entirely: a branch
  with no ruleset needs one written, a branch with a dormant one needs a
  switch flicked, and a report that says the first when the second is true
  sends somebody off to build what they already have. This repository was
  that repository.
- **§2 only the rules that are not in force are fetched in full (item 3.4,
  revisited).** The list endpoint says whether each ruleset is enforced and
  not what it covers, so coverage costs a request per ruleset. An enforced one
  has already been answered by the effective-rules endpoint and is never
  fetched; the usual repository therefore pays one list call and nothing else.
- **§2 coverage is read from GitHub's own patterns, and an unreadable one
  covers nothing (item 3.4, revisited).** `~ALL`, `~DEFAULT_BRANCH` and a
  `refs/heads/` fnmatch pattern are understood, an exclusion beats an
  inclusion, and a tag ruleset is skipped. `**`, which crosses a path
  separator, is not read at all: under-reporting a nicety costs a sharper
  remedy, while over-reporting sends somebody to a ruleset that would never
  have applied, and that is their afternoon.
- **§2 a credential that may not read the rules is not a failed run (item 3.4,
  revisited).** Reading rulesets is a permission of its own. A 403 or a 404
  from it yields no dormant rules and no error, because the reading sharpens a
  remedy and decides nothing — the protection flow actually acts on has
  already been read by then.
- **§2 a listing is a summary, and `core.PullRequest` says which fields it
  cannot carry (bug fix after item 5.8).** The table lists three reads where a
  caller might hope for one: the window from `pulls?state=…`, the reviews from
  `/pulls/{n}/reviews`, and the linkage from the `closingIssuesReferences`
  query. None of them is on the list payload, so `PullRequests` leaves
  `Reviews`, `ClosingIssues` and `Files.Paths` empty and the doc comments on
  both the interface and the struct say so. The alternative — having the
  adapter quietly fill them in — hides a read per pull request behind a call
  that looks like one, and the cost is unbounded by anything the caller chose.
  Anything that judges a merge therefore re-reads it by number, bounded by the
  window it already asked for.
- **§2 an archived repository is the forge's version of the same fact (issue
  #103).** Archiving a repository makes it read-only: GitHub refuses the
  writes, GitLab answers 403, and Gitea/Forgejo built the feature for the same
  purpose — confirm the field each one reports when its adapter is written.
  `core.Repository.Archived` already exists and the GitHub adapter already
  fills it from the repository payload's `archived`; **nothing reads it**, so
  `flow apply` against an archived repository fails item by item instead of
  saying the one thing that is true of all of them. That is issue #102, and
  the refusal belongs in the engine rather than in each adapter, so §3 and §4
  inherit it with the flag. Observing is deliberately unaffected: a read-only
  repository is still worth auditing, and this is the forge-side shape of the
  tracker rule in spec 11 §1.
- **§2 the squash header is the adapter's to report, and GitHub's is composed
  rather than read (issue #106).** `core.PullRequest.SquashHeader` is the
  commit header a squash of that pull request would put on the trunk, and the
  forge contract requires it of every pull request an adapter produces — from
  a listing as well as from the pull request's own endpoint, since a number
  and a title are all it is made of. GitHub serves no such field, so the
  adapter composes `"<title> (#<number>)"`, which is exactly what the merge
  box offers under `squash_merge_commit_title: PR_TITLE`; §3 Gitea/Forgejo
  appends the number the same way and §4 GitLab appends nothing and must
  report the title unchanged. It is filled in for open pull requests too,
  because judging the header before the commit exists is the gate's whole job.
  `MergeHeader()` falls back to the title so a pull request built by hand is
  judged rather than not judged at all; the contract case is what stops that
  fallback from hiding a silent adapter.
- **§2 the refusal #103 promised, placed once (issue #102).** `flow apply`
  reads the repository before the first item and refuses the whole run,
  `ErrConflict`, when it is archived: naming the repository, the archiving and
  the way out. It is in `engine.Apply` and not in any adapter, so §3 and §4
  get it the moment their adapters fill the flag in — which is what the forge
  contract case now requires of all of them. The tracker items go with the
  forge ones, because a board is configured for work in a repository and
  nobody is working in a repository nobody can write to; an apply that
  reconciled half the world would also write a lock claiming it had.
  A repository flow could not *read* is not a repository that refused: the
  read error is carried to the items that needed the answer, one at a time
  with the reason, so a rate limit on the way in is not reported as somebody
  having archived the project. Apply asks "may I write here" and "what is the
  trunk called" of the same endpoint and keeps the answer, failure included —
  two calls to learn one thing could also be answered differently each time,
  leaving a run told yes and then no about the repository it is half way
  through.

- **§2 flow wants the trunk up to date wherever it requires a check (issue
  #116).** `core.Protection.StrictChecks` was read from both GitHub protection
  endpoints, written to both, and merged across rulesets — and the engine
  never asked for it, so `flow apply` never switched it on and `flow doctor`
  could not see it off. It is in `forgeProtection`'s fields now, set when
  `policy.status_context` is — the same condition that makes
  `forge.required_status` a desired item at all.

  The reasoning is about what a status means. A status is about the commit it
  was posted on, so a check that passed on a branch which has since fallen
  behind was passed on a tree that is not the one landing. Two pull requests
  each green against the trunk they left can be red together, which is how
  this repository's own `main` broke (issue #114). Requiring the branch to be
  current is what makes the commit the gate judged the commit that lands, and
  flow claims the gate proves something about the trunk.

  Strictness hangs off the protection item rather than off the required
  checks, because it is a property of the branch — `core.Protection`'s own
  member, next to `allow_force_push` and `require_linear_history` — and
  because the two desired items are one setting at the forge anyway (the
  applier writes them together). Where flow requires no check of its own it
  asks for `false` rather than leaving the field out: an absent field and a
  field that says no are the same value to the comparer, and the one flow
  means is "I am not asking for this", which is what `false` says to a person
  reading the plan.

  Two consequences worth stating. The observation gains a field, so the
  protection hash in an existing `.flow.lock` moves and the first run after
  the upgrade reports drift on `forge.protection` — that is the finding and
  not a bug, and `flow apply` is the remedy. And §3 and §4 inherit the
  obligation: Gitea's `status_check_contexts` and GitLab's "pipelines must
  succeed" each need the nearest thing their forge has to currency, or they
  must observe `StrictChecks` as false and let flow report it as unsupported
  rather than quietly true.
- **§2 a commit status says who it is and when it was written (issue #129).**
  `core.CommitStatus` gained `ID` and `UpdatedAt`. A deployment record built
  out of a status is identified by the first and measured for lag by the
  second (spec 13 §1), and every researched forge carries both on the
  payload, so an adapter that drops them is dropping them rather than working
  around an absence. An identity the provider did not give stays absent
  rather than becoming "0": a link to status zero is worse than no link at
  all.
- **§1 an adapter says which repository it speaks for (issue #129).**
  `core.Forge.Slug()` reports it as the adapter was built, not as the
  provider answers, so an error about a repository flow cannot reach can
  still name it, and anything composed on top of a forge — the commit status
  deployment source — can say where it was reading.
- **§3 one adapter speaks for Gitea and Forgejo (item 7.3).** Forgejo is
  Gitea's fork and serves the same API under the same path, so the kind is
  `gitea` for both and the flavour is a detail of detection rather than of
  configuration. A second kind would double every declaration, every
  capability row and every recipe in the cookbook to say the same things
  twice.
- **§3 `forge.url` is not optional, and silence is a question (item 7.3).**
  GitHub has one host and a Gitea has whichever host somebody installed it
  on, so an empty `forge.url` cannot be defaulted. It is refused in the root
  before the credential is resolved, because spec 15 §1 does not want a
  `token_cmd` that unlocks a vault run to produce a secret for a provider
  flow was never going to speak to. The API root is the instance with
  `/api/v1` appended, and an instance already named with that path is left
  as it is.
- **§3 the link between a pull request and a ticket is unsupported, as §3
  said to confirm at build time (item 7.3).** Gitea stores the links it makes
  from the closing keywords in a body and publishes none of them: no field of
  the pull request payload names an issue and no endpoint lists them. So
  `linked_issues_read` is declared `false` and the engine falls back to the
  branch and the title (spec 05 §2). A pattern of flow's own here would be a
  guess dressed as the forge's answer, which is worse than the fallback it
  would silence.
- **§3 protection is two readings, and only one of them needs an
  administrator (item 7.3).** The branch carries what its protection comes to
  once every rule that covers it has been applied, and any credential that can
  see the branch can see that; the rule carries the settings the branch does
  not mention, and only an administrator may read it. A refusal on the rule is
  therefore read the same way as its absence — what the branch said about
  itself stands — because reporting it would make every read of a protected
  branch fail for a bot account that can do everything else flow asks of it.
  The rule is read under the name the branch gives for it
  (`effective_branch_protection_name`), never under the branch's own: a glob
  rule governs branches whose names it does not share.
- **§3 the same rule is kept at two addresses (item 7.3).** Gitea creates a
  branch protection at the collection and edits it at its own name. Flow
  tries the edit first because it is the ordinary case, and takes the 404 as
  the signal to create. Both carry the whole merged state, so a retry cannot
  do anything the first attempt did not.
- **§3 a comparison is asked twice and named by flow (item 7.3).** Gitea
  answers a comparison with the commits the head has that the base does not,
  and counts them; it never names the relation. One count cannot tell
  `identical` from `behind`, or `ahead` from `diverged`, so the question is
  put both ways round and flow names the relation itself — which is what
  containment proofs in spec 13 §3 depend on.
- **§3 a commit's statuses are a history, and the history is ordered by an
  index (item 7.3).** Gitea replaces nothing: publishing a status appends to
  the commit's history. The walk asks for `sort=highestindex` rather than for
  a time, because two runs of the same workflow second finish in the same
  second and a tie would leave the answer to whichever row the database
  happened to return; the index is assigned on receipt and cannot tie. The
  latest from each context is what `CommitStatuses` returns, since a history
  is not what a caller asking "what do the checks say" means (spec 09 §2).
  Publishing is skipped when that context already says exactly this, with the
  three strings trimmed on flow's side first — Gitea trims them before
  storing, so a description written with a trailing space would otherwise
  differ from the one read back, every run, for ever.
- **§3 what a squash will say is read from the repository once a run (item
  7.3).** Gitea composes the squash commit itself and offers no setting for
  it, so `squash_text.set` is `false` while `verify` stays true: flow can see
  where the words come from and cannot choose. The header is the pull
  request's title with its number appended, and the character in front of the
  number is `#` unless the repository sends its issues to an external
  tracker, when it is `!`. Where the issues of a repository live is a setting
  and a setting does not change halfway through a run, so it is read once and
  kept — but only when it was an answer, since a failed read must never
  become the one the adapter gives for the rest of the run.
- **§3 a file's bytes come from the endpoint that serves bytes (item 7.3).**
  Gitea keeps them at `/raw/{path}` rather than behind a media type on the
  contents endpoint, and the contents endpoint is read only for the blob
  identity an update must quote.
- **§3 the read that decides whether to write goes through the cache like
  any other (item 7.3).** Publishing a status is skipped when the context
  already says exactly this, and that decision is made on a reading of the
  commit — which made it tempting to mark the read "do not cache". It is not
  marked: [core.Client] never believes a stored answer without asking, since
  it sends the tag it holds and takes a 304 as permission to reuse the body,
  so the reading is of the commit as it stands either way. A second way of
  saying the same thing would be a second thing to keep true (spec 01 §5).

- **§4 one host is what "gitlab" means, and the rest must say (item 7.4).**
  Gitea was given no default because a Gitea is wherever somebody put it, and
  `forge.url` is mandatory for it. GitLab is the other case: `gitlab.com` is
  what the name means when nobody says otherwise, so an empty `forge.url`
  resolves there and a self-managed instance names itself. The API root is
  `/api/v4` on both, and the version is in the path rather than in a header —
  GitLab has no dated API version and no vendor media type to ask for.

- **§4 a project is one path segment, percent-encoded (item 7.4).** GitLab
  addresses `acme/widget` as `acme%2Fwidget`, which is a thing a fake can
  silently forgive: a test server that routes on the decoded path would never
  notice the adapter failing to encode. The fake routes on
  `URL.EscapedPath()`, so the encoding is part of what the contract proves.

- **§4 a review and an approval are two readings of one person (item 7.4).**
  GitLab keeps reviewers and approvals in separate endpoints, and an approver
  need never have been a reviewer. Flow wants one answer to "did a human
  approve this", so the adapter reads both and unions them; a person who
  approved without being asked counts, which is what `agent.human_review`
  means by an approval.

- **§4 a tier flow cannot see is a refusal to write, never a failure to read
  (item 7.4).** Approval rules and external status checks belong to tiers not
  every project has, and the answer to both on the tier most people have is a
  403 or a 404. Failing the read would make every reading of a protected
  branch fail on an ordinary GitLab, so a refused endpoint reads as no rule
  and no check — which is true of what GitLab will enforce. The distinction is
  kept where it changes an outcome: `SetProtection` asked to require a named
  check on an instance that has no external status checks refuses with
  `ErrUnavailable` naming the check, rather than writing nothing and reporting
  success. A read that cannot see a paid feature is still an answer; a write
  that cannot happen is not.

- **§4 GitLab answers the query, so flow does not answer it again (item
  7.4).** The listing's state, target branch and window are all parameters
  GitLab's own endpoint takes, and the first draft filtered the answer by the
  same three in flow. Two mechanisms answering one question drift apart
  silently, and the second one here was unreachable: no merge request GitLab
  excluded could arrive for flow to exclude. The callback now collects and
  counts, and `TestGitLabAsksTheQuestionInTheQuery` is what holds the query
  to its job. Gitea keeps its filtering because Gitea's listing has no window
  to ask for.

- **§4 an issue flow cannot name is not a link (item 7.4).** A project wired
  to an external issue tracker answers `closes_issues` with an `id` and a
  `title` and no `references` of any kind — a documented second shape of the
  same endpoint. There is no key in that: the number is the external
  tracker's, not GitLab's, and flow has no project to qualify it with. The
  adapter drops it, so `ClosingIssues` is empty and the tracker fallback does
  the work, which is the same answer flow gives for a forge that publishes no
  link at all.

- **§4 a squashed merge request has two commits and one of them landed (item
  7.4).** GitLab keeps `merge_commit_sha` and `squash_commit_sha` side by
  side. `flow check deploy` proves a deployment contains what landed, and what
  landed on the trunk of a squashing project is the squash. The adapter
  prefers it and falls back to the merge commit, which is what a project that
  does not squash has.

- **§4 containment is asked straight, and only straight (item 7.4).** GitLab's
  compare takes `straight`, and with it off the answer is against the merge
  base — a different question, and one that says a commit is contained when it
  is merely related. `flow check deploy` rests on the containment proof, so the
  adapter sends `straight=true` and the fake refuses anything else rather than
  answering a question nobody asked.

- **§4 GitLab appends nothing to a squash, so the header is the title (item
  7.4).** GitHub lands `<title> (#123)` and Gitea `<title> (#N)`; GitLab's
  default squash template is the title alone, and the gate lints the header
  the forge will write (spec 09). `core.PullRequest.SquashHeader` is therefore
  the title, and the room GitHub's suffix costs an author is room a GitLab
  author keeps.

- **§4 an error lands as a failure, because GitLab has no other word (item
  7.4).** Flow publishes four verdicts and GitLab's commit statuses know three:
  a check that could not run has failed as far as the forge is concerned. Both
  are writable rather than one being refused — the distinction is flow's and
  the forge's reader only needs to know the tick is not green.

- **§4 GitLab's deployments are a capability flow does not have yet (item
  7.4).** `assets/capabilities/gitlab.yaml` was written from research and
  claimed `deployments_read`, and nothing contradicted it because a capability
  file is a declaration rather than a test. GitLab does have an environments
  and deployments API; what flow does not have is a source that reads it —
  `internal/deploys` implements `forge_deployments` for GitHub alone. A
  repository configuring `deploys.source: forge_deployments` against GitLab
  would therefore have been told the capability was there and then found
  nothing reading it, which is the failure mode capability files exist to
  prevent. The entry now says `verify: false` and points at
  `deploys.source: commit_status`, which item 7.5 made work for any forge by
  reading statuses through `core.Forge`. A GitLab deployment source is worth
  having and is its own item.

- **A pull request says whether the forge would take it, and only an open one
  says it (issue #156).** `core.PullRequest.Mergeability` is one of
  `clean`, `conflicted`, `behind` or the empty string, which is the absence of
  a reading rather than a reading that nothing is wrong: GitHub computes it in
  the background and answers `null` until it has, and silence must not read as
  a refusal or the check would fail hardest on the pull request somebody
  opened a moment ago. Adapters fill it from `Forge.PullRequest` only, and
  drop it for anything merged or closed — every forge goes on answering for a
  pull request it has finished with, with whatever it last computed against a
  base that has moved on, and "this merged pull request conflicts" can only
  mislead whoever acts on it.

- **A listing never carries it, including from the forges whose listing
  does.** GitHub's list endpoint sends neither `mergeable` nor
  `mergeable_state`; GitLab's and Gitea's send theirs. An engine written
  against a listing would therefore answer "nothing is stuck" on GitHub and
  the truth everywhere else — a difference between providers nothing would
  catch, since each adapter's tests only ever meet their own. All three
  adapters drop it from a listing and the forge contract requires that.

- **`behind` is reported only where the protection makes it matter.** GitHub
  answers `mergeable_state: behind` when the base has strict required status
  checks and GitLab answers `detailed_merge_status: need_rebase` when the
  merge method demands a rebase; everywhere else falling behind the trunk is
  not a reason to refuse a merge, and flow does not invent one. Gitea has a
  single `mergeable` bool computed from conflicts alone — it applies
  `block_on_outdated_branch` at merge time and never says so beforehand — so
  a Gitea pull request is conflicted or clean and never behind.

- **Arming auto-merge is one method and three quite different endpoints
  (issue #167).** GitHub has a mutation of its own, `enablePullRequestAutoMerge`,
  and no REST equivalent — REST's merge endpoint merges now, which is not a
  thing flow has any business doing from a gate. Gitea and GitLab have only
  that merge endpoint, told to wait by a flag: `merge_when_checks_succeed`
  and `merge_when_pipeline_succeeds`. Flow always sends the flag explicitly
  rather than relying on a zero value, because a payload that dropped it
  would land the branch.

- **A condition only the provider knows comes back as invalid input.** A
  repository with the setting off, a draft, an approval still owing, a merge
  style the repository does not allow: GitHub answers all of them with a
  GraphQL error of type `UNPROCESSABLE`, and Gitea and GitLab with 405. The
  405 is rewritten, because `method not allowed` otherwise reads as a
  provider that cannot do the thing at all when it is this pull request that
  cannot — and the difference decides whether a caller ever asks again. The
  provider's own sentence is carried through in the `why`.

- **Only GitLab is asked whether arming would merge.** GitLab looks for
  something to wait for and, finding nothing, merges on the spot, so the
  adapter refuses to arm a project that does not require its pipeline to
  pass. GitHub and Gitea queue the request either way.

- **Idempotence is bought differently on each.** GitHub answers the pending
  request in the same round trip as the node id its mutation needs, so flow
  never arms what is armed. GitLab reads the merge request first. Gitea shows
  the schedule on no endpoint at all — which is what `auto_merge.verify:
  false` records — so the second arming is forgiven from its 409 rather than
  avoided.

- **GitLab's rename is read both ways round and written only the old way.**
  17.11 renamed `merge_when_pipeline_succeeds` to `auto_merge` and deprecated
  the first; a deprecated v4 parameter is removed when v4 is, so for as long
  as flow speaks `/api/v4` the old name reaches every installation and the
  new one reaches none the old one does not. Sending both was the first
  reading, and it is a line no test can fail: there is no release that
  refuses the old name, which is the whole reason the old name is the one to
  send. The reading is the other way round, and accepts either, because the
  two directions fail differently — a parameter an installation does not know
  is a refusal flow sees and reports, while a field it has stopped answering
  is a quiet "nothing is armed" that would have flow arm the same merge
  request every push.

- **The merge method is flow's vocabulary, validated everywhere and sent
  where it is wanted.** GitHub takes it as `PullRequestMergeMethod` and Gitea
  as a merge style; GitLab has no per-merge-request method at all and decides
  from the project's own settings, which `forge.merge_settings` already asks
  for. GitLab still refuses a name flow does not have, so a caller's mistake
  is a mistake on every provider rather than on two of them.

- **A pull request's line counts are gone (item 3.1, revisited).** Every
  adapter filled `core.FileSummary.Additions` and `Deletions` from the
  provider's payload, the contract fixture carried them, and in the whole of
  v1 no rule, check or rendering ever read either. A field three adapters pay
  to fill and nothing asks a question of is a promise flow is not keeping, so
  it is dropped rather than left to look load-bearing. What the gate reads of
  a change's shape is `Changed` and `Paths`: `agents.unreviewed_types` refuses
  to excuse a pull request whose files flow cannot enumerate (spec 09 §2), and
  that comparison needs a count and a list, not a diffstat. Should flow ever
  have an opinion about the *size* of a change — the Definition of Ready has
  `max_size` for a ticket and nothing for the change that answers one — it
  arrives as a rule in spec 09 with a configured threshold, and the counts
  come back with the rule that reads them. Nothing is lost meanwhile: the
  numbers are on every pull request page the forge already serves.
- **Trunk protection is written as a ruleset, and protection a credential
  cannot read is a warning, not a fault (#255).** GitHub answers 404 to
  classic branch protection both when there is none and when the caller is
  not an admin, so a daily run on a write-only bot read the admin's verified
  protection as "unprotected" and reopened its tickets. flow now writes one
  ruleset it owns by name (`flow: trunk protection`), which any collaborator
  can read through `/rules/branches/{b}`, and never writes classic
  protection. When classic protection is still in force and unreadable, the
  adapter falls back to `GET /branches/{b}` (protected, required checks) and
  marks the rest `Unreadable`: those fields are taken as desired and the
  outcome is a warning saying which credential could not look — never a
  change, never a reopened ticket.
- **Classic protection is moved into flow's ruleset, unless moving it would
  lose something (#258).** Writing new protection as a ruleset was not
  enough: a repository whose classic protection already said everything
  asked of it was up to date to an admin, so apply wrote nothing, and every
  run on any other credential warned that it could not look. Classic
  protection read by an admin is `AdminOnly`, and the desired protection
  asks for `admin_only: false`, so the plan shows an update. Apply writes the
  ruleset carrying everything the classic protection required and only then
  deletes the classic protection, so the branch is never unprotected; where
  the classic protection did not enforce its rules on admins, the ruleset
  lets the admin role past, so the move makes nothing stricter. Classic
  protection that says anything the ruleset would not carry — who may push,
  signed commits, a lock, code-owner review, approval of the last push, who
  may dismiss or skip review, blocked creation, fork syncing — is listed in
  `Immovable` and left exactly where it is; its place is taken as asked for
  and reported as a warning naming what a person would have to move.
- **Gitea and GitLab tell protection they cannot read from none, too
  (#291).** Both adapters mapped a refused protection read to "no rule", so
  a credential without admin read `block_on_outdated_branch`, force-push and
  the required checks as off. Gitea refuses `branch_protections` with 403 to
  anybody but a repository admin, and answers 404 only for a rule that is not
  under the name asked — a glob rule on a Gitea that does not name the rule
  governing a branch; both follow a branch that has said it is protected, so
  both are `Unreadable`, keeping what `GET /branches/{b}` showed. A write on
  that reading is refused before it is tried, because Gitea replaces a rule
  whole. GitLab answers 404 to `protected_branches/{name}` for a branch
  nobody protected and for one a wildcard rule covers, and 403 or 404 to a
  credential that may not look; on any of them the adapter asks
  `repository/branches/{b}`, whose `protected` anybody who can read the code
  may see. Approval rules and external status checks are refused alike by a
  tier without them and to a credential below maintainer, so the probe's
  access level decides: to a maintainer a refusal is the tier (none), to
  anybody else it is `Unreadable`. Checks GitLab could not read are
  `ChecksUnreadable`, which the plan takes as required with a warning, as it
  does the protection fields — GitHub and Gitea show the checks on the branch
  and never set it. A GitLab reading with any part unread is not written
  from: every GitLab write is judged against what is already there.
- **Gitea protection is written to the rule that governs the branch, and its
  required contexts are read as patterns (#293).** A Gitea rule may be a
  glob (`main*`), and the branch names the rule that governs it as
  `effective_branch_protection_name`. flow used to edit
  `branch_protections/{branch}`, which under a glob rule is 404, and then
  created a rule named after the branch — which wins over the glob and
  carries none of its push or merge whitelists. Writes now edit the
  governing rule by its (escaped) name, and create one only when no rule
  governs the branch. From 1.21 Gitea also matches each required context as
  a glob, so `Protection.CheckPatterns` says so and `RequiresCheck` matches
  with Gitea's own `modules/glob` semantics without separators (`*` crosses
  `/`, `?`, `[...]`/`[!...]`, `{a,b}`, `\` escapes) rather than
  `path.Match`, whose `*` stops at a slash. A trunk requiring `flow/*`
  therefore already requires `flow/policy`; a check no pattern covers is
  appended by its literal name, and the team's patterns are never rewritten.
