// Runs inside the AudioWorklet: downsamples the microphone to 16 kHz mono Int16 and posts
// ~40 ms chunks to the main thread. Bundled separately by Vite (see mic.ts).
//
// It records only between "start" and "flush". The microphone may stay open between
// recordings (so the next press is instant); audio from then is dropped right here and never
// reaches the page.
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
  private chunker = this.newChunker();
  private recording = false;

  constructor() {
    super();
    this.port.onmessage = (e: MessageEvent) => {
      if (e.data === "start") {
        // A fresh recording: nothing carried over from before.
        this.downsampler = new Downsampler(sampleRate, OUT_RATE);
        this.chunker = this.newChunker();
        this.recording = true;
      } else if (e.data === "flush") {
        if (this.recording) this.chunker.flush();
        this.recording = false;
        this.port.postMessage("flushed");
      }
    };
  }

  private newChunker() {
    return new Chunker(CHUNK_SAMPLES, (chunk) => this.port.postMessage(chunk.buffer, [chunk.buffer]));
  }

  process(inputs: Float32Array[][]): boolean {
    const channel = inputs[0]?.[0];
    if (channel && this.recording) this.chunker.push(this.downsampler.push(channel));
    return true;
  }
}

registerProcessor("hekate-pcm-capture", PcmCapture as unknown as new () => AudioWorkletProcessor);
