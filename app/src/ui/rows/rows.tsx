import type { ReactNode } from "react";
import type { CalendarEvent, Memory, Reminder, Task } from "../../data/model";
import { describeRrule } from "../../data/rrule";
import { dayHeading, dayKeyOfMs, dueLabel, eventTimeLabel, formatDateTime, formatDay, relativeTime } from "../../data/time";
import { ChevronIcon, PinIcon, RepeatIcon } from "../icons";
import { CheckCircle } from "../kit/kit";
import { kindLabel } from "./labels";
import styles from "./rows.module.css";

// Presentational rows for tasks, reminders, events and memories. The menu sections and the
// cards beside her share them; callers add the actions.

const PRIORITY = ["", "Low priority", "Medium priority", "High priority"];

interface TaskRowProps {
  task: Task;
  now: number;
  showList?: boolean;
  highlight?: boolean;
  compact?: boolean;
  // Checking the box. Omitted for a read-only row.
  onToggle?: (done: boolean) => void;
  // Opens the editor. When set, the text area becomes a button.
  onOpen?: () => void;
  expanded?: boolean;
  children?: ReactNode;
}

export function TaskRow({ task, now, showList, highlight, compact, onToggle, onOpen, expanded, children }: TaskRowProps) {
  const done = task.completed_at !== null;
  const due = task.due ? dueLabel(task.due, now) : undefined;
  const body = (
    <>
      <span className={styles.title} data-done={done || undefined}>
        {task.title}
      </span>
      <span className={styles.meta}>
        {due && !done && (
          <span className={styles.badge} data-tone={due.overdue ? "danger" : due.today ? "accent" : undefined}>
            {due.overdue ? `Overdue · ${due.text}` : due.text}
          </span>
        )}
        {done && task.completed_at && <span className={styles.badge}>Done {formatDay(dayKeyOfMs(Date.parse(task.completed_at)))}</span>}
        {task.rrule && (
          <span className={styles.badge} title={describeRrule(task.rrule)}>
            <RepeatIcon aria-hidden="true" />
            {describeRrule(task.rrule)}
          </span>
        )}
        {task.priority > 0 && (
          <span className={styles.badge} data-tone={task.priority === 3 ? "danger" : task.priority === 2 ? "accent" : undefined}>
            {PRIORITY[task.priority]}
          </span>
        )}
        {showList && <span className={styles.badge}>{task.list}</span>}
      </span>
      {!compact && task.notes && <span className={styles.notes}>{task.notes}</span>}
    </>
  );
  return (
    <div className={styles.row} data-highlight={highlight || undefined} data-compact={compact || undefined} data-expanded={expanded || undefined}>
      <div className={styles.line}>
        {onToggle && <CheckCircle checked={done} label={done ? `Reopen ${task.title}` : `Complete ${task.title}`} onChange={onToggle} />}
        {onOpen ? (
          <button type="button" className={styles.body} aria-expanded={expanded} onClick={onOpen}>
            {body}
            <ChevronIcon className={styles.chevron} />
          </button>
        ) : (
          <div className={styles.body}>{body}</div>
        )}
      </div>
      {children}
    </div>
  );
}

interface ReminderRowProps {
  reminder: Reminder;
  now: number;
  highlight?: boolean;
  compact?: boolean;
  onOpen?: () => void;
  expanded?: boolean;
  children?: ReactNode;
}

const STATUS_LABEL: Record<Reminder["status"], string> = {
  scheduled: "",
  fired: "Fired",
  done: "Done",
  cancelled: "Cancelled",
};

export function ReminderRow({ reminder, now, highlight, compact, onOpen, expanded, children }: ReminderRowProps) {
  const upcoming = reminder.status === "scheduled";
  const body = (
    <>
      <span className={styles.title} data-done={reminder.status === "done" || reminder.status === "cancelled" || undefined}>
        {reminder.text}
      </span>
      <span className={styles.meta}>
        <span className={styles.badge} data-tone={upcoming ? "accent" : undefined}>
          {upcoming ? relativeTime(reminder.at, now) : STATUS_LABEL[reminder.status]}
        </span>
        <span className={styles.when}>{formatDateTime(reminder.at)}</span>
        {reminder.rrule && (
          <span className={styles.badge}>
            <RepeatIcon aria-hidden="true" />
            {describeRrule(reminder.rrule)}
          </span>
        )}
      </span>
    </>
  );
  return (
    <div className={styles.row} data-highlight={highlight || undefined} data-compact={compact || undefined} data-expanded={expanded || undefined}>
      <div className={styles.line}>
        {onOpen ? (
          <button type="button" className={styles.body} aria-expanded={expanded} onClick={onOpen}>
            {body}
            <ChevronIcon className={styles.chevron} />
          </button>
        ) : (
          <div className={styles.body}>{body}</div>
        )}
      </div>
      {children}
    </div>
  );
}

interface EventRowProps {
  event: CalendarEvent;
  highlight?: boolean;
  compact?: boolean;
  onOpen?: () => void;
  expanded?: boolean;
  children?: ReactNode;
}

export function EventRow({ event, highlight, compact, onOpen, expanded, children }: EventRowProps) {
  const allDay = event.all_day || /^\d{4}-\d{2}-\d{2}$/.test(event.start);
  const time = eventTimeLabel(event);
  const [first, ...rest] = allDay ? [time] : time.split(" – ");
  const body = (
    <>
      <span className={styles.title}>{event.title}</span>
      <span className={styles.meta}>
        {event.location && (
          <span className={styles.badge}>
            <PinIcon aria-hidden="true" />
            {event.location}
          </span>
        )}
        {event.rrule && (
          <span className={styles.badge}>
            <RepeatIcon aria-hidden="true" />
            {describeRrule(event.rrule)}
          </span>
        )}
      </span>
    </>
  );
  return (
    <div className={styles.row} data-highlight={highlight || undefined} data-compact={compact || undefined} data-expanded={expanded || undefined}>
      <div className={styles.line}>
        <div className={styles.time} data-allday={allDay || undefined}>
          <span>{first}</span>
          {rest[0] && <span className={styles.timeEnd}>{rest[0]}</span>}
        </div>
        {onOpen ? (
          <button type="button" className={styles.body} aria-expanded={expanded} onClick={onOpen}>
            {body}
            <ChevronIcon className={styles.chevron} />
          </button>
        ) : (
          <div className={styles.body}>{body}</div>
        )}
      </div>
      {children}
    </div>
  );
}

interface MemoryRowProps {
  memory: Memory;
  highlight?: boolean;
  compact?: boolean;
  onOpen?: () => void;
  expanded?: boolean;
  now: number;
  children?: ReactNode;
}

export function MemoryRow({ memory, highlight, compact, onOpen, expanded, now, children }: MemoryRowProps) {
  const body = (
    <>
      <span className={styles.memoryText}>{memory.content}</span>
      <span className={styles.meta}>
        <span className={styles.badge} data-tone="accent">
          {kindLabel(memory.kind)}
        </span>
        <span className={styles.stars} role="img" aria-label={`Importance ${memory.importance} of 5`}>
          {Array.from({ length: 5 }, (_, i) => (
            <i key={i} data-on={i < memory.importance || undefined} />
          ))}
        </span>
        {memory.follow_up_at && (
          <span className={styles.badge}>
            Follow up {dayHeading(dayKeyOfMs(Date.parse(memory.follow_up_at)), dayKeyOfMs(now))}
          </span>
        )}
      </span>
    </>
  );
  return (
    <div className={styles.row} data-highlight={highlight || undefined} data-compact={compact || undefined} data-expanded={expanded || undefined}>
      <div className={styles.line}>
        {onOpen ? (
          <button type="button" className={styles.body} aria-expanded={expanded} onClick={onOpen}>
            {body}
            <ChevronIcon className={styles.chevron} />
          </button>
        ) : (
          <div className={styles.body}>{body}</div>
        )}
      </div>
      {children}
    </div>
  );
}
