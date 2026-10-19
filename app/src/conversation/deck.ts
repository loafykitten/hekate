import type { Card } from "../data/model";

// The cards on screen beside her. Framework free: the conversation feeds it, the UI reads it.
// At most MAX_CARDS show, newest first. A card goes on swipe or close, when the next turn
// starts, or after CARD_TTL_MS. A card that goes stays in the list as `leaving` for EXIT_MS so
// the UI can play its exit animation.

export const MAX_CARDS = 3;
export const CARD_TTL_MS = 45_000;
export const EXIT_MS = 220;

export interface DeckEntry {
  // Unique per arrival, so a card shown twice (same id) animates in again.
  key: string;
  source: "turn" | "notify";
  card: Card;
  leaving: boolean;
}

export interface DeckTimers {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

const realTimers: DeckTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

export class CardDeck {
  private entries: DeckEntry[] = [];
  private timers = new Map<string, unknown>();
  private seq = 0;

  constructor(
    private readonly onChange: () => void,
    private readonly clock: DeckTimers = realTimers,
    private readonly ttlMs = CARD_TTL_MS,
  ) {}

  // Everything on screen, leaving cards included.
  get items(): readonly DeckEntry[] {
    return this.entries;
  }

  // The cards that are staying.
  get active(): DeckEntry[] {
    return this.entries.filter((e) => !e.leaving);
  }

  // A newer card with the same kind and id replaces the older one (a refreshed list).
  add(card: Card, source: DeckEntry["source"]) {
    for (const old of this.entries.filter((e) => e.card.kind === card.kind && e.card.id === card.id)) this.remove(old.key);
    const entry: DeckEntry = { key: `${card.kind}:${card.id}:${++this.seq}`, source, card, leaving: false };
    this.entries = [entry, ...this.entries];
    this.setTimer(entry.key, () => this.dismiss(entry.key), this.ttlMs);
    for (const extra of this.active.slice(MAX_CARDS)) this.leave(extra.key);
    this.onChange();
  }

  dismiss(key: string) {
    if (this.leave(key)) this.onChange();
  }

  clear() {
    let changed = false;
    for (const e of this.active) changed = this.leave(e.key) || changed;
    if (changed) this.onChange();
  }

  private leave(key: string): boolean {
    const entry = this.entries.find((e) => e.key === key);
    if (!entry || entry.leaving) return false;
    this.entries = this.entries.map((e) => (e === entry ? { ...e, leaving: true } : e));
    this.setTimer(key, () => this.remove(key, true), EXIT_MS);
    return true;
  }

  private remove(key: string, notify = false) {
    this.forget(key);
    const before = this.entries.length;
    this.entries = this.entries.filter((e) => e.key !== key);
    if (notify && this.entries.length !== before) this.onChange();
  }

  private setTimer(key: string, fn: () => void, ms: number) {
    this.forget(key);
    this.timers.set(key, this.clock.set(fn, ms));
  }

  private forget(key: string) {
    if (!this.timers.has(key)) return;
    this.clock.clear(this.timers.get(key));
    this.timers.delete(key);
  }
}
