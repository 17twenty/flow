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

## Decisions

- **§1 markers live in `core`, and the caller picks the rendering (item
  2.15).** `Marker.String()` is the visible form, `Marker.HTML()` the comment
  form, and `MarkBody(body, marker, html)` takes which one to use. Both a
  forge comment and a tracker ticket carry markers, so the type cannot sit in
  either adapter package; and whether HTML comments survive is a fact each
  adapter knows about its provider, not something the marker can infer from
  the text it is given.
- **§1 a marker is a kind and a set of fields, and round-tripping is the
  contract (item 2.15).** `flow:<kind> k=v`, fields rendered in sorted order
  so the same marker always renders identically, values Go-quoted whenever
  they would not read back as one bare field — whitespace, control characters,
  a quote, a backslash, `=`, the empty string, or bytes that are not valid
  UTF-8. `FuzzMarkerParse` holds the property that anything flow parses it can
  write and parse again unchanged. A marker that did not survive the trip
  would make flow file a second follow-up instead of finding its first.
- **§1 the parser accepts kinds it does not know (item 2.15).**
  `MarkerFollowUp`, `MarkerPolicy`, `MarkerHealth` and `MarkerFlag` name what
  flow writes today; `ParseMarker` takes any non-empty kind. A ticket written
  by a newer flow must still be recognised as flow's own work by an older one,
  which errs towards updating a stranger's comment rather than towards
  duplicating one of flow's.
- **§1 `Matches` is one-way, `Equal` is equality (item 2.15).** A search
  marker matches when the kinds agree and every field it names agrees;
  `flow:followup check=tracker.columns` therefore finds the marker that also
  carries `repo=`, and a search naming no fields finds every follow-up. That
  is what lets a check look for its own ticket without knowing how many
  fields the version that filed it wrote.
- **§1 marking a body replaces every marker in it (item 2.15).** `MarkBody`
  strips markers wherever they are before appending the new one as the last
  line, so editing a comment ten times leaves one marker, and switching a
  provider from the HTML form to the visible form does not leave both. Flow
  reads bodies it did not write last.
- **§1 `ColumnID` is the tracker's column; `Status` is the ticket's status
  (item 2.3).** The shape lists both, and they are not the same thing: on
  GitHub Projects and Linear a status *is* a column, but a Jira board column
  gathers several statuses, so a ticket in "In Review" and one in "Awaiting
  QA" can share a column. `Move` takes the tracker's `ColumnID`, and the lock
  is what maps flow's configured column ids onto it — the adapter never sees
  flow's vocabulary.
- **§1 a ticket also carries its size and why it was closed (item 2.3).** The
  `max_size` rule in spec 04 §7 needs an estimate, and `live.tickets.
  done_merged` in spec 07 §6 exempts work closed as not planned. Both are
  facts about the ticket, so they belong on it: `Size` is zero when the
  tracker has no such field, and `CloseReason` is empty when it does not say.
