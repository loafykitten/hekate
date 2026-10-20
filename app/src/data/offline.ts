import type { Query } from "@tanstack/react-query";
import { menuData } from "./persist";
import { dayKeyOfMs, formatDateTime, formatTime } from "./time";

// The oldest "last updated" among the menu data on screen: the time the note says the data is from.
export function oldestUpdate(queries: Pick<Query, "queryKey" | "state">[]): number | undefined {
  let oldest: number | undefined;
  for (const q of queries) {
    if (!menuData(q) || !q.state.dataUpdatedAt) continue;
    if (oldest === undefined || q.state.dataUpdatedAt < oldest) oldest = q.state.dataUpdatedAt;
  }
  return oldest;
}

// "Offline · as of 2:02 PM", with the day when it isn't today.
export function offlineNote(updatedAt: number, nowMs: number): string {
  const iso = new Date(updatedAt).toISOString();
  const when = dayKeyOfMs(updatedAt) === dayKeyOfMs(nowMs) ? formatTime(iso) : formatDateTime(iso);
  return `Offline · as of ${when}`;
}
