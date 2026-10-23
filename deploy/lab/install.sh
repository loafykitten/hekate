#!/usr/bin/env bash
# One-time host setup for Hekate's lab VM, on an Arch-based host. From this directory:
#
#   sudo ./install.sh <hekate stack dir>
#
# Installs libvirt and QEMU, the lab's network and firewall, hekate-labctl and its units, and the
# stack's lab/ directories. Safe to run again (after pulling changes to these files, for example).
# Then build the VM with build-vm.sh. See README.md.
set -euo pipefail
export LC_ALL=C
cd "$(dirname "$(readlink -f "$0")")"
die() { echo "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run with sudo"
stack="$(readlink -f "${1:?usage: sudo ./install.sh <hekate stack dir>}")"
[[ -f "$stack/compose.yaml" && -d "$stack/data" ]] || die "$stack doesn't look like the Hekate stack (no compose.yaml and data/)"
operator="${SUDO_USER:-}"
[[ -n "$operator" && "$operator" != root ]] || die "run it with sudo from your own account"
# The service runs as the owner of data/ (compose's user:), and reads the lab's key and results.
svc_uid="$(stat -c %u "$stack/data")"
svc_gid="$(stat -c %g "$stack/data")"

pacman -S --needed --noconfirm libvirt qemu-base virt-install dnsmasq jq
# libvirt installs firewalld zones and policies; a running firewalld only sees them after a reload.
if systemctl is-active -q firewalld.service; then
  firewall-cmd --reload >/dev/null
fi
systemctl enable --now libvirtd.service
# The operator manages the VM with virsh without sudo (virsh -c qemu:///system).
usermod -aG libvirt "$operator"

if ! virsh -q -c qemu:///system net-info hekate-lab >/dev/null 2>&1; then
  virsh -q -c qemu:///system net-define network.xml
fi
virsh -q -c qemu:///system net-autostart hekate-lab
if ! virsh -q -c qemu:///system net-info hekate-lab | grep -Eq '^Active: +yes'; then
  virsh -q -c qemu:///system net-start hekate-lab
fi

install -d -m 0755 /etc/hekate-lab /var/lib/hekate-lab
install -m 0644 firewall.nft /etc/hekate-lab/firewall.nft
install -m 0755 labctl.sh /usr/local/sbin/hekate-labctl
echo "STACK_DIR=$stack" >/etc/hekate-lab/labctl.env
install -m 0644 systemd/hekate-lab-firewall.service systemd/hekate-labctl.service systemd/hekate-lab-vm.service /etc/systemd/system/
sed "s|@STACK_DIR@|$stack|" systemd/hekate-labctl.path >/etc/systemd/system/hekate-labctl.path

# build-vm.sh enables hekate-lab-vm.service once there is a VM.

# lab/requests: the service writes requests. lab/results and lab/.work: root only writes.
# lab/ssh: the service's key to the VM and the VM's host key.
install -d -m 0755 -o "$svc_uid" -g "$svc_gid" "$stack/lab"
install -d -m 0700 -o "$svc_uid" -g "$svc_gid" "$stack/lab/requests" "$stack/lab/ssh"
install -d -m 0755 -o root -g root "$stack/lab/results"
install -d -m 0700 -o root -g root "$stack/lab/.work"

systemctl daemon-reload
systemctl enable hekate-lab-firewall.service hekate-labctl.path
if systemctl is-active -q hekate-lab-firewall.service; then
  systemctl reload hekate-lab-firewall.service
else
  systemctl start hekate-lab-firewall.service
fi
systemctl restart hekate-labctl.path

echo
echo "Host side is ready. Next: sudo TS_AUTHKEY=tskey-... ./build-vm.sh (see README.md)."
echo "$operator was added to the libvirt group; log in again for it to apply."
