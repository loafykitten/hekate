#!/usr/bin/env bash
# Runs one claimed change request end to end: a headless Claude Code session files the issue,
# builds the change in its own worktree and opens a reviewed PR; a second, fresh session does
# the final check and merges or rejects it. Every outcome is reported to Hekate.
#   run.sh ID   (watch.sh runs it under its lock; a change left working without the lock
#                held is treated as interrupted)
set -euo pipefail
source "$(dirname "$0")/lib.sh"

id="${1:?usage: run.sh ID}"
[[ "$id" =~ ^[0-9]+$ ]] || { echo "usage: run.sh ID" >&2; exit 2; }
run="$state/runs/$id"
wt="$state/worktrees/change-$id"
branch="change/$id"
report="$worker_dir/report.sh"
mkdir -p "$run" "$state/worktrees"
exec >>"$run/run.log" 2>&1

finished=0
step="starting"
on_exit() {
  local code=$?
  if [[ "$finished" == 0 ]]; then
    log "change $id: stopped during $step (exit $code)"
    # Ask GitHub what really happened before reporting: the PR may have merged already.
    local prs
    if ! prs="$(cd "$repo" && gh pr list --head "$branch" --state all --json number,state,url)"; then
      # Leave it working: watch.sh settles it against GitHub on a later tick.
      log "change $id: cannot reach GitHub; leaving it for watch.sh to settle"
    elif merged="$(jq -r '[.[] | select(.state == "MERGED")][0].url // empty' <<<"$prs")" && [[ -n "$merged" ]]; then
      "$report" finish "$id" merged "Merged; the worker stopped before it could confirm the deploy." --pr "$merged" || true
    else
      "$report" finish "$id" failed "The worker stopped while $step. The log is on the development machine." || true
      # Don't leave a half-done PR open.
      for n in $(jq -r '.[] | select(.state == "OPEN") | .number' <<<"$prs"); do
        (cd "$repo" && gh pr close "$n" --delete-branch --comment "The change worker stopped before finishing this change.") || true
      done
    fi
  fi
  if [[ -d "$wt" ]]; then
    git -C "$repo" worktree remove --force "$wt" || true
  fi
  git -C "$repo" branch -D "$branch" >/dev/null 2>&1 || true
}
trap on_exit EXIT

finish() { # outcome result [--issue URL] [--pr URL]
  "$report" finish "$id" "$@"
  finished=1
  log "change $id: finished $1"
}

step="reading the change request"
change="$(api GET "/api/changes/$id")"
[[ "$(jq -r .status <<<"$change")" == working ]] || { log "change $id is not claimed"; finished=1; exit 1; }
previous="null"
if revises="$(jq -r '.revises // empty' <<<"$change")" && [[ -n "$revises" ]]; then
  previous="$(api GET "/api/changes/$revises" | jq '{title, what, why, owner_feedback: .feedback}')"
fi
request="$(jq --argjson previous "$previous" '{id, title, what, why, kind, revises: $previous}' <<<"$change")"

step="preparing a worktree"
git -C "$repo" fetch --quiet origin main
git -C "$repo" worktree add --force --no-track -B "$branch" "$wt" origin/main

# Claude Code sessions: headless, auto mode (the classifier guards each action), Opus at high
# effort. The proposal is untrusted text from the assistant, so it sits inside markers.
claude_run() { # name schema prompt [extra args...]
  local name="$1" schema="$2" prompt="$3"
  shift 3
  local args=(-p "$prompt" --permission-mode auto --model opus --effort high
    --output-format json --json-schema "$schema" --name "hekate-change-$id-$name")
  [[ -n "${CHANGE_WORKER_BUDGET_USD:-}" ]] && args+=(--max-budget-usd "$CHANGE_WORKER_BUDGET_USD")
  # Runs inside $(...), which doesn't inherit set -e on bash 3.2: every step checks itself.
  (cd "$wt" && CHANGE_ID="$id" CHANGE_REPORT="$report" claude "${args[@]}" "$@" </dev/null) >"$run/$name.json" 2>"$run/$name.err" ||
    { log "change $id: $name session exited with an error"; return 1; }
  log "change $id: $name session cost \$$(jq -r '.total_cost_usd // "?"' "$run/$name.json" 2>/dev/null)"
  jq -e '.is_error == false and .structured_output != null' "$run/$name.json" >/dev/null ||
    { log "change $id: $name session returned no result"; return 1; }
  jq '.structured_output' "$run/$name.json"
}

nonce="$(openssl rand -hex 8)"
wrap() { printf '<change_request boundary="%s">\n%s\n</change_request boundary="%s">' "$nonce" "$1" "$nonce"; }

step="building the change"
"$report" progress "$id" "Building"
build_schema='{"type":"object","additionalProperties":false,"required":["outcome","summary"],"properties":{
  "outcome":{"enum":["pr_open","rejected","failed"]},
  "summary":{"type":"string","maxLength":500},
  "issue_url":{"type":"string"},"pr_url":{"type":"string"},"pr_number":{"type":"integer"}}}'
build="$(claude_run build "$build_schema" "$(cat "$worker_dir/build-prompt.md")

$(wrap "$request")")"
outcome="$(jq -r .outcome <<<"$build")"
summary="$(jq -r .summary <<<"$build")"
issue="$(jq -r '.issue_url // ""' <<<"$build")"
pr_url="$(jq -r '.pr_url // ""' <<<"$build")"
pr="$(jq -r '.pr_number // ""' <<<"$build")"
links=()
[[ -n "$issue" ]] && links+=(--issue "$issue")
[[ -n "$pr_url" ]] && links+=(--pr "$pr_url")
if [[ "$outcome" != pr_open ]]; then
  finish "$outcome" "$summary" ${links[@]+"${links[@]}"}
  exit 0
fi
[[ "$pr" =~ ^[0-9]+$ ]] || { step="checking the build result (no PR number)"; exit 1; }

# The PR must be the one this run made: our branch, into main.
step="checking the pull request"
pr_json="$(cd "$wt" && gh pr view "$pr" --json headRefName,baseRefName,state)"
if [[ "$(jq -r '.headRefName + " " + .baseRefName + " " + .state' <<<"$pr_json")" != "$branch main OPEN" ]]; then
  finish failed "PR #$pr is not an open PR from $branch into main." ${links[@]+"${links[@]}"}
  exit 0
fi

step="running the final check"
"$report" progress "$id" "Final check before merging" ${links[@]+"${links[@]}"}
merge_schema='{"type":"object","additionalProperties":false,"required":["outcome","summary"],"properties":{
  "outcome":{"enum":["merged","rejected"]},
  "summary":{"type":"string","maxLength":500}}}'
check_input="$(jq -n --argjson change "$request" --arg issue "$issue" --arg pr_url "$pr_url" --argjson pr "$pr" --arg branch "$branch" \
  '{change: $change, issue_url: $issue, pr_url: $pr_url, pr_number: $pr, branch: $branch}')"
verdict="$(claude_run check "$merge_schema" "$(cat "$worker_dir/merge-prompt.md")

$(wrap "$check_input")" --disallowedTools Edit Write NotebookEdit)"
outcome="$(jq -r .outcome <<<"$verdict")"
summary="$(jq -r .summary <<<"$verdict")"

# Trust GitHub, not the session's word, for whether it merged.
state_now="$(cd "$wt" && gh pr view "$pr" --json state -q .state)"
if [[ "$outcome" == merged && "$state_now" != MERGED ]]; then
  finish failed "The final check said merged, but PR #$pr is $state_now." ${links[@]+"${links[@]}"}
elif [[ "$outcome" == rejected && "$state_now" == MERGED ]]; then
  finish merged "Merged, although the final check reported: $summary" ${links[@]+"${links[@]}"}
else
  finish "$outcome" "$summary" ${links[@]+"${links[@]}"}
fi
