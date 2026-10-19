// Runs inside the AudioWorklet: downsamples the microphone to 16 kHz mono Int16 and posts
// ~40 ms chunks to the main thread. Bundled separately by Vite (see mic.ts).
import { Chunker, Downsampler } from "./pcm";

declare const sampleRate: number;
declare class AudioWorkletProcessor {
  readonly port: MessagePort;
}
declare function registerProcessor(name: string, processor: new () => AudioWorkletProcessor): void;

const OUT_RATE = 16000;
const CHUNK_SAMPLES = 640; // 40 ms

class PcmCapture extends AudioWorkletProcessor {
  private downsampler = new Downsampler(sampleRate, OUT_RATE);
  private chunker = new Chunker(CHUNK_SAMPLES, (chunk) => this.port.postMessage(chunk.buffer, [chunk.buffer]));

  constructor() {
    super();
    this.port.onmessage = (e: MessageEvent) => {
      if (e.data === "flush") {
        this.chunker.flush();
        this.port.postMessage("flushed");
      }
    };
  }

  process(inputs: Float32Array[][]): boolean {
    const channel = inputs[0]?.[0];
    if (channel) this.chunker.push(this.downsampler.push(channel));
    return true;
  }
}

registerProcessor("hekate-pcm-capture", PcmCapture as unknown as new () => AudioWorkletProcessor);
