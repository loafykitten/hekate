import { describe, expect, it } from "vitest";
import {
  asMoneyAccount,
  asMoneyItem,
  asMoneyStatus,
  asMoneyTransaction,
  asSpending,
  parseCard,
} from "./model";
import { barScale, compareLabel, formatMoney, groupAccounts, monthLabel, monthOf, shiftMonth, transactionTitle } from "./money";
import { clearLink, oauthResume, readLink, storeLink } from "./plaid";

const item = { id: "1", institution: "Example Bank", status: "ok", error: "", last_sync_at: "2026-10-20T14:00:00-05:00", accounts: 2 };
const account = {
  id: "acc_1",
  item_id: "1",
  institution: "Example Bank",
  name: "Spending",
  mask: "1234",
  type: "depository",
  subtype: "checking",
  balance: { current: 1520.31, available: 1490.31, limit: null },
  currency: "USD",
  updated_at: "x",
};
const txn = {
  id: "txn_1",
  account_id: "acc_1",
  account: "Spending",
  date: "2026-10-19",
  name: "SQ *CORNER CAFE",
  merchant: "Corner Cafe",
  amount: -6.75,
  currency: "USD",
  pending: false,
  category: "FOOD_AND_DRINK",
  category_label: "Food and drink",
  detail: null,
};
const spending = {
  month: "2026-10",
  currency: "USD",
  spent: 1234.56,
  received: 2400,
  categories: [{ category: "FOOD_AND_DRINK", label: "Food and drink", spent: 312.4, count: 23, previous: 280.1 }],
  previous_spent: 1100,
  transfers: 500,
};

describe("money guards", () => {
  it("reads an item and defaults an unknown status to a problem", () => {
    expect(asMoneyItem(item)).toEqual({ ...item, status: "ok", history: "complete" });
    expect(asMoneyItem({ ...item, history: "partial" })?.history).toBe("partial");
    expect(asMoneyItem({ ...item, history: "later" })?.history).toBe("pending");
    expect(asMoneyItem({ ...item, status: "weird" })?.status).toBe("error");
    expect(asMoneyItem({ institution: "x" })).toBeUndefined();
    expect(asMoneyItem(null)).toBeUndefined();
  });

  it("reads the status, tolerating a null items list", () => {
    expect(asMoneyStatus({ configured: true, items: [item, 5] })).toEqual({ configured: true, items: [asMoneyItem(item)] });
    expect(asMoneyStatus({ configured: true, items: null })).toEqual({ configured: true, items: [] });
    expect(asMoneyStatus(undefined)).toEqual({ configured: false, items: [] });
  });

  it("reads an account, with null for balances the bank doesn't give", () => {
    expect(asMoneyAccount(account)?.balance).toEqual({ current: 1520.31, available: 1490.31, limit: null });
    expect(asMoneyAccount({ ...account, balance: { current: "12" } })?.balance).toEqual({ current: null, available: null, limit: null });
    expect(asMoneyAccount({ ...account, name: 3 })).toBeUndefined();
  });

  it("reads a transaction and drops one without a numeric amount", () => {
    expect(asMoneyTransaction(txn)?.merchant).toBe("Corner Cafe");
    expect(asMoneyTransaction({ ...txn, merchant: null, category: null })).toMatchObject({ merchant: null, category: null, pending: false });
    expect(asMoneyTransaction({ ...txn, amount: "6.75" })).toBeUndefined();
    expect(asMoneyTransaction({ ...txn, id: undefined })).toBeUndefined();
  });

  it("reads spending and rejects a malformed month", () => {
    expect(asSpending(spending)?.categories).toHaveLength(1);
    expect(asSpending({ ...spending, categories: null })?.categories).toEqual([]);
    expect(asSpending({ ...spending, month: "October" })).toBeUndefined();
  });

  it("parses the three money cards", () => {
    expect(parseCard({ kind: "accounts", id: "a", title: "Accounts", data: { items: [account, 1] } })).toMatchObject({ kind: "accounts", data: { items: [{ id: "acc_1" }] } });
    expect(parseCard({ kind: "transactions", id: "t", title: "", data: { items: [txn], highlight: ["txn_1"] } })).toMatchObject({ kind: "transactions", data: { highlight: ["txn_1"] } });
    expect(parseCard({ kind: "spending", id: "s", title: "", data: spending })).toMatchObject({ kind: "spending", data: { month: "2026-10" } });
    expect(parseCard({ kind: "spending", id: "s", title: "", data: { month: 5 } })).toBeUndefined();
  });
});

describe("formatting", () => {
  it("formats amounts with the currency and an optional sign", () => {
    expect(formatMoney(1520.31, "USD")).toContain("1,520.31");
    expect(formatMoney(-6.75, "USD", { sign: true })).toMatch(/[-−]\$?6\.75/);
    expect(formatMoney(2400, "USD", { sign: true })).toContain("+");
    expect(formatMoney(312.4, "USD", { whole: true })).not.toContain(".");
  });

  it("survives a currency code Intl doesn't know", () => {
    expect(formatMoney(5, "??")).toBe("5.00 ??");
  });

  it("prefers the merchant over the bank's text", () => {
    expect(transactionTitle({ merchant: "Corner Cafe", name: "SQ *X" })).toBe("Corner Cafe");
    expect(transactionTitle({ merchant: null, name: "SQ *X" })).toBe("SQ *X");
    expect(transactionTitle({ merchant: null, name: "" })).toBe("Transaction");
  });
});

describe("months", () => {
  it("names the month of a time and steps across year ends", () => {
    expect(monthOf(new Date(2026, 9, 20).getTime())).toBe("2026-10");
    expect(shiftMonth("2026-10", -1)).toBe("2026-09");
    expect(shiftMonth("2026-01", -1)).toBe("2025-12");
    expect(shiftMonth("2026-12", 1)).toBe("2027-01");
    expect(monthLabel("2026-10")).toContain("2026");
  });
});

describe("spending bars", () => {
  it("scales this and last month on one scale", () => {
    const scale = barScale([
      { spent: 200, previous: 400 },
      { spent: 100, previous: 0 },
    ]);
    expect(scale(400)).toBe(100);
    expect(scale(200)).toBe(50);
    expect(barScale([])(5)).toBe(0);
  });

  it("compares with last month in words", () => {
    expect(compareLabel(112, 100)).toBe("12% more than last month");
    expect(compareLabel(80, 100)).toBe("20% less than last month");
    expect(compareLabel(101, 100)).toBe("About the same as last month");
    expect(compareLabel(50, 0)).toBe("");
  });
});

describe("groupAccounts", () => {
  it("groups by bank item", () => {
    const a = asMoneyAccount(account)!;
    const b = asMoneyAccount({ ...account, id: "acc_2", item_id: "2" })!;
    const groups = groupAccounts([a, b, { ...a, id: "acc_3" }]);
    expect([...groups.keys()]).toEqual(["1", "2"]);
    expect(groups.get("1")).toHaveLength(2);
  });
});

function fakeStorage() {
  const data = new Map<string, string>();
  const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v), removeItem: (k: string) => void data.delete(k) } as Storage;
  return () => storage;
}

describe("the OAuth hop", () => {
  it("keeps the link token for a while, and a reconnect's item", () => {
    const st = fakeStorage();
    storeLink("link-sandbox-1", "7", 1_000, st);
    expect(readLink(2_000, st)).toEqual({ token: "link-sandbox-1", itemId: "7", at: 1_000 });
    storeLink("link-sandbox-2", undefined, 1_000, st);
    expect(readLink(2_000, st)).toEqual({ token: "link-sandbox-2", itemId: undefined, at: 1_000 });
    expect(readLink(1_000 + 31 * 60_000, st)).toBeUndefined();
    clearLink(st);
    expect(readLink(2_000, st)).toBeUndefined();
  });

  it("ignores junk in storage and storage that throws", () => {
    const st = fakeStorage();
    st().setItem("hekate-plaid-link", "{not json");
    expect(readLink(0, st)).toBeUndefined();
    const broken = () => {
      throw new Error("blocked");
    };
    expect(() => storeLink("t", undefined, 0, broken)).not.toThrow();
    expect(readLink(0, broken)).toBeUndefined();
    expect(() => clearLink(broken)).not.toThrow();
  });

  it("resumes only on /money/oauth, and only with a token", () => {
    const link = { token: "t", at: 1 };
    expect(oauthResume("/", link)).toEqual({ kind: "none" });
    expect(oauthResume("/money/oauth", link)).toEqual({ kind: "resume", link });
    expect(oauthResume("/money/oauth/", link)).toEqual({ kind: "resume", link });
    expect(oauthResume("/money/oauth", undefined)).toEqual({ kind: "lost" });
  });
});
