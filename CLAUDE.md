# Hekate

Personal AI assistant with a 3D animated Renamon avatar: a Go service on the homelab (agent on
Neuralwatt GLM 5.3, tools, scheduler, STT/TTS) feeding a React + three.js PWA that renders the
character, lip-syncs and plays gestures.

**Read first:** `docs/PLAN.md` (design + milestones), `docs/DEVELOPMENT.md` (change flow, gates,
deploys), `docs/HANDOFF.md` (character slice and asset pipeline), `docs/PERSONA.md`.

## Layout

- `service/`: Go service (`go test ./...`). Persona prompt in `service/persona/`.
- `app/`: React PWA + the framework-free three.js `Character` (`npm run check`).
- `deploy/`: Dockerfile, compose/Caddy examples, homelab deployer.
- `asset-pipeline/nukorio/`: reproducible Blender pipeline (built by GPT-6 Astra) that bakes the
  purchased rig + Mixamo clips into `assets/renamon-nukorio/` (GLBs + `manifest.json`).
- `Renamon-Nukorio/`, `mixamo/`, `assets/`: third-party character data. **Never commit or
  publish** models, textures, renders or screenshots of the character.

## Working here

- **The repo is public.** No secrets, real hostnames/domains/IPs, or personal data. Real config
  lives on the homelab; examples use placeholders.
- `main` is protected: branch, PR, `scripts/ai-review.sh`, squash-merge. Merges auto-deploy.
- The app is manifest-driven: read rig facts (bones, morphs, clips, outlines) from
  `assets/renamon-nukorio/manifest.json` instead of hard-coding them. three.js sanitizes node
  names (`head.x` → `headx`); use the manifest's `threejs_runtime_names`.
- After app changes: `npm run check`, then the `verify:*` scripts against the dev server and look
  at the screenshots (write them to `app/verify-out/`, which is git-ignored).
- 3D asset work goes to Astra through a written brief with a hard write scope (it needs
  `danger-full-access` for Blender), then verify its output yourself: renders, GLB inspection,
  source hash, files touched.
- Blender: `/Applications/Blender.app/Contents/MacOS/Blender -b --factory-startup
  --disable-autoexec`.
- Don't build mocks or throwaway stand-ins for planned features; build them for real when their
  turn comes.
