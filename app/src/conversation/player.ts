import type { Bubble } from "../character/bubble";
import type { Character } from "../character/character";
import type { Speech } from "../character/speech";
import type { Segment } from "./protocol";
import {
  base64ToBytes,
  blendExpressions,
  evenWords,
  expressionEnd,
  expressionProfile,
  sortByAt,
  splitRevealed,
  takeDue,
  type ActiveExpression,
  type Word,
} from "./schedule";

export interface PlayerHost {
  speech: Speech;
  bubble?: Bubble;
  // The character, once loaded.
  character: () => Character | undefined;
}

export interface PlayerOptions {
  gapSeconds?: number; // silence between segments
  lingerSeconds?: number; // how long the last line stays up after she finishes
  tickMs?: number;
}

interface Item {
  turnId: string;
  seq: number;
  text: string;
  start: number; // on the Speech clock
  end: number;
  words: Word[];
  gestures: Segment["gestures"];
  expressions: Segment["expressions"];
  gestureCursor: number;
  expressionCursor: number;
  started: boolean;
}

const LEAD = 0.03;
// An item that's been over this long when we first look at it (a backgrounded tab) is skipped
// silently instead of replaying its gestures.
const STALE = 0.75;

// Performs the segments of turns this client started: decodes the audio, queues it back to
// back in seq order, reveals the bubble word by word, fires gestures and expressions on time and
// remembers what was heard so an interrupt can say so. The mouth follows the audio through Speech.
export class SegmentPlayer {
  private readonly gap: number;
  private readonly linger: number;
  private readonly tickMs: number;
  private items: Item[] = [];
  private active: ActiveExpression[] = [];
  private chain: Promise<void> = Promise.resolve();
  private pending = 0;
  private epoch = 0;
  private turnId?: string;
  private nextSeq = 0;
  private turnOver = false;
  private heard = { turnId: "", seq: -1 };
  private timer?: ReturnType<typeof setInterval>;
  private lingerUntil?: number;
  private listeners = new Set<() => void>();
  private lastBusy = false;

  constructor(
    private readonly host: PlayerHost,
    options: PlayerOptions = {},
  ) {
    this.gap = options.gapSeconds ?? 0.12;
    this.linger = options.lingerSeconds ?? 3.5;
    this.tickMs = options.tickMs ?? 33;
  }

  onChange(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  // Segments are queued and performed in the order they arrive, which is seq order on the
  // socket. A repeat or out-of-date seq is dropped.
  enqueue(segment: Segment) {
    if (segment.turn_id !== this.turnId) {
      this.turnId = segment.turn_id;
      this.nextSeq = 0;
      this.turnOver = false;
    }
    if (segment.seq < this.nextSeq) return;
    this.nextSeq = segment.seq + 1;
    this.lingerUntil = undefined;
    this.pending++;
    const epoch = this.epoch;
    this.chain = this.chain
      .then(async () => {
        const buffer = await this.decode(segment);
        if (epoch === this.epoch) this.schedule(segment, buffer);
      })
      .finally(() => {
        this.pending--;
        this.notify();
      });
    this.notify();
  }

  // The turn is over: nothing more will arrive, so the last line can fade once it's been said.
  finishTurn(turnId: string) {
    if (turnId !== this.turnId) return;
    this.turnOver = true;
    if (!this.busy) this.startLinger();
  }

  // Another device interrupted this turn: stop if it's what she's still saying.
  stopTurn(turnId: string) {
    if (turnId === this.turnId && (this.busy || !this.turnOver)) this.stop();
  }

  // Silence her now (the user interrupted, or the connection dropped).
  stop() {
    this.epoch++;
    this.host.speech.stop();
    this.items = [];
    this.active = [];
    this.turnOver = true;
    this.lingerUntil = undefined;
    this.host.bubble?.hide();
    this.host.character()?.setExpression({});
    this.stopTimer();
    this.notify();
  }

  dispose() {
    this.stop();
    this.listeners.clear();
  }

  // Seq of the last segment of `turnId` the user heard at least partly, or -1.
  heardSeq(turnId: string | undefined): number {
    return turnId !== undefined && this.heard.turnId === turnId ? this.heard.seq : -1;
  }

  // Segments are decoding, queued or playing.
  get busy(): boolean {
    return this.pending > 0 || this.items.length > 0;
  }

  private async decode(segment: Segment): Promise<AudioBuffer | undefined> {
    const { speech } = this.host;
    if (!segment.audio_b64 || !speech.running || speech.muted) return undefined;
    try {
      const bytes = base64ToBytes(segment.audio_b64);
      const buffer = await speech.decode(bytes.buffer as ArrayBuffer);
      return speech.muted ? undefined : buffer; // muted while it decoded: show it as text
    } catch (e) {
      console.warn("segment audio could not be decoded; showing it as text", e);
      return undefined;
    }
  }

  private schedule(segment: Segment, buffer: AudioBuffer | undefined) {
    const { speech } = this.host;
    const last = this.items[this.items.length - 1];
    const notBefore = last ? last.end + this.gap : 0;
    let start: number;
    let end: number;
    if (buffer) {
      ({ start, end } = speech.enqueue(buffer, this.gap, notBefore));
    } else {
      start = Math.max(speech.now() + LEAD, notBefore);
      end = start + Math.max(0.3, segment.duration);
    }
    const duration = end - start;
    const hasTimings = buffer && segment.words && segment.words.length > 0;
    this.items.push({
      turnId: segment.turn_id,
      seq: segment.seq,
      text: segment.text,
      start,
      end,
      words: hasTimings ? segment.words! : evenWords(segment.text, duration),
      gestures: sortByAt(segment.gestures),
      expressions: sortByAt(segment.expressions),
      gestureCursor: 0,
      expressionCursor: 0,
      started: false,
    });
    this.startTimer();
    this.notify();
  }

  private startTimer() {
    this.timer ??= setInterval(() => this.tick(), this.tickMs);
  }

  private stopTimer() {
    if (this.timer !== undefined) clearInterval(this.timer);
    this.timer = undefined;
  }

  private startLinger() {
    this.lingerUntil = this.host.speech.now() + this.linger;
    this.startTimer();
  }

  private tick() {
    const { speech, bubble } = this.host;
    const now = speech.position;
    const character = this.host.character();

    for (const item of this.items) {
      if (now < item.start) break;
      const elapsed = now - item.start;
      if (!item.started) {
        item.started = true;
        this.heard = { turnId: item.turnId, seq: item.seq };
      }
      if (now >= item.end + STALE) {
        item.gestureCursor = item.gestures.length;
        item.expressionCursor = item.expressions.length;
        continue;
      }
      const g = takeDue(item.gestures, item.gestureCursor, elapsed);
      item.gestureCursor = g.cursor;
      for (const gesture of g.due) character?.playGesture(gesture.name);
      const e = takeDue(item.expressions, item.expressionCursor, elapsed);
      item.expressionCursor = e.cursor;
      for (const expression of e.due) {
        const profile = expressionProfile(expression.name);
        if (profile) this.active.push({ profile, startedAt: item.start + expression.at });
      }
      if (now < item.end) {
        const { shown, pending } = splitRevealed(item.words, elapsed);
        bubble?.setText(shown, pending);
      } else {
        bubble?.setText(item.words.map((w) => w.text).join(" "));
      }
    }
    // Lines that are finished drop out; the last one stays until it has been said.
    this.items = this.items.filter((item, i, all) => i === all.length - 1 || now < item.end);
    const lastItem = this.items[this.items.length - 1];
    if (lastItem && now >= lastItem.end && this.pending === 0) {
      this.items = [];
      if (this.turnOver) this.startLinger();
    }

    this.active = this.active.filter((a) => now < expressionEnd(a.profile, a.startedAt));
    const blend = blendExpressions(this.active, now);
    character?.setExpression(blend.weights, blend.eyesClosed);

    if (this.lingerUntil !== undefined && !this.busy && speech.now() >= this.lingerUntil) {
      this.lingerUntil = undefined;
      bubble?.hide();
    }
    if (!this.busy && this.lingerUntil === undefined && this.active.length === 0 && this.turnOver) {
      character?.setExpression({});
      this.stopTimer();
    }
    this.notify();
  }

  private notify() {
    const busy = this.busy;
    if (busy === this.lastBusy) return;
    this.lastBusy = busy;
    for (const listener of [...this.listeners]) listener();
  }
}
