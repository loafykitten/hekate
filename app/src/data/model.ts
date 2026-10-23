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

// The journal: her diary entry for a day, and what that night's reflection changed.
export interface DiaryEntry {
  day: string; // YYYY-MM-DD, local
  text: string;
  created_at: string;
  updated_at: string;
}
export const REFLECTION_ACTIONS = ["save", "update", "retire"] as const;
export type ReflectionAction = (typeof REFLECTION_ACTIONS)[number];
export interface ReflectionChange {
  id: string;
  action: ReflectionAction;
  memory_id: string;
  before: Memory | null; // null for save
  after: Memory | null; // null for retire
  reason: string;
  undone_at: string | null;
}
export interface Reflection {
  id: string;
  summary: string;
  created_at: string;
  changes: ReflectionChange[];
}
export interface JournalDay {
  day: string;
  diary: DiaryEntry | null;
  reflections: Reflection[];
}

export function asReflectionChange(v: unknown): ReflectionChange | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !(REFLECTION_ACTIONS as readonly string[]).includes(v.action as string)) return undefined;
  return {
    id,
    action: v.action as ReflectionAction,
    memory_id: idOf(v.memory_id) ?? "",
    before: asMemory(v.before) ?? null,
    after: asMemory(v.after) ?? null,
    reason: strOr(v.reason),
    undone_at: strOrNull(v.undone_at),
  };
}

function asDiaryEntry(v: unknown): DiaryEntry | null {
  if (!isRec(v) || !isStr(v.day) || !isStr(v.text)) return null;
  return { day: v.day, text: v.text, created_at: strOr(v.created_at), updated_at: strOr(v.updated_at) };
}

export function asJournalDay(v: unknown): JournalDay | undefined {
  if (!isRec(v) || !isStr(v.day)) return undefined;
  return {
    day: v.day,
    diary: asDiaryEntry(v.diary),
    reflections: list(v.reflections, (r): Reflection | undefined => {
      const id = isRec(r) ? idOf(r.id) : undefined;
      if (!isRec(r) || id === undefined) return undefined;
      return { id, summary: strOr(r.summary), created_at: strOr(r.created_at), changes: list(r.changes, asReflectionChange) };
    }),
  };
}

// The owner's choices for her routines (GET /api/routines).
export interface RoutineSettings {
  heartbeat: boolean; // nudges
  waking_start: string; // HH:MM
  waking_end: string;
  checkin: boolean; // the evening check-in
  checkin_at: string; // HH:MM
}

export function asRoutineSettings(v: unknown): RoutineSettings | undefined {
  if (!isRec(v) || typeof v.heartbeat !== "boolean" || !isStr(v.waking_start) || !isStr(v.waking_end)) return undefined;
  if (typeof v.checkin !== "boolean" || !isStr(v.checkin_at)) return undefined;
  return {
    heartbeat: v.heartbeat,
    waking_start: v.waking_start,
    waking_end: v.waking_end,
    checkin: v.checkin,
    checkin_at: v.checkin_at,
  };
}

export const IDEA_STATUSES = ["open", "done", "dismissed"] as const;
export type IdeaStatus = (typeof IDEA_STATUSES)[number];
// An improvement idea Renamon noted about herself.
export interface Idea {
  id: string;
  text: string;
  kind: string; // tool | fix | improvement | other; kept as sent
  status: IdeaStatus;
  created_at: string;
  updated_at: string;
}

export function asIdea(v: unknown): Idea | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.text) || !(IDEA_STATUSES as readonly string[]).includes(v.status as string)) return undefined;
  return { id, text: v.text, kind: strOr(v.kind), status: v.status as IdeaStatus, created_at: strOr(v.created_at), updated_at: strOr(v.updated_at) };
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

// --- money ---------------------------------------------------------------------------------

export type MoneyItemStatus = "ok" | "login_required" | "error";
export interface MoneyItem {
  id: string;
  institution: string;
  status: MoneyItemStatus;
  error: string;
  last_sync_at: string | null;
  accounts: number;
  // How much transaction history has arrived: Plaid gathers it over the first minutes.
  history: MoneyHistory;
}
export type MoneyHistory = "pending" | "partial" | "complete";
export interface MoneyStatus {
  configured: boolean;
  items: MoneyItem[];
}
export interface MoneyAccount {
  id: string;
  item_id: string;
  institution: string;
  name: string;
  mask: string;
  type: string;
  subtype: string;
  balance: { current: number | null; available: number | null; limit: number | null };
  currency: string;
  updated_at: string;
}
// amount: money in (+) or out (-). name and merchant are the bank's text.
export interface MoneyTransaction {
  id: string;
  account_id: string;
  account: string;
  date: string;
  name: string;
  merchant: string | null;
  amount: number;
  currency: string;
  pending: boolean;
  category: string | null;
  category_label: string | null;
  detail: string | null;
}
export interface SpendingCategory {
  category: string;
  label: string;
  spent: number;
  count: number;
  previous: number;
}
export interface Spending {
  month: string; // YYYY-MM
  currency: string;
  spent: number;
  received: number;
  categories: SpendingCategory[];
  previous_spent: number;
  transfers: number;
}

const MONEY_ITEM_STATUSES: readonly string[] = ["ok", "login_required", "error"];
export function asMoneyItem(v: unknown): MoneyItem | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined) return undefined;
  return {
    id,
    institution: strOr(v.institution),
    // A status this build doesn't know is shown as a problem rather than as fine.
    status: MONEY_ITEM_STATUSES.includes(v.status as string) ? (v.status as MoneyItemStatus) : "error",
    error: strOr(v.error),
    last_sync_at: strOrNull(v.last_sync_at),
    accounts: Math.max(0, numOr(v.accounts, 0)),
    // Missing (an older service) reads as complete; anything unknown as still coming.
    history: v.history === undefined || v.history === "complete" ? "complete" : v.history === "partial" ? "partial" : "pending",
  };
}

export function asMoneyStatus(v: unknown): MoneyStatus {
  const rec = isRec(v) ? v : {};
  return { configured: rec.configured === true, items: list(rec.items, asMoneyItem) };
}

export function asMoneyAccount(v: unknown): MoneyAccount | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.name)) return undefined;
  const b = isRec(v.balance) ? v.balance : {};
  return {
    id,
    item_id: idOf(v.item_id) ?? "",
    institution: strOr(v.institution),
    name: v.name,
    mask: strOr(v.mask),
    type: strOr(v.type),
    subtype: strOr(v.subtype),
    balance: { current: numOrNull(b.current), available: numOrNull(b.available), limit: numOrNull(b.limit) },
    currency: strOr(v.currency, "USD") || "USD",
    updated_at: strOr(v.updated_at),
  };
}

export function asMoneyTransaction(v: unknown): MoneyTransaction | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isNum(v.amount) || !isStr(v.date)) return undefined;
  return {
    id,
    account_id: idOf(v.account_id) ?? "",
    account: strOr(v.account),
    date: v.date,
    name: strOr(v.name),
    merchant: strOrNull(v.merchant),
    amount: v.amount,
    currency: strOr(v.currency, "USD") || "USD",
    pending: v.pending === true,
    category: strOrNull(v.category),
    category_label: strOrNull(v.category_label),
    detail: strOrNull(v.detail),
  };
}

function asSpendingCategory(v: unknown): SpendingCategory | undefined {
  if (!isRec(v) || !isStr(v.category)) return undefined;
  return {
    category: v.category,
    label: strOr(v.label) || v.category,
    spent: numOr(v.spent, 0),
    count: Math.max(0, numOr(v.count, 0)),
    previous: numOr(v.previous, 0),
  };
}

export function asSpending(v: unknown): Spending | undefined {
  if (!isRec(v) || !isStr(v.month) || !/^\d{4}-\d{2}$/.test(v.month)) return undefined;
  return {
    month: v.month,
    currency: strOr(v.currency, "USD") || "USD",
    spent: numOr(v.spent, 0),
    received: numOr(v.received, 0),
    categories: list(v.categories, asSpendingCategory),
    previous_spent: numOr(v.previous_spent, 0),
    transfers: numOr(v.transfers, 0),
  };
}

// --- cards ---------------------------------------------------------------------------------

export interface TasksCardData {
  items: Task[];
  highlight: string[];
  // What was done (an item may be one completion of a repeating task): shown without controls.
  record?: boolean;
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
export interface AccountsCardData {
  items: MoneyAccount[];
}
export interface TransactionsCardData {
  items: MoneyTransaction[];
  highlight: string[];
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
  | (CardBase & { kind: "approval"; data: Approval })
  | (CardBase & { kind: "accounts"; data: AccountsCardData })
  | (CardBase & { kind: "transactions"; data: TransactionsCardData })
  | (CardBase & { kind: "spending"; data: Spending });
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
      return { ...base, kind: "tasks", data: { items: list(d.items, asTask), highlight: highlights(d.highlight), record: d.record === true } };
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
    case "accounts":
      return { ...base, kind: "accounts", data: { items: list(d.items, asMoneyAccount) } };
    case "transactions":
      return { ...base, kind: "transactions", data: { items: list(d.items, asMoneyTransaction), highlight: highlights(d.highlight) } };
    case "spending": {
      const spending = asSpending(d);
      return spending && { ...base, kind: "spending", data: spending };
    }
    default:
      return undefined;
  }
}

// --- threads -------------------------------------------------------------------------------

export type ThreadModel = "deep" | "chat";

export interface Thread {
  id: string;
  title: string;
  model: ThreadModel;
  origin: "owner" | "renamon";
  running: boolean;
  // A second model is reviewing her lab work (running is true too).
  reviewing: boolean;
  archived: boolean;
  preview: string;
  created_at: string;
  updated_at: string;
}

export interface ThreadMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
  turn_id: string;
  created_at: string;
}

// One tool call of a turn, as saved.
export interface ThreadStep {
  turn_id: string;
  tool: string;
  label: string;
  status: "ok" | "error";
  summary: string;
  created_at: string;
}

export function asThread(v: unknown): Thread | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.title)) return undefined;
  return {
    id,
    title: v.title,
    // A model this app doesn't know is shown as the default.
    model: v.model === "chat" ? "chat" : "deep",
    origin: v.origin === "renamon" ? "renamon" : "owner",
    running: v.running === true,
    reviewing: v.reviewing === true,
    archived: v.archived === true,
    preview: strOr(v.preview),
    created_at: strOr(v.created_at),
    updated_at: strOr(v.updated_at),
  };
}

export function asThreadMessage(v: unknown): ThreadMessage | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined || !isStr(v.text) || (v.role !== "user" && v.role !== "assistant")) return undefined;
  return { id, role: v.role, text: v.text, turn_id: strOr(v.turn_id), created_at: strOr(v.created_at) };
}

export function asThreadStep(v: unknown): ThreadStep | undefined {
  if (!isRec(v) || !isStr(v.turn_id)) return undefined;
  return {
    turn_id: v.turn_id,
    tool: strOr(v.tool),
    label: strOr(v.label) || strOr(v.tool),
    status: v.status === "error" ? "error" : "ok",
    summary: strOr(v.summary),
    created_at: strOr(v.created_at),
  };
}

// A second model's review of her lab work (PROTOCOL: "Reviews of her lab work").
export type ReviewStatus = "running" | "done" | "failed" | "stopped";
export type ReviewVerdict = "looks_good" | "needs_changes" | "blocked";
export type Severity = "high" | "medium" | "low";

export interface ReviewFinding {
  severity: Severity;
  title: string;
  claim: string;
  evidence: string;
  fix: string;
  // Rests on what the reviewer knows, not on a command it ran.
  unverified: boolean;
  commands: string[];
}

export interface Review {
  id: string;
  thread_id: string;
  turn_id: string;
  round: number;
  status: ReviewStatus;
  model: string;
  verdict: ReviewVerdict | "";
  summary: string;
  findings: ReviewFinding[];
  verified: string[];
  commands: number;
  error: string;
  // The turn that handed her the findings; "" if none (yet).
  followup_turn_id: string;
  started_at: string;
  finished_at: string;
}

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter(isStr) : []);

function asFinding(v: unknown): ReviewFinding | undefined {
  if (!isRec(v) || !isStr(v.title)) return undefined;
  return {
    // A severity this app doesn't know sorts and shows as the middle one.
    severity: v.severity === "high" || v.severity === "low" ? v.severity : "medium",
    title: v.title,
    claim: strOr(v.claim),
    evidence: strOr(v.evidence),
    fix: strOr(v.fix),
    unverified: v.unverified === true,
    commands: strings(v.commands),
  };
}

export function asReview(v: unknown): Review | undefined {
  if (!isRec(v)) return undefined;
  const id = idOf(v.id);
  if (id === undefined) return undefined;
  const verdict = v.verdict === "looks_good" || v.verdict === "needs_changes" || v.verdict === "blocked" ? v.verdict : "";
  return {
    id,
    thread_id: strOr(v.thread_id),
    turn_id: strOr(v.turn_id),
    round: numOr(v.round, 1),
    status: v.status === "done" || v.status === "failed" || v.status === "stopped" ? v.status : "running",
    model: strOr(v.model),
    verdict,
    summary: strOr(v.summary),
    findings: (Array.isArray(v.findings) ? v.findings : []).map(asFinding).filter((f): f is ReviewFinding => f !== undefined),
    verified: strings(v.verified),
    commands: numOr(v.commands, 0),
    error: strOr(v.error),
    followup_turn_id: strOr(v.followup_turn_id),
    started_at: strOr(v.started_at),
    finished_at: strOr(v.finished_at),
  };
}
