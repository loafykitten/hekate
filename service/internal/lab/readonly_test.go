package lab

import "testing"

func TestReadOnly(t *testing.T) {
	readOnly := []string{
		// Her own looks from a real thread.
		`cat /opt/stacks/INVENTORY.md 2>/dev/null; echo ---; df -h /; echo ---; docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}'`,
		`docker info 2>/dev/null | grep -Ei 'cgroup|runtime|security|rootless' | head; echo ---; docker network ls; echo ---; sudo iptables -L -n 2>/dev/null | head -5; echo ---; sudo nft list ruleset 2>/dev/null | head -20`,
		// The reviewer's, from the trial.
		`docker compose -f /opt/stacks/downloads/compose.yaml config`,
		`docker manifest inspect qmcgaw/gluetun:v3.41.3 >/dev/null && echo OK || echo MISSING`,
		`curl -sL https://raw.githubusercontent.com/jlesage/docker-jdownloader-2/master/README.md | grep -in 'PUID\|PGID\|WEB_AUTH' | head -30`,
		`curl -s "https://hub.docker.com/v2/repositories/qmcgaw/gluetun/tags?page_size=40" | grep -o "v3\.[0-9]*\.[0-9]*"`,
		`stat -c '%U:%G %a %n' /opt/stacks/INVENTORY.md`,
		`sudo -n nft list ruleset | head -50`,
		`find /opt/stacks -maxdepth 2 -type d`,
		`curl -sL https://example.com/README.md | sed -n '100,220p'`,
		`ls -la /dev/net/tun; lsmod | grep wireguard`,
		`cd /opt/stacks/downloads && docker compose ps`,
		`docker logs --tail 50 gluetun 2>&1 | tail -20`,
		`journalctl -u ssh --since -1h --no-pager`,
		`systemctl status docker --no-pager`,
		`ip -4 addr show`,
		`timeout 20 curl -sI http://localhost:8085`,
		`docker ps -q | xargs -r docker inspect --format '{{.Name}}'`,
		`awk '{print $1}' /etc/passwd`,
		`du -sh /opt/stacks/* # sizes`,
		`LC_ALL=C sort -k2 /etc/hosts`,
		`wget -qO- https://example.com`,
		`command -v docker`,
		`docker stats --no-stream`,
		`uniq -c counts.txt`,
		`xxd -l 64 /bin/ls`,
		`yq '.services' compose.yaml`,
		`rg -n gluetun /opt/stacks`,
		`tree -L 2 /opt/stacks`,
		`apt-cache policy docker.io`,
		`yq -o json '.services' compose.yaml`,
		`tree -aL 2 /opt/stacks`,
		`sort -u -k2 /etc/hosts`,
		`dmesg -T --level=err`,
		`date -u +%F`,
		`docker compose -f compose.yaml config -q`,
	}
	for _, c := range readOnly {
		if err := ReadOnly(c); err != nil {
			t.Errorf("ReadOnly(%q) = %v, want read-only", c, err)
		}
	}

	changes := []string{
		`docker compose up -d`,
		`cd /opt/stacks/downloads && docker compose up -d`,
		`docker compose -f x.yaml config -o out.yaml`,
		`rm -rf /opt/stacks/downloads`,
		`echo hi > /opt/stacks/x`,
		`echo hi >> /opt/stacks/x`,
		`cat x | tee y`,
		`mkdir -p /opt/stacks/new`,
		`sudo chown 1000:1000 /opt/stacks/downloads`,
		`sed -i 's/a/b/' f`,
		`sed -n 's/a/b/w out' f`,
		`awk 'BEGIN{system("id")}'`,
		`awk '{print > "/tmp/x"}' f`,
		`find / -name x -delete`,
		`find / -exec rm {} \;`,
		`curl -X POST https://example.com`,
		`curl -sSLo /tmp/x https://example.com`,
		`curl -d a=b https://example.com`,
		`curl -o out https://example.com`,
		`wget https://example.com/file`,
		`cat $(echo /etc/passwd)`,
		"cat `echo x`",
		`sleep 100 &`,
		`(cd /tmp; ls)`,
		`cat <<EOF`,
		`systemctl restart docker`,
		`docker logs -f gluetun`,
		`docker run --rm alpine`,
		`docker exec gluetun sh`,
		`docker pull alpine`,
		`docker network create x`,
		`iptables -A INPUT -j DROP`,
		`nft flush ruleset`,
		`ip link set eth0 down`,
		`hostname newname`,
		`env rm -rf /`,
		`command rm x`,
		`sudo -u root rm x`,
		`xargs rm`,
		`timeout 5 rm x`,
		`apt-get install -y nginx`,
		`for f in *; do rm $f; done`,
		`./setup.sh`,
		`echo 'unclosed`,
		`crontab -e`,
		`docker stats`,
		// Writers in readers' clothing, and option spellings that write.
		`uniq in.txt out.txt`,
		`xxd -r dump.hex /usr/local/bin/x`,
		`xxd in out`,
		`yq -i '.a = 1' compose.yaml`,
		`yq -iP '.a = 1' compose.yaml`,
		`tree -o /etc/x /opt`,
		`rg --pre /tmp/x pattern`,
		`file -C -m magic`,
		`yq -s '"out"' '.' compose.yaml`,
		`yq --split-exp '"out"' '.' compose.yaml`,
		`tree -o/tmp/list /opt/stacks`,
		`tree -ao list /opt`,
		`sort -ofile in`,
		`sort -uo file in`,
		`date -s2026-01-01`,
		`dmesg -Hc`,
		`xxd -rp dump bin`,
		`apt-cache gencaches`,
		`docker compose -f /opt/stacks/a/compose.yaml config -o/etc/ssh/sshd_config`,
		`docker compose config -qo /etc/x`,
		`docker compose config --output=/etc/x`,
		// Options missing their value don't crash the check.
		`timeout -s`,
		`timeout -k`,
		`xargs -n`,
		`xargs -I`,
	}
	for _, c := range changes {
		if err := ReadOnly(c); err == nil {
			t.Errorf("ReadOnly(%q) = nil, want a change", c)
		}
	}
}

// ReadOnly runs on whatever a model wrote, inside the end of a turn: it must never panic.
func FuzzReadOnly(f *testing.F) {
	for _, s := range []string{"timeout -s", "xargs -n", "docker compose -f", "iptables -t", "sudo -n", `a "b`, "x 2>", "&>",
		"docker compose -f x config -o/y", "curl -sSLo f u", "sed -n 's/a/b/w x'", "uniq a b"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, cmd string) { _ = ReadOnly(cmd) })
}
