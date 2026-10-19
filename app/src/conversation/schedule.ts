// Pure scheduling helpers for performing a segment: which words are showing at time t, which
// gestures and expressions have come due, and the face envelope of an expression. Times are
// seconds into the segment unless noted.

export interface Word {
  text: string;
  start: number;
  end: number;
}

// Word timings for text with no audio: the words share `duration` in proportion to their
// length (plus a little for the pause a word costs), ending exactly at `duration`.
export function evenWords(text: string, duration: number): Word[] {
  const tokens = text.split(/\s+/).filter(Boolean);
  if (!tokens.length) return [];
  const weights = tokens.map((t) => t.length + 1.5);
  const total = weights.reduce((a, b) => a + b, 0);
  const words: Word[] = [];
  let at = 0;
  tokens.forEach((token, i) => {
    const span = (weights[i] / total) * Math.max(0, duration);
    words.push({ text: token, start: at, end: at + span });
    at += span;
  });
  return words;
}

// How many words are showing at time t: a word appears when it starts.
export function revealedWordCount(words: readonly Word[], t: number): number {
  let lo = 0;
  let hi = words.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (words[mid].start <= t) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// The words shown so far and the rest, for a bubble that reserves space for the whole line.
export function splitRevealed(words: readonly Word[], t: number): { shown: string; pending: string } {
  const n = revealedWordCount(words, t);
  const join = (ws: readonly Word[]) => ws.map((w) => w.text).join(" ");
  const pending = join(words.slice(n));
  return { shown: join(words.slice(0, n)), pending: pending ? ` ${pending}` : "" };
}

export interface Timed {
  at: number;
}

export function sortByAt<T extends Timed>(events: readonly T[] | null | undefined): T[] {
  return [...(events ?? [])].sort((a, b) => a.at - b.at);
}

// Events (sorted by `at`) that have come due by time t and weren't taken yet. `cursor` is how
// many were taken before; pass the returned cursor next time.
export function takeDue<T extends Timed>(events: readonly T[], cursor: number, t: number): { due: T[]; cursor: number } {
  let next = cursor;
  while (next < events.length && events[next].at <= t) next++;
  return { due: events.slice(cursor, next), cursor: next };
}

// What an ElevenLabs expression tag does to the face, using morphs the character already has.
// Weights are peak values; eyesClosed is how far the lids come down (0 open, 1 shut).
export interface ExpressionProfile {
  weights: Record<string, number>;
  eyesClosed: number;
  duration: number; // seconds the face holds before releasing
}

const LAUGH: ExpressionProfile = { weights: { eyeSquint: 0.7, mouthSmile: 0.8 }, eyesClosed: 0, duration: 1.3 };
const SIGH: ExpressionProfile = { weights: { browDown: 0.6 }, eyesClosed: 0.3, duration: 1.6 };
const SOFT: ExpressionProfile = { weights: { mouthSmile: 0.35 }, eyesClosed: 0, duration: 2.2 };

// Whispers change the voice, not the face, so they (and unknown tags) return undefined.
export function expressionProfile(name: string): ExpressionProfile | undefined {
  const n = name.toLowerCase();
  if (/whisper/.test(n)) return undefined;
  if (/laugh|chuckl|giggl|snicker/.test(n)) return LAUGH;
  if (/sigh|exhale/.test(n)) return SIGH;
  if (/soft|warm|gentl|smil|tender/.test(n)) return SOFT;
  return undefined;
}

const ATTACK = 0.15;
const RELEASE = 0.5;

// 0..1 over an expression's life: eases in, holds for `duration`, eases out.
export function expressionEnvelope(elapsed: number, duration: number): number {
  if (elapsed < 0 || elapsed >= duration + RELEASE) return 0;
  const ease = (x: number) => x * x * (3 - 2 * x);
  if (elapsed < ATTACK) return ease(elapsed / ATTACK);
  if (elapsed <= duration) return 1;
  return ease(1 - (elapsed - duration) / RELEASE);
}

export function expressionEnd(profile: ExpressionProfile, startedAt: number): number {
  return startedAt + profile.duration + RELEASE;
}

export interface ActiveExpression {
  profile: ExpressionProfile;
  startedAt: number; // absolute time on the player's clock
}

// Blends the running expressions at time `now`: the strongest wins per morph, never summing past
// what one expression asks for, so overlapping laughs don't distort the face.
export function blendExpressions(active: readonly ActiveExpression[], now: number): { weights: Record<string, number>; eyesClosed: number } {
  const weights: Record<string, number> = {};
  let eyesClosed = 0;
  for (const { profile, startedAt } of active) {
    const e = expressionEnvelope(now - startedAt, profile.duration);
    if (e <= 0) continue;
    for (const [name, peak] of Object.entries(profile.weights)) weights[name] = Math.max(weights[name] ?? 0, peak * e);
    eyesClosed = Math.max(eyesClosed, profile.eyesClosed * e);
  }
  return { weights, eyesClosed };
}

export function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}
