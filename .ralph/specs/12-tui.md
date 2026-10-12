# 12 — Terminal UI (minimal)

Bubble Tea v2 / Lip Gloss v2 / Bubbles v2 only (`charm.land/...`). v2 APIs:
declarative `tea.View`, `tea.KeyPressMsg`, `key.Matches`.

TUI runs only when stdin+stdout are TTYs, not `--json`/`--non-interactive`/`CI`,
`TERM != dumb`. Every screen has a plain equivalent.

Screens (only these):
1. **Init wizard** — detected forge/repo → tracker + owner/project (picker via
   Discover when credentials exist) → preset → done_on + deploy source → review
   rendered `.flow.yaml` → write.
2. **Picker** — fuzzy list for placeholder resolution (doctor/init).
3. **Doctor** — inline spinners per check (no alt-screen); `--watch` alt-screen
   table + details pane (`f` fix, `r` rerun, `a` attest, `q` quit).
4. **Apply confirmation** — outcome-grouped table, `y`/`n`.

Styles in `internal/ui/styles.go`: Ok, Warn, Err, Info, Muted, Accent, Code;
adaptive light/dark via `lipgloss.LightDark` + `HasDarkBackground` (TTY only);
print with `lipgloss.Fprint` for colour downsampling; glyph ASCII fallback.
Render functions are pure and golden-tested; models tested via `Update`.

## Decisions

- **The words are the engine's; the UI adds colour, glyphs and shape.** Which
  glyph a finding gets, what an outcome is called, what follows the arrow and
  what order the categories come in are properties of the decision (spec 05
  §5, spec 07 §9), not of the terminal. `internal/ui` therefore renders text
  by decorating the engine's own plain rendering line by line, rather than
  walking the result types a second time. A styled document with the colour
  taken off again is byte-for-byte the plain one — a property a test holds
  flow to, and one that a second implementation of the same layout could only
  promise.
- **Policy rendering stays in the engine.** `PolicyResult.Text` and
  `CommentBody` (spec 09) are an artefact posted to a forge, not terminal
  presentation, so they keep their own markdown. Only plan and doctor are
  rendered by `internal/ui`.
- **The ASCII fallback table is injective.** Two findings that differ in a
  Unicode terminal must differ in an ASCII one, or `--ascii` would quietly
  merge a warning into an error. This is why the already-ASCII `!` is itself
  translated, to `E`: `⚠` falls back to `!` and the two must not collide.
- **Glyph transliteration is token-wise; punctuation transliteration is
  per-line prose.** A replacer cannot tell the error glyph `!` from an
  exclamation mark in a message, so the two tables are applied to different
  parts of a line.
- **The interpunct has two ASCII mappings.** As a glyph it is `.`; as a
  separator it is `-`. A `|` — the other obvious choice — would turn the
  summary line of a markdown report into a table row.
- **NO_COLOR is read where every other personal setting is read**
  (`internal/config/personal.go`, spec 03 §4). `ui.Options` takes a plain
  `Colour bool` and does not consult the environment, which is what makes a
  rendering reproducible in a test. `lipgloss.Fprint` honours NO_COLOR and
  strips ANSI off a non-terminal as a second backstop.
- **JSON is passed through from the engine untouched** — no colour, no
  transliteration. Colouring it would break the parser the flag exists for,
  and translating its glyphs would change the data.
- **An unknown format is refused** with `core.ErrValidation` rather than
  falling back to text: a caller that asked for JSON and was handed a table
  would put it somewhere a machine was waiting.
- **The background query is guarded by `term.IsTerminal` on both files.** On
  Unix lipgloss refuses a pipe too, but on Windows it opens CONIN$/CONOUT$ and
  queries the real console even when stdio is redirected, so the guard is what
  makes the answer depend on where the output goes on every platform. The
  terminal check and the query are injected into an unexported
  `darkBackground`, because a test on Unix cannot otherwise tell flow's
  decision from the library's. When flow cannot ask, it assumes dark.
- **§1 a policy result has no markdown rendering (item 4.3).** The markdown a
  gate produces already exists: it is the comment the engine writes on the
  pull request, so that the same bytes go out whoever runs flow. A second one
  here would be a second answer to a settled question. Text and JSON only.
- **§1 a deployment has no markdown rendering (item 4.4).** `check deploy`
  reports to a terminal and to a parser, and posts nothing anywhere that would
  want a table. Asking for `--format markdown` is `ErrValidation`, as it is
  for the gate.
