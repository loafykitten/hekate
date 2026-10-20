import { createSyncStoragePersister } from "@tanstack/query-sync-storage-persister";
import type { Query, QueryKey } from "@tanstack/react-query";
import { DOMAINS } from "./queryKeys";

// Keeping the menu's last-known data across reloads, so the app opens offline showing what it last
// had. Only successful reads of the menu domains are kept (see persistableKey).

declare const __BUILD_ID__: string | undefined;

export const PERSIST_KEY = "hekate-query-cache";
export const PERSIST_MAX_AGE = 7 * 24 * 60 * 60_000; // a week
// A hash of the data layer's source (vite.config.ts): when response shapes can change, the stored
// cache is thrown away instead of being fed to code that expects something else.
export const PERSIST_BUSTER = typeof __BUILD_ID__ === "string" ? __BUILD_ID__ : "dev";

// A key's second part is "message" or "item" for single things fetched on demand: a mail message
// with its body, one approval. Those are never kept. Searches are never kept either: what she
// looked for isn't worth storing.
export function persistableKey(key: QueryKey): boolean {
  const [domain, kind, q] = key;
  if (typeof domain !== "string" || !(DOMAINS as readonly string[]).includes(domain)) return false;
  if ((domain === "mail" && kind === "message") || (domain === "approvals" && kind === "item")) return false;
  if ((domain === "mail" || domain === "memory") && kind === "list" && q !== "") return false;
  return true;
}

// Successful reads only. A refresh that failed (the service is down) leaves the query in the error
// state with its last good data; that data is still kept, or going offline would erase it.
export function shouldPersistQuery(query: Pick<Query, "queryKey" | "state">): boolean {
  const { status, data } = query.state;
  return (status === "success" || (status === "error" && data !== undefined)) && persistableKey(query.queryKey);
}

// localStorage that never throws: blocked, full or unavailable storage just means no cache.
export function safeStorage(get: () => Storage | undefined = () => globalThis.localStorage): Pick<Storage, "getItem" | "setItem" | "removeItem"> {
  const guarded = <T,>(fn: (s: Storage) => T, fallback: T): T => {
    try {
      const s = get();
      return s ? fn(s) : fallback;
    } catch {
      return fallback;
    }
  };
  return {
    getItem: (k) => guarded((s) => s.getItem(k), null),
    setItem: (k, v) => guarded((s) => s.setItem(k, v), undefined),
    removeItem: (k) => guarded((s) => s.removeItem(k), undefined),
  };
}

export function createQueryPersister(storage = safeStorage()) {
  return createSyncStoragePersister({ storage: storage as Storage, key: PERSIST_KEY, throttleTime: 1000 });
}
