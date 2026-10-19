# shellcheck shell=bash disable=SC2034 # the scripts that source this use its variables
# Shared setup for the change worker. Sourced, not run. See docs/DEVELOPMENT.md.

worker_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(git -C "$worker_dir" rev-parse --show-toplevel)"
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
