// Domain shapes of docs/PROTOCOL.md (tasks, reminders, calendar, memory, weather, cards) and the
// guards that read them from untrusted JSON. The guards are lenient about optional fields (a Go
// service may omit empty ones) and strict about the fields the UI can't do without; an entry that
// fails is dropped rather than failing the whole list.

export interface Task {
  id: string;
  title: string;
  notes: string;
  list: string;
  due: string | null; // null, a YYYY-MM-DD date (all day) or an RFC 3339 time
  rrule: string | null;
  priority: number; // 0 none, 1 low, 2 medium, 3 high
  completed_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface TaskList {
  name: string;
  open_count: number;
}

export type ReminderStatus = "scheduled" | "fired" | "done" | "cancelled";
export interface Reminder {
  id: string;
  text: string;
  at: string;
  rrule: string | null;
  status: ReminderStatus;
  last_fired_at: string | null;
  created_at: string;
}

export interface CalendarEvent {
  uid: string;
  instance: string | null;
  title: string;
  start: string; // a date for all-day events
  end: string;
  all_day: boolean;
  location: string;
  notes: string;
  rrule: string | null;
  created_by_hekate: boolean;
}

export const MEMORY_KINDS = ["fact", "preference", "person", "event", "follow_up"] as const;
export type MemoryKind = (typeof MEMORY_KINDS)[number];
export interface Memory {
  id: string;
  kind: string; // normally a MemoryKind; kept as sent so a newer kind still shows
  content: string;
  importance: number;
  follow_up_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface CoreEntry {
  key: string;
  value: string;
  updated_at: string;
}

export interface WeatherNow {
  temp: number;
  feels_like: number | null;
  condition: string;
  code: number;
}
export interface WeatherToday {
  high: number;
  low: number;
  precip_chance: number | null; // percent; null when the forecast has none
  sunrise: string;
  sunset: string;
}
export interface WeatherHour {
  time: string;
  temp: number;
  precip_chance: number | null;
  code: number;
}
export interface WeatherDay {
  date: string;
  high: number;
  low: number;
  precip_chance: number | null;
  code: number;
}
export interface Weather {
  place: string;
  unit: "F" | "C";
  now: WeatherNow;
  today: WeatherToday;
  hours: WeatherHour[];
  days: WeatherDay[];
}

export interface WebResult {
  title: string;
  url: string;
  snippet: string;
  source: string;
}

// --- primitives ----------------------------------------------------------------------------

type Rec = Record<string, unknown>;
export const isRec = (v: unknown): v is Rec => typeof v === "object" && v !== null && !Array.isArray(v);
const isStr = (v: unknown): v is string => typeof v === "string";
const isNum = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

function idOf(v: unknown): string | undefined {
  if (isStr(v) && v !== "") return v;
  if (isNum(v)) return String(v);
  return undefined;
}
const strOr = (v: unknown, fallback = ""): string => (isStr(v) ? v : fallback);
const strOrNull = (v: unknown): string | null => (isStr(v) && v !== "" ? v : null);
const numOr = (v: unknown, fallback: number): number => (isNum(v) ? v : fallback);
const numOrNull = (v: unknown): number | null => (isNum(v) ? v : null);

// Only http(s) links are ever rendered as links.
export function safeUrl(v: unknown): string | undefined {
  if (!isStr(v)) return undefined;
  try {
    const u = new URL(v);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}

export function list<T>(value: unknown, parse: (v: unknown) => T | undefined): T[] {
  if (!Array.isArray(value)) return [];
  const out: T[] = [];
  for (const v of value) {
    const parsed = parse(v);
    if (parsed !== undefined) out.push(parsed);
  }
  return out;
}

// --- entities ------------------------------------------------------------------------------

export function asTask(v: unknown): Task | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.title)) return undefined;
  return {
    id,
    title: v.title,
    notes: strOr(v.notes),
    list: strOr(v.list, "Inbox") || "Inbox",
    due: strOrNull(v.due),
    rrule: strOrNull(v.rrule),
    priority: Math.min(3, Math.max(0, Math.round(numOr(v.priority, 0)))),
    completed_at: strOrNull(v.completed_at),
    created_at: strOr(v.created_at),
    updated_at: strOr(v.updated_at),
  };
}

export function asTaskList(v: unknown): TaskList | undefined {
  if (!isRec(v) || !isStr(v.name)) return undefined;
  return { name: v.name, open_count: Math.max(0, numOr(v.open_count, 0)) };
}

const REMINDER_STATUSES: readonly string[] = ["scheduled", "fired", "done", "cancelled"];
export function asReminder(v: unknown): Reminder | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.text) || !isStr(v.at)) return undefined;
  return {
    id,
    text: v.text,
    at: v.at,
    rrule: strOrNull(v.rrule),
    status: (REMINDER_STATUSES.includes(v.status as string) ? v.status : "scheduled") as ReminderStatus,
    last_fired_at: strOrNull(v.last_fired_at),
    created_at: strOr(v.created_at),
  };
}

export function asEvent(v: unknown): CalendarEvent | undefined {
  if (!isRec(v)) return undefined;
  const uid = idOf(v.uid);
  if (uid === undefined || !isStr(v.title) || !isStr(v.start) || v.start === "") return undefined;
  return {
    uid,
    instance: strOrNull(v.instance),
    title: v.title,
    start: v.start,
    end: strOr(v.end, v.start) || v.start,
    all_day: v.all_day === true,
    location: strOr(v.location),
    notes: strOr(v.notes),
    rrule: strOrNull(v.rrule),
    created_by_hekate: v.created_by_hekate === true,
  };
}

export function asMemory(v: unknown): Memory | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.content)) return undefined;
  return {
    id,
    kind: strOr(v.kind, "fact") || "fact",
    content: v.content,
    importance: Math.min(5, Math.max(1, Math.round(numOr(v.importance, 3)))),
    follow_up_at: strOrNull(v.follow_up_at),
    created_at: strOr(v.created_at),
    updated_at: strOr(v.updated_at),
  };
}

export function asCoreEntry(v: unknown): CoreEntry | undefined {
  if (!isRec(v) || !isStr(v.key) || v.key === "" || !isStr(v.value)) return undefined;
  return { key: v.key, value: v.value, updated_at: strOr(v.updated_at) };
}

export function asWebResult(v: unknown): WebResult | undefined {
  if (!isRec(v) || !isStr(v.title)) return undefined;
  const url = safeUrl(v.url);
  if (!url) return undefined;
  return { title: v.title, url, snippet: strOr(v.snippet), source: strOr(v.source) };
}

function asHour(v: unknown): WeatherHour | undefined {
  if (!isRec(v) || !isStr(v.time) || !isNum(v.temp) || !isNum(v.code)) return undefined;
  return { time: v.time, temp: v.temp, precip_chance: numOrNull(v.precip_chance), code: v.code };
}

function asDay(v: unknown): WeatherDay | undefined {
  if (!isRec(v) || !isStr(v.date) || !isNum(v.high) || !isNum(v.low) || !isNum(v.code)) return undefined;
  return { date: v.date, high: v.high, low: v.low, precip_chance: numOrNull(v.precip_chance), code: v.code };
}

export function asWeather(v: unknown): Weather | undefined {
  if (!isRec(v) || !isRec(v.now) || !isRec(v.today)) return undefined;
  const { now, today } = v;
  if (!isNum(now.temp) || !isNum(now.code) || !isNum(today.high) || !isNum(today.low)) return undefined;
  return {
    place: strOr(v.place),
    unit: v.unit === "C" ? "C" : "F",
    now: { temp: now.temp, feels_like: numOrNull(now.feels_like), condition: strOr(now.condition), code: now.code },
    today: {
      high: today.high,
      low: today.low,
      precip_chance: numOrNull(today.precip_chance),
      sunrise: strOr(today.sunrise),
      sunset: strOr(today.sunset),
    },
    hours: list(v.hours, asHour),
    days: list(v.days, asDay),
  };
}

// --- cards ---------------------------------------------------------------------------------

export interface TasksCardData {
  items: Task[];
  highlight: string[];
}
export interface RemindersCardData {
  items: Reminder[];
  highlight: string[];
}
export interface EventsCardData {
  from: string;
  to: string;
  items: CalendarEvent[];
  highlight: string[];
}
export interface MemoryCardData {
  items: Memory[];
}
export interface WebResultsCardData {
  query: string;
  items: WebResult[];
}
export interface WebPageCardData {
  title: string;
  url: string;
  source: string;
  excerpt: string;
}

interface CardBase {
  id: string;
  title: string;
}
export type Card =
  | (CardBase & { kind: "tasks"; data: TasksCardData })
  | (CardBase & { kind: "reminders"; data: RemindersCardData })
  | (CardBase & { kind: "events"; data: EventsCardData })
  | (CardBase & { kind: "memory"; data: MemoryCardData })
  | (CardBase & { kind: "weather"; data: Weather })
  | (CardBase & { kind: "web_results"; data: WebResultsCardData })
  | (CardBase & { kind: "web_page"; data: WebPageCardData });
export type CardKind = Card["kind"];

function highlights(v: unknown): string[] {
  return list(v, idOf);
}

// Reads a `card` event's payload. Unknown kinds and malformed data give undefined (ignored).
export function parseCard(raw: unknown): Card | undefined {
  if (!isRec(raw) || !isStr(raw.kind) || !isRec(raw.data)) return undefined;
  const id = idOf(raw.id);
  if (id === undefined) return undefined;
  const base = { id, title: strOr(raw.title) };
  const d = raw.data;
  switch (raw.kind) {
    case "tasks":
      return { ...base, kind: "tasks", data: { items: list(d.items, asTask), highlight: highlights(d.highlight) } };
    case "reminders":
      return { ...base, kind: "reminders", data: { items: list(d.items, asReminder), highlight: highlights(d.highlight) } };
    case "events":
      return {
        ...base,
        kind: "events",
        data: { from: strOr(d.from), to: strOr(d.to), items: list(d.items, asEvent), highlight: highlights(d.highlight) },
      };
    case "memory":
      return { ...base, kind: "memory", data: { items: list(d.items, asMemory) } };
    case "weather": {
      const weather = asWeather(d);
      return weather && { ...base, kind: "weather", data: weather };
    }
    case "web_results":
      return { ...base, kind: "web_results", data: { query: strOr(d.query), items: list(d.items, asWebResult) } };
    case "web_page": {
      const url = safeUrl(d.url);
      if (!url || !isStr(d.title)) return undefined;
      return { ...base, kind: "web_page", data: { title: d.title, url, source: strOr(d.source), excerpt: strOr(d.excerpt) } };
    }
    default:
      return undefined;
  }
}
