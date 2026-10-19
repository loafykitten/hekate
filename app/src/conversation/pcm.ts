// Audio helpers for push-to-talk: float samples at the device rate in, 16 kHz little-endian
// Int16 chunks out. Pure and framework-free, so the AudioWorklet and the tests share it.
// (Int16Array uses the platform's byte order, which is little-endian on every browser target.)

export function floatToInt16(x: number): number {
  const clamped = x > 1 ? 1 : x < -1 ? -1 : x;
  return Math.round(clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff);
}

// Streaming rate converter: averages the input over each output sample's span (a box filter,
// so it also removes most of what would alias going from 48 kHz to 16 kHz). Fractional ratios
// (44.1 kHz -> 16 kHz) are handled by splitting a sample's weight across two outputs.
export class Downsampler {
  private readonly ratio: number;
  private acc = 0;
  private filled = 0; // input samples' worth accumulated into the current output sample

  constructor(inRate: number, outRate: number) {
    if (!(inRate > 0) || !(outRate > 0)) throw new RangeError("sample rates must be positive");
    this.ratio = inRate / outRate;
  }

  // Converts one block; state carries over between blocks.
  push(input: Float32Array): Int16Array {
    const out = new Int16Array(Math.ceil(input.length / this.ratio) + 1);
    let n = 0;
    for (let i = 0; i < input.length; i++) {
      const x = input[i];
      let weight = 1;
      while (weight > 1e-9) {
        const need = this.ratio - this.filled;
        if (weight < need - 1e-9) {
          this.acc += x * weight;
          this.filled += weight;
          weight = 0;
        } else {
          this.acc += x * need;
          weight -= need;
          out[n++] = floatToInt16(this.acc / this.ratio);
          this.acc = 0;
          this.filled = 0;
        }
      }
    }
    return out.subarray(0, n);
  }
}

// Gathers samples into fixed-size chunks (640 samples = 40 ms at 16 kHz).
export class Chunker {
  private buf: Int16Array;
  private len = 0;

  constructor(
    private readonly size: number,
    private readonly emit: (chunk: Int16Array) => void,
  ) {
    this.buf = new Int16Array(size);
  }

  push(samples: Int16Array) {
    let offset = 0;
    while (offset < samples.length) {
      const take = Math.min(this.size - this.len, samples.length - offset);
      this.buf.set(samples.subarray(offset, offset + take), this.len);
      this.len += take;
      offset += take;
      if (this.len === this.size) {
        this.emit(this.buf);
        this.buf = new Int16Array(this.size); // the emitted one may be transferred away
        this.len = 0;
      }
    }
  }

  // Emits whatever is left as a short chunk.
  flush() {
    if (this.len === 0) return;
    this.emit(this.buf.slice(0, this.len));
    this.len = 0;
  }
}

// 0..1 input level for the UI ring from one chunk: RMS, a small gate under room noise, and a
// square-root curve so ordinary speech moves the ring visibly.
export function pcmLevel(samples: Int16Array): number {
  if (!samples.length) return 0;
  let sum = 0;
  for (const s of samples) sum += (s / 0x8000) ** 2;
  const rms = Math.sqrt(sum / samples.length);
  return Math.min(1, Math.sqrt(Math.max(0, rms - 0.006)) * 2.6);
}
