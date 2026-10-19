import * as THREE from "three";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Character, type CharacterRig } from "./character";

// A one-bone character with an idle loop, a talk loop, a gesture and two fidgets.
function tiny() {
  const root = new THREE.Group();
  const head = new THREE.Bone();
  head.name = "head";
  root.add(head);
  const clip = (name: string, seconds: number) =>
    new THREE.AnimationClip(name, seconds, [
      new THREE.QuaternionKeyframeTrack("head.quaternion", [0, seconds], [0, 0, 0, 1, 0, 0, 0, 1]),
    ]);
  const rig: CharacterRig = {
    headBone: "head",
    mouth: [],
    blink: [],
    outline: { byMesh: {}, fallback: null },
    clips: { idle: "idle", talk: "talk", gestures: ["wave"], fidgets: ["glance", "shift"], headDriven: [] },
    procedural: { tail: [], ears: [], chinLift: 0 },
  };
  const c = Character.fromScene(root, [clip("idle", 4), clip("talk", 4), clip("wave", 2), clip("glance", 2), clip("shift", 9)], rig);
  c.blinkEnabled = false;
  return c;
}

const run = (c: Character, seconds: number) => {
  for (let t = 0; t < seconds; t += 0.1) c.update(0.1);
};

describe("Character idle variations", () => {
  afterEach(() => vi.restoreAllMocks());

  it("plays one after a quiet spell, and never the same one twice running", () => {
    const c = tiny(); // before stubbing: three.js makes clip ids from Math.random
    vi.spyOn(Math, "random").mockReturnValue(0); // shortest gap, first choice
    c.fidgets = true;
    run(c, 24);
    expect(c.playing).toBeUndefined();
    run(c, 2);
    expect(c.playing).toBe("glance");
    run(c, 3); // it finishes
    expect(c.playing).toBeUndefined();
    run(c, 26);
    expect(c.playing).toBe("shift");
  });

  it("waits while she's busy, and a fidget gives way the moment something happens", () => {
    const c = tiny();
    vi.spyOn(Math, "random").mockReturnValue(0);
    run(c, 30); // the conversation isn't idle
    expect(c.playing).toBeUndefined();
    c.fidgets = true;
    run(c, 26);
    expect(c.playing).toBe("glance");
    c.speaking = true;
    c.update(0.1);
    expect(c.playing).toBeUndefined();
  });

  it("doesn't let a fidget be played as a gesture, or a gesture as a fidget", () => {
    const c = tiny();
    expect(c.playGesture("glance")).toBe(false);
    expect(c.playGesture("wave")).toBe(true);
  });
});
