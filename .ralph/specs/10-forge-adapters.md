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
