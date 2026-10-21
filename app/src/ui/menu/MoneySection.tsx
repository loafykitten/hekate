import { useEffect, useRef, useState } from "react";
import {
  useAddMoneyItem,
  useMoneyAccounts,
  useMoneySpending,
  useMoneyStatus,
  useMoneyTransactions,
  useNow,
  useRemoveMoneyItem,
  useSyncMoney,
} from "../../data/hooks";
import { formatMoney, groupAccounts, isCredit, monthLabel, monthOf, shiftMonth } from "../../data/money";
import type { MoneyAccount, MoneyItem } from "../../data/model";
import { clearLink, linkErrorText, oauthResume, openLink, readLink, storeLink, type PlaidError, type PlaidMetadata } from "../../data/plaid";
import { moneyApi } from "../../data/rest";
import { relativeTime } from "../../data/time";
import { useConversation } from "../ConversationContext";
import { Button, Chip, ConfirmButton, SectionTitle, StateMessage, TextField } from "../kit/kit";
import { useDebounced } from "../kit/useDebounced";
import { useMenuActive } from "./MenuContext";
import { AccountName, SpendingBars, TransactionRow } from "./moneyParts";
import styles from "./sections.module.css";
import money from "./money.module.css";

type Note = { tone: "info" | "danger"; text: string };

// Read-only bank data through Plaid: the banks that are connected, their balances, where this
// month's money went and the latest transactions. Nothing here can move money.
export function MoneySection() {
  const active = useMenuActive();
  const status = useMoneyStatus(active);
  const [note, setNote] = useState<Note>();
  const link = useLinker(setNote);

  // The browser came back from a bank's own site: pick Link up where it left off.
  const resumed = useRef(false);
  const { resume } = link;
  useEffect(() => {
    if (resumed.current) return;
    resumed.current = true;
    resume();
  }, [resume]);

  if (status.isError && !status.data) {
    return (
      <StateMessage kind="error" title="Couldn't load bank data" onRetry={() => void status.refetch()}>
        {status.error.message}
      </StateMessage>
    );
  }
  if (!status.data) return <StateMessage kind="loading" />;
  if (!status.data.configured) {
    return (
      <div className={styles.stack}>
        {note && <NoteView note={note} />}
        <StateMessage kind="empty" title="Bank data isn't set up yet">
          Renamon can read balances and transactions through Plaid, read only. Add the Plaid keys to the homelab&rsquo;s environment and restart Hekate; there is nothing to show until then.
        </StateMessage>
      </div>
    );
  }

  const items = status.data.items;
  return (
    <div className={styles.stack}>
      {note && <NoteView note={note} />}
      {items.length === 0 ? (
        <StateMessage kind="empty" title="No bank connected">
          Connect a bank and Renamon can see your balances and spending. She can only read, never move money.
          <span className={money.aside}>This works best from a computer: some banks open their own sign-in in a pop-up window.</span>
        </StateMessage>
      ) : (
        <Banks items={items} active={active} link={link} setNote={setNote} />
      )}
      {items.length === 0 && (
        <div>
          <Button variant="primary" busy={link.busy} onClick={() => void link.connect()}>
            Connect a bank
          </Button>
        </div>
      )}
      {items.length > 0 && <Spending active={active} />}
      {items.length > 0 && <Transactions active={active} />}
    </div>
  );
}

function NoteView({ note }: { note: Note }) {
  return (
    <p className={styles.callout} data-tone={note.tone === "danger" ? "danger" : "accent"} role={note.tone === "danger" ? "alert" : "status"}>
      {note.text}
    </p>
  );
}

// --- Plaid Link ------------------------------------------------------------------------------

interface Linker {
  busy: boolean;
  connect: () => Promise<void>;
  reconnect: (itemId: string) => Promise<void>;
  resume: () => void;
}

function useLinker(setNote: (n: Note | undefined) => void): Linker {
  const add = useAddMoneyItem();
  const sync = useSyncMoney();
  const [busy, setBusy] = useState(false);

  // Finishes a Link session: a new bank is exchanged and saved, a reconnect only needs a fresh sync.
  const callbacksFor = (itemId?: string) => ({
    onSuccess: (publicToken: string, metadata: PlaidMetadata) => {
      clearLink();
      setBusy(true);
      const done = itemId
        ? sync.mutateAsync().then(() => setNote({ tone: "info", text: "Reconnected. Syncing now." }))
        : add
            .mutateAsync({
              publicToken,
              institution: { id: metadata.institution?.institution_id ?? "", name: metadata.institution?.name ?? "" },
            })
            .then(() => setNote({ tone: "info", text: "Connected. Transactions arrive over the next few minutes while the bank shares its history." }));
      done.catch((e: unknown) => setNote({ tone: "danger", text: `Couldn't finish: ${e instanceof Error ? e.message : "something went wrong"}` })).finally(() => setBusy(false));
    },
    onExit: (error: PlaidError | null) => {
      clearLink();
      setBusy(false);
      setNote(error ? { tone: "danger", text: linkErrorText(error) } : { tone: "info", text: "Nothing was changed." });
    },
  });

  async function start(itemId?: string) {
    setNote(undefined);
    setBusy(true);
    try {
      const token = await moneyApi.linkToken(itemId);
      // A bank that signs in on its own site returns to /money/oauth, which needs the token again.
      storeLink(token, itemId);
      await openLink(token, callbacksFor(itemId));
      // Link is up (its own window); the button frees up again when it reports back.
      setBusy(false);
    } catch (e) {
      clearLink();
      setBusy(false);
      setNote({ tone: "danger", text: e instanceof Error ? e.message : "Couldn't start the bank connection." });
    }
  }

  function resume() {
    const decision = oauthResume(location.pathname, readLink());
    if (decision.kind === "none") return;
    // Plaid wants the full address the bank sent us back to; the address then goes back to the app.
    const received = location.href;
    history.replaceState(history.state, "", "/?menu=money");
    if (decision.kind === "lost") {
      setNote({ tone: "info", text: "That bank sign-in didn't carry over. Try connecting again, ideally from the same browser." });
      return;
    }
    const { link } = decision;
    openLink(link.token, callbacksFor(link.itemId), received).catch((e: unknown) => {
      setNote({ tone: "danger", text: e instanceof Error ? e.message : "Couldn't resume the bank connection." });
    });
  }

  return { busy: busy || add.isPending || sync.isPending, connect: () => start(), reconnect: (id) => start(id), resume };
}

// --- banks and accounts ----------------------------------------------------------------------

function Banks({ items, active, link, setNote }: { items: MoneyItem[]; active: boolean; link: Linker; setNote: (n: Note | undefined) => void }) {
  const now = useNow();
  const conversation = useConversation();
  const accounts = useMoneyAccounts(active);
  const sync = useSyncMoney();
  const remove = useRemoveMoneyItem();
  const byItem = groupAccounts(accounts.data ?? []);
  const known = new Set(items.map((i) => i.id));
  const orphans = [...byItem].filter(([id]) => !known.has(id));

  return (
    <section>
      <SectionTitle
        action={
          <Button
            compact
            variant="quiet"
            busy={sync.isPending}
            onClick={() =>
              sync.mutate(undefined, {
                onSuccess: () => conversation.toast("info", "Syncing your banks. New activity appears in a moment.", "money-sync"),
              })
            }
          >
            Sync now
          </Button>
        }
      >
        Banks
      </SectionTitle>
      <div className={styles.stack} style={{ gap: "var(--space-4)" }}>
        {items.map((item) => (
          <div key={item.id} className={styles.group}>
            <div className={money.bankHead}>
              <div className={money.bankName}>
                <strong>{item.institution || "Bank"}</strong>
                <span>{item.last_sync_at ? `Synced ${relativeTime(item.last_sync_at, now)}` : "Not synced yet"}</span>
              </div>
              <span className={styles.pill} data-tone={item.status === "ok" ? "ok" : item.status === "login_required" ? "warn" : "danger"}>
                {item.status === "ok" ? "Connected" : item.status === "login_required" ? "Sign in again" : "Problem"}
              </span>
            </div>
            {item.status === "ok" && item.history !== "complete" && (
              <p className={money.bankNote}>
                {item.history === "pending"
                  ? "Gathering transactions from the bank. They arrive over the next few minutes."
                  : "The last few weeks are in. Older history is still on its way, so past months may look light."}
              </p>
            )}
            {item.status !== "ok" && (
              <p className={money.bankError}>
                {item.status === "login_required"
                  ? "The bank wants you to sign in again before Renamon can see new activity."
                  : item.error || "Something went wrong talking to the bank."}
              </p>
            )}
            <AccountList accounts={byItem.get(item.id) ?? []} loading={accounts.isPending} />
            <div className={money.bankActions}>
              {item.status === "login_required" && (
                <Button compact variant="primary" busy={link.busy} onClick={() => void link.reconnect(item.id)}>
                  Reconnect
                </Button>
              )}
              <ConfirmButton
                compact
                label="Disconnect"
                confirmLabel="Confirm disconnect"
                describe={item.institution || "bank"}
                busy={remove.isPending && remove.variables === item.id}
                onConfirm={() =>
                  remove.mutate(item.id, {
                    onSuccess: () => setNote({ tone: "info", text: `Disconnected ${item.institution || "the bank"}. Its accounts and transactions were removed from Hekate.` }),
                  })
                }
              />
            </div>
          </div>
        ))}
        {orphans.map(([id, list]) => (
          <div key={id} className={styles.group}>
            <div className={money.bankHead}>
              <div className={money.bankName}>
                <strong>{list[0].institution || "Bank"}</strong>
              </div>
            </div>
            <AccountList accounts={list} loading={false} />
          </div>
        ))}
        <div>
          <Button compact variant="quiet" busy={link.busy} onClick={() => void link.connect()}>
            Connect another bank
          </Button>
        </div>
        {accounts.isError && !accounts.data && (
          <StateMessage kind="error" title="Couldn't load accounts" onRetry={() => void accounts.refetch()}>
            {accounts.error.message}
          </StateMessage>
        )}
      </div>
    </section>
  );
}

function AccountList({ accounts, loading }: { accounts: MoneyAccount[]; loading: boolean }) {
  if (accounts.length === 0) return loading ? <StateMessage kind="loading" /> : <p className={money.bankError}>No accounts yet.</p>;
  return (
    <ul className={money.accounts}>
      {accounts.map((a) => (
        <li key={a.id} className={money.account}>
          <div className={money.accountName}>
            <span>
              <AccountName account={a} />
            </span>
            <small>{a.subtype || a.type}</small>
          </div>
          <div className={money.balance}>
            {a.balance.current !== null ? (
              <>
                <strong>{formatMoney(a.balance.current, a.currency)}</strong>
                <small>
                  {[
                    isCredit(a) ? "owed" : "",
                    a.balance.available !== null ? `${formatMoney(a.balance.available, a.currency)} available` : "",
                    a.balance.limit !== null ? `${formatMoney(a.balance.limit, a.currency)} limit` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </small>
              </>
            ) : (
              <small>No balance</small>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}

// --- spending --------------------------------------------------------------------------------

function Spending({ active }: { active: boolean }) {
  const now = useNow();
  const thisMonth = monthOf(now);
  const [month, setMonth] = useState(thisMonth);
  const spending = useMoneySpending(month, active);
  const data = spending.data;
  return (
    <section>
      <SectionTitle>Spending</SectionTitle>
      <div className={styles.stack} style={{ gap: "var(--space-3)" }}>
        <div className={styles.monthBar}>
          <Button compact variant="quiet" aria-label="Previous month" onClick={() => setMonth(shiftMonth(month, -1))}>
            &lsaquo;
          </Button>
          <h4 className={styles.monthName}>{monthLabel(month)}</h4>
          <Button compact variant="quiet" aria-label="Next month" disabled={month >= thisMonth} onClick={() => setMonth(shiftMonth(month, 1))}>
            &rsaquo;
          </Button>
        </div>
        {spending.isError && !data ? (
          <StateMessage kind="error" title="Couldn't load spending" onRetry={() => void spending.refetch()}>
            {spending.error.message}
          </StateMessage>
        ) : !data ? (
          <StateMessage kind="loading" />
        ) : (
          <div className={styles.group} style={{ padding: "var(--space-3)", opacity: spending.isPlaceholderData ? 0.6 : 1 }}>
            <SpendingBars data={data} />
          </div>
        )}
      </div>
    </section>
  );
}

// --- transactions ----------------------------------------------------------------------------

function Transactions({ active }: { active: boolean }) {
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState("");
  const q = useDebounced(search.trim(), 250);
  const spending = useMoneySpending(monthOf(useNow()), active);
  const txns = useMoneyTransactions({ q, category }, active);
  const items = txns.data ?? [];
  const filtered = q !== "" || category !== "";
  // The chips offer the categories seen this month, plus the one picked if it's from elsewhere.
  const cats = new Map((spending.data?.categories ?? []).map((c) => [c.category, c.label]));
  if (category && !cats.has(category)) cats.set(category, category);
  return (
    <section>
      <SectionTitle>Recent transactions</SectionTitle>
      <div className={styles.stack} style={{ gap: "var(--space-3)" }}>
        <TextField label="Search transactions" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Merchant or description" enterKeyHint="search" autoComplete="off" />
        {cats.size > 0 && (
          <div className={styles.chips} role="group" aria-label="Category">
            <Chip selected={category === ""} onClick={() => setCategory("")}>
              All
            </Chip>
            {[...cats].map(([key, label]) => (
              <Chip key={key} selected={category === key} onClick={() => setCategory(key)}>
                {label}
              </Chip>
            ))}
          </div>
        )}
        {txns.isError && !txns.data ? (
          <StateMessage kind="error" title="Couldn't load transactions" onRetry={() => void txns.refetch()}>
            {txns.error.message}
          </StateMessage>
        ) : txns.isPending ? (
          <StateMessage kind="loading" />
        ) : items.length === 0 ? (
          <StateMessage kind="empty" title={filtered ? "No matches" : "No transactions yet"}>
            {filtered ? "Try different words or another category." : "They appear after the first sync."}
          </StateMessage>
        ) : (
          <div className={styles.group}>
            {items.map((t) => (
              <TransactionRow key={t.id} txn={t} showAccount />
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
