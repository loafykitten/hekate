import { afterEach, describe, expect, it, vi } from "vitest";
import { loadCharacterSource, rigFrom, type Manifest } from "./manifest";

// A small hand-written manifest in the pipeline's schema 2. Not the real character.
const fixture: Manifest = {
  schema_version: 2,
  files: { desktop: { name: "model-desktop.glb" }, phone: { name: "model-phone.glb" } },
  skeleton: {
    head_bone: "head.x",
    tail: ["tail_1.x", "tail_2.x"],
    ears: { left: ["ear_l.x", "ear_l_tip.x"], right: ["ear_r.x"] },
    humanoid: { spine: "spine.x", chest: "chest.x" },
    threejs_runtime_names: { "head.x": "headx", "tail_1.x": "tail_1x", "tail_2.x": "tail_2x", "ear_l.x": "ear_lx", "ear_r.x": "ear_rx", "chest.x": "chestx" },
  },
  lipsync: { amplitude_only: { primary: "jawOpen", optional_blend: { mouthSmile: 0.15 } } },
  blink: { left: "blinkL", right: "blinkR" },
  outline: {
    meshes: {
      body: { entries: [{ enabled: false, color_linear: [1, 1, 1], relative_thickness: 0.5 }, { enabled: true, color_linear: [0.1, 0.2, 0.3], relative_thickness: 0.002 }] },
      eyes: { entries: [{ enabled: false, color_linear: [0, 0, 0], relative_thickness: 0.1 }] },
    },
  },
  animations: [
    { name: "idle_busy", loop: true, category: "idle", head_motion: "strong" },
    { name: "idle_calm", loop: true, category: "idle", head_motion: "none" },
    { name: "idle_once", loop: false, category: "idle" },
    { name: "chat", loop: true, category: "talk" },
    { name: "wave", loop: false, category: "gesture", head_motion: "moderate" },
    { name: "shake", loop: false, category: "gesture", head_motion: "strong" },
    { name: "stand", loop: true, category: "pose" },
  ],
};

describe("rigFrom", () => {
  const rig = rigFrom(fixture);

  it("maps bones through three.js runtime names", () => {
    expect(rig.headBone).toBe("headx");
    expect(rig.procedural.tail).toEqual(["tail_1x", "tail_2x"]);
    expect(rig.procedural.ears).toEqual(["ear_lx", "ear_rx"]); // first bone of each chain
    expect(rig.procedural.spine).toBe("chestx"); // chest preferred over spine
  });

  it("builds the mouth from the amplitude lipsync entry", () => {
    expect(rig.mouth).toEqual([
      { name: "jawOpen", weight: 1 },
      { name: "mouthSmile", weight: 0.15 },
    ]);
  });

  it("falls back to jawOpen and default blink morphs when the manifest has none", () => {
    const bare = rigFrom({ ...fixture, lipsync: undefined, blink: undefined });
    expect(bare.mouth).toEqual([{ name: "jawOpen", weight: 1 }]);
    expect(bare.blink).toEqual(["eyeBlink_L", "eyeBlink_R"]);
  });

  it("uses the first enabled outline entry, scaled, and null when none is enabled", () => {
    expect(rig.outline.byMesh.body?.color).toEqual([0.1, 0.2, 0.3]);
    expect(rig.outline.byMesh.body?.thickness).toBeCloseTo(0.002 * 3.5);
    expect(rig.outline.byMesh.eyes).toBeNull();
  });

  it("picks the calmest looping idle, the looping talk clip and the gestures", () => {
    expect(rig.clips.idle).toBe("idle_calm");
    expect(rig.clips.talk).toBe("chat");
    expect(rig.clips.gestures).toEqual(["wave", "shake"]);
    expect(rig.clips.headDriven).toEqual(["idle_busy", "shake"]);
  });

  it("takes any looping idle when all of them move the head", () => {
    const busy = rigFrom({ ...fixture, animations: [{ name: "only", loop: true, category: "idle", head_motion: "strong" }] });
    expect(busy.clips.idle).toBe("only");
  });
});

describe("loadCharacterSource", () => {
  afterEach(() => vi.unstubAllGlobals());

  function stub(manifest: unknown, { ok = true, status = 200, small = false } = {}) {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok, status, json: async () => manifest })));
    vi.stubGlobal("matchMedia", () => ({ matches: small }));
  }

  it("chooses the variant by screen size", async () => {
    stub(fixture, { small: true });
    expect((await loadCharacterSource(new URLSearchParams())).modelUrl).toBe("/character/model-phone.glb");
    stub(fixture, { small: false });
    expect((await loadCharacterSource(new URLSearchParams())).modelUrl).toBe("/character/model-desktop.glb");
  });

  it("honors ?variant= and falls back to the first file for unknown names", async () => {
    stub(fixture);
    expect((await loadCharacterSource(new URLSearchParams("variant=phone"))).label).toBe("phone");
    expect((await loadCharacterSource(new URLSearchParams("variant=nope"))).label).toBe("desktop");
  });

  it("rejects a missing manifest and an unsupported schema", async () => {
    stub({}, { ok: false, status: 404 });
    await expect(loadCharacterSource(new URLSearchParams())).rejects.toThrow(/404/);
    stub({ ...fixture, schema_version: 3 });
    await expect(loadCharacterSource(new URLSearchParams())).rejects.toThrow(/schema/);
  });
});
