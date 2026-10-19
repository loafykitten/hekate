#!/usr/bin/env bash
# Pull-based deployer for the homelab. Every INTERVAL seconds it pulls IMAGE:main; when the
# image differs from what's running it restarts the service, waits for /healthz to report the
# image's commit, and rolls back to the previous image if it doesn't. Each attempt is appended to
# $STATE_DIR/deploy.log as a JSON line (Hekate reads this file about itself).
set -uo pipefail

: "${STACK_DIR:?STACK_DIR must be the compose directory, mounted at the same path as on the host}"
IMAGE="${IMAGE:-ghcr.io/loafykitten/hekate}"
TAG="${TAG:-main}"
SERVICE="${SERVICE:-hekate}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:3200/healthz}"
INTERVAL="${INTERVAL:-60}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-90}"
STATE_DIR="${STATE_DIR:-$STACK_DIR/state}"
PREVIOUS="hekate-local:previous"

mkdir -p "$STATE_DIR"
cd "$STACK_DIR" || exit 1

log() { # event, then key=value pairs
  local event="$1"; shift
  local args=(--arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg event "$event")
  # shellcheck disable=SC2016 # jq variables, expanded by jq
  local fields='{ts: $ts, event: $event}'
  for kv in "$@"; do
    local k="${kv%%=*}"
    args+=(--arg "$k" "${kv#*=}")
    fields="$fields + {\"$k\": \$$k}"
  done
  jq -nc "${args[@]}" "$fields" | tee -a "$STATE_DIR/deploy.log"
}

compose() { docker compose "$@"; }
running_image() { docker inspect -f '{{.Image}}' "$(compose ps -q "$SERVICE" 2>/dev/null)" 2>/dev/null; }
image_commit() { docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$1" 2>/dev/null; }

healthy() { # expected commit (may be empty)
  local expected="$1" deadline=$((SECONDS + HEALTH_TIMEOUT)) body
  while ((SECONDS < deadline)); do
    if body="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null)" \
      && [[ "$(jq -r .ok <<<"$body")" == "true" ]] \
      && { [[ -z "$expected" ]] || [[ "$(jq -r .commit <<<"$body")" == "$expected" ]]; }; then
      return 0
    fi
    sleep 3
  done
  return 1
}

deploy_once() {
  [[ -f "$STATE_DIR/paused" ]] && return 0
  docker pull -q "$IMAGE:$TAG" >/dev/null 2>&1 || { log pull_failed image="$IMAGE:$TAG"; return 0; }
  local target current commit
  target="$(docker image inspect -f '{{.Id}}' "$IMAGE:$TAG")"
  current="$(running_image || true)"
  [[ "$target" == "$current" ]] && return 0
  [[ -f "$STATE_DIR/bad-image" && "$(cat "$STATE_DIR/bad-image")" == "$target" ]] && return 0

  commit="$(image_commit "$target")"
  log deploy_started commit="$commit" image="$target" previous="${current:-none}"
  [[ -n "$current" ]] && docker tag "$current" "$PREVIOUS"

  if HEKATE_IMAGE="$IMAGE:$TAG" compose up -d --no-deps "$SERVICE" >/dev/null 2>&1 && healthy "$commit"; then
    rm -f "$STATE_DIR/bad-image"
    log deploy_succeeded commit="$commit"
    docker image prune -f >/dev/null 2>&1 || true
    return 0
  fi

  echo "$target" >"$STATE_DIR/bad-image"
  local logs
  logs="$(docker logs --tail 20 "$(compose ps -aq "$SERVICE")" 2>&1 | tail -c 2000)"
  if [[ -n "$current" ]]; then
    HEKATE_IMAGE="$PREVIOUS" compose up -d --no-deps "$SERVICE" >/dev/null 2>&1
    if healthy ""; then
      log deploy_failed_rolled_back commit="$commit" tail="$logs"
    else
      log rollback_failed commit="$commit" tail="$logs"
    fi
  else
    log deploy_failed commit="$commit" tail="$logs"
  fi
}

log deployer_started image="$IMAGE:$TAG" interval="$INTERVAL"
while true; do
  deploy_once
  sleep "$INTERVAL"
done
