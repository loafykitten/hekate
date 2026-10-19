# Asset brief, round 4: more gestures from the Mixamo gesture pack

Round 3 (`BRIEF-ANIMATION.md`, `NOTES.md`, `animate_nukorio.py`, `animation_pipeline.py`)
retargeted eleven Mixamo clips onto the Nukorio Renamon and appended them to
`assets/renamon-nukorio/renamon-{desktop,phone}.glb`, with `animations[]` in `manifest.json`.
This round adds thirteen more one-shot clips from a Mixamo gesture pack, through the same
pipeline and to the same standard. Everything round 3 required still holds (read its brief and
NOTES first); only what's new is spelled out here.

How the app uses them: the assistant puts gesture names in her replies (`{nod_yes}`), and the
service builds the list of allowed names from the manifest's `category: "gesture"` clips. The
app plays a gesture over the calm base idle (`idle_2`) and crossfades back. Clips flagged
`idle_variation` will also be played now and then while she's idle, so they must look natural
with no conversation going on.

## Environment and scope

Same as rounds 2 and 3: Blender 5.2.1 headless with `--factory-startup --disable-autoexec`, no
add-ons, no network. Write ONLY inside `asset-pipeline/nukorio/` and `assets/renamon-nukorio/`
(scratch in `asset-pipeline/nukorio/tmp/`). Never modify `Renamon-Nukorio/`, `mixamo/`, `app/`,
`service/` or `assets/renamon/`.

## Input

`mixamo/Gestures Pack Basic.zip` (read it in place; extract to `tmp/` if you need files). Pin
the SHA-256 of the zip **and** of each member you use in `mixamo-inputs.json`, and refuse to run
on any mismatch, like the existing inputs. The zip holds `X Bot.fbx` (Mixamo's X Bot with skin,
same 65-bone `mixamorig:*` skeleton, same rest proportions and T-pose as `Idle.fbx`) and fifteen
animation FBXs without skin, binary, 30 fps, downloaded with keyframe reduction off. Their key
counts are lower than their frame counts (the pack's source data is sparser than round 3's), so
sample per frame when retargeting. `Idle.fbx` stays the bind/rest reference.

| Member | Frames | Clip name | Category | `idle_variation` |
|---|---:|---|---|---|
| `head nod yes.fbx` | 1–73 | `nod_yes` | gesture | no |
| `hard head nod.fbx` | 1–50 | `hard_nod` | gesture | no |
| `lengthy head nod.fbx` | 1–52 | `long_nod` | gesture | no |
| `sarcastic head nod.fbx` | 1–66 | `sarcastic_nod` | gesture | no |
| `shaking head no.fbx` | 1–52 | `head_shake_no` | gesture | no |
| `annoyed head shake.fbx` | 1–74 | `annoyed_head_shake` | gesture | no |
| `thoughtful head shake.fbx` | 1–93 | `thoughtful_head_shake` | gesture | no |
| `look away gesture.fbx` | 1–55 | `look_away` | gesture | yes |
| `angry gesture.fbx` | 1–66 | `angry` | gesture | no |
| `being cocky.fbx` | 1–87 | `cocky` | gesture | no |
| `happy hand gesture.fbx` | 1–83 | `happy_hands` | gesture | no |
| `relieved sigh.fbx` | 1–91 | `relieved_sigh` | gesture | yes |
| `weight shift.fbx` | 1–284 | `weight_shift` | idle | yes |

Skip `acknowledging.fbx` and `dismissing gesture.fbx`: they duplicate round 3's
`acknowledging` and `dismissing`, which stay as they are. Don't change any round-3 clip.

## Deliverables

1. The thirteen clips are part of the reproducible build (extend the round-3 stage; keep one
   build command). Rebuilding with unchanged inputs stays byte-identical.
2. Both GLBs gain the same thirteen clips. Skeleton, meshes, morphs and the eleven existing clips
   stay exactly as delivered (verify against the current delivery's data, as round 3 did against
   round 2).
3. Manifest (still `schema_version: 2`): each new clip gets an `animations[]` entry with the
   existing fields (`name, file, duration_seconds, frames, loop, category, description,
   source_sha256, head_motion`), all `loop: false`. Add a boolean `idle_variation` to **every**
   clip (true only for the three above; false for the round-3 clips). Write each `description`
   as a short plain phrase saying what she does and when it fits (the assistant may read these),
   e.g. "Two quick nods: agreeing or saying yes."
4. `weight_shift` is a ~9.5 s idle variation: it must start and end on the `idle` stance like the
   gestures. If the source steps, keep her planted as round 3 did; if it's mostly a slow sway,
   preserve that.
5. Renders + NOTES updates as in round 3, plus a section for this round.

## Requirements that matter most this round

- Same retargeting method as round 3 (own-rest rotation deltas, A-to-T arm calibration, leg-length
  hip scaling, horizontal root motion stripped, digitigrade FABRIK feet, thumb/index/pinky).
- Start and end every clip close to the `idle` first-frame stance, like round 3's gestures, so
  the app's crossfades are short and clean.
- Most of these are head clips: key head and neck freely, set `head_motion` honestly (`strong`
  for nods and shakes), and keep the arms quiet where the source keeps them quiet. No tail, ear
  or face keys.
- Hands and arms must not pass into the torso, face or chest fur (`angry`, `cocky` and
  `happy_hands` move the arms most; check them closely).
- Size: target ≤ 2 MB added per variant for the thirteen clips together; thin keys only within
  the visual tolerance round 3 used.
- Clips must bind in three.js with the installed `app/node_modules/three` (no
  "No target node found" warnings).
- Pipeline robustness stays as round 3 left it: staging, fresh-process verification bound to
  GLB and input hashes, atomic promotion with recovery, and the failure-path tests still pass
  (add cases for the new inputs if they need any).

## Verification you must do before finishing

- Fresh-process re-import of both GLBs: 24 clips, durations, rest pose, morphs and the eleven old
  clips' samples unchanged; no tail/ear/face tracks.
- three.js: load both GLBs, play every new clip for its full duration with an `AnimationMixer`,
  all tracks bind, bones move, endpoints near the `idle` stance.
- Contact sheets per new clip (five evenly spaced frames, front and three-quarter, full body),
  compared against the source motion on X Bot. Look at them yourself: arms through the body,
  broken wrists or fingers, feet sliding or sinking, knees bending backwards, popping, a nod that
  reads as a twitch. Fix and re-render until right.
- If a clip can't be made to look good, ship the others, leave it out of both the GLBs and the
  manifest, and say why in NOTES and your final message.

## Final message

Clip list with durations, category, `idle_variation` and `head_motion`; size growth per variant;
what was hard and how you handled it; clips dropped or of lower quality; confidence levels; the
exact rebuild and test commands.
