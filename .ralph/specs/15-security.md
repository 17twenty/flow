# 15 — Security

## 1. Trust boundaries
- Repo files are untrusted: no command execution from `.flow.yaml`; `token_cmd`
  only from user-owned TOML; a git-tracked `.flow.local.toml` is refused.
- Credential host binding: a credential is only sent to its provider's default
  API host or hosts listed in user config `[auth.<p>] hosts`. Otherwise prompt
  (interactive) or exit 77 with fix.
- CI: `pull_request` (never `pull_request_target`); no checkout of PR code is
  needed for `check pr`; least-privilege `permissions:` in templates, granted
  per job rather than per file so that only the gate may arm a merge.
- Agent PR content is untrusted like any PR; flow only reads metadata.

## 2. Redaction (`internal/core/redact.go`)
slog handler + error-string redactor: keys matching
`token|secret|password|authorization|api[_-]?key|cookie|session`; values matching
`gh[pousr]_…`, `github_pat_…`, `glpat-…`, `lin_api_…`, `lin_oauth_…`, `ATATT…`,
JWT-like; URL query params `key|token|access_token|private_token`.
Replacement `[REDACTED:<kind>]`.

## 3. Least privilege (dogfood defaults)
| Credential | Scopes |
|---|---|
| Actions `GITHUB_TOKEN` | `contents: read`, `pull-requests: write`, `statuses: write`, `issues: write`; the gate job adds `contents: write` to arm auto-merge; release job adds `contents: write`, `deployments: write` |
| `FLOW_PROJECT_TOKEN` | classic PAT `project` (+ `read:org` only if org-owned) |
| Agent identity (GitHub App) | contents write, pull requests write, issues read; NOT administration |

## 4. Files, network
User config 0600; cache dir 0700. TLS always verified; `SSL_CERT_FILE`,
`HTTPS_PROXY` honoured. No telemetry.

## Decisions

- **§2 `<kind>` in `[REDACTED:<kind>]`.** The spec names the replacement but not
  the vocabulary. `<kind>` is the credential shape that matched:
  `github_token` (`gh[pousr]_`), `github_pat`, `gitlab_token`, `linear_key`,
  `atlassian_token`, `jwt`. When only the *key name* or a *URL parameter name*
  identified the value as sensitive — the shape being unknown — the kind is
  `secret`. This keeps the marker informative for debugging without revealing
  anything about the value itself.
- **§2 URL parameter matching.** The listed names (`key|token|access_token|
  private_token`) are matched as suffixes of the parameter name, so `api_key`
  and `private_token` are both caught. Matching is case-insensitive.

- **§1 what approves a host (item 7.6).** Two things, and nothing else: the
  API host flow itself ships for that provider kind, and a host the person
  running flow listed under `[auth.<provider>] hosts`. `.flow.yaml` names the
  URL and the repository can change it, so the file being read cannot be what
  approves the host it names — a configuration that could nominate where its
  credential may be sent would approve itself. A kind flow ships no host for
  approves nothing rather than everything, which is what makes adding the
  next adapter safe by default.
- **§1 the question comes before the credential (item 7.6).** Where a
  credential may go is settled before it is asked for. A `token_cmd` unlocks
  a vault or asks for a fingerprint, and running one to produce a secret flow
  has already decided it will not send puts a question to somebody for
  nothing. The visible consequence is that a run against an unapproved host
  with no token configured reports the host, not the missing token.
- **§1 an answer given once lasts for the run (item 7.6).** An interactive
  yes is remembered for the process, so two providers on the same host are
  not two identical questions, and it goes no further: flow does not write to
  the user's configuration behind their back. The refusal it would otherwise
  print carries the exact line that makes the answer permanent, so saying yes
  and writing it down are one decision with two durations.
- **§1 an approval is of one credential (item 7.6).** It is keyed by provider
  and host together. Agreeing to send the forge's token to a host says
  nothing about the board's, even when both answer at the same name: they are
  two secrets, and the whole point is which one goes where.
- **§1 the approval binds the client, not the request (item 7.6).** What was
  approved is a host, so the client built for it refuses every request naming
  another — a redirect included, before any packet leaves, because a redirect
  off the host would carry the credential with it. `core.ClientOptions`
  `AllowHost` is that binding, and it can only narrow: inside a test binary
  the guard of spec 14 still refuses a non-loopback host whatever a caller
  allows, so a forgotten fake cannot reach a real provider because production
  code allowed its host.
- **§1 host names are compared by name alone (item 7.6).** Case-folded,
  without a port: `.flow.local.toml` may carry a hosts list but a host with a
  colon in it is already refused as malformed, so a port was never part of
  what anybody approved.
- **§1 refusing says which file to edit (item 7.6).** Both refusals — nobody
  was there to ask, and somebody said no — exit 77 and carry the line to
  write and the file to write it in, which is the user's own config and never
  the repository's. They differ only in the reason, because "you said no" and
  "nobody could be asked" are different situations with the same remedy.
- **Every credential shape an adapter accepts is a shape redaction knows
  (issue #168).** Linear spells a personal API key `lin_api_…` and an OAuth
  access token `lin_oauth_…`, and item 7.2 taught flow to send both; only the
  first was ever redacted, so the one flow had to treat specially in
  `Authorization` was the one it would have logged. The value patterns above
  are the list, and an adapter that learns a new credential shape adds it
  there in the same change.

- **§1 and §3 the gate holds `contents: write`, and no other job does
  (issue #190).** Arming a merge is arranging a write to the trunk, so GitHub
  refuses `enablePullRequestAutoMerge` to a token that may only read. The
  whole of the auto-merge the gate learned in #167 was therefore dead from
  the day it was written, and silently: arming is refused exactly as it is
  refused for a fork's run, which flow records and forgives, so an approved
  and green pull request simply went on waiting for a person. The permission
  is granted on the gate's job and nowhere else — a job's `permissions:`
  block replaces the file's whole set, which is what makes a per-job grant
  possible — so the jobs that only read a tree still only read it.
- **§3 a credential of its own was considered and refused (issue #190).** The
  daily health job already borrows `FLOW_PROJECT_TOKEN` where the Actions
  token is incapable, and arming could have followed it. It does not, because
  the two cases are not alike: the health job borrows a credential for a read
  GitHub will not grant a workflow at all, while arming is an ordinary write
  a workflow may hold. Putting it behind a secret would make the feature work
  here and nowhere an adopter had not first made a personal access token —
  a default that is off for everyone who did not read the documentation. The
  cost is stated rather than hidden: the gate, which otherwise only reads,
  may ask the forge to land a pull request, and the comment beside the
  permission says so.
