# Deploy patterns: telling flow what is in production

flow reads deployment records on the **application repository at the
application commit**. It never parses commit messages or image tags and never
talks to your cluster. Pick the pattern that matches how you ship.

| Pattern | Who records the deployment | flow config |
|---|---|---|
| CLI / library (e.g. flow itself) | Release workflow, after publishing: Deployment(env=production, ref=SHA) + success | `deploys.source: forge_deployments` |
| App + manifests in one repo, Argo CD / Flux | Argo CD notifications (revision = synced app SHA) | `forge_deployments` or `commit_status` |
| Separate GitOps repo | Bump step annotates the Application with app repo + SHA; Argo CD notifications report against them (`assets/templates/gitops/argocd-app-annotations.yaml`) | `forge_deployments` |
| Anything else (scripts, Spinnaker, …) | Call the forge Deployments API with ref=app SHA, or post a commit status with a fixed context | either |

## Commit statuses as deployment records

`deploys.source: commit_status` reads the statuses on your trunk's commits.
`deploys.contexts` names the ones that mean "deployed", exactly or by prefix:

```yaml
deploys:
  source: commit_status
  environment: production          # what the contexts stand for
  contexts: ['argocd/app-prod*']   # exact names, or one trailing * for a prefix
```

A commit status carries no environment of its own, so the contexts are what
stand for one. Only a trailing `*` is a pattern — `argocd/*/prod` matches a
context called exactly that — and the comparison is case-sensitive, because
the forge treats two spellings as two checks. Where one context says
`pending` and then `success`, the newest word wins; two different matching
contexts on one commit are two deployments.

Because a forge can be asked for the commits of a time window but never for
the statuses of one, this source looks at the trunk's commits in the window
(at most the hundred newest) and the statuses on them. A deployment of a
commit older than that — a rollback to an old release — is outside what it
can see. If your forge has a deployments API, prefer `forge_deployments`.

Then choose what "Done" means:
- `done_on: merged` + `deploys.max_lag` — Done on merge; doctor proves every
  merge shipped within the SLO (recommended when every merge auto-deploys).
- `done_on: deployed` — Done when running; `flow check deploy` (on
  `deployment_status` events) moves tickets; doctor catches up misses.
