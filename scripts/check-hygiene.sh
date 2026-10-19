#!/usr/bin/env bash
# Fails if tracked files include character assets, model files, renders or real env files.
# The repo is public; see docs/DEVELOPMENT.md ("Secrets and private details").
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

bad="$(git ls-files | grep -E \
  -e '^(Renamon-Nukorio|mixamo|assets)/' \
  -e '\.(glb|gltf|fbx|blend|blend1|usdz|tga|vrm)$' \
  -e '^asset-pipeline/.*\.(png|jpe?g|webp|exr)$' \
  -e '(^|/)\.env$' \
  -e '\.env$' \
  | grep -v -E '\.env\.example$' || true)"

if [[ -n "$bad" ]]; then
  echo "These files must not be committed (character assets, renders or real env files):" >&2
  echo "$bad" >&2
  exit 1
fi
# Private words (names, hostnames) can't be listed in a public repo, so they live in a local,
# untracked file: one per line in .git/info/private-words. CI skips this; the AI review covers it.
private="$(git rev-parse --git-common-dir)/info/private-words"
if [[ -s "$private" ]]; then
  leaks="$(git grep -n -i -w -F -f "$private" -- . ':!**/package-lock.json' || true)"
  if [[ -n "$leaks" ]]; then
    echo "Private words found in tracked files:" >&2
    echo "$leaks" >&2
    exit 1
  fi
fi
echo "hygiene: ok"
