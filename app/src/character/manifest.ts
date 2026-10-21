import type { CharacterRig, OutlineStyle } from "./character";

// Subset of the asset pipeline's manifest.json (schema_version 2) the app uses: morph-target
// face, shader outlines, desktop/phone variants, animation clips.
export interface Manifest {
  schema_version: 2;
  files: Record<string, { name: string }>;
  skeleton: {
    head_bone: string;
    tail?: string[];
    ears?: Record<string, string[]>;
    humanoid?: Record<string, string>;
    // GLTFLoader strips punctuation from node names (head.x -> headx).
    threejs_runtime_names?: Record<string, string>;
  };
  lipsync?: {
    amplitude_only?: { primary: string; optional_blend?: Record<string, number> };
    // Viseme class -> morph weights ("sil" is usually empty).
    viseme_based?: Record<string, Record<string, number>>;
  };
  blink?: { left: string; right: string };
  outline?: {
    meshes?: Record<string, { entries: { enabled: boolean; color_linear: [number, number, number]; relative_thickness: number }[] }>;
  };
  animations?: {
    name: string;
    loop: boolean;
    category?: "idle" | "talk" | "gesture" | "pose";
    head_motion?: "strong" | "moderate" | "none";
    // A quiet one-shot that suits idle moments (shifting weight, a glance aside).
    idle_variation?: boolean;
  }[];
}

// The asset's outline thickness is relative to body height; OutlineEffect works in roughly
// screen-relative units, so scale it to taste.
const OUTLINE_SCALE = 3.5;

export interface CharacterSource {
  rig: CharacterRig;
  modelUrl: string;
  label: string;
}

function clipsOf(animations: NonNullable<Manifest["animations"]>): CharacterRig["clips"] {
  const byCategory = (c: string) => animations.filter((a) => a.category === c);
  const strong = (a: (typeof animations)[number]) => a.head_motion === "strong";
  // An assistant mostly sits in its idle, so prefer the calmest looping one.
  const idles = byCategory("idle").filter((a) => a.loop);
  const idle = idles.find((a) => !strong(a)) ?? idles[0];
  return {
    idle: idle?.name,
    // A talking clip is one gesture among many, played when she tags it, not a loop under all
    // speech: one loop for every reply looked the same every time.
    gestures: [...byCategory("gesture"), ...byCategory("talk")].map((a) => a.name),
    fidgets: animations.filter((a) => a.idle_variation && !a.loop).map((a) => a.name),
    headDriven: animations.filter(strong).map((a) => a.name),
  };
}

export function rigFrom(m: Manifest): CharacterRig {
  const runtime = m.skeleton.threejs_runtime_names ?? {};
  const bone = (name: string) => runtime[name] ?? name;

  const byMesh: Record<string, OutlineStyle | null> = {};
  for (const [mesh, { entries }] of Object.entries(m.outline?.meshes ?? {})) {
    const e = entries.find((x) => x.enabled);
    byMesh[mesh] = e ? { color: e.color_linear, thickness: e.relative_thickness * OUTLINE_SCALE } : null;
  }

  const amp = m.lipsync?.amplitude_only;
  const open = amp
    ? [{ name: amp.primary, weight: 1 }, ...Object.entries(amp.optional_blend ?? {}).map(([name, weight]) => ({ name, weight }))]
    : [{ name: "jawOpen", weight: 1 }];
  const humanoid = m.skeleton.humanoid ?? {};
  const spine = humanoid.chest ?? humanoid.spine;

  return {
    headBone: bone(m.skeleton.head_bone),
    mouth: open,
    visemes: m.lipsync?.viseme_based,
    blink: m.blink ? [m.blink.left, m.blink.right] : ["eyeBlink_L", "eyeBlink_R"],
    outline: { byMesh, fallback: null },
    clips: clipsOf(m.animations ?? []),
    procedural: {
      spine: spine && bone(spine),
      tail: (m.skeleton.tail ?? []).map(bone),
      ears: Object.values(m.skeleton.ears ?? {}).map((chain) => bone(chain[0])),
      chinLift: 0.04, // rest pose already holds the head level; just a hint of engagement
    },
  };
}

// Loads the manifest and picks the phone or desktop variant (or ?variant=).
export async function loadCharacterSource(params: URLSearchParams): Promise<CharacterSource> {
  const res = await fetch("/character/manifest.json");
  if (!res.ok) throw new Error(`no character asset (manifest: ${res.status})`);
  const m = (await res.json()) as Manifest;
  if (m.schema_version !== 2) throw new Error(`unsupported manifest schema ${m.schema_version}`);
  const small = matchMedia("(max-width: 800px), (pointer: coarse)").matches;
  const wanted = params.get("variant") ?? (small ? "phone" : "desktop");
  const variant = wanted in m.files ? wanted : Object.keys(m.files)[0];
  return { rig: rigFrom(m), modelUrl: `/character/${m.files[variant].name}`, label: variant };
}
