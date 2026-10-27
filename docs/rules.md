# Why am I seeing this?

The `flow/policy` check on a pull request is flow's gate. When it fails, or
warns, its comment links each rule to the section below: what the rule asks,
why, and every way to resolve it. A rule's severity comes from
`policy.strictness` and can be changed per rule in `.flow.yaml`:

```yaml
policy:
  rules:
    wip.review: error             # off | warn | error
```

A warning never fails the check; an error does. The daily `flow doctor` audit
reports the same things after the fact, so a rule you switch off still shows up
there.

## pr.ticket.linked

**Asks:** that the pull request says what it is for, by closing a ticket.

**Why:** a merge nobody can trace back to a piece of work is a change nobody
asked for, and the board cannot move a card for work it cannot see.

**To resolve it, do one of:**

- Link a ticket. On GitHub, write a closing keyword in the description —
  `Closes #123` — or link the issue from the Development sidebar. `Refs #123`
  mentions an issue but closes nothing, so it does not count. On Jira or Linear,
  put the ticket key in the branch name or at the start of the title.
- If the change needs no ticket, title it with a type listed in
  `commits.unlinked_types` (by default `ci`, `build`, `docs`, `chore`).
- If it is urgent — a security fix, an outage — label the pull request with one
  of `workflow.expedite.labels` (by default `incident`). It may then merge
  without a ticket, and the daily audit (`live.prs.linked`) names it until
  somebody writes one up.

## pr.ticket.in_project

**Asks:** that the linked issue is a card on the board (GitHub Projects only).

**Why:** an issue that is not on the board has no column, so nothing can move it
and no limit counts it.

**To resolve it:** add the issue to the project, or restore it if it was
archived.

## ticket.ready

**Asks:** that the linked ticket met the Definition of Ready before work started
(`definition_of_ready.rules`): acceptance criteria, a parent, not blocked.

**Why:** work started on a ticket nobody can say is finished tends not to
finish.

**To resolve it:** fill in what the ticket is missing — an "Acceptance criteria"
heading, a parent issue — or move it back until it is ready. A ticket already in
progress or in review is not judged again.

## pr.title.conventional

**Asks:** that the title is a Conventional Commit header, because with squash
merges the title becomes the commit on the trunk.

**Why:** the trunk's history is read by people and by tools — release notes,
version bumps — and a header they cannot parse is a change they cannot account
for.

**To resolve it:** write it as `type(scope): description`, with a type from
`commits.types`, a scope from `commits.scopes.allowed` when scopes are required,
and the whole header, including the ` (#123)` GitHub adds on merge, within
`commits.header_max`.

## pr.breaking.explained

**Asks:** that a title marked breaking (`feat!:`) explains the break in a
"Breaking change" section of the description.

**Why:** whoever upgrades needs to know what to change.

**To resolve it:** add the section, or drop the `!` if nothing breaks.

## agent.human_review

**Asks:** that a pull request an agent opened (`agents.authors`,
`agents.labels`) has an approval from a person who is not an agent.

**Why:** somebody accountable has read what a machine wrote.

**To resolve it:** ask a person to review and approve. Types listed in
`agents.unreviewed_types` are exempt.

## wip.review

**Asks:** that the review column is within its limit. It warns and never fails
the check, unless `policy.rules` makes it an error.

**Why:** a long review queue is work waiting on people; the limit makes it
visible.

**To resolve it:** review or land something already in review. When the column
is over its limit, the oldest pull requests are not warned about.

## wip.in_progress

**Asks:** that the author has no more work in progress than the per-person limit.
It warns and never fails the check, unless `policy.rules` makes it an error.

**Why:** finishing beats starting.

**To resolve it:** finish or hand over something already started.

## ticket.not_blocked

**Asks:** that no linked ticket is labelled blocked (`workflow.blocked.label`).

**Why:** a blocked ticket moving forward means either it is not blocked any more
or something is being worked around.

**To resolve it:** clear the blocker, or say on the ticket that this pull
request is what clears it, and remove the label once it is gone.
