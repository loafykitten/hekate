import { describe, expect, it } from "vitest";
import { invalidationKeys, keys } from "./queryKeys";

describe("invalidationKeys", () => {
  it("maps each domain to its query key prefix", () => {
    expect(invalidationKeys(["tasks"])).toEqual([["tasks"]]);
    expect(invalidationKeys(["mail", "approvals"])).toEqual([["mail"], ["approvals"]]);
    expect(invalidationKeys(["changes"])).toEqual([["changes"]]);
    expect(invalidationKeys(["tasks", "reminders", "calendar", "memory", "weather"])).toEqual([["tasks"], ["reminders"], ["calendar"], ["memory"], ["weather"]]);
  });

  it("ignores unknown domains, duplicates and junk", () => {
    expect(invalidationKeys(["spaceships", "tasks", "tasks", 7, null])).toEqual([["tasks"]]);
    expect(invalidationKeys([])).toEqual([]);
    expect(invalidationKeys(undefined)).toEqual([]);
    expect(invalidationKeys("tasks")).toEqual([]);
  });

  it("covers every key a section uses", () => {
    const prefixOf = (key: readonly unknown[]) => [key[0]];
    expect(prefixOf(keys.tasks("Groceries", "open"))).toEqual(["tasks"]);
    expect(prefixOf(keys.lists)).toEqual(["tasks"]);
    expect(prefixOf(keys.reminders("upcoming"))).toEqual(["reminders"]);
    expect(prefixOf(keys.events("a", "b"))).toEqual(["calendar"]);
    expect(prefixOf(keys.calendarStatus)).toEqual(["calendar"]);
    expect(prefixOf(keys.memories("q", ""))).toEqual(["memory"]);
    expect(prefixOf(keys.core)).toEqual(["memory"]);
    expect(prefixOf(keys.weather(undefined))).toEqual(["weather"]);
    expect(prefixOf(keys.weather("Lisbon"))).toEqual(["weather"]);
    for (const key of [keys.mailStatus, keys.mailSummary, keys.mailMessages("q", "", ""), keys.mailMessage("1"), keys.mailRules]) expect(prefixOf(key)).toEqual(["mail"]);
    for (const key of [keys.approvals("pending"), keys.approval("1"), keys.approvalRules]) expect(prefixOf(key)).toEqual(["approvals"]);
    expect(prefixOf(keys.changes)).toEqual(["changes"]);
    for (const key of [keys.moneyStatus, keys.moneyAccounts, keys.moneyTransactions("q", ""), keys.moneySpending("2026-10")]) expect(prefixOf(key)).toEqual(["money"]);
    expect(invalidationKeys(["money"])).toEqual([["money"]]);
  });
});
