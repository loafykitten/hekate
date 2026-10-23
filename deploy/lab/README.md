# Her lab

A Debian 13 VM on the homelab that Renamon has root on: she installs things, runs Docker stacks
and uses them when the owner asks ("set up qBittorrent", then "download Arch with it"). Hekate
itself stays in its container: mail, money, memories and every secret stay out of the VM, which
she reaches over SSH. See `docs/PLAN.md` ("Her lab") for the design.

```
host (KVM)
├── hekate container ── ssh (her key; the VM's host key is pinned) ──► lab VM (tailnet: tag:hekate-lab)
│        │                                                             root, Docker, her apps
│        └─ request files ─► hekate-labctl (root): snapshots, rollback, cut off, stats
└── firewall.nft: the VM reaches the internet, never the LAN, the tailnet's other machines or the host
```

What keeps it contained:

- **The VM boundary.** Root in the VM is not root on the host.
- **The host's firewall** (`firewall.nft`): from the VM, no connections into private ranges (LAN,
  tailnet, Docker networks), none to the host but DHCP and DNS, no IPv6, nothing to port 25.
  Blocked attempts are counted for the security check.
- **The tailnet policy**: your devices may reach the VM; the VM may start nothing on the tailnet.
- **Snapshots**: two at most, disk only. The nightly one is taken only after a security check
  that found no sign of a break-in.
- **The nightly security check** (`service/internal/lab`): Debian packages (debsecan) and running
  container images (grype) against published vulnerabilities, matched with CISA's list of ones
  being exploited; signs of a break-in inside the VM (`vm/lab-audit`) and as the host sees it
  (firewall counters, CPU). A strong sign cuts the VM's network by itself and alerts you.

## Files

| File | What |
|---|---|
| `install.sh` | One-time host setup (libvirt, network, firewall, labctl, the stack's `lab/` dirs) |
| `build-vm.sh` | Builds the VM from Debian's cloud image; `--replace` rebuilds it from scratch |
| `labctl.sh` | `hekate-labctl`: snapshots, rollback, cut off/reconnect, stats; answers Hekate's requests |
| `firewall.nft`, `network.xml` | The VM's network and what it may reach |
| `systemd/` | The firewall, the VM (started only after its firewall), and the path unit that wakes labctl |
| `vm/` | What the VM gets at first boot: setup, `lab-audit`, sshd, nftables, upgrades, log limits |

## Setting it up

On the homelab, in a checkout of this repo (`git clone https://github.com/<owner>/hekate`):

1. **Host:** `cd deploy/lab && sudo ./install.sh <stack dir>` (e.g. `~/hekate`).
2. **Tailscale policy** (admin console → Access controls). Add the tag, and make sure no rule lets
   the lab start connections. The default policy (`"src": ["*"]`) includes tagged machines, so
   replace it with one for your own devices:
   ```jsonc
   "tagOwners": { "tag:hekate-lab": ["autogroup:admin"] },
   "grants": [
     // Your devices, and Hekate's Tailscale sidecar, reach everything, the lab included.
     { "src": ["autogroup:member"], "dst": ["*"], "ip": ["*"] },
     // Keep rules for your other tagged machines, but none with tag:hekate-lab or "*" as a source.
   ],
   ```
3. **Auth key** (admin console → Settings → Keys): one-off, pre-approved, tag `tag:hekate-lab`.
4. **VM:** `sudo TS_AUTHKEY=tskey-auth-... ./build-vm.sh [~/.ssh/id_ed25519.pub]`. With your
   public key, you can also `ssh hekate@hekate-lab` to look around. It takes 5–10 minutes and
   prints the VM's tailnet address.
5. **Hekate:** add `HEKATE_LAB_HOST=<that address>` to `hekate.env`, the three `lab/` volumes from
   `compose.example.yaml` to the hekate service, then `docker compose up -d hekate`. Ask her for
   `self_status`: "Lab" should say reachable.

After pulling changes to these files, run `install.sh` again (it's idempotent). Changes under
`vm/` reach only a VM built after them: rebuild, or apply them in the VM by hand.

## By hand

```sh
sudo hekate-labctl stats                 # what the host sees, and the snapshots
sudo hekate-labctl snapshot before-x     # a snapshot (the oldest beyond two is deleted)
sudo hekate-labctl rollback <name>       # restart on a snapshot
sudo hekate-labctl isolate | reconnect   # unplug / plug the VM's network
sudo systemctl stop | start hekate-lab-vm  # shut the VM down / start it (only with its firewall)
virsh -c qemu:///system console hekate-lab
```

If a check cut the lab off: read what it found (ask her, or `lab_status`), then roll back to a
snapshot from before, rebuild with `build-vm.sh --replace` (remove the old machine from the
Tailscale admin console first, and look at what the apps' data folders hold before reusing them),
or reconnect if it was a false alarm.
