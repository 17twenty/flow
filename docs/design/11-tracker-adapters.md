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
- **§GitHub Projects: a built-in workflow is not observable at setup (item
  4.6).** The Projects API will not say whether "Pull request merged" or "Item
  closed" is enabled, so the declaration now reads `verify: false` for all
  three native transitions, and `Observe` answers for them with an empty
  observation rather than "a tracker has nothing called that". They were
  marked `verify: true` on the strength of being checkable live, but §4 of
  spec 05 makes `verify` a question about the setup observation, and the live
  audit (spec 07 §6) is a different question asked of a different thing.
- **§GitHub Projects: `owner: native` means there is an automation, not that
  flow can read it (item 4.6).** Configuration semantics used to refuse
  `owner: native` for any event the tracker could neither set nor verify.
  That now also requires the declaration to offer no remedy: a capability that
  asks for an attestation is one with a real built-in workflow behind it that
  flow cannot see, which is precisely what `flow attest` exists for.

- **§1 `UpdateFollowUp` is the other half of `FindFollowUp` (item 5.7).** The
  interface could open a ticket and comment on one, but not rewrite one — so
  the pinned health report of spec 07 §10 had no way to be "updated in place"
  except by a new comment every morning, which is the notification stream the
  pinned issue exists to replace. `UpdateFollowUp(ctx, key, marker, body)`
  rewrites a body and keeps the marker.
- **§1 a rewrite is refused unless the marker is really there (item 5.7).**
  The adapter reads the ticket and checks the marker against what the body
  actually says, not against what flow filed earlier: an issue number can be
  reused in a fork and a person can edit a body. The one thing worse than
  failing to publish a report is publishing it over somebody's bug report, so
  a body without the marker is `ErrValidation` and is left alone.
- **§1 rewriting a body to what it already says is no work (item 5.7).** Every
  forge moves "last updated" on an edit. A daily report that sent identical
  bytes would make a quiet fortnight look like news every morning, so the
  adapter compares the marked body it is about to send against the one that is
  there and sends nothing when they match. The contract suite asserts the date
  did not move, which is the only way the difference is visible.
- **§1 `UpdateFollowUp` rewrites the title as well as the body (item 3.11,
  revisited).** A ticket's title is not something its body decides: a summary
  can improve while every word of the body stays, and the title is the whole
  of what a person reads in an issue list. The adapter sends both, refuses
  both when the marker is absent, and still sends nothing when both already
  say this — so the "no work" rule now compares the title too, and the
  contract suite asserts that a change to the title alone takes effect.
- **§2 `Move` takes the column's display name as readily as the tracker's own
  id (Stage B, found live).** `core.ColumnID` is documented as the tracker's
  identifier — a Projects status option id, a Linear state id — and the
  GitHub Projects adapter matched on exactly that. But the engine has no such
  id to give it: `workflow.columns` is written by hand (spec 04 §2), the lock
  records no mapping from a configured column to an option, and a column flow
  never created has no id anywhere in flow's world. So every configured
  transition named its target by display name and every one of them was
  refused, with `has no such column` and a list containing that very column.
  The adapter now resolves a target as an option id first and a display name
  second, folding case on the name because `engine.ColumnOf` reads a column
  back by a folded name and a move flow could not read back would not be
  idempotent. The ticket still comes back carrying the tracker's id.
- **§2 a column the board does not have is `ErrValidation`, and the contract
  says so (Stage B, found live).** The refusal existed in the real adapter
  and not in the reference fake, which invented a column out of whatever it
  was handed and recorded the ticket in it. That is why the contract suite
  was green while the only caller in the product was broken: the suite moved
  by id, because the fixture's constants are ids, and the fake accepted a
  name without noticing it had never heard of it. `FakeTracker` now refuses a
  column its fixture does not list — and only then, since a fixture that
  lists none describes no board — and the contract moves by name as well as
  by id.
- **§3 an archived item is off the board and takes no change (item: archiving,
  issue #99).** Spec 17 §3 asks this very board to auto-archive closed items,
  and nothing said what an adapter reports about one. GitHub Projects answers
  the same item two ways: `ProjectV2.items` leaves an archived item out, and
  `Issue.projectItems` returns it with the column it was archived in. Flow
  selected `isArchived` in neither query, so the same ticket was "not an item
  of the project" and "in column Done" at once, depending on which question
  was put. `core.Ticket` now carries `Archived`, both queries select the
  field, and the two readings keep their difference on purpose: a listing is
  the board, and a key is a question about an issue.
- **§5 a move of an archived item is refused, not performed.** GitHub answers
  `updateProjectV2ItemFieldValue` for an archived item with a null item, no
  `errors`, and no change — so an adapter that only looks for a GraphQL error
  reports a move the board never made. `setColumn` now treats a mutation that
  returned no item as a failure (`ErrConflict`), and `Move` refuses an
  archived item before asking, which is the refusal a person can act on. A
  write that changed nothing is not a write that worked.
- **§5 the reference fake models archiving the way the adapter does.** It is
  excluded from a listing, found by key, and refuses a move with
  `ErrConflict`; `TrackerContract` holds the case, so both halves must agree.
  The contract fixture gained `#15`, archived in In review, reachable as
  `coretest.ContractArchivedKey`.
- **§1 `Archived` is a question every tracker answers, and not always with a
  field (issue #103).** Flow asks a tracker two questions anyway — the board
  listing, and a ticket by key — so an adapter can report archiving even where
  the provider exposes nothing, by the two disagreeing. What each provider
  gives the adapter to work with:
  - *GitHub Projects (settled, issue #99).* `isArchived` on the item.
    `ProjectV2.items` excludes archived items, `Issue.projectItems` returns
    them; both queries select the field.
  - *Jira Cloud (item 7.1).* Issue archival is Cloud Premium (and Data Center
    8.1+). An archived issue is read-only and drops out of JQL search, while
    `GET /rest/api/3/issue/{key}` still returns the whole issue **with no
    archived field at all** — Atlassian has an open suggestion asking for one.
    So the adapter derives the flag: found by key, absent from the project's
    JQL listing. That inference is only sound against a full listing, never
    against one narrowed by `UpdatedSince`, a column or a limit, where absence
    means the filter and not the archive.
  - *Linear (item 7.2).* Every connection excludes archived entities unless
    the query passes `includeArchived: true`, and `archivedAt` carries the
    time. Linear **auto-archives issues a while after they close**, so flow's
    own tickets archive themselves in the ordinary course of a project and
    this is the tracker where the case is routine rather than exceptional.
    The board listing leaves `includeArchived` off, because a listing is the
    board; a lookup by key sets it, because a key is a question about an
    issue. `trashed` is a different state and reads as not found.
- **§5 the archived contract case is every adapter's.** `trackerArchived` is
  in `TrackerContractCases`, so Jira and Linear must pass what GitHub Projects
  passes: off the listing, present by key with its column, and a move refused
  with `ErrConflict` naming the archiving. Where a provider would accept the
  write, the adapter refuses it from the flag it already read — the contract
  is about what flow does, and flow must not report a move that it cannot
  know landed.

- **§3 flow does not translate its markdown into Jira's formatting (item
  7.1).** Jira Cloud keeps every body as an Atlassian Document Format tree,
  and the obvious reading of that is to render flow's headings, lists and
  fenced blocks as heading, list and code nodes. The obvious reading is
  wrong, and it is wrong because of what flow does with a body afterwards: a
  follow-up that already says the right thing must not be rewritten (§4,
  spec 07 §4), and the pinned health report parses its own table back out of
  the issue it wrote (spec 07 §10). Both rest on the body coming back as what
  went in, and a translation flow cannot reverse makes every run find the
  ticket changed. So the heading stays `## What to do`: ugly on the screen,
  and true.

- **§3 a line is a paragraph and a blank line is an empty one (item 7.1).**
  The one thing text has that an ADF document does not come by honestly is
  line structure. One line becomes one paragraph; a blank line becomes a
  paragraph with no content, which is valid ADF and the only node that reads
  back as a blank line. A text node holding an empty string, which is the
  other way to write it, Jira rejects. The rendering is exact in both
  directions and `FuzzADFRoundTrip` is what keeps it that way.

- **§3 reading a body is not the same job as writing one (item 7.1).** Flow
  writes two kinds of node and has to read every kind there is: an issue
  somebody wrote by hand is full of headings, lists, tables and panels, and
  the marker flow is looking for is in the text of them. The reading walks
  whatever it is given — a node whose children are blocks contributes each
  child's lines, one whose children are inline contributes a line — so a
  bulleted list reads as its items rather than as nothing. The v2 API's plain
  string is read as the text it is for the same reason: a body flow cannot
  parse is a ticket flow cannot read.

- **§3 a column is a status, and a board column is not one (item 7.1).**
  Jira has two things a person would call a column: the status an issue is
  in, which belongs to the project's workflow, and the column of a board,
  which is a grouping of statuses that somebody arranged. `workflow.columns`
  names one thing per column and `Move` takes one target, so the adapter
  reads statuses — `GET /project/{key}/statuses`, deduplicated across the
  issue types that share them, in the order Jira gave them. A board column is
  several statuses and a project may have several boards, so a board column
  is not a thing flow could move a ticket to without choosing which board it
  meant.

- **§3 flow sets no desired state in a Jira project (item 7.1).** The
  capability file as first researched said `status_options` and `labels` were
  settable and `wip_limits` verifiable. None of the three survived contact.
  A status can be created through the status API and a created status is
  still not a column: which statuses a project's workflow offers is the
  workflow's business and which column shows one is the board's, and neither
  is writable in the public Cloud API — so a flow that created one would
  report a column the team cannot see. A label has no registry at all: it
  exists the moment an issue carries one, so there is nothing to create and
  nothing to read back. A WIP limit is on a board, and a project may have
  several, so flow cannot say which one a limit is on. `Apply` therefore
  refuses every change with `ErrUnsupported` and the declaration says
  `set: false` everywhere, which is what makes the plan file a follow-up for
  somebody who administers the project instead of proposing work that can
  never land (spec 05 §4).

- **§3 the AC's board-configuration reading is not a verification (item
  7.1).** `GET /rest/agile/1.0/board/{id}/configuration` is real and flow
  does not call it. It answers for one board, and nothing in `.flow.yaml`
  names a board; picking one would make flow's reading of the team's limits
  depend on which board it happened to choose. `wip_limits` is therefore
  `verify: false` and compensated by `policy.wip`, which is a rule flow can
  enforce from the tickets themselves.

- **§3 an archived issue is the two readings disagreeing (item 7.1).**
  Jira Cloud publishes no archived flag anywhere — not on the issue, not in
  a field, not as an expansion (JRACLOUD-94071 is the open request for one).
  An archived issue is read-only, keeps its status, answers by key in full,
  and is in no search. That disagreement is the whole of the evidence, so the
  adapter derives `Archived` from it. The listing it derives it from is the
  whole project and nothing narrower: absence from a windowed or filtered
  search means the filter. It is read once a run, because archiving is not
  something that happens while flow is looking and a search per ticket would
  double the cost of an audit that walks a fortnight of them.

- **§3 a move is a workflow transition, so an unreachable column is refused
  (item 7.1).** GitHub Projects takes any option id for any item; Jira will
  only make the moves the workflow offers from where the issue is now. The
  adapter asks `GET /issue/{key}/transitions` and performs the one that lands
  on the wanted status, and when there is none it refuses with the two status
  names in the message and the remediation pointed at whoever administers the
  workflow. Closing is the same mechanism: a resolution is set on the way
  through only where the transition's own screen offers the field, because
  sending a field the screen does not have is how Jira answers 400.

- **§3 linkage is flow's convention and not the forge's (item 7.1).** Jira
  learns about a pull request through its own GitHub integration, and nothing
  flow can read tells it which issue a pull request closes. `LinkedKeys`
  therefore reads the project's key out of the branch name, the title and the
  header a squash would land — the convention every Jira team already has.
  It is the same fallback spec 09 §3 defines for a forge that publishes no
  link, written in the tracker rather than guessed at by the engine.

- **§3 Basic is a pair, and only half of it is the secret (item 7.1).**
  Jira Cloud authenticates an API token against the email of the account that
  made it. The email is not a secret and the token is, so they are paired in
  the root, after the credential has been resolved and before the client is
  built: `tracker.auth.email_env` names where the email comes from, the
  encoded value is the credential from that point on, and the HTTP client
  still knows one way to authenticate. A Jira site has its own address, so
  an empty `tracker.url` is a question rather than a default, asked — as a
  forge's is — before any `token_cmd` runs (spec 15 §1).

- **§3 a refusal is read in `core`, not in the adapter (item 7.1).** Jira
  writes a refusal the other way round from GitHub: a list of sentences under
  `errorMessages`, and `errors` holding a map of field to message rather than
  a list of objects. A reader that declares one shape cannot decode the other
  body at all, so `core.APIMessage` tries both and the adapter keeps no error
  path of its own — `core.Client` has already written the error by the time
  the adapter sees it, and a second reader would only disagree with the
  first.

- **§3 a reading is revalidated, never skipped (item 7.1).** What an issue
  may do next changes with every move it makes, and the three readings that
  move with the work — the transitions, the comments, the search — were first
  written with the ETag store switched off for fear of acting on yesterday's
  answer. The fear was misplaced: `core.Client` revalidates every cached GET
  conditionally, so a stored body is only ever reused when the provider says
  it is still the answer, and a cache the provider has agreed with is not a
  stale reading. The flags are gone — on the search it was doing nothing at
  all, since only a GET is cached and a JQL search is a POST — and a test
  moves an issue twice through one client with a warm store to prove the
  second move was decided on the issue as it then stood.


- **§4 one adapter for a workspace nobody hosts (item 7.2).** Linear has one
  host for everybody, so `tracker.url` names nothing and `tracker.project`
  holds a team key such as `ENG`. A team owns the workflow states that are
  flow's columns and the issues that are its tickets, which is what makes a
  team the board. The key is resolved into the team's id once a run and
  remembered; a key the workspace has not got is `ErrNotFound` naming the
  setting, not a failed query.

- **§4 the credential is the whole Authorization header (item 7.2).** A
  Linear personal API key is sent with no scheme in front of it, which every
  other provider flow speaks to would read as a malformed header. The empty
  string already means "the default", so `core.AuthSchemeNone` is what says
  it, and which kind of credential it is, is in the credential: a token
  beginning `lin_oauth_` is an OAuth access token and goes out as a Bearer
  token. Nobody has to be asked a second question about a credential that
  names itself.

- **§4 a provider names a failure where it likes, and `core` finds it (item
  7.2).** GitHub writes `type` beside the message and Linear writes
  `extensions: {{type, code}}` underneath it. `core.GraphQLError.Kind` answers
  the first non-empty of the two, so an adapter maps the names its own
  provider uses and never unwraps the envelope again. Both of Linear's are
  mapped: the codes are the precise names and the coarse types are underneath
  them, and a refusal flow has no code for must not read as an outage worth
  retrying.

- **§4 six state types are three categories (item 7.2).** Linear calls a
  state triage, backlog, unstarted, started, completed or canceled. The first
  three are work nobody has begun, the fourth is work in hand, and the last
  two are work that is over — a column flow calls done is a column work leaves
  the board from, and cancelled work has left it. Going the other way, a
  column flow creates in a category it was not told becomes `started`, because
  the two ends of the board are where Linear's own automations fire and the
  middle is the only category that cannot make the board say something untrue
  about work nobody has touched.

- **§4 a category is settled when a column is created (item 7.2).**
  `WorkflowStateUpdateInput` carries no type, so the only way to give an
  existing state another category would be to archive it and make a new one,
  which takes every issue standing in it off the board. flow refuses with
  `ErrUnsupported` and says what a person does instead; the declaration says
  `status_categories: set: false` for the same reason. A rename, a colour and
  a description are all changes Linear does take.

- **§4 an issue has no closed flag, so the column is the closing (item
  7.2).** Work is finished when it stands in a state Linear calls completed
  and dropped when it stands in one it calls canceled, and Linear dates both
  itself. `Close` with `not_planned` therefore looks for a cancelled column
  and `Close` with `completed` for a completed one, each the leftmost of its
  type; `Reopen` looks for unstarted, then backlog, then triage. A team with
  nowhere to cancel work completes it rather than failing the run.

- **§4 archived is the board's absence and trashed is gone (item 7.2).**
  Every Linear connection leaves archived issues out unless it is asked for
  them, and a lookup by identifier answers for one regardless — the two
  readings flow needs, with no deriving. Linear archives an issue a while
  after it closes, so the follow-up search asks `includeArchived: true` or
  flow would file the same ticket every month. An issue in the trash is not
  there at all, which is `ErrNotFound`.

- **§4 linkage is what Linear recorded, then what the branch says (item
  7.2).** `core.Tracker.LinkedKeys` is handed a pull request, no context and
  no error, so it cannot ask: the attachment reading is the one flow has
  already done. Every issue flow reads carries its attachments, and the
  addresses in them answer first — Linear's own GitHub integration records a
  pull request there. The fallback is the identifier in the branch, the title
  or the header a squash will land, which is Linear's own documented
  convention and never a pattern somebody configured (P12).

- **§4 the labels of the workspace, the labels of this board (item 7.2).** A
  Linear label belongs to a team or to the workspace, and a team may use
  either. flow reads them all and keeps the ones this team may use, so a
  `flow-followup` somebody made workspace-wide is found rather than created a
  second time; a label group is a heading rather than a label and a retired
  one is no longer offered, so neither is one. What flow creates, it creates
  on the team: a label every team sees is somebody else's decision.

- **§4 an estimate is points, and points are not hours (item 7.2).**
  `core.Ticket.Size` is a duration, and Linear's estimate is a number of
  points whose meaning each team settles for itself. flow leaves the field
  empty rather than multiplying by an hour it invented.

- **§1 a tracker can be asked what was said on a ticket (issue #145).**
  `core.Tracker.Comments` returns every comment on a ticket, oldest first, as
  `core.TicketComment` — author, body and the moment it was written. It is not
  a capability: every tracker flow speaks to has comments, and a declaration
  that could say no would be declaring something no adapter does. The marker
  is left in the body rather than parsed out by the adapter, because telling
  flow's own writing from somebody's explanation is the caller's question and
  `core.Marked` is where it is answered.

- **§1 one walk of a ticket's comments, two questions.** Finding flow's own
  comment by its marker and listing all of them are the same walk with
  different stopping conditions, so each adapter has one `walkComments` and
  both callers go through it: two pagers over the same endpoint would drift,
  and the one that is wrong would be the one nothing reads daily. Jira asks in
  the order the question wants — newest first for flow's own comment, oldest
  first for the list — because Jira is the only one of the three that orders a
  page on request.

- **§1 an empty list and a key nobody has are different answers.** Jira
  answers the comment endpoint of a missing issue with a 404 that names the
  endpoint rather than the issue, so the adapter reads the issue first: a
  board check that could not tell "nothing was said" from "no such ticket"
  would read a mistyped key as a clean bill of health.


- **§1 a branch a tracker has forgotten the name of is not a branch (issue
  #144).** GitHub keeps the link between an issue and a branch after somebody
  deletes the branch, and answers `linkedBranches` with a node whose `ref` is
  null. The work it stood for is over and flow could not name it in a
  sentence anyway, so the adapter drops it. The capability is declared
  `set: false, verify: true`: flow reads the link and never writes one, and
  creating a branch for somebody is not a remedy flow offers.

- **§1 two of the three trackers cannot answer this at all, and say so.**
  Jira's development information — branches, commits, pull requests — has no
  public API, and Linear's `Issue.branchName` is the name it *suggests*, not
  evidence anybody cut it. Both declare `linked_branches_read` unset and
  unverifiable, which is what makes `workflow.transitions.branch_created`
  refuse at validation on those boards rather than at six in the morning.

- **§1 `core.Ticket.Repo` is the repository a ticket belongs to, as
  `owner/name`, where the tracker records one (issue #89).** The GitHub
  Projects adapter already qualified a foreign ticket's *key* as
  `owner/repo#7`, but a key is a thing people type, and the engine parsing
  GitHub's spelling of one back apart would be the engine knowing which
  tracker it was talking to. The repository is a fact about the ticket, so the
  adapter states it. It is filled for every ticket, this repository's
  included: a field set only for strangers would leave an ordinary ticket
  looking like one from a tracker that records no repository at all. Jira and
  Linear leave it empty, and a board of theirs is one repository's by
  assumption.
- **§3 whether a built-in workflow is on is readable; what it does is not
  (issue #184).** `ProjectV2.workflows` lists the board's own
  automations by name with an `enabled` flag, so a workflow the configuration
  relies on and nobody switched on is a promise this board cannot keep,
  whoever vouches for it. Which column the workflow moves a card to is still
  unreadable, which is why the adapter refutes the attestation rather than
  replacing it: flow can prove the promise is not being kept and still cannot
  prove it is.
- **§3 the pairing of flow's events to GitHub's workflow names lives in the
  adapter (issue #184).** Translating flow's vocabulary into the
  provider's is the adapter's whole job. The events with no pairing are the
  ones GitHub has no built-in for; flow asks a person for nothing about those,
  so there is nothing to refute and no query to pay for.
- **§3 nothing is said about the workflows of a board nobody has
  (issue #184).** A missing board is the project check's finding,
  and every other tracker check waits on that one. A second complaint
  underneath it is noise.
- **§3 Jira and Linear name the board their credential cannot see
  (issue #292).** Jira's 404 on `mypermissions?projectKey=` and
  Linear's empty `teams(filter: {key})` each mean "gone or not shared",
  which is what `Permissions.Unreachable` holds, so `auth.tracker`
  warns with steps instead of burying it in evidence. Neither probe
  can reach that answer without a project or team configured.
- **§1 Jira's follow-up search reads the follow-up label, not the
  project (issue #296).** `FindFollowUp` narrows its JQL with
  `labels = "<first usable followups.labels entry>"` and still matches
  the marker in the descriptions it gets back. With no label Jira would
  take, it reads the whole project as before. There is no fallback to
  the full scan on a miss: a miss is the ordinary answer before a
  follow-up is filed, and scanning then would cost exactly what the
  label saves. A follow-up filed before the label was configured, or
  one whose label somebody removed, is not found, and flow files a
  fresh labelled one in its place. The health issue is found the same
  way and carries the same labels.
