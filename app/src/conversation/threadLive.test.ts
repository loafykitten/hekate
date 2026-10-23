import { describe, expect, it } from "vitest";
import type { ThreadEvent } from "./protocol";
import type { Review } from "../data/model";
import { currentStep, historyHolds, IDLE, isRunning, mergeReviews, placeReviews, reduceThreadEvent, reviewRunning, ThreadLiveStore, threadRunning } from "./threadLive";

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
const rv = (over: Partial<Review> = {}): Review => ({
  id: "rv1",
  thread_id: "th",
  turn_id: "t1",
  round: 1,
  status: "running",
  model: "m",
  verdict: "",
  summary: "",
  findings: [],
  verified: [],
  commands: 0,
  error: "",
  followup_turn_id: "",
  started_at: "2026-10-23T10:00:00Z",
  finished_at: "",
  ...over,
});
const review = (over: Partial<Review> = {}): ThreadEvent => ({ type: "review", review: rv(over) });
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

describe("review events", () => {
  it("adds a running review and replaces it by id when it ends", () => {
    const running = run(start(), end("Done."), review());
    expect(running.reviews).toHaveLength(1);
    expect(reviewRunning(running.reviews)).toBe(true);
    expect(threadRunning(false, running)).toBe(true); // the stop button shows while it runs
    const epoch = running.epoch;
    const done = reduceThreadEvent(running, review({ status: "done", verdict: "looks_good", finished_at: "x" }));
    expect(done.reviews).toHaveLength(1);
    expect(done.reviews[0]).toMatchObject({ status: "done", verdict: "looks_good" });
    expect(threadRunning(false, done)).toBe(false);
    expect(done.epoch).toBe(epoch + 1); // refetch the transcript and the thread
  });

  it("keeps reviews apart by id and ignores one it can't read", () => {
    const s = run(review(), review({ id: "rv2", round: 2 }), { type: "review", review: "junk" });
    expect(s.reviews.map((r) => r.id)).toEqual(["rv1", "rv2"]);
  });

  it("a review-origin turn with no owner words has nothing to show as the owner's", () => {
    const s = run(start("t2"), { type: "turn_start", turn_id: "t3", input_id: "", origin: "review", user_text: "" });
    expect(s.turn).toMatchObject({ id: "t3", userText: "" });
  });

  it("mergeReviews lets live win but not a stale running over a saved end", () => {
    const saved = [rv({ status: "done", verdict: "blocked" })];
    expect(mergeReviews(saved, [rv()])[0].status).toBe("done");
    expect(mergeReviews([rv()], [rv({ status: "failed" })])[0].status).toBe("failed");
    expect(mergeReviews([rv({ id: "b", started_at: "2026-10-23T11:00:00Z" })], [rv()]).map((r) => r.id)).toEqual(["rv1", "b"]);
  });
});

describe("placeReviews", () => {
  const msgs = [
    { id: "m1", role: "user", turn_id: "t1" },
    { id: "m2", role: "assistant", turn_id: "t1" },
    { id: "m3", role: "assistant", turn_id: "t2" }, // her reply to the findings (no owner message)
  ];
  it("puts the card after the reviewed reply, once, even with a follow-up", () => {
    const p = placeReviews(msgs, [rv({ followup_turn_id: "t2" })]);
    expect([...p.after.keys()]).toEqual(["m2"]);
    expect(p.before.size).toBe(0);
    expect(p.loose).toEqual([]);
  });
  it("stands before the follow-up reply when the reviewed turn isn't loaded", () => {
    const p = placeReviews([msgs[2]], [rv({ followup_turn_id: "t2" })]);
    expect(p.before.get("m3")).toHaveLength(1);
    expect(p.after.size).toBe(0);
  });
  it("leaves a review loose while its turn is only live", () => {
    expect(placeReviews([], [rv()]).loose).toHaveLength(1);
    expect(placeReviews(msgs, [rv({ turn_id: "t9", followup_turn_id: "t8" })]).loose).toHaveLength(1);
  });
  it("waits for her reply: an owner message alone at the end isn't where the card goes", () => {
    expect(placeReviews([msgs[0]], [rv()]).loose).toHaveLength(1);
    expect(placeReviews([msgs[0], msgs[2]], [rv()]).after.get("m1")).toHaveLength(1); // a reply-less turn mid-transcript
  });
  it("a round 2 review follows the follow-up reply", () => {
    const p = placeReviews(msgs, [rv({ followup_turn_id: "t2" }), rv({ id: "rv2", round: 2, turn_id: "t2" })]);
    expect(p.after.get("m2")?.[0].id).toBe("rv1");
    expect(p.after.get("m3")?.[0].id).toBe("rv2");
  });
});
