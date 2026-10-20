import { describe, expect, it } from "vitest";
import { asApproval, asApprovalRule, asEvent, asMailRule, asMailSummary, asMemory, asMessage, asReminder, asTask, asWeather, parseCard, safeUrl } from "./model";

const task = { id: "12", title: "Oat milk", notes: "", list: "Groceries", due: "2026-10-20", rrule: null, priority: 0, completed_at: null, created_at: "x", updated_at: "y" };
const reminder = { id: "7", text: "Call the dentist", at: "2026-10-20T09:00:00-05:00", rrule: null, status: "scheduled", last_fired_at: null, created_at: "x" };
const event = {
  uid: "abc",
  instance: "2026-10-20T15:00:00-05:00",
  title: "Dentist",
  start: "2026-10-20T15:00:00-05:00",
  end: "2026-10-20T16:00:00-05:00",
  all_day: false,
  location: "",
  notes: "",
  rrule: null,
  created_by_hekate: true,
};
const memory = { id: "3", kind: "preference", content: "Takes coffee black", importance: 3, follow_up_at: null, created_at: "x", updated_at: "y" };
const weather = {
  place: "Lisbon",
  unit: "C",
  now: { temp: 21.4, feels_like: 20, condition: "Partly cloudy", code: 2 },
  today: { high: 24, low: 16, precip_chance: 10, sunrise: "2026-10-20T07:45:00+01:00", sunset: "2026-10-20T18:55:00+01:00" },
  hours: [{ time: "2026-10-20T14:00:00+01:00", temp: 22, precip_chance: 5, code: 1 }],
  days: [{ date: "2026-10-21", high: 23, low: 15, precip_chance: 40, code: 61 }],
};

const message = {
  id: "381",
  account: "work",
  from: { name: "Shopify", address: "no-reply@example.com" },
  subject: "Your weekly update",
  date: "2026-10-19T08:12:00-05:00",
  folder: "INBOX",
  unread: true,
  category: "newsletter",
  gist: "Product news and a sale",
  can_unsubscribe: true,
};
const approval = {
  id: "5",
  kind: "mail_trash",
  title: "Move 14 newsletters from example.com to Trash?",
  detail: "",
  scope_label: "mail from example.com",
  status: "pending",
  result: null,
  decided_by: null,
  note: null,
  created_at: "2026-10-19T08:00:00Z",
  expires_at: "2026-10-20T08:00:00Z",
  decided_at: null,
};

const card = (kind: string, data: unknown, extra: object = {}) => ({ kind, id: "c1", title: "A card", data, ...extra });

describe("entity guards", () => {
  it("reads a task and fills the optional fields", () => {
    expect(asTask(task)).toEqual(task);
    expect(asTask({ id: 5, title: "x" })).toMatchObject({ id: "5", notes: "", list: "Inbox", due: null, rrule: null, priority: 0, completed_at: null });
  });

  it("clamps priority and drops entries missing what the UI needs", () => {
    expect(asTask({ ...task, priority: 9 })?.priority).toBe(3);
    expect(asTask({ title: "no id" })).toBeUndefined();
    expect(asTask({ id: "1" })).toBeUndefined();
    expect(asTask(null)).toBeUndefined();
    expect(asTask([])).toBeUndefined();
  });

  it("reads reminders, events and memories", () => {
    expect(asReminder(reminder)).toEqual(reminder);
    expect(asReminder({ ...reminder, status: "weird" })?.status).toBe("scheduled");
    expect(asReminder({ id: "1", text: "x" })).toBeUndefined();
    expect(asEvent(event)).toEqual(event);
    expect(asEvent({ ...event, end: undefined })?.end).toBe(event.start);
    expect(asEvent({ ...event, uid: undefined })).toBeUndefined();
    expect(asMemory(memory)).toEqual(memory);
    expect(asMemory({ ...memory, importance: 99 })?.importance).toBe(5);
    expect(asMemory({ id: "1" })).toBeUndefined();
  });

  it("accepts null for values the forecast doesn't have", () => {
    const sparse = {
      ...weather,
      now: { ...weather.now, feels_like: null },
      today: { ...weather.today, precip_chance: null },
      hours: [{ time: "2026-10-20T14:00:00+01:00", temp: 22, precip_chance: null, code: 1 }],
      days: [{ date: "2026-10-21", high: 23, low: 15, precip_chance: null, code: 61 }],
    };
    expect(asWeather(sparse)).toEqual(sparse);
  });

  it("reads weather and rejects an incomplete one", () => {
    expect(asWeather(weather)).toEqual(weather);
    expect(asWeather({ ...weather, now: { temp: 1 } })).toBeUndefined();
    expect(asWeather({ ...weather, today: undefined })).toBeUndefined();
    expect(asWeather({ ...weather, hours: [{ time: "x" }] })?.hours).toEqual([]);
  });
});

describe("safeUrl", () => {
  it("allows only http and https", () => {
    expect(safeUrl("https://example.com/a")).toBe("https://example.com/a");
    expect(safeUrl("http://example.com")).toBe("http://example.com/");
    expect(safeUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeUrl("data:text/html,hi")).toBeUndefined();
    expect(safeUrl("not a url")).toBeUndefined();
    expect(safeUrl(5)).toBeUndefined();
  });
});

describe("parseCard", () => {
  it("parses a tasks card with highlights", () => {
    const parsed = parseCard(card("tasks", { items: [task, { bad: true }], highlight: ["12", 13] }));
    expect(parsed).toMatchObject({ kind: "tasks", id: "c1", title: "A card" });
    expect(parsed?.kind === "tasks" && parsed.data).toEqual({ items: [task], highlight: ["12", "13"] });
  });

  it("parses a reminders card", () => {
    const parsed = parseCard(card("reminders", { items: [reminder] }));
    expect(parsed?.kind === "reminders" && parsed.data).toEqual({ items: [reminder], highlight: [] });
  });

  it("parses an events card", () => {
    const parsed = parseCard(card("events", { from: "2026-10-20", to: "2026-10-27", items: [event], highlight: ["abc"] }));
    expect(parsed?.kind === "events" && parsed.data).toEqual({ from: "2026-10-20", to: "2026-10-27", items: [event], highlight: ["abc"] });
  });

  it("parses a memory card", () => {
    const parsed = parseCard(card("memory", { items: [memory] }));
    expect(parsed?.kind === "memory" && parsed.data.items).toEqual([memory]);
  });

  it("parses a weather card, and ignores one without current conditions", () => {
    const parsed = parseCard(card("weather", weather));
    expect(parsed?.kind === "weather" && parsed.data).toEqual(weather);
    expect(parseCard(card("weather", { place: "x" }))).toBeUndefined();
  });

  it("parses web results, keeping only links it can open safely", () => {
    const parsed = parseCard(
      card("web_results", {
        query: "ferry times",
        items: [
          { title: "Timetable", url: "https://ferry.example/times", snippet: "Daily sailings", source: "ferry.example" },
          { title: "Sketchy", url: "javascript:alert(1)", snippet: "", source: "" },
          { title: "No url" },
        ],
      }),
    );
    expect(parsed?.kind === "web_results" && parsed.data).toEqual({
      query: "ferry times",
      items: [{ title: "Timetable", url: "https://ferry.example/times", snippet: "Daily sailings", source: "ferry.example" }],
    });
  });

  it("parses a web page card, but not one with an unsafe link", () => {
    const data = { title: "Ferry times", url: "https://ferry.example/times", source: "ferry.example", excerpt: "Sailings run daily." };
    const parsed = parseCard(card("web_page", data));
    expect(parsed?.kind === "web_page" && parsed.data).toEqual(data);
    expect(parseCard(card("web_page", { ...data, url: "javascript:1" }))).toBeUndefined();
  });

  it("ignores unknown kinds and malformed cards", () => {
    expect(parseCard(card("hologram", {}))).toBeUndefined();
    expect(parseCard(card("tasks", null))).toBeUndefined();
    expect(parseCard({ kind: "tasks", title: "no id", data: { items: [] } })).toBeUndefined();
    expect(parseCard("tasks")).toBeUndefined();
    expect(parseCard(undefined)).toBeUndefined();
  });

  it("accepts a numeric id and a missing title", () => {
    expect(parseCard({ kind: "memory", id: 4, data: { items: [] } })).toMatchObject({ id: "4", title: "" });
  });

  it("parses mail, mail message and approval cards", () => {
    const mail = parseCard(card("mail", { items: [message, { nope: 1 }], highlight: [381] }));
    expect(mail?.kind === "mail" && mail.data).toEqual({ items: [message], highlight: ["381"] });
    const one = parseCard(card("mail_message", { message, excerpt: "Hello" }));
    expect(one?.kind === "mail_message" && one.data).toEqual({ message, excerpt: "Hello" });
    expect(parseCard(card("mail_message", { excerpt: "no message" }))).toBeUndefined();
    const a = parseCard(card("approval", approval, { id: "approval-5" }));
    expect(a?.kind === "approval" && a.data).toEqual(approval);
    expect(parseCard(card("approval", { ...approval, status: "weird" }))).toBeUndefined();
  });
});

describe("mail and approval guards", () => {
  it("fills what a message may omit", () => {
    expect(asMessage({ id: 7, subject: "Hi", from: { address: "a@example.com" } })).toMatchObject({
      id: "7",
      from: { name: "", address: "a@example.com" },
      folder: "INBOX",
      unread: false,
      category: null,
      gist: null,
    });
    expect(asMessage({ subject: "no id" })).toBeUndefined();
  });

  it("reads rules, the summary and approval rules", () => {
    const rule = { id: "4", match: { from: "@example.com", category: null, subject: "upgrade", body: null }, except: [{ from: null, category: "receipt", subject: null }], action: "trash", folder: null, label: 'Trash mail from example.com with "upgrade" in the subject', hits: 12, last_hit_at: null, created_at: "x" };
    expect(asMailRule(rule)).toEqual(rule);
    // A rule from before subject and body words reads as having none.
    expect(asMailRule({ ...rule, match: { from: "@example.com", category: null } })?.match).toEqual({ from: "@example.com", category: null, subject: null, body: null });
    // ...and one from before exceptions as sparing nothing; junk entries are dropped.
    expect(asMailRule({ ...rule, except: undefined })?.except).toEqual([]);
    expect(asMailRule({ ...rule, except: ["x", { subject: "transaction" }] })?.except).toEqual([{ from: null, category: null, subject: "transaction" }]);
    expect(asMailRule({ id: "4" })).toBeUndefined();
    expect(asMailSummary({ unread: { newsletter: 3, promo: 0, bad: "x" }, attention: [message] })).toEqual({ unread: { newsletter: 3 }, attention: [message] });
    expect(asMailSummary(null)).toEqual({ unread: {}, attention: [] });
    expect(asApprovalRule({ id: "2", kind: "mail_trash", scope_label: "mail from example.com", uses: 3, created_at: "x" })).toMatchObject({ uses: 3 });
  });

  it("reads every approval status, including running", () => {
    for (const status of ["pending", "running", "done", "failed", "denied", "expired"]) expect(asApproval({ ...approval, status })?.status).toBe(status);
    expect(asApproval({ ...approval, scope_label: null })?.scope_label).toBeNull();
  });
});
