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
  // Keep the microphone open while the app is visible, so every press starts at once: it opens
  // when the app loads or comes back (if permission was already granted) and closes when the app
  // is hidden. Otherwise it closes after every recording. Either way only audio between a press
  // and its end is ever recorded: the worklet drops everything else.
  stayOpen?: boolean;
}

// Desktop browsers keep the mic open. Phones close it after each recording: with the mic open
// they route audio as a call, which makes her voice quieter.
export function defaultMicOptions(): MicOptions {
  const desktop = typeof window !== "undefined" && window.matchMedia?.("(hover: hover) and (pointer: fine)").matches;
  return { stayOpen: !!desktop };
}

// Whether the browser has blocked the microphone for this site. Unknown counts as not blocked:
// opening it then asks, which is expected in an app you talk to.
async function micBlocked(): Promise<boolean> {
  try {
    const status = await navigator.permissions?.query({ name: "microphone" as PermissionName });
    return status?.state === "denied";
  } catch {
    return false;
  }
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
  // An open in progress (from a press or from warming up), which a press waits for.
  private openingTask?: { generation: number; done: Promise<void> };
  // The open still in progress, if it hasn't been cancelled (stop and close move the generation
  // on, so a cancelled one is never waited for).
  private get opening(): Promise<void> | undefined {
    const task = this.openingTask;
    return task && task.generation === this.generation ? task.done : undefined;
  }

  // Set when the user turned down the prompt that warming up raised: no more prompts until a
  // press opens the mic.
  private warmRefused = false;
  private readonly stayOpen: boolean;
  // prepare() was called: the app wants the mic (speech-to-text is available), so coming back
  // to the app opens it again.
  private armed = false;

  constructor(
    private readonly onChunk: (chunk: ArrayBuffer) => void,
    options: MicOptions = {},
  ) {
    this.stayOpen = options.stayOpen ?? false;
    if (this.stayOpen && typeof document !== "undefined") {
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible") return this.armed && this.prepare();
        // A recording still draining its last chunks closes once it's done (see stop()).
        if (!this._active && this.forwarding === undefined) this.close();
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
  // With stayOpen it also opens the microphone now, when the user already allowed it.
  prepare(): void {
    this.armed = true;
    if (!this.ctx && typeof AudioContext !== "undefined") {
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
    if (this.stayOpen) void this.warm();
  }

  // Opens the microphone without recording, ahead of the press. The first time, this is where
  // the browser asks for permission.
  private async warm(): Promise<void> {
    const visible = () => typeof document === "undefined" || document.visibilityState === "visible";
    if (this.warmRefused || this.isOpen() || this.opening || this.stream || !visible()) return;
    if (!navigator.mediaDevices?.getUserMedia) return;
    const before = this.generation;
    if (await micBlocked()) return;
    // Whatever happened meanwhile wins: a press (it opens the mic itself), the app being hidden,
    // a close. Each of those moves the generation on.
    if (this.generation !== before || !visible() || this.opening || this.stream || this.isOpen()) return;
    const generation = ++this.generation;
    const opening = this.open(generation, false).then(
      () => {},
      (e: unknown) => {
        console.warn("microphone: couldn't open ahead of time", e); // the press will try, and say why
        if (classifyMicError(e).kind === "denied") this.warmRefused = true;
        if (generation === this.generation) this.release();
      },
    );
    const task = { generation, done: opening };
    this.openingTask = task;
    await opening;
    if (this.openingTask === task) this.openingTask = undefined;
  }

  // Resolves true once chunks are flowing, false if stop() was called first. Throws MicError.
  async start(): Promise<boolean> {
    if (this._active) throw new MicError("busy", "Already capturing.");
    // Resume inside the gesture, before anything awaits (iOS needs it there).
    void this.ctx?.resume().catch(() => {});
    const pending = this.opening;
    if (pending) {
      const generation = this.generation;
      await pending; // warming up already: wait for it rather than open twice
      if (generation !== this.generation) return false; // cancelled (or superseded) meanwhile
    }
    if (this.isOpen()) return this.record(++this.generation);
    if (this.stream) throw new MicError("busy", "Already capturing.");
    this.warmRefused = false; // the user is asking now
    const devices = navigator.mediaDevices;
    if (!devices?.getUserMedia) {
      const kind = window.isSecureContext ? "unsupported" : "insecure";
      throw new MicError(kind, MESSAGES[kind]);
    }
    const generation = ++this.generation;
    const opened = this.open(generation, true);
    // Registered so a warm-up starting meanwhile stands aside instead of opening a second time.
    const opening = opened.then(
      () => {},
      () => {},
    );
    const task = { generation, done: opening };
    this.openingTask = task;
    try {
      if (!(await opened)) return false;
      return await this.record(generation);
    } catch (e) {
      if (generation !== this.generation) return false;
      this.release();
      setAudioSession("playback");
      console.warn("microphone: capture failed", e);
      throw classifyMicError(e);
    } finally {
      if (this.openingTask === task) this.openingTask = undefined;
    }
  }

  // Opens the microphone and builds the capture graph, without recording. Resolves false when
  // a newer start or stop superseded it (its stream is dropped). Throws what getUserMedia throws.
  private async open(generation: number, inGesture: boolean): Promise<boolean> {
    // Created and resumed synchronously inside the gesture; reused for later presses.
    const ctx = (this.ctx ??= new AudioContext({ latencyHint: "interactive" }));
    const resumed = inGesture ? ctx.resume() : undefined;
    if (inGesture) setAudioSession("play-and-record");
    const began = performance.now();
    const stream = await openMic(navigator.mediaDevices);
    console.info(`microphone: opened in ${Math.round(performance.now() - began)} ms`);
    if (generation !== this.generation) return this.dropStale(stream); // released before the stream arrived
    this.stream = stream;
    // A device that goes away (unplugged headset) closes the mic; the next press reopens it.
    stream.getTracks().forEach((t) => t.addEventListener("ended", () => !this._active && this.close()));
    if (resumed) {
      await resumed.catch(() => {});
      await ctx.resume(); // iOS can leave the context "interrupted" after the permission prompt
    }
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
    return true;
  }

  // Ends the recording and waits for the last partial chunk so the end of the sentence isn't
  // clipped. The mic then closes, or with stayOpen stays open (not recording) while visible.
  async stop(): Promise<void> {
    this.generation++;
    const node = this.node;
    const wasActive = this._active;
    const ending = this.forwarding;
    this._active = false;
    const keepOpen = this.stayOpen && wasActive && this.isOpen();
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
    const hidden = typeof document !== "undefined" && document.visibilityState === "hidden";
    if (keepOpen && this.isOpen() && !hidden) return;
    this.close();
  }

  // Closes the microphone (and the indicator) unless a recording is running.
  close() {
    if (this._active) return;
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
