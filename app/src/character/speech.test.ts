import { afterEach, describe, expect, it, vi } from "vitest";
import { Speech } from "./speech";

// A controllable AudioContext: the test sets its state and clock by hand.
class FakeContext {
  static last: FakeContext;
  state: AudioContextState = "suspended";
  currentTime = 0;
  outputLatency = 0;
  destination = {};
  constructor() {
    FakeContext.last = this;
  }
  async resume() {
    this.state = "running";
  }
  async suspend() {
    this.state = "suspended";
  }
  async close() {}
  createBufferSource() {
    return { buffer: null, onended: null, connect() {}, start() {}, stop() {} };
  }
}

const buffer = (duration: number) =>
  ({ duration, sampleRate: 100, getChannelData: () => new Float32Array(Math.round(duration * 100)) }) as unknown as AudioBuffer;

describe("Speech clock", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("runs on the wall clock with nothing to hear, and freezes with audio cut off mid-reply", async () => {
    vi.useFakeTimers({ toFake: ["performance"] });
    vi.stubGlobal("AudioContext", FakeContext);
    const speech = new Speech();
    const ctx = FakeContext.last;
    const t0 = speech.now(); // never unlocked: the wall clock
    vi.advanceTimersByTime(500);
    expect(speech.now()).toBeCloseTo(t0 + 0.5, 3);

    // Once the context runs, the clock follows it, carrying on from where it was.
    await speech.unlock();
    ctx.currentTime = 10;
    const at = speech.now();
    expect(at).toBeCloseTo(t0 + 0.5, 3);
    const first = speech.enqueue(buffer(2));
    speech.enqueue(buffer(2), 0.1);
    expect(first.start).toBeCloseTo(at + 0.03, 3);
    ctx.currentTime = 10.5;
    expect(speech.position).toBeCloseTo(at + 0.5, 3);
    expect(speech.speaking).toBe(true);

    // A call comes in: the context is suspended and its clock stops. Queued speech must stay
    // queued on the same timeline, and the clock waits with it.
    ctx.state = "suspended";
    vi.advanceTimersByTime(3000);
    expect(speech.position).toBeCloseTo(at + 0.5, 3);
    expect(speech.speaking).toBe(true);

    ctx.state = "running";
    ctx.currentTime = 11;
    expect(speech.position).toBeCloseTo(at + 1, 3);
    expect(speech.speaking).toBe(true);

    // Suspended between replies: nothing to wait for, so the wall clock carries it on.
    ctx.currentTime = 20;
    expect(speech.speaking).toBe(false);
    const idle = speech.now();
    ctx.state = "suspended";
    vi.advanceTimersByTime(2000);
    expect(speech.now()).toBeCloseTo(idle + 2, 3);
    ctx.state = "running";
    expect(speech.now()).toBeCloseTo(idle + 2, 3); // following the audio again, without a jump
    ctx.currentTime = 20.25;
    expect(speech.now()).toBeCloseTo(idle + 2.25, 3);
  });

  it("goes quiet when muted: audio stops, the context is suspended and unlock waits", async () => {
    vi.useFakeTimers({ toFake: ["performance"] });
    vi.stubGlobal("AudioContext", FakeContext);
    const speech = new Speech();
    const ctx = FakeContext.last;
    await speech.unlock();
    ctx.currentTime = 5;
    const stopped: number[] = [];
    vi.spyOn(ctx, "createBufferSource").mockImplementation(() => ({
      buffer: null,
      onended: null,
      connect() {},
      start() {},
      stop: () => stopped.push(1),
    }));
    speech.enqueue(buffer(3));
    const before = speech.now();

    speech.muted = true;
    expect(stopped).toHaveLength(1);
    expect(speech.speaking).toBe(false);
    await Promise.resolve();
    expect(ctx.state).toBe("suspended");
    vi.advanceTimersByTime(1000);
    expect(speech.now()).toBeCloseTo(before + 1, 3); // text-only replies keep their pace
    expect(await speech.unlock()).toBe(false);
    expect(ctx.state).toBe("suspended");

    speech.muted = false;
    expect(await speech.unlock()).toBe(true);
    expect(ctx.state).toBe("running");
  });
});
