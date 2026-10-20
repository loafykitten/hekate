import type { Stage } from "../character/stage";
import type { Bubble } from "../character/bubble";
import { ConversationClient, type ConnectionStatus } from "./client";
import type { LiveEntry } from "./log";
import { MicCapture, MicError, classifyMicError, defaultMicOptions, setAudioSession } from "./mic";
import { SegmentPlayer } from "./player";
import { asApproval, parseCard, type Approval } from "../data/model";
import { setZone } from "../data/time";
import { CardDeck, type DeckEntry } from "./deck";
import { MAX_TEXT_LENGTH, type Capabilities, type Notify, type Segment, type TurnEnd } from "./protocol";

// The conversation as the UI sees it: owns the socket, the segment player and the microphone,
// and turns their events into one immutable state snapshot (for useSyncExternalStore). Framework
// free. The one place that knows how input, interruption and playback fit together.

// "starting": pressed, but the microphone is still opening and nothing is recorded yet.
export type Phase = "idle" | "starting" | "listening" | "transcribing" | "thinking" | "speaking";

export interface Toast {
  id: number;
  key: string;
  kind: "error" | "info";
  text: string;
}

// A `notify` event: shown as a banner even with no turn running.
export interface Notice {
  id: string;
  title: string;
  body: string;
  // The menu section it belongs to, when it has one.
  section?: "reminders" | "today" | "changes";
}

function noticeSection(kind: string): Notice["section"] {
  return kind === "reminder" ? "reminders" : kind === "approval" ? "today" : kind === "change" ? "changes" : undefined;
}

export const NOTICE_MS = 12_000;
export const ACK_GESTURE = "acknowledging";
export const VOICE_KEY = "hekate.voice";

type PrefStorage = Pick<Storage, "getItem" | "setItem">;

function browserStorage(): PrefStorage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined; // blocked (private window, site data off)
  }
}

export interface ConversationState {
  connection: ConnectionStatus;
  // Set once connected; reset when the connection drops.
  capabilities?: Capabilities;
  phase: Phase;
  // The press that started listening has been let go; the next press ends it.
  latched: boolean;
  caption?: { id: number; text: string };
  // The tool she's running right now, e.g. "Checking your calendar".
  activity?: string;
  toasts: Toast[];
  notices: Notice[];
  // Cards beside her, newest first.
  cards: readonly DeckEntry[];
  live: LiveEntry[];
  // Bumps whenever server-side history may have changed (connect, turn end).
  historyEpoch: number;
  // Her voice on this device. Off, she answers in the bubble only.
  voice: boolean;
}

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
  // Where per-device preferences are kept (localStorage by default).
  storage?: PrefStorage;
}

export interface MicLike {
  // Optional warm-up before the first press, so a press waits only for the microphone itself.
  prepare?(): void;
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
    notices: [],
    cards: [],
    live: [],
    historyEpoch: 0,
    voice: true,
  };
  private readonly storage?: PrefStorage;
  private voice = true;
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
  private readonly deck = new CardDeck(() => this.emit());
  private notices: Notice[] = [];
  private noticeTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private invalidateListeners = new Set<(domains: string[]) => void>();
  private approvalListeners = new Set<(approval: Approval) => void>();

  constructor(deps: ConversationDeps = {}) {
    this.client = deps.client ?? new ConversationClient();
    this.now = deps.now ?? (() => performance.now());
    this.storage = "storage" in deps ? deps.storage : browserStorage();
    try {
      this.voice = this.storage?.getItem(VOICE_KEY) !== "off";
    } catch {
      // unreadable: her voice stays on
    }
    this.state = { ...this.state, voice: this.voice };
    this.mic = (deps.createMic ?? ((onChunk) => new MicCapture(onChunk, defaultMicOptions())))((chunk) => this.onMicChunk(chunk));
    this.client.getHeardSeq = () => this.player?.heardSeq(this.lastMyTurn) ?? -1;
    this.client.getHeardTurn = () => this.lastMyTurn;
  }

  // --- lifecycle -------------------------------------------------------------------------

  start() {
    const c = this.client;
    this.unsubscribers = [
      c.onStatus((s) => this.onStatus(s)),
      c.on("ready", (m) => this.onReady(m.capabilities, m.timezone)),
      c.on("capabilities", (m) => this.onCapabilities(m.capabilities)),
      c.on("transcript", (m) => this.onTranscript(m.id, m.text, m.superseded)),
      c.on("turn_start", (m) => this.onTurnStart(m.turn_id, m.origin, m.user_text)),
      c.on("activity", (m) => this.onActivity(m.tool, m.label, m.state)),
      c.on("segment", (m) => this.onSegment(m)),
      c.on("turn_end", (m) => this.onTurnEnd(m.turn_id, m.text, m.status)),
      c.on("card", (m) => this.onCard(m.turn_id, m.card)),
      c.on("notify", (m) => this.onNotify(m)),
      c.on("invalidate", (m) => this.emitInvalidate(m.domains)),
      c.on("approval", (m) => this.onApproval(m.approval)),
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
    for (const t of this.noticeTimers.values()) clearTimeout(t);
    this.noticeTimers.clear();
    this.deck.clear();
    clearTimeout(this.captionTimer);
  }

  // The scene is ready (or gone): segments are performed through its speech, bubble and character.
  attachStage(stage: Stage | undefined) {
    this.player?.dispose();
    this.player = undefined;
    this.stage = stage;
    if (stage) {
      stage.speech.muted = !this.voice;
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

  // Called with the menu domains whose data changed on the server. A fresh connection reports
  // every domain, since changes made while it was down were missed.
  onInvalidate(listener: (domains: string[]) => void): () => void {
    this.invalidateListeners.add(listener);
    return () => this.invalidateListeners.delete(listener);
  }

  // Called when an approval was created or changed, on any device.
  onApprovalChange(listener: (approval: Approval) => void): () => void {
    this.approvalListeners.add(listener);
    return () => this.approvalListeners.delete(listener);
  }

  dismissCard(key: string) {
    this.deck.dismiss(key);
  }

  dismissNotice(id: string) {
    clearTimeout(this.noticeTimers.get(id));
    this.noticeTimers.delete(id);
    if (!this.notices.some((n) => n.id === id)) return;
    this.notices = this.notices.filter((n) => n.id !== id);
    this.emit();
  }

  // Turns her voice on or off on this device, and remembers it here. Off, she answers in the
  // bubble at reading speed (gestures and faces included) and the service skips speech for turns
  // this device starts; anything she's saying stops being heard at once. Call from a user
  // gesture: turning it back on unlocks audio.
  setVoice(on: boolean) {
    if (on === this.voice) return;
    this.voice = on;
    try {
      this.storage?.setItem(VOICE_KEY, on ? "on" : "off");
    } catch {
      // storage unavailable: it just isn't remembered
    }
    if (this.stage) this.stage.speech.muted = !on;
    if (on) this.unlockAudio();
    this.emit();
  }

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
    const id = this.client.say(trimmed, !this.voice);
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

  // Release of the talk button (or Space). Talking is a toggle: the press started it, and it
  // keeps listening until the next press ends it.
  releaseTalk() {
    const capture = this.capture;
    if (!capture || !this.pressOwnsCapture) return;
    this.pressOwnsCapture = false;
    capture.released = true;
    capture.latched = true;
    this.emit();
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
      this.client.pttEnd(capture.id, !this.voice);
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
    if (!this.voice) return; // nothing to play: leave the device's audio alone
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

  private onReady(capabilities: Capabilities, timezone?: string) {
    // Show times in the zone the service schedules in, not the device's.
    if (timezone) setZone({ tz: timezone });
    this.dismissToast("connection");
    this.dismissToast("offline-send");
    this.dismissToast("offline-talk");
    // Anything still live was lost with the old connection; history has what was saved.
    this.capabilities = capabilities;
    if (capabilities.stt) this.mic.prepare?.();
    this.live = [];
    this.historyEpoch++;
    this.emit();
    this.emitInvalidate(["tasks", "reminders", "calendar", "memory", "weather", "mail", "approvals", "changes"]);
  }

  private onTranscript(id: string, text: string, superseded = false) {
    const mine = this.pending?.id === id;
    if (superseded) {
      if (mine) this.clearPending(); // something newer was sent; nothing to say about this one
      return this.emit();
    }
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
      // A newer reply of ours replaces whatever is still playing (its turn may have finished on
      // the server before this input arrived, so nothing else stops it).
      if (this.player?.busy) this.player.stop();
      this.myTurn = this.lastMyTurn = turnId;
      this.spoke = false;
      this.clearPending();
      this.deck.clear(); // the last answer's cards make way for this one's
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
    if (capabilities.stt) this.mic.prepare?.(); // speech-to-text finished loading: get the mic ready
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
      return e.role === "assistant" ? { ...e, text, done: true, endEpoch } : { ...e, done: true, endEpoch };
    });
    if (turnId === this.myTurn) {
      this.myTurn = undefined;
      this.player?.finishTurn(turnId);
    }
    this.emit();
  }

  // Only turns this client started show cards (the other device shows its own).
  private onCard(turnId: string, raw: unknown) {
    if (turnId !== this.myTurn) return;
    const card = parseCard(raw);
    if (card) this.deck.add(card, "turn");
  }

  private onNotify(m: Notify) {
    const card = m.card === undefined ? undefined : parseCard(m.card);
    if (card) this.deck.add(card, "notify");
    const notice: Notice = {
      id: m.id,
      title: m.title || "Hekate",
      body: m.body || "",
      section: noticeSection(m.kind),
    };
    clearTimeout(this.noticeTimers.get(notice.id));
    this.notices = [...this.notices.filter((n) => n.id !== notice.id), notice].slice(-2);
    this.noticeTimers.set(
      notice.id,
      setTimeout(() => this.dismissNotice(notice.id), NOTICE_MS),
    );
    // A soft nod, unless she's in the middle of talking.
    if (!this.player?.busy && this.client.ready?.gestures.includes(ACK_GESTURE)) this.stage?.character?.playGesture(ACK_GESTURE);
    this.emit();
  }

  private onApproval(raw: unknown) {
    const approval = asApproval(raw);
    if (approval) for (const listener of [...this.approvalListeners]) listener(approval);
  }

  private emitInvalidate(domains: string[]) {
    for (const listener of [...this.invalidateListeners]) listener(domains);
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
    if (this.capture) return this.capture.ready ? "listening" : "starting";
    if (this.pending?.stage === "transcript") return "transcribing";
    if (this.pending) return "thinking";
    if (this.myTurn) return this.spoke || this.player?.busy ? "speaking" : "thinking";
    return this.player?.busy ? "speaking" : "idle";
  }

  private emit() {
    if (this.stage) this.stage.idle = this.phase() === "idle";
    const next: ConversationState = {
      ...this.state,
      connection: this.client.status,
      phase: this.phase(),
      latched: !!this.capture?.latched,
      caption: this.caption,
      activity: [...this.running.values()].at(-1),
      toasts: this.toasts,
      notices: this.notices,
      cards: this.deck.items,
      live: this.live,
      capabilities: this.capabilities,
      historyEpoch: this.historyEpoch,
      voice: this.voice,
    };
    const s = this.state;
    const same =
      s.connection === next.connection &&
      s.phase === next.phase &&
      s.latched === next.latched &&
      s.caption === next.caption &&
      s.activity === next.activity &&
      s.toasts === next.toasts &&
      s.notices === next.notices &&
      s.cards === next.cards &&
      s.live === next.live &&
      s.capabilities === next.capabilities &&
      s.historyEpoch === next.historyEpoch &&
      s.voice === next.voice;
    if (same) return;
    this.state = next;
    for (const listener of [...this.listeners]) listener();
  }
}
