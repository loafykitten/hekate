#!/usr/bin/env bash
# Installs (or reinstalls) the change worker as a launchd agent that runs watch.sh every 90
# seconds. The agent gets the PATH of the shell that ran this, so run it from a normal terminal
# and again after moving tools (e.g. a new Node version).
#   install.sh            install or update
#   install.sh --remove   stop and remove
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label="local.hekate.change-worker"
plist="$HOME/Library/LaunchAgents/$label.plist"
domain="gui/$(id -u)"

launchctl bootout "$domain/$label" 2>/dev/null || true
if [[ "${1:-}" == --remove ]]; then
  rm -f "$plist"
  echo "Removed $label."
  exit 0
fi

for tool in claude gh git go jq npm codex openssl; do
  command -v "$tool" >/dev/null || { echo "$tool is not on PATH" >&2; exit 1; }
done
gh auth status >/dev/null 2>&1 || { echo "gh is not logged in" >&2; exit 1; }
curl -fsS --max-time 10 "$HEKATE_URL/healthz" >/dev/null || { echo "cannot reach $HEKATE_URL/healthz" >&2; exit 1; }

mkdir -p "$state" "$(dirname "$plist")"
xml() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' <<<"$1"; }
cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key>
  <array><string>$(xml "$worker_dir/watch.sh")</string></array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>$(xml "$PATH")</string>
    <key>HOME</key><string>$(xml "$HOME")</string>
  </dict>
  <key>StartInterval</key><integer>90</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>$(xml "$state/watch.log")</string>
  <key>StandardErrorPath</key><string>$(xml "$state/watch.log")</string>
</dict>
</plist>
EOF
launchctl bootstrap "$domain" "$plist"
echo "Installed $label: it checks $HEKATE_URL every 90 s. Logs: $state"
