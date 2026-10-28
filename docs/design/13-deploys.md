# 13 — Deployment Signals

flow never talks to clusters or deploy tools. It reads **deployment records the
forge holds about the app repository**, written by whatever deploys.

## 1. Contract with deployers

A deployer (release workflow, Argo CD, Flux, anything) MUST record, on the
**application repository at the application commit SHA**:
- preferred: a forge Deployment for environment `deploys.environment` with a
  `success` deployment status (`source: forge_deployments`), or
- a commit status with a configured context and state success
  (`source: commit_status`).

No message parsing, no image-tag parsing (P12).

## 2. Patterns (documented in `assets/docs/deploy-patterns.md`)

- **CLI/library (flow itself)**: the release workflow, after publishing a
  release for SHA X, creates Deployment(env=production, ref=X) + status success.
  `done_on: merged` + `max_lag` proves every merge was released (spec 17).
- **Single repo + GitOps**: Argo CD/Flux sync the app repo path; Argo CD
  notifications report on the synced revision (= app SHA).
- **Separate GitOps repo**: the bump step writes the app repo and SHA into
  structured fields on the Argo CD Application (annotations
  `flow.dev/app-repo`, `flow.dev/app-revision`); the notification template's
  repo URL and revision are templated from those annotations, so Argo CD reports
  against the app commit. (Field names in notification templates vary by Argo CD
  version — templates in `assets/templates/gitops/` must state the tested version.)

## 3. Reading

GitHub: `GET /deployments?environment=<env>` (newest first) + latest status per
deployment; GitLab: `/deployments?environment=&status=success`; commit_status:
statuses on default-branch commits matching contexts. "Deployed SHA at time T" =
newest successful record ≤ T. Ancestry (`merge_sha ⊑ deployed_sha`) via forge
compare API, cached per pair in `FLOW_CACHE_DIR`.

## 4. Doctor

`deploys.source` (setup), `live.deploys.recent`, `live.deploys.lag`,
`live.tickets.done_deployed` (spec 07 §6). Missing records → remediation
`manual` with the pattern for the detected setup, owner `followups.owners.platform`.

## Decisions

- **§3 "inactive" is not a state (item 3.6).** GitHub writes an `inactive`
  deployment status on a record that a later deployment superseded, so the
  newest status on a year-old success says `inactive` and nothing else does.
  Reading it as the state would turn every past success into a non-success and
  make "what was production running at time T" unanswerable for every T but
  now. The state is therefore the newest status that is not `inactive`.
- **§3 a deployment nothing has reported on is pending (item 3.6).** A record
  with no status at all has not started, which is what `pending` already
  means, so it needs no fifth state of its own.
- **§3 a state flow does not understand is a refusal (item 3.6).** Guessing
  would either hide a deployment or invent one. The adapter refuses with
  `ErrUnavailable` naming the word it saw, as §2's ancestry already does for a
  comparison status it cannot read.
- **§3 the environment and the commit are the provider's filters, the window
  is the adapter's (item 3.6).** GitHub's deployments endpoint takes
  `environment` and `sha` but has no `since`, so the walk sends the first two
  and stops at the first record older than the window — which the provider's
  newest-first order makes the last one worth reading.
- **§3 the evidence comes from the status, falling back to the record (item
  3.6).** The status carries the line the deployer left and the address of the
  run behind it, so `URL` is its `log_url` (or `target_url`) and `Description`
  its description; the deployment record's own are used when the status left
  them empty. `UpdatedAt` is when the status was written, because that is when
  the state last changed.
- **§3 the ancestry cache is `core.Ancestry`, and only object names are
  remembered (item 3.6).** The engine's deployment checks need it and may not
  import an adapter (spec 01 §2), so it lives in core, in front of a
  `core.Comparer` — the one method of `Forge` the question needs. A pair of
  object names is remembered for good, since a commit's parents never change;
  a branch or a tag names whatever it points at today and is asked every time;
  and neither a refusal nor an ancestry flow does not understand is written,
  because neither is an answer.
- **§1 one capability declaration covers every deployment source (item 3.6).**
  `assets/capabilities/deploys.yaml` is named `deploys`, not after a provider:
  a deployment record says the same thing whichever forge holds it, and what
  flow may do with one — read it, never write it — does not change either.
- **§1 the record is the evidence, and the event is only a prompt (item 4.4).**
  `check deploy` is started by a CI event or by a person typing `--sha`, and
  neither of those is a deployment: both are claims about one. So the first
  thing the run does with a commit and an environment is ask the deployment
  source whether a successful record for that pair exists, and a run that
  cannot find one moves nothing and says so. This makes the answer the same
  whoever started the job, and makes a failed or half-finished deployment
  answer no for free.
- **§3 a merge with no merge commit is not running (item 4.4).** A forge may
  report a pull request as merged without saying what the merge produced.
  There is then nothing to compare the deployed commit with, so flow claims
  nothing and asks nothing — the alternative, treating "merged recently"
  as "merged into this", would finish work on somebody else's deployment.
- **§3 the ancestry cache is worth less than the deployment (item 4.4).** A
  machine with nowhere to keep the answers between runs gets an uncached
  `core.Ancestry`, not an error. The cache saves requests; a deployment is
  not worth refusing over a cache directory that could not be created.
- **§2 `max_lag` is everyone's objective, not just a deployed-done team's
  (item 5.2).** `done_on` decides when a ticket is finished; `max_lag` decides
  how long work may sit merged and unreleased. A team that calls work done on
  the merge still holds itself to the second, so the live-health audit asks it
  of every workflow that records deployments.
- **§3 the audit compares through the cache `flow check deploy` keeps
  (item 5.2).** Same directory, same names. A fortnight of merges compared
  afresh every morning is the same question put to the forge every day for
  answers that cannot change, and a second cache of its own would halve the
  value of both. A run with nowhere to keep them asks the forge directly: a
  report is not worth failing over a cache directory.
- **§3 a deployment is dated by when it succeeded (item 5.2).** A release that
  takes an hour took an hour. The record keeps both moments, and dating one by
  its creation would call a pipeline stopped while it was still finishing.
- **§3 a rollback does not un-ship what was released on time (item 5.2).** The
  question is whether some deployment at or before the deadline contained the
  merge, not whether the newest one does. A deployment that took production
  back to an earlier commit did not undo the release, and reading only the
  latest record would blame a team for recovering from an incident.
- **§3 a commit status speaks for one environment, and the contexts are what
  it speaks with (item 7.5).** A forge deployment record names its
  environment; a commit status has no field for one. The configured contexts
  are therefore the whole of what "deployed to production" means to this
  source, and the source is built for the one environment they stand for. A
  query for another is answered with nothing, without asking the provider
  anything, because that is the same answer as an environment nothing deploys
  to — and the record that comes back is spelled the way configuration spells
  it, not the way the caller asked.
- **§3 a context is a name, and only a trailing star is a pattern (item
  7.5).** `deploys.contexts` takes exact names or `prefix*` (spec 03 §1), so
  `argocd/*/prod` matches the context literally called `argocd/*/prod` and
  nothing else. Teaching it globbing would make a configuration that works
  today mean something else tomorrow, for no case anybody has. Matching is
  byte-exact for the same reason: `argocd/App-Prod` and `argocd/app-prod` are
  two different checks to the forge, and flow is in no position to decide they
  are one.
- **§3 a context's history is one deployment, not several (item 7.5).** A
  deployer writes `pending` and then `success` under the same context, and
  GitHub keeps both. Reading each status as a record would stand an unfinished
  deployment beside the finished one that replaced it. The newest status per
  context is the deployment, which is the same rule the deployments source
  already applies to a record's status history. Two *different* matching
  contexts on one commit are two deployments, because two deployers reported.
- **§3 the window selects commits, because statuses cannot be asked for one
  (item 7.5).** GitHub serves the commits of a window and offers no way to ask
  which statuses were written in one, so the only bounded walk is over the
  trunk's commits and the statuses hanging on them. A status is always written
  on a commit that already exists, so a commit inside the window carries every
  status that could date from inside it. What the rule costs is a deployment
  of an older commit — a rollback — which is outside the window the question
  named; the deployments source, which can filter server-side, does not share
  the limitation.
- **§3 a walk nothing bounds reads the trunk's newest hundred commits (item
  7.5).** Statuses are read one commit at a time, so an unbounded walk is one
  request per commit of the repository's history. The hundred newest is the
  provider's own page. A limit stops the walk early, since it goes newest
  commit first, and a query naming a commit walks nothing at all: that commit
  is the only one whose statuses are about it.
- **§3 the trunk is the repository's to name (item 7.5).** Configuration has
  no key for the default branch (spec 03 §1) and the repository already knows
  which branch it is, so an unnamed trunk is asked for once and remembered. A
  repository that answers with no default branch is `ErrUnavailable`, not a
  branch called "", which would list the commits of nothing and report that
  nothing had ever shipped.
- **§3 a commit status has four states and none of them is running (item
  7.5).** `success`, `failure`, `error` and `pending` are all a commit status
  offers, so a deployment reported this way has no in-progress state to
  report; `pending` covers both "not started" and "under way". A state flow
  does not understand is refused by name, as the deployments source already
  refuses one.
- **§1/§3 a commit status source reads a forge, not a provider (issue
  #129).** It needs three things — the trunk's name, the trunk's commits and
  one commit's statuses — and `core.Forge` answers all three, so the source
  holds a forge adapter instead of a provider's API and a credential of its
  own. `source: commit_status` is therefore available on every forge flow
  speaks, which is what a forge keeping no deployment records of its own
  needs (spec 10 §3). A deployment source holding a forge is not an adapter
  importing another adapter package (spec 01 §2): the dependency is on
  `core.Forge`, which is core's.
- **§3 collapsing a context's history belongs to the adapter (issue #129).**
  `core.Forge.CommitStatuses` answers with the latest verdict per context, and
  the forge contract proves that of every adapter, so this source filters by
  context and does not dedupe again. Two mechanisms answering the same
  question in different code drift apart silently.
- **§3 the states are core's words, not GitHub's (issue #129).** What arrives
  is a `core.CommitStatus`, whose `State` is one of core's four, so the map
  onto deployment states is written against `core.StatusSuccess` and its
  three companions. GitHub's spelling of them is the GitHub adapter's
  business, and the wire types that held it have gone.
- **A merge no longer on the trunk is not late (#278).** After
  flow-demo-web's `main` was reset, `live.deploys.lag` went on reporting a
  merge from before the reset as "still not running in production"; no
  branch the deployer ships holds it, so it never will be. A merge that
  would otherwise be a miss is first asked whether the trunk still contains
  it — straight from the forge, because the ancestry cache is keyed by
  commits that never change and the trunk is a name that does — and one the
  trunk does not contain (diverged, or a commit the forge has forgotten) is
  left out, with a line saying how many. Merges that are on time are never
  asked, so a healthy run costs nothing more.
- **§1 init writes a source the forge can be read through (#295).** Every
  preset ships `source: forge_deployments`, but GitLab and Gitea declare
  `deployments_read` unverifiable and the only reader of that source is
  GitHub's, so a repository on either had doctor's deploy observation fail
  on day one and take every planned check down with it, with nothing naming
  deploys.source. init now reads the forge's declaration — not a list of
  forge names, so a forge added later follows — and where it cannot read
  deployment records writes `source: commit_status` with
  `contexts: ['deploy/<environment>']`, and prints that it did and what the
  deployer must post. `commit_status` rather than `none`, because `none` is
  invalid under deploy-done (`done_on: deployed` needs a source, spec 04 §6)
  and would have meant deleting the environment and lag a team picked the
  preset for; and a named context rather than a `${pending:…}` placeholder,
  because placeholders are only allowed where doctor has a way to find the
  answer, and no API says which status a deployer means as "deployed". The
  name is the contract flow states, not a guess at the deployer's: a team
  already posting another changes it once. There is no `--deploy-source`
  flag to override this (spec 02 §2 Decisions); the file is the override.
  The other half is semantics: `forge_deployments` on a forge whose
  declaration cannot read them is refused by config.semantics, naming
  `commit_status` and `none`, in the same way a tracker that cannot see a
  ticket's branches refuses `branch_created` — a fact about the provider is
  wrong when written, not when doctor first observes it.
