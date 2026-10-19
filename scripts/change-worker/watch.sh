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

queued="$(api GET '/api/changes?status=queued')" || { log "cannot reach Hekate"; exit 0; }
id="$(jq -r '.changes | last | .id // empty' <<<"$queued")" # newest first, so last is oldest
[[ -n "$id" ]] || exit 0

if ! api POST "/api/changes/$id/claim" >/dev/null; then
  log "change $id: could not claim it"
  exit 0
fi
log "change $id: claimed"
"$worker_dir/run.sh" "$id"
