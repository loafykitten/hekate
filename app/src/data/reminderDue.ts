import type { Reminder } from "./model";
import { toMs } from "./time";

// A scheduled reminder is "due soon" this close to its time; only then does Done make sense.
export const DUE_SOON_MS = 60 * 60 * 1000;

// Done is for a reminder that has fired or is about to. On a freshly made future reminder it
// reads like "accept", so it isn't offered there.
export function showsDone(r: Reminder, nowMs: number): boolean {
  if (r.status === "fired") return true;
  if (r.status !== "scheduled") return false;
  return toMs(r.at) - nowMs <= DUE_SOON_MS;
}

// Snooze applies to any reminder still pending.
export function showsSnooze(r: Reminder): boolean {
  return r.status === "scheduled" || r.status === "fired";
}

// A reminder marked done can be put back at its original time while that time is still ahead.
export function canReopenAtOriginalTime(r: Reminder, nowMs: number): boolean {
  return toMs(r.at) > nowMs;
}
