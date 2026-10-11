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
