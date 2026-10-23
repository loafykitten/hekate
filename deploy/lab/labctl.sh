#!/usr/bin/env bash
# hekate-labctl: the host's side of Hekate's lab VM. Runs as root, and does only these things:
#
#   hekate-labctl stats                 state, CPU and network counters, firewall counters, snapshots
#   hekate-labctl list                  snapshots, oldest first
#   hekate-labctl snapshot <label>      disk snapshot; only the newest $KEEP are kept
#   hekate-labctl rollback <name>       restart the VM on a snapshot's disk
#   hekate-labctl isolate               unplug the VM's network (it keeps running)
#   hekate-labctl reconnect             plug it back in
#   hekate-labctl start | stop          start the VM (only with its firewall loaded) / shut it down
#   hekate-labctl serve                 answer Hekate's requests (hekate-labctl.path starts this)
#   hekate-labctl docker-user           let lab traffic through Docker's FORWARD policy
#
# Hekate's container can't run any of this itself. It drops a request file in
# $STACK_DIR/lab/requests ({"verb": "...", "arg": "..."}, named <id>.json) and reads the answer
# from $STACK_DIR/lab/results/<id>.json, a directory only root can write.
#
# Snapshots are qcow2 internal snapshots of the disk only, taken while the VM runs (through QEMU's
# monitor, with the guest's filesystems frozen by its agent). Each costs only the blocks that
# changed since, and deleting one gives the space back.
set -euo pipefail
export LC_ALL=C

DOMAIN=hekate-lab
DISK=/var/lib/libvirt/images/hekate-lab.qcow2
MAC=52:54:00:4c:41:42
KEEP=2
STATE=/var/lib/hekate-lab
# STACK_DIR comes from here (written by install.sh).
# shellcheck source=/dev/null
[[ -f /etc/hekate-lab/labctl.env ]] && . /etc/hekate-lab/labctl.env

virsh() { command virsh -q -c qemu:///system "$@"; }
die() { echo "$*" >&2; exit 1; }

# No early-exit readers (head, grep -q) after virsh in a pipeline: with pipefail, the broken pipe
# fails the whole pipeline after the answer was read.
domstate() {
  local out
  out="$(virsh domstate "$DOMAIN" 2>/dev/null)" || { echo missing; return; }
  echo "${out%%$'\n'*}"
}
running() { [[ "$(domstate)" == running ]]; }
isolated() { [[ -f $STATE/isolated ]]; }
# qmp sends one command to QEMU's monitor and prints the reply. virsh exits 0 even when QEMU
# answers with an error, so the reply is checked here.
qmp() {
  local out
  out="$(virsh qemu-monitor-command "$DOMAIN" "$1")" || die "QEMU's monitor didn't answer"
  jq -e 'has("return")' >/dev/null 2>&1 <<<"$out" || die "QEMU: $(jq -r '.error.desc // .' <<<"$out" 2>/dev/null | cut -c 1-300)"
  printf '%s\n' "$out"
}

# node prints the block node QEMU knows the disk by.
node() {
  local n
  n="$(qmp '{"execute":"query-block"}' | jq -r --arg disk "$DISK" \
    '.return[] | select(.inserted.file == $disk or .inserted.image.filename == $disk) | .inserted["node-name"]' | sed -n 1p)"
  [[ -n "$n" && "$n" != null ]] || die "can't find the lab's disk in QEMU"
  echo "$n"
}

# snapshots prints [{name, date}] oldest first (date: unix seconds).
snapshots() {
  if running; then
    qmp '{"execute":"query-block"}' | jq -c --arg disk "$DISK" \
      '[.return[] | select(.inserted.file == $disk or .inserted.image.filename == $disk) | .inserted.image.snapshots // [] | .[]
        | {name, date: .["date-sec"]}] | sort_by(.date)'
  else
    qemu-img info -U --output=json "$DISK" | jq -c '[.snapshots // [] | .[] | {name, date: .["date-sec"]}] | sort_by(.date)'
  fi
}

thaw() { timeout 30 virsh domfsthaw "$DOMAIN" >/dev/null 2>&1 || true; }

snapshot() {
  local label="$1" name
  [[ "$label" =~ ^[a-z0-9-]{1,24}$ ]] || die "a label is 1-24 lowercase letters, digits or dashes"
  isolated && die "the lab is cut off from the network; no snapshots until it's reconnected"
  name="$(date -u +%Y%m%dT%H%M%SZ)-$label"
  if running; then
    local n frozen=""
    n="$(node)"
    # Freeze the guest's filesystems so the snapshot is consistent; without the guest agent it
    # is still crash-consistent, like pulling the plug.
    if timeout 30 virsh domfsfreeze "$DOMAIN" >/dev/null 2>&1; then
      frozen=1
      trap thaw EXIT
    fi
    qmp "$(jq -nc --arg d "$n" --arg s "$name" '{execute: "blockdev-snapshot-internal-sync", arguments: {device: $d, name: $s}}')" >/dev/null
    if [[ -n "$frozen" ]]; then thaw; trap - EXIT; fi
  else
    qemu-img snapshot -c "$name" "$DISK"
  fi
  # Newer replaces older: keep the newest $KEEP.
  local old
  while read -r old; do
    [[ -n "$old" ]] || continue
    if running; then
      qmp "$(jq -nc --arg d "$(node)" --arg s "$old" '{execute: "blockdev-snapshot-delete-internal-sync", arguments: {device: $d, name: $s}}')" >/dev/null
    else
      qemu-img snapshot -d "$old" "$DISK"
    fi
  done < <(snapshots | jq -r --argjson keep "$KEEP" 'if length > $keep then .[0:length - $keep][].name else empty end')
  jq -nc --arg name "$name" '{name: $name}'
}

# start_vm starts the VM, and only when the firewall that contains it is loaded.
start_vm() {
  nft list table inet hekate_lab >/dev/null 2>&1 || die "the lab's firewall isn't loaded (hekate-lab-firewall.service); not starting the VM"
  running || virsh start "$DOMAIN" >/dev/null
}

stop_vm() {
  running || return 0
  virsh shutdown "$DOMAIN" >/dev/null 2>&1 || true
  local i
  for ((i = 0; i < 30; i++)); do
    running || return 0
    sleep 1
  done
  virsh destroy "$DOMAIN" >/dev/null
}

rollback() {
  local name="$1"
  snapshots | jq -e --arg n "$name" 'any(.[]; .name == $n)' >/dev/null || die "there is no snapshot named $name"
  stop_vm
  qemu-img snapshot -a "$name" "$DISK"
  start_vm
  jq -nc --arg name "$name" '{name: $name}'
}

# setlink sets the VM's network link, for now and for every later boot.
setlink() {
  if running; then virsh domif-setlink "$DOMAIN" "$MAC" "$1" >/dev/null; fi
  virsh domif-setlink "$DOMAIN" "$MAC" "$1" --config >/dev/null
}

isolate() {
  setlink down
  mkdir -p "$STATE"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$STATE/isolated"
  jq -nc '{isolated: true}'
}

reconnect() {
  setlink up
  rm -f "$STATE/isolated"
  jq -nc '{isolated: false}'
}

counter() { nft -j list counter inet hekate_lab "$1" 2>/dev/null | jq '[.nftables[] | .counter? | select(.) | .packets][0] // 0' || echo 0; }

stats() {
  local state dom="{}" since=""
  state="$(domstate)"
  if [[ "$state" == running ]]; then
    dom="$(virsh domstats "$DOMAIN" --cpu-total --interface --vcpu | jq -R -s '
      [split("\n")[] | select(test("^ +[a-z0-9._]+=")) | capture("^ *(?<k>[^=]+)=(?<v>.*)$")]
      | map({(.k): .v}) | add // {}')"
  fi
  isolated && since="$(cat "$STATE/isolated")"
  jq -nc --arg state "$state" --argjson dom "$dom" --arg since "$since" \
    --argjson smtp "$(counter smtp_out)" --argjson private "$(counter private_tcp)" --argjson host "$(counter host_tcp)" \
    --argjson disk "$( (stat -c %b "$DISK" 2>/dev/null || echo 0) | awk '{print $1 * 512}')" \
    --argjson snaps "$(snapshots 2>/dev/null || echo '[]')" '{
      state: $state,
      isolated: ($since != ""), isolated_since: $since,
      cpu_time_ns: (($dom["cpu.time"] // "0") | tonumber),
      vcpus: (($dom["vcpu.current"] // "0") | tonumber),
      rx_bytes: (($dom["net.0.rx.bytes"] // "0") | tonumber),
      tx_bytes: (($dom["net.0.tx.bytes"] // "0") | tonumber),
      smtp_blocked: $smtp, private_blocked: $private, host_blocked: $host,
      disk_bytes: $disk, snapshots: $snaps, at: now | floor
    }'
}

# docker-user: Docker sets the FORWARD policy to DROP; let the lab's traffic through it. Our own
# table (firewall.nft) still decides where that traffic may go.
docker_user() {
  iptables -nL DOCKER-USER >/dev/null 2>&1 || return 0
  iptables -C DOCKER-USER -i virbr-lab -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i virbr-lab -j ACCEPT
  iptables -C DOCKER-USER -o virbr-lab -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null \
    || iptables -I DOCKER-USER -o virbr-lab -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
}

# respond writes the answer to request id where only root can write, so the container that asked
# can read it but never plant anything in its place.
respond() { # id ok data-or-error
  local id="$1" tmp
  tmp="$(mktemp "$RESULTS/.tmp.XXXXXX")"
  if [[ "$2" == true ]]; then
    jq -nc --argjson data "$3" '{ok: true, data: $data}' >"$tmp"
  else
    jq -nc --arg error "$3" '{ok: false, error: $error}' >"$tmp"
  fi
  chmod 0644 "$tmp"
  mv -f "$tmp" "$RESULTS/$id.json"
}

# run_verb runs one verb in a subshell and answers with its JSON or its error.
run_verb() { # id verb arg
  local id="$1" out err
  err="$(mktemp)"
  if out="$(set -e; case "$2" in
      stats) stats ;;
      list) snapshots ;;
      snapshot) snapshot "$3" ;;
      rollback) rollback "$3" ;;
      isolate) isolate ;;
      reconnect) reconnect ;;
      *) die "unknown request" ;;
    esac 2>"$err")" && jq -e . >/dev/null 2>&1 <<<"$out"; then
    respond "$id" true "$out"
  else
    respond "$id" false "$(tail -c 400 "$err" | tr '\n' ' ')"
  fi
  rm -f "$err"
}

serve() {
  [[ -n "${STACK_DIR:-}" ]] || die "STACK_DIR is not set in /etc/hekate-lab/labctl.env"
  local requests="$STACK_DIR/lab/requests" work="$STACK_DIR/lab/.work"
  RESULTS="$STACK_DIR/lab/results"
  install -d -m 0700 -o root -g root "$work"
  install -d -m 0755 -o root -g root "$RESULTS"
  exec 9>/run/hekate-labctl.lock
  flock 9
  find "$RESULTS" -maxdepth 1 -type f -mmin +1440 -delete
  shopt -s nullglob
  local f base id w verb arg
  for f in "$requests"/*.json; do
    base="${f##*/}"
    id="${base%.json}"
    if [[ ! "$id" =~ ^[a-z0-9-]{8,64}$ ]]; then
      rm -rf -- "$f"
      continue
    fi
    # Move it where the container can't swap it, then look at what it is: a symlink moves as a
    # symlink and is refused.
    w="$work/$base"
    mv -f -- "$f" "$w" || continue
    if [[ -L "$w" || ! -f "$w" ]] || (($(stat -c %s "$w") > 4096)); then
      rm -rf -- "$w"
      respond "$id" false "bad request"
      continue
    fi
    verb="$(jq -r '.verb // "" | strings' "$w" 2>/dev/null || true)"
    arg="$(jq -r '.arg // "" | strings' "$w" 2>/dev/null || true)"
    rm -f -- "$w"
    run_verb "$id" "$verb" "$arg"
  done
}

[[ $EUID -eq 0 ]] || die "run as root"
case "${1:-}" in
  stats) stats ;;
  list) snapshots ;;
  snapshot) snapshot "${2:?label}" ;;
  rollback) rollback "${2:?snapshot name}" ;;
  isolate) isolate ;;
  reconnect) reconnect ;;
  serve) serve ;;
  start) start_vm ;;
  stop) stop_vm ;;
  docker-user) docker_user ;;
  *) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
