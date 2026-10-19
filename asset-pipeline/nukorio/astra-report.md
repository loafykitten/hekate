Built and verified in [assets/renamon-nukorio](assets/renamon-nukorio).

| Variant | Triangles | GLB size | Body texture |
|---|---:|---:|---:|
| [Desktop](assets/renamon-nukorio/renamon-desktop.glb) | 77,472 | 10.45 MB | 2048² |
| [Phone](assets/renamon-nukorio/renamon-phone.glb) | 29,223 | 4.05 MB | 1024² |

Both include 15 skinned meshes, 110 bones, and all 14 morphs: `jawOpen`, `viseme_aa`, `viseme_E`, `viseme_I`, `viseme_O`, `viseme_U`, `viseme_PP`, `mouthSmile`, `mouthFrown`, `eyeBlink_L`, `eyeBlink_R`, `eyeSquint`, `browUp`, `browDown`. None skipped. Brow motion affects the separate eye-line mesh.

The humanoid map covers 40 entries, including all three finger chains per hand. Tail and ear chains are mapped. The extra digitigrade hock needs deliberate handling during retargeting.

Outline metadata preserves brown `#88381d` at 1.224–5.500 mm and black at 0.760–2.180 mm, with exact per-mesh ratios in the [manifest](assets/renamon-nukorio/manifest.json). No outline geometry is exported.

Verification passed: fresh Blender imports, four normalized influences, source rest transforms, embedded textures, three.js morphs and AnimationMixer, and 52 inspected renders. A clean rebuild produced **byte-identical GLBs**. The source remains unchanged.

Rebuild from the workspace root:

```sh
TMPDIR="$PWD/asset-pipeline/nukorio/tmp" \
/Applications/Blender.app/Contents/MacOS/Blender \
  -b --factory-startup --disable-autoexec --python-exit-code 1 \
  --python asset-pipeline/nukorio/build_nukorio.py
```

High confidence in structure and inspected poses; medium confidence in subjective viseme readability. Live app shading, TTS timing, and Mixamo clips remain untested. Decisions, source warning handling, and integration details are in [NOTES.md](asset-pipeline/nukorio/NOTES.md).