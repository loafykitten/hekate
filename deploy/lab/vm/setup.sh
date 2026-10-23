#!/usr/bin/env bash
# First-boot setup of Hekate's lab, run once as root by cloud-init (runcmd) after the packages
# in the user-data are installed. build-vm.sh waits for the ready file this writes last.
set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
conf=/etc/hekate-lab
# shellcheck source=/dev/null
codename="$(. /etc/os-release && echo "$VERSION_CODENAME")"

usermod -aG docker hekate
systemctl start qemu-guest-agent || true
# debsecan recommends a mail server; the lab doesn't need one.
apt-get install -y --no-install-recommends debsecan

# Tailscale, from its own repository; unattended-upgrades keeps it current.
curl -fsSL "https://pkgs.tailscale.com/stable/debian/$codename.noarmor.gpg" -o /usr/share/keyrings/tailscale-archive-keyring.gpg
curl -fsSL "https://pkgs.tailscale.com/stable/debian/$codename.tailscale-keyring.list" -o /etc/apt/sources.list.d/tailscale.list
apt-get update
apt-get install -y tailscale
tailscale up --auth-key="file:$conf/ts-authkey" --hostname=hekate-lab --accept-dns=false
rm -f "$conf/ts-authkey"

# grype scans container images. Pinned to a release and its checksum, so a hijacked release
# can't slip in: take a newer one on purpose by changing these two lines.
grype_version=0.120.1
grype_sha256=111189f61433426af29cc3db33c6847d7e5272150926396b170a985fcda3a11c
deb="$(mktemp -d)/grype.deb"
curl -fsSL -o "$deb" "https://github.com/anchore/grype/releases/download/v$grype_version/grype_${grype_version}_linux_amd64.deb"
echo "$grype_sha256  $deb" | sha256sum -c -
apt-get install -y "$deb"
rm -rf "$(dirname "$deb")"
install -d -m 0755 /var/cache/grype /opt/stacks

systemctl enable --now nftables
systemctl restart ssh

sha256sum /etc/ssh/authorized_keys/* >"$conf/authorized_keys.sha256"
tailscale ip -4 >"$conf/ready"
