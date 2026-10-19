import { describe, expect, it } from "vitest";
import type { CalendarEvent } from "./model";
import {
  addDays,
  addMonths,
  dayHeading,
  dayKey,
  dayKeyOfMs,
  daysBetween,
  daysInMonth,
  dueLabel,
  eventTimeLabel,
  formatDateTime,
  formatDay,
  formatTime,
  fromDateTimeInputs,
  greeting,
  groupEventsByDay,
  isOverdue,
  minutesUntilTomorrowMorning,
  relativeTime,
  toDateInput,
  toMs,
  toRfc3339,
  toTimeInput,
  weekdayOf,
  resolveZone,
  setZone,
} from "./time";

const NY = { tz: "America/New_York", locale: "en-US" };
const TOKYO = { tz: "Asia/Tokyo", locale: "en-US" };
const plain = (s: string) => s.replace(/\s/g, " ");
const at = (iso: string) => Date.parse(iso);

describe("day keys and day math", () => {
  it("puts an instant on the calendar day of the zone", () => {
    expect(dayKey("2026-10-20T23:30:00-04:00", NY)).toBe("2026-10-20");
    expect(dayKey("2026-10-21T02:30:00Z", NY)).toBe("2026-10-20");
    expect(dayKey("2026-10-21T02:30:00Z", TOKYO)).toBe("2026-10-21");
  });

  it("keeps date-only values as they are", () => {
    expect(dayKey("2026-10-20", TOKYO)).toBe("2026-10-20");
    expect(dayKeyOfMs(at("2026-10-20T04:00:00Z"), NY)).toBe("2026-10-20");
    expect(dayKeyOfMs(at("2026-10-20T03:59:00Z"), NY)).toBe("2026-10-19");
  });

  it("adds days across month and year ends", () => {
    expect(addDays("2026-10-31", 1)).toBe("2026-11-01");
    expect(addDays("2026-01-01", -1)).toBe("2025-12-31");
    expect(addDays("2028-02-28", 1)).toBe("2028-02-29");
    expect(daysBetween("2026-10-20", "2026-10-23")).toBe(3);
    expect(daysBetween("2026-10-23", "2026-10-20")).toBe(-3);
  });

  it("knows weekdays and months", () => {
    expect(weekdayOf("2026-10-20")).toBe(2); // Tuesday
    expect(daysInMonth("2026-02-10")).toBe(28);
    expect(daysInMonth("2028-02-10")).toBe(29);
    expect(addMonths("2026-12-15", 1)).toBe("2027-01-01");
    expect(addMonths("2026-01-15", -1)).toBe("2025-12-01");
  });
});

describe("display", () => {
  it("formats times in the zone", () => {
    expect(plain(formatTime("2026-10-20T15:00:00-04:00", NY))).toBe("3:00 PM");
    expect(plain(formatTime("2026-10-20T15:00:00-04:00", TOKYO))).toBe("4:00 AM");
    expect(plain(formatDateTime("2026-10-20T15:00:00-04:00", NY))).toBe("Tue, Oct 20, 3:00 PM");
    expect(formatDateTime("2026-10-20", NY)).toBe("Tue, Oct 20");
    expect(formatDay("2026-10-21", NY)).toBe("Wed, Oct 21");
  });

  it("names the day relative to today", () => {
    expect(dayHeading("2026-10-20", "2026-10-20", NY)).toBe("Today");
    expect(dayHeading("2026-10-21", "2026-10-20", NY)).toBe("Tomorrow");
    expect(dayHeading("2026-10-19", "2026-10-20", NY)).toBe("Yesterday");
    expect(dayHeading("2026-10-25", "2026-10-20", NY)).toBe("Sun, Oct 25");
  });

  it("labels event times", () => {
    const timed = { start: "2026-10-20T15:00:00-04:00", end: "2026-10-20T16:30:00-04:00", all_day: false };
    expect(plain(eventTimeLabel(timed, NY))).toBe("3:00 PM – 4:30 PM");
    expect(eventTimeLabel({ start: "2026-10-20", end: "2026-10-21", all_day: true }, NY)).toBe("All day");
    expect(plain(eventTimeLabel({ ...timed, end: timed.start }, NY))).toBe("3:00 PM");
  });

  it("greets by time of day", () => {
    expect(greeting(at("2026-10-20T08:00:00-04:00"), NY)).toBe("Good morning");
    expect(greeting(at("2026-10-20T13:00:00-04:00"), NY)).toBe("Good afternoon");
    expect(greeting(at("2026-10-20T19:00:00-04:00"), NY)).toBe("Good evening");
    expect(greeting(at("2026-10-20T01:00:00-04:00"), NY)).toBe("Up late");
    // The same instant is a different time of day in another zone.
    expect(greeting(at("2026-10-20T08:00:00-04:00"), TOKYO)).toBe("Good evening");
  });
});

describe("relativeTime", () => {
  const now = at("2026-10-20T12:00:00-04:00");
  const rel = (iso: string) => relativeTime(iso, now, NY);
  it("counts minutes, hours and days ahead", () => {
    expect(rel("2026-10-20T12:00:20-04:00")).toBe("now");
    expect(rel("2026-10-20T12:35:00-04:00")).toBe("in 35 min");
    expect(rel("2026-10-20T14:00:00-04:00")).toBe("in 2 h");
    expect(rel("2026-10-20T13:30:00-04:00")).toBe("in 1 h 30 min");
    expect(rel("2026-10-20T17:20:00-04:00")).toBe("in 5 h");
    expect(rel("2026-10-23T12:00:00-04:00")).toBe("in 3 d");
  });

  it("counts backwards for the past", () => {
    expect(rel("2026-10-20T11:55:00-04:00")).toBe("5 min ago");
    expect(rel("2026-10-19T12:00:00-04:00")).toBe("1 d ago");
  });
});

describe("tasks", () => {
  const now = at("2026-10-20T12:00:00-04:00");
  it("knows when a due date or time has passed", () => {
    expect(isOverdue("2026-10-19", now, NY)).toBe(true);
    expect(isOverdue("2026-10-20", now, NY)).toBe(false);
    expect(isOverdue("2026-10-20T11:00:00-04:00", now, NY)).toBe(true);
    expect(isOverdue("2026-10-20T13:00:00-04:00", now, NY)).toBe(false);
    expect(isOverdue(null, now, NY)).toBe(false);
  });

  it("labels due dates", () => {
    expect(dueLabel("2026-10-20", now, NY)).toEqual({ text: "Today", overdue: false, today: true });
    expect(dueLabel("2026-10-18", now, NY)).toEqual({ text: "Sun, Oct 18", overdue: true, today: false });
    const timed = dueLabel("2026-10-21T17:00:00-04:00", now, NY);
    expect(plain(timed.text)).toBe("Tomorrow 5:00 PM");
    expect(timed.overdue).toBe(false);
  });
});

describe("native inputs", () => {
  it("splits a timestamp into date and time input values in the zone", () => {
    expect(toDateInput("2026-10-20T23:30:00-04:00", NY)).toBe("2026-10-20");
    expect(toTimeInput("2026-10-20T23:30:00-04:00", NY)).toBe("23:30");
    expect(toTimeInput("2026-10-20", NY)).toBe("");
    expect(toTimeInput("2026-10-21T02:30:00Z", NY)).toBe("22:30");
  });

  it("builds RFC 3339 with the zone's offset, through daylight saving changes", () => {
    expect(fromDateTimeInputs("2026-10-20", "09:00", NY)).toBe("2026-10-20T09:00:00-04:00");
    expect(fromDateTimeInputs("2026-11-02", "09:00", NY)).toBe("2026-11-02T09:00:00-05:00");
    expect(fromDateTimeInputs("2026-03-09", "09:00", NY)).toBe("2026-03-09T09:00:00-04:00");
    expect(fromDateTimeInputs("2026-03-07", "09:00", NY)).toBe("2026-03-07T09:00:00-05:00");
    expect(fromDateTimeInputs("2026-10-20", "09:00", TOKYO)).toBe("2026-10-20T09:00:00+09:00");
  });

  it("rejects incomplete input", () => {
    expect(fromDateTimeInputs("", "09:00", NY)).toBe("");
    expect(fromDateTimeInputs("2026-10-20", "", NY)).toBe("");
  });

  it("round-trips an instant", () => {
    const ms = at("2026-10-20T09:00:00-04:00");
    expect(toRfc3339(ms, NY)).toBe("2026-10-20T09:00:00-04:00");
    expect(toMs(toRfc3339(ms, TOKYO))).toBe(ms);
    expect(toMs("2026-10-20", NY)).toBe(at("2026-10-20T00:00:00-04:00"));
  });

  it("counts minutes until tomorrow at 9:00", () => {
    expect(minutesUntilTomorrowMorning(at("2026-10-20T20:00:00-04:00"), NY)).toBe(13 * 60);
    expect(minutesUntilTomorrowMorning(at("2026-10-20T00:30:00-04:00"), NY)).toBe(32 * 60 + 30);
  });
});

describe("groupEventsByDay", () => {
  const ev = (over: Partial<CalendarEvent>): CalendarEvent => ({
    uid: "u",
    instance: null,
    title: "Event",
    start: "2026-10-20T10:00:00-04:00",
    end: "2026-10-20T11:00:00-04:00",
    all_day: false,
    location: "",
    notes: "",
    rrule: null,
    created_by_hekate: true,
    ...over,
  });

  it("groups by local day, all-day first, then by start time", () => {
    const groups = groupEventsByDay(
      [
        ev({ uid: "b", title: "Lunch", start: "2026-10-20T12:00:00-04:00" }),
        ev({ uid: "a", title: "Standup", start: "2026-10-20T09:00:00-04:00" }),
        ev({ uid: "c", title: "Birthday", start: "2026-10-20", end: "2026-10-21", all_day: true }),
        ev({ uid: "d", title: "Dentist", start: "2026-10-22T15:00:00-04:00" }),
      ],
      NY,
    );
    expect(groups.map((g) => g.key)).toEqual(["2026-10-20", "2026-10-22"]);
    expect(groups[0].events.map((e) => e.title)).toEqual(["Birthday", "Standup", "Lunch"]);
  });

  it("uses the zone to decide the day", () => {
    const late = ev({ start: "2026-10-21T02:30:00Z", end: "2026-10-21T03:30:00Z" });
    expect(groupEventsByDay([late], NY)[0].key).toBe("2026-10-20");
    expect(groupEventsByDay([late], TOKYO)[0].key).toBe("2026-10-21");
  });

  it("spreads multi-day all-day events over their days (end is exclusive)", () => {
    const trip = ev({ title: "Trip", start: "2026-10-20", end: "2026-10-23", all_day: true });
    expect(groupEventsByDay([trip], NY).map((g) => g.key)).toEqual(["2026-10-20", "2026-10-21", "2026-10-22"]);
    const single = ev({ title: "Holiday", start: "2026-10-20", end: "2026-10-21", all_day: true });
    expect(groupEventsByDay([single], NY).map((g) => g.key)).toEqual(["2026-10-20"]);
    const noEnd = ev({ title: "Holiday", start: "2026-10-20", end: "2026-10-20", all_day: true });
    expect(groupEventsByDay([noEnd], NY).map((g) => g.key)).toEqual(["2026-10-20"]);
  });

  it("clips to a range", () => {
    const trip = ev({ start: "2026-10-20", end: "2026-10-24", all_day: true });
    expect(groupEventsByDay([trip], NY, { from: "2026-10-21", to: "2026-10-22" }).map((g) => g.key)).toEqual(["2026-10-21", "2026-10-22"]);
  });
});

describe("setZone", () => {
  it("ignores a zone the browser can't use, keeping the device's", () => {
    setZone({ tz: "Local" });
    expect(resolveZone().tz).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    setZone({ tz: "America/Chicago" });
    expect(resolveZone().tz).toBe("America/Chicago");
    setZone({});
  });
});

describe("groupEventsByDay: multi-day timed events", () => {
  const z = { tz: "America/Chicago" };
  const ev = (start: string, end: string) =>
    ({ uid: "u", instance: null, title: "Trip", start, end, all_day: false, location: "", notes: "", rrule: null, created_by_hekate: true }) as CalendarEvent;

  it("shows an ongoing timed event on each day it overlaps", () => {
    const groups = groupEventsByDay([ev("2026-10-19T18:00:00-05:00", "2026-10-21T10:00:00-05:00")], z);
    expect(groups.map((g) => g.key)).toEqual(["2026-10-19", "2026-10-20", "2026-10-21"]);
  });

  it("doesn't spill into the next day when it ends at midnight", () => {
    const groups = groupEventsByDay([ev("2026-10-19T22:00:00-05:00", "2026-10-20T00:00:00-05:00")], z);
    expect(groups.map((g) => g.key)).toEqual(["2026-10-19"]);
  });
});
