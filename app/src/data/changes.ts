import type { ChangeRequest, ChangeStatus } from "./model";

// How the Changes section words a change request: a chip per status, and the one line that says
// where it stands.

export type ChangeTone = "ok" | "danger" | "accent" | undefined;

const STATUS: Record<ChangeStatus, { label: string; tone: ChangeTone }> = {
  proposed: { label: "Proposed", tone: undefined },
  denied: { label: "Denied", tone: undefined },
  expired: { label: "Expired", tone: undefined },
  queued: { label: "Approved", tone: "accent" },
  working: { label: "Working", tone: "accent" },
  merged: { label: "Merged", tone: "ok" },
  rejected: { label: "Rejected", tone: "danger" },
  failed: { label: "Failed", tone: "danger" },
};

export const changeStatusLabel = (s: ChangeStatus): string => STATUS[s].label;
export const changeStatusTone = (s: ChangeStatus): ChangeTone => STATUS[s].tone;

// "feature" -> "Feature"; an unknown kind reads as plain words.
export function changeKindLabel(kind: string): string {
  const spaced = kind.replace(/[_-]+/g, " ").trim();
  return spaced ? spaced.charAt(0).toUpperCase() + spaced.slice(1) : "Change";
}

// The line under the title: the current step while it's being built, the outcome once it's over, the
// owner's reason when it was denied.
// A change that failed or was turned down can go back on the list. (A proposal whose approval
// itself failed reads "failed" too, but nobody ever took it up.)
export function canRetry(c: ChangeRequest): boolean {
  return (c.status === "failed" || c.status === "rejected") && c.attempts > 0;
}

export function changeSummary(c: ChangeRequest): string {
  if (c.status === "working") return c.stage;
  if (c.status === "queued" && c.attempts > 0) return "Back on the list";
  if (c.status === "denied") return c.feedback ?? "";
  if (c.status === "merged" || c.status === "rejected" || c.status === "failed") return c.result ?? c.stage;
  return "";
}
