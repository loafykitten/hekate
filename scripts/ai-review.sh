#!/usr/bin/env bash
# Independent Codex review of the current branch's PR. Posts findings as a PR comment and sets
# the `ai-review` commit status on the PR head, which branch protection requires.
# See docs/DEVELOPMENT.md.
set -euo pipefail

model="gpt-6.1-sol"
override=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --astra) model="gpt-6-astra"; shift ;;
    --override) override="${2:?--override needs a reason}"; shift 2 ;;
    *) echo "usage: $0 [--astra] [--override reason]" >&2; exit 2 ;;
  esac
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

# GitHub can take a few seconds to see a fresh push; wait for the PR head to match HEAD.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  pr_json="$(gh pr view --json number,headRefOid,baseRefName,url)"
  [[ "$(jq -r .headRefOid <<<"$pr_json")" == "$(git rev-parse HEAD)" ]] && break
  sleep 3
done
pr="$(jq -r .number <<<"$pr_json")"
head="$(jq -r .headRefOid <<<"$pr_json")"
base="$(jq -r .baseRefName <<<"$pr_json")"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "Working tree is dirty; commit and push before reviewing." >&2
  exit 1
fi
if [[ "$(git rev-parse HEAD)" != "$head" ]]; then
  echo "Local HEAD differs from the PR head ($head); push first." >&2
  exit 1
fi

set_status() { # state description
  gh api -X POST "repos/$repo/statuses/$head" \
    -f state="$1" -f context="ai-review" -f description="$2" >/dev/null
}

if [[ -n "$override" ]]; then
  gh pr comment "$pr" --body "**ai-review override** for \`${head:0:7}\`: $override"
  set_status success "Override: ${override:0:100}"
  echo "ai-review: success (override)"
  exit 0
fi

git fetch -q origin "$base"
set_status pending "Reviewing with $model"

out="$(mktemp)"
prompt="You are reviewing pull request #$pr in the Hekate repo. The change is exactly
\`git diff origin/$base...HEAD\` (run it; also \`git log origin/$base..HEAD\`). Read surrounding code as needed.
Read docs/PLAN.md and docs/DEVELOPMENT.md for intent and conventions. Report real problems:
correctness bugs, security issues (this repo is public: flag any secret, private hostname/IP or
personal data), data loss, broken gates, races, missing tests for risky logic, and clear
violations of the documented conventions. Severity: blocker = must not merge; major = should
fix before merge; minor/nit = optional. Verdict is approve when there are no blocker or major
findings. Do not report style preferences as major."

log="$(mktemp)"
trap 'rm -f "$out" "$log"' EXIT
if ! codex exec -m "$model" -c model_reasoning_effort="high" -s read-only --ephemeral \
  --output-schema scripts/ai-review.schema.json -o "$out" "$prompt" >"$log" 2>&1; then
  tail -20 "$log" >&2
  set_status error "Codex review failed to run"
  echo "codex review failed" >&2
  exit 1
fi

verdict="$(jq -r .verdict "$out")"
blocking="$(jq '[.findings[] | select(.severity == "blocker" or .severity == "major")] | length' "$out")"

body="$(jq -r --arg model "$model" --arg sha "${head:0:7}" '
  "### AI review (\($model)) of `\($sha)`: **\(.verdict)**\n\n\(.summary)\n" +
  (if (.findings | length) == 0 then "\nNo findings."
   else "\n| Severity | Where | Finding |\n|---|---|---|\n" +
     ([.findings[] | "| \(.severity) | `\(.file):\(.line)` | **\(.title)**: \(.detail | gsub("\n"; " ") | gsub("\\|"; "\\\\|")) |"] | join("\n"))
   end)' "$out")"
gh pr comment "$pr" --body "$body" >/dev/null

if [[ "$verdict" == "approve" && "$blocking" == "0" ]]; then
  set_status success "Approved by $model"
  echo "ai-review: success"
else
  set_status failure "$blocking blocking finding(s) from $model"
  echo "ai-review: failure ($blocking blocking)"
fi
jq . "$out"
