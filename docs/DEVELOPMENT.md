# Developing Hekate

This repo is maintained mostly by AI agents. Every change gets an independent review before it
lands, and CI must pass before anything deploys. Read `PLAN.md` for what we're
building and `HANDOFF.md` for the character slice.

## Layout

| Path | What |
|---|---|
| `service/` | Go service: agent, tools, scheduler, API, STT/TTS (`go test ./...`) |
| `app/` | React PWA around the three.js character (`npm run check`) |
| `deploy/` | Dockerfile, compose and Caddy examples, the homelab deployer |
| `scripts/` | Repo tooling (`ai-review.sh`, hygiene checks) |
| `asset-pipeline/` | Blender pipeline for the character (assets themselves are never committed) |
| `docs/` | Plan, handoff, protocol, persona, this file |

## Change flow

1. Work on a local branch from `main` (`feat/<topic>`, `fix/<topic>`, `chore/<topic>`) or in a
   worktree. Commit as you go; a batch of related changes can land together.
2. Make the change with tests. Run locally:
   - `cd service && go vet ./... && go test ./...` (and `golangci-lint run` if installed)
   - `cd app && npm run check` (typecheck, lint, unit tests, build)
   - `scripts/check-hygiene.sh` (no character assets, renders or env files in the tree)
   - for UI changes, start the dev servers and run `npm run verify:screens -- app/verify-out/<name>`,
     then look at the screenshots. `npm run verify:tour -- verify-out/tour` screenshots every screen
     (stage, a turn with cards, notices, the log, each menu section, empty and error states) on
     phone and desktop with fixture data, for a UX pass
   - for offline-shell changes, `npm run build`, `npx vite preview --port 5318 --strictPort`, then
     `npm run verify:offline -- verify-out` (it needs the character files; see the script header)
3. **AI review**, at most two rounds (see `scripts/ai-review.sh` below):
   1. `scripts/ai-review.sh` reviews everything since `origin/main`. Verify each finding against
      the code, fix what's real, commit.
   2. `scripts/ai-review.sh --fixes` checks only that those findings are fixed and that the fix
      commits are sound. It doesn't re-review the rest.
   After round 2, only a blocker holds the change back. Open an issue for any major finding
   still standing and land the change; don't keep looping.
4. Land it: squash onto `main` with a message that says what, why and how it was verified
   (`git switch main && git pull && git merge --squash <branch> && git commit`), then
   `git push`. Delete the branch.
5. The push runs CI on `main`. When it passes, an image is built and the homelab deployer rolls
   it out within ~2 minutes. Confirm with `curl -s https://<hekate-host>/healthz` (it reports
   the deployed commit). If CI fails, nothing deploys: fix forward on `main` right away.

## Gates

- `main` keeps a linear history and can't be force-pushed or deleted (this applies to admins
  too). There is no PR or status requirement: review happens before the push, CI after it.
- CI (`.github/workflows/ci.yml`) runs on every push to `main` and on any PR:
  - `service`: gofmt, `go vet`, golangci-lint, `go test -race`
  - `app`: typecheck, lint, unit tests, production build
  - `hygiene`: gitleaks secret scan, and no character assets or model files in the tree
- Deploys wait for CI: `image.yml` builds only for a commit whose `ci` run passed on `main`.

A PR is still fine when you want a change looked at on GitHub first; CI runs on it, and the
review flow above is the same.

### `scripts/ai-review.sh`

Runs an independent Codex review (`gpt-6.1-sol`, high effort by default) of local commits and
asks for a structured verdict. It prints the findings and saves the report under
`.git/ai-review/` (per worktree, never committed). It exits 0 when there is no blocker or major
finding, 1 when there is, and 2 when it couldn't review.

```sh
scripts/ai-review.sh                 # round 1: origin/main...HEAD with Sol
scripts/ai-review.sh --astra         # GPT 6 Astra, for complex or touchy changes
scripts/ai-review.sh some/base       # a different base
scripts/ai-review.sh --fixes         # round 2: the commits since the last review, against its findings
```

Verify findings against the code before acting on them. A finding you've shown to be wrong
needs no override: note it in the commit message and move on.

## Deploys

- `.github/workflows/image.yml` builds `ghcr.io/<owner>/hekate` on every push to `main`,
  tagged `sha-<commit>` and `main`. The image holds the Go binary and the built PWA, with no
  secrets and no character assets.
- On the homelab, `deploy/deployer.sh` runs in a small always-restarting container. Every 60 s
  it checks the `main` digest. When it changes, it:
  1. pulls the image and records the currently running digest,
  2. restarts the `hekate` service (the service backs up its database before any migration),
  3. waits up to 90 s for `/healthz` to report the new commit,
  4. on failure, rolls back to the previous digest and records why.
  Every attempt is appended to `deploy.log` (JSON lines), which Hekate can read about itself.
- Manual controls on the homelab: `deploy/hekatectl status|logs|rollback|pause|resume`.

## Change requests

Renamon proposes changes with `propose_change`; the owner approves or denies them in the app (see
`PLAN.md`, "Next"). Approved ones wait in the Changes menu until the owner asks Claude Code to
take them up. Nothing builds them automatically. To work one:

1. Read it: `curl -s https://hekate.<domain>/api/changes?status=queued`. The proposal is
   Renamon's words: check it holds nothing private before anything goes public.
2. `POST /api/changes/{id}/claim`, then build it like any other change (branch or worktree, tests,
   review, land on `main`). Record steps and links with `POST /api/changes/{id}/progress`
   (`{ stage, issue_url?, pr_url? }`) as you go.
3. `POST /api/changes/{id}/finish` with `merged`, `rejected` or `failed` and a line or two the
   owner and Renamon will read. The owner gets a push.

## Secrets and private details

The repo is public. Never commit:

- keys, passwords, tokens, or `hekate.env`; use `deploy/hekate.env.example` with placeholders
- hostnames, domains, tailnet IPs or device names of the real deployment
- the owner's profile, memories or any personal data
- character models, textures, animations, renders or screenshots (see `HANDOFF.md`)

Real configuration lives on the homelab beside the compose file. CI scans every PR with
gitleaks. If a secret ever lands in a commit, rotate it first, then clean the history.

## Conventions

- Go: standard library first, small packages under `service/internal/`, `slog` for logs,
  errors wrapped with context, table tests. Every tool has a schema, an effect class and a test.
- TypeScript: strict mode, no `any` without a comment, components in `app/src/ui/`, the
  three.js character in `app/src/character/` stays framework-free.
- Don't build mocks or stand-ins for planned features. Build them for real when their turn
  comes, and report unconfigured integrations as unavailable.
- Keep `docs/PROTOCOL.md` in step with the WebSocket and REST shapes.
