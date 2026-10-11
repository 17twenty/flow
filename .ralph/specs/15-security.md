# 15 — Security

## 1. Trust boundaries
- Repo files are untrusted: no command execution from `.flow.yaml`; `token_cmd`
  only from user-owned TOML; a git-tracked `.flow.local.toml` is refused.
- Credential host binding: a credential is only sent to its provider's default
  API host or hosts listed in user config `[auth.<p>] hosts`. Otherwise prompt
  (interactive) or exit 77 with fix.
- CI: `pull_request` (never `pull_request_target`); no checkout of PR code is
  needed for `check pr`; least-privilege `permissions:` in templates.
- Agent PR content is untrusted like any PR; flow only reads metadata.

## 2. Redaction (`internal/core/redact.go`)
slog handler + error-string redactor: keys matching
`token|secret|password|authorization|api[_-]?key|cookie|session`; values matching
`gh[pousr]_…`, `github_pat_…`, `glpat-…`, `lin_api_…`, `ATATT…`, JWT-like; URL
query params `key|token|access_token|private_token`. Replacement `[REDACTED:<kind>]`.

## 3. Least privilege (dogfood defaults)
| Credential | Scopes |
|---|---|
| Actions `GITHUB_TOKEN` | `contents: read`, `pull-requests: write`, `statuses: write`, `issues: write`; release job adds `contents: write`, `deployments: write` |
| `FLOW_PROJECT_TOKEN` | classic PAT `project` (+ `read:org` only if org-owned) |
| Agent identity (GitHub App) | contents write, pull requests write, issues read; NOT administration |

## 4. Files, network
User config 0600; cache dir 0700. TLS always verified; `SSL_CERT_FILE`,
`HTTPS_PROXY` honoured. No telemetry.
