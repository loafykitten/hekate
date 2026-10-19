Completed. Both delivered GLBs contain all 11 clips, with the original Round 2 asset data preserved exactly.

| Clip | Duration | Loop | Category |
|---|---:|:---:|---|
| idle | 4.000 s | Yes | idle |
| idle_2 | 8.333 s | Yes | idle |
| talking | 5.167 s | Yes | talk |
| waving | 4.733 s | No | gesture |
| thankful | 3.000 s | No | gesture |
| acknowledging | 1.933 s | No | gesture |
| dismissing | 2.233 s | No | gesture |
| excited | 6.567 s | No | gesture |
| no | 5.000 s | No | gesture |
| smh | 7.300 s | No | gesture |
| standing_pose | 0.033 s | Yes | pose |

Growth: **desktop +937,664 bytes; phone +936,716 bytes**, comfortably below budget.

Digitigrade legs use foot-contact correction; three-digit hands use Thumb/Index/Pinky mapping; arm corrections provide chest and face clearance. Source footwork is adapted to a planted stance. No clips were dropped; `smh` retains minor sleeve overlap. `idle_2` is the calmer idle.

Fresh Blender imports, full-duration three.js playback, track binding, loop endpoints, identical variant samples, and ten pipeline tests passed. Sole movement stays below 1.5 mm. Stale verification rejection and promotion recovery are fixed. A bounded UV calibration also fixes existing phone-export nondeterminism while retaining the exact original checksum.

Confidence is **high for preservation and playback checks**, **medium for motion fidelity and collision clearance between inspected frames**. Live app/WebGL playback remains untested.

[Rebuild instructions and detailed notes](asset-pipeline/nukorio/NOTES.md) · [Contact sheets and renders](assets/renamon-nukorio/renders)