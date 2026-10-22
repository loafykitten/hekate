#!/usr/bin/env bash
# Independent Codex review of local commits before they land on main. See docs/DEVELOPMENT.md.
#
#   scripts/ai-review.sh [--astra] [base]   full review of base...HEAD (base: origin/main)
#   scripts/ai-review.sh [--astra] --fixes  checks the commits since the last review: are its
#                                           findings fixed, and is the fix diff sound
#
# Reports are kept in .git/ai-review/ (per worktree). Exit 0: nothing blocking; 1: blocker or
# major findings; 2: usage error or the review could not run.
set -euo pipefail

model="gpt-6.1-sol"
fixes=""
base=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --astra) model="gpt-6-astra"; shift ;;
    --fixes) fixes=1; shift ;;
    -*) echo "usage: $0 [--astra] [--fixes] [base]" >&2; exit 2 ;;
    *) base="$1"; shift ;;
  esac
done
if [[ -n "$fixes" && -n "$base" ]]; then
  echo "--fixes takes its range from the last review; drop the base" >&2
  exit 2
fi

cd "$(git rev-parse --show-toplevel)"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "Working tree is dirty; commit before reviewing." >&2
  exit 2
fi

dir="$(git rev-parse --absolute-git-dir)/ai-review"
mkdir -p "$dir"
head="$(git rev-parse HEAD)"
excludes="-- . ':(exclude)**/package-lock.json' ':(exclude)**/go.sum'"
rules="Read surrounding code as needed, but keep the review focused on the diff.
Read docs/PLAN.md and docs/DEVELOPMENT.md for intent and conventions. Report real problems:
correctness bugs, security issues (this repo is public: flag any secret, private hostname/IP or
personal data), data loss, races, missing tests for risky logic, and clear violations of the
documented conventions. Severity: blocker = must not land; major = should fix before landing;
minor/nit = optional. Verdict is approve when there are no blocker or major findings. Do not
report style preferences as major."

if [[ -n "$fixes" ]]; then
  last="$dir/last.json"
  if [[ ! -f "$last" ]]; then
    echo "No earlier review in this worktree; run a full review first." >&2
    exit 2
  fi
  from="$(jq -r .head "$last")"
  if [[ "$from" == "$head" ]]; then
    echo "Nothing committed since the last review (${head:0:7})." >&2
    exit 2
  fi
  if ! git merge-base --is-ancestor "$from" HEAD; then
    echo "The last review's head (${from:0:7}) is no longer in this branch; run a full review." >&2
    exit 2
  fi
  range="$from..HEAD"
  round="$(( $(jq -r .round "$last") + 1 ))"
  prompt="You are re-checking fixes in the Hekate repo after an earlier review of commit
${from:0:7}. That review reported these findings (JSON):

$(jq '.findings' "$last")

The fixes are exactly \`git diff $range $excludes\` (lockfiles are excluded on purpose;
also run \`git log $range\`). Do two things only:
1. For each earlier blocker or major finding, decide whether these commits fix it. Report any
   that are not fixed again, at their original severity.
2. Review the fix diff itself for new problems it introduces.
Do not re-review code these commits leave unchanged, and do not raise new findings about it.
$rules"
else
  if [[ -z "$base" ]]; then
    git fetch -q origin main
    base="origin/main"
  fi
  range="$base...HEAD"
  round=1
  prompt="You are reviewing a change in the Hekate repo before it lands on main. The change is
exactly \`git diff $range $excludes\` (lockfiles are excluded on purpose; also run
\`git log ${base}..HEAD\`).
$rules"
fi
if [[ -z "$(git log --oneline "${range/.../..}")" ]]; then
  echo "No commits in $range." >&2
  exit 2
fi

# Progress goes to .git/ai-review/codex.log so a slow review can be watched (tail -f).
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
echo "Reviewing $range with $model (round $round)…" >&2
if ! codex exec -m "$model" -c model_reasoning_effort="high" -s read-only --ephemeral \
  --output-schema scripts/ai-review.schema.json -o "$out" "$prompt" </dev/null >"$dir/codex.log" 2>&1; then
  tail -20 "$dir/codex.log" >&2
  echo "codex review failed" >&2
  exit 2
fi

report="$dir/${head:0:7}-r$round.json"
jq --arg head "$head" --arg range "$range" --arg model "$model" --argjson round "$round" \
  '. + {head: $head, range: $range, model: $model, round: $round}' "$out" >"$report"
cp "$report" "$dir/last.json"

jq -r '
  "AI review (\(.model)) of \(.range), round \(.round): \(.verdict)\n\n\(.summary)\n" +
  (if (.findings | length) == 0 then "\nNo findings."
   else ([.findings[] | "\n[\(.severity)] \(.file):\(.line) \(.title)\n  \(.detail)"] | join("\n"))
   end)' "$report"
echo
echo "Report: $report"

blocking="$(jq '[.findings[] | select(.severity == "blocker" or .severity == "major")] | length' "$report")"
if [[ "$blocking" != "0" ]]; then
  exit 1
fi
