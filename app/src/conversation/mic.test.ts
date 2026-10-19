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
function stubAudio(refusals: string[] = []) {
  const requests: MediaStreamConstraints[] = [];
  let resolveModule!: () => void;
  const moduleLoaded = new Promise<void>((r) => (resolveModule = r));
  const streams: { stopped: boolean }[] = [];
  // Each worklet node: the messages it got, and a way to play the worklet's side. By default a
  // flush delivers a last partial chunk of the recording, then the acknowledgment; with
  // `manualFlush` the test sends those itself.
  const nodes: { sent: unknown[]; emit: (data: unknown) => void; manualFlush: boolean }[] = [];
  const node = () => ({ connect: (n: unknown) => n, disconnect: () => {} });
  vi.stubGlobal("navigator", {
    mediaDevices: {
      getUserMedia: async (constraints: MediaStreamConstraints) => {
        requests.push(constraints);
        const refusal = refusals.shift();
        if (refusal) throw named(refusal);
        const track = {
          stopped: false,
          readyState: "live",
          stop() {
            this.stopped = true;
            this.readyState = "ended";
          },
          addEventListener: () => {},
        };
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
      port: { onmessage: ((e: { data: unknown }) => void) | null; postMessage: (m: unknown) => void };
      connect = (n: unknown) => n;
      disconnect = () => {};
      constructor() {
        const sent: unknown[] = [];
        const entry = { sent, emit: (data: unknown) => port.onmessage?.({ data }), manualFlush: false };
        const port = {
          onmessage: null as ((e: { data: unknown }) => void) | null,
          postMessage: (m: unknown) => {
            sent.push(m);
            const flush = (m as { flush?: number }).flush;
            if (flush !== undefined && !entry.manualFlush) {
              queueMicrotask(() => {
                port.onmessage?.({ data: { id: flush, pcm: new Int16Array([7]).buffer } });
                port.onmessage?.({ data: { flushed: flush } });
              });
            }
          },
        };
        this.port = port;
        nodes.push(entry);
      }
    },
  );
  return { streams, requests, nodes, resolveModule };
}

describe("MicCapture lifecycle", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

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

  it("retries with default audio when the tuned request finds no device", async () => {
    const audio = stubAudio(["NotFoundError"]);
    audio.resolveModule();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const mic = new MicCapture(() => {});
    expect(await mic.start()).toBe(true);
    expect(audio.requests).toHaveLength(2);
    expect(audio.requests[1]).toEqual({ audio: true });
  });

  it("does not retry a refusal", async () => {
    const audio = stubAudio(["NotAllowedError"]);
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const mic = new MicCapture(() => {});
    await expect(mic.start()).rejects.toMatchObject({ kind: "denied" });
    expect(audio.requests).toHaveLength(1);
  });
});

describe("MicCapture.prepare", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("loads the worklet ahead of the press, and the press reuses it", async () => {
    const audio = stubAudio();
    audio.resolveModule();
    let contexts = 0;
    const Base = globalThis.AudioContext;
    vi.stubGlobal(
      "AudioContext",
      class extends (Base as unknown as new () => object) {
        constructor() {
          super();
          contexts++;
        }
      },
    );
    const mic = new MicCapture(() => {});
    mic.prepare();
    expect(contexts).toBe(1);
    expect(await mic.start()).toBe(true);
    expect(contexts).toBe(1);
  });
});

describe("MicCapture keeping the mic open", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("stays open between recordings, starts the next at once, and closes when idle", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "info").mockImplementation(() => {});
    const audio = stubAudio();
    audio.resolveModule();
    const chunks: ArrayBuffer[] = [];
    const mic = new MicCapture((c) => chunks.push(c), { keepOpenMs: 60_000 });

    expect(await mic.start()).toBe(true);
    await mic.stop();
    expect(audio.streams[0].stopped).toBe(false); // still open: the indicator stays on

    expect(chunks).toHaveLength(1); // the last partial chunk, delivered before the acknowledgment
    chunks.length = 0;

    // Audio while open but not recording never reaches the page's callback.
    audio.nodes[0].emit({ id: 1, pcm: new Int16Array([5, 5]).buffer });
    expect(chunks).toHaveLength(0);

    expect(await mic.start()).toBe(true);
    expect(audio.requests).toHaveLength(1); // no second getUserMedia
    expect(audio.nodes).toHaveLength(1);
    expect(audio.nodes[0].sent).toEqual([{ start: 1 }, { flush: 1 }, { start: 2 }]);
    audio.nodes[0].emit({ id: 2, pcm: new Int16Array([1, 2]).buffer });
    expect(chunks).toHaveLength(1);
    await mic.stop();

    vi.advanceTimersByTime(60_000);
    expect(audio.streams[0].stopped).toBe(true);
    expect(await mic.start()).toBe(true); // reopens
    expect(audio.requests).toHaveLength(2);
  });

  it("closes after every recording when not kept open", async () => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    const audio = stubAudio();
    audio.resolveModule();
    const mic = new MicCapture(() => {});
    expect(await mic.start()).toBe(true);
    await mic.stop();
    expect(audio.streams[0].stopped).toBe(true);
  });
});

describe("MicCapture recording handoff", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("a cancelled recording's late audio never joins the next one", async () => {
    vi.spyOn(console, "info").mockImplementation(() => {});
    const audio = stubAudio();
    audio.resolveModule();
    const chunks: number[] = [];
    const mic = new MicCapture((c) => chunks.push(new Int16Array(c)[0]), { keepOpenMs: 60_000 });
    expect(await mic.start()).toBe(true);
    const node = audio.nodes[0];
    node.manualFlush = true;
    const stopping = mic.stop(); // cancel: nobody waits for it
    expect(await mic.start()).toBe(true); // and start again right away

    node.emit({ id: 1, pcm: new Int16Array([111]).buffer }); // the old recording's buffered audio
    node.emit({ flushed: 1 });
    node.emit({ id: 2, pcm: new Int16Array([222]).buffer });
    await stopping;
    expect(chunks).toEqual([222]);
  });
});
