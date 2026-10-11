#!/usr/bin/env bash
# Seed GitHub issues from .ralph/IMPLEMENTATION_PLAN.md (bootstrap Stage A).
#   GH_TOKEN=<token> ./bootstrap/seed-issues.sh 17twenty/flow <project-number>
# Requires: curl, jq, and GH_TOKEN with `repo` + `project` scopes.
# Idempotent: skips items whose plan line already has "(#N)".
# Creates one parent issue per phase and one sub-issue per item, adds all to the
# project. Items already ticked in the plan are created closed, so the board
# tells the truth about work finished before seeding. Phase 0 items are placed
# in Ready; others Backlog.
set -euo pipefail

REPO="${1:?owner/repo}"; PROJECT="${2:?project number}"; OWNER="${REPO%%/*}"
: "${GH_TOKEN:?set GH_TOKEN to a token with repo + project scopes}"
PLAN=".ralph/IMPLEMENTATION_PLAN.md"
API="https://api.github.com"

rest() { # method path [json-body]
  local method="$1" path="$2"
  # GitHub asks for at least a second between mutative requests; without it a
  # run this size trips the secondary rate limit part-way through.
  [[ "$method" != "GET" ]] && sleep 1
  if [[ $# -ge 3 ]]; then
    curl -sS -X "$method" -H "Authorization: Bearer $GH_TOKEN" \
      -H "Accept: application/vnd.github+json" -d "$3" "$API$path"
  else
    curl -sS -X "$method" -H "Authorization: Bearer $GH_TOKEN" \
      -H "Accept: application/vnd.github+json" "$API$path"
  fi
}

gql() { # query [jq-args...] — variables come from $GQL_VARS (JSON object)
  local query="$1"; shift
  local payload; payload=$(jq -n --arg q "$query" --argjson v "${GQL_VARS:-\{\}}" \
    '{query:$q, variables:$v}')
  local out; out=$(curl -sS -X POST -H "Authorization: Bearer $GH_TOKEN" \
    -H "Content-Type: application/json" -d "$payload" "$API/graphql")
  if jq -e '.errors' >/dev/null 2>&1 <<<"$out"; then
    echo "graphql error: $(jq -c '.errors' <<<"$out")" >&2; return 1
  fi
  jq "$@" <<<"$out"
}

# ---- resolve the project and its Status field -------------------------------
GQL_VARS=$(jq -n --arg l "$OWNER" --argjson n "$PROJECT" '{login:$l, number:$n}')
project_json=$(gql 'query($login:String!,$number:Int!){
  user(login:$login){ projectV2(number:$number){ id
    field(name:"Status"){ ... on ProjectV2SingleSelectField { id options{ id name } } } } } }' \
  '.data.user.projectV2')

project_id=$(jq -r '.id' <<<"$project_json")
status_field=$(jq -r '.field.id' <<<"$project_json")
[[ "$project_id" == "null" ]] && { echo "project $PROJECT not found for $OWNER" >&2; exit 1; }
opt() { jq -r --arg n "$1" '.field.options[] | select(.name==$n) | .id' <<<"$project_json"; }
READY=$(opt "Ready"); BACKLOG=$(opt "Backlog")
[[ -z "$READY" || -z "$BACKLOG" ]] && { echo "Status needs Ready and Backlog options" >&2; exit 1; }

add_to_project() { # $1 issue node_id, $2 status option id
  # The project's auto-add workflow may already have taken the issue, and it
  # races issue creation: sometimes we add it first, sometimes it does. Look for
  # an existing item before adding, and fall back to a lookup if the add loses
  # the race, so either order converges on one item.
  local item
  item=$(project_item_for "$1")
  if [[ -z "$item" ]]; then
    GQL_VARS=$(jq -n --arg p "$project_id" --arg c "$1" '{project:$p, content:$c}')
    item=$(gql 'mutation($project:ID!,$content:ID!){
      addProjectV2ItemById(input:{projectId:$project, contentId:$content}){ item{ id } } }' \
      -r '.data.addProjectV2ItemById.item.id' 2>/dev/null || true)
    [[ -z "$item" || "$item" == "null" ]] && item=$(project_item_for "$1")
  fi
  if [[ -z "$item" || "$item" == "null" ]]; then
    echo "could not place issue in project" >&2; return 1
  fi
  GQL_VARS=$(jq -n --arg p "$project_id" --arg i "$item" --arg f "$status_field" --arg o "$2" \
    '{project:$p, item:$i, field:$f, opt:$o}')
  gql 'mutation($project:ID!,$item:ID!,$field:ID!,$opt:String!){
    updateProjectV2ItemFieldValue(input:{projectId:$project, itemId:$item, fieldId:$field,
      value:{singleSelectOptionId:$opt}}){ projectV2Item{ id } } }' -r '.data!=null' >/dev/null
}

project_item_for() { # $1 issue node_id -> this project's item id, or empty
  GQL_VARS=$(jq -n --arg i "$1" '{id:$i}')
  gql 'query($id:ID!){ node(id:$id){ ... on Issue {
      projectItems(first:20){ nodes{ id project{ id } } } } } }' \
    -r --arg p "$project_id" \
    '[.data.node.projectItems.nodes[] | select(.project.id==$p) | .id][0] // empty'
}

# Existing issues, fetched once. Phase parents are matched on title; plan items
# are matched on the "Plan item <id>." line in their body, which is unique and
# survives a title edit. Both guard against a half-finished run duplicating work
# when the plan markers were not written.
existing=$(rest GET "/repos/$REPO/issues?state=all&per_page=100")
page=2
while [[ $(jq 'length' <<<"$existing") -ge $((100 * (page - 1))) ]]; do
  more=$(rest GET "/repos/$REPO/issues?state=all&per_page=100&page=$page")
  [[ $(jq 'length' <<<"$more") -eq 0 ]] && break
  existing=$(jq -s 'add' <(printf '%s' "$existing") <(printf '%s' "$more"))
  page=$((page + 1))
done
phase_parents=$(jq '[.[] | select(.labels // [] | map(.name) | index("phase"))]' <<<"$existing")

phase_num=""; parent=""
# The loop rewrites $PLAN as it goes, so iterate over an immutable snapshot.
tmp=$(mktemp); cp "$PLAN" "$tmp"
cp "$PLAN" "$PLAN.seed-input"
trap 'rm -f "$tmp" "$tmp.new" "$PLAN.seed-input"' EXIT

while IFS= read -r line; do
  if [[ "$line" =~ ^##\ Phase\ ([0-9]+)\ —\ (.*)$ ]]; then
    phase_num="${BASH_REMATCH[1]}"; phase_title="${BASH_REMATCH[2]}"
    parent=$(jq -r --arg t "Phase $phase_num — " \
      'map(select(.title | startswith($t))) | .[0].number // empty' <<<"$phase_parents")
    if [[ -z "$parent" ]]; then
      body=$(printf 'Tracking issue for plan phase %s.\n\n## Acceptance criteria\n- All sub-issues closed' "$phase_num")
      created=$(rest POST "/repos/$REPO/issues" \
        "$(jq -n --arg t "Phase $phase_num — $phase_title" --arg b "$body" \
           '{title:$t, body:$b, labels:["phase"]}')")
      parent=$(jq -r '.number' <<<"$created")
      add_to_project "$(jq -r '.node_id' <<<"$created")" "$BACKLOG"
      echo "phase $phase_num → #$parent"
    fi

  elif [[ "$line" =~ ^-\ \[([\ x])\]\ ([0-9]+\.[0-9]+)\ (.*)\ —\ specs:\ (.*)$ ]] && [[ "$line" != *"(#"* ]]; then
    done_mark="${BASH_REMATCH[1]}"; id="${BASH_REMATCH[2]}"
    title="${BASH_REMATCH[3]}"; tail="${BASH_REMATCH[4]}"
    # tail is "<specs> — deps: <ids>"; keep them apart so the issue body reads well.
    specs="${tail%% — deps: *}"; deps="${tail#* — deps: }"
    specs=$(printf '%s' "$specs" | sed 's/[[:space:]]*$//')
    deps=$(printf '%s' "$deps" | sed 's/[[:space:]]*$//')
    ac=$(awk -v id="$id" '
      $0 ~ "^- \\[[ x]\\] "id" " {f=1; next}
      f && /^- \[/ {exit}
      f && /^  - AC:/ {sub(/^  - AC: */, "- "); print}' "$PLAN.seed-input")
    [[ -z "$ac" ]] && ac="- Implemented as specified in: $specs"
    body=$(printf '## Why\nPlan item %s.\n\n## Acceptance criteria\n%s\n\n## Spec references\n%s' \
      "$id" "$ac" "$specs")
    if [[ -n "$deps" && "$deps" != "—" ]]; then
      body=$(printf '%s\n\n## Depends on\nPlan items: %s' "$body" "$deps")
    fi

    created=$(jq -r --arg m "Plan item $id." \
      '[.[] | select((.body // "") | contains($m))][0] // empty' <<<"$existing")
    if [[ -n "$created" ]]; then
      num=$(jq -r '.number' <<<"$created")
      echo "adopted $id → #$num (created by an earlier run)"
    else
      created=$(rest POST "/repos/$REPO/issues" \
        "$(jq -n --arg t "$title" --arg b "$body" '{title:$t, body:$b, labels:["agent"]}')")
      num=$(jq -r '.number' <<<"$created")
      child_id=$(jq -r '.id' <<<"$created")

      rest POST "/repos/$REPO/issues/$parent/sub_issues" \
        "$(jq -n --argjson i "$child_id" '{sub_issue_id:$i}')" >/dev/null
      echo "seeded $id → #$num"
    fi

    status="$BACKLOG"; [[ "$phase_num" == "0" ]] && status="$READY"
    add_to_project "$(jq -r '.node_id' <<<"$created")" "$status"

    # Work finished before seeding is recorded as finished: closing the issue
    # lets the project's "Item closed → Done" workflow place it truthfully.
    if [[ "$done_mark" == "x" && "$(jq -r '.state' <<<"$created")" != "closed" ]]; then
      rest PATCH "/repos/$REPO/issues/$num" '{"state":"closed","state_reason":"completed"}' >/dev/null
      echo "  closed #$num: plan item already ticked"
    fi

    sed "s/^- \[\([ x]\)\] $id \(.*\)$/- [\1] $id \2 (#$num)/" "$tmp" > "$tmp.new"
    mv "$tmp.new" "$tmp"
    # Write through after every item: if this run dies, the markers already
    # recorded keep the next run from creating duplicates.
    cp "$tmp" "$PLAN"
  fi
done < "$PLAN.seed-input"

cp "$tmp" "$PLAN"
echo "done. Commit the updated plan: git commit -am 'docs(ralph): link plan items to issues'"
