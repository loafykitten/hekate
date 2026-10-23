#!/usr/bin/env bash
# Sets up the lab's reviewer account, which a second model uses to check Renamon's work and which
# can only look (docs/PLAN.md, "Reviewing her lab work"). Run as root in the lab with the
# reviewer's public key: by first-boot setup (setup.sh), or by add-reviewer.sh on the host for a
# lab built before reviews. Safe to run again. Expects lab-docker-ro beside it.
#
#   reviewer-setup.sh <public-key-file>
set -euo pipefail
export LC_ALL=C PATH=/usr/sbin:/usr/bin:/sbin:/bin
key="${1:?usage: reviewer-setup.sh <public-key-file>}"
conf=/etc/hekate-lab
lib=/usr/local/lib/hekate-lab
here="$(dirname "$(readlink -f "$0")")"

fp="$(ssh-keygen -lf "$key" | awk '{print $2}')"
[[ "$fp" == SHA256:* ]] || { echo "not a public key: $key" >&2; exit 1; }

# The account: no password, not in the docker group, the journal readable. Its only root powers
# are lab-docker-ro and listing the firewall.
id reviewer >/dev/null 2>&1 || useradd --create-home --shell /bin/bash --user-group reviewer
passwd -l reviewer >/dev/null
usermod -aG systemd-journal reviewer
gpasswd -d reviewer docker >/dev/null 2>&1 || true

install -m 0755 "$here/lab-docker-ro" /usr/local/sbin/lab-docker-ro
install -d -m 0755 "$lib/reviewer-bin"
# shellcheck disable=SC2016 # the $@ is for the wrapper
printf '#!/bin/sh\n# The reviewer'\''s docker: read-only commands, through sudo (lab-docker-ro).\nexec sudo -n /usr/local/sbin/lab-docker-ro "$@"\n' \
  >"$lib/reviewer-bin/docker"
chmod 0755 "$lib/reviewer-bin/docker"

sudoers="$(mktemp)"
cat >"$sudoers" <<'EOF'
# The lab reviewer looks and changes nothing (reviewer-setup.sh).
Defaults:reviewer !requiretty
reviewer ALL=(root) NOPASSWD: /usr/local/sbin/lab-docker-ro, /usr/sbin/nft list ruleset, /usr/sbin/iptables -S, /usr/sbin/iptables -t nat -S, /usr/sbin/ip6tables -S
EOF
visudo -cqf "$sudoers"
install -m 0440 "$sudoers" /etc/sudoers.d/hekate-lab-reviewer
rm -f "$sudoers"

# SSH: its key beside hekate's, fixed like hers, and known to the nightly security check.
install -m 0644 "$key" /etc/ssh/authorized_keys/reviewer
grep -qxF "$fp" "$conf/allowed-fingerprints" || echo "$fp" >>"$conf/allowed-fingerprints"
sed -i -E 's/^AllowUsers .*/AllowUsers hekate reviewer/' /etc/ssh/sshd_config.d/10-hekate-lab.conf
sshd -t
systemctl reload ssh
sha256sum /etc/ssh/authorized_keys/* >"$conf/authorized_keys.sha256"
echo "reviewer is set up ($fp)"
