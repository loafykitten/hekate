#!/usr/bin/env bash
# Tests the worker's outcomes against a real Hekate built from this checkout, with fake `gh`,
# `claude` and review commands each scenario controls, and a throwaway git "origin" whose main
# can move mid-run. Nothing touches GitHub or starts a Claude session.
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

# A bare origin (its main can move), the clone the worker makes worktrees in, and a second clone
# that plays "someone else merging".
mkdir -p "$tmp/config/hekate" "$tmp/bin" "$tmp/fake"
echo "HEKATE_URL=http://127.0.0.1:$port" >"$tmp/config/hekate/change-worker.env"
git clone --quiet --bare "$root" "$tmp/origin.git"
git -C "$tmp/origin.git" update-ref refs/heads/main "$(git -C "$root" rev-parse HEAD)"
git clone --quiet "$tmp/origin.git" "$tmp/repo"
git clone --quiet "$tmp/origin.git" "$tmp/mover"
fake="$tmp/fake"
export XDG_CONFIG_HOME="$tmp/config" XDG_STATE_HOME="$tmp/state" CHANGE_WORKER_REPO="$tmp/repo" \
  CHANGE_WORKER_REVIEW="$tmp/bin/review" CHANGE_WORKER_DEPLOY_WAIT=0 \
  GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

# move-main FILE TEXT: someone else lands a commit on main.
cat >"$tmp/bin/move-main" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$tmp/mover" && git pull --quiet origin main
echo "\$2" >"\$1" && git add "\$1" && git commit --quiet -m "elsewhere: \$1" && git push --quiet origin HEAD:main
EOF

# Fake gh. PRs live in $fake/prs.json. $fake/gh-down fails every call, $fake/close-fails fails
# closing. Merging works like branch protection: only a branch that contains main merges.
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
fake="${FAKE_DIR:?}"
[[ -e "$fake/gh-down" ]] && { echo "gh: network down" >&2; exit 1; }
echo "gh $*" >>"$fake/gh.log"
set_state() { jq --argjson n "$1" --arg s "$2" 'map(if .number == $n then .state = $s else . end)' "$fake/prs.json" >"$fake/prs.tmp" && mv "$fake/prs.tmp" "$fake/prs.json"; }
case "$1 $2" in
  "pr list") jq --arg h "$4" '[.[] | select(.headRefName == $h) | {number, state, url}]' "$fake/prs.json" ;;
  "pr close")
    [[ -e "$fake/close-fails" ]] && { echo "gh: close failed" >&2; exit 1; }
    set_state "$3" CLOSED
    # As the real gh does: the local branch goes first, and when a worktree holds it that fails,
    # leaving the remote branch in place; otherwise the remote branch is deleted too.
    if [[ -e "$fake/close-delete-fails" ]]; then echo "failed to delete local branch" >&2; exit 1; fi
    head="$(jq -r --argjson n "$3" '.[] | select(.number == $n) | .headRefName' "$fake/prs.json")"
    git push --quiet origin --delete "$head" 2>/dev/null || true ;;
  "pr checks") exit 0 ;;
  "pr merge")
    sha="${*: -1}"
    git fetch --quiet origin main
    git merge-base --is-ancestor origin/main "$sha" || { echo "gh: base branch was modified" >&2; exit 1; }
    set_state "$3" MERGED ;;
  "pr view")
    pr="$(jq --argjson n "$3" '.[] | select(.number == $n) | . + {mergeCommit: {oid: "abc123"}}' "$fake/prs.json")"
    if [[ "${*: -2:1}" == "-q" ]]; then jq -r "${*: -1}" <<<"$pr"; else jq '{headRefName, baseRefName, state}' <<<"$pr"; fi ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
EOF
# Fake claude. Each session (build, check-N, sync-N) logs itself and its prompt, then answers
# from $fake/<build|check|sync>.json; $fake/<kind>.fail makes it crash. The build commits a file
# and pushes the branch; $fake/conflict makes main change that file too, $fake/move-clean moves
# main elsewhere, and $fake/move-during-check moves it while the final check runs.
cat >"$tmp/bin/claude" <<EOF
#!/usr/bin/env bash
fake="\${FAKE_DIR:?}"
prompt=""
while [[ \$# -gt 0 ]]; do
  case "\$1" in --name) name="\${2#hekate-change-*-}" ;; -p) prompt="\$2" ;; esac
  shift
done
kind="\${name%%-*}"
echo "\$name" >>"\$fake/sessions.log"
printf '%s' "\$prompt" >"\$fake/\$name.prompt"
[[ -e "\$fake/\$kind.fail" ]] && exit 1
# Only the JSON answer goes to stdout, as with the real claude.
case "\$kind" in
  build)
    { echo "change \$CHANGE_ID \$RANDOM" >"change-\$CHANGE_ID.txt" && git add . && git commit --quiet -m build && git push --quiet origin "HEAD:change/\$CHANGE_ID"; } >&2 || exit 1
    [[ -e "\$fake/conflict" ]] && "$tmp/bin/move-main" "change-\$CHANGE_ID.txt" "main's version"
    [[ -e "\$fake/move-clean" ]] && "$tmp/bin/move-main" "elsewhere-\$CHANGE_ID.txt" "unrelated" ;;
  sync)
    { git fetch --quiet origin main && git merge --quiet -X ours --no-edit origin/main; } >&2 ;;
  check)
    [[ -e "\$fake/move-during-check" ]] && { rm "\$fake/move-during-check"; "$tmp/bin/move-main" "late-\$CHANGE_ID.txt" "late"; }
    # Hekate goes away: point the worker's config at a closed port until the test restores it.
    [[ -e "\$fake/hekate-down" ]] && echo "HEKATE_URL=http://127.0.0.1:1" >"$tmp/config/hekate/change-worker.env" ;;
esac
jq '{is_error: false, total_cost_usd: 0, structured_output: .}' "\$fake/\$kind.json"
EOF
# Fake review: logs how it was run; $fake/review-fails fails it.
cat >"$tmp/bin/review" <<'EOF'
#!/usr/bin/env bash
echo "review $*" >>"${FAKE_DIR:?}/review.log"
[[ ! -e "$FAKE_DIR/review-fails" ]]
EOF
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" FAKE_DIR="$fake"

failures=0
api() { curl -fsS -X "$1" -H 'Content-Type: application/json' ${3:+--data-binary "$3"} "127.0.0.1:$port$2"; }
fail() { echo "FAIL: $* ($scenario)"; failures=$((failures + 1)); }

# queue: a fresh proposal the owner has allowed, with a clean fake slate. Prints its id.
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
  echo '{"verdict":"approve","summary":"Shipped."}' >"$fake/check.json"
  echo '{"outcome":"synced","summary":"Merged main."}' >"$fake/sync.json"
  echo "$n"
}
pr() { # pr ID STATE [NUMBER]: the PR the build session "opened" for change ID
  local num="${3:-$((100 + $1))}"
  jq --argjson id "$1" --argjson n "$num" --arg s "$2" '. + [{number: $n, state: $s, url: "https://github.com/example/hekate/pull/\($n)", headRefName: "change/\($id)", baseRefName: "main"}]' \
    "$fake/prs.json" >"$fake/prs.tmp" && mv "$fake/prs.tmp" "$fake/prs.json"
}
build_opens() { # build_opens ID [NUMBER]: the build session reports an open PR
  local num="${2:-$((100 + $1))}"
  jq -n --argjson id "$1" --argjson n "$num" '{outcome: "pr_open", summary: "Built it.", issue_url: "https://github.com/example/hekate/issues/\($id)", pr_url: "https://github.com/example/hekate/pull/\($n)", pr_number: $n}' >"$fake/build.json"
  pr "$1" OPEN "$num"
}
tick() { "$here/watch.sh" || true; }
field() { api GET "/api/changes/$1" | jq -r ".$2"; }
expect() { # expect ID STATUS [PR_STATE]
  local got pr_state
  got="$(field "$1" status)"
  [[ "$got" == "$2" ]] || { fail "change $1 is $got, want $2"; return; }
  if [[ -n "${3:-}" ]]; then
    pr_state="$(jq -r --argjson n $((100 + $1)) '.[] | select(.number == $n) | .state' "$fake/prs.json")"
    [[ "$pr_state" == "$3" ]] || { fail "PR for change $1 is $pr_state, want $3"; return; }
  fi
  echo "ok: $scenario"
}
sessions() { paste -sd, "$fake/sessions.log"; }

scenario="build rejects it, no PR"
id="$(queue)"
echo '{"outcome":"rejected","summary":"It already works."}' >"$fake/build.json"
tick; expect "$id" rejected

scenario="approved and merged, main never moved"
id="$(queue)"; build_opens "$id"
tick; expect "$id" merged MERGED
[[ "$(sessions)" == build,check-1 && ! -e "$fake/review.log" ]] || fail "sessions $(sessions), reviews $(cat "$fake/review.log" 2>/dev/null)"
[[ "$(field "$id" result)" == Shipped.* ]] || fail "result $(field "$id" result)"

scenario="final check rejects: the worker closes the PR"
id="$(queue)"; build_opens "$id"
echo '{"verdict":"reject","summary":"Out of scope."}' >"$fake/check.json"
tick; expect "$id" rejected CLOSED

scenario="main moved cleanly after the build: updated, reviewed again, merged"
id="$(queue)"; build_opens "$id"; touch "$fake/move-clean"
tick; expect "$id" merged MERGED
[[ "$(cat "$fake/review.log")" == "review " ]] || fail "reviews: $(cat "$fake/review.log" 2>/dev/null)"
git -C "$tmp/origin.git" merge-base --is-ancestor "$(git -C "$tmp/origin.git" rev-parse main)" "change/$id" || fail "the branch doesn't contain main"

scenario="main moved with a conflict: a sync session resolves it, the full review runs"
id="$(queue)"; build_opens "$id"; touch "$fake/conflict"
tick; expect "$id" merged MERGED
[[ "$(sessions)" == build,sync-1,check-1 ]] || fail "sessions $(sessions)"
[[ "$(cat "$fake/review.log")" == "review --astra" ]] || fail "reviews: $(cat "$fake/review.log" 2>/dev/null)"

scenario="the conflict can't be resolved: failed, PR closed"
id="$(queue)"; build_opens "$id"; touch "$fake/conflict"
echo '{"outcome":"failed","summary":"The two changes contradict each other."}' >"$fake/sync.json"
tick; expect "$id" failed CLOSED
[[ "$(field "$id" result)" == "The two changes contradict each other." ]] || fail "result $(field "$id" result)"

scenario="main moved during the final check: merge refused, round two merges"
id="$(queue)"; build_opens "$id"; touch "$fake/move-during-check"
tick; expect "$id" merged MERGED
[[ "$(sessions)" == build,check-1,check-2 ]] || fail "sessions $(sessions)"

scenario="the review after an update fails: failed, PR closed"
id="$(queue)"; build_opens "$id"; touch "$fake/move-clean" "$fake/review-fails"
tick; expect "$id" failed CLOSED

scenario="retry: queued again, and the next build hears about the last attempt"
api POST "/api/changes/$id/retry" >/dev/null
[[ "$(field "$id" status)" == queued && "$(field "$id" previous_pr_url)" == *"/pull/$((100 + id))" ]] || fail "after retry: $(api GET "/api/changes/$id")"
rm -f "$fake/sessions.log" "$fake/review-fails" "$fake/move-clean"
build_opens "$id" $((200 + id)) # the second attempt opens a new PR
tick
[[ "$(field "$id" status)" == merged && "$(field "$id" attempts)" == 2 ]] || fail "after the second attempt: $(api GET "/api/changes/$id" | jq -c '{status, attempts}')"
if grep -q "\"previous_pr_url\": \"https://github.com/example/hekate/pull/$((100 + id))\"" "$fake/build.prompt" &&
  grep -q '"issue_url"' "$fake/build.prompt"; then
  echo "ok: $scenario"
else
  fail "the build prompt lacks the last attempt"
fi

scenario="the PR closes but gh can't delete the local branch: still recorded at once"
id="$(queue)"; build_opens "$id"
echo '{"verdict":"reject","summary":"Out of scope."}' >"$fake/check.json"
touch "$fake/close-delete-fails"
tick; expect "$id" rejected CLOSED
[[ "$(field "$id" result)" == "Out of scope." ]] || fail "result $(field "$id" result)"

scenario="...and a retry after that rebuilds and pushes the branch afresh"
api POST "/api/changes/$id/retry" >/dev/null
rm -f "$fake/sessions.log" "$fake/close-delete-fails"
echo '{"verdict":"approve","summary":"Shipped."}' >"$fake/check.json"
build_opens "$id" $((200 + id))
tick
if [[ "$(field "$id" status)" == merged ]]; then
  echo "ok: $scenario"
else
  fail "after the retry: $(api GET "/api/changes/$id" | jq -c '{status, result}')"
fi

scenario="rejected, the PR won't close: stays working"
id="$(queue)"; build_opens "$id"
echo '{"verdict":"reject","summary":"Out of scope."}' >"$fake/check.json"
touch "$fake/close-fails"
tick; expect "$id" working OPEN
scenario="...and the next tick settles it with the run's own outcome"
rm "$fake/close-fails"
tick; expect "$id" rejected CLOSED
[[ "$(field "$id" result)" == "Out of scope." ]] || fail "result $(field "$id" result)"

scenario="Hekate is down when the run reports: stays working, the outcome kept"
id="$(queue)"; build_opens "$id"
echo '{"verdict":"reject","summary":"Out of scope."}' >"$fake/check.json"
touch "$fake/hekate-down"
tick; expect "$id" working CLOSED
scenario="...and the next tick reports the run's own outcome once Hekate is back"
echo "HEKATE_URL=http://127.0.0.1:$port" >"$tmp/config/hekate/change-worker.env"
tick; expect "$id" rejected CLOSED
[[ "$(field "$id" result)" == "Out of scope." ]] || fail "result $(field "$id" result)"

scenario="a run left a broken saved outcome: ignored, and the queue carries on"
id="$(queue)"
api POST "/api/changes/$id/claim" >/dev/null # claimed, then the machine died mid-write
mkdir -p "$tmp/state/hekate-change-worker/runs/$id" && printf '{"outcome":"rej' >"$tmp/state/hekate-change-worker/runs/$id/outcome.json"
next="$(queue)"; build_opens "$next"
tick
if [[ "$(field "$id" status)" == failed && "$(field "$next" status)" == merged ]]; then
  echo "ok: $scenario"
else
  fail "statuses: $(field "$id" status) and $(field "$next" status)"
fi

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
