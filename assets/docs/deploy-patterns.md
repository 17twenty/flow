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

Then choose what "Done" means:
- `done_on: merged` + `deploys.max_lag` — Done on merge; doctor proves every
  merge shipped within the SLO (recommended when every merge auto-deploys).
- `done_on: deployed` — Done when running; `flow check deploy` (on
  `deployment_status` events) moves tickets; doctor catches up misses.
