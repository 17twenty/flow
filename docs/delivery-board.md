# The delivery board, verified

The proposal this tool was built from is
[`enhanced-delivery.md`](https://gist.github.com/17twenty/136ef4e6dd6125743079a0f96fad5c86)
— *Lean Kanban Workflow (Proposal)*. It describes two boards. This document
takes the **delivery board** (§5–§12 of the proposal), line by line, and says
what in flow makes each line true, or that nothing does.

It is checked by docs_test.go: every configuration key, policy rule and
doctor check named here must exist in the binary, and the configuration in
§"A delivery board, as flow" must parse and validate. A document that drifts
away from the code fails the build rather than quietly misleading somebody.

The proposal describes two boards. flow implements one of them, and §4
below says whose the other is and why it stays theirs.

---

## §4 The product board, and why it is not flow's

The proposal's first board carries an epic from a problem somebody noticed
to a feature every user has. **flow does not model it, and that
is a decision rather than an omission** — spec 00 §2 lists it among the things
flow is not, and the Decision at the end of that spec says why.

The why is in the proposal's own exit rules, each of which names its owner:

| Proposal §4.2: exit rule | Who can answer it |
|---|---|
| Idea → Discovery: the product owner agrees to investigate | a person |
| Discovery → Shaped: the functional requirements are written and agreed | a person |
| Shaped → Building: stories exist on the delivery board, and the flag is named | a person splitting the work; `flag_if_user_facing` then holds the name on each story |
| Building → Rolling out: all stories are Done | **the delivery board**, which flow keeps true |
| Rolling out → Live: the flag is on for 100% of users and the metrics are acceptable | a flag service and a metrics tool |
| Live: the flag is removed from the code | the code, which flow does not read |

The seventh column, Dropped, has no exit rule at all: it is where a decision
goes, and no event implies it.

One of the six is a fact flow holds. The rest belong to a product owner, a
flag service and a metrics tool — three things spec 00 §2 already says flow is
not. flow's fifth principle is *enforce in the forge, display in the
tracker*: it shows a column because something it owns makes the column true.
On the product board it would own nothing and display everything.

So flow supplies the facts and leaves the column to the people who own the
decision:

- **"All stories are Done"** is the delivery board, which flow moves on the
  forge's own events and audits with `live.tickets.done_merged` and
  `live.tickets.done_deployed`. An epic is a ticket's parent, and its stories
  are the tickets that name it.
- **"An epic cannot move to Live while a cleanup ticket is open"** is the
  cleanup ticket `live.flags.cleanup` files from the `Flag-Added:` trailer.
  flow opens it, never closes it — it cannot read the code — and
  `live.flags.age` turns an old one into a finding. The fact is flow's; the
  column that shows it is not.
- **"The flag is on for 100% of users"** is a flag service's to answer, and
  flow is not one (spec 00 §2).

A team that wants the product board in the same tool makes it a second
project and moves it by hand, or with its tracker's own automation. flow's
`workflow.columns` describe the board that Git events move; there is no
second workflow and no second project in the configuration, which is the
shape of this decision in the schema.

## §5.2 Columns

| Proposal | flow | Where |
|---|---|---|
| Backlog — work that is not ready, moved by a person | a column with `category: todo` | `workflow.columns` |
| Needs Design — optional, moved by a person | a column with `category: todo`; flow never moves work into or out of it | `workflow.columns` |
| Ready — clear acceptance criteria, moved by a person (the gate) | the column carrying `gate: definition_of_ready` | `workflow.columns[].gate` |
| In Progress — a branch exists, moved by automation, WIP 2 per person | a per-person limit on the column, warned on every gated pull request and audited daily by `live.wip` | `workflow.columns[].wip.per_person` |
| In Review — a PR is open, moved by automation, WIP 2 × reviewers | a limit on the column; flow does not multiply it by anything, because it cannot count your reviewers | `workflow.columns[].wip.limit` |
| Done — merged **and** synced to production, moved by automation | `workflow.done_on: deployed` | `workflow.done_on` |

Backlog → Ready is nobody's job in flow: it judges readiness and never
promotes. That is the proposal's "the gate" — a person decides, and
`ticket.ready` refuses the pull request if they decided wrongly, as long as
the work has not already started. Once a ticket is in a column past the gate
the rule is skipped rather than judged: the Definition of Ready was the entry
gate, and re-litigating it on every pull request would make a late discovery
look like a policy failure.

## §5.3 Definition of Ready

| Proposal | flow | Where |
|---|---|---|
| Acceptance criteria you can write as tests | `acceptance_criteria` | `definition_of_ready.rules` |
| Links to its epic | `parent_link` | `definition_of_ready.rules` |
| States the feature flag name, if user-visible | `flag_if_user_facing` | `definition_of_ready.rules` |
| Design attached, if it has a UI | `design_if_ui` | `definition_of_ready.rules` |
| Small: one person or agent finishes it in 1–2 days | `definition_of_ready.max_size: 2d` | `definition_of_ready.max_size` |

The size rule only bites where the tracker records an estimate flow can read
as a duration. Jira's is a duration; GitHub Projects has no estimate field
flow reads; Linear's is a number of points, whose meaning each team settles
for itself, so flow leaves it empty rather than inventing an hour. Where there
is no estimate the rule passes rather than failing work it cannot measure.

## §5.4 Definition of Done

| Proposal | flow | Where |
|---|---|---|
| The author wrote tests | — | nothing checks this; CI is where a team enforces it |
| CI passes | the required status check, and the audit that it was green **when it merged** | `forge.required_checks`, `live.prs.gated` |
| One person reviewed and approved | `agent.human_review` for machine-written work; `live.prs.reviewed` for the audit | `agents.require_human_review` |
| Merged to main | `pr_merged` | `workflow.transitions` |
| GitOps synced to production | `deployed`, proven by the deployment record | `workflow.done_on`, `deploys.source` |
| Behind a flag, if not ready for users | `flag_if_user_facing`, and the cleanup ticket | `live.flags.cleanup` |

`agent.human_review` asks for a person's approval of work a machine wrote. It
does not ask for one on every pull request, because flow's own rule is that
the forge enforces review (`forge.protection`) and the gate proves what the
forge could not. A team that wants the proposal's wording exactly sets a
required approving review count in branch protection, which `flow apply`
writes and `forge.protection` audits.

## §5.5 Rules

| Proposal | flow |
|---|---|
| One workflow for stories, tasks and bugs | flow has one workflow; a ticket's type is not read anywhere |
| No sub-tasks | flow reads a ticket's **parent** (the epic) and never its children |
| No "Story Bugs" | nothing in flow models one |
| No fix versions or release windows | nothing in flow models one; `deploys.max_lag` is an SLO, not a window |
| No sprints | nothing in flow models one |

## §6 Automation: Git moves the tickets

| Proposal: Git event | Proposal: transition | flow |
|---|---|---|
| Branch created with ticket key | Ready → In Progress | `branch_created`, on a tracker that keeps the link itself. Not on this one: Jira's development information has no public API, so the configuration below starts at the pull request |
| PR opened | In Progress → In Review | `pr_ready_for_review` |
| PR changes requested | In Review → In Progress | `changes_requested` |
| PR merged **and** production sync succeeds | In Review → Done | `deployed`, with `workflow.done_on: deployed` |
| Production sync fails | the ticket stays in In Review | flow moves nothing until the deployment record exists, and `live.deploys.lag` says how long it has been waiting |

Not *with ticket key*, though. flow performs `branch_created` from the
tracker's own link between a ticket and its branches — `Issue.linkedBranches`
on GitHub, written by "Create a branch" and by `gh issue develop` — and never
from the branch's name, which would be the guess P12 refuses with the
configuration taken away. A tracker that keeps no such link may not configure
the event at all, so this is one of the two rows where the proposal's board
and this tracker disagree, and the disagreement is refused at `flow init`
rather than discovered at six in the morning.

The proposal puts the ticket key in the branch name and the PR title. flow
reads both, plus the header a squash will land, and never a pattern somebody
configured: the key's shape belongs to the tracker (`PROJ-123` for Jira,
`ENG-12` for Linear, `#12` for GitHub). On GitHub it prefers what the forge
says the pull request closes, which is a structured link rather than a guess.

## §7 Blocked work

| Proposal | flow |
|---|---|
| No Blocked column | flow has no blocked column and the preset does not offer one |
| Use the flag on the card | `workflow.blocked.label` — a label is what GitHub Projects and Linear have; Jira's flag is a label to flow |
| Add a comment saying why, what unblocks it, and who monitors it | `workflow.blocked.require_comment`, read by `live.tickets.blocked_explained`: an open blocked ticket needs a comment somebody other than flow wrote, and the remediation asks for the three things |
| The card stays in its column | flow never moves blocked work; `ticket.not_blocked` warns on the pull request and `not_blocked` refuses the Definition of Ready |

## §8 Urgent work

| Proposal | flow |
|---|---|
| Expedite swimlane | `workflow.expedite.labels` — a label, because a swimlane is a view and flow does not make views |
| Expedite work ignores WIP limits | the WIP rules skip expedited tickets |
| One expedite item at a time per team | `workflow.expedite.max: 1` |

## §9 Feature flags

| Proposal | flow |
|---|---|
| A PR that adds a flag gets a cleanup ticket | `live.flags.cleanup` reads the `Flag-Added:` trailer on trunk commits and files one per flag, once, found again by its marker |
| Each flag has one owner | the cleanup ticket is assigned from `followups.owners` |
| Each flag has one epic | — a cleanup ticket has no parent, so it does not meet flow's own Definition of Ready |
| An epic cannot move to Live while a cleanup ticket is open | the open cleanup ticket is the fact the rule rests on, and the board that reads it is somebody else's (§4 above) |
| Review open flags monthly; remove flags older than 90 days or explain why | `live.flags.age` — every open cleanup ticket whose flag is older than `health.flag_max_age` (90d) is a finding unless the ticket carries `health.flag_kept_label` |

flow never closes a flag cleanup ticket: it cannot read the code, so it cannot
know the flag is gone. A person closes it, which is the opposite of every
other follow-up flow files.

## §10 AI agents

| Proposal | flow |
|---|---|
| Agents take work only from Ready | the agent skill (`skills/flow/SKILL.md`) tells a harness that the column carrying the gate is where takeable work sits, and `ticket.ready` refuses a pull request whose ticket never passed it |
| Mark agent work with the label `agent`, or an agent user | `agents.labels` and `agents.authors` — either one makes a pull request agent-written |
| A person reviews each agent PR; the reviewer owns the merge | `agent.human_review`, which relaxed strictness does not relax |
| The WIP limit on In Review matters: agents outrun reviewers | `workflow.columns[].wip.limit` on the review column, warned on every gated pull request, and `live.wip` counts the days the queue was over it. Expedited work is the one exception, which is the team’s own agreed way past a limit |
| Track throughput and rework rate for agent work | `live.agents.rework` — how much a machine merged in the window and how much of it came back first, reported in the same health issue and never graded |

## §11 Testing · §12 Change management

flow adds no QA column and models no handoff. The change record the proposal
asks for is what `flow doctor` reads every day, and the pinned health issue is
where it is written down: the pull request's diff and approval, the gate's
verdict recorded as a commit status, the deployment record, and the ticket key
that links the lot. `flow doctor --report` writes the same thing as a file for
somebody who wants to keep it.

---

## A delivery board, as flow

This is the proposal's delivery board written as `.flow.yaml`. It is the
`deploy-done` preset with the proposal's sixth column and its full Definition
of Ready. docs_test.go parses and validates exactly this text.

```yaml
version: 1
preset: deploy-done

forge:
  type: github
  repo: acme/widget
  url: https://github.com
  auth: { token_env: GITHUB_TOKEN }
  required_checks: [test]

tracker:
  type: jira-cloud
  project: HPB
  url: https://acme.atlassian.net
  auth: { token_env: JIRA_API_TOKEN, email_env: JIRA_EMAIL }

workflow:
  done_on: deployed
  columns:
    - { id: backlog,      name: Backlog,      category: todo }
    - { id: needs_design, name: Needs Design, category: todo }
    - { id: ready,        name: Ready,        category: todo, gate: definition_of_ready }
    - { id: in_progress,  name: In Progress,  category: in_progress, wip: { per_person: 2 } }
    - { id: in_review,    name: In Review,    category: in_progress, wip: { limit: 4 } }
    - { id: done,         name: Done,         category: done }
  transitions:
    pr_opened_draft:     { to: in_progress, owner: flow }
    pr_ready_for_review: { to: in_review,   owner: flow }
    changes_requested:   { to: in_progress, owner: flow }
    deployed:            { to: done,        owner: flow }
  expedite:
    labels: [incident]
    max: 1
  blocked:
    label: blocked
    require_comment: true

definition_of_ready:
  rules: [acceptance_criteria, parent_link, not_blocked, flag_if_user_facing, design_if_ui]
  max_size: 2d

commits:
  convention: conventional
  header_max: 72
  merge_strategy: squash
  unlinked_types: [ci, build, chore, docs]

deploys:
  source: forge_deployments
  environment: production
  max_lag: 2h
  max_age: 7d

policy:
  status_context: flow/policy
  strictness: standard
  comment: on_failure

agents:
  labels: [agent]
  require_human_review: true

followups:
  labels: [flow-followup]
  owners:
    default: alice
```

`flow plan` against that configuration proposes the branch protection, the
merge settings, the board's columns and the labels; `flow apply` writes what
the credential may write and files a follow-up for the rest.

## What the proposal asks for and flow does not do

Each one that is left is an issue rather than a paragraph. A gap this
document found and flow has since closed leaves the list and is described in
the section it belongs to.

Nothing is left on it. The five gaps this verification found when it was
written — the product board, the transition a branch starts, the comment a
blocked ticket owes, the age of a feature flag, and what the agents got
through — are each closed, and each is described in the section above that
the proposal puts it in. The list is kept because the next reading of the
next proposal will fill it again.
