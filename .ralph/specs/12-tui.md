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
