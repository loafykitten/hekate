import type { MoneyAccount, MoneyTransaction } from "./model";

// Display helpers for the Money section and its cards.

// An amount in its currency. A code Intl doesn't know falls back to a plain number with the code.
export function formatMoney(amount: number, currency: string, opts: { sign?: boolean; whole?: boolean } = {}): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency,
      signDisplay: opts.sign ? "exceptZero" : "auto",
      ...(opts.whole ? { maximumFractionDigits: 0 } : {}),
    }).format(amount);
  } catch {
    return `${opts.sign && amount > 0 ? "+" : ""}${amount.toFixed(opts.whole ? 0 : 2)} ${currency}`;
  }
}

// --- months ----------------------------------------------------------------------------------

export function monthOf(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export function shiftMonth(month: string, by: number): string {
  const [y, m] = month.split("-").map(Number);
  const index = y * 12 + (m - 1) + by;
  return `${Math.floor(index / 12)}-${String((index % 12) + 1).padStart(2, "0")}`;
}

export function monthLabel(month: string): string {
  const [y, m] = month.split("-").map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

// --- spending bars ---------------------------------------------------------------------------

// How far each bar runs, as a percentage of the longest (this month's or last month's, whichever
// is bigger), so the two months are comparable on one scale.
export function barScale(rows: { spent: number; previous: number }[]): (value: number) => number {
  const max = Math.max(0, ...rows.flatMap((r) => [r.spent, r.previous]));
  return (value) => (max <= 0 ? 0 : Math.min(100, Math.max(0, (value / max) * 100)));
}

// "12% more than last month", "about the same", or nothing when there is nothing to compare with.
export function compareLabel(spent: number, previous: number): string {
  if (previous <= 0) return "";
  const change = (spent - previous) / previous;
  if (Math.abs(change) < 0.03) return "About the same as last month";
  const pct = Math.round(Math.abs(change) * 100);
  return `${pct}% ${change > 0 ? "more" : "less"} than last month`;
}

// --- transactions ----------------------------------------------------------------------------

export function transactionTitle(t: Pick<MoneyTransaction, "merchant" | "name">): string {
  return t.merchant || t.name || "Transaction";
}

export function isMoneyIn(t: Pick<MoneyTransaction, "amount">): boolean {
  return t.amount > 0;
}

// --- accounts --------------------------------------------------------------------------------

export function isCredit(a: Pick<MoneyAccount, "type">): boolean {
  return a.type === "credit" || a.type === "loan";
}

export function accountLabel(a: Pick<MoneyAccount, "name" | "mask">): string {
  return a.mask ? `${a.name} ···· ${a.mask}` : a.name;
}

export function groupAccounts(accounts: MoneyAccount[]): Map<string, MoneyAccount[]> {
  const groups = new Map<string, MoneyAccount[]>();
  for (const a of accounts) {
    const key = a.item_id || a.institution;
    groups.set(key, [...(groups.get(key) ?? []), a]);
  }
  return groups;
}
