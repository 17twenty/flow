---
name: flow
description: Work a ticket to a reviewed pull request in a repository governed by flow. Use when the repository has a .flow.yaml, or when asked to pick up a ticket, open a pull request against a protected trunk, or find out why a flow/policy status is red.
---

# Working a ticket with flow

`flow` makes one lean, trunk-based workflow true and keeps it true. It owns the
rules; you do the work. Everything below is read out of the repository's own
`.flow.yaml` — never assume the numbers in this file, ask the repository.

The loop, once: **verify the repo → take a ticket → branch → commit → pull
request → wait for the gate and a human → stop.**

## 0. Is this a flow repository, and which flow

```bash
test -f .flow.yaml && flow version
```

No `.flow.yaml` means flow is not the authority here; stop and say so. If `flow`
is not on `PATH`, install the release binary — or, if this repository *is*
flow's source, build one outside the working tree:

```bash
go build -o /tmp/flowbin . && /tmp/flowbin version
```

Build to a temporary path, never into the checkout you are about to open a pull
request from. Every command below reads `.flow.yaml` from the git root, so run
them anywhere inside it.

## 1. Credentials

flow never stores a credential. The configuration names the environment
variable each provider's token comes from, and the tracker often names a
different one from the forge:

```bash
grep -n -A1 'auth:' .flow.yaml      # e.g. token_env: GITHUB_TOKEN
```

Export every variable named there, from wherever your secrets live, and export
them again in each shell you run:

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
flow doctor --json |
  jq -r '.exit_code, (.checks[] | "\(.result.state)\t\(.id)\t\(.result.message)")'
```

The run's exit code is `0` everything holds · `2` something is actionable · `1`
flow could not find out. Each check carries its own state, and there are more
than two:

| state | whose it is |
|---|---|
| `pass`, `skip` | nobody's: it holds, or this configuration never asked for it |
| `warn` | below target, above the threshold — does not set the exit code |
| `fail`, `error` | something is wrong, or flow could not find out |
| `input` | flow needs a value nobody has given it |
| `drift` | a setting flow was told to keep has been changed |
| `external` | somebody with access has been asked, and a follow-up is open |
| `attest` | a human's word is what settles it, and it has not been given |

So **an exit of 2 is not automatically yours**. An `external` or `attest` item
is addressed to a maintainer, and a historical `fail` about work that merged
long before you arrived is a finding, not a blocker. What stops you starting is
the repository's own *setup* being incoherent: `config.*` failing, no board, no
gate. Those say what to run.

Read the state, not the file. `.flow.yaml` may hold `${pending:…}` placeholders
that `.flow.lock` has already resolved — the lock is where flow writes what it
has discovered, so a placeholder in the configuration with a passing check
beside it is a resolved placeholder.

```bash
flow plan            # read-only: what flow would change, and what it cannot
```

Never run `flow apply` to make a check pass. It changes the repository's
settings and the board; it is the maintainer's command unless the ticket you
are working is about those settings.

## 3. Take a ticket

flow has no command that lists your board, deliberately: the board belongs to
the tracker and flow only reads it to judge it. Use the tracker's own API (see
the recipe at the bottom for GitHub Projects — and read all of the board, not
the first page of it).

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
flow does the moving: a card you drag yourself makes the board say something
nobody can verify.

## 4. Branch, work, commit

Before writing anything, read how this repository works: `AGENTS.md`,
`CONTRIBUTING.md`, `README.md`, whatever it has. flow owns the workflow, not
the conventions — branch naming, the test command, the toolchain and the
house style are the repository's, and they are not in this skill.

```bash
git switch -c <branch-the-repository-would-name>
```

**Commit as the account whose work this is.** If you are running as a machine
account, the repository's `agents.authors` is probably that account's login,
and `agent.human_review`, the agent label and the audit of who reviewed what
all hang off it. A commit authored as the human whose git config you inherited
makes every one of those say something untrue:

```bash
git -c user.name='Flow Bot' \
    -c user.email='341252896+flow-bot-gh@users.noreply.github.com' commit -F /tmp/msg
```

The `<id>+<login>@users.noreply.github.com` form is what makes GitHub attribute
the commit to that account; the id comes from `/users/<login>`.

Commit messages are linted by the same parser the gate uses, so check each one
before you commit rather than after:

```bash
printf '%s\n' 'feat(engine): the plan names what it could not do' > /tmp/msg
flow check commit /tmp/msg    # silence is a pass; exit 1 lists the rules broken
```

`flow check commit --fix /tmp/msg` applies the safe repairs in place.
`flow hook install` wires that into `git commit` — but the hook calls `flow`
from `PATH`, and where there is none it says so and lets the commit through.
The hook is a convenience; running `flow check commit` yourself is the thing
that protects you.

The allowed types, the allowed scopes and the header length are in `commits:`
in `.flow.yaml`: `commits.types`, `commits.scopes.allowed`,
`commits.header_max`. Read them, do not guess. One trap in choosing a type:
`commits.unlinked_types` are the types exempt from naming a ticket, so picking
one of those for work that *has* a ticket quietly switches off the rule that
links them.

## 5. Open the pull request

Two things the gate will not forgive:

- **The body must link the ticket**: `Closes #123` (`Fixes`/`Resolves` also
  work) in the pull request *body*, not only in a commit message. That is
  `pr.ticket.linked`.
- **The title must itself be a valid commit header** — type, scope, length —
  because the squash lands it as the commit. The forge may append to it:
  GitHub squashes `<title> (#123)`, so a title that fits `commits.header_max`
  exactly still breaks it once merged. flow lints the header the forge *will
  write*, so leave about seven characters of room.

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
| `wip.in_progress`, `wip.review` | too much work in flight (warnings) |
| `ticket.not_blocked` | a linked ticket carries the blocked label (warning) |

A rule can also be skipped rather than judged — `ticket.ready` is, once the
work has started, because the Definition of Ready was the entry gate and is not
re-litigated. A skip is not a failure.

`agent.human_review` failing is the gate working, not a fault. **Never approve
your own pull request**, and never remove the agent label to clear it. Ask the
human and stop:

```bash
# Request the review, then say in one comment what is outstanding.
api -X POST ".../pulls/123/requested_reviewers" -d '{"reviewers":["their-login"]}'
api -X POST ".../issues/123/comments" -d '{"body":"Ready for review: …"}'
```

Who to ask is in the repository: `followups.owners` names the maintainers, and
the people who approved recent pull requests are the other answer.

A red status after an edit is normal: the gate re-runs on `edited`, so fixing a
title is a title change and nothing else.

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
does all of it. `jq` and `python3` are assumed; `gh` is not.

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
```

The board is `tracker.owner` and `tracker.project` from `.flow.yaml`, with the
project number possibly resolved in `.flow.lock` instead. **Page it.** A board
is routinely larger than one page, and a query that stops at a hundred items
reports the ticket you were sent to work as not being on the board at all —
which looks exactly like `pr.ticket.in_project` failing:

```bash
python3 - "$GITHUB_TOKEN" 17twenty 2 <<'PY'
import json, sys, urllib.request
tok, owner, number = sys.argv[1], sys.argv[2], int(sys.argv[3])
query = """query($owner:String!,$number:Int!,$after:String){
  user(login:$owner){ projectV2(number:$number){ items(first:100, after:$after){
    pageInfo{ hasNextPage endCursor }
    nodes{ isArchived
      content{ ... on Issue { number title state } }
      fieldValueByName(name:"Status"){
        ... on ProjectV2ItemFieldSingleSelectValue { name } } } } } } }"""
after = None
while True:
    body = json.dumps({"query": query,
                       "variables": {"owner": owner, "number": number, "after": after}})
    request = urllib.request.Request("https://api.github.com/graphql", data=body.encode())
    request.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(request) as answer:
        page = json.load(answer)["data"]["user"]["projectV2"]["items"]
    for item in page["nodes"]:
        card, status = item["content"], item["fieldValueByName"]
        if card:
            print(status and status["name"], card["number"], card["title"], sep="\t")
    if not page["pageInfo"]["hasNextPage"]:
        break
    after = page["pageInfo"]["endCursor"]
PY
```

An organisation-owned board is `organization(login:…)` rather than `user(…)`;
`tracker.owner_type` says which. Send GraphQL as a request body built in a
file or a program, never as an inline `"$(jq …)"`: the shell mangles it and the
error points at valid syntax.

```bash
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

Pushing as a machine account needs the token in the remote, not an SSH key:

```bash
git remote set-url origin https://x-access-token@github.com/owner/name.git
git -c credential.helper='!f(){ echo username=x-access-token; echo "password=$TOK"; };f' \
  push origin HEAD
```

Check what you are actually pushing to first — `git ls-remote --get-url origin`.
A global `url.<ssh>.insteadOf <https>` rewrites an HTTPS remote back to SSH
without saying so, and the push then lands as whoever owns the SSH key.
