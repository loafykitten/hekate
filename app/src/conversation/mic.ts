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

export interface MicOptions {
  // How long the microphone stays open after a recording, so the next press starts at once.
  // 0 closes it after every recording. Either way only audio between a press and its end is
  // ever recorded: the worklet drops everything else.
  keepOpenMs?: number;
}

// Desktop browsers keep the mic open between recordings. Phones close it each time: with the
// mic open they route audio as a call, which makes her voice quieter.
export function defaultMicOptions(): MicOptions {
  const desktop = typeof window !== "undefined" && window.matchMedia?.("(hover: hover) and (pointer: fine)").matches;
  return { keepOpenMs: desktop ? 5 * 60_000 : 0 };
}

export class MicCapture {
  private ctx?: AudioContext;
  private loaded?: Promise<void>;
  private stream?: MediaStream;
  private node?: AudioWorkletNode;
  private mute?: GainNode;
  private source?: MediaStreamAudioSourceNode;
  private generation = 0;
  // The recording whose audio is forwarded: set when one starts, kept while its last chunks
  // drain after stop(), cleared once they have. Chunks from any other recording are dropped.
  private recordingId = 0;
  private forwarding?: number;
  private flushed?: (id: number) => void;
  private smoothed = 0;
  private _active = false;
  private idleTimer?: ReturnType<typeof setTimeout>;
  private readonly keepOpenMs: number;

  constructor(
    private readonly onChunk: (chunk: ArrayBuffer) => void,
    options: MicOptions = {},
  ) {
    this.keepOpenMs = options.keepOpenMs ?? 0;
    if (this.keepOpenMs > 0 && typeof document !== "undefined") {
      // Close an idle open mic as soon as the app goes to the background.
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "hidden" && !this._active) this.close();
      });
    }
  }

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
    if (this._active) throw new MicError("busy", "Already capturing.");
    clearTimeout(this.idleTimer);
    if (this.isOpen()) return this.record(++this.generation);
    if (this.stream) throw new MicError("busy", "Already capturing.");
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
      const began = performance.now();
      stream = await openMic(devices);
      console.info(`microphone: opened in ${Math.round(performance.now() - began)} ms`);
      if (generation !== this.generation) return this.dropStale(stream); // released before the stream arrived
      this.stream = stream;
      // A device that goes away (unplugged headset) closes the mic; the next press reopens it.
      stream.getTracks().forEach((t) => t.addEventListener("ended", () => !this._active && this.close()));
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
      node.port.onmessage = (e: MessageEvent<{ id?: number; pcm?: ArrayBuffer; flushed?: number }>) => {
        const { id, pcm, flushed } = e.data;
        if (flushed !== undefined) return this.flushed?.(flushed);
        // Only the current recording's audio, never a cancelled one's or anything in between.
        if (!pcm || id === undefined || id !== this.forwarding) return;
        this.smoothed = Math.max(pcmLevel(new Int16Array(pcm)), this.smoothed * 0.7);
        this.onChunk(pcm);
      };
      // Some browsers only run worklets that are connected to the destination; keep it silent.
      const mute = ctx.createGain();
      mute.gain.value = 0;
      const source = ctx.createMediaStreamSource(stream);
      source.connect(node).connect(mute).connect(ctx.destination);
      this.node = node;
      this.mute = mute;
      this.source = source;
      return this.record(generation);
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

  // Ends the recording and waits for the last partial chunk so the end of the sentence isn't
  // clipped. The mic then closes, or stays open (not recording) for keepOpenMs.
  async stop(): Promise<void> {
    this.generation++;
    const node = this.node;
    const wasActive = this._active;
    const ending = this.forwarding;
    this._active = false;
    const keepOpen = this.keepOpenMs > 0 && wasActive && this.isOpen();
    if (!keepOpen) this.stream?.getTracks().forEach((t) => t.stop()); // the mic indicator goes off now
    if (node && wasActive && ending !== undefined) {
      // The ending recording keeps forwarding until the worklet says its last chunk is out.
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, FLUSH_TIMEOUT_MS);
        this.flushed = (id) => {
          if (id !== ending) return;
          clearTimeout(timer);
          resolve();
        };
        node.port.postMessage({ flush: ending });
      });
    }
    if (this.forwarding === ending) this.forwarding = undefined;
    this.flushed = undefined;
    if (keepOpen && this.isOpen()) {
      clearTimeout(this.idleTimer);
      this.idleTimer = setTimeout(() => this.close(), this.keepOpenMs);
      return;
    }
    this.close();
  }

  // Closes the microphone (and the indicator) unless a recording is running.
  close() {
    if (this._active) return;
    clearTimeout(this.idleTimer);
    this.generation++;
    this.forwarding = undefined;
    this.release();
    setAudioSession("playback");
    void this.ctx?.suspend().catch(() => {});
  }

  private isOpen(): boolean {
    return !!this.node && !!this.stream?.getTracks().some((t) => t.readyState === "live");
  }

  // Starts recording on an open mic.
  private async record(generation: number): Promise<boolean> {
    const ctx = this.ctx;
    const node = this.node;
    if (!ctx || !node) return false;
    // Switch recordings before anything awaits: from here on a previous (perhaps cancelled)
    // recording's late chunks are dropped.
    const id = ++this.recordingId;
    this.forwarding = id;
    setAudioSession("play-and-record");
    await ctx.resume().catch(() => {});
    if (generation !== this.generation) {
      if (this.forwarding === id) this.forwarding = undefined;
      return false; // stopped while resuming
    }
    this.smoothed = 0;
    this._active = true;
    node.port.postMessage({ start: id });
    return true;
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
