import { describe, expect, it } from "vitest";
import { offlineNote, oldestUpdate } from "./offline";
import { keys } from "./queryKeys";
import { formatDateTime, formatTime } from "./time";

const q = (queryKey: readonly unknown[], dataUpdatedAt: number, status = "success", data: unknown = []) =>
  ({ queryKey, state: { status, dataUpdatedAt, data } }) as never;

describe("oldestUpdate", () => {
  it("is the oldest successful menu query", () => {
    expect(oldestUpdate([q(keys.lists, 300), q(keys.tasks(undefined, "open"), 100), q(keys.core, 200)])).toBe(100);
  });
  it("ignores queries that are never stored and ones without data", () => {
    expect(oldestUpdate([q(keys.mailMessage("m"), 1), q(["healthz"], 2), q(keys.ideas, 0, "pending", undefined), q(keys.lists, 500)])).toBe(500);
    expect(oldestUpdate([])).toBeUndefined();
  });
  it("counts a query whose refresh failed", () => {
    expect(oldestUpdate([q(keys.lists, 50, "error")])).toBe(50);
  });
});

describe("offlineNote", () => {
  it("says the time for data from today", () => {
    const at = Date.parse("2026-10-20T14:02:00Z");
    const iso = new Date(at).toISOString();
    expect(offlineNote(at, at + 60_000)).toBe(`Offline · as of ${formatTime(iso)}`);
  });
  it("adds the day for older data", () => {
    const at = Date.parse("2026-10-17T14:02:00Z");
    expect(offlineNote(at, Date.parse("2026-10-20T14:00:00Z"))).toBe(`Offline · as of ${formatDateTime(new Date(at).toISOString())}`);
  });
});
