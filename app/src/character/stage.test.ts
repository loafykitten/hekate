import { describe, expect, it } from "vitest";
import { computeFraming, DEFAULT_BODY_HEIGHT, mouthTarget, parseStageParams, smoothMouth } from "./stage";

describe("computeFraming", () => {
  const base = { fovDegrees: 30, bodyHeight: DEFAULT_BODY_HEIGHT };

  it("aims at 62% of body height and fits its height on wide screens", () => {
    const f = computeFraming({ ...base, aspect: 1.6 });
    expect(f.target).toEqual([0, 1.7 * 0.62, 0]);
    const fitH = (1.7 * 0.72) / Math.tan((15 * Math.PI) / 180);
    expect(f.position[2]).toBeCloseTo(fitH);
    expect(f.position[1]).toBeCloseTo(1.7 * 0.62 + 0.05);
  });

  it("backs off on narrow screens so the width fits too", () => {
    const wide = computeFraming({ ...base, aspect: 1.6 });
    const phone = computeFraming({ ...base, aspect: 390 / 844 });
    expect(phone.position[2]).toBeGreaterThan(wide.position[2]);
    // Aspects at or above 1/1.4 only need the height fit.
    expect(computeFraming({ ...base, aspect: 1 / 1.4 }).position[2]).toBeCloseTo(wide.position[2]);
  });

  it("scales with body height", () => {
    const tall = computeFraming({ ...base, bodyHeight: 3.4, aspect: 1.6 });
    const short = computeFraming({ ...base, aspect: 1.6 });
    expect(tall.position[2]).toBeCloseTo(short.position[2] * 2);
  });

  it("frames the face from the head height, or a guess before the character loads", () => {
    const loaded = computeFraming({ ...base, aspect: 1, face: { headY: 1.5 } });
    expect(loaded.target[1]).toBeCloseTo(1.62);
    expect(loaded.position[0]).toBe(0.25);
    expect(loaded.position[1]).toBeCloseTo(1.67);
    expect(loaded.position[2]).toBe(0.9);
    const early = computeFraming({ ...base, aspect: 1, face: {} });
    expect(early.target[1]).toBeCloseTo(1.7 * 0.85);
  });
});

describe("mouth", () => {
  it("is silent below the noise floor, half open on typical syllables, full only when very loud", () => {
    expect(mouthTarget(0)).toBe(0);
    expect(mouthTarget(0.2)).toBe(0);
    expect(mouthTarget(1)).toBeCloseTo(0.65);
    expect(mouthTarget(5)).toBe(1);
  });

  it("lets a forced value win", () => {
    expect(mouthTarget(0, 0.6)).toBe(0.6);
  });

  it("opens faster than it closes and never overshoots", () => {
    const dt = 1 / 60;
    const open = smoothMouth(0, 1, dt);
    const close = 1 - smoothMouth(1, 0, dt);
    expect(open).toBeGreaterThan(close);
    expect(open).toBeLessThan(1);
    expect(smoothMouth(0.5, 0.5, dt)).toBeCloseTo(0.5);
  });
});

describe("parseStageParams", () => {
  it("defaults to the body view with blinking on", () => {
    expect(parseStageParams(new URLSearchParams())).toEqual({ view: "body", forcedMouth: undefined, pinnedMorphs: [], gesture: undefined, blink: true });
  });

  it("reads the debug params", () => {
    const p = parseStageParams(new URLSearchParams("view=face&mouth=0.4&morph=viseme_O:1&morph=eyeBlink_L:0.5&morph=browUp&gesture=waving&blink=0"));
    expect(p.view).toBe("face");
    expect(p.forcedMouth).toBe(0.4);
    expect(p.pinnedMorphs).toEqual([
      { name: "viseme_O", weight: 1 },
      { name: "eyeBlink_L", weight: 0.5 },
      { name: "browUp", weight: 1 },
    ]);
    expect(p.gesture).toBe("waving");
    expect(p.blink).toBe(false);
  });
});
