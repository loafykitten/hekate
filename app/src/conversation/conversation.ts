import type { Stage } from "../character/stage";
import type { Bubble } from "../character/bubble";
import { ConversationClient, type ConnectionStatus } from "./client";
import type { LiveEntry } from "./log";
import { MicCapture, MicError, classifyMicError, setAudioSession } from "./mic";
import { SegmentPlayer } from "./player";
import { MAX_TEXT_LENGTH, type Capabilities, type Segment, type TurnEnd } from "./protocol";

// The conversation as the UI sees it: owns the socket, the segment player and the microphone,
// and turns their events into one immutable state snapshot (for useSyncExternalStore). Framework
// free. The one place that knows how input, interruption and playback fit together.

export type Phase = "idle" | "listening" | "transcribing" | "thinking" | "speaking";

export interface Toast {
  id: number;
  key: string;
  kind: "error" | "info";
  text: string;
}

export interface ConversationState {
  connection: ConnectionStatus;
  // Set once connected; reset when the connection drops.
  capabilities?: Capabilities;
  phase: Phase;
  // Tap-to-latch listening (as opposed to holding the button).
  latched: boolean;
  caption?: { id: number; text: string };
  // The tool she's running right now, e.g. "Checking your calendar".
  activity?: string;
  toasts: Toast[];
  live: LiveEntry[];
  // Bumps whenever server-side history may have changed (connect, turn end).
  historyEpoch: number;
}

// A press shorter than this latches listening on instead of ending it on release.
export const TAP_MS = 250;
const PENDING_TIMEOUT_MS = 60_000;
const MAX_LIVE = 40; // live entries kept for the log while history hasn't caught up

interface Capture {
  id: string;
  pressedAt: number;
  released: boolean;
  latched: boolean;
  ready: boolean;
}

type Pending = { id: string; stage: "transcript" | "turn" };

export interface ConversationDeps {
  client?: ConversationClient;
  createMic?: (onChunk: (chunk: ArrayBuffer) => void) => MicLike;
  now?: () => number;
}

export interface MicLike {
  start(): Promise<boolean>;
  stop(): Promise<void>;
  readonly level: number;
}

export class Conversation {
  readonly client: ConversationClient;
  private readonly mic: MicLike;
  private readonly now: () => number;
  private player?: SegmentPlayer;
  private stage?: Stage;
  private state: ConversationState = {
    connection: "closed",
    phase: "idle",
    latched: false,
    toasts: [],
    live: [],
    historyEpoch: 0,
  };
  private capabilities?: Capabilities;
  private live: LiveEntry[] = [];
  private historyEpoch = 0;
  private listeners = new Set<() => void>();
  private unsubscribers: (() => void)[] = [];

  private capture?: Capture;
  private streamingId?: string; // ptt id whose audio chunks are being sent
  private pressOwnsCapture = false;
  private pending?: Pending;
  private pendingTimer?: ReturnType<typeof setTimeout>;
  private myTurn?: string; // turn this client started, until its turn_end
  private lastMyTurn?: string;
  private spoke = false;
  private running = new Map<string, string>(); // tool -> label of the tools running now
  private caption?: { id: number; text: string };
  private captionTimer?: ReturnType<typeof setTimeout>;
  private toasts: Toast[] = [];
  private toastTimers = new Map<number, ReturnType<typeof setTimeout>>();
  private toastSeq = 0;
  private everOpen = false;

  constructor(deps: ConversationDeps = {}) {
    this.client = deps.client ?? new ConversationClient();
    this.now = deps.now ?? (() => performance.now());
    this.mic = (deps.createMic ?? ((onChunk) => new MicCapture(onChunk)))((chunk) => this.onMicChunk(chunk));
    this.client.getHeardSeq = () => this.player?.heardSeq(this.lastMyTurn) ?? -1;
  }

  // --- lifecycle -------------------------------------------------------------------------

  start() {
    const c = this.client;
    this.unsubscribers = [
      c.onStatus((s) => this.onStatus(s)),
      c.on("ready", (m) => this.onReady(m.capabilities)),
      c.on("transcript", (m) => this.onTranscript(m.id, m.text)),
      c.on("turn_start", (m) => this.onTurnStart(m.turn_id, m.origin, m.user_text)),
      c.on("activity", (m) => this.onActivity(m.tool, m.label, m.state)),
      c.on("segment", (m) => this.onSegment(m)),
      c.on("capabilities", (m) => this.onCapabilities(m.capabilities)),
      c.on("turn_end", (m) => this.onTurnEnd(m.turn_id, m.text, m.status)),
      c.on("error", (m) => this.onError(m.code, m.message, m.input_id)),
    ];
    c.connect();
    this.emit();
  }

  stop() {
    this.unsubscribers.forEach((u) => u());
    this.unsubscribers = [];
    this.client.disconnect();
    this.dropInFlight();
    for (const t of this.toastTimers.values()) clearTimeout(t);
    this.toastTimers.clear();
    clearTimeout(this.captionTimer);
  }

  // The scene is ready (or gone): segments are performed through its speech, bubble and character.
  attachStage(stage: Stage | undefined) {
    this.player?.dispose();
    this.player = undefined;
    this.stage = stage;
    if (stage) {
      const host = { speech: stage.speech, bubble: stage.bubble as Bubble | undefined, character: () => stage.character };
      this.player = new SegmentPlayer(host);
      this.player.onChange(() => this.emit());
    }
    this.emit();
  }

  // --- state for the UI ------------------------------------------------------------------

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getState = (): ConversationState => this.state;

  // 0..1 microphone level while listening.
  micLevel(): number {
    return this.mic.level;
  }

  get connected(): boolean {
    return this.client.status === "open";
  }

  // --- input -----------------------------------------------------------------------------

  // Call from a user gesture (it unlocks audio playback for the replies to come).
  sendText(text: string): boolean {
    const trimmed = text.trim();
    if (!trimmed) return false;
    this.unlockAudio();
    if (trimmed.length > MAX_TEXT_LENGTH) {
      this.toast("error", `That's too long to send (${MAX_TEXT_LENGTH} characters at most).`, "too-long");
      return false;
    }
    if (!this.connected) {
      this.toast("error", "Not connected to Hekate yet.", "offline-send");
      return false;
    }
    this.interruptIfBusy();
    const id = this.client.say(trimmed);
    if (!id) return false;
    this.setPending({ id, stage: "turn" });
    this.setCaption(trimmed);
    this.emit();
    return true;
  }

  // Press of the talk button (or Space). Starts push-to-talk, ends a latched one, or interrupts
  // her first. Call synchronously from the event handler: iOS needs the gesture.
  pressTalk() {
    this.unlockAudio();
    if (this.capture?.latched) {
      this.pressOwnsCapture = false;
      this.finishCapture();
      return;
    }
    if (this.capture || this.pending?.stage === "transcript") return;
    if (!this.connected) {
      this.toast("error", "Not connected to Hekate yet.", "offline-talk");
      return;
    }
    if (this.capabilities && !this.capabilities.stt) {
      this.toast("error", "Voice input isn't available right now. You can still type.", "stt-off");
      return;
    }
    this.interruptIfBusy();
    const id = this.client.pttStart();
    if (!id) return;
    const capture: Capture = { id, pressedAt: this.now(), released: false, latched: false, ready: false };
    this.capture = capture;
    this.streamingId = id;
    this.pressOwnsCapture = true;
    this.clearPending();
    this.mic.start().then(
      (ok) => {
        if (!ok) return;
        capture.ready = true;
        if (this.capture === capture && capture.released && !capture.latched) {
          // Let go while the permission prompt or the device was still opening.
          this.cancelCapture();
        }
        this.emit();
      },
      (e: unknown) => {
        if (this.capture !== capture) return;
        const err = e instanceof MicError ? e : classifyMicError(e);
        this.cancelCapture();
        this.toast("error", err.message, `mic-${err.kind}`);
      },
    );
    this.emit();
  }

  // Release of the talk button (or Space).
  releaseTalk() {
    const capture = this.capture;
    if (!capture || !this.pressOwnsCapture) return;
    this.pressOwnsCapture = false;
    capture.released = true;
    if (this.now() - capture.pressedAt < TAP_MS) {
      capture.latched = true;
      this.emit();
    } else {
      this.finishCapture();
    }
  }

  // The press was taken away from us (OS interruption): discard rather than send half a thought.
  abortTalk() {
    if (this.capture && this.pressOwnsCapture) {
      this.pressOwnsCapture = false;
      this.cancelCapture();
    }
  }

  cancelCapture() {
    const capture = this.capture;
    if (!capture) return;
    this.capture = undefined;
    this.pressOwnsCapture = false;
    this.streamingId = undefined;
    void this.mic.stop();
    this.client.pttCancel(capture.id);
    this.emit();
  }

  // Esc and the like: stop whatever she's saying, or discard a recording.
  interruptNow() {
    if (this.capture) this.cancelCapture();
    else if (this.interruptIfBusy()) this.emit();
  }

  private finishCapture() {
    const capture = this.capture;
    if (!capture) return;
    if (!capture.ready) {
      this.cancelCapture();
      return;
    }
    this.capture = undefined;
    this.setPending({ id: capture.id, stage: "transcript" });
    this.emit();
    void this.mic.stop().then(() => {
      this.streamingId = undefined;
      this.client.pttEnd(capture.id);
    });
  }

  private onMicChunk(chunk: ArrayBuffer) {
    if (this.streamingId) this.client.pttAudio(chunk);
  }

  // New input while she's thinking or talking: stop that first and tell the server what was heard.
  private interruptIfBusy(): boolean {
    if (!this.client.turnActive && !this.player?.busy && !this.myTurn) return false;
    // Only report what was heard of a turn this device played; another device's running turn is
    // interrupted by the server when our input arrives.
    if (!this.client.turnActive || this.client.activeTurnMine) this.client.interrupt(this.client.getHeardSeq());
    this.player?.stop();
    this.myTurn = undefined;
    this.spoke = false;
    this.running.clear();
    return true;
  }

  private unlockAudio() {
    setAudioSession("playback"); // iOS: play through the speaker and ignore the silent switch
    void this.stage?.speech.unlock();
  }

  // --- server events ---------------------------------------------------------------------

  private onStatus(status: ConnectionStatus) {
    if (status === "open") this.everOpen = true;
    if (status === "closed") {
      this.dropInFlight();
      this.toast(
        "error",
        this.everOpen ? "Connection lost. Reconnecting…" : "Can't reach Hekate. Retrying…",
        "connection",
        true,
      );
    }
    this.emit();
  }

  private onReady(capabilities: Capabilities) {
    this.dismissToast("connection");
    this.dismissToast("offline-send");
    this.dismissToast("offline-talk");
    // Anything still live was lost with the old connection; history has what was saved.
    this.capabilities = capabilities;
    this.live = [];
    this.historyEpoch++;
    this.emit();
  }

  private onTranscript(id: string, text: string) {
    const mine = this.pending?.id === id;
    if (!text.trim()) {
      if (mine) {
        this.clearPending();
        this.toast("info", "I didn't catch that.", "empty-transcript");
      }
      return this.emit();
    }
    this.setCaption(text);
    if (mine) this.setPending({ id, stage: "turn" });
    this.emit();
  }

  private onTurnStart(turnId: string, origin: string, userText: string) {
    const mine = origin === this.client.clientId;
    if (mine) {
      this.myTurn = this.lastMyTurn = turnId;
      this.spoke = false;
      this.clearPending();
    } else if (this.player?.busy || this.myTurn) {
      // Another device started a turn: whatever this one was still saying is superseded.
      this.player?.stop();
      this.myTurn = undefined;
    }
    if (!this.caption || this.caption.text !== userText) this.setCaption(userText);
    this.running.clear();
    this.live = [
      ...this.live,
      { id: `u:${turnId}`, turnId, role: "user" as const, text: userText, done: true },
      { id: `a:${turnId}`, turnId, role: "assistant" as const, text: "", done: false },
    ].slice(-MAX_LIVE);
    this.emit();
  }

  private onActivity(tool: string, label: string, state: "start" | "done" | "error") {
    if (state === "start") this.running.set(tool, label);
    else this.running.delete(tool);
    this.emit();
  }

  private onSegment(segment: Segment) {
    this.live = this.live.map((e) =>
      e.id === `a:${segment.turn_id}` ? { ...e, text: e.text ? `${e.text} ${segment.text}` : segment.text } : e,
    );
    if (segment.turn_id === this.myTurn) {
      this.spoke = true;
      this.player?.enqueue(segment);
    }
    this.emit();
  }

  private onCapabilities(capabilities: Capabilities) {
    this.capabilities = capabilities;
    this.emit();
  }

  private onTurnEnd(turnId: string, text: string, status: TurnEnd["status"]) {
    // Interrupted from any device (possibly after generation finished, while audio was queued
    // here): stop saying it.
    if (status === "interrupted") this.player?.stopTurn(turnId);
    this.running.clear();
    this.historyEpoch++;
    const endEpoch = this.historyEpoch;
    this.live = this.live.map((e) => {
      if (e.turnId !== turnId) return e;
      return e.role === "assistant" ? { ...e, text: text || e.text, done: true, endEpoch } : { ...e, done: true, endEpoch };
    });
    if (turnId === this.myTurn) {
      this.myTurn = undefined;
      this.player?.finishTurn(turnId);
    }
    this.emit();
  }

  private onError(code: string, message: string, inputId?: string) {
    if (this.pending && (inputId === undefined || inputId === this.pending.id)) this.clearPending();
    if (inputId !== undefined && this.capture?.id === inputId) this.cancelCapture();
    this.toast("error", message || "Something went wrong.", `error-${code}`);
    this.emit();
  }

  // The connection is gone: nothing in flight is coming back.
  private dropInFlight() {
    if (this.capture) {
      this.capture = undefined;
      this.streamingId = undefined;
      this.pressOwnsCapture = false;
      void this.mic.stop();
    }
    this.clearPending();
    this.myTurn = undefined;
    this.spoke = false;
    this.running.clear();
    this.player?.stop();
    this.capabilities = undefined;
  }

  // --- bookkeeping -----------------------------------------------------------------------

  private setPending(p: Pending) {
    this.pending = p;
    clearTimeout(this.pendingTimer);
    this.pendingTimer = setTimeout(() => {
      if (this.pending?.id !== p.id) return;
      this.pending = undefined;
      this.toast("error", "Hekate didn't answer. Try again.", "pending-timeout");
      this.emit();
    }, PENDING_TIMEOUT_MS);
  }

  private clearPending() {
    this.pending = undefined;
    clearTimeout(this.pendingTimer);
  }

  private setCaption(text: string) {
    const id = (this.caption?.id ?? 0) + 1;
    this.caption = { id, text };
    clearTimeout(this.captionTimer);
    this.captionTimer = setTimeout(
      () => {
        if (this.caption?.id !== id) return;
        this.caption = undefined;
        this.emit();
      },
      Math.min(9000, 3500 + text.length * 40),
    );
  }

  toast(kind: Toast["kind"], text: string, key: string, sticky = false) {
    const existing = this.toasts.find((t) => t.key === key);
    const id = existing?.id ?? ++this.toastSeq;
    this.toasts = [...this.toasts.filter((t) => t.key !== key), { id, key, kind, text }].slice(-3);
    clearTimeout(this.toastTimers.get(id));
    this.toastTimers.delete(id);
    if (!sticky) this.toastTimers.set(id, setTimeout(() => this.dismissToast(key), kind === "error" ? 6000 : 3500));
    this.emit();
  }

  dismissToast(key: string) {
    const t = this.toasts.find((x) => x.key === key);
    if (!t) return;
    clearTimeout(this.toastTimers.get(t.id));
    this.toastTimers.delete(t.id);
    this.toasts = this.toasts.filter((x) => x !== t);
    this.emit();
  }

  private phase(): Phase {
    if (this.capture) return "listening";
    if (this.pending?.stage === "transcript") return "transcribing";
    if (this.pending) return "thinking";
    if (this.myTurn) return this.spoke || this.player?.busy ? "speaking" : "thinking";
    return this.player?.busy ? "speaking" : "idle";
  }

  private emit() {
    const next: ConversationState = {
      ...this.state,
      connection: this.client.status,
      phase: this.phase(),
      latched: !!this.capture?.latched,
      caption: this.caption,
      activity: [...this.running.values()].at(-1),
      toasts: this.toasts,
      live: this.live,
      capabilities: this.capabilities,
      historyEpoch: this.historyEpoch,
    };
    const s = this.state;
    const same =
      s.connection === next.connection &&
      s.phase === next.phase &&
      s.latched === next.latched &&
      s.caption === next.caption &&
      s.activity === next.activity &&
      s.toasts === next.toasts &&
      s.live === next.live &&
      s.capabilities === next.capabilities &&
      s.historyEpoch === next.historyEpoch;
    if (same) return;
    this.state = next;
    for (const listener of [...this.listeners]) listener();
  }
}
