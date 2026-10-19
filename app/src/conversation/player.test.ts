import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SegmentPlayer } from "./player";
import type { Segment } from "./protocol";

// A speech double on a clock the test moves by hand. Audio "decodes" to its byte length / 100 s.
class FakeSpeech {
  running = true;
  position = 100;
  queued: { start: number; end: number; duration: number }[] = [];
  stopped = 0;
  now() {
    return this.position;
  }
  async decode(data: ArrayBuffer) {
    return { duration: data.byteLength / 100 } as AudioBuffer;
  }
  enqueue(buffer: AudioBuffer, gap: number, notBefore: number) {
    const last = this.queued.at(-1);
    const start = Math.max(this.position + 0.03, notBefore, last ? last.end + gap : 0);
    const item = { start, end: start + buffer.duration, duration: buffer.duration };
    this.queued.push(item);
    return { start: item.start, end: item.end, done: Promise.resolve() };
  }
  stop() {
    this.stopped++;
    this.queued = [];
  }
}

const audio = (seconds: number) => btoa("a".repeat(Math.round(seconds * 100)));

function segment(over: Partial<Segment> = {}): Segment {
  return {
    type: "segment",
    turn_id: "t1",
    seq: 0,
    text: "Morning, Rika.",
    duration: 1,
    audio_b64: null,
    audio_format: null,
    words: null,
    gestures: [],
    expressions: [],
    ...over,
  };
}

function setup() {
  const speech = new FakeSpeech();
  const texts: { shown: string; pending: string }[] = [];
  let hidden = 0;
  const bubble = {
    setText: (shown: string, pending = "") => texts.push({ shown, pending }),
    hide: () => hidden++,
  };
  const gestures: string[] = [];
  const expressions: { weights: Record<string, number>; eyes: number }[] = [];
  const character = {
    playGesture: (n: string) => gestures.push(n),
    setExpression: (weights: Record<string, number>, eyes = 0) => expressions.push({ weights, eyes }),
  };
  const player = new SegmentPlayer(
    { speech: speech as never, bubble: bubble as never, character: () => character as never },
    { gapSeconds: 0.1, lingerSeconds: 2, tickMs: 10 },
  );
  const advance = (seconds: number) => {
    speech.position += seconds;
    vi.advanceTimersByTime(20);
  };
  return { speech, player, texts, gestures, expressions, advance, hidden: () => hidden };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("SegmentPlayer", () => {
  it("reveals a text-only segment evenly over its duration and fires gestures and expressions on time", async () => {
    const { player, speech, texts, gestures, expressions, advance } = setup();
    player.enqueue(segment({ duration: 2, gestures: [{ name: "waving", at: 0.5 }], expressions: [{ name: "laughs", at: 1 }] }));
    await vi.advanceTimersByTimeAsync(0);
    expect(player.busy).toBe(true);
    expect(speech.queued).toEqual([]); // no audio, nothing handed to Speech

    advance(0.2); // starts at ~100.03
    expect(texts.at(-1)!.shown).toBe("Morning,");
    expect(gestures).toEqual([]);
    advance(0.5);
    expect(gestures).toEqual(["waving"]);
    advance(0.7); // elapsed ~1.4
    expect(texts.at(-1)!.shown).toBe("Morning, Rika.");
    expect(expressions.at(-1)!.weights.mouthSmile).toBeGreaterThan(0);
    advance(3);
    expect(player.busy).toBe(false);
    expect(gestures).toEqual(["waving"]); // each fires once
  });

  it("plays audio segments back to back in order with a gap, and reveals by the word timings", async () => {
    const { player, speech, texts, advance } = setup();
    player.enqueue(segment({ seq: 0, audio_b64: audio(1), text: "One two.", words: [{ text: "One", start: 0, end: 0.4 }, { text: "two.", start: 0.6, end: 1 }] }));
    player.enqueue(segment({ seq: 1, audio_b64: audio(0.5), text: "Three." }));
    await vi.advanceTimersByTimeAsync(0);
    expect(speech.queued).toHaveLength(2);
    const [a, b] = speech.queued;
    expect(a.duration).toBeCloseTo(1);
    expect(b.start).toBeCloseTo(a.end + 0.1);

    advance(a.start - speech.position + 0.3);
    expect(texts.at(-1)).toEqual({ shown: "One", pending: " two." });
    advance(0.5);
    expect(texts.at(-1)).toEqual({ shown: "One two.", pending: "" });
    advance(b.start - speech.position + 0.01);
    expect(texts.at(-1)!.shown).toBe("Three."); // no words from the server: even reveal, first word at once
  });

  it("remembers the last segment heard, per turn", async () => {
    const { player, speech, advance } = setup();
    expect(player.heardSeq("t1")).toBe(-1);
    player.enqueue(segment({ seq: 0, audio_b64: audio(1) }));
    player.enqueue(segment({ seq: 1, audio_b64: audio(1) }));
    await vi.advanceTimersByTimeAsync(0);
    expect(player.heardSeq("t1")).toBe(-1); // queued, not yet audible
    advance(speech.queued[0].start - speech.position + 0.2);
    expect(player.heardSeq("t1")).toBe(0);
    advance(speech.queued[1].start - speech.position + 0.2);
    expect(player.heardSeq("t1")).toBe(1);
    expect(player.heardSeq("other")).toBe(-1);
    expect(player.heardSeq(undefined)).toBe(-1);
  });

  it("drops repeated seqs and starts over for a new turn", async () => {
    const { player, speech } = setup();
    player.enqueue(segment({ seq: 0, audio_b64: audio(1) }));
    player.enqueue(segment({ seq: 0, audio_b64: audio(1) }));
    await vi.advanceTimersByTimeAsync(0);
    expect(speech.queued).toHaveLength(1);
    player.enqueue(segment({ turn_id: "t2", seq: 0, audio_b64: audio(1) }));
    await vi.advanceTimersByTimeAsync(0);
    expect(speech.queued).toHaveLength(2);
  });

  it("stop() silences everything, clears the bubble and ignores audio still decoding", async () => {
    const { player, speech, hidden, expressions } = setup();
    player.enqueue(segment({ audio_b64: audio(1) }));
    player.stop(); // before the decode finished
    await vi.advanceTimersByTimeAsync(10);
    expect(speech.queued).toEqual([]);
    expect(speech.stopped).toBe(1);
    expect(hidden()).toBe(1);
    expect(expressions.at(-1)).toEqual({ weights: {}, eyes: 0 });
    expect(player.busy).toBe(false);
  });

  it("hides the bubble a while after the turn is over and everything was said", async () => {
    const { player, hidden, advance } = setup();
    player.enqueue(segment({ duration: 1 }));
    await vi.advanceTimersByTimeAsync(0);
    player.finishTurn("t1");
    advance(1.5);
    expect(hidden()).toBe(0);
    advance(3);
    expect(hidden()).toBe(1);
  });

  it("falls back to text when the audio can't be played", async () => {
    const { player, speech, texts, advance } = setup();
    speech.running = false;
    player.enqueue(segment({ audio_b64: audio(1), duration: 1 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(speech.queued).toEqual([]);
    advance(0.3);
    expect(texts.length).toBeGreaterThan(0);
  });

  it("lets a late tick skip past a stale segment's gestures instead of replaying them", async () => {
    const { player, gestures, advance } = setup();
    player.enqueue(segment({ duration: 1, gestures: [{ name: "waving", at: 0.2 }] }));
    await vi.advanceTimersByTimeAsync(0);
    advance(10); // the tab was in the background
    expect(gestures).toEqual([]);
  });
});
