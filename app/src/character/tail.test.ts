import { describe, expect, it } from "vitest";
import { TailMotion, type TailAngles, type TailInput } from "./tail";

// A seeded generator, so flicks land at the same moments every run.
function seeded(seed = 1) {
  let s = seed;
  return () => {
    s = (s * 16807) % 2147483647;
    return (s - 1) / 2147483646;
  };
}

const still: Omit<TailInput, "dt"> = { liveliness: 0, baseTurn: 0, baseTip: 0 };

// Runs the tail for `seconds` and returns a copy of every frame's angles.
function simulate(tail: TailMotion, seconds: number, dt = 1 / 60, input: (t: number) => Omit<TailInput, "dt"> = () => still) {
  const frames: TailAngles[][] = [];
  for (let t = 0; t < seconds; t += dt) frames.push(tail.update({ dt, ...input(t) }).map((a) => ({ ...a })));
  return frames;
}

const tipYaw = (f: TailAngles[]) => f.reduce((sum, a) => sum + a.yaw, 0);
const tipPitch = (f: TailAngles[]) => f.reduce((sum, a) => sum + a.pitch, 0);

describe("TailMotion", () => {
  it("rests in a curve that droops at the base and lifts toward the tip", () => {
    const [first] = simulate(new TailMotion(8, seeded()), 0.01);
    expect(first[0].pitch).toBeLessThan(-0.2);
    expect(first[7].pitch).toBeGreaterThan(0.2);
  });

  it("keeps moving, side to side and up and down, within bounds", () => {
    const frames = simulate(new TailMotion(8, seeded()), 120);
    const yaws = frames.map(tipYaw);
    const pitches = frames.map(tipPitch);
    // It sways a visible amount both ways...
    expect(Math.max(...yaws)).toBeGreaterThan(0.35);
    expect(Math.min(...yaws)).toBeLessThan(-0.35);
    expect(Math.max(...pitches) - Math.min(...pitches)).toBeGreaterThan(0.15);
    // ...but never wildly (the springs are capped, the waves bounded).
    for (const f of frames) for (const a of f) expect(Math.abs(a.yaw) + Math.abs(a.pitch)).toBeLessThan(0.75);
  });

  it("never jumps between frames, even when she starts and stops talking", () => {
    const frames = simulate(new TailMotion(8, seeded()), 60, 1 / 60, (t) => ({ ...still, liveliness: Math.floor(t / 5) % 2 }));
    for (let k = 1; k < frames.length; k++) {
      for (let i = 0; i < 8; i++) {
        expect(Math.abs(frames[k][i].yaw - frames[k - 1][i].yaw)).toBeLessThan(0.03);
        expect(Math.abs(frames[k][i].pitch - frames[k - 1][i].pitch)).toBeLessThan(0.03);
      }
    }
  });

  it("moves more while she talks", () => {
    // Average speed of the tip, which the slow drift barely touches.
    const speed = (liveliness: number) => {
      const yaws = simulate(new TailMotion(8, seeded()), 60, 1 / 60, () => ({ ...still, liveliness })).map(tipYaw);
      return yaws.slice(1).reduce((sum, y, k) => sum + Math.abs(y - yaws[k]), 0) / 60;
    };
    expect(speed(1)).toBeGreaterThan(speed(0) * 1.4);
  });

  it("lags behind a turn of the body, then settles", () => {
    // Two identical tails; one feels the body turn 0.3 rad to her left over a tenth of a second.
    const calm = new TailMotion(8, seeded());
    const turned = new TailMotion(8, seeded());
    const dt = 1 / 60;
    const turning = (t: number) => ({ ...still, baseTurn: t < 0.1 ? (0.3 * dt) / 0.1 : 0 });
    const a = simulate(calm, 4, dt);
    const b = simulate(turned, 4, dt, turning);
    const lag = b.map((f, k) => tipYaw(f) - tipYaw(a[k]));
    // The tip swings the other way first...
    expect(Math.min(...lag.slice(0, 30))).toBeLessThan(-0.1);
    // ...and is back with the calm tail a few seconds later.
    expect(Math.abs(lag.at(-1)!)).toBeLessThan(0.01);
  });

  it("holds against part of a hip tilt", () => {
    const [level] = simulate(new TailMotion(8, seeded()), 0.01);
    const [leaning] = simulate(new TailMotion(8, seeded()), 0.01, 1 / 60, () => ({ ...still, baseLean: -0.4 }));
    // Leaning back by 0.4 rad lifts the base by half that, so the tail drops only half as far.
    expect(leaning[0].pitch - level[0].pitch).toBeCloseTo(0.2, 5);
    expect(leaning[7].pitch).toBeCloseTo(level[7].pitch, 5);
  });

  it("flicks now and then", () => {
    // Over two minutes there are several flicks; each shows as a quick change at the tip.
    const frames = simulate(new TailMotion(8, seeded(7)), 120);
    const speeds = frames.slice(1).map((f, k) => Math.abs(f[7].yaw - frames[k][7].yaw) * 60);
    const fast = speeds.filter((v) => v > 0.25).length;
    expect(fast).toBeGreaterThan(0);
  });

  it("copes with odd frame times and bone counts", () => {
    expect(new TailMotion(0).update({ dt: 0.016, ...still })).toEqual([]);
    const one = new TailMotion(1, seeded());
    for (const dt of [0, 1 / 15, 5, -1]) {
      const [a] = one.update({ dt, ...still });
      expect(Number.isFinite(a.yaw) && Number.isFinite(a.pitch)).toBe(true);
    }
    // Two bones keep the same overall curve as eight.
    const two = simulate(new TailMotion(2, seeded()), 0.01)[0];
    const eight = simulate(new TailMotion(8, seeded()), 0.01)[0];
    expect(tipPitch(two)).toBeCloseTo(tipPitch(eight), 1);
  });
});
