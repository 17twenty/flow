# 20 — Reconcile, roles, runners and adoption

Specs 00–19 describe a flow that is told when to act: `check pr` runs on a
pull request event, the tracker's own automation runs on a merge, `check
deploy` runs on a deployment event, and `doctor` checks the lot once a day.
Every one of those is an event somebody else has to deliver. This spec is
what flow does when they are not delivered, and what that changes for the
person adopting it. Where it and an earlier spec disagree, this one wins and
the earlier spec is amended in the same change.

## 1. The principle: converge, do not wait to be told

**P14 Level-triggered.** Every flow run that is allowed to write settles
everything within its authority that it can see, whatever event started it.
An event decides *when* flow looks, never *whether* the work gets done. A
missed event — a forge that suppresses it, a runner that was asleep, a native
automation somebody switched off — costs latency, never correctness.

What this rules out is any design in which correctness depends on a chain of
events firing in order. The case that forced it (issue #201): a pull request
merged for GitHub's Actions token causes no further events, so the issue it
closes stays open, `push` workflows on the trunk never start, and the release
and the deployment that follow a merge never happen. No forge-specific trick
fixes the class; converging does.

The engine already decides from state rather than payloads (spec 04,
Decisions), so
P14 is a statement about *when flow runs* and *what a run is allowed to
finish*, not a new decision procedure.

## 2. `flow sync`: the cheap, frequent half

`flow sync` performs the catch-ups flow owns and exits. It is `doctor --fix`
narrowed to the live checks whose remedy is flow's own act:

| Catch-up | Check it settles | Spec |
|---|---|---|
| Move tickets the configuration gives flow | `live.transitions.flow` | 07 §6, 04 §5 |
| Close issues a merged pull request names and the forge left open | `live.prs.closed_issues` | 07 §6 |
| Bring a pull request that passed the gate up to date with the trunk it is behind | `live.prs.current` | 07 §6, issue #240 |

The last is a push to the pull request's branch, so it is made with the
credential the daily job uses, never the Actions token: GitHub starts no
checks for a push that token makes, and the pull request would wait for ever
on checks that never run. On a forge flow cannot update branches on it warns
rather than fails.

It reads a short window (default `48h`, `--window`), never publishes the
health report, never proposes the lock, never files follow-ups, and prints
what it did. It exits as doctor would on the checks it ran: 0 when everything
it could see is settled — including what it settled just now, which its
report names — 2 when something it saw still needs a person, and the usual
non-zero codes when it could not look or could not act.

`doctor --fix` keeps doing everything `sync` does: `sync` is a subset run
often, not a second implementation. The split is between *settling* (cheap,
frequent, writes) and *auditing* (a fortnight of history, daily, reports).

## 3. Where `sync` runs: any trigger the runner has

Correctness needs only that *something* runs `sync` reasonably often. The
shipped CI template runs it:

1. as a second step of the gate job, after `check pr` — every pull request
   event settles what the previous merge left behind;
2. on a schedule, hourly — the backstop for a quiet repository;
3. on `push` to the trunk where the forge delivers it — the fast path.

None of the three is required for correctness, and none depends on another.
A forge whose CI cannot schedule still converges on its next pull request.

## 4. Owning a transition beats attesting somebody else's

Spec 04 §5 lets a transition be `owner: native` (the tracker moves it) or
`owner: flow`. A native transition flow cannot observe has to be attested by
a person every 30 days (spec 07 §8), which is the most onerous recurring
cost flow imposes.

Under P14 flow can perform the `pr_merged` and `issue_closed` transitions
itself on its next run. So:

- The shipped presets give `pr_merged` and `issue_closed` to **flow**. A
  native automation that also moves the card is harmless: flow then finds
  nothing to do, and `live.transitions.flow` holds.
- `owner: native` remains for teams that want the tracker alone to move a
  ticket, and is still attested where it cannot be observed.
- An attestation is reserved for a fact nobody can observe and nobody can
  make true by acting — on GitHub Projects, the expedite view.

## 5. Credentials are roles, not tokens

flow names three roles. A configuration maps each to a credential; one
credential may hold several.

| Role | Does | Needs |
|---|---|---|
| **reader** | the audit: reads history, settings, the board | read on forge and tracker |
| **gate** | posts `flow/policy` and its comment | commit statuses, pull request comments |
| **actor** | moves tickets, closes issues, arms merges, applies settings | write on the tracker; write (and admin, for settings) on the forge |

What is forge-specific is not the roles but a forge's quirks about them, and
those live where every other provider fact lives: the adapter's capability
declaration (spec 05). GitHub's declares, against `auto_merge`, that a merge
armed by the Actions token causes no further events. flow does not route
around that in code; it detects the consequence on any forge (§6) and the
declaration says what to do about it.

## 6. A merge that started nothing is a finding

`live.trunk.built`: every merge on the trunk in the window has at least one
of `forge.required_checks` reported against the merge commit itself. A merge
the forge built nothing for is a merge whose events were suppressed or whose
pipeline is broken; either way, `push`-triggered work — builds, releases,
deployments — did not happen. The check is forge-neutral; the remedy text
comes from the forge's declaration.

## 7. The runner contract

flow needs four things from wherever it runs, and nothing else:

1. a trigger when a pull request changes, with the pull request's number;
2. a schedule;
3. a credential for the forge and one for the tracker (§5);
4. the flow binary, at a pinned version.

How a run learns (1) is the only part that differs between CI systems, so it
sits behind one small seam: `FLOW_PR` (and `FLOW_RUN_URL`, for the status's
link) in the environment win everywhere; failing those, flow reads the CI
system it recognises (GitHub
Actions' event payload, GitLab CI's `CI_MERGE_REQUEST_IID`, Forgejo and Gitea
Actions as GitHub's). Templates ship per CI system under
`assets/templates/ci/<ci>-flow.yml`, and every template installs a released
binary at a pinned version rather than compiling flow from source with
whatever toolchain the runner has.

## 8. Adopting in stages

`mode` (top level of `.flow.yaml`) says how much flow may do:

| Mode | flow writes | Credential needed |
|---|---|---|
| `observe` | nothing — `doctor` reports, `check pr` and `sync` say what they would do | reader |
| `gate` | `flow/policy` and its comment | reader + gate |
| `automate` (default) | everything this set of specs describes | all three |

The workflow is the same in every mode: the mode in `.flow.yaml` governs what
each command writes, so `flow init --mode observe` gives an existing
repository and board their report the same day, before anybody grants flow a
write, and raising the mode is a one-line change the gate judges like any
other. Below automate, `apply` refuses, `doctor` ignores `--fix`, `check
deploy` and `sync` change nothing, and `check pr` posts only what the mode
allows.

## 9. Order of work

Phase 9 of `IMPLEMENTATION_PLAN.md` carries this spec, in an order where each
step stands on its own: `sync` (§2) and its triggers (§3) first, because they
repair what a lost event leaves on the board; then flow-owned merge
transitions (§4) and the suppressed-merge finding (§5, §6); then the runner
seam and templates (§7) and modes (§8).

## Decisions

- **Converge rather than cascade (issue #201).** Triggering follow-on runs
  from a run that might itself be suppressed only moves the problem, and the
  fix that keeps auto-merge — arming with a second credential — is a GitHub
  answer to what is a general question: what happens when an event is lost.
  Every forge loses events; a system that converges on its next run needs no
  answer per forge.
- **Converging does not make an Actions-token merge safe (issue #213).**
  `sync` closes the issues such a merge left open and moves its cards, but
  what a push to the trunk starts — the build, the release, the deployment —
  belongs to the forge, and GitHub starts none of it. So flow does not switch
  auto-merge back on where the gate arms with the Actions token: on its own
  repository `forge.auto_merge` stays off, a person or the bot merges, and
  `live.trunk.built` (§6) is the finding if a merge ever starts nothing again.
  A team that wants auto-merge on GitHub arms with a credential that is not
  the Actions token, which the `auto_merge` declaration says.
- **`sync` is a subset of `doctor --fix`, not a sibling.** One catch-up, run
  by two commands, cannot disagree with itself; two would.
- **Flow owns the merge transitions by default.** Attesting a native
  automation every 30 days is the cost adopters feel most, and P14 makes the
  automation unnecessary for correctness. A team that prefers the tracker to
  move its own cards keeps `owner: native`.
- **Roles are named, not configured.** `forge.auth` and `tracker.auth` stay
  the only credential keys; the roles are vocabulary for what each credential
  must allow, for the documentation, the probe and the modes, rather than
  three more keys every adopter has to fill in.
