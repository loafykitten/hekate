import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationClient } from "./client";
import { Conversation, TAP_MS, type MicLike } from "./conversation";
import { FakeSocket, READY } from "./fakes";
import { MicError } from "./mic";

function setup(opts: { startImpl?: () => Promise<boolean>; ready?: object } = {}) {
  FakeSocket.reset();
  let clock = 1000;
  const mic = {
    level: 0,
    start: vi.fn(opts.startImpl ?? (async () => true)),
    stop: vi.fn(async () => {}),
  } satisfies MicLike;
  let onChunk: (chunk: ArrayBuffer) => void = () => {};
  const client = new ConversationClient({ url: "ws://test/api/ws", socketFactory: (url) => new FakeSocket(url), setTimer: () => 0, clearTimer: () => {} });
  const conversation = new Conversation({
    client,
    now: () => clock,
    createMic: (cb) => {
      onChunk = cb;
      return mic;
    },
  });
  conversation.start();
  const socket = FakeSocket.last;
  socket.open();
  socket.receive(opts.ready ?? READY);
  return {
    conversation,
    socket,
    mic,
    chunk: (c: ArrayBuffer) => onChunk(c),
    tick: (ms: number) => (clock += ms),
    state: () => conversation.getState(),
    types: () => socket.json.map((m) => m.type),
  };
}

const turnEnd = (turn_id: string, text = "") => ({
  type: "turn_end",
  turn_id,
  status: "done",
  text,
  model: "m",
  usage: { input_tokens: 1, output_tokens: 1, cost_usd: null },
});
const seg = (turn_id: string, seq: number, text: string) => ({
  type: "segment",
  turn_id,
  seq,
  text,
  duration: 1,
  audio_b64: null,
  audio_format: null,
  words: null,
  gestures: [],
  expressions: [],
});

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("Conversation: typed input", () => {
  it("walks idle -> thinking -> speaking -> idle and records the turn in the live log", () => {
    const t = setup();
    expect(t.state().phase).toBe("idle");
    expect(t.state().capabilities).toEqual({ stt: true, tts: true });
    expect(t.conversation.sendText("  hello  ")).toBe(true);
    expect(t.socket.json[0]).toMatchObject({ type: "say", text: "hello" });
    expect(t.state().phase).toBe("thinking");
    expect(t.state().caption?.text).toBe("hello");

    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "hello" });
    expect(t.state().phase).toBe("thinking");
    t.socket.receive({ type: "activity", turn_id: "t1", tool: "calendar", label: "Checking your calendar", state: "start" });
    expect(t.state().activity).toBe("Checking your calendar");
    t.socket.receive({ type: "activity", turn_id: "t1", tool: "calendar", label: "Checking your calendar", state: "done" });
    expect(t.state().activity).toBeUndefined();

    t.socket.receive(seg("t1", 0, "Morning."));
    t.socket.receive(seg("t1", 1, "Slept well?"));
    expect(t.state().phase).toBe("speaking");
    expect(t.state().live.map((e) => e.text)).toEqual(["hello", "Morning. Slept well?"]);

    t.socket.receive(turnEnd("t1", "Morning. Slept well?"));
    expect(t.state().phase).toBe("idle");
    expect(t.state().live.every((e) => e.done && e.endEpoch !== undefined)).toBe(true);
  });

  it("ignores blank text and warns when offline", () => {
    const t = setup();
    expect(t.conversation.sendText("   ")).toBe(false);
    t.socket.drop();
    expect(t.conversation.sendText("hi")).toBe(false);
    expect(t.state().toasts.map((x) => x.key)).toContain("offline-send");
  });

  it("interrupts a running turn first, then sends", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "a" });
    t.conversation.sendText("never mind");
    expect(t.types()).toEqual(["interrupt", "say"]);
    // The interrupted turn's late segments are not performed (and nothing breaks).
    t.socket.receive(seg("t1", 0, "late"));
    expect(t.state().phase).toBe("thinking"); // waiting on the new input
  });

  it("shows another client's turn in the log without taking it over", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t9", input_id: "x", origin: "other", user_text: "from my laptop" });
    t.socket.receive(seg("t9", 0, "Hello laptop."));
    expect(t.state().phase).toBe("idle");
    expect(t.state().live.map((e) => e.text)).toEqual(["from my laptop", "Hello laptop."]);
    expect(t.state().caption?.text).toBe("from my laptop");
  });

  it("turns server errors into toasts and frees a waiting input", () => {
    const t = setup();
    t.conversation.sendText("hi");
    const id = t.socket.json[0].id;
    t.socket.receive({ type: "error", code: "llm_unavailable", message: "I can't think right now.", input_id: id });
    expect(t.state().phase).toBe("idle");
    expect(t.state().toasts[0]).toMatchObject({ kind: "error", text: "I can't think right now." });
  });
});

describe("Conversation: push-to-talk", () => {
  it("holds: start, stream chunks, release sends ptt_end after the mic flushed", async () => {
    const t = setup();
    t.conversation.pressTalk();
    expect(t.types()).toEqual(["ptt_start"]);
    expect(t.socket.json[0]).toMatchObject({ sample_rate: 16000 });
    expect(t.state().phase).toBe("listening");
    await vi.advanceTimersByTimeAsync(0); // mic.start resolves

    const audio = new Int16Array([1, 2]).buffer;
    t.chunk(audio);
    expect(t.socket.sent.at(-1)).toBe(audio);

    t.tick(TAP_MS + 100);
    t.conversation.releaseTalk();
    expect(t.state().phase).toBe("transcribing");
    expect(t.mic.stop).toHaveBeenCalledOnce();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.types()).toEqual(["ptt_start", "ptt_end"]);
    expect(t.socket.json[1].id).toBe(t.socket.json[0].id);

    t.socket.receive({ type: "transcript", id: t.socket.json[0].id, text: "what's on today" });
    expect(t.state().caption?.text).toBe("what's on today");
    expect(t.state().phase).toBe("thinking");
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: t.socket.json[0].id, origin: "c1", user_text: "what's on today" });
    expect(t.state().phase).toBe("thinking");
  });

  it("a quick tap latches listening, and the next tap sends", async () => {
    const t = setup();
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.tick(80);
    t.conversation.releaseTalk();
    expect(t.state()).toMatchObject({ phase: "listening", latched: true });
    expect(t.mic.stop).not.toHaveBeenCalled();

    t.tick(2000);
    t.conversation.pressTalk(); // second tap ends it
    t.conversation.releaseTalk(); // and its release is ignored
    expect(t.state().phase).toBe("transcribing");
    await vi.advanceTimersByTimeAsync(0);
    expect(t.types()).toEqual(["ptt_start", "ptt_end"]);
  });

  it("an empty transcript ends quietly with a note", async () => {
    const t = setup();
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.tick(500);
    t.conversation.releaseTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.socket.receive({ type: "transcript", id: t.socket.json[0].id, text: "" });
    expect(t.state().phase).toBe("idle");
    expect(t.state().toasts.map((x) => x.text)).toContain("I didn't catch that.");
  });

  it("pressing while she is working interrupts her first", async () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "a" });
    t.socket.receive(seg("t1", 0, "One."));
    t.conversation.pressTalk();
    expect(t.types()).toEqual(["interrupt", "ptt_start"]);
    expect(t.state().phase).toBe("listening");
    await vi.advanceTimersByTimeAsync(0);
  });

  it("cancels the recording when the mic is blocked", async () => {
    const t = setup({
      startImpl: async () => {
        throw new MicError("denied", "Microphone access is blocked.");
      },
    });
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.types()).toEqual(["ptt_start", "ptt_cancel"]);
    expect(t.state().phase).toBe("idle");
    expect(t.state().toasts[0].text).toContain("Microphone access is blocked");
  });

  it("discards a hold that ended before the mic was ready", async () => {
    let finish: (ok: boolean) => void = () => {};
    const t = setup({ startImpl: () => new Promise<boolean>((r) => (finish = r)) });
    t.conversation.pressTalk();
    t.tick(900); // a long press, still waiting on the permission prompt
    t.conversation.releaseTalk();
    expect(t.types()).toEqual(["ptt_start", "ptt_cancel"]);
    expect(t.state().phase).toBe("idle");
    finish(false);
    await vi.advanceTimersByTimeAsync(0);
  });

  it("explains instead of recording when speech-to-text is unavailable or the socket is down", () => {
    const off = setup({ ready: { ...READY, capabilities: { stt: false, tts: false } } });
    off.conversation.pressTalk();
    expect(off.types()).toEqual([]);
    expect(off.state().toasts[0].text).toMatch(/Voice input/);

    const down = setup();
    down.socket.drop();
    down.conversation.pressTalk();
    expect(down.state().toasts.map((x) => x.key)).toContain("offline-talk");
  });

  it("drops an in-progress recording when the connection is lost", async () => {
    const t = setup();
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.socket.drop();
    expect(t.state().phase).toBe("idle");
    expect(t.mic.stop).toHaveBeenCalled();
    expect(t.state().toasts.map((x) => x.key)).toContain("connection");
  });
});

describe("Conversation: connection", () => {
  it("clears the connection toast and live entries on a fresh ready, and asks for history again", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "other", user_text: "x" });
    const epoch = t.state().historyEpoch;
    t.socket.drop();
    expect(t.state().toasts.map((x) => x.key)).toContain("connection");
    t.conversation.client.reconnectNow();
    FakeSocket.last.open();
    FakeSocket.last.receive(READY);
    expect(t.state().toasts.map((x) => x.key)).not.toContain("connection");
    expect(t.state().live).toEqual([]);
    expect(t.state().historyEpoch).toBeGreaterThan(epoch);
  });
});

describe("Conversation: capabilities", () => {
  it("enables voice input when the service reports speech-to-text became ready", () => {
    const t = setup({ ready: { ...READY, capabilities: { stt: false, tts: true } } });
    expect(t.state().capabilities?.stt).toBe(false);
    t.socket.receive({ type: "capabilities", capabilities: { stt: true, tts: true } });
    expect(t.state().capabilities).toEqual({ stt: true, tts: true });
  });
});
