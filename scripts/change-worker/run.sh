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
# On a retry the issue stays, and the last attempt's outcome and PR come along.
request="$(jq --argjson previous "$previous" '{id, title, what, why, kind, revises: $previous}
  + (if .attempts > 1 then {attempt: .attempts, issue_url, previous_result, previous_pr_url} else {} end)' <<<"$change")"

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

# Commands the tests replace: the review gate and how long to wait for a deploy.
review="${CHANGE_WORKER_REVIEW:-scripts/ai-review.sh}"
deploy_wait="${CHANGE_WORKER_DEPLOY_WAIT:-300}"

# sync: brings the branch up to date with main, which may have moved since the build (another
# change, or someone merging by hand). Conflicts go to a short Claude session that resolves them
# and reruns the gates. Anything new is pushed, reviewed and checked again before the final check.
sync_schema='{"type":"object","additionalProperties":false,"required":["outcome","summary"],"properties":{
  "outcome":{"enum":["synced","failed"]},
  "summary":{"type":"string","maxLength":500}}}'
sync() {
  local astra=()
  git -C "$wt" fetch --quiet origin main || return 1
  git -C "$wt" merge-base --is-ancestor origin/main HEAD && return 0 # already current
  log "change $id: main moved; bringing the branch up to date"
  "$report" progress "$id" "Updating to the latest main" ${links[@]+"${links[@]}"} || true
  if ! git -C "$wt" merge --quiet --no-edit origin/main; then
    git -C "$wt" merge --abort || return 1
    local resolved
    resolved="$(claude_run "sync-$round" "$sync_schema" "$(cat "$worker_dir/sync-prompt.md")

$(wrap "$(jq -n --argjson pr "$pr" --arg branch "$branch" '{pr_number: $pr, branch: $branch}')")")" || return 1
    if [[ "$(jq -r .outcome <<<"$resolved")" != synced ]]; then
      sync_failure="$(jq -r .summary <<<"$resolved")"
      return 1
    fi
    git -C "$wt" merge-base --is-ancestor origin/main HEAD || { sync_failure="The update didn't include the latest main."; return 1; }
    astra=(--astra) # resolved conflicts are new code: the full review
  fi
  git -C "$wt" push --quiet origin "HEAD:$branch" || return 1
  "$report" progress "$id" "Updated to the latest main; reviewing again" ${links[@]+"${links[@]}"} || true
  (cd "$wt" && "$review" ${astra[@]+"${astra[@]}"}) || { sync_failure="The review after updating to the latest main found problems."; return 1; }
  (cd "$wt" && gh pr checks "$pr" --watch --required --interval 20 >/dev/null) || { sync_failure="Checks failed after updating to the latest main."; return 1; }
}

check_schema='{"type":"object","additionalProperties":false,"required":["verdict","summary"],"properties":{
  "verdict":{"enum":["approve","reject"]},
  "summary":{"type":"string","maxLength":500}}}'
merged=0
for round in 1 2 3; do
  step="bringing the branch up to date"
  sync_failure=""
  if ! sync; then
    [[ -n "$sync_failure" ]] && { finish failed "$sync_failure" ${links[@]+"${links[@]}"}; exit 0; }
    exit 1
  fi
  head="$(git -C "$wt" rev-parse HEAD)"

  step="running the final check"
  "$report" progress "$id" "Final check before merging" ${links[@]+"${links[@]}"}
  check_input="$(jq -n --argjson change "$request" --arg issue "$issue" --arg pr_url "$pr_url" --argjson pr "$pr" --arg branch "$branch" --arg head "$head" \
    '{change: $change, issue_url: $issue, pr_url: $pr_url, pr_number: $pr, branch: $branch, head_commit: $head}')"
  verdict="$(claude_run "check-$round" "$check_schema" "$(cat "$worker_dir/merge-prompt.md")

$(wrap "$check_input")" --disallowedTools Edit Write NotebookEdit)"
  summary="$(jq -r .summary <<<"$verdict")"
  if [[ "$(jq -r .verdict <<<"$verdict")" != approve ]]; then
    finish rejected "$summary" ${links[@]+"${links[@]}"} # closes the PR
    exit 0
  fi

  # Merge exactly what was checked: if main moved meanwhile, GitHub refuses and we go round again.
  step="merging"
  if (cd "$wt" && gh pr merge "$pr" --squash --delete-branch --match-head-commit "$head" >/dev/null); then
    merged=1
    break
  fi
  [[ "$(cd "$wt" && gh pr view "$pr" --json state -q .state)" == MERGED ]] && { merged=1; break; }
  log "change $id: merge refused (main moved?); round $round of 3"
done
if [[ "$merged" == 0 ]]; then
  finish failed "Main kept moving while this change waited to merge, three times over. Try it again when things are quieter." ${links[@]+"${links[@]}"}
  exit 0
fi

# Merging deploys. Wait for the service to report the merge commit (the deployer rolls back a
# deploy that fails its health check, which then never shows up here).
step="confirming the deploy"
"$report" progress "$id" "Merged; waiting for the deploy" ${links[@]+"${links[@]}"} || true
commit="$(cd "$wt" && gh pr view "$pr" --json mergeCommit -q .mergeCommit.oid)"
deployed=0
for _ in $(seq $((deploy_wait / 15 + 1))); do
  [[ "$(curl -fsS --max-time 10 "$HEKATE_URL/healthz" | jq -r .commit 2>/dev/null)" == "$commit" ]] && { deployed=1; break; }
  sleep $((deploy_wait < 15 ? deploy_wait : 15))
done
if [[ "$deployed" == 1 ]]; then
  finish merged "$summary It's live." ${links[@]+"${links[@]}"}
else
  finish merged "$summary It's merged, but the deploy didn't show up within $((deploy_wait / 60)) minutes; check the deploy log." ${links[@]+"${links[@]}"}
fi
