import type { ChangeRequest, ChangeStatus } from "./model";

// How the Changes section words a change request: a chip per status, and the one line that says
// where it stands.

export type ChangeTone = "ok" | "danger" | "accent" | undefined;

const STATUS: Record<ChangeStatus, { label: string; tone: ChangeTone }> = {
  proposed: { label: "Proposed", tone: undefined },
  denied: { label: "Denied", tone: undefined },
  expired: { label: "Expired", tone: undefined },
  queued: { label: "Queued", tone: "accent" },
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

// The line under the title: the worker's step while it works, the outcome once it's over, the
// owner's reason when it was denied.
export function changeSummary(c: ChangeRequest): string {
  if (c.status === "working") return c.stage;
  if (c.status === "denied") return c.feedback ?? "";
  if (c.status === "merged" || c.status === "rejected" || c.status === "failed") return c.result ?? c.stage;
  return "";
}
