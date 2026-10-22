import { describe, expect, it } from "vitest";
import type { Reminder } from "./model";
import { canReopenAtOriginalTime, DUE_SOON_MS, showsDone, showsSnooze } from "./reminderDue";

const NOW = Date.parse("2026-10-20T12:00:00Z");
const reminder = (over: Partial<Reminder>): Reminder => ({
  id: "1",
  text: "Water the plants",
  at: new Date(NOW + 3 * 3600_000).toISOString(),
  rrule: null,
  status: "scheduled",
  last_fired_at: null,
  created_at: new Date(NOW).toISOString(),
  ...over,
});

describe("reminder actions", () => {
  it("hides Done on a future reminder", () => {
    expect(showsDone(reminder({}), NOW)).toBe(false);
    expect(showsSnooze(reminder({}))).toBe(true);
  });
  it("shows Done when it is due soon, overdue or has fired", () => {
    expect(showsDone(reminder({ at: new Date(NOW + DUE_SOON_MS).toISOString() }), NOW)).toBe(true);
    expect(showsDone(reminder({ at: new Date(NOW - 60_000).toISOString() }), NOW)).toBe(true);
    expect(showsDone(reminder({ status: "fired" }), NOW)).toBe(true);
  });
  it("never offers Done on finished reminders", () => {
    expect(showsDone(reminder({ status: "done", at: new Date(NOW - 1000).toISOString() }), NOW)).toBe(false);
    expect(showsSnooze(reminder({ status: "done" }))).toBe(false);
  });
  it("can put a done reminder back only while its time is ahead", () => {
    expect(canReopenAtOriginalTime(reminder({ status: "done" }), NOW)).toBe(true);
    expect(canReopenAtOriginalTime(reminder({ status: "done", at: new Date(NOW - 1000).toISOString() }), NOW)).toBe(false);
  });
});
