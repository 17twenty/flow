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
- **§3 a run that is being watched and a run that is being waited on are
  different screens (item 6.3).** `flow doctor` draws an inline display and
  leaves the report under it; `--watch` takes the alternate screen and leaves
  nothing. The report is a document somebody will scroll back to, so the
  display that preceded it has to be a few lines of their session rather than
  a window that took the terminal and handed it back empty. The watch screen
  is the opposite case — a display of something that keeps changing, where a
  minute's worth of stale reports in the scrollback would be worse than
  nothing — so it is the one rendering flow produces that is not a document.
- **§3 the run says where it has got to; it does not know who is listening
  (item 6.3).** `engine.DoctorInput.Progress` is a callback and may be nil,
  which is a run nobody is watching. A channel would have a size, and the size
  would decide whether the engine blocks on a terminal. Both moments of a
  check are reported — reached, then answered — because the gap between them
  is exactly the wait the display exists to fill, and a screen that only spoke
  when a check finished would be silent for the whole of a live check reading
  a fortnight of pull requests.
- **§3 the display empties itself and the screen is told, not killed
  (item 6.3).** The last thing the terminal holds is the report, so a finished
  `ui.Progress` draws nothing: a display left above the document would be the
  same findings twice. The root sends `ui.Finished` rather than cancelling the
  program, because a screen that is killed cannot take itself off the
  terminal. A run that failed before its first check still sends it.
- **§3 what the screen draws is the engine's line, cut rather than folded
  (item 6.3).** A finding is one line by construction, so both screens render
  `Checked.Line()` and truncate it to the width the program reported. A line
  that folded would make the inline display taller every time it redrew, and
  would make the watch screen's list lie about how many findings fit on a
  page. What the waiting line is cut to is the check's id, not the count:
  the number somebody is watching is how many are left.
- **§3 one clock, wound when the screen opens and by itself (item 6.3).**
  `WatchEvery` is a rate limit as much as a refresh — a run reads a fortnight
  of pull requests from a forge — so there is exactly one chain of ticks. A
  clock wound by each run that finished instead would leave a second chain
  behind every time somebody pressed `r`, and a window left open all
  afternoon would ask faster and faster for no reason anybody asked for. A
  tick that declines to act, because a run is in flight or a note is half
  written, still winds the clock.
- **§3 a run that went wrong keeps what was true (item 6.3).** A failed run
  answers with no report at all, and putting that on the screen would replace
  something known with nothing. The screen says the asking went wrong and
  leaves the findings where they are; the exit code is the last run that
  *finished*, and a window closed before any run came back exits on the reason
  there is no verdict rather than on 0.
- **§3 the pane is the one rendering that does not cap the evidence
  (item 6.3).** `EvidenceShown` exists because a report that scrolled a
  hundred pull requests past a reader would have told them nothing. A pane
  somebody opened on one check, and can scroll, is the case that cap was
  protecting them from, so `Checked.Detail` shows all of it.
- **§3 the screen asks flow for things by running the commands (item 6.3).**
  `f` and `a` go through a `ui.Runner` the root implements with the same
  `DoctorCmd` and `AttestCmd` a person would have typed, so what may be
  attested, how long a word stands and whose it is stay spec 07 §8's rules in
  one place. Which checks the `a` key will even open a note on is the
  engine's judgement too, carried on the result as `RemedyAttest`: a screen
  that let somebody vouch for anything would be a way to silence a failure
  flow could have proved.
- **§3 a flag that cannot be honoured is refused (item 6.3).** `--watch`
  with `--report` is a usage error, because a session that keeps asking has no
  last run to write down and the file would hold whichever run happened to be
  on the screen when somebody closed the window. `--watch` with no terminal is
  a usage error for the same kind of reason: it is a request to keep asking,
  and a report printed once is not that. Neither quietly becomes the other
  command.

- **§1 the wizard fills in the flags and configures nothing (item 6.1).**
  Every question it asks is a flag of spec 02 §2, and what it collects goes
  back onto the command and down the road a command line takes. That is what
  makes the plain equivalent plain: it is not a second implementation that has
  to be kept in step with this one, it is the same one. A person who answers
  eight questions is shown, on the last screen, the `flow init` that does it
  again without them — and only the flags that differ from what flow would
  have done unasked, because every flag in a command line is one more thing a
  reader has to decide whether somebody meant.
- **§1 the wizard does not discover, because `init` works offline
  (item 6.1).** §1 offers a picker over `Discover` "when credentials exist",
  and spec 02 §2 says in the same breath that `init` MUST succeed offline and
  that it is the command run before there is a configuration naming a tracker
  to hold a credential for. The MUST wins. The board is left as
  `${pending:project}`, which is what `flow doctor --fix` resolves — through
  the picker of §2, over that adapter's `Discover`, in the command that has
  the credential (spec 07 §3). So the picker spec 12 asks for is reached, and
  reached once: the placeholder ladder is not reimplemented here.
- **§1 `done_on` and the deploy source are asked at the preset, because that
  is where their answer is (item 6.1).** §1 ends the wizard with two more
  questions, and in flow's configuration neither is a value a repository can
  hold on its own: `workflow.done_on` must agree with
  `workflow.transitions.deployed` and `deploys.source` with the rest of its
  block, so the only answer either can be given is the one its preset already
  gives (spec 02 §2). Asking them as steps of their own would have been two
  screens on which every other answer produced a file flow refuses to read.
  They are therefore asked at `--preset`, where each option says what it means
  for both, and the step carries a line saying so — a question that is folded
  into another has to be visibly answered there, or the wizard has quietly
  dropped it.
- **§1 the screen starts from the file flow would have written (item 6.1).**
  The answers open filled in — the detected forge, the preset and that
  preset's own tracker — rather than blank, and the same value is the
  baseline the review's command line is shortened against. The two cannot
  drift apart because they are one value, which is what makes "press enter all
  the way through" and `flow init --yes` the same file and `flow init` the
  command the review offers for it.
- **§1 a question this configuration does not have is not asked (item 6.1).**
  An owner is a GitHub Projects idea and an address is a Jira one, so each is
  a step that applies or does not, and the count in the heading is of the
  questions there are rather than of the steps there might have been. A wizard
  that asked everybody everything would be a form.
- **§1 escape goes back, and backing out of the first question is cancelling
  (item 6.1).** Going back has to cost nothing — an answer somebody returns to
  check is still there, or the screen is a reason not to look — and there has
  to be a way out that is not an interrupt. `q` quits a list and is a letter
  in a field, because a board called "quarterly" has to be typeable; ctrl-c is
  both, because ctrl-c is always both. A screen nobody confirmed writes
  nothing, which is what an interrupt, a terminal closing and a model that
  came back as something else all arrive here as.
- **§1 the review shows the file, cut rather than scrolled (item 6.1).** The
  point of the last screen is that somebody recognises what their answers
  became, and the file itself is about to be in their working tree where they
  can read all of it. A preview that failed is on the screen in place of the
  file: a configuration flow would refuse to read is a thing to know before it
  is written, not after.
