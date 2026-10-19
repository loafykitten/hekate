import { afterEach, describe, expect, it, vi } from "vitest";
import { classifyMicError, MicCapture, MicError } from "./mic";

const named = (name: string) => Object.assign(new Error("x"), { name });

describe("classifyMicError", () => {
  it("separates a blocked mic from a missing or busy one", () => {
    expect(classifyMicError(named("NotAllowedError")).kind).toBe("denied");
    expect(classifyMicError(named("NotFoundError")).kind).toBe("no-device");
    expect(classifyMicError(named("NotReadableError")).kind).toBe("busy");
    expect(classifyMicError(new Error("weird")).kind).toBe("failed");
    expect(classifyMicError("string").kind).toBe("failed");
  });

  it("words each kind for the user and passes MicErrors through", () => {
    expect(classifyMicError(named("NotAllowedError")).message).toMatch(/blocked/);
    expect(classifyMicError(named("NotFoundError")).message).toMatch(/No microphone/);
    const own = new MicError("insecure", "needs https");
    expect(classifyMicError(own)).toBe(own);
  });
});

// Minimal Web Audio and getUserMedia doubles: enough to drive MicCapture's lifecycle.
function stubAudio() {
  let resolveModule!: () => void;
  const moduleLoaded = new Promise<void>((r) => (resolveModule = r));
  const streams: { stopped: boolean }[] = [];
  const node = () => ({ connect: (n: unknown) => n, disconnect: () => {} });
  vi.stubGlobal("navigator", {
    mediaDevices: {
      getUserMedia: async () => {
        const track = { stopped: false, stop() { this.stopped = true; } };
        streams.push(track);
        return { getTracks: () => [track] };
      },
    },
  });
  vi.stubGlobal(
    "AudioContext",
    class {
      audioWorklet = { addModule: () => moduleLoaded };
      resume = async () => {};
      suspend = async () => {};
      createGain = () => ({ ...node(), gain: { value: 1 } });
      createMediaStreamSource = () => node();
      destination = {};
    },
  );
  vi.stubGlobal(
    "AudioWorkletNode",
    class {
      port = { onmessage: null as unknown, postMessage: () => {} };
      connect = (n: unknown) => n;
      disconnect = () => {};
    },
  );
  return { streams, resolveModule };
}

describe("MicCapture lifecycle", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("a start cancelled while the worklet loads never tears down the next recording", async () => {
    const audio = stubAudio();
    const mic = new MicCapture(() => {});
    const first = mic.start();
    await vi.waitFor(() => expect(audio.streams).toHaveLength(1)); // waiting on addModule now
    await mic.stop();
    const second = mic.start();
    await vi.waitFor(() => expect(audio.streams).toHaveLength(2));
    audio.resolveModule();

    expect(await first).toBe(false);
    expect(await second).toBe(true);
    expect(mic.active).toBe(true);
    expect(audio.streams[0].stopped).toBe(true);
    expect(audio.streams[1].stopped).toBe(false);
  });
});
