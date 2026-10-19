import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Card } from "../data/model";
import { CARD_TTL_MS, CardDeck, EXIT_MS, MAX_CARDS } from "./deck";

const card = (kind: "memory" | "tasks", id: string): Card =>
  kind === "memory"
    ? { kind, id, title: id, data: { items: [] } }
    : { kind, id, title: id, data: { items: [], highlight: [] } };

const ids = (deck: CardDeck) => deck.active.map((e) => e.card.id);

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("CardDeck", () => {
  it("shows the newest first and keeps at most MAX_CARDS", () => {
    const onChange = vi.fn();
    const deck = new CardDeck(onChange);
    for (const id of ["a", "b", "c", "d"]) deck.add(card("memory", id), "turn");
    expect(ids(deck)).toEqual(["d", "c", "b"]);
    expect(deck.active).toHaveLength(MAX_CARDS);
    // The oldest is still there, leaving, until its exit animation is done.
    expect(deck.items.at(-1)).toMatchObject({ leaving: true });
    vi.advanceTimersByTime(EXIT_MS);
    expect(deck.items).toHaveLength(MAX_CARDS);
  });

  it("replaces a card with the same kind and id at once", () => {
    const deck = new CardDeck(() => {});
    deck.add(card("tasks", "1"), "turn");
    const first = deck.items[0].key;
    deck.add(card("tasks", "1"), "turn");
    expect(deck.items).toHaveLength(1);
    expect(deck.items[0].key).not.toBe(first);
  });

  it("expires cards after the TTL, each on its own clock", () => {
    const deck = new CardDeck(() => {});
    deck.add(card("memory", "a"), "turn");
    vi.advanceTimersByTime(CARD_TTL_MS - 1000);
    deck.add(card("memory", "b"), "notify");
    vi.advanceTimersByTime(1000);
    expect(ids(deck)).toEqual(["b"]);
    vi.advanceTimersByTime(EXIT_MS + CARD_TTL_MS);
    expect(deck.items).toEqual([]);
  });

  it("dismisses one card or all of them, and ignores unknown keys", () => {
    const onChange = vi.fn();
    const deck = new CardDeck(onChange);
    deck.add(card("memory", "a"), "turn");
    deck.add(card("memory", "b"), "turn");
    onChange.mockClear();
    deck.dismiss("nope");
    expect(onChange).not.toHaveBeenCalled();
    deck.dismiss(deck.items[0].key);
    expect(ids(deck)).toEqual(["a"]);
    deck.clear();
    expect(ids(deck)).toEqual([]);
    deck.clear();
    expect(onChange).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(EXIT_MS);
    expect(deck.items).toEqual([]);
    expect(onChange).toHaveBeenCalledTimes(4);
  });
});
