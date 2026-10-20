// Mouth shapes timed to speech. The service reads them from the voice's per-character timing as
// viseme classes (sil, PP, FF, TH, DD, kk, CH, SS, nn, RR, aa, E, I, O, U); the manifest maps
// each class to this character's morphs. The track holds the shapes of the lines queued to play,
// on the Speech clock, and answers which classes are showing at a given moment.

export interface Viseme {
  v: string;
  at: number; // seconds into its line
}

// One shape eases into the next over this long, as lips move between sounds.
export const VISEME_BLEND = 0.07;
// The lips get there a moment before the sound does.
export const VISEME_LEAD = 0.04;

// The classes showing `t` seconds into a line, as strengths that sum to at most 1: the current
// shape blending in over the one before it. Silence (and before the first) shows nothing.
export function visemeBlend(visemes: readonly Viseme[], t: number): Record<string, number> {
  let i = -1;
  while (i + 1 < visemes.length && visemes[i + 1].at <= t) i++;
  if (i < 0) return {};
  const out: Record<string, number> = {};
  const f = Math.min(1, (t - visemes[i].at) / VISEME_BLEND);
  const add = (v: string, w: number) => {
    if (v !== "sil" && w > 0) out[v] = (out[v] ?? 0) + w;
  };
  add(visemes[i].v, f);
  if (i > 0) add(visemes[i - 1].v, 1 - f);
  return out;
}

interface Line {
  start: number;
  end: number;
  visemes: readonly Viseme[];
}

export class LipTrack {
  private lines: Line[] = [];

  // A line scheduled to play from start to end (Speech clock).
  add(start: number, end: number, visemes: readonly Viseme[]) {
    if (visemes.length === 0) return;
    this.lines.push({ start, end, visemes: [...visemes].sort((a, b) => a.at - b.at) });
  }

  clear() {
    this.lines = [];
  }

  // The shapes showing at time `now` (Speech clock), or undefined when no timed line is playing:
  // then the mouth follows loudness alone.
  at(now: number): Record<string, number> | undefined {
    const t = now + VISEME_LEAD;
    this.lines = this.lines.filter((l) => t < l.end + VISEME_BLEND);
    const line = this.lines.find((l) => t >= l.start);
    return line ? visemeBlend(line.visemes, t - line.start) : undefined;
  }
}
