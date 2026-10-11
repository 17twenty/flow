# 11 — Tracker Adapters

## 1. Common

Markers: HTML-comment style where preserved (GitHub), visible muted last line
`flow:<kind> k=v` elsewhere (Jira ADF, Linear). Parsers accept both.
`core.Ticket{Key, ID, URL, Title, Body (markdown), Status{ID,Name}, ColumnID,
Labels, Assignees, Parent, Blocked, Closed, UpdatedAt}`.

## 2. GitHub Projects v2 (v1 — dogfood target)

Auth: token with `project` scope (classic PAT) — `GITHUB_TOKEN` in Actions cannot
access user-owned Projects; use a repository secret (e.g. `FLOW_PROJECT_TOKEN`).
Fine-grained tokens: use only if they support user-owned Projects at build time
(record Decision); doctor probes access either way.

| Need | GraphQL |
|---|---|
| Project | `user(login){projectV2(number){id title}}` or `organization(login){…}` per `owner_type` |
| Status field + options | `projectV2{field(name:"Status"){… on ProjectV2SingleSelectField{id options{id name}}}}` |
| Items (with issue content) | `projectV2{items(first:100, after){nodes{id updatedAt fieldValueByName(name:"Status"){… on ProjectV2ItemFieldSingleSelectValue{optionId name}} content{… on Issue{number title body url state closedAt labels(first:20){nodes{name}} assignees(first:5){nodes{login}} parent{number} repository{nameWithOwner}}}}}}` |
| Item for an issue | `repository{issue(number){projectItems(first:10){nodes{id project{id}}}}}` |
| Move | `updateProjectV2ItemFieldValue(input:{projectId,itemId,fieldId,value:{singleSelectOptionId}})` |
| Options edit | `updateProjectV2Field` — HAZARD: replaces options and can clear item values → snapshot/restore by name; >500 items → follow_up |
| Follow-ups / comments | forge REST issues API in `forge.repo` + `addProjectV2ItemById` |
| Labels | REST `/repos/{o}/{r}/labels` |

Ticket key = `#<number>` (repo-qualified when cross-repo). Native transitions:
the built-in project workflows (item added, item reopened, item closed, code
changes requested, code review approved, pull request merged) are configured in
the UI; new projects enable "item closed → Done" and "pull request merged → Done"
by default. They are not manageable via API in v1 → `tracker.native.<event>`
outcomes are `attest` (plus live verification). There is no built-in workflow for
"PR opened" → those transitions are `owner: flow`.
WIP column limits: per view, advisory, UI-only → `compensate` (policy.wip).
Expedite: a saved view filtered by label → `attest`.
Rate: shares GitHub GraphQL budget (5,000 points/h); keep queries small, page 100.

## 3. Jira Cloud (Phase 7)

As researched: statuses/workflows via bulk workflow API (project admin);
board columns/swimlanes/WIP not writable (follow_up / attest / compensate,
verify via `GET /rest/agile/1.0/board/{id}/configuration`); search `POST
/rest/api/3/search/jql`; transitions via `/issue/{key}/transitions`; ADF for
bodies. Native transitions via Jira Automation + GitHub integration: not
listable via API → attest + live verification. Linkage fallback: Jira key in
branch name or PR title (Jira's own convention).

## 4. Linear (Phase 7)

GraphQL; `workflowStateCreate` for columns; native PR automation per team
(UI) → attest unless an API exists at build time; linkage: Linear attachment for
the PR URL first, identifier in branch/title fallback. 5,000 req/h, 3M complexity/h.

## 5. Contract

`coretest.TrackerContract`: probe mapping, discover/ParseRef, observe
canonicalisation stability, move idempotency, follow-up marker upsert/find,
reopen, pagination, error mapping.
