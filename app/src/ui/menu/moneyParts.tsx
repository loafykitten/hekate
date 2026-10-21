import { barScale, compareLabel, formatMoney, isMoneyIn, transactionTitle } from "../../data/money";
import type { MoneyTransaction, Spending } from "../../data/model";
import { formatDay } from "../../data/time";
import type { MoneyAccount } from "../../data/model";
import money from "./money.module.css";

// Pieces shared by the Money section and the money cards.

export function TransactionRow({ txn, showAccount, highlight, compact }: { txn: MoneyTransaction; showAccount?: boolean; highlight?: boolean; compact?: boolean }) {
  const incoming = isMoneyIn(txn);
  return (
    <div className={money.txn} data-compact={compact || undefined} data-highlight={highlight || undefined}>
      <div className={money.txnBody}>
        <span className={money.txnTitle}>{transactionTitle(txn)}</span>
        <span className={money.txnMeta}>
          {[formatDay(txn.date), txn.category_label, showAccount ? txn.account : ""].filter(Boolean).join(" · ")}
          {txn.pending && <span className={money.pending}>Pending</span>}
        </span>
      </div>
      <span className={money.amount} data-in={incoming || undefined}>
        {formatMoney(txn.amount, txn.currency, { sign: true })}
      </span>
    </div>
  );
}

// A month's spending by category: this month's bar with last month's as a faint one under it, on
// one scale. Transfers and loan payments are not spending; they are noted apart.
export function SpendingBars({ data, compact }: { data: Spending; compact?: boolean }) {
  const scale = barScale(data.categories);
  const compare = compareLabel(data.spent, data.previous_spent);
  const rows = compact ? data.categories.slice(0, 5) : data.categories;
  return (
    <div className={money.spending}>
      <div className={money.totals}>
        <div>
          <small>Spent</small>
          <strong>{formatMoney(data.spent, data.currency)}</strong>
        </div>
        <div>
          <small>Received</small>
          <strong data-in>{formatMoney(data.received, data.currency)}</strong>
        </div>
      </div>
      {compare && <p className={money.compare}>{compare}</p>}
      {rows.length === 0 ? (
        <p className={money.quiet}>No spending this month.</p>
      ) : (
        <ul className={money.cats} aria-label="Spending by category">
          {rows.map((c) => (
            <li key={c.category}>
              <div className={money.catLine}>
                <span>{c.label}</span>
                <span className={money.catAmount}>{formatMoney(c.spent, data.currency, { whole: true })}</span>
              </div>
              <div className={money.bar} role="img" aria-label={`${c.label}: ${formatMoney(c.spent, data.currency, { whole: true })} this month, ${formatMoney(c.previous, data.currency, { whole: true })} last month`}>
                <span className={money.barNow} style={{ width: `${scale(c.spent)}%` }} />
                <span className={money.barPrev} style={{ width: `${scale(c.previous)}%` }} />
              </div>
            </li>
          ))}
        </ul>
      )}
      {compact && data.categories.length > rows.length && <p className={money.quiet}>and {data.categories.length - rows.length} more</p>}
      {!compact && data.categories.length > 0 && (
        <p className={money.legend}>
          <span className={money.keyNow} /> This month <span className={money.keyPrev} /> Last month
        </p>
      )}
      {data.transfers > 0 && (
        <p className={money.quiet}>Not counted: {formatMoney(data.transfers, data.currency, { whole: true })} in transfers and loan payments.</p>
      )}
    </div>
  );
}

// An account's name with its last digits kept together on one line.
export function AccountName({ account }: { account: Pick<MoneyAccount, "name" | "mask"> }) {
  return (
    <>
      {account.name}
      {account.mask && <span className={money.mask}>{`···· ${account.mask}`}</span>}
    </>
  );
}
