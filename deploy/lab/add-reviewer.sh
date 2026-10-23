#!/usr/bin/env bash
# Adds the reviewer account to a lab built before reviews, without rebuilding it: a second model
# uses that account to check Renamon's work there, and can only look (docs/PLAN.md, "Reviewing
# her lab work"). A lab built by a newer build-vm.sh has it already. From this directory, as the
# owner of the stack (you're in the libvirt group after install.sh; no sudo needed):
#
#   ./add-reviewer.sh <hekate stack dir>
#
# It makes the reviewer's key in <stack>/lab/ssh if there isn't one, and sets up the account in
# the running VM through its guest agent. Hekate starts reviewing on its own once the key is there.
# Safe to run again.
set -euo pipefail
export LC_ALL=C
cd "$(dirname "$(readlink -f "$0")")"
die() { echo "$*" >&2; exit 1; }

DOMAIN=hekate-lab
stack="$(readlink -f "${1:?usage: ./add-reviewer.sh <hekate stack dir>}")"
ssh_dir="$stack/lab/ssh"
[[ -d "$ssh_dir" ]] || die "$ssh_dir doesn't exist: is $stack the Hekate stack, with install.sh run?"
# Hekate runs as the owner of data/ and must be able to read the key.
[[ "$(stat -c %u "$stack/data")" == "$(id -u)" ]] || die "run this as the owner of $stack/data, which Hekate runs as"
command -v jq >/dev/null || die "jq is needed"

virsh() { command virsh -q -c qemu:///system "$@"; }
agent() { virsh qemu-agent-command "$DOMAIN" "$1"; }
agent '{"execute":"guest-ping"}' >/dev/null || die "the lab's guest agent doesn't answer: is the VM running?"

# put <guest path> <local file>: writes a file in the VM (root's, mode 0644 until changed).
put() {
  local h
  h="$(agent "$(jq -nc --arg p "$1" '{execute: "guest-file-open", arguments: {path: $p, mode: "w"}}')" | jq -r .return)"
  [[ "$h" =~ ^[0-9]+$ ]] || die "couldn't open $1 in the VM"
  agent "$(jq -nc --argjson h "$h" --arg b "$(base64 -w0 "$2")" '{execute: "guest-file-write", arguments: {handle: $h, "buf-b64": $b}}')" >/dev/null
  agent "$(jq -nc --argjson h "$h" '{execute: "guest-file-close", arguments: {handle: $h}}')" >/dev/null
}

# run <program> [args...]: runs a program in the VM as root, prints what it printed, and fails
# when it does.
run() {
  local pid status i
  pid="$(agent "$(jq -nc --arg p "$1" '{execute: "guest-exec", arguments: {path: $p, arg: $ARGS.positional, "capture-output": true}}' \
    --args -- "${@:2}")" | jq -r .return.pid)"
  [[ "$pid" =~ ^[0-9]+$ ]] || die "couldn't run $1 in the VM"
  for ((i = 0; i < 120; i++)); do
    status="$(agent "$(jq -nc --argjson p "$pid" '{execute: "guest-exec-status", arguments: {pid: $p}}')")"
    if [[ "$(jq -r .return.exited <<<"$status")" == true ]]; then
      jq -r '.return["out-data"] // empty' <<<"$status" | base64 -d
      jq -r '.return["err-data"] // empty' <<<"$status" | base64 -d >&2
      return "$(jq -r '.return.exitcode // 1' <<<"$status")"
    fi
    sleep 1
  done
  die "$1 didn't finish in the VM within 2 minutes"
}

if [[ ! -f "$ssh_dir/reviewer_ed25519" ]]; then
  ssh-keygen -q -t ed25519 -N "" -C reviewer -f "$ssh_dir/reviewer_ed25519.new"
  # Hekate notices the key as soon as it's there, so it appears only once the account is ready.
  trap 'rm -f "$ssh_dir/reviewer_ed25519.new" "$ssh_dir/reviewer_ed25519.new.pub"' EXIT
  pub="$ssh_dir/reviewer_ed25519.new.pub"
else
  pub="$ssh_dir/reviewer_ed25519.pub"
fi

lib=/usr/local/lib/hekate-lab
run /usr/bin/install -d -m 0755 "$lib"
put "$lib/reviewer-setup.sh" vm/reviewer-setup.sh
put "$lib/lab-docker-ro" vm/lab-docker-ro
put /etc/hekate-lab/reviewer.pub "$pub"
run /usr/bin/chmod 0755 "$lib/reviewer-setup.sh" "$lib/lab-docker-ro"
run /usr/bin/chmod 0644 /etc/hekate-lab/reviewer.pub
run /bin/bash "$lib/reviewer-setup.sh" /etc/hekate-lab/reviewer.pub

if [[ "$pub" == *.new.pub ]]; then
  chmod 0600 "$ssh_dir/reviewer_ed25519.new"
  mv "$ssh_dir/reviewer_ed25519.new.pub" "$ssh_dir/reviewer_ed25519.pub"
  mv "$ssh_dir/reviewer_ed25519.new" "$ssh_dir/reviewer_ed25519"
fi
echo "Done: Hekate reviews her lab work from her next change on (no restart needed)."
