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
- **§2 a ticket is an issue, and its column is where this board holds it (item
  3.9).** An issue nobody has put on the board is still a ticket: it has a key,
  a title and acceptance criteria, which is everything spec 04 §7 reads. It
  simply sits in no column, and a tracker that refused to describe it at all
  would make the Definition of Ready unable to say why the work is not ready.
  An issue may be on several boards at once; only the item whose project is the
  number flow was pointed at is this board's, and taking the first item handed
  back would move somebody else's board.
- **§2 a key is `#7` here and `owner/repo#7` elsewhere, and nothing else is a
  key (item 3.9).** It is the form this section names and the form the forge
  adapter already writes a closing reference in, so linkage needs no
  translation. A bare number is refused: a tracker that accepted one would take
  a Jira key, a pull request number or a commit subject's "42" and resolve it
  against its own board, and the mistake would be silent. Without a
  `forge.repo`, `#7` names no repository at all, and flow says so rather than
  guessing.
- **§2 a ticket's id is the issue's, not the board item's (item 3.9).** The
  item id identifies where an issue is pinned rather than what it is: it
  changes when somebody removes the issue from the board and adds it again, and
  an issue on no board has none. `core.Ticket.ID` is the tracker's identifier
  for the ticket, so it is the issue's node id, and the item id is resolved by
  whichever mutation needs one.
- **§2 a ticket changed when either half of it did (item 3.9).** Editing the
  issue does not touch the project item, and moving the item does not touch the
  issue. `UpdatedAt` is therefore the later of the two: a window built on
  either alone would miss work — a ticket moved into review but not edited, or
  one rewritten in place without moving.
- **§2 a draft item is not a ticket (item 3.9).** A board holds draft notes and
  pull requests as well as issues. They have no key to refer to them by and
  nothing a check can read, and GitHub reports them as content matching no
  inline fragment, so they are skipped rather than turned into keyless tickets
  the engine would then have to special-case everywhere.
- **§2 `Blocked` is never set by this adapter (item 3.9).** GitHub has no
  blocked flag. Blocking is a label, and which label is `workflow.blocked.label`
  — configuration, which an adapter may not see (spec 01 §2). The engine reads
  the label against the ticket's own labels, so an adapter that guessed at
  "blocked" would be a second, silent definition of the same thing.
- **§2 a move reads before it writes (item 3.9).** A ticket already in the
  column is left alone and no mutation is sent. Spec 06 §4 counts a write as a
  change that happened, so a move that sent the mutation regardless would have
  every daily doctor run report work it did not do — and would move the item's
  `updatedAt`, which is half of when the ticket last changed.
- **§2 a move refuses a column the board has not got, by name (item 3.9).** The
  option id is an opaque `PVTSSFO_…`; GitHub's refusal quotes it back and
  nobody can act on that. Flow checks the id against the board's own options
  first and lists the column names instead.
- **§2 flow will not add an issue to a board on a transition's behalf (item
  3.9).** `Move` of an issue that is not an item of the board is `ErrNotFound`.
  Adding it is a different act with a different consequence — the board's
  built-in workflows run on what is added — and a transition that quietly
  enrolled issues would reorganise a board nobody asked it to.
- **§2 tickets by key are read one at a time, and a key nobody holds is left
  out (item 3.9).** Two keys need not share a repository, so there is no one
  query for an arbitrary set of them. A caller naming several tickets is asking
  which of them are there, and one that has been deleted is an answer rather
  than a failure of the whole query.
- **§2 a rewrite sends every option back with its own id (item 3.10).**
  GitHub has no mutation that edits one option of a single-select field: the
  set is written whole, and an option the server does not recognise is a new
  option with a new id, which every item pointing at the old one loses.
  `ProjectV2SingleSelectFieldOptionInput` takes an optional `id` precisely to
  preserve an option's identity across such a write, so flow sends the id,
  name, colour and description of every column it is not changing exactly as
  it read them. This also preserves the board's own built-in automations,
  which name their columns by id and would otherwise be silently unhooked.
  There is no fallback for a server whose schema lacks the field: that is a
  GraphQL validation error, which happens before anything is written, and
  flow reports it rather than retrying a write that would clear the board.
- **§2 the snapshot and restore happen anyway (item 3.10).** Spec 06 §4
  asks for them and they are right whichever way the provider behaves: flow
  reads the column of every item, rewrites the set, puts back whatever was
  cleared, and reads the board again to prove it. Because the restore reads
  before it writes, a provider that honoured the ids costs zero item
  mutations and the note says every column survived; the verification pass
  runs only when something had to be put back.
- **§2 the snapshot covers every item on the board, not just tickets (item
  3.10).** Draft issues and pull requests sit in columns too, and a rewrite
  clears theirs identically. Restoring only the issues would quietly empty
  the rest of the board.
- **§2 items are put back by column name, through the rename (item 3.10).**
  An id is what the rewrite may have changed, so it cannot be what the
  restore matches on; the name is what a human means by the column. A rename
  carries its items with it — an item that was in `Ready` belongs in whatever
  `Ready` was renamed to, not in nothing.
- **§2 colour and description are read but are not observed (item 3.10).**
  The rewrite has to send them back, so the project query now asks for them.
  They stay out of `Observe` deliberately: a column flow created in grey that
  somebody recoloured is the same column, and an observation that included
  the colour would report that as drift and recolour it on every apply.
- **§2 a new column is grey with no description (item 3.10).** GitHub's
  input makes the colour mandatory and a flow configuration has no opinion
  about it. Whoever wants another colour picks it in the UI, and flow sends
  it back unchanged ever after.
- **§2 deleting a column that holds items is refused before any write (item
  3.10).** The refusal is `ErrConflict` and names how many items are in it.
  Unlike a cleared value, there is nothing flow could do afterwards to put
  those items back — the column they belong in would no longer exist — so
  this is checked against the snapshot, before the mutation is sent.
- **§2 option edits follow the tracker contract's own semantics (item
  3.10).** Creating a column that is already there is `ErrConflict`; updating
  or deleting one that is gone is `ErrNotFound`; an operation flow does not
  know is `ErrValidation`. An update asking for the name the column already
  has rewrites nothing and sends no mutation, so applying the same change
  twice touches the board once.
- **§2 issues and comments are GraphQL; labels stay REST (item 3.11).** The
  table says "forge REST issues API" for follow-ups and comments. flow uses
  GraphQL for both instead: `createIssue` answers with the node id
  `addProjectV2ItemById` needs, so filing and putting the ticket on the board
  are one transport, one error vocabulary and one round trip fewer. Labels are
  the exception and stay REST, because GraphQL has no complete set of label
  mutations — there is no rename — and the REST endpoints are a whole set.
- **§2 labels need `forge.repo` and are refused without it (item 3.11).** A
  project can hold issues from many repositories, so there is no one
  repository to infer labels from. Observing or applying `tracker.labels`
  without a repository is `ErrValidation` naming the setting, not a request
  against an empty path.
- **§2 a repository with no labels still exists (item 3.11).** The labels
  observation is always `Exists: true` when the repository can be read.
  `Exists: false` means flow could not see the thing at all, and a repository
  somebody has emptied of labels is not that — it is a repository with
  everything left to create.
- **§2 a label change sends only what it names (item 3.11).** flow's desired
  labels carry a name and nothing else, so an update must leave the colour and
  description alone; sending flow's default grey on a rename would repaint a
  label the team chose the colour of. A create has to send a colour because
  GitHub's API requires one, and that is where the default belongs.
- **§2 a duplicate label is found by reading, not by the 422 (item 3.11).**
  GitHub answers both "already exists" and "that is not a label name" with
  422, and the per-error `code` that tells them apart is in a body the client
  does not hand back with the error. So `create` lists the labels first and
  answers a name already there — case-insensitively, as GitHub matches — with
  the `ErrConflict` the apply loop relies on.
- **§2 a label is found by node id when the change carries one (item 3.11).**
  The name may be the very thing changing, so update and delete resolve the
  label through the id the plan copied off the observation, falling back to
  the name. A label that has gone since the plan was made is `ErrNotFound`
  with "run flow plan again" as the fix.
