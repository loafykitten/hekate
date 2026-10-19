import { describe, expect, it } from "vitest";
import { mergeLog, type LiveEntry } from "./log";

const h = (id: string, role: "user" | "assistant", text: string) => ({ id, role, text, created_at: "2026-10-19T10:00:00Z" });
const live = (turnId: string, role: "user" | "assistant", text: string, done: boolean): LiveEntry => ({ id: `${role}:${turnId}`, turnId, role, text, done });

describe("mergeLog", () => {
  it("appends live turns after history", () => {
    const log = mergeLog([h("1", "user", "hi")], [live("t1", "user", "what's up", true), live("t1", "assistant", "Not much.", false)]);
    expect(log.map((e) => e.text)).toEqual(["hi", "what's up", "Not much."]);
    expect(log.map((e) => e.pending)).toEqual([false, false, true]);
  });

  it("drops live entries once history was fetched after their turn ended", () => {
    const ended = (turnId: string, role: "user" | "assistant", text: string, endEpoch: number): LiveEntry => ({ ...live(turnId, role, text, true), endEpoch });
    const history = [h("1", "user", "hello"), h("2", "assistant", "Morning."), h("3", "user", "hello"), h("4", "assistant", "Again.")];
    const entries = [ended("t2", "user", "hello", 5), ended("t2", "assistant", "Again.", 5)];
    expect(mergeLog(history, entries, 5).map((e) => e.id)).toEqual(["1", "2", "3", "4"]);
    // History fetched before the turn ended may not have it yet, even if an earlier message reads the same.
    expect(mergeLog(history.slice(0, 2), entries, 4).map((e) => e.text)).toEqual(["hello", "Morning.", "hello", "Again."]);
  });

  it("omits a finished turn that said nothing", () => {
    expect(mergeLog([], [live("t1", "assistant", "", true)])).toEqual([]);
  });
});
