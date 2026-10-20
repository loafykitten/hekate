import type { QueryKey } from "@tanstack/react-query";

// Query keys for the menu data. Every key starts with its domain, so invalidating a domain
// refreshes everything under it (lists, searches, ranges).
export const keys = {
  tasks: (list: string | undefined, status: "open" | "done" | "all") => ["tasks", "list", list ?? "", status] as const,
  lists: ["tasks", "lists"] as const,
  reminders: (status: "upcoming" | "past" | "all") => ["reminders", status] as const,
  calendarStatus: ["calendar", "status"] as const,
  events: (from: string, to: string) => ["calendar", "events", from, to] as const,
  memories: (q: string, kind: string) => ["memory", "list", q, kind] as const,
  core: ["memory", "core"] as const,
  weather: (place: string | undefined) => ["weather", place ?? "home"] as const,
  mailStatus: ["mail", "status"] as const,
  mailSummary: ["mail", "summary"] as const,
  mailMessages: (q: string, category: string, account: string) => ["mail", "list", q, category, account] as const,
  mailMessage: (id: string) => ["mail", "message", id] as const,
  mailRules: ["mail", "rules"] as const,
  approvals: (status: "pending" | "recent") => ["approvals", "list", status] as const,
  approval: (id: string) => ["approvals", "item", id] as const,
  approvalRules: ["approvals", "rules"] as const,
  changes: ["changes", "list"] as const,
};

export const DOMAINS = ["tasks", "reminders", "calendar", "memory", "weather", "mail", "approvals", "changes"] as const;
export type Domain = (typeof DOMAINS)[number];

// The query keys an `invalidate` event refreshes. Unknown domains are ignored (a newer service
// may add some), and an empty or malformed list refreshes nothing.
export function invalidationKeys(domains: unknown): QueryKey[] {
  if (!Array.isArray(domains)) return [];
  const seen = new Set<string>();
  for (const d of domains) if (typeof d === "string" && (DOMAINS as readonly string[]).includes(d)) seen.add(d);
  return [...seen].map((d) => [d]);
}
