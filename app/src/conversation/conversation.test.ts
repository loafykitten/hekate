import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConversationClient } from "./client";
import { Conversation, type MicLike } from "./conversation";
import { FakeSocket, FakeSpeech, READY } from "./fakes";
import { MicError } from "./mic";

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return { data, getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
}

function setup(opts: { startImpl?: () => Promise<boolean>; ready?: object; storage?: ReturnType<typeof memoryStorage>; stage?: object } = {}) {
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
  if (opts.stage) conversation.attachStage(opts.stage as never);
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

describe("Conversation: the evening check-in", () => {
  const stage = (speech: FakeSpeech) => ({ speech, bubble: undefined, character: undefined }) as never;
  const checkIns = (t: ReturnType<typeof setup>) => t.socket.json.filter((m) => m.type === "checkin");

  it("opened by its notification, waits for the scene, then asks once and in the bubble while audio is locked", () => {
    const t = setup();
    t.conversation.startCheckIn("2026-10-20");
    expect(checkIns(t)).toEqual([]); // no scene yet
    const speech = new FakeSpeech();
    speech.running = false; // no gesture yet: the browser holds audio back
    t.conversation.attachStage(stage(speech));
    expect(checkIns(t)).toEqual([{ type: "checkin", id: expect.any(String), day: "2026-10-20", quiet: true }]);
    t.conversation.attachStage(stage(speech));
    expect(checkIns(t)).toHaveLength(1);
  });

  it("waits for the connection", () => {
    const t = setup();
    t.conversation.attachStage(stage(new FakeSpeech()));
    t.socket.drop();
    t.conversation.startCheckIn("2026-10-20");
    t.conversation.client.reconnectNow();
    FakeSocket.last.open();
    expect(FakeSocket.last.json.filter((m) => m.type === "checkin")).toEqual([]);
    FakeSocket.last.receive(READY);
    expect(FakeSocket.last.json.filter((m) => m.type === "checkin")).toEqual([{ type: "checkin", id: expect.any(String), day: "2026-10-20" }]);
  });

  it("speaks when audio already plays here, or when started from the notice's button", () => {
    const t = setup();
    const speech = new FakeSpeech();
    t.conversation.attachStage(stage(speech));
    t.conversation.startCheckIn("2026-10-20");
    expect(checkIns(t).at(-1)).not.toHaveProperty("quiet");
    speech.running = false;
    t.conversation.startCheckIn(undefined, true);
    expect(speech.unlocks).toBe(1);
    expect(checkIns(t).at(-1)).toEqual({ type: "checkin", id: expect.any(String) });
  });

  it("is quiet with her voice off", () => {
    const t = setup({ storage: memoryStorage({ "hekate.voice": "off" }) });
    t.conversation.attachStage(stage(new FakeSpeech()));
    t.conversation.startCheckIn(undefined, true);
    expect(checkIns(t).at(-1)).toMatchObject({ quiet: true });
  });

  it("shows its notify as a notice that can start it", () => {
    const t = setup();
    t.socket.receive({ type: "notify", id: "checkin-2026-10-20", kind: "checkin", title: "How did today go?", body: "" });
    expect(t.state().notices).toEqual([{ id: "checkin-2026-10-20", title: "How did today go?", body: "", checkIn: true }]);
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
    expect(t.state().notices).toEqual([{ id: "n1", title: "Reminder", body: "Call the dentist", section: "reminders", cardKey: t.state().cards[0].key }]);
    expect(t.state().cards.map((c) => [c.card.kind, c.source])).toEqual([["reminders", "notify"]]);
    expect(play).toHaveBeenCalledWith("acknowledging");
    vi.advanceTimersByTime(13_000);
    expect(t.state().notices).toEqual([]);
  });

  it("ties each notice to its own card, even when the next one of the same id replaces it", () => {
    const t = setup();
    const reminder = (id: string) => ({ type: "notify", id, kind: "reminder", title: "Reminder", body: id, card: { ...remindersCard, id: "reminders" } });
    t.socket.receive(reminder("first"));
    t.socket.receive(reminder("second"));
    const [first, second] = t.state().notices;
    const shown = t.state().cards.filter((c) => !c.leaving).map((c) => c.key);
    expect(shown).toEqual([second.cardKey]); // the first card was replaced
    expect(shown).not.toContain(first.cardKey); // so its banner is the only place it shows
  });

  it("shows an approval notify like a reminder, pointing at Today", () => {
    const t = setup();
    const approval = { id: "5", kind: "mail_trash", title: "Trash 14 newsletters?", detail: "", scope_label: "mail from example.com", status: "pending", result: null, decided_by: null, created_at: "x", expires_at: null, decided_at: null };
    t.socket.receive({ type: "notify", id: "n3", kind: "approval", title: "Needs your OK", body: "Trash 14 newsletters?", card: { kind: "approval", id: "approval-5", title: "Trash?", data: approval } });
    expect(t.state().notices).toMatchObject([{ id: "n3", section: "today" }]);
    expect(t.state().cards.map((c) => [c.card.kind, c.source])).toEqual([["approval", "notify"]]);
  });

  it("points a heartbeat nudge at Today", () => {
    const t = setup();
    t.socket.receive({ type: "notify", id: "n5", kind: "nudge", title: "Overdue", body: "Overdue: Pay rent" });
    expect(t.state().notices).toMatchObject([{ id: "n5", title: "Overdue", body: "Overdue: Pay rent", section: "today" }]);
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
    expect(seen.at(-1)).toEqual(["tasks", "reminders", "calendar", "memory", "weather", "mail", "approvals", "changes", "ideas", "journal", "routines", "threads"]);
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

describe("Conversation: threads", () => {
  const wrapped = (thread: string, event: object) => ({ type: "thread_event", thread, event });
  const threadTurn = (turn = "tt1") => [
    wrapped("th1", { type: "turn_start", turn_id: turn, input_id: "i1", origin: "c1", user_text: "look into it" }),
    wrapped("th1", { type: "activity", turn_id: turn, tool: "web_search", label: "Searching the web", state: "start" }),
    wrapped("th1", { ...seg(turn, 0, "Here is what I found."), audio_b64: null }),
    wrapped("th1", { ...turnEnd(turn, "Here is what I found.") }),
  ];

  it("keeps wrapped events away from the main conversation", () => {
    const t = setup();
    const before = t.state();
    for (const m of threadTurn()) t.socket.receive(m);
    const after = t.state();
    expect(after.live).toEqual([]);
    expect(after.caption).toBeUndefined();
    expect(after.activity).toBeUndefined();
    expect(after.phase).toBe("idle");
    expect(after.cards).toEqual([]);
    expect(after.historyEpoch).toBe(before.historyEpoch);
    expect(t.conversation.client.turnActive).toBe(false);
    expect(t.conversation.threads.get("th1").turn?.text).toBe("Here is what I found.");
  });

  it("leaves the main turn running while a thread's turn comes and goes", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "m1", input_id: "x", origin: "c1", user_text: "hi" });
    for (const m of threadTurn()) t.socket.receive(m);
    expect(t.conversation.client.activeTurnId).toBe("m1");
    expect(t.state().live.map((e) => e.turnId)).toEqual(["m1", "m1"]);
  });

  it("sends text to a thread without interrupting the main turn", () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "m1", input_id: "x", origin: "c1", user_text: "hi" });
    expect(t.conversation.sendThreadText("th1", "  go on  ")).toBeTruthy();
    expect(t.socket.json.at(-1)).toMatchObject({ type: "say", text: "go on", thread: "th1" });
    expect(t.types()).not.toContain("interrupt");
    expect(t.conversation.client.activeTurnId).toBe("m1");
    expect(t.state().caption?.text).toBe("hi"); // not "go on"
  });

  it("sends a thread message with the ids of its uploaded files, and none otherwise", () => {
    const t = setup();
    expect(t.conversation.sendThreadText("th1", "update it", ["f_1", "f_2"])).toBeTruthy();
    expect(t.socket.json.at(-1)).toMatchObject({ type: "say", text: "update it", thread: "th1", files: ["f_1", "f_2"] });
    t.conversation.sendThreadText("th1", "no files", []);
    expect(t.socket.json.at(-1)).not.toHaveProperty("files");
    t.conversation.sendThreadText("th1", "plain");
    expect(t.socket.json.at(-1)).not.toHaveProperty("files");
  });

  it("talks to a thread with the same microphone, without hushing the main conversation", async () => {
    const t = setup();
    t.socket.receive({ type: "turn_start", turn_id: "m1", input_id: "x", origin: "c1", user_text: "hi" });
    t.conversation.pressTalk("th1");
    await Promise.resolve();
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_start", thread: "th1" });
    expect(t.types()).not.toContain("interrupt");
    expect(t.state().phase).not.toBe("listening"); // the stage stays out of it
    expect(t.state().threadTalk).toMatchObject({ thread: "th1", ready: true });
    t.conversation.pressTalk(); // the voice stage's button can't take the microphone meanwhile
    expect(t.types().filter((x) => x === "ptt_start")).toHaveLength(1);
    t.conversation.releaseTalk();
    t.conversation.pressTalk("th1");
    await vi.waitFor(() => expect(t.types()).toContain("ptt_end"));
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_end", thread: "th1" });
    expect(t.mic.start).toHaveBeenCalledTimes(1);
  });

  it("takes no new input for a thread while it works (the service would refuse it)", () => {
    const t = setup();
    t.socket.receive(threadTurn()[0]); // turn_start: she's working
    const sent = t.socket.json.length;
    expect(t.conversation.sendThreadText("th1", "and also")).toBeUndefined();
    t.conversation.pressTalk("th1");
    expect(t.conversation.holdTalk("th1")).toBe(false);
    expect(t.socket.json.length).toBe(sent);
    t.socket.receive(threadTurn()[3]); // turn_end: done
    expect(t.conversation.sendThreadText("th1", "and also")).toBeTruthy();
  });

  it("sends a held key's speech on release, and drops a bare tap", async () => {
    const t = setup();
    expect(t.conversation.holdTalk("th1")).toBe(true);
    await Promise.resolve();
    t.tick(600);
    t.conversation.releaseHold();
    await vi.waitFor(() => expect(t.types()).toContain("ptt_end"));
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_end", thread: "th1" });

    t.conversation.holdTalk();
    await Promise.resolve();
    t.tick(50);
    t.conversation.releaseHold();
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_cancel" });
  });

  it("cancels a held key when the window loses focus", async () => {
    const t = setup();
    t.conversation.holdTalk();
    await Promise.resolve();
    t.conversation.cancelHold();
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_cancel" });
    expect(t.state().phase).toBe("idle");
  });

  it("routes a thread input's transcript and errors to the thread", () => {
    const t = setup();
    t.conversation.sendThreadText("th1", "hello");
    const id = t.socket.json.at(-1)?.id as string;
    t.socket.receive({ type: "transcript", id, text: "hello" });
    expect(t.state().caption).toBeUndefined();
    t.socket.receive({ type: "error", code: "busy", message: "Three threads are already working.", input_id: id });
    expect(t.state().toasts).toEqual([]);
    expect(t.conversation.threads.get("th1").error).toBe("Three threads are already working.");
  });

  it("stops a thread's turn by id, and a thread notice carries its thread", () => {
    const t = setup();
    t.socket.receive(threadTurn()[0]);
    t.conversation.stopThread("th1");
    expect(t.socket.json.at(-1)).toEqual({ type: "interrupt", heard_seq: -1, thread: "th1", turn_id: "tt1" });
    t.socket.receive({ type: "notify", id: "th1", kind: "thread", title: "Done", body: "Set up the thing" });
    expect(t.state().notices[0]).toMatchObject({ thread: "th1" });
    expect(t.state().notices[0].section).toBeUndefined();
  });

  it("closing the menu during a latched thread recording cancels it and frees the microphone", async () => {
    const t = setup();
    t.conversation.pressTalk("th1");
    await Promise.resolve();
    t.conversation.releaseTalk();
    expect(t.state().threadTalk).toMatchObject({ thread: "th1", latched: true });
    t.conversation.cancelThreadCapture("other"); // another thread's view going away leaves it be
    expect(t.state().threadTalk).toBeDefined();
    t.conversation.cancelThreadCapture("th1"); // the thread's view stopped being on screen
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_cancel", thread: "th1" });
    expect(t.mic.stop).toHaveBeenCalled();
    expect(t.state().threadTalk).toBeUndefined();
    t.chunk(new Int16Array([1]).buffer);
    expect(t.socket.sent.at(-1)).not.toBeInstanceOf(ArrayBuffer); // nothing streams any more
    t.conversation.pressTalk(); // the voice stage can have the microphone
    expect(t.socket.json.at(-1)).toMatchObject({ type: "ptt_start" });
    expect(t.socket.json.at(-1)?.thread).toBeUndefined();
  });

  it("holds the microphone until the last recording is flushed", async () => {
    const t = setup();
    let release: () => void = () => {};
    t.mic.stop.mockImplementationOnce(() => new Promise<void>((r) => (release = r)));
    t.conversation.pressTalk("th1");
    await Promise.resolve();
    t.conversation.releaseTalk();
    t.conversation.pressTalk("th1"); // sends it; the mic is still stopping
    const first = t.socket.json.at(-1)?.id;
    t.conversation.pressTalk("th1"); // too soon: nothing starts
    expect(t.conversation.holdTalk("th1")).toBe(false);
    expect(t.types()).toEqual(["ptt_start"]);
    release();
    await vi.waitFor(() => expect(t.types()).toEqual(["ptt_start", "ptt_end"]));
    expect(t.socket.json[1].id).toBe(first);
    t.conversation.pressTalk("th1");
    await Promise.resolve();
    expect(t.types()).toEqual(["ptt_start", "ptt_end", "ptt_start"]);
    const audio = new Int16Array([5]).buffer;
    t.chunk(audio);
    expect(t.socket.sent.at(-1)).toBe(audio); // the second recording streams
  });

  it("names the input a thread refused, from a wrapped error or a plain one", () => {
    const t = setup();
    const id = t.conversation.sendThreadText("th1", "hello")!;
    t.socket.receive(wrapped("th1", { type: "error", code: "busy", message: "Too many threads.", input_id: id }));
    expect(t.conversation.threads.get("th1")).toMatchObject({ error: "Too many threads.", failedInput: id });
    const id2 = t.conversation.sendThreadText("th1", "again")!;
    expect(t.conversation.threads.get("th1").failedInput).toBeUndefined();
    t.socket.receive({ type: "error", code: "bad_message", message: "No such thread.", input_id: id2 });
    expect(t.conversation.threads.get("th1").failedInput).toBe(id2);
  });

  it("doesn't call a turn that was taken and then failed a refusal (its words are saved)", () => {
    const t = setup();
    const id = t.conversation.sendThreadText("th1", "look into it")!;
    t.socket.receive(wrapped("th1", { type: "turn_start", turn_id: "tt9", input_id: id, origin: "c1", user_text: "look into it" }));
    t.socket.receive(wrapped("th1", { type: "error", code: "llm_error", message: "Something went wrong.", input_id: id }));
    expect(t.conversation.threads.get("th1")).toMatchObject({ error: "Something went wrong.", failedInput: undefined });
  });
});
