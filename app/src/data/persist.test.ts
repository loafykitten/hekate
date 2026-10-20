import { dehydrate, QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { createQueryPersister, PERSIST_KEY, persistableKey, safeStorage, shouldPersistQuery } from "./persist";
import { keys } from "./queryKeys";

describe("what is persisted", () => {
  it("keeps the menu's read queries", () => {
    for (const key of [
      keys.tasks(undefined, "open"),
      keys.lists,
      keys.reminders("upcoming"),
      keys.events("a", "b"),
      keys.calendarStatus,
      keys.memories("", ""),
      keys.core,
      keys.weather(undefined),
      keys.mailSummary,
      keys.mailMessages("", "", ""),
      keys.approvals("pending"),
      keys.changes,
      keys.ideas,
      keys.journal,
      keys.routines,
    ]) {
      expect(persistableKey(key), JSON.stringify(key)).toBe(true);
    }
  });

  it("leaves out a single message with its body, single approvals, searches and non-menu queries", () => {
    expect(persistableKey(keys.mailMessage("m1"))).toBe(false);
    expect(persistableKey(keys.approval("a1"))).toBe(false);
    expect(persistableKey(keys.mailMessages("invoice", "", ""))).toBe(false);
    expect(persistableKey(keys.memories("dentist", ""))).toBe(false);
    expect(persistableKey(["healthz"])).toBe(false);
    expect(persistableKey(["history"])).toBe(false);
  });

  it("keeps only queries that have data, including one whose refresh failed", async () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.lists, []);
    qc.setQueryData(keys.mailMessage("m1"), { body: { text: "secret" } });
    await qc.prefetchQuery({ queryKey: keys.ideas, queryFn: () => Promise.reject(new Error("down")), retry: false });
    qc.setQueryData(keys.changes, []);
    await qc.fetchQuery({ queryKey: keys.changes, queryFn: () => Promise.reject(new Error("down")), retry: false, staleTime: 0 }).catch(() => {});
    expect(qc.getQueryState(keys.changes)?.status).toBe("error");
    const stored = dehydrate(qc, { shouldDehydrateQuery: shouldPersistQuery }).queries.map((q) => q.queryKey[0]);
    expect(stored.sort()).toEqual(["changes", "tasks"]);
  });
});

describe("storage failures", () => {
  it("never throws when storage is blocked", () => {
    const blocked = safeStorage(() => {
      throw new DOMException("denied", "SecurityError");
    });
    expect(blocked.getItem("k")).toBeNull();
    expect(() => blocked.setItem("k", "v")).not.toThrow();
    expect(() => blocked.removeItem("k")).not.toThrow();
  });

  it("never throws when storage is full", () => {
    const full = {
      getItem: () => "x",
      setItem: () => {
        throw new DOMException("quota", "QuotaExceededError");
      },
      removeItem: () => {},
    } as unknown as Storage;
    const s = safeStorage(() => full);
    expect(s.getItem("k")).toBe("x");
    expect(() => s.setItem("k", "v")).not.toThrow();
  });

  it("treats missing storage as empty", () => {
    expect(safeStorage(() => undefined).getItem("k")).toBeNull();
  });

  it("the persister writes under its own key and restores nothing from broken storage", async () => {
    const data = new Map<string, string>();
    const persister = createQueryPersister({
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    });
    persister.persistClient({ timestamp: 1, buster: "b", clientState: { mutations: [], queries: [] } });
    await new Promise((r) => setTimeout(r, 1100));
    expect(data.has(PERSIST_KEY)).toBe(true);
    const broken = createQueryPersister(
      safeStorage(() => {
        throw new Error("blocked");
      }),
    );
    expect(await broken.restoreClient()).toBeUndefined();
  });
});
