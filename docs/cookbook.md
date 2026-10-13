# Cookbook — the manual bits

flow automates what it can and *attests* what it cannot. This is the "cannot"
list: the steps a human performs once, by hand, before and around a flow
installation. Spec 17 §5 Stage A names them; this file is the recipe.

Everything here is done with `curl` and `jq` against `api.github.com`. `gh` is
not required and is deliberately not used — it is a GitHub-shaped tool, and the
point of keeping these recipes at the REST/GraphQL level is that the shape of
each step stays visible when the next forge adapter is written.

Conventions below: `$GH_TOKEN` is a token with admin on the repo (the human's),
`$BOT` is the machine user's token, `$REPO` is `owner/name`.

---

## 1. Two identities, and why

| | Account | Does |
|---|---|---|
| Human | `@17twenty` | Shapes issues, reviews and approves PRs, owns follow-ups, holds admin |
| Agent | `flow-bot-gh` | Commits, pushes, opens PRs, moves board items |

They must be different accounts. A PR author cannot approve their own PR, so
with one identity `agent.human_review` and `live.prs.reviewed` can never pass —
not "would be awkward", but *unsatisfiable*. flow's own gate would be a lie on
flow's own repo.

A **machine user** — an ordinary account used by software — is the portable
answer. A GitHub App is more precise (short-lived installation tokens, finer
permissions) and was declined anyway: Apps are a GitHub concept, and flow's
adapter boundary (spec 10) exists to stay thin enough to re-implement for
GitLab or Gitea. Those forges have machine users and tokens; they do not have
Apps. See spec 17 Decisions.

### 1.1 Create it

In the UI, as the machine user: sign up, verify a distinct email address.
Nothing scriptable, and nothing flow can attest beyond "a second login appears
as the author of these commits".

### 1.2 Its token

One classic PAT, scopes `repo`, `project`, `workflow`. This single token is
used for *both* the API and git push (§3), which is the property that makes the
setup forge-neutral.

Keep it out of the tree. This repo reads it from `.flow-bot-token`
(gitignored):

```
#flow-bot-gh
PAT_TOKEN=ghp_...
```

Verify what you have, before wiring anything to it:

```sh
BOT=$(sed -n 's/^PAT_TOKEN=//p' .flow-bot-token)
curl -s -D /tmp/h -H "Authorization: Bearer $BOT" https://api.github.com/user \
  | jq '{login,id,type}'
grep -i '^x-oauth-scopes' /tmp/h
```

The `id` matters: it is half of the noreply email in §4.

### 1.3 Repo collaborator

As the human:

```sh
curl -s -X PUT -H "Authorization: Bearer $GH_TOKEN" \
  https://api.github.com/repos/$REPO/collaborators/flow-bot-gh \
  -d '{"permission":"push"}'
```

The machine user must accept the invitation (`PATCH
/user/repository_invitations/<id>` with its own token, or the UI). Confirm from
the bot's side — `permissions.push` must be `true`:

```sh
curl -s -H "Authorization: Bearer $BOT" https://api.github.com/repos/$REPO \
  | jq '.permissions'
```

`push`, not `admin`: the agent should not be able to change the ruleset that
judges it.

---

## 2. The Project (tracker)

A user-owned Projects v2 board, Kanban template, Status options `Backlog ·
Ready · In progress · In review · Done` (spec 17 §1). Built-in workflows per
spec 17 §3 — these are UI-only, which is precisely why `flow attest` exists.

### 2.1 Project access is NOT repo access

This one bites. Adding the bot to the repository grants it nothing on a
user-owned Project. Its token sees:

```json
{"data":{"user":{"projectV2":null}},"errors":[{"type":"NOT_FOUND", ...}]}
```

`null` reads like "wrong project number" and sends you looking in the wrong
place. It means "not a collaborator on this Project".

Fix it with the human's token (UI equivalent: Project → Settings → Manage
access → Invite collaborators):

```sh
# Project node id and user node id
PID=$(curl -s -H "Authorization: Bearer $GH_TOKEN" -X POST https://api.github.com/graphql \
  -d '{"query":"query{user(login:\"17twenty\"){projectV2(number:2){id}}}"}' \
  | jq -r .data.user.projectV2.id)
BOTID=$(curl -s -H "Authorization: Bearer $GH_TOKEN" -X POST https://api.github.com/graphql \
  -d '{"query":"query{user(login:\"flow-bot-gh\"){id}}"}' | jq -r .data.user.id)

jq -nc --arg p "$PID" --arg b "$BOTID" '{
  query: "mutation($p:ID!,$b:ID!){updateProjectV2Collaborators(input:{projectId:$p,collaborators:[{userId:$b,role:WRITER}]}){clientMutationId}}",
  variables: {p:$p, b:$b}
}' > /tmp/mut.json
curl -s -H "Authorization: Bearer $GH_TOKEN" -X POST https://api.github.com/graphql \
  -d @/tmp/mut.json | jq -c .
```

Then verify as the bot — the same query must return the project, not `null`.

> **Send GraphQL from a file, not from an inline `$(...)`.** A payload built by
> command substitution and passed as `-d "$(...)"` came back as a parse error
> at a column that pointed at perfectly valid syntax; written to a file and
> sent with `-d @file`, the identical query succeeded. Shell quoting, not
> GraphQL. Debugging the query is debugging the wrong layer.

### 2.2 Board recipes

Status field and its options:

```sh
jq -nc '{query:"query{user(login:\"17twenty\"){projectV2(number:2){id
  fields(first:20){nodes{... on ProjectV2SingleSelectField{id name options{id name}}}}}}}"}' \
  > /tmp/q.json
curl -s -H "Authorization: Bearer $BOT" -X POST https://api.github.com/graphql -d @/tmp/q.json
```

Moving an item needs three ids (project, item, field) plus the option id —
`updateProjectV2ItemFieldValue`. Scripted in `/tmp/move.sh` during
construction; `flow` does it natively once `check pr` is live.

---

## 3. Push credential: a token over HTTPS

No SSH key for the machine user — on purpose. A token that works for both git
and the API is one secret to rotate and the same shape on every forge:
`https://x-access-token:$TOKEN@host/owner/repo`.

A **deploy key** is the tempting alternative and is a trap for this job: it is
bound to one repository and cannot call an API at all, so it can push the
branch but never open the PR. Two credentials, one identity, one of them doing
half the job.

Wire git to read the token file (repo-local, so the secret never enters
`~/.gitconfig`):

```sh
git config --local remote.origin.url 'https://x-access-token@github.com/17twenty/flow.git'
git config --local credential.helper \
  '!f() { test "$1" = get && sed -n "s/^PAT_TOKEN=/username=x-access-token\npassword=/p" /abs/path/.flow-bot-token; }; f'
```

### 3.1 Two ways this host pushes as the wrong human, silently

Both were hit for real. Both succeed, and both report nothing.

1. **`~/.ssh/config` beats `IdentitiesOnly=yes`.** A `Host github.com` block
   with an `IdentityFile` supplies the human's key even when the git command
   names a different one. Only `-F /dev/null` excludes it.
2. **`url.<base>.insteadOf` rewrites the remote after the fact.** This host has
   `url.git@github.com:.insteadOf = https://github.com/` in `--global`, so an
   HTTPS remote is quietly converted to SSH — the credential helper is never
   consulted and the push goes out over the human's key. The giveaway is the
   push output saying `To github.com:owner/repo.git` (scp syntax) when the
   remote was configured as `https://`.

   The fix is a URL the rewrite cannot match: `https://x-access-token@github.com/...`
   does not start with `https://github.com/`.

Belt and braces — make SSH fail loudly rather than impersonate:

```sh
git config --local core.sshcommand \
  "sh -c 'echo >&2 \"SSH is not an identity here - push over HTTPS\"; exit 1' --"
```

### 3.2 Verify the credential, do not trust it

Ask git which secret it would actually hand over, and compare hashes (never
print the token):

```sh
printf 'protocol=https\nhost=github.com\nusername=x-access-token\n\n' \
  | git credential fill | sed -n 's/^password=//p' | shasum | cut -c1-12
sed -n 's/^PAT_TOKEN=//p' .flow-bot-token | shasum | cut -c1-12
```

Equal means the bot's token is what leaves the machine. This matters because a
system-level helper (`osxkeychain`, configured in
`/opt/homebrew/etc/gitconfig`) may hold the human's credential for the same
host and is consulted first.

Cheap end-to-end check: push a throwaway branch and read the actor back.

```sh
git push origin HEAD:refs/heads/identity-probe
curl -s -H "Authorization: Bearer $BOT" "https://api.github.com/repos/$REPO/events?per_page=10" \
  | jq -c '[.[]|{type,actor:.actor.login}][0:4]'
git push origin --delete identity-probe
```

(The events feed is cached for about a minute, so a fresh push may not appear
at once. The `git credential fill` comparison is the authoritative check.)

---

## 4. Commit authorship

Authorship is data in the commit; the push credential is transport. They are
independent, and both must name the agent.

```sh
git -c user.name='Flow Bot' \
    -c user.email='341252896+flow-bot-gh@users.noreply.github.com' \
    commit -m 'feat(scope): ...'
```

The `<id>+<login>@users.noreply.github.com` form is what makes GitHub attribute
the commit to the account — avatar, profile link, contribution graph. Any other
address leaves an unlinked name, and `agents.authors` matching gets murky.

Confirm after pushing:

```sh
curl -s -H "Authorization: Bearer $BOT" https://api.github.com/repos/$REPO/commits/<sha> \
  | jq '{author:.author.login, committer:.committer.login, name:.commit.author.name}'
```

`author.login` must be `flow-bot-gh`. If it is `null`, the email is wrong.

---

## 5. Repository settings

Squash-only merges, PR title as the commit title (spec 17 §1):

```sh
curl -s -X PATCH -H "Authorization: Bearer $GH_TOKEN" https://api.github.com/repos/$REPO \
  -d '{"allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false,
       "squash_merge_commit_title":"PR_TITLE","squash_merge_commit_message":"PR_BODY",
       "delete_branch_on_merge":true}'
```

Labels `agent`, `blocked`, `incident`, `flow-followup`, `phase` — `POST
/repos/$REPO/labels`.

The **ruleset** on `main` requiring `flow/policy` and the build is *not*
manual: that is `flow apply` (spec 06). Flow's own gate comes from
`policy.status_context`; the build comes from `forge.required_checks`, which
is where you name the contexts the trunk must pass besides flow's. Name them
as the forge reports them — this repository's build is the check run `test`,
not the `ci / test` the web UI composes out of the workflow and the job. If
you are creating the ruleset by hand, flow has failed at its own job.

---

## 6. What stays manual forever

Not everything can be reconciled. These are attested with `flow attest
<capability> --note ...`, which records a human's word with a timestamp and
lets `flow doctor` distinguish "verified" from "asserted" (spec 07 §8):

- Projects v2 **built-in workflows** (item added → Backlog, PR merged → Done,
  …) — no API exposes them.
- Project **views** and column limits.
- The machine user's existence, its email, and its 2FA.
- Org/enterprise policy that sits above the repo.

The honest summary: creating accounts and clicking Project workflow toggles
cannot be automated today. Everything else on this page is a one-off `curl`
that flow either performs itself or checks.

### 6.1 The four this repository owes

`flow doctor` on `17twenty/flow` reports these as `attest`, and an unattested
check is worth exit 2 (spec 07 §2), so the Stage C streak cannot start until a
human has looked and said so:

```sh
flow attest tracker.native.pr_merged          --note "Project workflow 'Pull request merged' is on and sets Status: Done"
flow attest tracker.native.issue_closed       --note "Project workflow 'Item closed' is on and sets Status: Done"
flow attest tracker.native.changes_requested  --note "Project workflow 'Code changes requested' is on and sets Status: In progress"
flow attest tracker.expedite                  --note "<what the board does with expedited work, and how you checked>"
```

Read each one before running its line — an attestation is a person's word,
not a way to make a check quiet (spec 07 §8). There is no URL for the
workflows panel: it is an overlay, not a page. Open
<https://github.com/users/17twenty/projects/2>, click the **…** menu at the
top right, and choose **Workflows**.

Each command writes one entry to `.flow.lock`, which is committed, so they
land through a pull request like anything else and the word stands for thirty
days.

A note on what this costs: `live.transitions.native` already watches these
same workflows behave on real tickets, so the attestation is the setup half of
a fact the live half re-checks every day. That is the design — flow is allowed
to take your word for the switch, and never for the outcome.
