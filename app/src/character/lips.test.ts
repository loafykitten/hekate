import { describe, expect, it } from "vitest";
import { LipTrack, VISEME_BLEND, VISEME_LEAD, visemeBlend } from "./lips";

const line = [
  { v: "PP", at: 0 },
  { v: "O", at: 0.1 },
  { v: "sil", at: 0.3 },
];

describe("visemeBlend", () => {
  it("shows nothing before the first shape", () => {
    expect(visemeBlend(line, -0.01)).toEqual({});
  });

  it("eases each shape in over the one before it", () => {
    expect(visemeBlend(line, 0.035).PP).toBeCloseTo(0.5); // the first eases in from rest
    expect(visemeBlend(line, 0.09)).toEqual({ PP: 1 });
    const mid = visemeBlend(line, 0.1 + VISEME_BLEND / 2);
    expect(mid.O).toBeCloseTo(0.5);
    expect(mid.PP).toBeCloseTo(0.5);
    expect(visemeBlend(line, 0.25)).toEqual({ O: 1 });
  });

  it("closes the mouth on silence", () => {
    expect(visemeBlend(line, 0.3 + VISEME_BLEND / 2).O).toBeCloseTo(0.5);
    expect(visemeBlend(line, 1)).toEqual({});
  });

  it("never sums past 1, even when a shape repeats", () => {
    const same = visemeBlend([{ v: "aa", at: 0 }, { v: "aa", at: 0.05 }], 0.06);
    expect(same.aa).toBeCloseTo(1);
  });
});

describe("LipTrack", () => {
  it("answers for the line playing, a moment ahead of the sound", () => {
    const track = new LipTrack();
    track.add(10, 10.4, line);
    expect(track.at(9)).toBeUndefined(); // nothing playing yet: loudness alone
    expect(track.at(10.1 - VISEME_LEAD + 0.08)).toEqual({ O: 1 });
    expect(track.at(11)).toBeUndefined(); // done, and dropped
  });

  it("plays lines back to back and forgets them when cleared", () => {
    const track = new LipTrack();
    track.add(10, 10.4, line);
    track.add(10.5, 11, [{ v: "E", at: 0 }]);
    expect(track.at(10.6)).toEqual({ E: 1 });
    track.clear();
    expect(track.at(10.6)).toBeUndefined();
  });

  it("ignores lines without shapes", () => {
    const track = new LipTrack();
    track.add(10, 11, []);
    expect(track.at(10.5)).toBeUndefined();
  });
});
