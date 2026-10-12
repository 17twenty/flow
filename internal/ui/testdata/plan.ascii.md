## flow plan

forge `github` - tracker `github-projects` - deploys `github-deployments`

| | item | plan | why |
|---|---|---|---|
| + | `forge.branch_protection` | native | main is unprotected and the credential may protect it |
| ~ | `tracker.columns` | compensated -> a saved view | the board cannot name its own columns through the API |
| ! | `tracker.automation` | follow-up -> @platform | the credential may not change this board's workflow |
| * | `tracker.saved_view` | attest -> flow attest tracker.saved_view | no API reports whether the view exists -- a human must say |
| x | `deploys.environments` | unsupported | this deployment source has no environments to configure |
| . | `forge.pr_template` | up to date | the template is already what the configuration asks for |

**`forge.branch_protection`**

- `update` "main" required_checks="flow/policy" reviews="1"
- hazard: rewriting protection clears any rule flow does not send

6 items - 1 up to date - 1 native - 1 compensated - 1 follow-up - 1 attest - 1 unsupported
