# shellcheck shell=bash disable=SC2034 # the scripts that source this use its variables
# Shared setup for the change worker. Sourced, not run. See docs/DEVELOPMENT.md.

worker_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="${CHANGE_WORKER_REPO:-$(git -C "$worker_dir" rev-parse --show-toplevel)}" # tests point it at a clone
config="${XDG_CONFIG_HOME:-$HOME/.config}/hekate/change-worker.env"
state="${XDG_STATE_HOME:-$HOME/.local/state}/hekate-change-worker"

if [[ ! -f "$config" ]]; then
  echo "change worker: $config is missing; see docs/DEVELOPMENT.md" >&2
  exit 1
fi
# shellcheck source=/dev/null
source "$config"
: "${HEKATE_URL:?set HEKATE_URL in $config}"
HEKATE_URL="${HEKATE_URL%/}"
export HEKATE_URL

# Logs go to stderr, so a log line inside $(...) isn't captured as output.
log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

# api METHOD PATH [JSON]: one call to Hekate. Prints the body; fails on any non-2xx.
api() {
  local args=(-fsS --max-time 30 -X "$1" -H 'Content-Type: application/json')
  [[ $# -ge 3 ]] && args+=(--data-binary "$3")
  curl "${args[@]}" "$HEKATE_URL$2"
}

# settle_prs BRANCH: closes any open PR from BRANCH and confirms GitHub shows none open. Prints
# the URL of a merged PR from it, if there is one. Fails when GitHub can't be reached or a PR
# stays open; the caller then leaves the change working so watch.sh tries again.
settle_prs() {
  local branch="$1" prs n
  prs="$(cd "$repo" && gh pr list --head "$branch" --state all --json number,state,url)" || return 1
  for n in $(jq -r '.[] | select(.state == "OPEN") | .number' <<<"$prs"); do
    # A fixed comment: the PR is public, and the reason may not be. The exit status isn't trusted
    # either way: gh can close the PR and then fail to delete a local branch a worktree holds.
    (cd "$repo" && gh pr close "$n" --delete-branch \
      --comment "Closed by the change worker without merging; the outcome is recorded in Hekate.") >&2 || true
  done
  prs="$(cd "$repo" && gh pr list --head "$branch" --state all --json number,state,url)" || return 1
  jq -e 'all(.[]; .state != "OPEN")' <<<"$prs" >/dev/null || return 1
  # gh deletes the local branch before the remote one, so a failure there leaves the remote
  # branch behind, and a retry would rebuild it from main and fail to push. Its commits stay
  # reachable through the closed PR (refs/pull/N/head).
  local found=0
  git -C "$repo" ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1 || found=$?
  case "$found" in
    0) git -C "$repo" push --quiet origin --delete "$branch" >&2 || return 1 ;;
    2) ;; # no such branch
    *) return 1 ;; # couldn't ask: try again later
  esac
  jq -r '[.[] | select(.state == "MERGED")][0].url // empty' <<<"$prs"
}
