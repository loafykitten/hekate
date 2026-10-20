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
  version?: string; // sent back with edits so a stale edit is refused, not applied on top
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

export interface MailAddress {
  name: string;
  address: string;
}

export const MAIL_CATEGORIES = ["needs_reply", "important", "personal", "receipt", "notification", "newsletter", "promo"] as const;
export type MailCategory = (typeof MAIL_CATEGORIES)[number];
export interface Message {
  id: string;
  account: string;
  from: MailAddress;
  subject: string;
  date: string;
  folder: string; // INBOX, Archive, Trash or another folder
  unread: boolean;
  category: string | null; // normally a MailCategory; null until classified
  gist: string | null;
  can_unsubscribe: boolean;
}

export interface MailRule {
  id: string;
  match: { from: string | null; category: string | null; subject: string | null; body: string | null };
  except: { from: string | null; category: string | null; subject: string | null }[]; // mail the rule spares
  action: string; // archive | trash | mark_read | move | unsubscribe
  folder: string | null;
  label: string;
  hits: number;
  last_hit_at: string | null;
  created_at: string;
}

export interface MailAccountStatus {
  name: string;
  address: string;
  ok: boolean;
  error: string;
  last_sweep_at: string | null;
  inbox_count: number;
}
export interface MailStatus {
  configured: boolean;
  accounts: MailAccountStatus[];
}
export interface MailSummary {
  unread: Record<string, number>;
  attention: Message[];
}
export interface MailBody {
  text: string;
  truncated: boolean;
}

export const APPROVAL_STATUSES = ["pending", "running", "done", "failed", "denied", "expired"] as const;
export type ApprovalStatus = (typeof APPROVAL_STATUSES)[number];
export interface Approval {
  id: string;
  kind: string; // the action, e.g. mail_trash
  title: string;
  detail: string;
  scope_label: string | null; // what "Always" covers; null when it can't become a rule
  status: ApprovalStatus;
  result: string | null;
  decided_by: string | null; // owner | rule
  note: string | null; // the owner's reason for a denial, if they gave one
  created_at: string;
  expires_at: string | null;
  decided_at: string | null;
}
export interface ApprovalRule {
  id: string;
  kind: string;
  scope_label: string;
  uses: number;
  created_at: string;
}

export const CHANGE_STATUSES = ["proposed", "denied", "expired", "queued", "working", "merged", "rejected", "failed"] as const;
export type ChangeStatus = (typeof CHANGE_STATUSES)[number];
export interface ChangeRequest {
  id: string;
  title: string;
  what: string; // what to build, in her words
  why: string;
  kind: string; // feature | fix | improvement; kept as sent so a newer kind still shows
  revises: string | null; // id of the denied proposal this replaces
  status: ChangeStatus;
  approval_id: string | null;
  feedback: string | null; // the owner's note when denied
  stage: string; // the latest step while it's built, e.g. "Building"
  issue_url: string | null;
  pr_url: string | null;
  result: string | null; // once finished: what happened
  attempts: number; // times it has been taken up
  previous_result: string | null; // after a retry: how the last attempt ended
  previous_pr_url: string | null;
  created_at: string;
  updated_at: string;
  finished_at: string | null;
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
    version: isStr(v.version) && v.version !== "" ? v.version : undefined,
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

function asAddress(v: unknown): MailAddress {
  if (!isRec(v)) return { name: "", address: "" };
  return { name: strOr(v.name).trim(), address: strOr(v.address).trim() };
}

export function asMessage(v: unknown): Message | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || (!isStr(v.subject) && !isStr(v.date))) return undefined;
  return {
    id,
    account: strOr(v.account),
    from: asAddress(v.from),
    subject: strOr(v.subject),
    date: strOr(v.date),
    folder: strOr(v.folder, "INBOX") || "INBOX",
    unread: v.unread === true,
    category: strOrNull(v.category),
    gist: strOrNull(v.gist),
    can_unsubscribe: v.can_unsubscribe === true,
  };
}

export function asMailRule(v: unknown): MailRule | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.label)) return undefined;
  const match = isRec(v.match) ? v.match : {};
  return {
    id,
    match: { from: strOrNull(match.from), category: strOrNull(match.category), subject: strOrNull(match.subject), body: strOrNull(match.body) },
    except: (Array.isArray(v.except) ? v.except : [])
      .filter(isRec)
      .map((e) => ({ from: strOrNull(e.from), category: strOrNull(e.category), subject: strOrNull(e.subject) })),
    action: strOr(v.action),
    folder: strOrNull(v.folder),
    label: v.label,
    hits: Math.max(0, numOr(v.hits, 0)),
    last_hit_at: strOrNull(v.last_hit_at),
    created_at: strOr(v.created_at),
  };
}

export function asMailStatus(v: unknown): MailStatus {
  const rec = isRec(v) ? v : {};
  return {
    configured: rec.configured === true,
    accounts: list(rec.accounts, (a): MailAccountStatus | undefined => {
      if (!isRec(a) || !isStr(a.name)) return undefined;
      return {
        name: a.name,
        address: strOr(a.address),
        ok: a.ok !== false,
        error: strOr(a.error),
        last_sweep_at: strOrNull(a.last_sweep_at),
        inbox_count: Math.max(0, numOr(a.inbox_count, 0)),
      };
    }),
  };
}

export function asMailSummary(v: unknown): MailSummary {
  const rec = isRec(v) ? v : {};
  const unread: Record<string, number> = {};
  if (isRec(rec.unread)) for (const [k, n] of Object.entries(rec.unread)) if (isNum(n) && n > 0) unread[k] = n;
  return { unread, attention: list(rec.attention, asMessage) };
}

export function asMailBody(v: unknown): MailBody | undefined {
  if (!isRec(v) || !isStr(v.text)) return undefined;
  return { text: v.text, truncated: v.truncated === true };
}

export function asApproval(v: unknown): Approval | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.title) || !(APPROVAL_STATUSES as readonly string[]).includes(v.status as string)) return undefined;
  return {
    id,
    kind: strOr(v.kind),
    title: v.title,
    detail: strOr(v.detail),
    scope_label: strOrNull(v.scope_label),
    status: v.status as ApprovalStatus,
    result: strOrNull(v.result),
    decided_by: strOrNull(v.decided_by),
    note: strOrNull(v.note),
    created_at: strOr(v.created_at),
    expires_at: strOrNull(v.expires_at),
    decided_at: strOrNull(v.decided_at),
  };
}

// Only links to GitHub are ever shown for a change (its issue and PR live there).
export function githubUrl(v: unknown): string | null {
  return isStr(v) && v.startsWith("https://github.com/") ? v : null;
}

export function asChangeRequest(v: unknown): ChangeRequest | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.title) || !(CHANGE_STATUSES as readonly string[]).includes(v.status as string)) return undefined;
  return {
    id,
    title: v.title,
    what: strOr(v.what),
    why: strOr(v.why),
    kind: strOr(v.kind),
    revises: idOf(v.revises) ?? null,
    status: v.status as ChangeStatus,
    approval_id: idOf(v.approval_id) ?? null,
    feedback: strOrNull(v.feedback),
    stage: strOr(v.stage),
    issue_url: githubUrl(v.issue_url),
    pr_url: githubUrl(v.pr_url),
    result: strOrNull(v.result),
    attempts: Math.max(0, numOr(v.attempts, 0)),
    previous_result: strOrNull(v.previous_result),
    previous_pr_url: githubUrl(v.previous_pr_url),
    created_at: strOr(v.created_at),
    updated_at: strOr(v.updated_at),
    finished_at: strOrNull(v.finished_at),
  };
}

export function asApprovalRule(v: unknown): ApprovalRule | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined) return undefined;
  return { id, kind: strOr(v.kind), scope_label: strOr(v.scope_label), uses: Math.max(0, numOr(v.uses, 0)), created_at: strOr(v.created_at) };
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
export interface MailCardData {
  items: Message[];
  highlight: string[];
}
export interface MailMessageCardData {
  message: Message;
  excerpt: string;
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
  | (CardBase & { kind: "web_page"; data: WebPageCardData })
  | (CardBase & { kind: "mail"; data: MailCardData })
  | (CardBase & { kind: "mail_message"; data: MailMessageCardData })
  | (CardBase & { kind: "approval"; data: Approval });
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
    case "mail":
      return { ...base, kind: "mail", data: { items: list(d.items, asMessage), highlight: highlights(d.highlight) } };
    case "mail_message": {
      const message = asMessage(d.message);
      return message && { ...base, kind: "mail_message", data: { message, excerpt: strOr(d.excerpt) } };
    }
    case "approval": {
      const approval = asApproval(d);
      return approval && { ...base, kind: "approval", data: approval };
    }
    default:
      return undefined;
  }
}
