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
echo "hygiene: ok"
