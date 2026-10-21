import { useMemo, useState, type MouseEvent, type ReactNode } from "react";
import { senderName } from "../../data/mail";
import type { Card, Memory, Message, MoneyAccount, Reminder, Task } from "../../data/model";
import { useDeleteMemory, useLiveApproval, useNow, useReminderDone, useSetTaskDone, useSnoozeReminder } from "../../data/hooks";
import { dayHeading, dayKeyOfMs, groupEventsByDay, formatDay, formatTime, formatWeekday } from "../../data/time";
import { degrees, GROUP_LABEL, isDaytime, precipLabel, weatherGroup } from "../../data/weather";
import type { Section } from "../menu/sections";
import { BellIcon, CalendarIcon, CloseIcon, ExternalIcon, MailIcon, MemoryIcon, MoneyIcon, ShieldIcon, TasksIcon, TodayIcon } from "../icons";
import { Button } from "../kit/kit";
import { formatMoney, isCredit } from "../../data/money";
import { AccountName, SpendingBars, TransactionRow } from "../menu/moneyParts";
import money from "../menu/money.module.css";
import { ApprovalPanel } from "../approvals/ApprovalPanel";
import { setMailTarget } from "../menu/mailTarget";
import { EventRow, MemoryRow, MessageRow, ReminderRow, TaskRow } from "../rows/rows";
import { sectionOfCard } from "./sectionOfCard";
import styles from "./cards.module.css";
import { WeatherIcon } from "./WeatherIcon";

// One component per card kind. A card is a snapshot of what she found or changed; the rows
// that can act (checking a task, snoozing a reminder, forgetting a memory) call the same REST
// endpoints as the menu, with the result shown at once and undone if the service refuses.

const KIND_ICON: Partial<Record<Card["kind"], ReactNode>> = {
  tasks: <TasksIcon />,
  reminders: <BellIcon />,
  events: <CalendarIcon />,
  memory: <MemoryIcon />,
  weather: <TodayIcon />,
  mail: <MailIcon />,
  mail_message: <MailIcon />,
  approval: <ShieldIcon />,
  accounts: <MoneyIcon />,
  transactions: <MoneyIcon />,
  spending: <MoneyIcon />,
};

const DEFAULT_TITLE: Record<Card["kind"], string> = {
  tasks: "Tasks",
  reminders: "Reminders",
  events: "Calendar",
  memory: "Memory",
  weather: "Weather",
  web_results: "Search results",
  web_page: "Page",
  mail: "Mail",
  mail_message: "Mail",
  approval: "Approval",
  accounts: "Accounts",
  transactions: "Transactions",
  spending: "Spending",
};

interface FrameProps {
  card: Card;
  onClose: () => void;
  onOpenSection: (section: Section) => void;
  children: ReactNode;
}

// Clicks on the card's own surface open its menu section; controls inside keep their meaning.
function isInteractive(target: EventTarget | null): boolean {
  return target instanceof Element && !!target.closest("a, button, input, select, textarea, label, summary");
}

export function CardFrame({ card, onClose, onOpenSection, children }: FrameProps) {
  const section = sectionOfCard(card);
  // An approval's own title is the question, shown in full in the body.
  const title = card.kind === "approval" ? DEFAULT_TITLE.approval : card.title || DEFAULT_TITLE[card.kind];
  return (
    <article
      className={styles.card}
      data-kind={card.kind}
      data-openable={section ? true : undefined}
      aria-label={title}
      onClick={(e: MouseEvent) => {
        if (section && !isInteractive(e.target)) onOpenSection(section);
      }}
    >
      <header className={styles.header}>
        <span className={styles.kindIcon} aria-hidden="true">
          {KIND_ICON[card.kind] ?? <ExternalIcon />}
        </span>
        {section ? (
          <button type="button" className={styles.titleButton} data-swipe-ok onClick={() => onOpenSection(section)} aria-label={`Open ${title} in the menu`}>
            {title}
          </button>
        ) : (
          <h2 className={styles.title}>{title}</h2>
        )}
        <button type="button" className={styles.close} aria-label={`Dismiss ${title}`} onClick={onClose}>
          <CloseIcon />
        </button>
      </header>
      <div className={styles.body}>{children}</div>
    </article>
  );
}

export function CardView({ card, onClose, onOpenSection }: Omit<FrameProps, "children">) {
  let content: ReactNode;
  switch (card.kind) {
    case "tasks":
      content = <TasksCard items={card.data.items} highlight={card.data.highlight} record={card.data.record} />;
      break;
    case "reminders":
      content = <RemindersCard items={card.data.items} highlight={card.data.highlight} />;
      break;
    case "events":
      content = <EventsCard data={card.data} />;
      break;
    case "memory":
      content = <MemoryCard items={card.data.items} />;
      break;
    case "weather":
      content = <WeatherCard data={card.data} />;
      break;
    case "web_results":
      content = <WebResultsCard data={card.data} />;
      break;
    case "web_page":
      content = <WebPageCard data={card.data} />;
      break;
    case "mail":
      content = <MailCard data={card.data} onOpenSection={onOpenSection} />;
      break;
    case "mail_message":
      content = <MailMessageCard data={card.data} onOpenSection={onOpenSection} />;
      break;
    case "approval":
      content = <ApprovalCard approval={card.data} />;
      break;
    case "accounts":
      content = <AccountsCard items={card.data.items} />;
      break;
    case "transactions":
      content = <TransactionsCard data={card.data} />;
      break;
    case "spending":
      content = <SpendingBars data={card.data} compact />;
      break;
  }
  // Opening a mail card from its title lands on the message it is about.
  const openFromFrame = (section: Section) => {
    if (card.kind === "mail_message") setMailTarget(card.data.message.id);
    onOpenSection(section);
  };
  return (
    <CardFrame card={card} onClose={onClose} onOpenSection={openFromFrame}>
      {content}
    </CardFrame>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className={styles.empty}>{children}</p>;
}

// --- tasks ---------------------------------------------------------------------------------

function TasksCard({ items, highlight, record }: { items: Task[]; highlight: string[]; record?: boolean }) {
  const now = useNow();
  const setDone = useSetTaskDone();
  const [local, setLocal] = useState<Record<string, Task>>({});
  const tasks = items.map((t) => local[t.id] ?? t);

  function toggle(task: Task, done: boolean) {
    const original = local[task.id] ?? task;
    // Show it checked at once (a repeating task jumps to its next date when the service answers).
    setLocal((l) => ({ ...l, [task.id]: { ...original, completed_at: done ? new Date().toISOString() : null } }));
    setDone.mutate(
      { id: task.id, done, due: original.due },
      {
        onSuccess: (updated) => setLocal((l) => ({ ...l, [task.id]: updated })),
        onError: () => setLocal((l) => ({ ...l, [task.id]: original })),
      },
    );
  }

  if (tasks.length === 0) return <Empty>Nothing on this list.</Empty>;
  return (
    <div className={styles.list}>
      {tasks.map((t) => (
        <TaskRow key={t.id} task={t} now={now} compact highlight={highlight.includes(t.id)} showList={new Set(tasks.map((x) => x.list)).size > 1} onToggle={record ? undefined : (done) => toggle(t, done)} busy={setDone.isPending && setDone.variables?.id === t.id} />
      ))}
    </div>
  );
}

// --- reminders -----------------------------------------------------------------------------

function RemindersCard({ items, highlight }: { items: Reminder[]; highlight: string[] }) {
  const now = useNow();
  const snooze = useSnoozeReminder();
  const markDone = useReminderDone();
  const [local, setLocal] = useState<Record<string, Reminder>>({});
  const reminders = items.map((r) => local[r.id] ?? r);

  function done(r: Reminder) {
    markDone.mutate(r.id, { onSuccess: (updated) => setLocal((l) => ({ ...l, [r.id]: updated })) });
  }
  function snoozeBy(r: Reminder, minutes: number) {
    snooze.mutate({ id: r.id, minutes }, { onSuccess: (updated) => setLocal((l) => ({ ...l, [r.id]: updated })) });
  }

  if (reminders.length === 0) return <Empty>No reminders.</Empty>;
  return (
    <div className={styles.list}>
      {reminders.map((r) => (
        <ReminderRow key={r.id} reminder={r} now={now} compact highlight={highlight.includes(r.id)}>
          {(r.status === "scheduled" || r.status === "fired") && (
            <div className={styles.actions}>
              <Button compact onClick={() => snoozeBy(r, 10)} disabled={snooze.isPending}>
                Snooze 10 min
              </Button>
              <Button compact variant="primary" onClick={() => done(r)} disabled={markDone.isPending}>
                Done
              </Button>
            </div>
          )}
        </ReminderRow>
      ))}
    </div>
  );
}

// --- events --------------------------------------------------------------------------------

function EventsCard({ data }: { data: Extract<Card, { kind: "events" }>["data"] }) {
  const now = useNow();
  const today = dayKeyOfMs(now);
  const groups = useMemo(() => groupEventsByDay(data.items), [data.items]);
  if (groups.length === 0) return <Empty>Nothing scheduled.</Empty>;
  return (
    <div className={styles.list}>
      {groups.map((g) => (
        <section key={g.key} className={styles.day}>
          <h3 className={styles.dayHeading}>
            {dayHeading(g.key, today)}
            {dayHeading(g.key, today) !== formatDay(g.key) && <span>{formatDay(g.key)}</span>}
          </h3>
          {g.events.map((e) => (
            <EventRow key={`${e.uid}:${e.instance ?? ""}`} event={e} compact highlight={data.highlight.includes(e.uid)} />
          ))}
        </section>
      ))}
    </div>
  );
}

// --- memory --------------------------------------------------------------------------------

function MemoryCard({ items }: { items: Memory[] }) {
  const forget = useDeleteMemory();
  const [gone, setGone] = useState<string[]>([]);
  const now = useNow();
  const shown = items.filter((m) => !gone.includes(m.id));

  function remove(m: Memory) {
    setGone((g) => [...g, m.id]);
    forget.mutate(m.id, { onError: () => setGone((g) => g.filter((id) => id !== m.id)) });
  }

  if (shown.length === 0) return <Empty>{items.length ? "Forgotten." : "Nothing saved."}</Empty>;
  return (
    <div className={styles.list}>
      {shown.map((m) => (
        <MemoryRow key={m.id} memory={m} now={now} compact>
          <div className={styles.actions}>
            <Button compact variant="danger" onClick={() => remove(m)}>
              Forget
            </Button>
          </div>
        </MemoryRow>
      ))}
    </div>
  );
}

// --- weather -------------------------------------------------------------------------------

function WeatherCard({ data }: { data: Extract<Card, { kind: "weather" }>["data"] }) {
  const { now, today, hours, days, unit } = data;
  const nowMs = useNow();
  const night = !isDaytime(new Date(nowMs).toISOString(), today.sunrise, today.sunset);
  const group = weatherGroup(now.code);
  const hourly = hours.slice(0, 12);
  return (
    <div className={styles.weather}>
      <div className={styles.weatherNow}>
        <WeatherIcon group={group} night={night} size={64} />
        <div>
          <div className={styles.temp}>
            {degrees(now.temp)}
            <span>{unit}</span>
          </div>
          <div className={styles.condition}>{now.condition || GROUP_LABEL[group]}</div>
          {now.feels_like !== null && <div className={styles.muted}>Feels like {degrees(now.feels_like)}</div>}
        </div>
        <dl className={styles.today}>
          <div>
            <dt>High</dt>
            <dd>{degrees(today.high)}</dd>
          </div>
          <div>
            <dt>Low</dt>
            <dd>{degrees(today.low)}</dd>
          </div>
          {today.precip_chance !== null && (
            <div>
              <dt>Rain</dt>
              <dd>{precipLabel(today.precip_chance)}</dd>
            </div>
          )}
        </dl>
      </div>
      {data.place && <p className={styles.place}>{data.place}</p>}
      {hourly.length > 0 && (
        <ol className={styles.hours} aria-label="Next 12 hours">
          {hourly.map((h) => (
            <li key={h.time}>
              <span className={styles.muted}>{formatTime(h.time).replace(/:00(?=\s)/, "").replace(/\s/g, "").toLowerCase()}</span>
              <WeatherIcon group={weatherGroup(h.code)} night={!isDaytime(h.time, today.sunrise, today.sunset)} size={28} />
              <strong>{degrees(h.temp)}</strong>
              {precipLabel(h.precip_chance, 20) && <span className={styles.rain}>{precipLabel(h.precip_chance, 20)}</span>}
            </li>
          ))}
        </ol>
      )}
      {days.length > 0 && (
        <ul className={styles.days} aria-label="Next days">
          {days.slice(0, 5).map((d) => (
            <li key={d.date}>
              <span className={styles.dayName}>{d.date === dayKeyOfMs(nowMs) ? "Today" : formatWeekday(d.date)}</span>
              <WeatherIcon group={weatherGroup(d.code)} size={26} />
              {precipLabel(d.precip_chance, 20) ? <span className={styles.rain}>{precipLabel(d.precip_chance, 20)}</span> : <span />}
              <span className={styles.range}>
                <strong>{degrees(d.high)}</strong> <span className={styles.muted}>{degrees(d.low)}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// --- web -----------------------------------------------------------------------------------

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

function WebResultsCard({ data }: { data: Extract<Card, { kind: "web_results" }>["data"] }) {
  if (data.items.length === 0) return <Empty>No results{data.query ? ` for “${data.query}”` : ""}.</Empty>;
  return (
    <div className={styles.list}>
      {data.query && <p className={styles.query}>“{data.query}”</p>}
      {data.items.map((r) => (
        <a key={r.url} className={styles.result} href={r.url} target="_blank" rel="noopener noreferrer">
          <span className={styles.resultTitle}>{r.title}</span>
          <span className={styles.resultSource}>{r.source || hostOf(r.url)}</span>
          {r.snippet && <span className={styles.snippet}>{r.snippet}</span>}
        </a>
      ))}
    </div>
  );
}

function WebPageCard({ data }: { data: Extract<Card, { kind: "web_page" }>["data"] }) {
  return (
    <a className={styles.result} href={data.url} target="_blank" rel="noopener noreferrer">
      <span className={styles.resultTitle}>{data.title}</span>
      <span className={styles.resultSource}>{data.source || hostOf(data.url)}</span>
      {data.excerpt && <span className={styles.excerpt}>{data.excerpt}</span>}
    </a>
  );
}

// --- mail ----------------------------------------------------------------------------------

function MailCard({ data, onOpenSection }: { data: Extract<Card, { kind: "mail" }>["data"]; onOpenSection: (s: Section) => void }) {
  const now = useNow();
  if (data.items.length === 0) return <Empty>No messages.</Empty>;
  const accounts = new Set(data.items.map((m: Message) => m.account));
  return (
    <div className={styles.list}>
      {data.items.map((m) => (
        <MessageRow
          key={m.id}
          message={m}
          now={now}
          compact
          showAccount={accounts.size > 1}
          highlight={data.highlight.includes(m.id)}
          onOpen={() => {
            setMailTarget(m.id);
            onOpenSection("mail");
          }}
        />
      ))}
    </div>
  );
}

function MailMessageCard({ data, onOpenSection }: { data: Extract<Card, { kind: "mail_message" }>["data"]; onOpenSection: (s: Section) => void }) {
  const { message, excerpt } = data;
  return (
    <button
      type="button"
      className={styles.mailMessage}
      onClick={() => {
        setMailTarget(message.id);
        onOpenSection("mail");
      }}
    >
      <span className={styles.mailFrom}>{senderName(message.from)}</span>
      <span className={styles.mailSubject}>{message.subject || "(no subject)"}</span>
      {message.gist && <span className={styles.mailGist}>{message.gist}</span>}
      {excerpt && <span className={styles.mailExcerpt}>{excerpt}</span>}
    </button>
  );
}

// --- approvals -----------------------------------------------------------------------------

function ApprovalCard({ approval }: { approval: Extract<Card, { kind: "approval" }>["data"] }) {
  return <ApprovalPanel approval={useLiveApproval(approval)} />;
}

// --- money ---------------------------------------------------------------------------------

function AccountsCard({ items }: { items: MoneyAccount[] }) {
  if (items.length === 0) return <Empty>No accounts.</Empty>;
  const banks = new Set(items.map((a) => a.institution));
  return (
    <ul className={money.accounts}>
      {items.map((a) => (
        <li key={a.id} className={money.account} style={{ borderTop: 0 }}>
          <div className={money.accountName}>
            <span>
              <AccountName account={a} />
            </span>
            <small>{[banks.size > 1 ? a.institution : "", a.subtype || a.type].filter(Boolean).join(" · ")}</small>
          </div>
          <div className={money.balance}>
            {a.balance.current !== null ? (
              <>
                <strong>{formatMoney(a.balance.current, a.currency)}</strong>
                {isCredit(a) && <small>owed</small>}
                {!isCredit(a) && a.balance.available !== null && a.balance.available !== a.balance.current && <small>{formatMoney(a.balance.available, a.currency)} available</small>}
              </>
            ) : a.balance.available !== null ? (
              // Some banks only say what's available.
              <>
                <strong>{formatMoney(a.balance.available, a.currency)}</strong>
                <small>available</small>
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

function TransactionsCard({ data }: { data: Extract<Card, { kind: "transactions" }>["data"] }) {
  if (data.items.length === 0) return <Empty>No transactions.</Empty>;
  return (
    <div className={styles.list}>
      {data.items.map((t) => (
        <TransactionRow key={t.id} txn={t} compact highlight={data.highlight.includes(t.id)} />
      ))}
    </div>
  );
}
