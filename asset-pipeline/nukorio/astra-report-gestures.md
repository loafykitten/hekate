Round 4 is complete: both GLBs contain **24 clips**, preserving the eleven round-3 clips, skeleton, meshes and morphs exactly. All thirteen additions are one-shots (`loop: false`).

| Clip | Seconds | Category | idle_variation | head_motion |
| --- | ---: | --- | --- | --- |
| `nod_yes` | 2.400 | gesture | false | strong |
| `hard_nod` | 1.633 | gesture | false | strong |
| `long_nod` | 1.700 | gesture | false | strong |
| `sarcastic_nod` | 2.167 | gesture | false | strong |
| `head_shake_no` | 1.700 | gesture | false | strong |
| `annoyed_head_shake` | 2.433 | gesture | false | strong |
| `thoughtful_head_shake` | 3.067 | gesture | false | strong |
| `look_away` | 1.800 | gesture | true | strong |
| `angry` | 2.167 | gesture | false | strong |
| `cocky` | 2.867 | gesture | false | strong |
| `happy_hands` | 2.733 | gesture | false | strong |
| `relieved_sigh` | 3.000 | gesture | true | strong |
| `weight_shift` | 9.433 | idle | true | moderate |

**Growth over round 3:** desktop +795,204 bytes (0.758 MiB); phone +794,084 bytes (0.757 MiB). Both are below the 2 MB target.

Sparse source curves are sampled at 30 fps through the existing rest-relative retargeter. Digitigrade FABRIK keeps feet planted; the existing wrist-clearance corrections handle the torso, fur and head. All new clips return to the idle stance. Tail, ear and face tracks remain absent.

**None dropped.** `weight_shift` is subtler than the source because lateral root movement is stripped; `angry` is restrained in the source too. All thirteen contact sheets were inspected against X Bot. Confidence is high in preservation, binding, endpoints and measured foot contacts; medium in aesthetic fidelity and collision clearance between inspected poses. App/WebGL playback with procedural layers remains untested.

Verification passed: fresh Blender imports, full-duration 60 Hz three.js playback with zero binding warnings, and **20 pipeline tests**. New sole displacement stays below 1.313 mm, with downward movement below 0.590 mm. ZIP/member hash checks, stale-record rejection and recoverable promotion are enforced. Two complete source builds match across all **319 delivered files** after removing only Blender's PNG date/render-time metadata; the final saved delivery matches those hashes.

Rebuild from the repository root; this includes imports, renders and three.js verification:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/build_nukorio.py
```

Run the failure-path, preservation and PNG tests:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" PYTHONDONTWRITEBYTECODE=1 \
  /Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/test_pipeline.py
```

My writes stayed within the two authorized directories. No network or Git state-changing commands were used; concurrent app/Git changes were left untouched.
