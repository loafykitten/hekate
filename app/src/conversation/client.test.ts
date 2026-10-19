import { beforeEach, describe, expect, it, vi } from "vitest";
import { backoffDelay, ConversationClient, DEFAULT_RECONNECT, defaultSocketUrl } from "./client";
import { FakeSocket, READY } from "./fakes";
import { parseServerMessage } from "./protocol";

function setup() {
  FakeSocket.reset();
  const timers: { fn: () => void; ms: number }[] = [];
  const client = new ConversationClient({
    url: "ws://test/api/ws",
    socketFactory: (url) => new FakeSocket(url),
    random: () => 0.5,
    setTimer: (fn, ms) => {
      timers.push({ fn, ms });
      return timers.length;
    },
    clearTimer: () => {},
  });
  return { client, timers };
}

function connected() {
  const ctx = setup();
  ctx.client.connect();
  const socket = FakeSocket.last;
  socket.open();
  socket.receive(READY);
  return { ...ctx, socket };
}

describe("backoffDelay", () => {
  it("grows exponentially with equal jitter and caps at the maximum", () => {
    const at = (attempt: number, r: number) => backoffDelay(attempt, DEFAULT_RECONNECT, () => r);
    expect(at(0, 0)).toBe(250); // base 500: half fixed...
    expect(at(0, 1)).toBe(500); // ...half random
    expect(at(1, 1)).toBe(1000);
    expect(at(3, 1)).toBe(4000);
    expect(at(20, 0)).toBe(7500);
    expect(at(20, 1)).toBe(15000);
  });
});

describe("defaultSocketUrl", () => {
  it("follows the page's scheme", () => {
    expect(defaultSocketUrl({ protocol: "https:", host: "h.example" })).toBe("wss://h.example/api/ws");
    expect(defaultSocketUrl({ protocol: "http:", host: "localhost:5173" })).toBe("ws://localhost:5173/api/ws");
  });
});

describe("parseServerMessage", () => {
  it("accepts known types and ignores everything else", () => {
    expect(parseServerMessage('{"type":"transcript","id":"1","text":"hi"}')).toMatchObject({ text: "hi" });
    expect(parseServerMessage('{"type":"from_the_future"}')).toBeUndefined();
    expect(parseServerMessage("not json")).toBeUndefined();
    expect(parseServerMessage("42")).toBeUndefined();
    expect(parseServerMessage(new ArrayBuffer(4))).toBeUndefined();
  });
});

describe("ConversationClient", () => {
  beforeEach(() => FakeSocket.reset());

  it("is connecting until ready arrives, then exposes the handshake", () => {
    const { client } = setup();
    const statuses: string[] = [];
    client.onStatus((s) => statuses.push(s));
    client.connect();
    expect(client.status).toBe("connecting");
    const socket = FakeSocket.last;
    expect(socket.binaryType).toBe("arraybuffer");
    socket.open();
    expect(client.status).toBe("connecting");
    socket.receive(READY);
    expect(client.status).toBe("open");
    expect(client.clientId).toBe("c1");
    expect(client.capabilities).toEqual({ stt: true, tts: true });
    expect(client.ready?.gestures).toEqual(["waving"]);
    expect(statuses).toEqual(["connecting", "open"]);
  });

  it("dispatches each message type to its subscribers and supports unsubscribing", () => {
    const { client, socket } = connected();
    const transcripts: string[] = [];
    const off = client.on("transcript", (m) => transcripts.push(m.text));
    socket.receive({ type: "transcript", id: "i1", text: "hello" });
    off();
    socket.receive({ type: "transcript", id: "i2", text: "again" });
    expect(transcripts).toEqual(["hello"]);
  });

  it("skips malformed and unknown frames without throwing", () => {
    const { client, socket } = connected();
    const spy = vi.fn();
    client.on("segment", spy);
    expect(() => {
      socket.receive("garbage");
      socket.receive({ type: "unknown_thing" });
    }).not.toThrow();
    expect(spy).not.toHaveBeenCalled();
  });

  it("sends say, ptt and interrupt in the documented shapes", () => {
    const { client, socket } = connected();
    const sayId = client.say("hi there");
    const pttId = client.pttStart();
    const chunk = new Int16Array([1, 2, 3]).buffer;
    client.pttAudio(chunk);
    client.pttEnd(pttId!);
    client.pttCancel("zzz");
    client.interrupt(2);
    expect(socket.json).toEqual([
      { type: "say", id: sayId, text: "hi there" },
      { type: "ptt_start", id: pttId, sample_rate: 16000 },
      { type: "ptt_end", id: pttId },
      { type: "ptt_cancel", id: "zzz" },
      { type: "interrupt", heard_seq: 2 },
    ]);
    expect(socket.sent[2]).toBe(chunk); // audio goes out as a binary frame, between start and end
  });

  it("refuses to send before ready and after the socket drops", () => {
    const { client } = setup();
    client.connect();
    const socket = FakeSocket.last;
    socket.open();
    expect(client.say("early")).toBeUndefined();
    expect(client.pttAudio(new ArrayBuffer(2))).toBe(true); // an open socket takes audio
    socket.receive(READY);
    socket.drop();
    expect(client.say("late")).toBeUndefined();
    expect(client.pttAudio(new ArrayBuffer(2))).toBe(false);
    expect(socket.json).toEqual([]);
  });

  it("interrupts the running turn, with the heard seq, before new input", () => {
    const { client, socket } = connected();
    client.getHeardSeq = () => 1;
    socket.receive({ type: "turn_start", turn_id: "t1", input_id: "i", origin: "c1", user_text: "x" });
    expect(client.turnActive).toBe(true);
    const id = client.say("actually, never mind");
    const types = socket.json.map((m) => m.type);
    expect(types).toEqual(["interrupt", "say"]);
    expect(socket.json[0]).toEqual({ type: "interrupt", heard_seq: 1 });
    expect(socket.json[1]).toMatchObject({ id });
    expect(client.turnActive).toBe(false);
    // The old turn's end arriving later doesn't disturb a newer turn.
    socket.receive({ type: "turn_start", turn_id: "t2", input_id: id, origin: "c1", user_text: "y" });
    socket.receive({ type: "turn_end", turn_id: "t1", status: "interrupted", text: "", model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } });
    expect(client.activeTurnId).toBe("t2");
  });

  it("does not interrupt when no turn is running, and push-to-talk interrupts like text", () => {
    const { client, socket } = connected();
    client.say("one");
    expect(socket.json.map((m) => m.type)).toEqual(["say"]);
    socket.receive({ type: "turn_start", turn_id: "t1", input_id: "i", origin: "c2", user_text: "x" });
    client.pttStart();
    expect(socket.json.map((m) => m.type)).toEqual(["say", "interrupt", "ptt_start"]);
    expect(socket.json[1]).toEqual({ type: "interrupt", heard_seq: -1 });
  });

  it("reconnects with backoff, resets after ready, and expects a fresh ready", () => {
    const { client, socket, timers } = connected();
    socket.receive({ type: "turn_start", turn_id: "t1", input_id: "i", origin: "c1", user_text: "x" });
    socket.drop();
    expect(client.status).toBe("closed");
    expect(client.ready).toBeUndefined();
    expect(client.turnActive).toBe(false);
    expect(timers.map((t) => t.ms)).toEqual([375]); // attempt 0, jitter 0.5: 250 + 0.5 * 250

    timers[0].fn();
    const second = FakeSocket.last;
    expect(second).not.toBe(socket);
    second.drop(); // never became ready
    expect(timers.map((t) => t.ms)).toEqual([375, 750]);

    timers[1].fn();
    FakeSocket.last.open();
    FakeSocket.last.receive({ ...READY, client_id: "c9" });
    expect(client.clientId).toBe("c9");
    FakeSocket.last.drop();
    expect(timers[2].ms).toBe(375); // back to the first step
  });

  it("stops reconnecting after disconnect() and can skip the wait with reconnectNow()", () => {
    const { client, socket, timers } = connected();
    socket.drop();
    expect(FakeSocket.instances).toHaveLength(1);
    client.reconnectNow();
    expect(FakeSocket.instances).toHaveLength(2);
    FakeSocket.last.drop();
    client.disconnect();
    timers.at(-1)!.fn();
    expect(FakeSocket.instances).toHaveLength(2); // the stale timer did nothing
    expect(client.status).toBe("closed");
  });
});
