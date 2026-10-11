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
- **§1 `core.Tracker` gains `Close` (item 2.10).** Spec 07 §4's lifecycle ends
  "verified: comment + close", and the interface had only the inverse,
  `Reopen`. Moving the ticket to the Done column is not the same thing — a
  follow-up about repository protection need not be on the board at all, and a
  tracker's "closed" is a property of the issue rather than of a view. `Close`
  takes a close reason, because spec 07 §6 already distinguishes `completed`
  from `not_planned` and the two ends of the lifecycle mean different things.
  It is idempotent: doctor runs daily, and the second run must not move the
  date the work was finished. Added now rather than later because the fake is
  still the only implementation.
- **§2 a project is its number, everywhere flow records it (item 3.8).**
  GitHub addresses a board three ways: an opaque node id (`PVT_…`) that every
  mutation needs, a number in the address bar, and a title. The number is what
  `tracker.project` holds, what a human can check against the address bar, and
  what `ParseRef` returns — so it is the id of the observed item and of a
  discovery candidate too. The node id is a detail of whichever query needs
  one, resolved from the number; recording it would put a string nobody can
  recognise in the lock, and a board restored from a backup has a new one.
- **§2 a candidate's address is built, not quoted (item 3.8).** GitHub sends a
  project's `url` with the rest of it, and it would have been free to pass on.
  But spec 07 §3 offers candidates to a human to paste back, and the contract
  holds an adapter to parsing what it itself offered: an address flow composed
  is one `ParseRef` accepts by construction, where a provider that changed its
  address format — or an Enterprise Server behind a proxy — would otherwise
  make discovery offer references the same adapter then refuses.
- **§2 the board and its columns are one query (item 3.8).** The columns are
  the options of the board's own `Status` field, so `projectV2(number)` carries
  them in the same document. Observing `tracker.project` and
  `tracker.status_options` separately would be two queries about one board
  against the 5,000-point budget this section asks to keep queries small for.
- **§2 a column is its name and its id, and nothing else (item 3.8).** GitHub
  reports an option's colour and description as well. Spec 06 §1 writes the
  desired option as a name alone, so neither can ever be a difference the plan
  resolves — but both would go into the hash of the observation, and spec 07 §7
  would then report recolouring a column as drift. An observation carries what
  the desired state is written in.
- **§2 a closed board is never offered (item 3.8).** Discovery takes the only
  candidate automatically when there is one (spec 07 §3). A board the team has
  finished with is the one answer that must never be chosen on their behalf,
  and a team whose only remaining board is closed is better told that nothing
  was found.
- **§2 a hint names a board, it does not search for one (item 3.8).** The hint
  is matched against the title exactly (case-insensitively) or against the
  number. GitHub's own project search is fuzzy, and a setup step that silently
  takes a near miss writes the wrong board into the lock — where the next
  thing flow does is reorganise its columns.
- **§2 `owner_type` chooses the query field, and the wrong one finds nothing
  (item 3.8).** A person's projects are under `user(login:)` and an
  organisation's under `organization(login:)`; the same login may be neither.
  GitHub answers the wrong one with a null owner rather than an error, and
  flow reads that as no projects — an honest answer, since the owner asked
  about genuinely has none. The addresses differ the same way (`/users/…`
  against `/orgs/…`), which is why both kinds sit the contract.
- **§2 a board that is not there is an observation that does not exist (item
  3.8).** GraphQL answers a missing `projectV2` with a null rather than an
  error, as it does a board whose `Status` field somebody deleted. Both are
  the plan's "this has to be created or found first" case and neither is a
  failure, so `Observe` reports `exists: false` and `Probe` counts a board it
  cannot read as a finding rather than refusing.
- **§2 reading the board is proved by reading it; writing to it needs the
  scope as well (item 3.8).** A role and a scope are two limits and both
  apply, as in spec 05 §3. The trial read is the only honest answer to "may
  this credential see this board" — a fine-grained token or an App announces
  no scopes at all, and a classic token's `project` scope says what the token
  may be used for rather than which boards the account can reach — so
  `project_read` comes from the read having worked, and `read:project` plays
  no part in the probe at all. `project_write` needs the read *and* the
  `project` scope where the credential announced any.
- **§2 a per-view setting is observed as absent without asking (item 3.8).**
  Column limits and the expedite view are view settings the API does not
  report, which is what the declaration says by refusing to verify them.
  `Observe` returns an observation that does not exist and sends no query:
  spending a point to be told nothing is worse than knowing in advance, and
  spec 05 §4 turns the absence into the compensation or the attestation.
