#!/usr/bin/env bash
# Picks up the oldest change request the owner allowed and runs it. launchd starts this every
# 90 seconds (see install.sh); one change runs at a time.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

mkdir -p "$state"
lock="$state/lock"
if ! mkdir "$lock" 2>/dev/null; then
  pid="$(cat "$lock/pid" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    exit 0 # a change is already running
  fi
  log "clearing a stale lock (pid ${pid:-unknown})"
  rm -rf "$lock"
  mkdir "$lock"
fi
echo $$ >"$lock/pid"
trap 'rm -rf "$lock"' EXIT

# Holding the lock means no run is live, so anything still working was cut off (a restart,
# a crash). Close it out before taking new work; run.sh would have reported it otherwise.
working="$(api GET '/api/changes?status=working')" || { log "cannot reach Hekate"; exit 0; }
for stale in $(jq -r '.changes[].id' <<<"$working"); do
  if ! prs="$(cd "$repo" && gh pr list --head "change/$stale" --state all --json number,state,url)"; then
    log "change $stale: cut off, but GitHub can't be reached; trying again next time"
    exit 0 # no new work until it's settled
  fi
  merged="$(jq -r '[.[] | select(.state == "MERGED")][0].url // empty' <<<"$prs")"
  if [[ -n "$merged" ]]; then
    log "change $stale: was cut off after merging"
    "$worker_dir/report.sh" finish "$stale" merged "Merged; the worker was interrupted before it could confirm the deploy." --pr "$merged" || true
  else
    log "change $stale: was cut off, marking it failed"
    "$worker_dir/report.sh" finish "$stale" failed "The worker was interrupted (the machine restarted or the run crashed)." || true
    for n in $(jq -r '.[] | select(.state == "OPEN") | .number' <<<"$prs"); do
      (cd "$repo" && gh pr close "$n" --delete-branch --comment "The change worker was interrupted before finishing this change.") || true
    done
  fi
  git -C "$repo" worktree remove --force "$state/worktrees/change-$stale" 2>/dev/null || true
  git -C "$repo" branch -D "change/$stale" >/dev/null 2>&1 || true
done

queued="$(api GET '/api/changes?status=queued')" || { log "cannot reach Hekate"; exit 0; }
id="$(jq -r '.changes | last | .id // empty' <<<"$queued")" # newest first, so last is oldest
[[ -n "$id" ]] || exit 0

if ! api POST "/api/changes/$id/claim" >/dev/null; then
  log "change $id: could not claim it"
  exit 0
fi
log "change $id: claimed"
"$worker_dir/run.sh" "$id"
