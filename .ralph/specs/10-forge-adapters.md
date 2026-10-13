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
