# Nukorio export

The delivery is `assets/renamon-nukorio/`: desktop and phone GLBs, schema-v2 manifest, and verification PNGs. All geometry comes from the purchased rig; no replacement mouth or interior was created. The source file is unchanged.

Rebuild from the workspace root with Blender 5.2.1 and the installed Node/three.js:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/build_nukorio.py
```

The script checks the calibrated source SHA-256, evaluates 562 simple drivers without auto-run/add-ons, builds in `tmp/staging`, verifies in fresh Blender processes and three.js, checks the visually reviewed GLB hashes in `visual-baseline.json`, and recoverably promotes the result. The previous delivery stays in `tmp/previous-delivery`. Logs and intermediate data stay in `tmp`. A changed source, toolchain, or intentional asset revision requires recalibration and visual review before updating the baseline; failures leave the current delivery intact.

| Variant | Triangles | Body texture | Face subdivision |
| --- | ---: | --- | ---: |
| Desktop | 77,472 | 2048² | 2 |
| Phone | 29,223 | 1024² | 1 |

Both have 15 skinned meshes, seven materials, 110 bones, and the same 14 morph names: `jawOpen`, `viseme_aa`, `viseme_E`, `viseme_I`, `viseme_O`, `viseme_U`, `viseme_PP`, `mouthSmile`, `mouthFrown`, `eyeBlink_L`, `eyeBlink_R`, `eyeSquint`, `browUp`, `browDown`. None were skipped. Morphs are on `head`, `eye_black`, and `eye_line`; each contains the shared dictionary. Brow motion affects only the artist's separate eye-line mesh. Stationary eye spheres need no morphs. The squint raises the lower lids; combine it with a smile for a happy expression.

Desktop preserves the evaluated head topology at source render subdivision 2. Phone uses subdivision 1. Body/fur/ear/claw reductions use the evaluated surface; reduced vertices receive barycentrically sampled deltas. Four normalized skin weights are retained. Face bones are merged into `head.x`; body/helper rest transforms are preserved and checked against the actual exported joint matrices.

The 40-entry humanoid map covers torso, head, both shoulders/arms, legs/feet/toes, and three complete finger chains per hand. This character has thumb/index/pinky digits, so middle/ring chains are absent. The extra digitigrade hock joint needs deliberate rotation distribution when retargeting. Tail and ear chains are ordered in the manifest. three.js removes punctuation from runtime names (`head.x` → `headx`): use `bone.userData.name` or the manifest's runtime-name map. Pitch axes are derived from exported glTF transforms, not Blender's reconstructed bone-display orientation.

Body material is metallic 0 / roughness 1. `_Highlight` supplies the lit branch of the original two-tone shader; it is intentionally not merged into the requested base-color texture. Eyes use `KHR_materials_unlit`, including an 8×128 bake of the source generated-Z blue gradient. PBR verification lighting differs from the source toon renderer; the app still supplies toon shading and outlines.

No outline geometry is exported. Despite its name, `Outline_Renamon_Yellow` is brown `#88381d`, linear RGB `[0.24513844, 0.03960315, 0.01209240]`. It applies at 1.760 mm to body/hands, 1.224 mm to head/ears, and 5.500 mm to shoulder fur. Black outlines apply to fur (1.320 mm), sleeves (2.180 mm), claws (1.198 mm), and finger rings (0.760 mm). All six eye meshes have no GN outline. Exact thickness/height ratios and source noise settings are in the manifest.

The source emits a 48→24 vertex-count mismatch for the lip ribbon's bound CorrectiveSmooth after its Mask. Blender already skips that invalid modifier; the build explicitly disables this no-op. The ribbon, facial correctives, other modifiers, and drivers remain active during capture. Exporter warnings were resolved.

Verification includes both fresh imports, exact topology ordering across poses, embedded PNG dimensions, raw GLB weights, source rest transforms, sub-micrometer position round trips, all morphs through GLTFLoader/CPU evaluation, and an AnimationMixer head-nod track. The 42 desktop and 10 phone renders were inspected, including full-weight blinks, requested combinations, maximum jaw+smile, and smile+squint. High confidence in asset structure and the inspected poses; medium confidence in subjective viseme readability. The extra Oculus phoneme mappings are approximations for this snout. Live app/WebGL appearance, TTS timing, and arbitrary simultaneous morph extremes were not exercised. Round 3 animation verification is recorded below.


## Round 3: Mixamo animation

The same rebuild command above now runs `animate_nukorio.py`, fresh-process `verify_animations.py`, contact-sheet assembly, and the real installed three.js loader/mixer before promotion. No add-ons or network are used. `mixamo-inputs.json` pins all eleven FBX SHA-256 values; every animation stage checks these and the original `.blend` checksum before work. `Idle.fbx` supplies the skin-bearing bind/rest reference for every clip.

| Clip | Seconds | Loop | Category |
| --- | ---: | --- | --- |
| idle | 4.000 | yes | idle |
| idle_2 | 8.333 | yes | idle |
| talking | 5.167 | yes | talk |
| waving | 4.733 | no | gesture |
| thankful | 3.000 | no | gesture |
| acknowledging | 1.933 | no | gesture |
| dismissing | 2.233 | no | gesture |
| excited | 6.567 | no | gesture |
| no | 5.000 | no | gesture |
| smh | 7.300 | no | gesture |
| standing_pose | 0.033 | yes | pose |

No clips dropped. `standing_pose` intentionally holds a single pose for the source's two-frame interval. `idle` has pronounced looking-around motion; `idle_2` is the quieter companion idle. `no` includes a broad forward lean and hand gesture; `smh` brings crossed hands in front of the face. Those behaviors are present in the source. Strong head-driving clips are marked with `head_motion: "strong"` in the manifest, including `idle` and `talking`; reduce the app's additive head layer there.

The desktop file is 11,387,932 bytes (10.860 MiB), +937,664 bytes (0.894 MiB). Phone is 4,983,936 bytes (4.753 MiB), +936,716 bytes (0.893 MiB). Original Round 2 GLB hashes remain in `visual-baseline.json`; the build copies and checks the original baseline under `tmp/round2`. Animation data is appended directly to each GLB: all original JSON nodes, hierarchy, meshes, skins, material definitions, morph accessors, and the entire original binary payload compare exactly, rather than passing through another mesh export. Both variants receive identical motion samples.

Retargeting uses own-rest world-space rotation deltas and explicit A-to-T calibration for arms/hands. Mixamo Thumb/Index/Pinky map to Renamon's corresponding digits; unused human fingers are discarded. Hip translation is scaled by the measured leg-length ratio and stripped of horizontal travel. A per-clip vertical stance offset provides extension reserve while preserving the source vertical bob. Source leg rotations seed a rest-biased three-link FABRIK solve, distributing flexion over thigh, knee and hock; a second solve after transition blending holds feet/toes on their rest-plane contacts. The original stepping and crossed-leg poses are deliberately adapted to a stationary, planted stance, including `standing_pose`. This is the main fidelity compromise.

Wrist reach is corrected around the torso/chest fur and a clearance volume following the moving head, with an analytic two-link arm solve preserving the source elbow plane. Hands stay in front of the fur and face in inspected frames. Close hand/sleeve overlap remains in `smh`; no visible arms-through-torso or reversed knees in the reviewed contact sheets. Confidence is medium for collision clearance between inspected frames and for the naturalness of the adapted lower-body motion, especially the energetic `excited`/`no` clips. This is not a continuous mesh-collision proof.

Loops ease their final 0.5 seconds onto the opening pose; one-shots use smooth 0.4-second bookends that exactly match `idle` frame zero. Foot contact is solved again after those blends. Key reduction permits at most 0.05 degrees local rotation error / 0.05 mm hip-position error. There are 43 tracks per clip: 42 bone rotations and one hip translation. No tail, ear, face, morph-weight or mesh tracks are authored. Tail and ears still inherit motion from their parents; that is expected, and leaves their local procedural layers free.

Verification: both GLBs reimported in separate Blender processes, immutable data compared against the actual Round 2 bytes/hashes, all clips/durations checked, and every clip played for its full duration at 60 Hz with the installed GLTFLoader/AnimationMixer. Every track resolves to a bone after name sanitization, with zero binding warnings. Nonstatic clips move bones, loops match at both endpoints, one-shot endpoints match idle, and horizontal hip channels are constant. CPU-skinned sole samples move less than 1.5 mm from rest across both variants; maximum downward sole deviation is below 0.5 mm. This includes interpolation and key reduction. Full app/WebGL playback and arbitrary combinations with procedural layers remain untested.

`renders/animation_<clip>_contact.png` contains five evenly spaced times, left to right, with front on top and three-quarter below. The ten individual PNGs per clip are included too. All eleven sheets were inspected and compared with source motion rendered on the skin-bearing Mixamo character. `render_mixamo.py` reproduces those source comparisons under `tmp/source-motion`. The animation hashes are visually calibrated in `animation-visual-baseline.json`; a drift stops promotion for review.

Robustness: `--manifest` checks cached verification against the current staged GLB SHA-256, byte count and current input hashes, and checks the fresh animation verification records too. Stale records, changed FBXs and mixed stages are rejected. Promotion copies and validates the candidate first, journals the rename phase, restores the old delivery on failure, and recovers a missing delivery from `tmp/previous-delivery` on the next build. Ten failure-path tests cover changed GLB/input hashes, Mixamo mismatch, interrupted rename recovery, second-rename failure, post-promotion validation failure, successful promotion, exact small-UV-drift recovery, rejection of excessive UV drift, and rejection of non-UV drift. Run them with the same Blender flags and `--python asset-pipeline/nukorio/test_pipeline.py` after a completed build. The prior delivery is retained in `tmp/previous-delivery`.

FBX import logs warn about unsupported `Short` user properties. Those are custom metadata; the actual 65-bone transforms and keyed animation are read. No external scripts or bundled rig add-ons are enabled. Confidence is high in asset preservation, binding, loop endpoints, measured foot contact and recovery tests; medium in aesthetic fidelity and continuous collision clearance.


A clean Round 2 rebuild exposed existing Blender Decimate nondeterminism: six phone-fur UVs changed by at most 0.00001252 (16 binary bytes), while all other JSON/binary data matched. `round2-uv-calibration.json` preserves that reviewed UV accessor. The exporter restores it only when deviations are <=0.00002 and then requires the *entire* original phone GLB SHA-256, rejecting any other change. This makes fresh source builds reproduce the exact Round 2 delivery without relaxing the asset-preservation gate. The desktop regenerated exactly without correction.


## Round 4: gesture pack

Both variants now contain 24 clips. The thirteen additions below are all one-shots (`loop: false`), including `weight_shift`. Every animation has a boolean `idle_variation`; only the three marked true below opt into occasional idle playback. All eleven round-3 metadata entries are preserved except for adding `idle_variation: false`.

| Clip | Seconds | Category | idle_variation | head_motion |
| --- | ---: | --- | --- | --- |
| nod_yes | 2.400 | gesture | false | strong |
| hard_nod | 1.633 | gesture | false | strong |
| long_nod | 1.700 | gesture | false | strong |
| sarcastic_nod | 2.167 | gesture | false | strong |
| head_shake_no | 1.700 | gesture | false | strong |
| annoyed_head_shake | 2.433 | gesture | false | strong |
| thoughtful_head_shake | 3.067 | gesture | false | strong |
| look_away | 1.800 | gesture | true | strong |
| angry | 2.167 | gesture | false | strong |
| cocky | 2.867 | gesture | false | strong |
| happy_hands | 2.733 | gesture | false | strong |
| relieved_sigh | 3.000 | gesture | true | strong |
| weight_shift | 9.433 | idle | true | moderate |

Desktop is 12,183,136 bytes, an increase of 795,204 bytes (0.758 MiB) over round 3. Phone is 5,778,020 bytes, an increase of 794,084 bytes (0.757 MiB). Both are below the 2 MB round-4 growth target and retain identical animation samples.

`mixamo-inputs.json` pins the gesture ZIP plus all thirteen used animation members and `X Bot.fbx`. The archive hash is `5627c03289a24e707362a538b8e0715f93ea6131324de2f1d5bcdd2b35f07f97`. Member hashes are checked independently, duplicate/missing members are rejected, and extraction writes only explicitly named files under `tmp/gesture-pack`. Cached extracted files are regenerated from verified archive bytes before import. The duplicate acknowledging/dismissing members are unused. `Idle.fbx` remains the bind reference.

Sparse source curves are sampled at every frame at 30 fps. The round-3 own-rest rotation mapping, A-to-T arm calibration, leg-length hip scaling, thumb/index/pinky mapping, torso/head wrist clearance and two-pass digitigrade FABRIK are unchanged. The existing clearance corrections suffice for the inspected new clips; no new per-clip arm offsets were needed. Quiet head gestures retain quiet arms. All thirteen use the existing 0.4-second transition bookends to `idle` frame zero, including the 9.433-second `weight_shift`; head/neck remain available to the motion while tail, ear and face tracks remain absent. Thinning retains the round-3 0.05-degree / 0.05-mm tolerance.

No clips were dropped. `weight_shift` retains slow torso rotation and vertical motion, but horizontal root stripping and planted feet make its lateral weight transfer subtler than X Bot's. `angry` is a restrained shoulder/arm gesture in the source, not a large dramatic action. These are fidelity/readability limitations rather than corrupted motion. All new head gestures are marked strong; `weight_shift` is moderate. `happy_hands` also gets strong because its head excursion is about 37.5 degrees.

The build first reproduces the exact round-2 GLBs, then the exact eleven-clip round-3 GLBs before appending the new clips. Round-3 SHA values were checked against the delivery present before this work and remain pinned in `animation-visual-baseline.json`; verified reference copies stay in `tmp/round3`. Fresh-process verification compares all original JSON structure, the entire original binary payload, and every old animation sampler exactly. Both variants freshly reimport with 110 bones, 15 meshes, the unchanged morphs/rest transforms, and 24 clips. A track allowlist derived from the preserved idle rejects any additional bone/face channels.

The installed three.js loader/mixer plays all 24 clips through their full durations at 60 Hz with no binding warnings. New clip endpoints match the `idle` stance, horizontal root translation is constant, loops retain their endpoint checks, and nonstatic clips move. Across the thirteen additions and both variants, maximum sampled CPU-skinned sole displacement is below 1.313 mm and downward displacement from each sample's rest position is below 0.590 mm. The authored rest soles are below world zero, so absolute world Y is not used as the penetration metric. Head excursion and maximum per-step head rotation are recorded in the manifest verification data.

All thirteen target contact sheets were inspected against X Bot source sheets: five evenly spaced times, front above three-quarter, full body. `angry`, `cocky` and `happy_hands` show clear hands/wrists without visible torso or face penetration; no reversed knees or broken fingers were seen. Source comparisons are reproducible under `tmp/source-gestures`; delivered target sheets are `renders/animation_<clip>_contact.png`. `gesture-visual-baseline.json` pins the reviewed GLBs while retaining the old round-3 calibration separately.

High confidence in preservation, binding, measured contacts and endpoints; medium confidence in subjective gesture readability and clearance between inspected poses. App/WebGL playback with procedural layers and arbitrary gesture interruption remains untested.


Round-4 final checks: all 20 pipeline tests pass, including the original recovery/rollback and UV-calibration cases plus ZIP/member/X Bot hash mismatch, missing member pins, stale pack verification, corrupt extraction-cache recovery, old animation structure/sample mutation, and PNG determinism/corruption checks. Staging and hash-bound fresh verification remain in place; promotion still validates before and after moving the delivery and rolls back and recovers as in round 3.

Two independent complete source builds produced byte-identical GLBs, manifest, contact sheets and encoded render pixels. Blender's individual rendered PNGs included volatile `Date` and `RenderTime` text chunks. `deterministic_png.py` now validates PNG chunk checksums and removes only those two fields at the end of the animation pipeline, preserving every other chunk exactly. All 319 canonical delivered files from the two source builds compare byte-for-byte; the final fresh verification/promotion matches that same hash set. Evidence is in `tmp/round4-reproducibility.json`, `tmp/build-round4.log`, `tmp/rebuild-round4.log`, `tmp/finalize-round4.log`, and `tmp/test-round4.log`.

Rebuild from the repository root (includes both fresh imports, contact renders, exact preservation checks, and the installed three.js loader/mixer):

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/build_nukorio.py
```

Pipeline tests:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/test_pipeline.py
```

Regenerate the X Bot comparison sheets and frames:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/render_mixamo.py -- --gestures
```

All agent-authored writes stayed under the two authorized folders; no network or state-changing Git commands were used. An early snapshot of 18,825 outside-scope files matched exactly. The final snapshot observed concurrent app microphone edits, app build/test outputs, and Git-state changes; those were left untouched. All other outside-scope paths remained unchanged. The input hashes still match. `tmp/scope-verification.json` records that distinction rather than claiming the entire shared workspace stayed unchanged.
