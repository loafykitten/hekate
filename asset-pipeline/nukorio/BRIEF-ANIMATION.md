# Asset brief, round 3: retarget Mixamo animations onto the Nukorio Renamon

Round 2 (`BRIEF.md`, `NOTES.md`, `build_nukorio.py`) produced
`assets/renamon-nukorio/renamon-{desktop,phone}.glb` + `manifest.json` (schema 2)
with a 110-bone deform skeleton, morph-target face, and a humanoid bone map. They
have no animation clips. This round adds Mixamo clips to both GLBs.

The app (three.js) will: loop an idle; play `talking` (or layer parts of it) while
the assistant speaks; fire one-shot gestures (wave, nod, no, …) and crossfade back
to idle. It layers procedural motion on top: tail sway, ear twitches, a small head
nod/chin lift, blinks and mouth morphs. So **clips must not key the tail, ears, or
face**, and should leave the head free enough for a small additive nod.

## Environment and scope

Same as round 2: Blender 5.2.1 headless with `--factory-startup --disable-autoexec`,
no add-ons, no network. Write ONLY inside `asset-pipeline/nukorio/` and
`assets/renamon-nukorio/` (scratch in `asset-pipeline/nukorio/tmp/`). Never modify
`Renamon-Nukorio/`, `mixamo/`, `app/`, or `assets/renamon/`.

## Inputs (`mixamo/`)

All are binary FBX at 30 fps on Mixamo's default character, same 65-bone
`mixamorig:*` skeleton (root `mixamorig:Hips`, five-finger hands).

| File | Frames | Notes |
|---|---:|---|
| `Idle.fbx` | 1–121 | **With skin**: use it as the reference bind/rest skeleton |
| `Idle-2.fbx` | 1–251 | |
| `Talking.fbx` | 1–156 | |
| `Waving.fbx` | 1–143 | |
| `Thankful.fbx` | 1–91 | |
| `Acknowledging.fbx` | 1–59 | likely a nod |
| `Dismissing Gesture.fbx` | 1–68 | |
| `Excited.fbx` | 1–198 | |
| `No.fbx` | 1–151 | head shake |
| `Smh.fbx` | 1–220 | head shake / disappointment |
| `Female Standing Pose.fbx` | 1–2 | static pose |

## Deliverables

1. Extend the reproducible build so the clips are part of it (extend
   `build_nukorio.py` or add a stage it calls, your choice). Record the
   SHA-256 of every Mixamo input and refuse to run on mismatch, like the
   `.blend` check.
2. Both GLBs gain the same clips; skeleton, meshes and morphs stay exactly as
   delivered in round 2 (rest pose, names, morph data unchanged; verify).
3. Manifest (still `schema_version: 2`) gains `animations`: an array of
   `{ name, file, duration_seconds, frames, loop, category, description,
   source_sha256 }` with `category` one of `idle | talk | gesture | pose`.
4. Renders + NOTES updates as below.

## Retargeting requirements

- Clip names, lowercase snake_case: `idle`, `idle_2`, `talking`, `waving`,
  `thankful`, `acknowledging`, `dismissing`, `excited`, `no`, `smh`,
  `standing_pose`. Loops: `idle`, `idle_2`, `talking`, `standing_pose`. The rest are
  one-shots that start and end close to the idle stance, so crossfades are
  short and clean.
- Loops must loop seamlessly (first and last frame match; blend the tail
  into the head if the source doesn't).
- **Rest-pose difference.** Mixamo rests in a T-pose; Renamon's exported rest is an
  A-pose. Retarget by rotation relative to each rig's own rest so arms end up
  where the Mixamo character's arms are, not 45° off.
- **Proportions.** Use rotation retargeting for the limbs and spine; scale hip
  translation by leg-length ratio. **Strip horizontal root motion** (keep the
  character centered on its origin; keep vertical bob and hip rotation).
- **Legs.** Renamon has a digitigrade leg with an extra hock segment (see the
  round-2 humanoid map notes). Map Mixamo UpLeg/Leg/Foot/Toe sensibly and
  distribute knee bend across her segments so feet stay planted. Feet must not
  slide or sink through the ground plane (her rest-pose sole height) in idles
  and gestures.
- **Hands.** She has thumb, index and pinky digits. Map Mixamo Thumb → thumb,
  Index → index, and Pinky (or a Ring/Pinky blend, whichever looks better) → pinky.
- **Collisions.** Her sleeves are large and the shoulder/chest fur is bulky. Check
  hands and forearms don't sink into the torso, face or chest fur in any clip;
  apply per-clip or global arm-space offsets if needed. Small sleeve overlap
  is acceptable; arms through the body are not.
- **Don't key:** tail chain, ear chains, any face-related data. Head and neck
  may be keyed by the clips (nods and shakes are the point of `acknowledging`,
  `no`, `smh`), but note in the manifest which clips drive the head strongly,
  so the app can tone down its own head layer during them.
- Thin keyframes after retargeting only within a visual error tolerance; keep
  each GLB's growth reasonable (target ≤ 3 MB added per variant).
- Clips must bind in three.js: GLTFLoader sanitizes node names (`head.x` →
  `headx`); verify every track of every clip resolves to a node (no
  PropertyBinding "No target node found" warnings) using the installed
  `app/node_modules/three`.

## Pipeline robustness (from the round-2 review; fix while you're in there)

- `--manifest` currently trusts cached `verified-*.json` records without checking
  the staged GLBs. Bind verification records to the current GLB hashes (and
  input hashes) and reject stale ones.
- Promotion is two renames; an interruption after moving the current delivery
  aside leaves `assets/renamon-nukorio/` missing. Restore the previous delivery on
  failure and recover from an interrupted promotion on the next run.

## Verification you must do before finishing

- Fresh-process re-import of both GLBs: clip list, durations, rest pose and morph
  data unchanged from round 2 (compare against the round-2 delivery hashes and
  data), no tail/ear/face tracks.
- three.js: load both GLBs, play every clip with an `AnimationMixer` for its full
  duration, confirm all tracks bind and bones move; for loops, confirm first/last
  pose match within tolerance.
- Renders from the re-imported desktop GLB, per clip: a contact sheet (or separate
  PNGs) of ~5 evenly spaced frames, front and three-quarter, full body, named by
  clip. Look at them: arms through the body, broken wrists/fingers, feet sliding
  or sinking, knees bending backwards, T-pose/A-pose offset errors, popping. Fix
  and re-render until right. Compare overall motion against the source Mixamo
  clip (render the Mixamo character for the same frames if it helps you judge).
- If a clip can't be made to look good, ship the others, leave it out, and say
  why in NOTES and your final message.

## Final message

Clip list with durations/loop/category, size growth per variant, what was hard
(legs, hands, collisions) and how you handled it, clips dropped or of lower
quality, the robustness fixes, and confidence levels.
