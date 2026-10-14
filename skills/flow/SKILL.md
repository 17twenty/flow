---
name: flow
description: Work a ticket to a reviewed pull request in a repository governed by flow. Use when the repository has a .flow.yaml, or when asked to pick up a ticket, open a pull request against a protected trunk, or find out why a flow/policy status is red.
---

# Working a ticket with flow

`flow` makes one lean, trunk-based workflow true and keeps it true. It owns the
rules; you do the work. Everything below is read out of the repository's own
`.flow.yaml` — never assume the numbers in this file, ask the repository.

The loop, once: **verify the repo → take a ticket → branch → commit → pull
request → wait for the gate and a human → merge.**

## 0. Is this a flow repository, and which flow

```bash
test -f .flow.yaml && flow version
```

No `.flow.yaml` means flow is not the authority here; stop and say so. No `flow`
on `PATH` means build it (`go build -o bin/flow .` in flow's own checkout) or
install the release binary. Every command below reads `.flow.yaml` from the git
root, so run them anywhere inside the checkout.

## 1. Credentials

flow never stores a credential. The configuration names the environment
variable each provider's token comes from:

```bash
grep -n -A1 'auth:' .flow.yaml      # e.g. token_env: GITHUB_TOKEN
```

Export each one named there, from wherever your secrets live:

```bash
export GITHUB_TOKEN="$(…your secret store…)"
export FLOW_PROJECT_TOKEN="$GITHUB_TOKEN"   # only if the tracker names its own
```

Three rules that are not negotiable:

- **Never write a token into a file inside the repository**, not even a note to
  yourself. Forge push-protection rejects the whole push, and the credential is
  then burnt.
- **Never echo a token.** Check you have one with `test -n "$GITHUB_TOKEN"`.
- A credential is only sent to its provider's own API host. If flow exits 77
  saying a host is not approved, that is the guard working: fix the
  configuration or list the host in your user config, do not route around it.

## 2. Verify the repository before you touch it

```bash
flow doctor --json | jq '{exit: .summary, failing: [.checks[] | select(.outcome=="fail") | .id]}'
```

Exit `0` everything holds · `2` something is actionable · `1` flow could not
find out. A `2` is not automatically yours: a drift finding or a follow-up
addressed to a maintainer is theirs. What stops you starting is a repository
whose *setup* is incoherent — unresolved placeholders, no board, no gate. Those
say what to run, usually `flow doctor --fix` or `flow plan` then `flow apply`.

```bash
flow plan            # read-only: what flow would change, and what it cannot
flow apply --auto-approve   # only if the plan is yours to apply
```

Never run `flow apply` as a way of making a check pass. It changes the
repository's settings and the board; it is the maintainer's command unless the
ticket you are working is about those settings.

## 3. Take a ticket

flow has no command that lists your board, deliberately: the board belongs to
the tracker and flow only reads it to judge it. Use the tracker's own API (see
the recipe at the bottom for GitHub Projects).

What `.flow.yaml` tells you about which ticket you may take:

- `workflow.columns` — the column carrying `gate: definition_of_ready` is where
  takeable work sits. Work in a `todo` column *before* that gate is not ready
  for you.
- `definition_of_ready.rules` — a ticket failing these will fail `ticket.ready`
  at the gate. Acceptance criteria missing is the usual one: ask for them,
  do not invent them.
- `workflow.blocked.label` — never take a blocked ticket.
- `workflow.columns[].wip` — `per_person` caps how many tickets you may have in
  progress. Exceeding it is a warning on every pull request you then open. One
  ticket at a time is the intent.

Do not move the card by hand. Opening the pull request is what moves it, and
flow does the moving (§4 of the workflow model): a card you drag yourself makes
the board say something nobody can verify.

## 4. Branch, work, commit

```bash
git switch -c <short-topic-branch>
```

Commit messages are linted by the same parser the gate uses, so check each one
before you commit rather than after:

```bash
printf '%s\n' 'feat(engine): the plan names what it could not do' > /tmp/msg
flow check commit /tmp/msg    # silence is a pass; exit 1 lists the rules broken
git commit -F /tmp/msg
```

`flow check commit --fix /tmp/msg` applies the safe repairs in place.
`flow hook install` wires it into `git commit` for the rest of the session.

The allowed types, the allowed scopes and the header length are all in
`commits:` in `.flow.yaml`. A scope is usually required and usually must come
from the configured list — read it, do not guess.

## 5. Open the pull request

Two things the gate will not forgive:

- **The body must link the ticket**: `Closes #123` (`Fixes`/`Resolves` also
  work) in the pull request *body*, not only in a commit message. That is
  `pr.ticket.linked`, and only the types in `commits.unlinked_types` are exempt.
- **The title must itself be a valid commit header** — type, scope, length —
  because the squash lands it as the commit. The forge may append to it:
  GitHub squashes `<title> (#123)`, so a title that fits `header_max` exactly
  still breaks it once merged. flow lints the header the forge *will write*, so
  leave about seven characters of room.

If `agents.labels` names a label, add it: it is how the repository tells agent
work from everyone else's, and dropping it does not dodge the review rule — it
makes the check pass while proving nothing.

Before you ask anyone to look, see the verdict yourself:

```bash
flow check pr --pr 123 --dry-run --exit-code
```

`--dry-run` posts nothing. `--exit-code` is what makes a failing policy exit
non-zero at your desk; in CI the commit status carries the verdict instead and
the job exits 0 on purpose.

## 6. Wait for the gate, and for a person

CI runs `flow check pr`. It posts a commit status (`policy.status_context`,
usually `flow/policy`), writes one comment listing every rule that failed with
its fix, and moves the ticket's card. Read that comment; the rule ids are the
whole diagnosis:

| id | what it means for you |
|---|---|
| `pr.ticket.linked` | the body does not name a ticket |
| `pr.ticket.in_project` | the ticket is not on the board (or is archived) |
| `ticket.ready` | the ticket does not meet the Definition of Ready |
| `pr.title.conventional` | the title, as it would land, is not a valid header |
| `pr.breaking.explained` | a `!` title with no "Breaking change" section |
| `agent.human_review` | **a human has not approved yet** |

`agent.human_review` failing is the gate working, not a fault. **Never approve
your own pull request**, and never remove the agent label to clear it. Ask the
human, say what is waiting, and wait.

A red status after an edit: the gate re-runs on `edited`, so fixing a title is
a title change and nothing else.

## 7. After the merge

The trunk is protected by the status the gate posts, so the merge is somebody's
decision, not a push you make. Once it lands, the native automations close the
ticket and move the card; flow verifies that rather than doing it. Confirm with
`flow doctor` and stop. If the card is wrong, that is a finding worth reporting,
not a card to drag.

## Exit codes

`0` OK · `1` failed · `2` action required · `64` you typed something wrong ·
`69` provider unavailable · `77` not permitted (or an unapproved host) · `130`
interrupted.

## Never

- Write a credential into a repository file, or print one.
- Approve your own pull request, or merge past a red gate.
- Push to the protected trunk directly.
- Weaken or skip a test, or hand-move a card, to make a check green. Every one
  of those makes the board say something untrue, which is the one thing this
  workflow exists to prevent.

## Recipe: GitHub and GitHub Projects with a PAT

A classic PAT with `repo` (and `project` if the board is a Projects v2 board)
does all of it. `jq` is assumed; `gh` is not.

```bash
TOK="$GITHUB_TOKEN"; REPO=owner/name
api() { curl -fsS -H "Authorization: Bearer $TOK" \
  -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" "$@"; }

# Open issues, newest first
api "https://api.github.com/repos/$REPO/issues?state=open&per_page=50" |
  jq -r '.[] | select(.pull_request|not) | "\(.number)\t\(.title)"'

# One issue, with its body (the acceptance criteria live there)
api "https://api.github.com/repos/$REPO/issues/123" | jq -r '.title, .body'

# The board, with each card's column
cat > /tmp/q.json <<'JSON'
{"query":"{ user(login:\"OWNER\") { projectV2(number: 2) { items(first: 100) {
  nodes { content { ... on Issue { number title } }
    fieldValueByName(name: \"Status\") {
      ... on ProjectV2ItemFieldSingleSelectValue { name } } } } } } }"}
JSON
api -d @/tmp/q.json https://api.github.com/graphql |
  jq -r '.data.user.projectV2.items.nodes[] |
         "\(.fieldValueByName.name)\t#\(.content.number)\t\(.content.title)"'

# Open the pull request (the body carries the link), then label it
api -X POST "https://api.github.com/repos/$REPO/pulls" \
  -d '{"title":"feat(engine): …","head":"my-branch","base":"main",
       "body":"What changed and why.\n\nCloses #123"}' | jq .number
api -X POST "https://api.github.com/repos/$REPO/issues/456/labels" \
  -d '{"labels":["agent"]}' >/dev/null

# Is the gate green yet?
SHA=$(api "https://api.github.com/repos/$REPO/pulls/456" | jq -r .head.sha)
api "https://api.github.com/repos/$REPO/commits/$SHA/status" |
  jq -r '.statuses[] | "\(.context)\t\(.state)\t\(.description)"'
api "https://api.github.com/repos/$REPO/commits/$SHA/check-runs" |
  jq -r '.check_runs[] | "\(.name)\t\(.conclusion // .status)"'
```

Send GraphQL as `-d @file`, never as an inline `"$(jq …)"`: the shell mangles
it and the error points at valid syntax.

Pushing as a machine account needs the token in the remote, not an SSH key:
`https://x-access-token@github.com/owner/name.git` with a credential helper, and
commits authored as `<id>+<login>@users.noreply.github.com` so the forge
attributes them to that account.
