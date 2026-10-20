# Two repositories, one board

A frontend and a backend often share a plan. The tickets are the same tickets —
`hbp-412` is one piece of work that happens to need a change in each — and the
team wants one board for them, not two.

flow supports that, with one thing said out loud. This page is what to write
and why.

## What flow can and cannot see

flow runs **in a repository**. `forge.repo` names it, the credential reaches
it, and every pull request flow reads is one of that repository's. The board is
the other half, and a board is not scoped to a repository at all: a GitHub
Project gathers issues from wherever you add them from.

So a flow running in `acme/api` and looking at a board it shares with
`acme/web` sees:

- `acme/api`'s tickets, and the pull requests behind them. It can judge these.
- `acme/web`'s tickets, and **no** pull requests at all. It cannot judge these,
  and left alone it would report every one of them as work that reached Done
  without a merge behind it.

The second is what `tracker.shared_with` is for.

## The configuration

In **each** repository, name the others:

```yaml
# acme/api/.flow.yaml
forge:
  repo: acme/api
tracker:
  type: github-projects
  owner: acme
  owner_type: org
  project: "4"
  shared_with:
    - acme/web
```

```yaml
# acme/web/.flow.yaml
forge:
  repo: acme/web
tracker:
  type: github-projects
  owner: acme
  owner_type: org
  project: "4"            # the same board
  shared_with:
    - acme/api
```

Everything else — the columns, the transitions, the Definition of Ready, the
policy rules — should be the same in both. They describe the board, and there
is one board.

A repository does not name itself: `shared_with` is the list of repositories
whose work *this* flow is not the one to judge.

## What each half then does

| | `acme/api`'s flow | `acme/web`'s flow |
|---|---|---|
| `acme/api`'s tickets | audits them | passes over them |
| `acme/web`'s tickets | passes over them | audits them |
| a third repository's | **reports them** | **reports them** |

Concretely:

- `live.tickets.done_merged`, `live.tickets.done_deployed` and
  `live.tickets.review_has_pr` skip a named sibling's tickets, and say how many
  they skipped — "all 6 tickets that reached Done had work behind them, not
  counting 3 tickets a repository sharing this board owns".
- `live.transitions.flow` will not move a named sibling's ticket, even under
  `--fix`. The reasons to leave it where it is live in the other repository.
- `live.tickets.shared_board` reports any repository on the board that
  **nothing** names, and tells you the line to write.

That last check is the one to watch. A board that quietly gained a third
repository is a board where some work is audited by nobody, and the silence is
exactly what would hide it.

## The gate, per repository

`flow check pr` runs in the repository the pull request is in and judges that
pull request. Nothing about it is shared, so each repository wires its own gate
the usual way (`docs/cookbook.md` §7) and each gets its own `flow/policy`
status. Two pull requests for one ticket — one in each repository — are two
independent gates, which is what you want: each is reviewed by the people who
know that codebase.

Branch protection is per repository too. Requiring `flow/policy` on `acme/api`
says nothing about `acme/web`.

## Ticket keys across repositories

GitHub writes a cross-repository issue reference as `owner/repo#7`, and flow
uses exactly that. A ticket in the board's own repository is `#7`; one from
elsewhere is `acme/web#7`. Both forms work anywhere flow takes a key, and a
commit or pull request in `acme/api` that writes `Closes acme/web#7` links the
two the way GitHub does.

## Trackers that have no repositories

`shared_with` is a GitHub Projects key, and flow refuses it elsewhere.

Jira and Linear scope work to a *project* or a *team*, not to a repository: a
ticket there does not record which codebase it is about, so flow in `acme/api`
has no way to tell a sibling's ticket from its own. Two repositories against
one Jira project is therefore **not** an arrangement flow can audit today. One
project per repository is, and the shared plan lives above both as epics.

## Checklist

1. Add the second repository's issues to the same board.
2. Copy `.flow.yaml` across; change `forge.repo` and swap the `shared_with`
   entries.
3. `flow doctor` in each repository. `live.tickets.shared_board` should pass in
   both.
4. Install the gate workflow in the second repository and require
   `flow/policy` on its trunk.
5. `flow apply` in each: the labels and the board are shared, so the second run
   should find most of its work already done.
