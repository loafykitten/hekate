#!/usr/bin/env bash
# Tests the worker's outcomes against a real Hekate built from this checkout, with fake `gh` and
# `claude` commands each scenario controls. Nothing touches GitHub or starts a Claude session.
# Needs go, jq and sqlite3.   scripts/change-worker/test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
port=$((20000 + RANDOM % 20000))
svc_pid=""
keep=0 # a failed run keeps $tmp for its logs
cleanup() {
  [[ -n "$svc_pid" ]] && kill "$svc_pid" 2>/dev/null || true
  [[ "$keep" == 1 ]] || rm -rf "$tmp"
}
trap cleanup EXIT

# The service, on a fresh database.
(cd "$root/service" && go build -o "$tmp/hekate" ./cmd/hekate)
HEKATE_ADDR="127.0.0.1:$port" HEKATE_DATA_DIR="$tmp/data" HEKATE_STT=off NEURALWATT_API_KEY="" \
  "$tmp/hekate" >"$tmp/service.log" 2>&1 &
svc_pid=$!
for _ in $(seq 50); do curl -fs "127.0.0.1:$port/healthz" >/dev/null && break; sleep 0.2; done
db="$tmp/data/hekate.db"

# The worker's config, state and a clone to make worktrees in.
mkdir -p "$tmp/config/hekate" "$tmp/bin" "$tmp/fake"
echo "HEKATE_URL=http://127.0.0.1:$port" >"$tmp/config/hekate/change-worker.env"
git clone --quiet "$root" "$tmp/repo"
export XDG_CONFIG_HOME="$tmp/config" XDG_STATE_HOME="$tmp/state" CHANGE_WORKER_REPO="$tmp/repo"
fake="$tmp/fake"

# Fake gh: PRs live in $fake/prs.json; $fake/gh-down makes every call fail, $fake/close-fails
# makes closing fail.
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
fake="${FAKE_DIR:?}"
[[ -e "$fake/gh-down" ]] && { echo "gh: network down" >&2; exit 1; }
echo "gh $*" >>"$fake/gh.log"
[[ "$1 $2" == "pr list" ]] && { head="$4"; jq --arg h "$head" '[.[] | select(.headRefName == $h) | {number, state, url}]' "$fake/prs.json"; exit 0; }
if [[ "$1 $2" == "pr close" ]]; then
  [[ -e "$fake/close-fails" ]] && { echo "gh: close failed" >&2; exit 1; }
  jq --argjson n "$3" 'map(if .number == $n then .state = "CLOSED" else . end)' "$fake/prs.json" >"$fake/prs.tmp"
  mv "$fake/prs.tmp" "$fake/prs.json"
  exit 0
fi
if [[ "$1 $2" == "pr view" ]]; then
  pr="$(jq --argjson n "$3" '.[] | select(.number == $n)' "$fake/prs.json")"
  if [[ "${*: -2:1}" == "-q" ]]; then jq -r "${*: -1}" <<<"$pr"; else jq '{headRefName, baseRefName, state}' <<<"$pr"; fi
  exit 0
fi
echo "fake gh: unexpected $*" >&2; exit 1
EOF
# Fake claude: answers each session (build, check) from $fake/<name>.json and logs it to
# $fake/sessions.log; $fake/<name>.fail makes that session crash.
cat >"$tmp/bin/claude" <<'EOF'
#!/usr/bin/env bash
fake="${FAKE_DIR:?}"
while [[ $# -gt 0 ]]; do [[ "$1" == --name ]] && name="${2##*-}"; shift; done
[[ -e "$fake/$name.fail" ]] && exit 1
echo "$name" >>"$fake/sessions.log"
# $fake/check.merges: the final-check session merges the PR, as the real one does.
if [[ "$name" == check && -e "$fake/check.merges" ]]; then
  jq 'map(.state = "MERGED")' "$fake/prs.json" >"$fake/prs.tmp" && mv "$fake/prs.tmp" "$fake/prs.json"
fi
jq '{is_error: false, total_cost_usd: 0, structured_output: .}' "$fake/$name.json"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH" FAKE_DIR="$fake"

failures=0
api() { curl -fsS -X "$1" -H 'Content-Type: application/json' ${3:+--data-binary "$3"} "127.0.0.1:$port$2"; }

# queue: a fresh proposal the owner has allowed. Prints its id.
queue() {
  local n now later
  n="$(sqlite3 "$db" "SELECT COALESCE(MAX(id), 0) + 1 FROM change_requests")" # $(queue) is a subshell
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)" later="$(date -u -v+7d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+7 days' +%Y-%m-%dT%H:%M:%SZ)"
  sqlite3 "$db" "INSERT INTO change_requests (id, title, what, why, kind, created_at, updated_at) VALUES ($n, 'Change $n', 'Do it.', 'Because.', 'feature', '$now', '$now');
    INSERT INTO approvals (id, kind, title, payload, status, created_at, expires_at) VALUES ($n, 'change_request', 'Build: Change $n?', '{\"id\":$n}', 'pending', '$now', '$later');
    UPDATE change_requests SET approval_id = $n WHERE id = $n;"
  api POST "/api/approvals/$n/decide" '{"decision":"once"}' >/dev/null
  rm -f "$fake"/*
  echo '[]' >"$fake/prs.json"
  echo "$n"
}
pr() { # pr ID STATE: the PR the build session "opened" for change ID
  jq --argjson id "$1" --arg s "$2" '. + [{number: (100 + $id), state: $s, url: "https://github.com/example/hekate/pull/\(100 + $id)", headRefName: "change/\($id)", baseRefName: "main"}]' \
    "$fake/prs.json" >"$fake/prs.tmp" && mv "$fake/prs.tmp" "$fake/prs.json"
}
build_opens() { # build_opens ID: the build session reports an open PR
  jq -n --argjson id "$1" '{outcome: "pr_open", summary: "Built it.", issue_url: "https://github.com/example/hekate/issues/\($id)", pr_url: "https://github.com/example/hekate/pull/\(100 + $id)", pr_number: (100 + $id)}' >"$fake/build.json"
}
tick() { "$here/watch.sh" || true; }
expect() { # expect ID STATUS [PR_STATE]
  local got pr_state
  got="$(api GET "/api/changes/$1" | jq -r .status)"
  if [[ "$got" != "$2" ]]; then
    echo "FAIL: change $1 is $got, want $2 ($scenario)"; failures=$((failures + 1)); return
  fi
  if [[ -n "${3:-}" ]]; then
    pr_state="$(jq -r --argjson n $((100 + $1)) '.[] | select(.number == $n) | .state' "$fake/prs.json")"
    [[ "$pr_state" == "$3" ]] || { echo "FAIL: PR for change $1 is $pr_state, want $3 ($scenario)"; failures=$((failures + 1)); return; }
  fi
  echo "ok: $scenario"
}

scenario="build rejects it, no PR"
id="$(queue)"
echo '{"outcome":"rejected","summary":"It already works."}' >"$fake/build.json"
tick; expect "$id" rejected

scenario="final check merges it"
id="$(queue)"; build_opens "$id"; pr "$id" OPEN
echo '{"outcome":"merged","summary":"Shipped."}' >"$fake/check.json"
touch "$fake/check.merges"
tick; expect "$id" merged MERGED
scenario="...through both sessions, with the final check's summary"
got="$(api GET "/api/changes/$id" | jq -r .result)"
if [[ "$(paste -sd, "$fake/sessions.log")" == build,check && "$got" == Shipped. ]]; then
  echo "ok: $scenario"
else
  echo "FAIL: sessions $(paste -sd, "$fake/sessions.log"), result \"$got\" ($scenario)"; failures=$((failures + 1))
fi

scenario="final check rejects but leaves the PR open: the worker closes it"
id="$(queue)"; build_opens "$id"; pr "$id" OPEN
echo '{"outcome":"rejected","summary":"Out of scope."}' >"$fake/check.json"
tick; expect "$id" rejected CLOSED

scenario="final check says merged but the PR is open"
id="$(queue)"; build_opens "$id"; pr "$id" OPEN
echo '{"outcome":"merged","summary":"Shipped."}' >"$fake/check.json"
tick; expect "$id" failed CLOSED

scenario="rejected, the PR won't close: stays working"
id="$(queue)"; build_opens "$id"; pr "$id" OPEN
echo '{"outcome":"rejected","summary":"Out of scope."}' >"$fake/check.json"
touch "$fake/close-fails"
tick; expect "$id" working OPEN
scenario="...and the next tick settles it once closing works"
rm "$fake/close-fails"
tick; expect "$id" failed CLOSED

scenario="build crashes with GitHub down: stays working"
id="$(queue)"; touch "$fake/build.fail" "$fake/gh-down"
tick; expect "$id" working
scenario="...and the next tick settles it once GitHub is back"
rm "$fake/gh-down"
tick; expect "$id" failed

scenario="interrupted after merging: the next tick records the merge"
id="$(queue)"
api POST "/api/changes/$id/claim" >/dev/null # claimed, then the machine restarted
pr "$id" MERGED
tick; expect "$id" merged MERGED

if [[ "$failures" -gt 0 ]]; then
  echo "$failures failed; logs in $tmp (service.log, state/hekate-change-worker/runs/)" >&2
  keep=1
  exit 1
fi
echo "all passed"
