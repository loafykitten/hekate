# Developing Hekate

This repo is maintained mostly by AI agents. The gates below make it hard to ship something
broken, and they keep every change reviewable after the fact. Read `PLAN.md` for what we're
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

1. Branch from `main`: `feat/<topic>`, `fix/<topic>` or `chore/<topic>`.
2. Make the change with tests. Run locally:
   - `cd service && go vet ./... && go test ./...` (and `golangci-lint run` if installed)
   - `cd app && npm run check` (typecheck, lint, unit tests, build)
   - for UI changes, start the dev servers and run `npm run verify:screens -- app/verify-out/<name>`,
     then look at the screenshots
3. Push and open a PR with `gh pr create --fill` (or a written body: what, why, how verified).
4. **AI review**: `scripts/ai-review.sh` (see below). Fix what's real, push, run it again.
5. Merge with `gh pr merge --squash --delete-branch` once the required checks are green.
   Auto-merge (`--auto`) is fine.
6. The merge builds an image and the homelab deployer rolls it out within ~2 minutes. Confirm
   with `curl -s https://<hekate-host>/healthz` (it reports the deployed commit).

## Gates on `main`

Branch protection (applies to admins too, so agents can't push around it):

- Pull request required. Squash merge only, linear history, branches auto-deleted.
- Required status checks:
  - `service`: gofmt, `go vet`, golangci-lint, `go test -race`
  - `app`: typecheck, lint, unit tests, production build
  - `hygiene`: gitleaks secret scan, and no character assets or model files in the tree
  - `ai-review`: a commit status on the PR's head SHA, posted by `scripts/ai-review.sh`
- Any new push invalidates `ai-review`, because the status is tied to the commit SHA.

### `scripts/ai-review.sh`

Runs an independent Codex review of the PR (`gpt-6.1-sol`, high effort by default) against its
base branch. It asks for a structured verdict, posts the findings as a PR comment, and sets the
`ai-review` status on the head commit:

- **success**: no blocker or major findings
- **failure**: anything blocking. Fix it, push, rerun.

```sh
scripts/ai-review.sh                      # Sol, high effort
scripts/ai-review.sh --astra              # GPT 6 Astra for complex or touchy changes
scripts/ai-review.sh --override "why"     # finding verified as a false positive: records the reason
```

Verify findings against the code before acting on them. Override only a finding you've shown to
be wrong. The override reason is posted on the PR for the record.

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

## Change requests: the worker

Renamon proposes changes with `propose_change`; the owner approves them in the app (see
`PLAN.md`, "Next"). Approved ones are built by `scripts/change-worker/` on the development
machine, which reaches Hekate over the tailnet like any of the owner's devices:

- `watch.sh` (launchd, every 90 s) claims the oldest queued change, one at a time.
- `run.sh ID` makes a worktree of `origin/main` on `change/ID` and runs a headless Claude Code
  session (`build-prompt.md`, auto permission mode, Opus at high effort). That session files a
  sanitized issue, builds the change, opens the PR and runs `ai-review.sh --astra` (up to three
  rounds). A second, fresh session (`merge-prompt.md`, no file edits) checks CI, the review, the
  diff's scope and privacy, then merges or closes the PR. `run.sh` confirms the PR state with
  GitHub before reporting the outcome.
- Progress and outcomes go to Hekate (`report.sh`, the `/api/changes` endpoints in
  `PROTOCOL.md`); the owner sees them in the app and by push.

Setup, once: create `~/.config/hekate/change-worker.env` with `HEKATE_URL=https://hekate.<domain>`
(optionally `CHANGE_WORKER_BUDGET_USD=` to cap each session), make sure `gh` and `codex` are
logged in, then run `scripts/change-worker/install.sh` (`--remove` to uninstall). Logs and
per-change transcripts are in `~/.local/state/hekate-change-worker/`. The launchd job runs the
scripts from this checkout, so keep it on `main`.

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
