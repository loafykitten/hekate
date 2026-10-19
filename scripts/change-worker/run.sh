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
    finish failed "The worker stopped while $step. The log is on the development machine." || true
  fi
  if [[ -d "$wt" ]]; then
    git -C "$repo" worktree remove --force "$wt" || true
  fi
  git -C "$repo" branch -D "$branch" >/dev/null 2>&1 || true
}
trap on_exit EXIT

# finish OUTCOME RESULT [--issue URL] [--pr URL]: reports how the change ended. Anything but a
# merge first closes the branch's PR and confirms it closed (and becomes a merge if GitHub says
# it merged after all). When GitHub can't confirm, the change stays working for watch.sh.
finish() {
  local outcome="$1" result="$2" merged
  shift 2
  if [[ "$outcome" != merged ]]; then
    if ! merged="$(settle_prs "$branch")"; then
      log "change $id: cannot confirm its PR is closed; leaving it for watch.sh"
      finished=1
      return 0
    fi
    if [[ -n "$merged" ]]; then
      outcome=merged result="Merged, although the worker reported: $result"
      set -- "$@" --pr "$merged"
    fi
  fi
  "$report" finish "$id" "$outcome" "$result" "$@"
  finished=1
  log "change $id: finished $outcome"
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

# Trust GitHub, not the session's word, for whether it merged. A rejection goes through finish,
# which closes the PR if the session didn't and notices if it merged after all.
step="confirming the outcome"
state_now="$(cd "$wt" && gh pr view "$pr" --json state -q .state)"
case "$outcome:$state_now" in
  merged:MERGED) finish merged "$summary" ${links[@]+"${links[@]}"} ;;
  merged:*) finish failed "The final check said it merged, but the pull request is $(tr "[:upper:]" "[:lower:]" <<<"$state_now")." ${links[@]+"${links[@]}"} ;;
  *) finish rejected "$summary" ${links[@]+"${links[@]}"} ;;
esac
