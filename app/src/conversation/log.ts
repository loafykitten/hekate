import type { HistoryMessage } from "./protocol";

// One turn as it happens, before the server's history has caught up.
export interface LiveEntry {
  id: string;
  turnId: string;
  role: "user" | "assistant";
  text: string;
  done: boolean;
  // historyEpoch value once its turn ended; history fetched at or after it contains this entry.
  endEpoch?: number;
}

export interface LogEntry {
  id: string;
  role: "user" | "assistant";
  text: string;
  // Still being written (assistant) so the log can show it as in progress.
  pending: boolean;
}

// History from REST (oldest first) followed by live turns. `loadedEpoch` is the history epoch the
// REST data was fetched at: a finished live entry whose turn ended at or before it is already in
// that history (the server's copy is authoritative), so it is dropped rather than shown twice.
export function mergeLog(history: readonly HistoryMessage[], live: readonly LiveEntry[], loadedEpoch = -1): LogEntry[] {
  const entries: LogEntry[] = history.map((m) => ({ id: m.id, role: m.role, text: m.text, pending: false }));
  for (const e of live) {
    if (e.endEpoch !== undefined && e.endEpoch <= loadedEpoch) continue;
    if (e.done && !e.text.trim()) continue;
    entries.push({ id: e.id, role: e.role, text: e.text, pending: !e.done });
  }
  return entries;
}
