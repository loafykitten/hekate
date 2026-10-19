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

// Plays synthesized speech and exposes its loudness so the mouth can follow it.
export class Speech {
  private ctx = new AudioContext();
  private current?: AudioBufferSourceNode;
  private active = false; // an utterance is playing or still draining through output latency
  private envelope: Float32Array = new Float32Array(0);
  private startedAt = 0;
  private duration = 0;

  // Browsers start audio suspended until a user gesture; call this from one, before any await.
  async unlock(): Promise<boolean> {
    if (this.ctx.state !== "running") {
      try {
        await this.ctx.resume();
      } catch {
        return false;
      }
    }
    return this.ctx.state === "running";
  }

  async synthesize(text: string): Promise<AudioBuffer> {
    const res = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
    if (!res.ok) throw new Error(`TTS failed: ${res.status} ${await res.text()}`);
    return this.ctx.decodeAudioData(await res.arrayBuffer());
  }

  // Resolves when the source finishes (or is interrupted by the next utterance).
  play(buffer: AudioBuffer): Promise<void> {
    this.stop();
    const source = this.ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(this.ctx.destination);
    this.envelope = loudnessEnvelope(buffer);
    this.current = source;
    this.active = true;
    this.startedAt = this.ctx.currentTime;
    this.duration = buffer.duration;
    return new Promise((resolve) => {
      source.onended = () => {
        if (this.current === source) this.current = undefined;
        resolve();
      };
      source.start();
    });
  }

  stop() {
    this.current?.stop();
    this.current = undefined;
    this.active = false;
  }

  // Seconds into the utterance that the listener is hearing now. The source node finishes
  // up to outputLatency earlier (notably on Bluetooth), so everything keys off this instead.
  private heard() {
    return this.ctx.currentTime - this.startedAt - this.ctx.outputLatency;
  }

  get speaking() {
    return this.active && this.heard() < this.duration;
  }

  // 0..1 through the current utterance, as heard.
  get progress() {
    if (!this.active || !this.duration) return 0;
    return Math.min(1, Math.max(0, this.heard() / this.duration));
  }

  // Normalized loudness of what's audible right now: ~1 on loud syllables, 0 in gaps.
  level(): number {
    if (!this.speaking) return 0;
    const frame = Math.floor(this.heard() * ENVELOPE_RATE);
    return frame >= 0 && frame < this.envelope.length ? this.envelope[frame] : 0;
  }
}
