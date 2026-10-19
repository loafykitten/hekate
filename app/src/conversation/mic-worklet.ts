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

// Messages in: { start: id } begins recording `id`; { flush: id } sends what's left of it and
// stops. Out: { id, pcm } chunks, and { flushed: id } once a recording is fully delivered. The
// id lets the page drop anything that belongs to a recording it no longer wants.
class PcmCapture extends AudioWorkletProcessor {
  private downsampler = new Downsampler(sampleRate, OUT_RATE);
  private chunker = this.newChunker(0);
  private recording: number | undefined;

  constructor() {
    super();
    this.port.onmessage = (e: MessageEvent<{ start?: number; flush?: number }>) => {
      const { start, flush } = e.data;
      if (start !== undefined) {
        // A fresh recording: nothing carried over from before.
        this.downsampler = new Downsampler(sampleRate, OUT_RATE);
        this.chunker = this.newChunker(start);
        this.recording = start;
      } else if (flush !== undefined) {
        if (this.recording === flush) {
          this.chunker.flush();
          this.recording = undefined;
        }
        this.port.postMessage({ flushed: flush });
      }
    };
  }

  private newChunker(id: number) {
    return new Chunker(CHUNK_SAMPLES, (chunk) => this.port.postMessage({ id, pcm: chunk.buffer }, [chunk.buffer]));
  }

  process(inputs: Float32Array[][]): boolean {
    const channel = inputs[0]?.[0];
    if (channel && this.recording !== undefined) this.chunker.push(this.downsampler.push(channel));
    return true;
  }
}

registerProcessor("hekate-pcm-capture", PcmCapture as unknown as new () => AudioWorkletProcessor);
