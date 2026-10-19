import type { HistoryMessage } from "../conversation/protocol";

export const HISTORY_PAGE = 50;

// One page of conversation history, oldest first. `before` is the id of the oldest message
// already shown.
export async function fetchMessages(before?: string, signal?: AbortSignal): Promise<HistoryMessage[]> {
  const params = new URLSearchParams({ limit: String(HISTORY_PAGE) });
  if (before) params.set("before", before);
  const res = await fetch(`/api/conversation/messages?${params}`, { signal, headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`messages: ${res.status}`);
  const body = (await res.json()) as { messages?: unknown };
  if (!Array.isArray(body.messages)) throw new Error("messages: unexpected response");
  return body.messages as HistoryMessage[];
}
