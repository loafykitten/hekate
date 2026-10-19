import type { SocketLike } from "./client";

// Test double for WebSocket: records what the client sends and lets a test push frames in.
export class FakeSocket implements SocketLike {
  static instances: FakeSocket[] = [];
  readyState = 0;
  binaryType = "blob";
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  sent: (string | ArrayBuffer | ArrayBufferView)[] = [];
  closed = false;

  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }

  static reset() {
    FakeSocket.instances = [];
  }

  static get last(): FakeSocket {
    return FakeSocket.instances[FakeSocket.instances.length - 1];
  }

  send(data: string | ArrayBuffer | ArrayBufferView) {
    this.sent.push(data);
  }

  close() {
    this.closed = true;
    this.readyState = 3;
  }

  // Test helpers
  open() {
    this.readyState = 1;
    this.onopen?.({});
  }

  receive(message: unknown) {
    this.onmessage?.({ data: typeof message === "string" ? message : JSON.stringify(message) });
  }

  drop() {
    this.readyState = 3;
    this.onclose?.({});
  }

  get json(): Record<string, unknown>[] {
    return this.sent.filter((s): s is string => typeof s === "string").map((s) => JSON.parse(s) as Record<string, unknown>);
  }
}

export const READY = {
  type: "ready",
  client_id: "c1",
  conversation_id: "conv1",
  capabilities: { stt: true, tts: true },
  gestures: ["waving"],
};
