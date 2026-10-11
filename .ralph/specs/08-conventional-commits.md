# 08 — Conventional Commits (parser + linter only)

flow checks commit headers; it does not write commits, changelogs or versions
(use git-cliff, svu, release-please). With squash merges the **PR title is the
commit header**, so the gate checks PR titles; the optional local hook gives
early feedback on individual commits.

## 1. Grammar (`internal/commits/parse.go`)

```
header  = type [ "(" scope ")" ] [ "!" ] ": " description
type    = 1*ALPHA                  ; case checked by linter
scope   = 1*( ALNUM / "-" / "_" / "/" / "." / "," / " " )
```
Body/footers: parse only what the linter needs — blank line after header,
`BREAKING CHANGE:` footer, merge/revert/fixup detection. Trailers (e.g.
`Flag-Added:`) are read via `git interpret-trailers --parse` in `internal/git`,
not by this package. Strip git comment lines (respect `core.commentChar`) and the
scissors line. Never errors; non-conventional input yields `Type == ""`.
`FuzzParse` must not panic.

## 2. Rules (stable ids)

| id | default | Rule |
|---|---|---|
| `cc.header.format` | error | Parses as `type(scope)!: description`. If this fails, report only this. |
| `cc.type.allowed` | error | Lowercase, in `commits.types`. |
| `cc.scope.required` | per config | |
| `cc.scope.allowed` | error if list non-empty | Each comma-separated scope allowed. |
| `cc.header.length` | error | ≤ `header_max` runes. |
| `cc.description.case` | warn | Lowercase first letter unless acronym (≥2 caps) or backticked identifier. |
| `cc.description.period` | warn | No trailing period. |
| `cc.body.leading_blank` | error (commit only) | Blank line between header and body. |
| `cc.breaking.explained` | error | `!` ⇒ `BREAKING CHANGE:` footer or non-empty body (commit) / PR body section "Breaking change" (PR). |

Each violation: `{rule, severity, message, column, fix}`. Merge commits skipped.

## 3. Safe autofixes (`check commit --fix`)

Lowercase type; trim header whitespace; insert missing blank line after header;
strip trailing period. Idempotent. Nothing else is ever rewritten.

## 4. Test data

`assets/testdata/commits/valid.txt` / `invalid.txt` (samples separated by `---`;
invalid samples preceded by `# expect: <rule-ids>`). NOTE: these seeds include
`Refs:`/`Flag-Added:`/`Migration:` footers from an earlier, wider design. In v2:
- ignore footer-related expectations whose rule ids no longer exist
  (`cc.footer.*`, `cc.trailer.*`, `cc.body.wrap`, `cc.description.imperative`) —
  item 1.2 MUST rewrite the seed files to v2 rules in the same commit and record
  a Decision here.
