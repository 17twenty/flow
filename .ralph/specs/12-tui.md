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
- **§4 the screen is the shape, never the decision (item 6.4).** Whether flow
  may ask at all, what it is asking about and what the answer means were
  already settled before any of this: `ApplyCmd.approve` refuses
  non-interactively with exit 64 and counts the changes, and a plan with
  nothing to do is approved without a question (spec 06 §3). The screen is
  chosen after all of that, by one predicate, and the plain prompt is what is
  left when it says no. So there is no apply a terminal can agree to that a
  pipe cannot, which is what "every screen has a plain equivalent" has to mean
  to be worth anything.
- **§0 a TUI needs both ends of a terminal, and one function decides it
  (item 6.4).** `Context.tui` is `Context.interactive` plus two things: stdin
  must be a terminal too, because a screen reads keys, and `TERM=dumb` is a
  terminal saying it cannot do this. `--json` is excluded there rather than at
  each call site, since a document for a machine is the one case where asking
  is still fine and drawing never is. Both go through `terminalCheck`, a
  package variable holding `isTerminal`, for the same reason `newAdapters` is
  one: a test process is never a terminal, and the half of every one of these
  rules that matters is the half that only happens when it is.
- **§4 the table is grouped, and the order of the groups is the engine's
  (item 6.4).** An apply is agreed to as a few kinds of thing — this many flow
  will do, these it is handing to somebody, this one nobody can do here — so
  the rows are grouped by outcome with a count on each heading. The order is
  `engine.Outcomes`, the same list the plan's own summary line counts in, so
  the screen and the line under it cannot disagree about what comes first. The
  words and the glyph are the engine's (spec 05 §5); the shape, the padding
  and the colour are this package's.
- **§4 the first answer is the answer (item 6.4).** Quitting is a command and
  not an exit, so a key already in the terminal's buffer is still delivered
  after the question has been settled. The model ignores it. A refusal that a
  second keystroke could turn into an apply would be worse than no
  confirmation at all, and this is the one screen where that is true.
- **§4 an unanswered screen is a no (item 6.4).** The zero `Confirmation`
  refuses, so an interrupt, a terminal closing and a model that came back as
  something else all land on "nothing was applied" without anybody having to
  enumerate them. What is not that is an error: a terminal that could not be
  put into raw mode or a run whose deadline expired has not refused anything,
  and reporting it as a refusal would describe the symptom and hide the cause.
- **§4 the screen takes the width the program gives it (item 6.4).** A table
  wider than the terminal is folded by the terminal wherever it runs out, which
  turns a grouped table into a wall. `tea.WindowSizeMsg` is therefore the one
  message besides a key press that the model keeps, and the table is rendered
  to it. Until the program has said, the table is unconstrained: guessing
  eighty columns would fold a plan that fits.
- **§2 the picker answers a question; it does not decide there is one
  (item 6.2).** The ladder in spec 03 §1.1 is the engine's, and the rung this
  screen stands on is "a provider offered more than one answer". So the root
  hands `engine.ResolveInput` a `Chooser` and the engine decides when to call
  it. A chooser is supplied only when flow would draw a screen at all; a nil
  one is a rung the engine steps over, and the plain equivalent is the one it
  already writes — the candidates in the report with the `--set` that records
  an answer.
- **§2 a person who declines to pick has not answered (item 6.2).** Escape
  and `q` leave the question open, which the chooser reports as
  `core.ErrNeedsInput` naming the `--set` that would settle it, rather than as
  a failure or as a candidate nobody chose. The zero `Picker` has picked
  nothing for the same reason the zero `Confirmation` refuses: an interrupt, a
  terminal closing and a model that came back as something else all land on
  the safe reading without anybody enumerating them.
- **§2 a key belongs to the filter before it belongs to the screen
  (item 6.2).** Return applies a filter and escape abandons one, so a picker
  that read either as an answer would make typing a name impossible. The rule
  is inherited rather than restated: the screen asks the list what state it is
  in and which of its own bindings are live, which is also why the key line
  can only name keys that would do something.
- **§2 only the name is filtered on (item 6.2).** A fuzzy match reports which
  characters it matched so they can be underlined, and underlining them means
  the string filtered and the string drawn are the same string. A person who
  has the identifier does not need a picker — they have `--set`.
- **§2 the rows are drawn by flow and browsed by the library (item 6.2).**
  bubbles ships a delegate with a palette of its own, and flow has seven
  styles and a mode with no colour at all. So the list is given flow's styles,
  its filter input and its pagination marks, and the rows are rendered here:
  two characters of cursor in both alphabets, a name cut to the terminal
  rather than folded, and a detail line only when a candidate has one — a row
  that lies about its height fills a page with twice the candidates it can
  show.
- **§2 the list guesses eighty by twenty-four until the program says
  otherwise (item 6.2).** The apply confirmation can be drawn unconstrained
  and left for the terminal to fold; a list cannot, because its height is how
  many candidates it shows and zero of them is an empty screen.
