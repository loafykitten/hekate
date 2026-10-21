import { dehydrate, QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { createQueryPersister, holdWhileChanging, PERSIST_KEY, persistableKey, safeStorage, shouldPersistQuery } from "./persist";
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
  });

  it("never keeps bank data", () => {
    for (const key of [keys.moneyStatus, keys.moneyAccounts, keys.moneyTransactions("", ""), keys.moneySpending("2026-10")]) {
      expect(persistableKey(key), JSON.stringify(key)).toBe(false);
    }
    const qc = new QueryClient();
    qc.setQueryData(keys.moneyAccounts, [{ id: "acc_1" }]);
    qc.setQueryData(keys.moneySpending("2026-10"), { spent: 1 });
    qc.setQueryData(keys.lists, []);
    expect(dehydrate(qc, { shouldDehydrateQuery: shouldPersistQuery }).queries.map((q) => q.queryKey[0])).toEqual(["tasks"]);
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

  it("keeps data only for a week from when it was fetched, however often it's saved again", () => {
    const qc = new QueryClient();
    const fetched = Date.now() - 8 * 24 * 60 * 60_000;
    qc.setQueryData(keys.lists, [], { updatedAt: fetched });
    qc.setQueryData(keys.changes, []);
    const stored = dehydrate(qc, { shouldDehydrateQuery: (q) => shouldPersistQuery(q) }).queries.map((q) => q.queryKey[0]);
    expect(stored).toEqual(["changes"]);
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

describe("holdWhileChanging", () => {
  it("stores nothing while a change is in flight, so an unconfirmed tick-off is never kept", async () => {
    const stored: unknown[] = [];
    const inner = { persistClient: (c: unknown) => void stored.push(c), restoreClient: async () => undefined, removeClient: () => undefined };
    const qc = new QueryClient();
    const persister = holdWhileChanging(inner, () => qc.isMutating() > 0);
    let finish!: () => void;
    const pending = qc.getMutationCache().build(qc, { mutationFn: () => new Promise<void>((r) => (finish = r)) }).execute(undefined);
    await Promise.resolve();
    await persister.persistClient({ timestamp: 1, buster: "", clientState: dehydrate(qc) });
    expect(stored).toHaveLength(0);
    finish();
    await pending;
    await persister.persistClient({ timestamp: 2, buster: "", clientState: dehydrate(qc) });
    expect(stored).toHaveLength(1);
  });
});

describe("restoring", () => {
  it("drops queries fetched more than a week ago, even in a copy saved since", async () => {
    const day = 24 * 60 * 60_000;
    const mem = new Map<string, string>();
    const storage = { getItem: (k: string) => mem.get(k) ?? null, setItem: (k: string, v: string) => void mem.set(k, v), removeItem: (k: string) => void mem.delete(k) };
    const qc = new QueryClient();
    const t0 = Date.now();
    qc.setQueryData(keys.lists, [], { updatedAt: t0 });
    qc.setQueryData(keys.changes, [], { updatedAt: t0 + 6 * day });
    mem.set(PERSIST_KEY, JSON.stringify({ timestamp: t0 + 6 * day, buster: "", clientState: dehydrate(qc) }));
    const restored = await createQueryPersister(storage, () => t0 + 8 * day).restoreClient();
    expect(restored?.clientState.queries.map((q) => q.queryKey[0])).toEqual(["changes"]);
  });
});
