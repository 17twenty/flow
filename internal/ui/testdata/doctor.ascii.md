## flow doctor

Live health over the last `14d`.

### config

| | check | found |
|---|---|---|
| + | `config.syntax` | .flow.yaml is valid |

### forge

| | check | found |
|---|---|---|
| x | `forge.protection` | main may be pushed to directly |
| || | `forge.required_checks` | forge.protection did not hold |

**`forge.protection`** -- the trunk is protected

- fix: protect main
- run: `flow apply`

### tracker

| | check | found |
|---|---|---|
| ? | `tracker.project` | which board is this team's? |
| ... | `tracker.automation` | asked 9 days ago |
| * | `tracker.saved_view` | nobody has vouched for this yet |

**`tracker.project`** -- the board flow manages is known

- fix: name the board
- run: `flow doctor --fix --set tracker.project=<id>`

**`tracker.automation`** -- the board moves tickets by itself

- fix: the follow-up is open (@platform)

**`tracker.saved_view`** -- the ready view exists

- fix: say what you see
- run: `flow attest tracker.saved_view --note '…'`

### live

| | check | found |
|---|---|---|
| ! | `live.prs.linked` | 12/19 some pull requests name no ticket |
| E | `live.deploys.recent` | unavailable |

**`live.prs.linked`** -- every pull request names its ticket

- #101 tidy the loader <https://example.invalid/101>
- #104 bump deps <https://example.invalid/104>
- #109 fix the flake <https://example.invalid/109>
- #112 rename a field <https://example.invalid/112>
- #115 drop dead code <https://example.invalid/115>
- ... and 2 more

- fix: ask for the ticket key in the title
  1. agree the convention
  2. turn the rule on in .flow.yaml

### meta

| | check | found |
|---|---|---|
| . | `meta.lock` | no lock is written in CI |

4 actions for you - 1 waiting on others - 1 attestation due - exit 1
