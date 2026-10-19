import type { CalendarEvent } from "./model";

// Date and time helpers. Everything is computed in one IANA zone (the configured zone, by
// default the device's) with Intl, never by shifting Dates by hand, so DST and zone differences
// are handled by the platform. Day keys are "YYYY-MM-DD" strings; they sort and compare as text.

export interface Zone {
  tz?: string;
  locale?: string;
}

let configured: Zone = {};

// The zone and locale the app displays in. Unset values fall back to the device's.
export function setZone(zone: Zone) {
  // Only zones this browser can use; otherwise keep the device's.
  if (zone.tz) {
    try {
      new Intl.DateTimeFormat("en-US", { timeZone: zone.tz });
    } catch {
      zone = { ...zone, tz: undefined };
    }
  }
  configured = zone;
}

export function resolveZone(z: Zone = {}): { tz: string; locale: string | undefined } {
  return {
    tz: z.tz ?? configured.tz ?? Intl.DateTimeFormat().resolvedOptions().timeZone,
    locale: z.locale ?? configured.locale,
  };
}

const MINUTE = 60_000;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

export const isDateOnly = (s: string): boolean => DATE_ONLY.test(s);

interface Parts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
  weekday: number; // 0 = Sunday
}

const partsFormats = new Map<string, Intl.DateTimeFormat>();
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function partsIn(ms: number, tz: string): Parts {
  let fmt = partsFormats.get(tz);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      weekday: "short",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
      second: "numeric",
    });
    partsFormats.set(tz, fmt);
  }
  const out: Record<string, string> = {};
  for (const p of fmt.formatToParts(new Date(ms))) out[p.type] = p.value;
  return {
    year: Number(out.year),
    month: Number(out.month),
    day: Number(out.day),
    hour: Number(out.hour) % 24,
    minute: Number(out.minute),
    second: Number(out.second),
    weekday: WEEKDAYS.indexOf(out.weekday),
  };
}

const pad = (n: number, width = 2) => String(n).padStart(width, "0");
const key = (y: number, m: number, d: number) => `${pad(y, 4)}-${pad(m)}-${pad(d)}`;

function parseKey(k: string): [number, number, number] {
  const [y, m, d] = k.split("-").map(Number);
  return [y, m, d];
}

// --- instants and zones --------------------------------------------------------------------

// Offset of `tz` from UTC at that instant, in minutes (east positive).
export function tzOffsetMinutes(ms: number, tz: string): number {
  const p = partsIn(ms, tz);
  const asUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  return Math.round((asUtc - Math.floor(ms / 1000) * 1000) / MINUTE);
}

// The instant at which the wall clock in `tz` reads y-m-d h:mi.
export function zonedToMs(y: number, mo: number, d: number, h: number, mi: number, tz: string): number {
  const guess = Date.UTC(y, mo - 1, d, h, mi);
  const first = guess - tzOffsetMinutes(guess, tz) * MINUTE;
  return guess - tzOffsetMinutes(first, tz) * MINUTE;
}

function formatOffset(minutes: number): string {
  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  return `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`;
}

// RFC 3339 with the zone's offset, e.g. 2026-10-20T09:00:00-04:00.
export function toRfc3339(ms: number, z: Zone = {}): string {
  const { tz } = resolveZone(z);
  const p = partsIn(ms, tz);
  return `${key(p.year, p.month, p.day)}T${pad(p.hour)}:${pad(p.minute)}:${pad(p.second)}${formatOffset(tzOffsetMinutes(ms, tz))}`;
}

// Start of the day for a date-only value, or the instant for a full timestamp.
export function toMs(s: string, z: Zone = {}): number {
  if (isDateOnly(s)) {
    const [y, m, d] = parseKey(s);
    return zonedToMs(y, m, d, 0, 0, resolveZone(z).tz);
  }
  return Date.parse(s);
}

// --- day keys ------------------------------------------------------------------------------

export function dayKeyOfMs(ms: number, z: Zone = {}): string {
  const p = partsIn(ms, resolveZone(z).tz);
  return key(p.year, p.month, p.day);
}

// The calendar day a date or timestamp falls on in the zone.
export function dayKey(s: string, z: Zone = {}): string {
  return isDateOnly(s) ? s : dayKeyOfMs(Date.parse(s), z);
}

export function addDays(dayKeyValue: string, n: number): string {
  const [y, m, d] = parseKey(dayKeyValue);
  const t = new Date(Date.UTC(y, m - 1, d + n));
  return key(t.getUTCFullYear(), t.getUTCMonth() + 1, t.getUTCDate());
}

export function daysBetween(a: string, b: string): number {
  const [ay, am, ad] = parseKey(a);
  const [by, bm, bd] = parseKey(b);
  return Math.round((Date.UTC(by, bm - 1, bd) - Date.UTC(ay, am - 1, ad)) / 86_400_000);
}

export function weekdayOf(dayKeyValue: string): number {
  const [y, m, d] = parseKey(dayKeyValue);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
}

export function monthStart(dayKeyValue: string): string {
  return `${dayKeyValue.slice(0, 7)}-01`;
}

export function addMonths(dayKeyValue: string, n: number): string {
  const [y, m] = parseKey(dayKeyValue);
  const t = new Date(Date.UTC(y, m - 1 + n, 1));
  return key(t.getUTCFullYear(), t.getUTCMonth() + 1, 1);
}

export function daysInMonth(dayKeyValue: string): number {
  const [y, m] = parseKey(dayKeyValue);
  return new Date(Date.UTC(y, m, 0)).getUTCDate();
}

// --- display -------------------------------------------------------------------------------

function onDay(dayKeyValue: string, z: Zone, options: Intl.DateTimeFormatOptions): string {
  const [y, m, d] = parseKey(dayKeyValue);
  return new Intl.DateTimeFormat(resolveZone(z).locale, { ...options, timeZone: "UTC" }).format(new Date(Date.UTC(y, m - 1, d, 12)));
}

// "Wed, Oct 21"
export function formatDay(dayKeyValue: string, z: Zone = {}): string {
  return onDay(dayKeyValue, z, { weekday: "short", month: "short", day: "numeric" });
}

// "Wed"
export function formatWeekday(dayKeyValue: string, z: Zone = {}): string {
  return onDay(dayKeyValue, z, { weekday: "short" });
}

// "Wednesday, October 21"
export function formatDayLong(dayKeyValue: string, z: Zone = {}): string {
  return onDay(dayKeyValue, z, { weekday: "long", month: "long", day: "numeric" });
}

// "October 2026"
export function formatMonth(dayKeyValue: string, z: Zone = {}): string {
  return onDay(dayKeyValue, z, { month: "long", year: "numeric" });
}

// "Today", "Tomorrow", "Yesterday", otherwise "Wed, Oct 21".
export function dayHeading(dayKeyValue: string, todayKeyValue: string, z: Zone = {}): string {
  const diff = daysBetween(todayKeyValue, dayKeyValue);
  if (diff === 0) return "Today";
  if (diff === 1) return "Tomorrow";
  if (diff === -1) return "Yesterday";
  return formatDay(dayKeyValue, z);
}

export function formatTime(iso: string, z: Zone = {}): string {
  const { tz, locale } = resolveZone(z);
  return new Intl.DateTimeFormat(locale, { hour: "numeric", minute: "2-digit", timeZone: tz }).format(new Date(iso));
}

// "Wed, Oct 21, 3:00 PM" (or without the time for date-only values).
export function formatDateTime(s: string, z: Zone = {}): string {
  if (isDateOnly(s)) return formatDay(s, z);
  return `${formatDay(dayKey(s, z), z)}, ${formatTime(s, z)}`;
}

// "3:00 – 4:30 PM" style range for a timed event; "All day" for all-day ones.
export function eventTimeLabel(e: Pick<CalendarEvent, "start" | "end" | "all_day">, z: Zone = {}): string {
  if (e.all_day || isDateOnly(e.start)) return "All day";
  const start = formatTime(e.start, z);
  if (!e.end || e.end === e.start) return start;
  return `${start} – ${formatTime(e.end, z)}`;
}

// "in 2 h", "in 35 min", "in 3 d", "5 min ago", "now".
export function relativeTime(target: string, nowMs: number, z: Zone = {}): string {
  const minutes = Math.round((toMs(target, z) - nowMs) / MINUTE);
  const future = minutes >= 0;
  const m = Math.abs(minutes);
  let span: string;
  if (m < 1) return "now";
  if (m < 60) span = `${m} min`;
  else if (m < 180) span = m % 60 === 0 ? `${m / 60} h` : `${Math.floor(m / 60)} h ${m % 60} min`;
  else if (m < 24 * 60) span = `${Math.round(m / 60)} h`;
  else span = `${Math.round(m / 1440)} d`;
  return future ? `in ${span}` : `${span} ago`;
}

export function greeting(nowMs: number, z: Zone = {}): string {
  const { hour } = partsIn(nowMs, resolveZone(z).tz);
  if (hour >= 5 && hour < 12) return "Good morning";
  if (hour >= 12 && hour < 17) return "Good afternoon";
  if (hour >= 17 && hour < 22) return "Good evening";
  return "Up late";
}

// --- tasks ---------------------------------------------------------------------------------

export function isOverdue(due: string | null, nowMs: number, z: Zone = {}): boolean {
  if (!due) return false;
  return isDateOnly(due) ? due < dayKeyOfMs(nowMs, z) : Date.parse(due) < nowMs;
}

export interface DueLabel {
  text: string;
  overdue: boolean;
  today: boolean;
}

// "Today", "Tomorrow 5:00 PM", "Oct 18" with whether it's late.
export function dueLabel(due: string, nowMs: number, z: Zone = {}): DueLabel {
  const today = dayKeyOfMs(nowMs, z);
  const dk = dayKey(due, z);
  const heading = dayHeading(dk, today, z);
  const time = isDateOnly(due) ? "" : ` ${formatTime(due, z)}`;
  return { text: `${heading}${time}`, overdue: isOverdue(due, nowMs, z), today: dk === today };
}

// --- native inputs -------------------------------------------------------------------------

// Value for <input type="date"> and <input type="time"> from a date or timestamp.
export function toDateInput(s: string, z: Zone = {}): string {
  return dayKey(s, z);
}

export function toTimeInput(s: string, z: Zone = {}): string {
  if (isDateOnly(s)) return "";
  const p = partsIn(Date.parse(s), resolveZone(z).tz);
  return `${pad(p.hour)}:${pad(p.minute)}`;
}

// Combines the date and time inputs into an RFC 3339 timestamp in the zone; "" if no date.
export function fromDateTimeInputs(date: string, time: string, z: Zone = {}): string {
  if (!DATE_ONLY.test(date)) return "";
  const m = /^(\d{2}):(\d{2})/.exec(time);
  if (!m) return "";
  const [y, mo, d] = parseKey(date);
  return toRfc3339(zonedToMs(y, mo, d, Number(m[1]), Number(m[2]), resolveZone(z).tz), z);
}

// Minutes from `nowMs` until 9:00 tomorrow in the zone (for "snooze until tomorrow morning").
export function minutesUntilTomorrowMorning(nowMs: number, z: Zone = {}): number {
  const [y, m, d] = parseKey(addDays(dayKeyOfMs(nowMs, z), 1));
  return Math.max(1, Math.ceil((zonedToMs(y, m, d, 9, 0, resolveZone(z).tz) - nowMs) / MINUTE));
}

// --- agenda --------------------------------------------------------------------------------

export interface DayGroup {
  key: string;
  events: CalendarEvent[];
}

const MAX_SPAN_DAYS = 31;

// Days an event shows on. Timed events belong to the day they start. All-day ends follow the
// iCalendar convention (exclusive), so a one-day event ends the next day.
function eventDays(e: CalendarEvent, z: Zone): string[] {
  const start = dayKey(e.start, z);
  if (!e.all_day && !isDateOnly(e.start)) return [start];
  const end = e.end ? dayKey(e.end, z) : start;
  const span = Math.min(MAX_SPAN_DAYS, Math.max(1, daysBetween(start, end)));
  return Array.from({ length: span }, (_, i) => addDays(start, i));
}

export function compareEvents(a: CalendarEvent, b: CalendarEvent, z: Zone = {}): number {
  const aAll = a.all_day || isDateOnly(a.start);
  const bAll = b.all_day || isDateOnly(b.start);
  if (aAll !== bAll) return aAll ? -1 : 1;
  return toMs(a.start, z) - toMs(b.start, z) || a.title.localeCompare(b.title);
}

// Events grouped by day in the zone, days ascending, all-day events first in each day.
// `from`/`to` (inclusive day keys) clip the groups.
export function groupEventsByDay(events: CalendarEvent[], z: Zone = {}, range: { from?: string; to?: string } = {}): DayGroup[] {
  const groups = new Map<string, CalendarEvent[]>();
  for (const e of events) {
    for (const k of eventDays(e, z)) {
      if ((range.from && k < range.from) || (range.to && k > range.to)) continue;
      const bucket = groups.get(k);
      if (bucket) bucket.push(e);
      else groups.set(k, [e]);
    }
  }
  return [...groups.keys()]
    .sort()
    .map((k) => ({ key: k, events: groups.get(k)!.sort((a, b) => compareEvents(a, b, z)) }));
}
