# Hekate

A personal assistant with a 3D animated partner as its face. A Go service runs around the clock
on a home server: it talks to an LLM, keeps your calendar, tasks, reminders and lists, helps tame
your inbox, remembers what matters, and reflects on its day. A web app (an installable PWA)
reached over Tailscale renders the character full screen. You hold to talk, and she answers in
her own voice, with gestures and cards for the details.

It's built for one person and for daily life rather than technical work.

> **Status:** early. See [`docs/PLAN.md`](docs/PLAN.md) for the design and milestones.

## Pieces

- **`service/`**: Go. Agent loop on Neuralwatt-hosted GLM 5.3 (Flash for conversation, the full
  model when depth helps), tool modules (memory, tasks, reminders, CalDAV calendar, IMAP mail,
  web search, weather, self-inspection), a scheduler for briefings, heartbeats and nightly
  reflection, web push, local speech-to-text (Parakeet via sherpa-onnx) and ElevenLabs
  text-to-speech with timestamps for lip sync.
- **`app/`**: React + three.js PWA. Full-screen character, push-to-talk and text entry, cards,
  and a menu for everything she manages.
- **`deploy/`**: container image, compose and reverse-proxy examples, and a pull-based homelab
  deployer with health checks and rollback.
- **`asset-pipeline/`**: the Blender pipeline that bakes a rigged character into the GLBs and
  `manifest.json` the app reads.

## Bring your own character

The app is driven by an asset manifest, not hard-coded to one model. **No character models,
textures, animations or renders are included in this repository.** The character used in
development is purchased, third-party work, and the character it depicts belongs to its rights
holders. To run Hekate you need your own rigged model, processed into the manifest format
described in [`docs/HANDOFF.md`](docs/HANDOFF.md).

## Developing

See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) for the change flow, the gates on `main`
(CI, secret scanning, an AI review check) and how deploys work.

## License

[MIT](LICENSE), for the source code only.
