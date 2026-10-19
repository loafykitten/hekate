import { MAIL_CATEGORIES, type MailAddress, type Message } from "./model";
import { dayKey, dayKeyOfMs, daysBetween, formatDay, formatTime, resolveZone } from "./time";

// Labels and formatting for mail. The service sends the category as a key.

export const CATEGORY_LABEL: Record<string, string> = {
  needs_reply: "Needs reply",
  important: "Important",
  personal: "Personal",
  receipt: "Receipts",
  notification: "Notifications",
  newsletter: "Newsletters",
  promo: "Promos",
};

// Singular form, for the label on a single message.
const CATEGORY_ONE: Record<string, string> = {
  receipt: "Receipt",
  notification: "Notification",
  newsletter: "Newsletter",
  promo: "Promo",
};

export function categoryLabel(category: string | null): string {
  if (!category) return "";
  return CATEGORY_ONE[category] ?? CATEGORY_LABEL[category] ?? category;
}

// The categories in the order the filter chips show them.
export const CATEGORY_ORDER: readonly string[] = MAIL_CATEGORIES;

// Names the sender the way a mail app does: the display name, else the address.
export function senderName(from: MailAddress): string {
  return from.name || from.address || "Unknown sender";
}

// "Today" mail shows its time; recent days name themselves; older mail shows the date.
export function mailWhen(iso: string, nowMs: number): string {
  if (!iso || Number.isNaN(Date.parse(iso))) return "";
  const today = dayKeyOfMs(nowMs);
  const day = dayKey(iso);
  const diff = daysBetween(day, today);
  if (diff === 0) return formatTime(iso);
  if (diff === 1) return "Yesterday";
  if (day.slice(0, 4) === today.slice(0, 4)) return formatDay(day).replace(/^\w+,\s*/, "");
  const { tz, locale } = resolveZone();
  return new Intl.DateTimeFormat(locale, { month: "short", day: "numeric", year: "numeric", timeZone: tz }).format(new Date(iso));
}

// "Wed, Oct 21, 3:00 PM" in full for the message view, with the year when it isn't this one.
export function mailWhenLong(iso: string, nowMs: number): string {
  if (!iso || Number.isNaN(Date.parse(iso))) return "";
  const day = dayKey(iso);
  const sameYear = day.slice(0, 4) === dayKeyOfMs(nowMs).slice(0, 4);
  const { tz, locale } = resolveZone();
  const date = new Intl.DateTimeFormat(locale, { weekday: "short", month: "short", day: "numeric", ...(sameYear ? {} : { year: "numeric" }), timeZone: tz }).format(new Date(iso));
  return `${date}, ${formatTime(iso)}`;
}

// Account names are only worth showing when there is more than one.
export function accountsOf(messages: readonly Message[]): Set<string> {
  return new Set(messages.map((m) => m.account).filter(Boolean));
}

// The unread count that goes on the "All" chip: every category's unread mail, classified or not.
export function totalUnread(unread: Record<string, number>): number {
  return Object.values(unread).reduce((sum, n) => sum + n, 0);
}

// The unread chips to show: known categories in order, then any new ones the service sends.
export function filterCategories(unread: Record<string, number>): string[] {
  const extra = Object.keys(unread).filter((k) => !CATEGORY_ORDER.includes(k));
  return [...CATEGORY_ORDER, ...extra];
}
