# Tracker and Forge API Research (verified 2026-10-09)

Facts backing `assets/capabilities/*.yaml`. Re-verify before each adapter
release; admin APIs and rate limits change often. Paraphrased; follow the
links for the authoritative text.

## Trackers

### Jira Cloud
- Workflows and statuses can be created and updated via the bulk workflow
  endpoints (`/rest/api/3/workflows/create`, `/workflows/update`, plus
  validation endpoints). Project-scoped creation needs Administer projects;
  global scope needs Administer Jira.
  https://www.withone.ai/knowledge/jira/conn_mod_def%3A%3AGJ4qukkiew4%3A%3AKqobRX5BTNqwgZS_9Cu7HA
- The legacy single "Create workflow" endpoint was scheduled for removal on
  2026-02-01 in favour of bulk create.
  https://docs.rs/crate/jira_v3_openapi/latest/source/docs/WorkflowsApi.md
- Bulk create has had a reported bug ("Status name is not unique") when
  referencing existing statuses: fetch and reuse statuses by id; validate first.
  https://tnckb94959.lithium.com/forums/Jira-questions/Jira-Bulk-Create-workflows-potential-bug/qaq-p/2768040
- Board configuration is READ-ONLY in the public Cloud API: no endpoint to set
  columns, swimlanes or board admin; long-standing feature request now "under
  consideration" with no commitment. Undocumented greenhopper endpoints are
  browser-only and unsupported — do not use.
  https://jira.atlassian.com/browse/JRACLOUD-74565 ·
  https://jira.atlassian.com/browse/JSWCLOUD-20535 ·
  https://community.developer.atlassian.com/t/unable-to-update-jira-board-configuration-using-ap-request-or-addon-httpclient-but-it-works-from-postman-app/33917
- Rate limits: points-based hourly quotas for Forge/Connect/OAuth apps enforced
  from 2026-03-02 (Tier 1 global pool reported as 65,000 points/hour). API-token
  traffic remains under burst limits only, but Atlassian says this asymmetry is
  temporary. New `RateLimit`/`Beta-RateLimit` headers.
  https://developer.atlassian.com/cloud/jira/platform/rate-limiting ·
  https://www.atlassian.com/blog/platform/evolving-api-rate-limits ·
  https://supergood.ai/api-report-card/atlassian-jira
- Search migrated to `nextPageToken` pagination (`/search/jql`).

### Linear
- `workflowStateCreate` adds a state to a team's workflow (states = board columns).
  https://www.withone.ai/knowledge/linear/conn_mod_def%3A%3AGJ4vQy_-I8E%3A%3AnL6K7NngQ5qrAmM5QCkBPQ/md
- Native GitHub integration: per-team PR/commit automation mapping PR events to
  statuses (configured under team issue statuses & automations); multiple PRs
  per issue wait for all to merge.
  https://linear.app/docs/github · https://linear.app/changelog/2019-07-11-github-workflow-configuration
- Rate limits (current docs): 5,000 requests/hour and 3,000,000 complexity
  points/hour per API key; headers `X-RateLimit-Requests-*`,
  `X-RateLimit-Complexity-*`, `X-Complexity`. (Older docs show lower numbers.)
  https://linear.app/developers/rate-limiting
- No native WIP limits found (absence not proven).
- Verified 2026-10-14 against the published SDL
  (raw.githubusercontent.com/linear/linear/master/packages/sdk/src/schema.graphql)
  while writing the adapter (item 7.2):
  - One endpoint, `POST https://api.linear.app/graphql`. A personal API key is
    the whole `Authorization` header with no scheme; an OAuth access token is
    sent as `Bearer`. https://linear.app/developers/graphql
  - `issue(id:)` and `issueUpdate(id:)` both take the human identifier
    (`ENG-123`) as well as the uuid.
  - `Issue` carries identifier, number, title, description, url, branchName,
    trashed, state{id,name,type,position}, team, parent, assignee, labels,
    attachments, archivedAt, completedAt, canceledAt, createdAt, updatedAt.
    There is no open/closed flag: completed and canceled state types are what
    closing means, and Linear dates both itself.
  - `WorkflowState.type` is one of triage, backlog, unstarted, started,
    completed, canceled. `WorkflowStateCreateInput` takes a type;
    `WorkflowStateUpdateInput` does **not**, so a state's category cannot be
    changed after it is created. A state is removed with
    `workflowStateArchive`, which refuses while issues still stand in it.
  - `IssueLabel` has a nullable `team` (null = workspace-wide), `isGroup` and
    `retiredAt`; colours are `#rrggbb`. Root `issueLabels` lists both kinds.
  - Errors arrive HTTP 200 with `extensions: {type, code, userError,
    userPresentableMessage}` — the name of the failure is *not* beside the
    message as GitHub's is. Codes seen: ENTITY_NOT_FOUND, AUTHENTICATION_ERROR,
    FEATURE_NOT_ACCESSIBLE, INVALID_INPUT, RATELIMITED.
  - Paging is Relay: `first`/`after` with `pageInfo{hasNextPage endCursor}`.
    Every connection excludes archived entities unless `includeArchived: true`.
    https://linear.app/developers/pagination
  - `attachmentsForURL(url:)` answers which issues an address is attached to,
    and `IssueFilter.attachments` filters a listing by one — but
    `core.Tracker.LinkedKeys` is given no context, so flow reads each issue's
    attachments as it reads the issue.
  - `Team.membership{owner}` and `User.admin` are the two facts that say
    whether a credential may edit a team's workflow states; Linear publishes
    no per-permission endpoint.

### GitHub Projects (v2)
- Status is a single-select field; `updateProjectV2Field` reportedly REPLACES the
  field definition when adding options, which can clear existing item values.
  https://github.com/orgs/community/discussions/198803
- Column limits exist per board view, are advisory (do not block adds or
  automations) and are set in the UI.
  https://docs.github.com/en/issues/planning-and-tracking-with-projects/customizing-views-in-your-project/customizing-the-board-layout
- A REST API for Projects was added in 2025 alongside GraphQL.

### Trello
- Lists (columns) fully manageable via REST (create/update/archive/move).
- 300 requests/10s per API key, 100/10s per token; `/1/members/` 100 per 900s;
  `x-rate-limit-*` headers.
  https://rollout.com/integration-guides/trello/api-essentials
- No native WIP limits (Power-Ups exist).

### ClickUp
- Statuses cannot be created/updated via the public API (open feature requests;
  spaces created via API get only default statuses).
  https://feedback.clickup.com/public-api/p/allow-statuses-to-be-updated-at-the-space-level-via-api ·
  https://feedback.clickup.com/public-api/p/create-manage-task-statuses-on-lists-folders-space-via-api
- Per-token limits: 100/min (Free, Unlimited, Business), 1,000/min (Business
  Plus), 10,000/min (Enterprise).
  https://rollout.com/integration-guides/clickup/api-essentials

## Forges

### GitHub
- REST primary limit 5,000/h (PAT); `GITHUB_TOKEN` in Actions 1,000/h per repo;
  GitHub Apps owned by Enterprise Cloud orgs 15,000/h.
  https://fast.io/resources/github-api-rate-limiting-strategies/
- Conditional requests (ETag / Last-Modified) returning 304 don't count against
  the primary limit; REST only (no GraphQL conditional requests).
  https://jamiemagee.co.uk/blog/making-the-most-of-github-rate-limits/
- ETags are token-scoped; GitHub App installation tokens refresh at least
  hourly, so cached ETags stop matching after rotation.
  https://github.com/github/docs/issues/34689
- Commit statuses work with ordinary tokens; Checks API requires a GitHub App.

### GitLab
- External status checks (proper MR gate for external services) are
  Ultimate-only; MRs send a webhook to the external service, but check results
  can also be set via API. Not blocking by default; must be enabled.
  https://docs.gitlab.com/user/project/merge_requests/status_checks/
- External commit statuses are available on all tiers; GitLab attaches them to
  the newest pipeline for the SHA or creates one; duplicate pipelines for the
  same commit can make statuses land in an unexpected pipeline or vanish from
  the MR view.
  https://docs.gitlab.com/ci/ci_cd_for_external_repos/external_commit_statuses
- GitLab.com authenticated API: 2,000 requests/min per user; docs also show
  plan-based allowances that may be lower — read headers.
  https://docs.gitlab.com/user/gitlab_com/rate_limits/

### Gitea / Forgejo
- Branch protection exposes `enable_status_check` and `status_check_contexts`
  via API; merges (UI + API) are refused until required contexts succeed.
  https://docs.rs/gitea-client/latest/src/gitea_client/models/branch_protection.rs.html ·
  https://git.fb1.uni-bremen.de/davrot/forgejo-aneksajo/commit/04ca7f004710de2b408f558f6f148894aa61ba57
- Verified 2026-10-14 against an instance's own schema (`GET
  /swagger.v1.json` from codeberg.org, Gitea 1.22). `BranchProtection`,
  `CreateBranchProtectionOption` and `EditBranchProtectionOption` name
  `enable_status_check`, `status_check_contexts`, `required_approvals`,
  `dismiss_stale_approvals`, `block_on_outdated_branch` and
  `apply_to_admins`. **There is no force-push setting of any kind**, and the
  create takes `rule_name`/`branch_name` where the edit takes neither. The
  earlier reading above had guessed `block_admin_merge_override` and
  `enable_force_push`; neither exists, and Gitea ignores a key it does not
  know, so a plan would have claimed protection nobody had. Read the
  instance's schema, not a client library generated from an older one.
- `Branch` carries `effective_branch_protection_name`, which is how a branch
  says which rule governs it — a glob rule is kept under its own name and not
  the branch's.
- `Repository` carries `external_tracker`, `archived`, and the four merge
  strategies `allow_squash_merge`, `allow_merge_commits`, `allow_rebase`,
  `default_delete_branch_after_merge`. There is no squash title or message
  setting: Gitea composes the squash commit itself.

## Go ecosystem (verified for the build)
- Charm v2 libraries use vanity paths `charm.land/bubbletea/v2`,
  `charm.land/lipgloss/v2`, `charm.land/bubbles/v2`; v2 uses declarative
  `tea.View` and `tea.KeyPressMsg`.
  https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md
- `gopkg.in/yaml.v3` was archived 2025-04-01; the YAML organization maintains
  `go.yaml.in/yaml/v3` (same API).
  https://github.com/go-yaml/yaml · https://pkg.go.dev/go.yaml.in/yaml/v3

## Delivery research (context for policy defaults)
- 2025 DORA report: AI adoption now correlates positively with throughput but
  still negatively with delivery stability; small batches improve both AI output
  quality and stability; code review becomes the dominant bottleneck.
  https://cloud.google.com/blog/products/ai-machine-learning/announcing-the-2025-dora-report ·
  https://newsletter.getdx.com/p/doras-2025-research-on-the-impact
  → hence default `wip.review` rule, `max_size: 2d` DoR, `agent.human_review`.

## Added for v2 (verified 2026-10-09)

### GitHub Projects built-in workflows
- Built-in workflows only set the Status field on events. Documented set:
  item added, item reopened, item closed, code changes requested, code review
  approved, pull request merged; plus auto-add (by filter) and auto-archive.
  There is no built-in for "PR opened" or "PR ready for review".
  https://dev.classmethod.jp/articles/lets-touch-the-built-in-workflows-function-of-github-projects-beta/
- New projects enable two workflows by default: closed issues/PRs → Done, and
  merged PRs → Done. Configured in the UI (Project → Workflows); not managed via
  the public API in v1 → flow attests + verifies behaviour live.
  https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-built-in-automations
- Teams running their own status automation report having to disable the
  built-ins to avoid conflicts (e.g. "Item closed" re-applying statuses).
  https://github.com/SeattleColleges/SeattleColleges-AD-Practicum-Wiki/wiki/Project-Status-Sync

### Credentials for user-owned Projects (verify at build time)
- The Actions `GITHUB_TOKEN` is repository-scoped and cannot access a
  user-owned Project; use a PAT with `project` scope stored as a secret.
  Fine-grained token support for user-owned Projects must be checked at build
  time (record a Decision in spec 11).

## Archiving (verified 2026-10-13, after issue #99)

Every provider has a way of putting work out of sight without closing or
deleting it, and the one thing they share is that two readings of the same
thing can disagree. Flow needs the flag (`core.Ticket.Archived`,
`core.Repository.Archived`) because an archived thing takes no writes.

### GitHub Projects — archived items
- `ProjectV2.items` **excludes** archived items; `Issue.projectItems`
  **includes** them, with the column they were archived in. `isArchived` is on
  `ProjectV2Item` and both of flow's queries now select it.
- `updateProjectV2ItemFieldValue` on an archived item answers
  `{"updateProjectV2ItemFieldValue": null}` with **no `errors`** and changes
  nothing: the returned item id is the only evidence the write landed.
- Auto-archive is one of the built-in workflows (spec 17 §3 asks for it on the
  dogfood board), so this is routine, not exotic.

### Jira Cloud — archived issues
- Cloud Premium only (Data Center 8.1+). Archived issues are read-only and
  drop out of JQL search, project lists, dashboards and reports.
  https://confluence.atlassian.com/adminjiraserver/archiving-an-issue-968669980.html
- `GET /rest/api/3/issue/{key}` still returns the full issue, and the payload
  carries **no archived flag of any kind**; there is an open Atlassian
  suggestion for a REST API that lists archived issues.
  https://jira.atlassian.com/browse/JRACLOUD-94071
- So the adapter derives it: present by key, absent from a *full* JQL listing.
  A narrowed listing proves nothing — absence then means the filter.
- `PUT /rest/api/2/issue/{key}/archive` and `POST /rest/api/2/issue/archive`
  are the Data Center archiving endpoints.
  https://developer.atlassian.com/server/jira/platform/jira-archiver/

### Linear — archived issues
- Every connection takes `includeArchived: Boolean` ("Should archived
  resources be included", default **false**), and `archivedAt` is on the
  entity (null when live). `trashed` is a separate state.
- Linear **auto-archives** issues, cycles and projects a while after they
  close, so a Linear board will archive flow's own tickets as a matter of
  course.
- `issueArchive(id, trash)` archives; the entity comes back with `archivedAt`
  set.

### Archived repositories (the forge side, issue #102)
- GitHub: `archived: true` in the repository payload; the repository is
  read-only. Flow already reads it into `core.Repository.Archived`.
- GitLab: `archived` on the project; "repository, packages, issues, merge
  requests and all other functions are read-only", and writes answer 403.
- Gitea/Forgejo: the same feature by design — "you cannot push, create issues,
  comment or do anything else which may change it". Confirm the field the
  `/repos/{owner}/{repo}` payload carries when the adapter is written.
