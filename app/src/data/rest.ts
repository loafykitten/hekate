import type { LinkedAccount } from "./plaid";
import {
  asApproval,
  asApprovalRule,
  asChangeRequest,
  asIdea,
  asJournalDay,
  asReflectionChange,
  asRoutineSettings,
  asCoreEntry,
  asEvent,
  asMailBody,
  asMoneyAccount,
  asMoneyItem,
  asMoneyStatus,
  asMoneyTransaction,
  asSpending,
  asMailRule,
  asMailStatus,
  asMailSummary,
  asMemory,
  asMessage,
  asReminder,
  asTask,
  asReview,
  asThread,
  asThreadMessage,
  asThreadStep,
  asTaskList,
  asWeather,
  isRec,
  list,
  type Approval,
  type ApprovalRule,
  type ChangeRequest,
  type Idea,
  type IdeaStatus,
  type JournalDay,
  type RoutineSettings,
  type CalendarEvent,
  type CoreEntry,
  type MailBody,
  type MailRule,
  type MailStatus,
  type MailSummary,
  type MoneyAccount,
  type MoneyItem,
  type MoneyStatus,
  type MoneyTransaction,
  type Spending,
  type Memory,
  type Message,
  type Task,
  type Review,
  type Thread,
  type ThreadMessage,
  type ThreadModel,
  type ThreadStep,
  type TaskList,
  type Weather,
} from "./model";

// The REST half of docs/PROTOCOL.md: a thin typed client. Errors carry the service's
// `{ "error": "message" }` text so the UI can show it as is.

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

// The proxy answers these when the service itself is down or restarting. The service uses them
// too, for an integration that failed (calendar login refused, mail not set up), but always with
// its own JSON error, so only a gateway status without one means Hekate is out of reach.
const GATEWAY = [502, 503, 504];

// The service couldn't be reached at all (no network, or the proxy found nothing behind it).
export function unreachable(e: unknown): boolean {
  return e instanceof ApiError && e.status === 0;
}

type Query = Record<string, string | number | undefined>;

export interface RequestOptions {
  body?: unknown;
  query?: Query;
  signal?: AbortSignal;
}

function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== "") params.set(k, String(v));
  const s = params.toString();
  return s ? `${path}?${s}` : path;
}

// Returns the parsed JSON body, or undefined for 204 and 202 (accepted: the work runs later, and
// there's nothing to report yet).
export async function request(method: string, path: string, { body, query, signal }: RequestOptions = {}): Promise<unknown> {
  let res: Response;
  try {
    res = await fetch(withQuery(`/api${path}`, query), {
      method,
      signal,
      headers: { Accept: "application/json", ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError("Hekate isn't reachable.", 0);
  }
  if (res.status === 204 || res.status === 202) return undefined;
  let json: unknown;
  try {
    json = await res.json();
  } catch {
    // An SPA fallback page or a proxy error is not the service's answer.
    if (GATEWAY.includes(res.status)) throw new ApiError("Hekate isn't reachable.", 0);
    throw new ApiError(res.ok ? "Hekate sent something unexpected." : `Hekate answered ${res.status}.`, res.status);
  }
  if (!res.ok) {
    const own = isRec(json) && typeof json.error === "string" && json.error ? json.error : undefined;
    if (!own && GATEWAY.includes(res.status)) throw new ApiError("Hekate isn't reachable.", 0);
    throw new ApiError(own ?? `Hekate answered ${res.status}.`, res.status);
  }
  return json;
}

function field(body: unknown, name: string): unknown {
  return isRec(body) ? body[name] : undefined;
}

function one<T>(body: unknown, parse: (v: unknown) => T | undefined): T {
  const value = parse(body);
  if (value === undefined) throw new ApiError("Hekate sent something unexpected.", 200);
  return value;
}

function many<T>(body: unknown, name: string, parse: (v: unknown) => T | undefined): T[] {
  // A missing or null array is an empty list (a Go nil slice encodes as null).
  if (!isRec(body)) throw new ApiError("Hekate sent something unexpected.", 200);
  return list(body[name], parse);
}

// --- tasks ---------------------------------------------------------------------------------

export type TaskFields = Partial<Pick<Task, "title" | "notes" | "list" | "due" | "rrule" | "priority">>;

export const tasksApi = {
  list: async (params: { list?: string; status?: "open" | "done" | "all" }, signal?: AbortSignal) =>
    many(await request("GET", "/tasks", { query: { list: params.list, status: params.status }, signal }), "tasks", asTask),
  lists: async (signal?: AbortSignal): Promise<TaskList[]> => many(await request("GET", "/lists", { signal }), "lists", asTaskList),
  create: async (fields: TaskFields & { title: string }) => one(await request("POST", "/tasks", { body: fields }), asTask),
  patch: async (id: string, fields: TaskFields) => one(await request("PATCH", `/tasks/${encodeURIComponent(id)}`, { body: fields }), asTask),
  // `due` is the occurrence being completed, so a repeated request can't complete the next one too.
  complete: async (id: string, due?: string | null) =>
    one(await request("POST", `/tasks/${encodeURIComponent(id)}/complete`, due ? { body: { due } } : undefined), asTask),
  reopen: async (id: string) => one(await request("POST", `/tasks/${encodeURIComponent(id)}/reopen`), asTask),
  remove: async (id: string) => void (await request("DELETE", `/tasks/${encodeURIComponent(id)}`)),
};

// --- reminders -----------------------------------------------------------------------------

export type ReminderFields = { text?: string; at?: string; rrule?: string | null };

export const remindersApi = {
  list: async (status: "upcoming" | "past" | "all", signal?: AbortSignal) =>
    many(await request("GET", "/reminders", { query: { status }, signal }), "reminders", asReminder),
  create: async (fields: ReminderFields & { text: string; at: string }) =>
    one(await request("POST", "/reminders", { body: fields }), asReminder),
  patch: async (id: string, fields: ReminderFields) =>
    one(await request("PATCH", `/reminders/${encodeURIComponent(id)}`, { body: fields }), asReminder),
  snooze: async (id: string, minutes: number) =>
    one(await request("POST", `/reminders/${encodeURIComponent(id)}/snooze`, { body: { minutes } }), asReminder),
  done: async (id: string) => one(await request("POST", `/reminders/${encodeURIComponent(id)}/done`), asReminder),
  remove: async (id: string) => void (await request("DELETE", `/reminders/${encodeURIComponent(id)}`)),
};

// --- push ----------------------------------------------------------------------------------

export const pushApi = {
  key: async (): Promise<string> => {
    const key = field(await request("GET", "/push/key"), "public_key");
    if (typeof key !== "string" || !key) throw new ApiError("Hekate has no push key.", 200);
    return key;
  },
  subscribe: async (subscription: unknown) => void (await request("POST", "/push/subscriptions", { body: subscription })),
  unsubscribe: async (endpoint: string) => void (await request("DELETE", "/push/subscriptions", { body: { endpoint } })),
  test: async () => void (await request("POST", "/push/test")),
};

// --- calendar ------------------------------------------------------------------------------

export interface CalendarStatus {
  configured: boolean;
  calendar_name?: string;
}

// `instance` names the occurrence an edit was made from, so the service applies a moved start or
// a new end to a recurring series relative to that occurrence.
export type EventFields = Partial<Pick<CalendarEvent, "title" | "start" | "end" | "all_day" | "location" | "notes" | "rrule" | "instance" | "version">>;

export const calendarApi = {
  status: async (signal?: AbortSignal): Promise<CalendarStatus> => {
    const body = await request("GET", "/calendar/status", { signal });
    return {
      configured: field(body, "configured") === true,
      calendar_name: typeof field(body, "calendar_name") === "string" ? (field(body, "calendar_name") as string) : undefined,
    };
  },
  events: async (from: string, to: string, signal?: AbortSignal) =>
    many(await request("GET", "/calendar/events", { query: { from, to }, signal }), "events", asEvent),
  create: async (fields: EventFields & { title: string; start: string }) =>
    one(await request("POST", "/calendar/events", { body: fields }), asEvent),
  patch: async (uid: string, fields: EventFields) =>
    one(await request("PATCH", `/calendar/events/${encodeURIComponent(uid)}`, { body: fields }), asEvent),
  remove: async (uid: string) => void (await request("DELETE", `/calendar/events/${encodeURIComponent(uid)}`)),
};

// --- memory --------------------------------------------------------------------------------

export type MemoryFields = Partial<Pick<Memory, "content" | "kind" | "importance" | "follow_up_at">>;

export const memoryApi = {
  list: async (params: { q?: string; kind?: string; limit?: number }, signal?: AbortSignal) =>
    many(await request("GET", "/memory", { query: params, signal }), "memories", asMemory),
  patch: async (id: string, fields: MemoryFields) =>
    one(await request("PATCH", `/memory/${encodeURIComponent(id)}`, { body: fields }), asMemory),
  remove: async (id: string) => void (await request("DELETE", `/memory/${encodeURIComponent(id)}`)),
  core: async (signal?: AbortSignal): Promise<CoreEntry[]> => many(await request("GET", "/memory/core", { signal }), "entries", asCoreEntry),
  putCore: async (key: string, value: string) =>
    one(await request("PUT", `/memory/core/${encodeURIComponent(key)}`, { body: { value } }), asCoreEntry),
  removeCore: async (key: string) => void (await request("DELETE", `/memory/core/${encodeURIComponent(key)}`)),
};

// --- journal -------------------------------------------------------------------------------

export const journalApi = {
  list: async (signal?: AbortSignal): Promise<JournalDay[]> => many(await request("GET", "/journal", { signal }), "days", asJournalDay),
  undo: async (changeId: string) => one(await request("POST", `/reflections/changes/${encodeURIComponent(changeId)}/undo`), asReflectionChange),
};

// --- routines ------------------------------------------------------------------------------

export const routinesApi = {
  get: async (signal?: AbortSignal): Promise<RoutineSettings> => one(await request("GET", "/routines", { signal }), asRoutineSettings),
  patch: async (fields: Partial<RoutineSettings>) => one(await request("PATCH", "/routines", { body: fields }), asRoutineSettings),
};

// --- ideas ---------------------------------------------------------------------------------

export const ideasApi = {
  list: async (signal?: AbortSignal): Promise<Idea[]> => many(await request("GET", "/ideas", { signal }), "ideas", asIdea),
  setStatus: async (id: string, status: IdeaStatus) => one(await request("PATCH", `/ideas/${encodeURIComponent(id)}`, { body: { status } }), asIdea),
};

// --- changes -------------------------------------------------------------------------------

export const changesApi = {
  list: async (signal?: AbortSignal): Promise<ChangeRequest[]> => many(await request("GET", "/changes", { signal }), "changes", asChangeRequest),
  retry: async (id: string) => one(await request("POST", `/changes/${encodeURIComponent(id)}/retry`), asChangeRequest),
};

// --- weather -------------------------------------------------------------------------------

// Null when the service doesn't know where she is (404): not an error, just nothing to show.
export async function fetchWeather(place?: string, signal?: AbortSignal): Promise<Weather | null> {
  try {
    return one(await request("GET", "/weather", { query: { place }, signal }), asWeather);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

// --- mail ----------------------------------------------------------------------------------

export interface MailQuery {
  q?: string;
  category?: string;
  account?: string;
  folder?: "inbox" | "all";
  unread?: boolean;
  limit?: number;
}

export interface MailMessageDetail {
  message: Message;
  body: MailBody;
}

export const mailApi = {
  status: async (signal?: AbortSignal): Promise<MailStatus> => asMailStatus(await request("GET", "/mail/status", { signal })),
  summary: async (signal?: AbortSignal): Promise<MailSummary> => asMailSummary(await request("GET", "/mail/summary", { signal })),
  messages: async (params: MailQuery, signal?: AbortSignal) =>
    many(
      await request("GET", "/mail/messages", {
        query: { ...params, unread: params.unread === undefined ? undefined : String(params.unread) },
        signal,
      }),
      "messages",
      asMessage,
    ),
  // Fetched live from the mail server; 404 when the message is gone from it.
  message: async (id: string, signal?: AbortSignal): Promise<MailMessageDetail> => {
    const body = await request("GET", `/mail/messages/${encodeURIComponent(id)}`, { signal });
    const message = asMessage(field(body, "message"));
    if (!message) throw new ApiError("Hekate sent something unexpected.", 200);
    return { message, body: asMailBody(field(body, "body")) ?? { text: "", truncated: false } };
  },
  archive: async (id: string) => one(await request("POST", `/mail/messages/${encodeURIComponent(id)}/archive`), asMessage),
  trash: async (id: string) => one(await request("POST", `/mail/messages/${encodeURIComponent(id)}/trash`), asMessage),
  markRead: async (id: string, unread: boolean) =>
    one(await request("POST", `/mail/messages/${encodeURIComponent(id)}/read`, { body: { unread } }), asMessage),
  rules: async (signal?: AbortSignal): Promise<MailRule[]> => many(await request("GET", "/mail/rules", { signal }), "rules", asMailRule),
  removeRule: async (id: string) => void (await request("DELETE", `/mail/rules/${encodeURIComponent(id)}`)),
};

// --- approvals -----------------------------------------------------------------------------

export type ApprovalDecision = "once" | "always" | "deny";

export const MAX_DENY_NOTE = 500;

export function decideBody(decision: ApprovalDecision, note?: string): { decision: ApprovalDecision; note?: string } {
  const text = decision === "deny" ? note?.trim().slice(0, MAX_DENY_NOTE) : undefined;
  return text ? { decision, note: text } : { decision };
}

export const approvalsApi = {
  list: async (status: "pending" | "recent", signal?: AbortSignal): Promise<Approval[]> =>
    many(await request("GET", "/approvals", { query: { status }, signal }), "approvals", asApproval),
  // There is no endpoint for one approval; it is in the pending list, or among the recent ones.
  find: async (id: string, signal?: AbortSignal): Promise<Approval | null> => {
    for (const status of ["pending", "recent"] as const) {
      const found = (await approvalsApi.list(status, signal)).find((a) => a.id === id);
      if (found) return found;
    }
    return null;
  },
  // `note` is the owner's reason for a denial; it is only sent with "deny", and only when there is one.
  decide: async (id: string, decision: ApprovalDecision, note?: string) =>
    one(await request("POST", `/approvals/${encodeURIComponent(id)}/decide`, { body: decideBody(decision, note) }), asApproval),
  rules: async (signal?: AbortSignal): Promise<ApprovalRule[]> =>
    many(await request("GET", "/approvals/rules", { signal }), "rules", asApprovalRule),
  removeRule: async (id: string) => void (await request("DELETE", `/approvals/rules/${encodeURIComponent(id)}`)),
};

// --- money ---------------------------------------------------------------------------------

export interface TransactionQuery {
  q?: string;
  category?: string;
  account?: string;
  from?: string;
  to?: string;
  limit?: number;
}

export const moneyApi = {
  status: async (signal?: AbortSignal): Promise<MoneyStatus> => asMoneyStatus(await request("GET", "/money/status", { signal })),
  // A link token for Plaid Link: a new bank, or with itemId to reconnect that one.
  linkToken: async (itemId?: string): Promise<string> => {
    const token = field(await request("POST", "/money/link-token", { body: itemId ? { item_id: itemId } : {} }), "link_token");
    if (typeof token !== "string" || token === "") throw new ApiError("Hekate sent something unexpected.", 200);
    return token;
  },
  addItem: async (publicToken: string, institution: { id: string; name: string }, accounts: LinkedAccount[]): Promise<MoneyItem> =>
    one(await request("POST", "/money/items", { body: { public_token: publicToken, institution, accounts } }), asMoneyItem),
  removeItem: async (id: string) => void (await request("DELETE", `/money/items/${encodeURIComponent(id)}`)),
  sync: async () => void (await request("POST", "/money/sync")),
  accounts: async (signal?: AbortSignal): Promise<MoneyAccount[]> => many(await request("GET", "/money/accounts", { signal }), "accounts", asMoneyAccount),
  transactions: async (params: TransactionQuery, signal?: AbortSignal): Promise<MoneyTransaction[]> =>
    many(await request("GET", "/money/transactions", { query: { ...params }, signal }), "transactions", asMoneyTransaction),
  spending: async (month: string, signal?: AbortSignal): Promise<Spending> => one(await request("GET", "/money/spending", { query: { month }, signal }), asSpending),
};

// --- threads -------------------------------------------------------------------------------

export const THREAD_PAGE = 50;
export type ThreadFields = { title?: string; model?: ThreadModel; archived?: boolean };

export interface ThreadPage {
  messages: ThreadMessage[]; // oldest first
  steps: ThreadStep[];
  // Of the turns on this page, or that started them; one may show on two pages.
  reviews: Review[];
}

export const threadsApi = {
  list: async (archived: boolean, signal?: AbortSignal): Promise<Thread[]> =>
    many(await request("GET", "/threads", { query: { archived: archived ? "true" : undefined }, signal }), "threads", asThread),
  create: async (fields: { title?: string; model?: ThreadModel } = {}) => one(await request("POST", "/threads", { body: fields }), asThread),
  get: async (id: string, signal?: AbortSignal) => one(await request("GET", `/threads/${encodeURIComponent(id)}`, { signal }), asThread),
  patch: async (id: string, fields: ThreadFields) => one(await request("PATCH", `/threads/${encodeURIComponent(id)}`, { body: fields }), asThread),
  // `before` is the id of the oldest message already shown.
  messages: async (id: string, before?: string, signal?: AbortSignal): Promise<ThreadPage> => {
    const body = await request("GET", `/threads/${encodeURIComponent(id)}/messages`, { query: { before, limit: THREAD_PAGE }, signal });
    return { messages: many(body, "messages", asThreadMessage), steps: many(body, "steps", asThreadStep), reviews: many(body, "reviews", asReview) };
  },
};
