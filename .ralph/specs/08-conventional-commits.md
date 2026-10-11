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

## Decisions

- **§4 seed files rewritten (item 1.2).** Both corpus files now describe v2 only.
  Samples expecting the retired `cc.footer.ticket`, `cc.trailer.flag.name`,
  `cc.trailer.migration.value`, `cc.body.wrap` and `cc.description.imperative`
  rules were removed, and `cc.breaking.description` was renamed to its v2 id
  `cc.breaking.explained` (its sample lost its body, which v2 accepts as an
  explanation). The `Refs:`, `Flag-Added:`, `Flag-Removed:`, `Migration:` and
  `Agent:` trailers were dropped from `valid.txt`: flow does not read them, and
  leaving them implied otherwise. `Co-authored-by:` and `BREAKING CHANGE:`
  remain, being git and Conventional Commits standards respectively. New samples
  cover `cc.scope.required` and `cc.scope.allowed`, which the v1 corpus never
  exercised. `TestCorpusCoversEveryRule` now fails if any rule in `AllRules()`
  lacks a sample.
- **§4 corpus directives.** A sample may carry `# settings: <directives>` before
  its `# expect:` line, so scope rules can be exercised without forcing them on
  the whole corpus. Two directives exist: `scope_required`, and
  `scopes=a|b|c` to set the allowed list. Any other directive fails the test
  rather than being ignored.
- **§2 which messages are skipped.** The table says "Merge commits skipped".
  This extends to every form git writes itself: merges, `Revert "…"` reverts and
  `fixup!`/`squash!`/`amend!` autosquash commits. None can satisfy the grammar,
  no human chose their wording, and the autosquash ones disappear on rebase, so
  reporting `cc.header.format` against them is pure noise. A message using the
  conventional `revert:` *type* is an ordinary commit and is linted normally.
- **§2 `cc.scope.allowed` with several scopes.** One violation is reported per
  disallowed scope, each pointing at that scope's own column, rather than one
  violation for the whole list. Scope comparison is case-sensitive: configured
  scopes are the authority on their own spelling.
- **§2 `cc.description.case` exceptions.** "Acronym" means the opening word
  holds two or more capitals (`CSV`, `GPU`, `I/O`). "Backticked identifier"
  means the description's first character is a backtick.
- **§2 `cc.breaking.explained` for pull requests.** A "Breaking change" section
  is a line that, once markdown heading and emphasis characters and a trailing
  colon are stripped, equals "breaking change" or "breaking changes" in any
  case; a `BREAKING CHANGE:` footer in the body also satisfies the rule.

- **§3 "strip trailing period" strips every trailing stop, and the spaces with
  them.** Removing exactly one stop would leave `feat: done...` as `feat: done..`,
  still violating the rule and still needing a fix — so a second `--fix` would
  change the file again and the autofix would not be idempotent, which §3
  requires. Stops and trailing whitespace are therefore trimmed together in one
  pass, using the same definition of whitespace as the header trim. Fuzzing found
  both halves of this: `"A:  ."` oscillated between `"a:  "` and `"a:"` when the
  cutsets were separate, and `"A: \r."` did the same when one cutset counted a
  carriage return as space and the other did not.
- **§3 a description of nothing but stops is left alone.** `feat(cli): ...`
  would become `feat(cli): ` — a header with no description, which no longer
  parses. The fixer declines rather than turning a lintable message into an
  unparseable one; the linter still reports the stop.
- **§3 leading whitespace is not trimmed when it would expose the comment
  character.** git decides what is a comment from the line's first character, so
  rewriting `" #ops: thing"` to `"#ops: thing"` would not tidy the header, it
  would delete the line from the commit; the same move on a `" # ---- >8 ----"`
  line would truncate everything after it. Such a header keeps its indentation
  and only loses trailing whitespace. Found by fuzzing.
- **§3 the blank line is inserted directly after the header, before any
  comments.** git strips comment lines before applying its own cleanup, so a
  blank inserted after an intervening comment would not survive to separate
  header from body.
- **§1 git parses trailers, and `BREAKING CHANGE:` is not one (item 1.11).**
  `internal/git.Trailers` shells out to `git interpret-trailers --parse`, so a
  repository's own `trailer.*` settings decide where the trailer block starts
  and folded values are unfolded for free. git requires a trailer key to be a
  single token, so Conventional Commits' `BREAKING CHANGE:` footer is body text
  to it: `internal/commits` finds that footer itself and must keep doing so.
- **§1 `core.commentChar: auto` reads as unset (item 1.11).** With `auto`, git
  picks a character per message that does not appear in it. flow cannot predict
  the choice, so `Repo.CommentChar` reports no character and the caller keeps
  git's default `#`. The alternative — guessing — would strip a line somebody
  meant to keep.
