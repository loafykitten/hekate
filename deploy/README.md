# Deploying Hekate

How the pieces fit is in `docs/DEVELOPMENT.md` ("Deploys"). This is the one-time server setup.

1. Pick a stack directory on the server (the examples use `/srv/hekate`) and copy into it:
   `compose.example.yaml` → `compose.yaml`, `hekate.env.example` → `hekate.env` (chmod 600),
   `radicale/config.example` → `radicale/config`, the `deployer/` folder and `hekatectl`.
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

Calendar clients: add a CalDAV account with server `https://dav.<your domain>/`, the user and
password from step 3. iOS/macOS: Settings → Calendar → Accounts → Other → CalDAV. Android: DAVx5.
