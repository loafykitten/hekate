import { describe, expect, it } from "vitest";
import type { ThreadEvent } from "./protocol";
import { currentStep, historyHolds, IDLE, isRunning, reduceThreadEvent, ThreadLiveStore, threadRunning } from "./threadLive";

const start = (turn_id = "t1"): ThreadEvent => ({ type: "turn_start", turn_id, input_id: "i", origin: "c", user_text: "do it" });
const activity = (state: "start" | "done" | "error", tool = "lab_shell", label = "Running a command", turn_id = "t1"): ThreadEvent => ({ type: "activity", turn_id, tool, label, state });
const segment = (text: string, turn_id = "t1"): ThreadEvent => ({ type: "segment", turn_id, seq: 0, text, duration: 0, audio_b64: null, audio_format: null, words: null, gestures: [], expressions: [] });
const end = (text: string, status: "done" | "interrupted" | "error" = "done", turn_id = "t1"): ThreadEvent => ({
  type: "turn_end",
  turn_id,
  status,
  text,
  model: "m",
  usage: { input_tokens: 1, output_tokens: 1, cost_usd: null },
});
const run = (...events: ThreadEvent[]) => events.reduce(reduceThreadEvent, IDLE);

describe("reduceThreadEvent", () => {
  it("starts a turn with the owner's words", () => {
    const s = run(start());
    expect(isRunning(s)).toBe(true);
    expect(s.turn).toMatchObject({ id: "t1", userText: "do it", text: "", steps: [] });
  });

  it("accumulates segments exactly as written (PROTOCOL: concatenate with nothing between)", () => {
    expect(run(start(), segment("First. Sec"), segment("ond.")).turn?.text).toBe("First. Second.");
    expect(run(start(), segment("# Title\n"), segment("- item")).turn?.text).toBe("# Title\n- item");
    expect(run(start(), segment("```sh"), segment("\nls\n```")).turn?.text).toBe("```sh\nls\n```");
  });

  it("tracks tool steps from start to done or error", () => {
    const s = run(start(), activity("start"), activity("done"), activity("start", "web_search", "Searching"), activity("error", "web_search", "Searching"));
    expect(s.turn?.steps).toEqual([
      { tool: "lab_shell", label: "Running a command", state: "ok" },
      { tool: "web_search", label: "Searching", state: "error" },
    ]);
    const running = run(start(), activity("start"), activity("start", "web_search", "Searching"));
    expect(currentStep(running.turn!)?.label).toBe("Searching");
  });

  it("closes the turn on turn_end with the full text, and bumps the epoch", () => {
    const s = run(start(), activity("start"), segment("part"), end("The whole reply."));
    expect(isRunning(s)).toBe(false);
    expect(s.turn).toMatchObject({ text: "The whole reply.", ended: true, status: "done" });
    expect(s.turn?.steps[0].state).toBe("ok"); // nothing is left spinning
    expect(s.epoch).toBe(1);
    expect(s.error).toBeUndefined();
  });

  it("shows an error inline, and a failed turn without one says so", () => {
    expect(run(start(), { type: "error", code: "llm_error", message: "The model fell over." }).error).toBe("The model fell over.");
    const failed = run(start(), end("", "error"));
    expect(failed.error).toMatch(/didn't finish/);
    // a specific error stays instead of the generic one
    expect(run(start(), { type: "error", code: "turn_timeout", message: "Ran out of time." }, end("", "error")).error).toBe("Ran out of time.");
  });

  it("picks up a turn it joined halfway, and ignores cards", () => {
    const s = run(segment("midway"), activity("start"));
    expect(s.turn).toMatchObject({ id: "t1", text: "midway" });
    expect(run(start(), { type: "card", turn_id: "t1", card: { kind: "x", id: "1", title: "", data: {} } })).toEqual(run(start()));
  });

  it("starts fresh on the next turn and clears the old error", () => {
    const s = run(start(), segment("a"), end("a"), start("t2"));
    expect(s.turn).toMatchObject({ id: "t2", text: "", ended: false });
    expect(s.epoch).toBe(1);
    expect(run({ type: "error", code: "x", message: "bad" }, start()).error).toBeUndefined();
  });
});

describe("ThreadLiveStore", () => {
  it("keeps a snapshot per thread, stable until it changes", () => {
    const store = new ThreadLiveStore();
    let calls = 0;
    store.subscribe(() => calls++);
    expect(store.get("a")).toBe(IDLE);
    store.apply("a", start());
    const a = store.get("a");
    expect(store.get("a")).toBe(a);
    expect(store.get("b")).toBe(IDLE);
    expect(calls).toBe(1);
  });

  it("settles an ended turn once history holds it, and only that turn", () => {
    const store = new ThreadLiveStore();
    store.apply("a", start());
    store.settle("a", "t1"); // still running: stays
    expect(store.get("a").turn).toBeDefined();
    store.apply("a", end("done"));
    store.settle("a", "other");
    expect(store.get("a").turn).toBeDefined();
    store.settle("a", "t1");
    expect(store.get("a").turn).toBeUndefined();
  });

  it("clears error on a new send and reports a failure", () => {
    const store = new ThreadLiveStore();
    store.fail("a", "busy", "No room.");
    expect(store.get("a").error).toBe("No room.");
    store.sent("a", true);
    expect(store.get("a")).toMatchObject({ error: undefined, transcribing: true });
  });

  it("threadRunning prefers what was seen live over the server's flag", () => {
    const store = new ThreadLiveStore();
    expect(threadRunning(true, store.get("a"))).toBe(true);
    store.apply("a", start());
    store.apply("a", end("x"));
    expect(threadRunning(true, store.get("a"))).toBe(false);
  });
});

describe("historyHolds", () => {
  const turn = (text: string) => run(start(), segment(text), end(text)).turn!;
  it("waits for her saved reply", () => {
    expect(historyHolds([{ role: "user", turn_id: "t1" }], turn("Done."))).toBe(false);
    expect(historyHolds([{ role: "user", turn_id: "t1" }, { role: "assistant", turn_id: "t1" }], turn("Done."))).toBe(true);
    expect(historyHolds([{ role: "assistant", turn_id: "t0" }], turn("Done."))).toBe(false);
  });
  it("lets a turn that wrote nothing go once its words are saved, and never a running one", () => {
    expect(historyHolds([{ role: "user", turn_id: "t1" }], turn(""))).toBe(true);
    expect(historyHolds([{ role: "assistant", turn_id: "t1" }], run(start()).turn!)).toBe(false);
  });
});
