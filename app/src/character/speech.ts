const ENVELOPE_RATE = 100; // frames per second

// Loudness per 10 ms frame, scaled so the utterance's loud syllables sit near 1.
// Normalizing per utterance keeps the jaw independent of how loud a given TTS voice is.
function loudnessEnvelope(buffer: AudioBuffer): Float32Array {
  const data = buffer.getChannelData(0);
  const hop = Math.max(1, Math.round(buffer.sampleRate / ENVELOPE_RATE));
  const env = new Float32Array(Math.ceil(data.length / hop));
  for (let f = 0; f < env.length; f++) {
    let sum = 0;
    const end = Math.min(data.length, (f + 1) * hop);
    for (let i = f * hop; i < end; i++) sum += data[i] * data[i];
    env[f] = Math.sqrt(sum / (end - f * hop));
  }
  const voiced = env.filter((v) => v > 0.005).sort();
  const reference = voiced.length ? voiced[Math.floor(voiced.length * 0.9)] : 1;
  for (let f = 0; f < env.length; f++) env[f] /= reference;
  return env;
}

interface Scheduled {
  start: number; // AudioContext time the source starts (and, minus output latency, is first heard)
  end: number;
  envelope: Float32Array;
  source: AudioBufferSourceNode;
}

export interface Enqueued {
  start: number;
  end: number;
  // Resolves when the source finishes (or is cut off by stop()).
  done: Promise<void>;
}

// Plays speech audio and exposes its loudness so the mouth can follow it. Buffers can be
// queued back to back (with a gap), and `position` is the clock the queue runs on.
export class Speech {
  private ctx = new AudioContext();
  private queue: Scheduled[] = []; // in AudioContext time
  private _muted = false;
  // The clock is the context's currentTime plus drift while it follows the audio, and runs on
  // the wall clock from its last reading while it doesn't (see now()). drift only moves when it
  // starts following the audio again, so the clock never jumps.
  private drift = 0;
  private last?: { wall: number; clock: number; free: boolean };

  // Browsers start audio suspended until a user gesture; call this from one, before any await.
  async unlock(): Promise<boolean> {
    if (this._muted) return false;
    if (this.ctx.state !== "running") {
      try {
        await this.ctx.resume();
      } catch {
        return false;
      }
    }
    return this.ctx.state === "running";
  }

  get running(): boolean {
    return this.ctx.state === "running";
  }

  // Her voice is off on this device: queued audio stops and the context is suspended, so the
  // device's other audio (music, a call) carries on undisturbed. Replies run as text on the wall
  // clock. Unmuting takes an unlock() from a user gesture before audio plays again.
  get muted(): boolean {
    return this._muted;
  }

  set muted(on: boolean) {
    if (on === this._muted) return;
    this._muted = on;
    if (!on) return;
    this.stop();
    void this.ctx.suspend().catch(() => {});
  }

  decode(data: ArrayBuffer): Promise<AudioBuffer> {
    return this.ctx.decodeAudioData(data);
  }

  // Plays one buffer now, replacing whatever was playing. Resolves when it ends.
  play(buffer: AudioBuffer): Promise<void> {
    this.stop();
    return this.enqueue(buffer).done;
  }

  // Schedules a buffer after everything already queued, `gap` seconds after the last one ends
  // (and not before `notBefore`, on the same clock).
  enqueue(buffer: AudioBuffer, gap = 0, notBefore = 0): Enqueued {
    this.prune(); // also brings drift up to date
    const last = this.queue[this.queue.length - 1];
    const start = Math.max(this.ctx.currentTime + 0.03, notBefore - this.drift, last ? last.end + gap : 0);
    const end = start + buffer.duration;
    const source = this.ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(this.ctx.destination);
    const item: Scheduled = { start, end, envelope: loudnessEnvelope(buffer), source };
    this.queue.push(item);
    const done = new Promise<void>((resolve) => {
      source.onended = () => resolve();
    });
    source.start(start);
    return { start: start + this.drift, end: end + this.drift, done };
  }

  stop() {
    for (const item of this.queue) {
      try {
        item.source.stop();
      } catch {
        // already ended
      }
    }
    this.queue = [];
  }

  // Releases the audio context (browsers cap how many can be open).
  dispose() {
    this.stop();
    void this.ctx.close().catch(() => {});
  }

  // The clock everything is scheduled on. It follows the audio while the context runs, or while
  // audio is queued: if the context is suspended mid-reply (a call, the app backgrounded), the
  // clock freezes with the audio, so text and gestures pause with what's heard. With nothing to
  // hear (never unlocked, muted, or suspended between replies) it runs on the wall clock, so
  // text-only replies keep their pace.
  now(): number {
    const wall = performance.now() / 1000;
    const free = !this.running && !this.queue.some((i) => i.end > this.ctx.currentTime);
    let clock: number;
    if (free) {
      clock = this.last ? this.last.clock + (wall - this.last.wall) : wall;
    } else {
      if (this.last?.free) this.drift = this.last.clock + (wall - this.last.wall) - this.ctx.currentTime;
      clock = this.ctx.currentTime + this.drift;
    }
    this.last = { wall, clock, free };
    return clock;
  }

  // What the listener is hearing now, on the same clock as Enqueued.start/end. The source node
  // finishes up to outputLatency earlier (notably on Bluetooth), so everything keys off this.
  // Safari doesn't implement outputLatency.
  get position(): number {
    return this.now() - (this.running ? (this.ctx.outputLatency ?? 0) : 0);
  }

  // position in AudioContext time, which the queue is kept in.
  private heardAt(): number {
    const at = this.position;
    return at - this.drift;
  }

  private current(): Scheduled | undefined {
    const at = this.heardAt();
    return this.queue.find((i) => at >= i.start && at < i.end);
  }

  private prune() {
    const at = this.heardAt() - 1;
    this.queue = this.queue.filter((i) => i.end > at);
  }

  // Something is playing or still queued.
  get speaking(): boolean {
    const last = this.queue[this.queue.length - 1];
    return !!last && this.heardAt() < last.end;
  }

  // 0..1 through the buffer being heard now.
  get progress(): number {
    const item = this.current();
    return item ? (this.heardAt() - item.start) / (item.end - item.start) : 0;
  }

  // Normalized loudness of what's audible right now: ~1 on loud syllables, 0 in gaps.
  level(): number {
    const item = this.current();
    if (!item) return 0;
    const frame = Math.floor((this.heardAt() - item.start) * ENVELOPE_RATE);
    return frame >= 0 && frame < item.envelope.length ? item.envelope[frame] : 0;
  }
}
