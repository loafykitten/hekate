# Hekate handoff (2026-10-19)

State of the project at the end of the first session, written so a fresh session can start
**planning** without re-deriving anything. Read this first; `asset-pipeline/nukorio/NOTES.md`
has the asset-level detail.

## What we're building

A personal AI assistant with a 3D animated character (Renamon) as its face:

- A **service on the user's `homelab` machine** runs the LLM, TTS (ElevenLabs) and the logic
  that turns a reply into a "performance" for the character.
- **Client apps on phone and desktop** render the character, play the audio, lip-sync, show a
  speech bubble, and play gestures/expressions the LLM asks for.
- The code may be published on GitHub, but **the character assets never are**: they're
  third-party IP (see Licensing). Other users would bring their own models, so the app is driven
  by an asset **manifest**, not hard-coded to this rig.

The user is new to 3D. Explain 3D topics in plain terms and make the asset decisions concrete.

## Where things stand

**Update 2026-10-19 (evening):** milestones M0–M2 of `docs/PLAN.md` are merged and auto-deployed
to the homelab: the Go service (agent on GLM 5.3, ElevenLabs v4 voice, local Parakeet speech-to-
text, tasks/reminders/push/memory/CalDAV calendar/web/weather) and the React PWA (push-to-talk,
cards, menu). The rest of this document describes the original character slice, which the app
still builds on; `docs/PLAN.md`, `docs/DEVELOPMENT.md` and `docs/PROTOCOL.md` describe the current
system. Next: M3 (mail) and M4 (proactive: briefings, heartbeat, reflection).

The **vertical slice** (first session) proved the model works as an avatar:

What the slice (`app/`) does today:

- Loads `assets/renamon-nukorio/renamon-{desktop,phone}.glb` (chosen by screen size) with toon
  shading and per-mesh outlines (three.js `OutlineEffect`, colors/thickness from the manifest).
- Idle loop (`idle_2`); crossfades to the `talking` loop while speaking and back; 7 one-shot
  gestures from a dev-only dropdown (`waving`, `thankful`, `acknowledging`, `dismissing`,
  `excited`, `no`, `smh`).
- Type text and press Say: the dev server synthesizes speech with **macOS `say`** (placeholder),
  the mouth (`jawOpen` + a little `mouthSmile` morph) follows a per-utterance-normalized loudness
  envelope, and the bubble types the text out in sync with playback.
- Procedural layers on top of clips: breathing, tail sway, ear twitches, head sway, slight chin
  lift while speaking, blinks every 2–6 s (morphs). The head layer backs off during clips that
  move the head a lot.

### Run it

```sh
cd app && npm install && npm run dev -- --port 5317 --strictPort   # http://localhost:5317/
HEKATE_LAN=1 npm run dev                                           # expose on LAN (phone testing)
```

Debug URL params: `?variant=phone|desktop`, `?view=face`, `?mouth=0..1` (pin mouth open),
`?morph=viseme_O:1&morph=eyeBlink_L:0.5` (pin morph weights), `?gesture=waving`, `?blink=0`,
`?say=Hello` (speak on load; needs a prior user gesture for audio in real browsers). In dev builds
the `Character` is exposed as `window.hekate`.

### Verify it

With the dev server running (scripts use the installed Google Chrome headless with software
WebGL via `playwright-core`; no browser download):

| Command (in `app/`) | What it checks |
|---|---|
| `npm run typecheck` | client + dev server |
| `npm run verify:screens -- <outDir>` | desktop, phone, face close-up, mid-speech screenshots |
| `npm run verify:clips` | idle → gesture → idle → talk → idle; all animation tracks bind |
| `npm run verify:blend` | crossfade edge cases: weights sum to 1, no outgoing weight rebounds, no pops |
| `npm run verify:strip -- <out.png> <gesture\|talk>` | contact sheet of frames through a transition |
| `npm run verify:loop` | exactly one render loop after load (StrictMode remount regression) |

Write screenshot output outside the repo or into `app/verify-out/` (git-ignored). They contain
the character.

## Code map (`app/`)

Since M0 (2026-10-19) the app is a React PWA shell around these framework-free modules, which
now live in `src/character/`; the render loop and debug params moved from `main.ts` into
`src/character/stage.ts`, and the macOS `say` stand-in (`/api/tts`, `?say=`) is gone. Dev asset
serving is `server/devAssets.ts`. See `docs/DEVELOPMENT.md` for the current layout.

- `src/character.ts`: `Character` class. Loads the GLB, converts materials to toon (keeps
  `KHR_materials_unlit` eyes unlit, copies `side`), sets per-mesh outline params, indexes morph
  targets by name across meshes, and runs the animation system:
  - **Custom crossfader** (`blend`/`fadeTo`/`updateBlend`): each fade snapshots all current
    weights and smoothstep-interpolates the whole set to the target, so weights always sum to 1
    and outgoing clips only decrease. three's `crossFadeTo` popped when interrupted.
  - **Gestures**: two action instances per clip so a gesture can restart while its last run fades
    out; re-requesting the active gesture is a no-op; the return to the loop starts
    `fadeOut` seconds before the clip ends (fades scale down for short clips).
  - **Loop entry at the closest phase** (`closestPhase` via track interpolants), so idle↔talk
    doesn't swing limbs.
  - **Procedural layering**: driven bones are reset to last frame's clip output before
    `mixer.update` (the mixer skips writing unchanged values, so offsets would otherwise
    accumulate), then layered.
- `src/manifest.ts`: maps `manifest.json` (schema 2) → `CharacterRig` (head bone with three's
  sanitized runtime names, mouth/blink morph names, outlines, clips, procedural bones,
  `chinLift`). Picks the phone/desktop variant.
- `src/speech.ts`: Web Audio playback. Precomputes a 10 ms loudness envelope normalized to the
  utterance's 90th percentile; keys everything off the *heard* time (`currentTime − start −
  outputLatency`) so Bluetooth latency doesn't desync the mouth/bubble.
- `src/bubble.ts`: bubble anchored above the head, clamped on screen, reveals text by playback
  progress.
- `src/main.ts`: renderer/camera/framing, the `say()` flow (newest request wins; audio unlocked
  on the user gesture), mouth smoothing, dev gesture picker, debug params.
- `server/devApi.ts`: **dev-only stand-in for the homelab service**. `POST /api/tts {text}`
  → WAV via `say` (one at a time, 20 s timeout, 1000-char limit); `GET /character/<file>` serves
  the asset folder (manifest + GLBs only, no traversal). Loopback-only unless `HEKATE_LAN=1`.

## Assets

### The character

`assets/renamon-nukorio/` (git-ignored), built from the purchased Blender rig
`Renamon-Nukorio/Renamon_V2.blend` (Auto-Rig Pro-style body rig + custom face rig by the artist;
README in that folder) plus Mixamo clips in `mixamo/`.

| Variant | Triangles | Size | Body texture |
|---|---:|---:|---:|
| desktop | 77,472 | 11.4 MB | 2048² |
| phone | 29,223 | 5.0 MB | 1024² |

- 110-bone deform skeleton, A-pose rest, +Y up, facing +Z. Face bones were baked into morphs and
  their weights merged into `head.x`.
- **14 morphs** on `head`/`eye_black`/`eye_line`: `jawOpen`, `viseme_aa|E|I|O|U|PP`,
  `mouthSmile`, `mouthFrown`, `eyeBlink_L|R`, `eyeSquint`, `browUp`, `browDown`. Visemes are
  self-contained (include their jaw opening); don't add `jawOpen` on top of a full viseme.
- **24 clips** (30 fps Mixamo, retargeted; round 4 added thirteen from Mixamo's gesture pack:
  nods, head shakes, `look_away`, `angry`, `cocky`, `happy_hands`, `relieved_sigh`,
  `weight_shift`). Clips flagged `idle_variation` (`look_away`, `relieved_sigh`, `weight_shift`)
  play now and then while the conversation is idle (every 25–60 s), and give way at once when
  anything happens. Round 3's clips: loops `idle` (busy: arms lift mid-cycle), `idle_2`
  (calm, the app's base), `talking`, `standing_pose`; gestures as listed above. Gestures start and
  end on `idle`'s first-frame stance (arms down), so from `idle_2` (hands clasped) every gesture
  opens with the hands unclasping. That reads fine with the current fades; an optional fix is to
  have Astra rebase the gestures onto `idle_2`'s posture.
- Manifest highlights the app relies on: `files.{desktop,phone}`,
  `skeleton.{head_bone,tail,ears,humanoid,threejs_runtime_names}`, `lipsync.amplitude_only`,
  `lipsync.viseme_based` (Oculus phoneme class → morph weights, ready for real lip sync),
  `blink.{left,right}`, `outline.meshes`, `animations[].{name,loop,category,head_motion,idle_variation}`.

### The pipeline

`asset-pipeline/nukorio/` was built by **GPT-6 Astra** (Codex CLI) from the briefs in that folder
(`BRIEF.md` = bake round, `BRIEF-ANIMATION.md` = Mixamo round). It's reproducible
(byte-identical rebuilds), refuses to run if any source hash changes, builds in staging, verifies
in fresh Blender processes and three.js, and promotes atomically with rollback. Rebuild and test
commands are in `NOTES.md`. Note: rebuilding changes nothing unless sources or scripts change.

## Decisions so far (and why)

- **Model: the Nukorio purchased rig.** Rejected a Sketchfab statue (posed sculpt, no rig) and a
  Digimon game rip (no mouth: Astra cut one in, but it looked like a gaping black wedge). The
  Nukorio rig has an artist-made mouth and full face rig; we capture it into morphs instead of
  inventing geometry.
- **Who does what:** 3D asset work goes to **Astra** via written briefs (it led the
  rigging/character-prep benchmarks). Claude writes the app/service and **independently verifies
  Astra's output** (renders, GLB inspection, scope checks), because Astra's self-reports have
  been accurate but are not taken on faith. Reviews go to gpt-6.1-sol (see the global CLAUDE.md).
- **Animations from Mixamo**, downloaded by the user (FBX Binary, 30 fps, no keyframe reduction,
  first file with skin), retargeted by Astra. No tail/ear/face keys in clips; the app drives
  those procedurally.
- **Base idle = `idle_2` (calm)**, not Mixamo `Idle` (busy, arm swings up to 113°).
- **Lip sync today is loudness-driven** (`jawOpen` + 0.15 `mouthSmile`). Viseme-driven sync
  waits for real per-character timing from the TTS.

### Agreed design for the assistant pipeline (not built yet; "no mocks, build it for real")

1. LLM output contains **our gesture tags in curly braces** (`Hi! {waving} I checked…`) and
   **ElevenLabs expression tags in square brackets** (`[laughs]`).
2. The **service** extracts `{…}` tags and records their character offsets. Unknown names are
   dropped, never spoken; the allowed list comes from the asset manifest's gestures.
3. Send ElevenLabs the text with only its own tags, using the **with-timestamps** API (per-character
   timing).
4. Strip `[…]` tags for display text; map gesture offsets and words to times.
5. Send the app `{ audio, text, wordTimings, events: [{ gesture, at }] }`. The app plays the
   audio, fires gestures at their times, reveals words on schedule, mouth from loudness (later
   visemes).
- The system prompt's gesture list is **generated from the manifest**, with a rule like "at most
  1–2 per reply".
- **Streaming is wanted** (LLM → TTS → app sentence by sentence); design the payload for it from
  the start even if v1 sends whole replies.
- Idea to carry into planning: map ElevenLabs tags to faces (`[laughs]` → `eyeSquint` +
  `mouthSmile`, `[sighs]` → `browDown`) using the existing morphs.

## Planning agenda (next session)

1. **Homelab service**: language/runtime, LLM provider and model, ElevenLabs integration, tag
   parsing, conversation memory, how the service is reached from phone/desktop (auth, network:
   LAN/VPN/tunnel), and how it serves assets.
2. **Payload/stream contract**: message shapes for whole replies and streamed sentences, audio
   format, timing data, gesture/expression events, interruption (user speaks while she talks).
3. **Clients**: the web slice as the shared renderer; packaging for desktop (e.g. a webview
   shell) and phone (PWA vs a native wrapper); mic input / speech-to-text; upper-body framing on
   phones (she's small on phone screens today).
4. **Character features**: viseme lip sync from TTS timings, ElevenLabs tag → expression map,
   more gestures (Mixamo), optional gesture rebase onto `idle_2`.
5. **Repo hygiene**: the project isn't a git repo yet; init before real work. Keep assets out
   (the `.gitignore` already covers them).

## Open questions / things to verify early

- ~~ElevenLabs timestamps with audio tags~~ **Answered 2026-10-19**: tags *do* appear in the
  `with-timestamps` alignment, character by character, and on `eleven_v3` they span the time
  the sound takes (`[chuckles]` covered 0–0.84 s). So: drop tag characters from word timings,
  and use a tag's first character time as the expression's `at`. Latency for a short sentence:
  `eleven_v3` ~1.7 s (supports tags), `eleven_flash_v2_5` ~0.26 s (no tags). **Default is now
  `eleven_v4`** (same timestamps and tag behavior, ~1.25 s vs v3's ~2.65 s for a typical sentence;
  `eleven_v4_turbo` ~0.9 s), measured 2026-10-19.
- **Homelab OS/hardware**: unknown. `say` (macOS) is only a dev placeholder.
- Which LLM and whether the user wants local inference on the homelab or an API.
- How the user wants to talk to it (push-to-talk, wake word, typing).

## Licensing and hygiene

- `Renamon-Nukorio/` is a **purchased** rig; `mixamo/` clips are from Adobe Mixamo; Renamon is
  Bandai/Toei IP. Personal use on the user's own devices is the agreed scope. Never commit or
  publish models, textures, renders or screenshots of the character (`.gitignore` covers
  `Renamon-Nukorio/`, `mixamo/`, `assets/`, model formats, reference renders, `app/verify-out/`).
- Open the `.blend` only with `--factory-startup --disable-autoexec` (third-party file). Its 562
  drivers are simple expressions, so they evaluate without Python.
- Headless Blender crashes inside Codex's macOS sandbox (USD arch check). Astra runs need
  `-s danger-full-access`, so briefs must state a hard write scope; check it afterwards.
- Old/rejected assets were moved to the macOS Trash on 2026-10-19 (game rip, round-1 pipeline).
