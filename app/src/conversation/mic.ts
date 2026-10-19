import { pcmLevel } from "./pcm";
import workletUrl from "./mic-worklet.ts?worker&url";

// Push-to-talk capture: getUserMedia -> AudioWorklet (downsample to 16 kHz Int16, 40 ms
// chunks). Call start() directly from the press handler: iOS Safari only lets the audio
// context start, and the permission prompt appear, inside a user gesture.

export type MicErrorKind = "denied" | "no-device" | "insecure" | "unsupported" | "busy" | "failed";

export class MicError extends Error {
  constructor(
    readonly kind: MicErrorKind,
    message: string,
  ) {
    super(message);
    this.name = "MicError";
  }
}

const MESSAGES: Record<MicErrorKind, string> = {
  denied: "Microphone access is blocked. Allow it in your browser's site settings, or type instead.",
  "no-device":
    "No microphone found. If one is connected, check that this browser is allowed to use it in your system's privacy settings.",
  insecure: "The microphone needs a secure (HTTPS) connection.",
  unsupported: "This browser can't capture audio.",
  busy: "The microphone is in use by another app.",
  failed: "Couldn't start the microphone.",
};

// Maps what getUserMedia and the audio setup throw to something the UI can word.
export function classifyMicError(e: unknown): MicError {
  if (e instanceof MicError) return e;
  const name = typeof e === "object" && e !== null && "name" in e ? String((e as { name: unknown }).name) : "";
  let kind: MicErrorKind = "failed";
  if (name === "NotAllowedError" || name === "SecurityError" || name === "PermissionDeniedError") kind = "denied";
  else if (name === "NotFoundError" || name === "OverconstrainedError" || name === "DevicesNotFoundError") kind = "no-device";
  else if (name === "NotReadableError" || name === "AbortError" || name === "TrackStartError") kind = "busy";
  return new MicError(kind, MESSAGES[kind]);
}

// Asks for a voice-tuned mono stream, then for any audio input if the browser can't match that
// (Firefox reports some refusals as NotFoundError).
async function openMic(devices: MediaDevices): Promise<MediaStream> {
  try {
    return await devices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (e) {
    const name = e instanceof Error || e instanceof DOMException ? e.name : "";
    if (name !== "NotFoundError" && name !== "OverconstrainedError") throw e;
    console.warn("microphone: tuned request failed, retrying with defaults", e);
    return await devices.getUserMedia({ audio: true });
  }
}

type AudioSessionNavigator = Navigator & { audioSession?: { type: string } };

// Safari 16.4+: "play-and-record" while the mic is open, "playback" otherwise, which also makes
// Web Audio ignore the silent switch. Other browsers have no audioSession.
export function setAudioSession(type: "playback" | "play-and-record") {
  try {
    const session = (navigator as AudioSessionNavigator).audioSession;
    if (session) session.type = type;
  } catch {
    // unsupported value
  }
}

const FLUSH_TIMEOUT_MS = 150;

export class MicCapture {
  private ctx?: AudioContext;
  private loaded?: Promise<void>;
  private stream?: MediaStream;
  private node?: AudioWorkletNode;
  private mute?: GainNode;
  private source?: MediaStreamAudioSourceNode;
  private generation = 0;
  private flushed?: () => void;
  private smoothed = 0;
  private _active = false;

  constructor(private readonly onChunk: (chunk: ArrayBuffer) => void) {}

  get active(): boolean {
    return this._active;
  }

  // 0..1, rises with the voice and falls away quickly.
  get level(): number {
    return this._active ? this.smoothed : 0;
  }

  // Creates the audio context (suspended until a press resumes it) and loads the capture
  // worklet ahead of time, so the first press only waits for the microphone to open.
  prepare(): void {
    if (this.ctx || typeof AudioContext === "undefined") return;
    try {
      const ctx = (this.ctx = new AudioContext({ latencyHint: "interactive" }));
      const loaded = ctx.audioWorklet.addModule(workletUrl);
      this.loaded = loaded;
      loaded.catch(() => {
        if (this.loaded === loaded) this.loaded = undefined; // the press tries again
      });
    } catch {
      // The press creates it instead.
    }
  }

  // Resolves true once chunks are flowing, false if stop() was called first. Throws MicError.
  async start(): Promise<boolean> {
    if (this._active || this.stream) throw new MicError("busy", "Already capturing.");
    const devices = navigator.mediaDevices;
    if (!devices?.getUserMedia) {
      const kind = window.isSecureContext ? "unsupported" : "insecure";
      throw new MicError(kind, MESSAGES[kind]);
    }
    const generation = ++this.generation;
    let stream: MediaStream | undefined;
    try {
      // Created and resumed synchronously inside the gesture; reused for later presses.
      const ctx = (this.ctx ??= new AudioContext({ latencyHint: "interactive" }));
      const resumed = ctx.resume();
      setAudioSession("play-and-record");
      stream = await openMic(devices);
      if (generation !== this.generation) return this.dropStale(stream); // released before the stream arrived
      this.stream = stream;
      await resumed.catch(() => {});
      await ctx.resume(); // iOS can leave the context "interrupted" after the permission prompt
      this.loaded ??= ctx.audioWorklet.addModule(workletUrl);
      await this.loaded;
      // Cancelled while the worklet loaded; a newer capture may already own this.stream.
      if (generation !== this.generation) return this.dropStale(stream);

      const node = new AudioWorkletNode(ctx, "hekate-pcm-capture", {
        numberOfInputs: 1,
        numberOfOutputs: 1,
        outputChannelCount: [1],
      });
      node.port.onmessage = (e: MessageEvent) => {
        if (e.data === "flushed") return this.flushed?.();
        const chunk = e.data as ArrayBuffer;
        this.smoothed = Math.max(pcmLevel(new Int16Array(chunk)), this.smoothed * 0.7);
        this.onChunk(chunk);
      };
      // Some browsers only run worklets that are connected to the destination; keep it silent.
      const mute = ctx.createGain();
      mute.gain.value = 0;
      const source = ctx.createMediaStreamSource(stream);
      source.connect(node).connect(mute).connect(ctx.destination);
      this.node = node;
      this.mute = mute;
      this.source = source;
      this.smoothed = 0;
      this._active = true;
      return true;
    } catch (e) {
      if (generation !== this.generation) {
        this.dropStale(stream);
        return false;
      }
      this.release();
      setAudioSession("playback");
      console.warn("microphone: capture failed", e);
      throw classifyMicError(e);
    }
  }

  // Stops the tracks right away (the browser's mic indicator goes off), then waits for the
  // last partial chunk so the end of the sentence isn't clipped.
  async stop(): Promise<void> {
    this.generation++;
    const node = this.node;
    const wasActive = this._active;
    this._active = false;
    this.stream?.getTracks().forEach((t) => t.stop());
    if (node && wasActive) {
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, FLUSH_TIMEOUT_MS);
        this.flushed = () => {
          clearTimeout(timer);
          resolve();
        };
        node.port.postMessage("flush");
      });
    }
    this.flushed = undefined;
    this.release();
    setAudioSession("playback");
    void this.ctx?.suspend().catch(() => {});
  }

  // Cleans up a superseded start: only its own stream, never the current capture's resources.
  private dropStale(stream: MediaStream | undefined): false {
    stream?.getTracks().forEach((t) => t.stop());
    if (stream && this.stream === stream) this.stream = undefined;
    return false;
  }

  private release() {
    this.stream?.getTracks().forEach((t) => t.stop());
    this.source?.disconnect();
    this.node?.disconnect();
    this.mute?.disconnect();
    if (this.node) this.node.port.onmessage = null;
    this.stream = this.source = this.node = this.mute = undefined;
    this.smoothed = 0;
  }
}
