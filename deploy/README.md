# Deploying Hekate

How the pieces fit is in `docs/DEVELOPMENT.md` ("Deploys"). This is the one-time server setup.

1. Pick a stack directory on the server (the examples use `/srv/hekate`) and copy into it:
   `compose.example.yaml` → `compose.yaml`, `hekate.env.example` → `hekate.env` (chmod 600),
   `radicale/config.example` → `radicale/config`, the `deployer/` folder and `hekatectl`.
   Optionally `deployer.env.example` → `deployer.env` (chmod 600) for Telegram alerts on failed
   deploys and an outside health ping (see below).
2. Create the bind-mounted directories yourself, owned by the user the service runs as (`user:`
   in compose, 1000:1000 in the example) and by you, the operator. Otherwise Docker creates them
   as root and the service can't write its database or the deployer its log:
   ```sh
   mkdir -p data state character radicale/data
   sudo chown 1000:1000 data            # skip if you are uid 1000
   chmod 600 hekate.env
   ```
   Put your character's `manifest.json` and GLBs in `character/`.
3. Create the calendar login: `htpasswd -B -c radicale/users <user>` (bcrypt).
4. Add the routes from `Caddyfile.example` to your reverse proxy and reload it.
5. `docker compose up -d`. The deployer from then on tracks `ghcr.io/<owner>/hekate:main`.
6. Check with `./hekatectl status`.

Alerts from outside: Hekate can't tell anyone it's down. Create a check at healthchecks.io (or
any dead-man's-switch service) with a 1-minute period and a few minutes' grace, connect its
Telegram integration, and put the ping URL in `deployer.env` as `HEALTHCHECK_URL`. The deployer
pings it each round while Hekate is healthy, so silence (Hekate down, the homelab off, the network
out) becomes a message. After changing `deployer/` or `deployer.env`:
`docker compose up -d --build deployer`.

Calendar clients: add a CalDAV account with server `https://dav.<your domain>/`, the user and
password from step 3. iOS/macOS: Settings → Calendar → Accounts → Other → CalDAV. Android: DAVx5.

Mail: for each account, create an app-specific password at your provider with IMAP enabled
(Zoho: Security → App Passwords; IMAP access must be on in Mail settings) and add
`HEKATE_MAIL_<n>_USER` / `HEKATE_MAIL_<n>_PASS` to `hekate.env` (see the example for the
optional host, label and archive folder). Restart the service; the first sweep caches the last
30 days of inbox headers and sorts the last 14 days.

Her lab (optional): a VM on the homelab she can set things up on. See `lab/README.md`.

## On your phone

1. With Tailscale connected, open `https://hekate.<your domain>/` in Safari (iOS) or Chrome
   (Android) and add it to the Home Screen. On iPhone, notifications only work for the Home Screen
   app.
2. Open the Home Screen app, allow the microphone the first time you hold the talk button, and
   turn on notifications in Menu → Settings.
3. Add the calendar: iOS Settings → Calendar → Accounts → Add Account → Other → CalDAV, server
   `dav.<your domain>`, with the login from step 3 above. Android: DAVx5 with the same details.
