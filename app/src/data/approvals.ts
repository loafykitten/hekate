import type { QueryClient } from "@tanstack/react-query";
import type { Approval } from "./model";
import { keys } from "./queryKeys";

// Keeping the cached approvals in step with the service. An `approval` event, a decision made
// here and a refetch all end up as the same approval; whichever list or card shows it updates.

const byDecidedDesc = (a: Approval, b: Approval) => (b.decided_at ?? b.created_at).localeCompare(a.decided_at ?? a.created_at);

// The pending list after `a` changed: it joins while pending and leaves once decided.
export function patchPending(current: Approval[], a: Approval): Approval[] {
  const rest = current.filter((x) => x.id !== a.id);
  if (a.status !== "pending") return rest;
  return current.some((x) => x.id === a.id) ? current.map((x) => (x.id === a.id ? a : x)) : [a, ...rest];
}

// The recent (decided) list after `a` changed: decided approvals join, newest decision first.
export function patchRecent(current: Approval[], a: Approval): Approval[] {
  const rest = current.filter((x) => x.id !== a.id);
  if (a.status === "pending") return rest;
  return [a, ...rest].sort(byDecidedDesc);
}

// pending -> running -> done | failed; a later state beats an earlier one (a card's snapshot can
// be older than the cache, or newer).
const rank = (a: Approval) => (a.status === "pending" ? 0 : a.status === "running" ? 1 : 2);
export function freshest(a: Approval, b: Approval): Approval {
  if (rank(a) !== rank(b)) return rank(a) > rank(b) ? a : b;
  return (b.decided_at ?? "") > (a.decided_at ?? "") ? b : a;
}

export function applyApproval(qc: QueryClient, a: Approval) {
  qc.setQueryData<Approval | null>(keys.approval(a.id), (cur) => (cur ? freshest(cur, a) : a));
  qc.setQueryData<Approval[]>(keys.approvals("pending"), (cur) => cur && patchPending(cur, a));
  qc.setQueryData<Approval[]>(keys.approvals("recent"), (cur) => cur && patchRecent(cur, a));
  // A change request reads its status and the owner's reason from its approval.
  if (a.kind === "change_request") void qc.invalidateQueries({ queryKey: keys.changes });
}

// "mail_trash" -> "Trash mail"; unknown kinds read as plain words.
const KIND_LABEL: Record<string, string> = {
  mail_trash: "Move mail to Trash",
  mail_unsubscribe: "Unsubscribe from mail",
  mail_rule: "Add a mail rule",
  mail_archive: "Archive mail",
  mail_move: "Move mail",
  mail_mark: "Mark mail",
};

export function approvalKindLabel(kind: string): string {
  const known = KIND_LABEL[kind];
  if (known) return known;
  const spaced = kind.replace(/[_-]+/g, " ").trim();
  return spaced ? spaced.charAt(0).toUpperCase() + spaced.slice(1) : "Action";
}
