import { describe, expect, it } from "vitest";
import {
  base64ToBytes,
  blendExpressions,
  evenWords,
  expressionEnvelope,
  expressionProfile,
  revealedWordCount,
  sortByAt,
  splitRevealed,
  takeDue,
} from "./schedule";

const words = [
  { text: "Morning,", start: 0.0, end: 0.41 },
  { text: "Ember.", start: 0.45, end: 0.9 },
  { text: "Slept", start: 1.2, end: 1.5 },
  { text: "well?", start: 1.5, end: 1.9 },
];

describe("revealedWordCount", () => {
  it("counts the words that have started", () => {
    expect(revealedWordCount(words, -0.1)).toBe(0);
    expect(revealedWordCount(words, 0)).toBe(1);
    expect(revealedWordCount(words, 0.44)).toBe(1);
    expect(revealedWordCount(words, 0.45)).toBe(2);
    expect(revealedWordCount(words, 1.0)).toBe(2); // pauses between words hold the line
    expect(revealedWordCount(words, 1.5)).toBe(4);
    expect(revealedWordCount(words, 99)).toBe(4);
    expect(revealedWordCount([], 1)).toBe(0);
  });
});

describe("splitRevealed", () => {
  it("returns the shown text and the pending rest, with the separating space on the pending side", () => {
    expect(splitRevealed(words, 0.5)).toEqual({ shown: "Morning, Ember.", pending: " Slept well?" });
    expect(splitRevealed(words, 0)).toEqual({ shown: "Morning,", pending: " Ember. Slept well?" });
    expect(splitRevealed(words, 5)).toEqual({ shown: "Morning, Ember. Slept well?", pending: "" });
  });
});

describe("evenWords", () => {
  it("spreads words over the duration in proportion to length and ends exactly on it", () => {
    const w = evenWords("I am extraordinarily happy", 4);
    expect(w.map((x) => x.text)).toEqual(["I", "am", "extraordinarily", "happy"]);
    expect(w[0].start).toBe(0);
    expect(w.at(-1)!.end).toBeCloseTo(4);
    for (let i = 1; i < w.length; i++) expect(w[i].start).toBeCloseTo(w[i - 1].end);
    const span = (i: number) => w[i].end - w[i].start;
    expect(span(2)).toBeGreaterThan(span(0) * 3);
  });

  it("handles empty text and extra whitespace", () => {
    expect(evenWords("  ", 2)).toEqual([]);
    expect(evenWords(" a   b ", 2).map((x) => x.text)).toEqual(["a", "b"]);
  });
});

describe("takeDue", () => {
  const events = sortByAt([{ name: "b", at: 1.2 }, { name: "a", at: 0 }, { name: "c", at: 3 }]);

  it("sorts, then hands out each event once as time passes", () => {
    expect(events.map((e) => e.name)).toEqual(["a", "b", "c"]);
    let r = takeDue(events, 0, 0);
    expect(r.due.map((e) => e.name)).toEqual(["a"]);
    r = takeDue(events, r.cursor, 1);
    expect(r.due).toEqual([]);
    r = takeDue(events, r.cursor, 1.2);
    expect(r.due.map((e) => e.name)).toEqual(["b"]);
    r = takeDue(events, r.cursor, 10);
    expect(r.due.map((e) => e.name)).toEqual(["c"]);
    expect(takeDue(events, r.cursor, 20).due).toEqual([]);
  });

  it("delivers several at once when a tick is late", () => {
    expect(takeDue(events, 0, 5).due).toHaveLength(3);
  });

  it("copes with a missing list", () => {
    expect(sortByAt(null)).toEqual([]);
  });
});

describe("expressions", () => {
  it("maps tags to face profiles and leaves whispers alone", () => {
    expect(expressionProfile("laughs")?.weights).toMatchObject({ eyeSquint: expect.any(Number), mouthSmile: expect.any(Number) });
    expect(expressionProfile("chuckles")).toBe(expressionProfile("laughs"));
    expect(expressionProfile("sighs")?.weights).toHaveProperty("browDown");
    expect(expressionProfile("sighs")?.eyesClosed).toBeGreaterThan(0);
    expect(expressionProfile("softly")?.weights).toHaveProperty("mouthSmile");
    expect(expressionProfile("warmly")?.weights).toHaveProperty("mouthSmile");
    expect(expressionProfile("whispers")).toBeUndefined();
    expect(expressionProfile("sings")).toBeUndefined();
  });

  it("eases in, holds, and eases out", () => {
    const d = 1.3;
    expect(expressionEnvelope(-1, d)).toBe(0);
    expect(expressionEnvelope(0, d)).toBe(0);
    expect(expressionEnvelope(0.075, d)).toBeCloseTo(0.5);
    expect(expressionEnvelope(0.6, d)).toBe(1);
    expect(expressionEnvelope(d, d)).toBe(1);
    expect(expressionEnvelope(d + 0.25, d)).toBeCloseTo(0.5);
    expect(expressionEnvelope(d + 0.5, d)).toBe(0);
    expect(expressionEnvelope(100, d)).toBe(0);
  });

  it("blends overlapping expressions by taking the strongest per morph", () => {
    const laugh = expressionProfile("laughs")!;
    const sigh = expressionProfile("sighs")!;
    const mid = blendExpressions([{ profile: laugh, startedAt: 0 }, { profile: sigh, startedAt: 0 }], 0.5);
    expect(mid.weights.eyeSquint).toBeCloseTo(0.7);
    expect(mid.weights.browDown).toBeCloseTo(0.6);
    expect(mid.eyesClosed).toBeCloseTo(0.3);
    const twice = blendExpressions([{ profile: laugh, startedAt: 0 }, { profile: laugh, startedAt: 0.2 }], 0.6);
    expect(twice.weights.mouthSmile).toBeCloseTo(0.8); // not 1.6
    expect(blendExpressions([{ profile: laugh, startedAt: 0 }], 10)).toEqual({ weights: {}, eyesClosed: 0 });
  });
});

describe("base64ToBytes", () => {
  it("decodes audio payloads", () => {
    expect([...base64ToBytes("AQID/w==")]).toEqual([1, 2, 3, 255]);
  });
});
