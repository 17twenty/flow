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
