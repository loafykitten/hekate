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
  async close() {}
  createBufferSource() {
    return { buffer: null, onended: null, connect() {}, start() {}, stop() {} };
  }
}

const buffer = (duration: number) =>
  ({ duration, sampleRate: 100, getChannelData: () => new Float32Array(Math.round(duration * 100)) }) as unknown as AudioBuffer;

describe("Speech clock", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("uses the wall clock only until audio has run, then freezes with a suspended context", async () => {
    vi.stubGlobal("AudioContext", FakeContext);
    const speech = new Speech();
    const ctx = FakeContext.last;
    expect(speech.now()).toBeCloseTo(performance.now() / 1000, 1); // never unlocked: wall clock

    await speech.unlock();
    ctx.currentTime = 10;
    speech.enqueue(buffer(2));
    speech.enqueue(buffer(2), 0.1);
    ctx.currentTime = 10.5;
    expect(speech.speaking).toBe(true);

    // A call comes in: the context is suspended and its clock stops. Queued speech must stay
    // queued on the same timeline rather than jumping to an unrelated clock.
    ctx.state = "suspended";
    expect(speech.position).toBe(10.5);
    expect(speech.speaking).toBe(true);

    ctx.state = "running";
    ctx.currentTime = 11;
    expect(speech.position).toBe(11);
    expect(speech.speaking).toBe(true);
  });
});
