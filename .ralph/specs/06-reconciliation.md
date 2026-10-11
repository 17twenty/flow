- **§1 a status option's board position is not desired (item 2.5).** Item 2.4
  asked for each column at its configured index. §4's hazard is that setting
  a GitHub Projects option set replaces it, which can clear every item's
  status — so flow would propose that destructive write to move a column one
  place left. The set of column names is what the workflow depends on; the
  order they are drawn in is the board's own business, and a team that drags
  one is not drifting.
- **§2 the plan is pure, and the adapters are not passed to it (item 2.5).**
  `NewPlan` takes the desired state, the observations, and each provider as
  `{kind, capabilities, permissions}`. Given the same input it gives the same
  plan on any machine at any time, which is what makes the golden files worth
  pinning and what lets spec 05 §6's comparison put another tracker's
  declarations in front of one unchanged configuration with no network at all.
- **§2 contents are compared by name, and the provider may know more (item
  2.5).** An observed thing matches when it says everything the desired one
  says, not when it says exactly the same things: a label flow asked for by
  name has a colour and a description flow never mentioned, and calling that
  a difference would propose an update on every run for ever. An update
  carries the provider's own id, because that is what apply needs to address
  it.
- **§2 every item lists its changes, whatever flow will do about them (item
  2.5).** A compensated, attested, follow-up or unsupported item still shows
  the creates, updates and deletes that would make the provider agree. Those
  changes are the content of the follow-up issue and of the thing a human is
  being asked to attest to; hiding them would leave the plan saying somebody
  must act without saying what the act is.
- **§2 an item with no capability is resolved as read-only (item 2.5).**
  `forge.default_branch` names none (§1), so the planner hands the ladder a
  synthesised `{set: false, verify: true, remediation: follow_up}` rather than
  deciding its outcome itself. Spec 05 §4 stays the one place an outcome is
  reached, and a trunk that is not there comes out as a follow-up instead of a
  special case.
- **§3 a plan goes stale against three hashes and every observation (item
  2.5).** `flow apply --plan` compares the configuration hash, the desired
  hash, and each item's observed hash against a plan made now, and refuses on
  the first difference, naming it. The three answer different questions — what
  the team asked for, what flow made of it, what the provider held — and
  applying a plan once any of them has moved would make a change nobody
  approved. The lock hash is recorded but not compared: an attestation made
  since the plan can only make flow do less.
- **§4 a removal is proposed, never assumed (item 2.5).** The plan lists a
  delete for anything observed that the configuration does not ask for —
  subject to §4's rule that an additive set only yields what the lock records
  as flow's own creation — and `--prune` decides at apply time whether it
  happens. A plan that hid unwanted leftovers until somebody passed a flag
  would be answering a different question from the one `flow plan` is asked.
