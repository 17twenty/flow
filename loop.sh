#!/usr/bin/env bash
# Ralph loop for flow-builds-flow.
#   AGENT_CMD="claude -p" ./loop.sh            # build (mode from .ralph/AGENTS.md)
#   MODE=plan AGENT_CMD="claude -p" ./loop.sh  # one planning pass
#   MAX=20 …                                   # cap iterations
# Requirements: GH_TOKEN=<the flow-bot-gh PAT> (see docs/cookbook.md).
# Run sandboxed with scoped, non-production credentials.
set -euo pipefail
AGENT_CMD="${AGENT_CMD:?set AGENT_CMD}"; MAX="${MAX:-0}"; RALPH=".ralph"
POLL="${POLL:-60}"   # seconds between PR state checks in pr mode

prompt="$RALPH/PROMPT.md"; [[ "${MODE:-build}" == plan ]] && { prompt="$RALPH/PROMPT_plan.md"; MAX=1; }
mode() { sed -n 's/^CURRENT MODE: *//p' "$RALPH/AGENTS.md" | head -1; }

wait_for_human() {
  # pr mode: block while an agent PR is awaiting review (not changes-requested).
  while :; do
    state=$(gh pr list --label agent --state open --json number,reviewDecision,isDraft \
      --jq 'map(select(.isDraft==false)) | if length==0 then "none" else .[0].reviewDecision // "REVIEW_REQUIRED" end')
    case "$state" in
      none|CHANGES_REQUESTED) return 0 ;;
      *) echo "waiting for human review ($state)…"; sleep "$POLL" ;;
    esac
  done
}

i=0; broken=0
while :; do
  i=$((i+1)); echo "=== iteration $i ($(mode)) $(date -u +%FT%TZ) ==="
  [[ "$(mode)" == pr && "$prompt" == "$RALPH/PROMPT.md" ]] && wait_for_human
  git switch main -q && git pull -q --ff-only || true
  $AGENT_CMD < "$prompt" || echo "agent exited non-zero"
  if go build ./... >/dev/null 2>&1; then broken=0; else broken=$((broken+1)); echo "build broken"; fi
  [[ $broken -ge 2 ]] && { echo "build broken twice; stopping for human"; exit 1; }
  [[ "$MAX" -gt 0 && "$i" -ge "$MAX" ]] && break
done
