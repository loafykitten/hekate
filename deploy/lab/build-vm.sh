#!/usr/bin/env bash
# Builds Hekate's lab VM from Debian 13's cloud image. Run after install.sh, from this directory:
#
#   sudo ./build-vm.sh [--replace] [owner-public-key.pub]
#
# It asks for a one-off, pre-approved Tailscale auth key tagged tag:hekate-lab (see README.md);
# TS_AUTHKEY in the environment works too, but a key typed at the prompt stays out of shell history.
# The owner's public key, if given, may also log in as hekate (for looking around by hand).
# --replace deletes the existing VM, its disk and its snapshots first: a clean start, e.g. after
# a break-in. Remove the old hekate-lab machine from the Tailscale admin console before that, so
# the new one gets the same name.
set -euo pipefail
export LC_ALL=C
cd "$(dirname "$(readlink -f "$0")")"
die() { echo "$*" >&2; exit 1; }

DOMAIN=hekate-lab
DISK=/var/lib/libvirt/images/hekate-lab.qcow2
MAC=52:54:00:4c:41:42
SIZE=64G
MEMORY=8192
VCPUS=4
IMAGE=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2
SUMS=https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS

[[ $EUID -eq 0 ]] || die "run with sudo"
# One build at a time: a failed build cleans up, and must never clean up another's VM.
exec 8>/run/hekate-lab-build.lock
flock -n 8 || die "another build-vm.sh is running"
replace=""
owner_key=""
for a in "$@"; do
  case "$a" in
    --replace) replace=1 ;;
    -*) die "usage: sudo TS_AUTHKEY=... ./build-vm.sh [--replace] [owner-public-key.pub]" ;;
    *) owner_key="$a" ;;
  esac
done
[[ -z "$owner_key" || -f "$owner_key" ]] || die "no such key file: $owner_key"
[[ -f /etc/hekate-lab/labctl.env ]] || die "run install.sh first"
# shellcheck source=/dev/null
. /etc/hekate-lab/labctl.env
ssh_dir="$STACK_DIR/lab/ssh"
svc="$(stat -c %u:%g "$STACK_DIR/data")"
virsh() { command virsh -q -c qemu:///system "$@"; }

nft list table inet hekate_lab >/dev/null 2>&1 || die "the lab's firewall isn't loaded: run install.sh first"
virsh net-info hekate-lab 2>/dev/null | grep -E '^Active: +yes' >/dev/null || die "the lab's network isn't running: run install.sh again"
if virsh dominfo "$DOMAIN" >/dev/null 2>&1; then
  [[ -n "$replace" ]] || die "$DOMAIN already exists; --replace deletes it, its disk and its snapshots"
  systemctl stop hekate-lab-vm.service 2>/dev/null || true
  virsh destroy "$DOMAIN" >/dev/null 2>&1 || true
  virsh undefine "$DOMAIN" >/dev/null
  rm -f "$DISK" /var/lib/hekate-lab/isolated
fi
if [[ -e "$DISK" ]]; then
  [[ -n "$replace" ]] || die "$DISK exists without a VM (left by a failed build?); --replace deletes it"
  rm -f "$DISK"
fi
if [[ -z "${TS_AUTHKEY:-}" ]]; then
  [[ -t 0 ]] || die "run it in a terminal, or set TS_AUTHKEY"
  read -rsp "Tailscale auth key (tag:hekate-lab): " TS_AUTHKEY
  echo
fi
[[ "$TS_AUTHKEY" == tskey-* ]] || die "that doesn't look like a Tailscale auth key"

work="$(mktemp -d /var/tmp/hekate-lab.XXXXXX)"
chmod 0700 "$work"
created=""
built=""
# A build that stops partway leaves no VM or disk of its own behind; the key material in $work
# goes either way.
cleanup() {
  rm -rf "$work"
  if [[ -n "$created" && -z "$built" ]]; then
    virsh destroy "$DOMAIN" >/dev/null 2>&1 || true
    virsh undefine "$DOMAIN" >/dev/null 2>&1 || true
    rm -f "$DISK"
  fi
}
trap cleanup EXIT

echo "Downloading Debian 13's cloud image..."
curl -fsSL -o "$work/SHA512SUMS" "$SUMS"
curl -fL --progress-bar -o "$work/debian.qcow2" "$IMAGE"
want="$(awk '$2 == "debian-13-genericcloud-amd64.qcow2" {print $1}' "$work/SHA512SUMS")"
[[ -n "$want" ]] || die "the image isn't listed in SHA512SUMS"
echo "$want  $work/debian.qcow2" | sha512sum -c --quiet - || die "the image's checksum doesn't match"
created=1
qemu-img convert -O qcow2 "$work/debian.qcow2" "$DISK"
qemu-img resize -q "$DISK" "$SIZE"

# Hekate's key to the lab and the reviewer's (both kept across rebuilds), and a fresh host key for
# the lab, which Hekate pins instead of trusting on first use.
if [[ ! -f "$ssh_dir/id_ed25519" ]]; then
  ssh-keygen -q -t ed25519 -N "" -C hekate -f "$ssh_dir/id_ed25519"
fi
if [[ ! -f "$ssh_dir/reviewer_ed25519" ]]; then
  ssh-keygen -q -t ed25519 -N "" -C reviewer -f "$ssh_dir/reviewer_ed25519"
fi
ssh-keygen -q -t ed25519 -N "" -C hekate-lab -f "$work/host_key"
install -m 0644 "$work/host_key.pub" "$ssh_dir/host_key.pub"
chown "$svc" "$ssh_dir/id_ed25519" "$ssh_dir/id_ed25519.pub" "$ssh_dir/reviewer_ed25519" "$ssh_dir/reviewer_ed25519.pub" \
  "$ssh_dir/host_key.pub"
chmod 0600 "$ssh_dir/id_ed25519" "$ssh_dir/reviewer_ed25519"

cat "$ssh_dir/id_ed25519.pub" >"$work/authorized_keys"
[[ -n "$owner_key" ]] && cat "$owner_key" >>"$work/authorized_keys"
while read -r key; do
  [[ -n "$key" ]] && ssh-keygen -lf - <<<"$key" | awk '{print $2}'
done <"$work/authorized_keys" >"$work/fingerprints"

b64() { base64 -w0 "$1"; }
file() { # path mode source
  printf '  - path: %s\n    permissions: "%s"\n    encoding: b64\n    content: %s\n' "$1" "$2" "$(b64 "$3")"
}
printf '%s' "$TS_AUTHKEY" >"$work/ts-authkey"
{
  cat <<EOF
#cloud-config
hostname: hekate-lab
users:
  - name: hekate
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    lock_passwd: true
disable_root: true
ssh_pwauth: false
ssh_deletekeys: true
ssh_genkeytypes: []
ssh_keys:
  ed25519_private: |
$(sed 's/^/    /' "$work/host_key")
  ed25519_public: $(cat "$work/host_key.pub")
package_update: true
package_upgrade: true
packages: [qemu-guest-agent, docker.io, docker-compose, apparmor, unattended-upgrades, nftables, jq, curl, ca-certificates, tmux, git]
write_files:
EOF
  file /etc/ssh/authorized_keys/hekate 0644 "$work/authorized_keys"
  file /etc/hekate-lab/allowed-fingerprints 0644 "$work/fingerprints"
  file /etc/hekate-lab/ts-authkey 0600 "$work/ts-authkey"
  file /etc/ssh/sshd_config.d/10-hekate-lab.conf 0644 vm/sshd.conf
  file /etc/nftables.conf 0755 vm/nftables.conf
  file /etc/apt/apt.conf.d/52hekate-lab 0644 vm/auto-upgrades.conf
  file /etc/docker/daemon.json 0644 vm/docker-daemon.json
  file /etc/systemd/journald.conf.d/hekate-lab.conf 0644 vm/journald.conf
  file /usr/local/sbin/lab-audit 0755 vm/lab-audit
  file /usr/local/lib/hekate-lab/setup.sh 0755 vm/setup.sh
  file /usr/local/lib/hekate-lab/reviewer-setup.sh 0755 vm/reviewer-setup.sh
  file /usr/local/lib/hekate-lab/lab-docker-ro 0755 vm/lab-docker-ro
  file /etc/hekate-lab/reviewer.pub 0644 "$ssh_dir/reviewer_ed25519.pub"
  printf 'runcmd:\n  - [/usr/local/lib/hekate-lab/setup.sh]\n'
} >"$work/user-data"

echo "Creating the VM..."
# A video card with no display attached: Debian's GRUB entry runs load_video, and with no card at
# all that crashes into a reset before the kernel starts (a boot loop).
virt-install --connect qemu:///system --name "$DOMAIN" \
  --memory "$MEMORY" --vcpus "$VCPUS" --cpu host-passthrough --osinfo linux2022 --import \
  --disk "path=$DISK,format=qcow2,bus=virtio,discard=unmap" \
  --network "network=hekate-lab,model=virtio,mac=$MAC" \
  --channel unix,target.type=virtio,target.name=org.qemu.guest_agent.0 \
  --cloud-init "user-data=$work/user-data" \
  --graphics none --video vga --console pty,target.type=serial --noautoconsole
built=1
# Not libvirt's autostart: hekate-lab-vm.service starts it at boot, after its firewall.
systemctl enable hekate-lab-vm.service
systemctl start hekate-lab-vm.service

# The auth key is single use, and the copy on the host goes with $work.
echo "Waiting for first-boot setup to finish (packages, Docker, Tailscale; usually 5-10 minutes)..."
agent() { virsh qemu-agent-command "$DOMAIN" "$1" 2>/dev/null; }
handle=""
for ((i = 0; i < 240; i++)); do
  if handle="$(agent '{"execute":"guest-file-open","arguments":{"path":"/etc/hekate-lab/ready","mode":"r"}}' | jq -r .return)" \
    && [[ "$handle" =~ ^[0-9]+$ ]]; then
    break
  fi
  handle=""
  sleep 5
done
[[ -n "$handle" ]] || die "setup didn't finish in 20 minutes: look with 'sudo virsh console $DOMAIN' (log in isn't possible; watch the boot) or the guest's /var/log/cloud-init-output.log"
ip="$(agent "{\"execute\":\"guest-file-read\",\"arguments\":{\"handle\":$handle,\"count\":64}}" | jq -r '.return["buf-b64"]' | base64 -d | tr -d '[:space:]')"
agent "{\"execute\":\"guest-file-close\",\"arguments\":{\"handle\":$handle}}" >/dev/null || true

cat <<EOF

The lab is up. Its tailnet address: ${ip:-unknown (see the Tailscale admin console)}

If hekate.env doesn't have it yet, add:
  HEKATE_LAB_HOST=${ip:-<the lab tailnet IP>}
and give the hekate service these volumes in compose.yaml:
  - ./lab/ssh:/lab/ssh:ro
  - ./lab/requests:/lab/requests
  - ./lab/results:/lab/results:ro
then: docker compose up -d hekate
EOF
