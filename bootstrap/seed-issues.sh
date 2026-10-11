#!/usr/bin/env bash
# Seed GitHub issues from .ralph/IMPLEMENTATION_PLAN.md (bootstrap Stage A).
#   ./bootstrap/seed-issues.sh 17twenty/flow <project-number>
# Requires: gh (authenticated with repo + project scopes), jq.
# Idempotent: skips items whose plan line already has "(#N)".
# Creates one parent issue per phase and one sub-issue per item, adds all to the
# project. Phase 0 items are labelled `agent` and placed in Ready; others Backlog.
set -euo pipefail
REPO="${1:?owner/repo}"; PROJECT="${2:?project number}"; OWNER="${REPO%%/*}"
PLAN=".ralph/IMPLEMENTATION_PLAN.md"

project_id=$(gh project view "$PROJECT" --owner "$OWNER" --format json --jq .id)
field_json=$(gh project field-list "$PROJECT" --owner "$OWNER" --format json)
status_field=$(jq -r '.fields[] | select(.name=="Status") | .id' <<<"$field_json")
opt() { jq -r --arg n "$1" '.fields[] | select(.name=="Status") | .options[] | select(.name==$n) | .id' <<<"$field_json"; }
READY=$(opt "Ready"); BACKLOG=$(opt "Backlog")

add_to_project() { # $1 issue url, $2 option id
  local item; item=$(gh project item-add "$PROJECT" --owner "$OWNER" --url "$1" --format json --jq .id)
  gh project item-edit --id "$item" --project-id "$project_id" --field-id "$status_field" --single-select-option-id "$2" >/dev/null
}

phase_title=""; phase_num=""; parent=""
tmp=$(mktemp); cp "$PLAN" "$tmp"
while IFS= read -r line; do
  if [[ "$line" =~ ^##\ Phase\ ([0-9]+)\ —\ (.*)$ ]]; then
    phase_num="${BASH_REMATCH[1]}"; phase_title="${BASH_REMATCH[2]}"
    parent=$(gh issue list -R "$REPO" --label phase --search "in:title \"Phase $phase_num —\"" --json number --jq '.[0].number // empty')
    if [[ -z "$parent" ]]; then
      url=$(gh issue create -R "$REPO" --label phase --title "Phase $phase_num — $phase_title" \
        --body $'Tracking issue for plan phase '"$phase_num"$'.\n\n## Acceptance criteria\n- All sub-issues closed')
      parent="${url##*/}"; add_to_project "$url" "$BACKLOG"
    fi
  elif [[ "$line" =~ ^-\ \[\ \]\ ([0-9]+\.[0-9]+)\ (.*)\ —\ specs:\ (.*)$ ]] && [[ "$line" != *"(#"* ]]; then
    id="${BASH_REMATCH[1]}"; title="${BASH_REMATCH[2]}"; specs="${BASH_REMATCH[3]}"
    # Collect AC lines that follow this item in the plan.
    ac=$(awk -v id="$id" '
      $0 ~ "^- \\[ \\] "id" " {f=1; next}
      f && /^- \[/ {exit}
      f && /^  - AC:/ {sub(/^  - AC: */, "- "); print}' "$PLAN")
    [[ -z "$ac" ]] && ac="- Implemented as specified in: $specs"
    labels="agent"
    body=$'## Why\nPlan item '"$id"$'.\n\n## Acceptance criteria\n'"$ac"$'\n\n## Spec references\n'"$specs"
    url=$(gh issue create -R "$REPO" --label "$labels" --title "$title" --body "$body")
    num="${url##*/}"
    # Link as sub-issue of the phase parent (REST sub-issues API).
    child_id=$(gh api "repos/$REPO/issues/$num" --jq .id)
    gh api -X POST "repos/$REPO/issues/$parent/sub_issues" -F sub_issue_id="$child_id" >/dev/null
    status="$BACKLOG"; [[ "$phase_num" == "0" ]] && status="$READY"
    add_to_project "$url" "$status"
    sed -i "s/^- \[ \] $id \(.*\)$/- [ ] $id \1 (#$num)/" "$tmp"
    echo "seeded $id → #$num"
  fi
done < "$PLAN"
cp "$tmp" "$PLAN"; rm -f "$tmp"
echo "done. Commit the updated plan: git commit -am 'docs(ralph): link plan items to issues'"
