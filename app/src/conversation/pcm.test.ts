import { describe, expect, it } from "vitest";
import { Chunker, Downsampler, floatToInt16, pcmLevel } from "./pcm";

describe("floatToInt16", () => {
  it("scales to the full signed range and clips", () => {
    expect(floatToInt16(0)).toBe(0);
    expect(floatToInt16(1)).toBe(32767);
    expect(floatToInt16(-1)).toBe(-32768);
    expect(floatToInt16(0.5)).toBe(16384);
    expect(floatToInt16(3)).toBe(32767);
    expect(floatToInt16(-3)).toBe(-32768);
  });
});

function sine(freq: number, rate: number, seconds: number, amp = 0.5): Float32Array {
  const out = new Float32Array(Math.round(rate * seconds));
  for (let i = 0; i < out.length; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / rate);
  return out;
}

const peak = (a: Int16Array) => a.reduce((m, v) => Math.max(m, Math.abs(v)), 0);

describe("Downsampler", () => {
  it("48 kHz -> 16 kHz yields a third of the samples", () => {
    const d = new Downsampler(48000, 16000);
    const out = d.push(new Float32Array(4800));
    expect(out.length).toBe(1600);
  });

  it("averages each group of three input samples", () => {
    const d = new Downsampler(48000, 16000);
    const out = d.push(Float32Array.from([0.3, 0.6, 0.9, -0.3, -0.3, -0.3]));
    expect([...out]).toEqual([floatToInt16(0.6), floatToInt16(-0.3)]);
  });

  it("keeps the count right for 44.1 kHz and across block boundaries", () => {
    const whole = new Downsampler(44100, 16000).push(sine(440, 44100, 1));
    expect(Math.abs(whole.length - 16000)).toBeLessThanOrEqual(1);

    const input = sine(440, 44100, 1);
    const split = new Downsampler(44100, 16000);
    const parts: number[] = [];
    for (let i = 0; i < input.length; i += 128) parts.push(...split.push(input.subarray(i, i + 128)));
    expect(parts.length).toBe(whole.length);
    expect(parts).toEqual([...whole]); // block size doesn't change the result
  });

  it("preserves in-band tone amplitude and strongly attenuates a tone above the new Nyquist", () => {
    const tone = new Downsampler(48000, 16000).push(sine(1000, 48000, 0.5));
    expect(peak(tone)).toBeGreaterThan(0.45 * 32767);
    const alias = new Downsampler(48000, 16000).push(sine(15000, 48000, 0.5)); // would fold to 1 kHz
    expect(peak(alias)).toBeLessThan(0.2 * 32767);
  });

  it("passes 16 kHz through unchanged", () => {
    const input = Float32Array.from([0.1, -0.2, 0.3]);
    expect([...new Downsampler(16000, 16000).push(input)]).toEqual([...input].map(floatToInt16));
  });

  it("rejects nonsense rates", () => {
    expect(() => new Downsampler(0, 16000)).toThrow(RangeError);
  });
});

describe("Chunker", () => {
  it("emits 640-sample chunks and flushes the remainder", () => {
    const chunks: number[] = [];
    const c = new Chunker(640, (chunk) => chunks.push(chunk.length));
    c.push(new Int16Array(500));
    expect(chunks).toEqual([]);
    c.push(new Int16Array(1500));
    expect(chunks).toEqual([640, 640, 640]); // 2000 samples: three full chunks...
    c.flush();
    expect(chunks).toEqual([640, 640, 640, 80]); // ...and 80 left over
    c.flush();
    expect(chunks).toHaveLength(4);
  });

  it("keeps sample order", () => {
    const got: number[] = [];
    const c = new Chunker(4, (chunk) => got.push(...chunk));
    c.push(Int16Array.from([1, 2, 3]));
    c.push(Int16Array.from([4, 5, 6, 7, 8, 9]));
    c.flush();
    expect(got).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
  });
});

describe("pcmLevel", () => {
  it("is silent for silence and room noise, and rises with speech-level signals", () => {
    expect(pcmLevel(new Int16Array(640))).toBe(0);
    expect(pcmLevel(new Int16Array(640).fill(100))).toBe(0);
    const speech = new Downsampler(16000, 16000).push(sine(200, 16000, 0.04, 0.15));
    expect(pcmLevel(speech)).toBeGreaterThan(0.4);
    expect(pcmLevel(new Downsampler(16000, 16000).push(sine(200, 16000, 0.04, 1)))).toBe(1);
  });
});
