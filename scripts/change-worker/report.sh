#!/usr/bin/env bash
# Report a change request's progress or outcome to Hekate. The worker sessions call it too.
#   report.sh progress ID STAGE [--issue URL] [--pr URL]
#   report.sh finish ID merged|rejected|failed RESULT [--issue URL] [--pr URL]
set -euo pipefail
source "$(dirname "$0")/lib.sh"

usage() { echo "usage: $0 progress ID STAGE [--issue URL] [--pr URL] | finish ID OUTCOME RESULT [--issue URL] [--pr URL]" >&2; exit 2; }
[[ $# -ge 3 ]] || usage
what="$1" id="$2"
[[ "$id" =~ ^[0-9]+$ ]] || usage
case "$what" in
  progress) stage="$3"; shift 3 ;;
  finish) [[ $# -ge 4 ]] || usage; outcome="$3" result="$4"; shift 4 ;;
  *) usage ;;
esac
issue="" pr=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --issue) issue="${2:?}"; shift 2 ;;
    --pr) pr="${2:?}"; shift 2 ;;
    *) usage ;;
  esac
done

if [[ "$what" == progress ]]; then
  body="$(jq -n --arg stage "$stage" --arg issue "$issue" --arg pr "$pr" \
    '{stage: $stage} + (if $issue != "" then {issue_url: $issue} else {} end) + (if $pr != "" then {pr_url: $pr} else {} end)')"
else
  body="$(jq -n --arg outcome "$outcome" --arg result "$result" --arg issue "$issue" --arg pr "$pr" \
    '{outcome: $outcome, result: $result} + (if $issue != "" then {issue_url: $issue} else {} end) + (if $pr != "" then {pr_url: $pr} else {} end)')"
fi
api POST "/api/changes/$id/$what" "$body" >/dev/null
