import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationClient } from "./client";
import { Conversation, type MicLike } from "./conversation";
import { FakeSocket, FakeSpeech, READY } from "./fakes";
import { MicError } from "./mic";

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return { data, getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
}

function setup(opts: { startImpl?: () => Promise<boolean>; ready?: object; storage?: ReturnType<typeof memoryStorage> } = {}) {
  FakeSocket.reset();
  let clock = 1000;
  const mic = {
    level: 0,
    start: vi.fn(opts.startImpl ?? (async () => true)),
    stop: vi.fn(async () => {}),
    prepare: vi.fn(),
  } satisfies MicLike;
  let onChunk: (chunk: ArrayBuffer) => void = () => {};
  const client = new ConversationClient({ url: "ws://test/api/ws", socketFactory: (url) => new FakeSocket(url), setTimer: () => 0, clearTimer: () => {} });
  const storage = opts.storage ?? memoryStorage();
  const conversation = new Conversation({
    client,
    storage,
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
    storage,
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
  it("toggles: a press starts and streams chunks, the next press sends ptt_end after the mic flushed", async () => {
    const t = setup();
    t.conversation.pressTalk();
    expect(t.types()).toEqual(["ptt_start"]);
    expect(t.socket.json[0]).toMatchObject({ sample_rate: 16000 });
    expect(t.state().phase).toBe("starting"); // nothing is recorded until the mic is open
    await vi.advanceTimersByTimeAsync(0); // mic.start resolves
    expect(t.state().phase).toBe("listening");

    const audio = new Int16Array([1, 2]).buffer;
    t.chunk(audio);
    expect(t.socket.sent.at(-1)).toBe(audio);

    t.tick(3000);
    t.conversation.releaseTalk(); // letting go doesn't end it, even after a long hold
    expect(t.state()).toMatchObject({ phase: "listening", latched: true });
    expect(t.mic.stop).not.toHaveBeenCalled();
    t.conversation.pressTalk(); // the next press does
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

  it("a quick tap starts listening, and the next tap sends", async () => {
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
    t.conversation.releaseTalk();
    t.tick(500);
    t.conversation.pressTalk();
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
    expect(t.state().phase).toBe("starting");
    await vi.advanceTimersByTimeAsync(0);
    expect(t.state().phase).toBe("listening");
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

  it("keeps starting through a release, and a second tap before the mic opens cancels", async () => {
    let finish: (ok: boolean) => void = () => {};
    const t = setup({ startImpl: () => new Promise<boolean>((r) => (finish = r)) });
    t.conversation.pressTalk();
    t.tick(900); // still waiting on the permission prompt
    t.conversation.releaseTalk();
    expect(t.state().phase).toBe("starting");
    t.conversation.pressTalk(); // changed their mind before anything was recorded
    t.conversation.releaseTalk();
    expect(t.types()).toEqual(["ptt_start", "ptt_cancel"]);
    expect(t.state().phase).toBe("idle");
    finish(false);
    await vi.advanceTimersByTimeAsync(0);
  });

  it("gets the mic ready once speech-to-text is available, even if it loads after connecting", () => {
    const later = setup({ ready: { ...READY, capabilities: { stt: false, tts: false } } });
    expect(later.mic.prepare).not.toHaveBeenCalled();
    later.socket.receive({ type: "capabilities", capabilities: { stt: true, tts: false } });
    expect(later.mic.prepare).toHaveBeenCalledOnce();

    const now = setup();
    expect(now.mic.prepare).toHaveBeenCalledOnce();
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

describe("Conversation: two devices", () => {
  it("new input during another device's reply doesn't send this device's heard position", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t9", input_id: "x", origin: "other-device", user_text: "from the phone" });
    t.socket.receive(seg("t9", 0, "Phone reply."));
    t.conversation.sendText("typed on the desktop");
    expect(t.types()).toEqual(["say"]);
  });

  it("still interrupts its own running turn with what was heard", () => {
    const t = setup();
    t.conversation.sendText("first");
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "first" });
    t.conversation.sendText("second");
    expect(t.types()).toEqual(["say", "interrupt", "say"]);
  });
});

describe("Conversation: superseded input and back-to-back turns", () => {
  it("a transcript superseded by another device's input just clears the wait", async () => {
    const t = setup();
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.conversation.releaseTalk();
    t.tick(500);
    t.conversation.pressTalk();
    t.conversation.releaseTalk();
    await vi.advanceTimersByTimeAsync(0);
    const id = t.socket.json[0].id as string;
    expect(t.state().phase).toBe("transcribing");
    t.socket.receive({ type: "transcript", id, text: "", superseded: true });
    expect(t.state().phase).toBe("idle");
    expect(t.state().toasts).toEqual([]);
  });

  it("a newer local turn stops the previous reply still playing here", async () => {
    const t = setup();
    const speech = new FakeSpeech();
    t.conversation.attachStage({ speech, bubble: undefined, character: undefined } as never);
    t.conversation.sendText("one");
    t.conversation.sendText("two"); // before turn_start of "one" arrived: nothing to interrupt yet
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "one" });
    t.socket.receive({ ...seg("t1", 0, "First reply."), audio_b64: btoa("a".repeat(300)), audio_format: "mp3" });
    t.socket.receive(turnEnd("t1", "First reply.")); // finished on the server before "two" arrived
    await vi.runOnlyPendingTimersAsync();
    expect(speech.queued.length).toBe(1);

    t.socket.receive({ type: "turn_start", turn_id: "t2", input_id: "y", origin: "c1", user_text: "two" });
    expect(speech.stopped).toBe(1);
  });
});

describe("Conversation: voice off", () => {
  it("starts with her voice on, and turning it off quiets typed and spoken input and is remembered", async () => {
    const t = setup();
    const speech = new FakeSpeech();
    t.conversation.attachStage({ speech, bubble: undefined, character: undefined } as never);
    expect(t.state().voice).toBe(true);
    expect(speech.muted).toBe(false);
    t.conversation.sendText("loud");
    expect(t.socket.json.at(-1)).not.toHaveProperty("quiet");
    expect(speech.unlocks).toBe(1);

    t.conversation.setVoice(false);
    expect(t.state().voice).toBe(false);
    expect(speech.muted).toBe(true);
    expect(t.storage.data.get("hekate.voice")).toBe("off");
    t.conversation.sendText("quiet please");
    expect(t.socket.json.at(-1)).toMatchObject({ type: "say", text: "quiet please", quiet: true });
    expect(speech.unlocks).toBe(1); // nothing to play: audio is left alone

    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    t.conversation.releaseTalk();
    t.conversation.pressTalk();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_end", quiet: true });

    t.conversation.setVoice(true);
    expect(speech.muted).toBe(false);
    expect(speech.unlocks).toBe(2); // the toggle is a gesture: audio is ready for the next reply
    expect(t.storage.data.get("hekate.voice")).toBe("on");
  });

  it("restores the device's choice, muting the stage when it arrives", () => {
    const t = setup({ storage: memoryStorage({ "hekate.voice": "off" }) });
    expect(t.state().voice).toBe(false);
    const speech = new FakeSpeech();
    t.conversation.attachStage({ speech, bubble: undefined, character: undefined } as never);
    expect(speech.muted).toBe(true);
  });
});

const memoryCard = (id = "m1") => ({ kind: "memory", id, title: "Saved", data: { items: [] } });
const remindersCard = { kind: "reminders", id: "r1", title: "Reminder", data: { items: [], highlight: ["7"] } };

describe("Conversation: cards, notices and invalidation", () => {
  function startTurn(t: ReturnType<typeof setup>, turn = "t1", origin = "c1") {
    t.socket.receive({ type: "turn_start", turn_id: turn, input_id: "x", origin, user_text: "hi" });
  }

  it("shows cards from turns this client started", () => {
    const t = setup();
    startTurn(t);
    t.socket.receive({ type: "card", turn_id: "t1", card: memoryCard() });
    expect(t.state().cards.map((c) => [c.card.kind, c.source])).toEqual([["memory", "turn"]]);
  });

  it("ignores cards from another client's turn, unknown kinds and malformed cards", () => {
    const t = setup();
    startTurn(t, "t1", "someone-else");
    t.socket.receive({ type: "card", turn_id: "t1", card: memoryCard() });
    expect(t.state().cards).toEqual([]);
    t.socket.receive({ type: "turn_end", turn_id: "t1", status: "done", text: "", model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } });
    startTurn(t, "t2");
    t.socket.receive({ type: "card", turn_id: "t2", card: { kind: "hologram", id: "h", title: "", data: {} } });
    t.socket.receive({ type: "card", turn_id: "t2", card: { kind: "tasks", id: "t", title: "", data: "nope" } });
    expect(t.state().cards).toEqual([]);
  });

  it("keeps the latest three and clears them when the next turn starts", () => {
    const t = setup();
    startTurn(t);
    for (const id of ["a", "b", "c", "d"]) t.socket.receive({ type: "card", turn_id: "t1", card: memoryCard(id) });
    expect(t.state().cards.filter((c) => !c.leaving).map((c) => c.card.id)).toEqual(["d", "c", "b"]);
    startTurn(t, "t2");
    expect(t.state().cards.filter((c) => !c.leaving)).toEqual([]);
  });

  it("lets cards expire after about 45 seconds", () => {
    const t = setup();
    startTurn(t);
    t.socket.receive({ type: "card", turn_id: "t1", card: memoryCard() });
    vi.advanceTimersByTime(44_000);
    expect(t.state().cards.filter((c) => !c.leaving)).toHaveLength(1);
    vi.advanceTimersByTime(2_000);
    expect(t.state().cards.filter((c) => !c.leaving)).toHaveLength(0);
  });

  it("dismisses a card on request", () => {
    const t = setup();
    startTurn(t);
    t.socket.receive({ type: "card", turn_id: "t1", card: memoryCard() });
    t.conversation.dismissCard(t.state().cards[0].key);
    expect(t.state().cards.filter((c) => !c.leaving)).toEqual([]);
  });

  it("turns notify into a banner and a card, with a soft gesture when she knows one", () => {
    const t = setup({ ready: { ...READY, gestures: ["waving", "acknowledging"] } });
    const play = vi.fn();
    t.conversation.attachStage({ character: { playGesture: play }, speech: {}, bubble: undefined } as never);
    t.socket.receive({ type: "notify", id: "n1", kind: "reminder", title: "Reminder", body: "Call the dentist", card: remindersCard });
    expect(t.state().notices).toEqual([{ id: "n1", title: "Reminder", body: "Call the dentist", section: "reminders" }]);
    expect(t.state().cards.map((c) => [c.card.kind, c.source])).toEqual([["reminders", "notify"]]);
    expect(play).toHaveBeenCalledWith("acknowledging");
    vi.advanceTimersByTime(13_000);
    expect(t.state().notices).toEqual([]);
  });

  it("shows an approval notify like a reminder, pointing at Today", () => {
    const t = setup();
    const approval = { id: "5", kind: "mail_trash", title: "Trash 14 newsletters?", detail: "", scope_label: "mail from example.com", status: "pending", result: null, decided_by: null, created_at: "x", expires_at: null, decided_at: null };
    t.socket.receive({ type: "notify", id: "n3", kind: "approval", title: "Needs your OK", body: "Trash 14 newsletters?", card: { kind: "approval", id: "approval-5", title: "Trash?", data: approval } });
    expect(t.state().notices).toMatchObject([{ id: "n3", section: "today" }]);
    expect(t.state().cards.map((c) => [c.card.kind, c.source])).toEqual([["approval", "notify"]]);
  });

  it("points a change notify at the Changes section", () => {
    const t = setup();
    t.socket.receive({ type: "notify", id: "n4", kind: "change", title: "Change merged", body: "Match mail rules on subject words" });
    expect(t.state().notices).toMatchObject([{ id: "n4", section: "changes" }]);
  });

  it("passes approval events on, and ignores malformed ones", () => {
    const t = setup();
    const seen: string[] = [];
    t.conversation.onApprovalChange((a) => seen.push(`${a.id}:${a.status}`));
    t.socket.receive({ type: "approval", approval: { id: "5", title: "Trash?", status: "running" } });
    t.socket.receive({ type: "approval", approval: { id: "5" } });
    expect(seen).toEqual(["5:running"]);
  });

  it("shows a notice without a card, and stays quiet if she has no acknowledging gesture", () => {
    const t = setup();
    const play = vi.fn();
    t.conversation.attachStage({ character: { playGesture: play }, speech: {}, bubble: undefined } as never);
    t.socket.receive({ type: "notify", id: "n2", kind: "reminder", title: "Stretch", body: "" });
    expect(t.state().notices).toHaveLength(1);
    expect(t.state().cards).toEqual([]);
    expect(play).not.toHaveBeenCalled();
  });

  it("passes invalidate domains on, and everything again after a reconnect", () => {
    const t = setup();
    const seen: string[][] = [];
    t.conversation.onInvalidate((d) => seen.push(d));
    t.socket.receive({ type: "invalidate", domains: ["tasks", "calendar"] });
    expect(seen).toEqual([["tasks", "calendar"]]);
    t.socket.drop();
    t.conversation.client.reconnectNow();
    FakeSocket.last.open();
    FakeSocket.last.receive(READY);
    expect(seen.at(-1)).toEqual(["tasks", "reminders", "calendar", "memory", "weather", "mail", "approvals", "changes", "ideas", "journal"]);
  });
});

describe("Conversation: authoritative turn text", () => {
  it("an interrupted reply nobody heard is empty in the live log", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "t1", input_id: "x", origin: "c1", user_text: "hi" });
    t.socket.receive(seg("t1", 0, "Never heard."));
    t.socket.receive({ ...turnEnd("t1", ""), status: "interrupted" });
    expect(t.state().live.find((e) => e.role === "assistant")?.text).toBe("");
  });
});
