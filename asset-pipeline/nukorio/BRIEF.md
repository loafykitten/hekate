# Asset brief, round 2: bake the Nukorio Renamon rig into an app-ready GLB

You are preparing a 3D character for a real-time desktop/phone AI assistant built
on three.js. The app loads a GLB, plays a procedural idle (breathing, tail and ear
sway, blinks), and lip-syncs the character to TTS audio by blending **morph
targets**. Later, Mixamo animations will be retargeted onto the exported skeleton.

The source is a professionally rigged Blender file. It was built for animating in
Blender: control rig, drivers, lip ribbons, geometry-nodes outlines, procedural eye
shaders. None of that survives glTF export. Your job is to **capture what the
artist's rig produces** (especially the face) into plain glTF data: a skinned
mesh, a deform skeleton, and morph targets. Do not invent new mouth geometry. The
artist's mouth is the reason we switched to this model.

## Environment

- Working dir: `the repo root` (not a git repo).
- Blender 5.2.1: `/Applications/Blender.app/Contents/MacOS/Blender`. Always run
  `Blender -b --factory-startup --disable-autoexec ...`. The file is a third-party
  purchase; never enable auto-run scripts. All 562 drivers in it are simple
  expressions/averages, so they evaluate without Python. Do not install the bundled
  `rig_tools` add-on; it's an animator UI and isn't needed.
- Node 24 is available (`app/node_modules/three` is installed; read-only use is fine
  for a loader check, as in round 1's `asset-pipeline/verify_threejs.mjs`).
- Round 1 (a different model) lives in `asset-pipeline/build_renamon.py`; reuse its
  patterns where they help (fresh-process re-import verification, renders,
  manifest generation).

## Write scope (hard rule)

- Write ONLY inside `asset-pipeline/nukorio/` and `assets/renamon-nukorio/`.
  Scratch files go in `asset-pipeline/nukorio/tmp/`.
- Never modify `Renamon-Nukorio/` (the source), `app/`, `assets/renamon/`, or
  anything else. No network access needed.
- Set `TMPDIR`, Python's `tempfile.tempdir`, and Blender's temp dir to
  `asset-pipeline/nukorio/tmp/` at the very top of the script, before any import or
  export, so nothing lands in system temp.

## Source facts (verify, don't trust blindly)

`Renamon-Nukorio/Renamon_V2.blend`, 24 fps, rest pose is an A-pose.

- Armature `Renamon_Rig`: 878 bones, 330 with `use_deform`. Auto-Rig Pro style body
  (`c_traj` root, `thigh_b_str.l`, `foot.l`, `spine_twist_*`, `neck.x`, `head.x`,
  `shoulder.l`, `hand.l`, finger bones, spline-IK tail `spline_twist_01..07.x`),
  plus a custom face rig: `DEF_Jaw` (also posed directly), a lip ribbon driven
  by helper mesh `HLP_Renamon_Ribbon_Lip` + empties `HLP_Lip_*` and bones
  `DEF_Lip_*`/`CTRL_Lip_*`, eyelid bones (`*Eyelid_Top1..9.L/R`,
  `*Eyelid_Bottom1..9.L/R`), brows, cheeks, ears (`*Ear*`), mouth-corner control
  `CTRL_Lip_Main_Corn.L/R`.
- `Renamon_Head` has shape keys `SK_Jaw` (driven by `DEF_Jaw` rot X:
  `max(0, rot_x)/0.3953`, so positive X opens) and 16 mouth-corner correctives
  `SK_MC_*` driven by `CTRL_Lip_Main_Corn.L/R` location X/Y (ranges approximately
  ±0.04–0.06).
- Character meshes to export: `Renamon_Body_Final`, `Renamon_Body_Hands`,
  `Renamon_Head`, `Renamon_Ear`, `Renamon_Fur`, `Renamon_Shoulder_Fur`,
  `Renamon_Sleeve`, `Renamon_Finger_Claws`, `Renamon_Finger_Ring`, and the eye set
  `Renamon_BlackEye`, `Renamon_Blue_Eye`, `Renamon_Blue_Eye_Line.L`,
  `Renamon_Eye_Shine`, `Renamon_Iris.L`, `Renamon_Eye_Line`. Several use Mirror,
  Subdivision (viewport 1 / render 2), CorrectiveSmooth, Shrinkwrap, Mask, and a
  geometry-nodes "Outline Modifier".
- Do NOT export: `Renamon_Body_Sculpt` (1M verts), `Renamon_Head_V2-*` (shape-key
  authoring copies), `Renamon_Sleeve_BU`, `Renamon_Head_Proxy`, `HLP_*` helpers
  (needed for evaluation only), `cs_*`/`WGT-*` widgets, lights, camera.
- Materials: `Renamon_Texture` (packed 4096² `Renamon_V7_BaseColor.png` plus a
  `_Highlight` variant, through a toon node group), emission-based procedural eye
  materials (`Renamon_BlueEye` uses a coordinate gradient), outline materials
  `Outline Material` / `Outline_Renamon_Yellow`.
- Reference renders of the source rig (EEVEE) are attached to your prompt: neutral,
  jaw open, slight smile while talking, full body. Your export should look like these
  in the app.

## Deliverables

1. `asset-pipeline/nukorio/build_nukorio.py`: one headless script, source `.blend`
   → final GLBs + manifest + renders, deterministic. Build into a staging dir and
   only promote to `assets/renamon-nukorio/` after verification passes. Refuse to
   run if the source file's SHA-256 differs from the one you calibrated against.
   Rebuild instructions go in NOTES.
2. `assets/renamon-nukorio/renamon-desktop.glb` and `renamon-phone.glb`. Budgets:
   desktop ≤ 80k triangles with 2048² textures; phone ≤ 30k triangles with 1024²
   textures. Pick the subdivision level per variant to hit them (likely 1 for
   desktop, 0 for phone). Same skeleton, morph names and materials in both.
3. `assets/renamon-nukorio/manifest.json`, `schema_version: 2`, generated by
   re-importing the exported GLBs in a fresh process. Hash the actual GLB bytes,
   don't copy the hash from in-memory state.
4. `assets/renamon-nukorio/renders/`: verification PNGs rendered from the re-imported
   desktop GLB (see Verification).
5. `asset-pipeline/nukorio/NOTES.md`: short. Decisions, anything you couldn't do or
   aren't confident about.

## Requirements

### Skeleton

- Export only what the app and Mixamo retargeting need: the deform bones for body,
  arms, hands, fingers, legs, feet, toes, tail, ears, neck and head, plus fur and
  sleeve helper deform bones if dropping them would visibly hurt the shape.
  Face deformation is captured as morph targets (below), so the face/lip/eyelid
  /brow/cheek deform bones should NOT be exported. Merge their vertex weights into
  their nearest exported ancestor (normally `head.x`) so the face follows the
  head rigidly at rest.
- Re-parent exported bones into a clean hierarchy, preserving rest transforms.
  ARP deform bones often parent through non-deform bones. Bone names stay as in
  the source.
- Must play correctly in three.js with standard skinning (≤ 4 influences per
  vertex, normalized).
- In the manifest, map the standard humanoid bones (hips, spine, chest, neck, head,
  shoulders, upper arms, forearms, hands, finger chains, thighs, shins, feet, toes)
  to exported bone names, so Mixamo retargeting is straightforward later. Also list
  the tail chain and ear chains in root→tip order.

### Face → morph targets (the main task)

Pose the source rig's face controls (with the body at rest), evaluate the mesh
with drivers, correctives, lip ribbon and modifiers (except the outline),
and store each pose's difference from the neutral evaluation as a morph target. Put
the morph targets on every exported mesh that the pose actually moves (the head,
and very likely eye lines, the black eye patch, eyelids and eye meshes). Use
identical morph names across meshes.

Required morphs (names exact):

- `jawOpen`: the jaw at a natural maximum for lively speech, including `SK_Jaw`'s
  correction. Find the angle where it still looks good; it doesn't have to be
  the rig's limit.
- Visemes for a snout, each self-contained, including whatever jaw opening it
  needs: `viseme_aa` (open "ah"), `viseme_E` (mid-open, corners slightly back),
  `viseme_I` (narrow opening, corners back), `viseme_O` (rounded, corners in),
  `viseme_U` (small rounded, corners in, nearly closed), `viseme_PP` (lips pressed
  closed). Use the lip and corner controls to shape them; the artist's control
  ranges show what the rig is designed for.
- Expressions: `mouthSmile`, `mouthFrown` (both corners), `eyeBlink_L`,
  `eyeBlink_R` (fully closed, clean lid line, no iris poking through),
  `eyeSquint` (both eyes, happy squint), `browUp`, `browDown` (if the brow rig
  supports them; skip with a note if not).
- Each morph's full weight is its intended maximum; the app will blend
  weights in 0..1 and may combine `jawOpen` with `mouthSmile`, or a viseme with a
  blink. Check those combinations don't break (no interpenetration, no
  holes, no inside-out eyes).
- Neutral (all weights 0) must match the source rig's neutral face exactly.
- glTF morph target names must come through in `mesh.extras.targetNames` (the
  Blender exporter does this from shape-key names); verify in three.js that
  `morphTargetDictionary` has them.

### Materials and look

- Body: base color from `Renamon_V7_BaseColor.png`, downscaled per variant.
  Figure out what `_Highlight` is and whether it matters; note it. Standard PBR
  with metallic 0 / roughness 1. The app does its own toon shading.
- Eyes: bake the procedural emission eye materials into small textures (or flat
  colors where that's faithful), so they look like the source renders under
  unlit/emissive display. Mark emissive in glTF where appropriate.
- Outline: do NOT bake outline geometry. The app will draw outlines with a
  shader that follows skinning and morphs. Instead, record in the manifest the
  outline color and relative thickness per material/mesh as the artist set them
  in the GN modifier and outline materials (e.g. dark brown for the body, yellow
  where `Outline_Renamon_Yellow` is used) and which meshes have no outline.
- Exported mesh names should be clean and meaningful (`body`, `head`, `ears`,
  `fur`, `eye_iris`, …), listed in the manifest with their materials.

### Export

- glTF 2.0 binary, +Y up, character facing +Z, meters, A-pose rest.
- No animation clips required this round; don't export the source's pose actions.
- Apply Mirror/Subdivision/CorrectiveSmooth/Shrinkwrap/Mask as needed so the
  exported mesh matches the evaluated neutral look. Morph targets must be
  computed on the same final topology as the exported base mesh.
- Check the exporter's warnings and resolve them or explain them in NOTES.

## Manifest (schema_version 2), at least

- `files`: per variant: name, bytes, sha256, triangle count, texture sizes.
- `coordinates`, `rest_pose: "A-pose"`.
- `meshes`: name, material, skinned, morph target names.
- `skeleton`: bone list with parents; `humanoid` map; `tail` and `ears` chains;
  `head_bone`; for each humanoid bone, the local axis that pitches the
  head/nods (so the app can layer procedural motion), if determinable.
- `morphs`: for each name: description, meshes it's on, recommended max weight
  if < 1, and `lipsync`: which morphs to use for (a) amplitude-only driving
  (likely `jawOpen`, optionally with a small `viseme_O`/`mouthSmile` blend) and (b)
  viseme-based driving (map Oculus-style phoneme classes sil/PP/FF/TH/DD/kk/CH/SS/nn/
  RR/aa/E/I/O/U to your morphs, with weights).
- `blink`: morph names, typical duration.
- `outline`: per mesh/material color (linear RGB + hex) and relative thickness;
  meshes without outline.
- `materials`: name, textures used, emissive or not.

## Verification you must do before finishing

- Re-import both GLBs in a fresh Blender process: bones, skin weights (normalized,
  ≤ 4 influences), morph names on the expected meshes, embedded textures,
  triangle budgets.
- three.js check (Node, GLTFLoader with an image shim like round 1): both GLBs
  load, `SkinnedMesh` count as expected, `morphTargetDictionary` contains all
  morph names on the head mesh, setting each morph to 1 changes vertex positions,
  and bones animate with an `AnimationMixer` test track on `head.x`.
- Renders from the re-imported desktop GLB (EEVEE or Workbench with textures),
  face close-ups front + three-quarter at a consistent camera: neutral, every morph
  at 1.0, and combinations `jawOpen 0.6 + mouthSmile 0.5`, `viseme_O + eyeBlink_L`,
  `jawOpen 1 + eyeBlink_L + eyeBlink_R`; full body front + three-quarter. Name files
  by content (`face_3q_viseme_O.png`, …).
- Look at the renders and compare them with the attached source references. Fix
  holes, inside-out faces, eyes poking through lids, texture swim, mismatched
  neutral, missing eyes or materials. Iterate until right.
- If something can't be done well, say so in NOTES and your final message rather
  than shipping it broken.

## Final message

Summarize what was built, how to rebuild, triangle counts and sizes per variant,
the morph list (with anything skipped and why), the outline parameters, the
humanoid map coverage, and open issues with confidence levels.
