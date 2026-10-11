# PLANNING MODE — no product code

0a. Read all of `.ralph/specs/`, `.ralph/IMPLEMENTATION_PLAN.md`, `.ralph/AGENTS.md`.
0b. Read the source tree and open/closed `agent` issues (`gh issue list --label agent --state all`).
1. Gap analysis: every MUST in the specs → implemented / partial / missing (search, don't assume).
2. Update the plan: keep checked items; add missing items with id, title,
   spec refs, deps, ACs (observable); split anything bigger than ~1–3 files + tests.
   For each NEW item also create a GitHub issue (Acceptance criteria section,
   label `agent`, sub-issue of the phase parent, Project status Backlog) and
   append `(#N)` to the plan line.
3. Record spec contradictions under "Spec issues" with a proposed resolution.
4. Ship as a `docs(ralph): refresh implementation plan` change (direct or PR per MODE).
