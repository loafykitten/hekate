import { useState, type ReactNode } from "react";
import {
  useCreateReminder,
  useDeleteReminder,
  useNow,
  usePatchReminder,
  useReminderDone,
  useReminders,
  useSnoozeReminder,
} from "../../data/hooks";
import type { Reminder } from "../../data/model";
import { canReopenAtOriginalTime, showsDone, showsSnooze } from "../../data/reminderDue";
import { minutesUntilTomorrowMorning, timestampFromInputs, toDateInput, toMs, toRfc3339, toTimeInput, localTimeExists, SKIPPED_TIME_HINT } from "../../data/time";
import { PlusIcon, SnoozeIcon, TrashIcon, CheckIcon } from "../icons";
import { Button, Chip, ConfirmButton, Segmented, StateMessage, TextField } from "../kit/kit";
import { useAutoFocus } from "../kit/useAutoFocus";
import { ReminderRow } from "../rows/rows";
import { RepeatSelect } from "./RepeatSelect";
import styles from "./sections.module.css";
import { useMenuActive } from "./MenuContext";

// Snooze for a pending reminder; Done only once it is due soon or has fired (never on a
// freshly made future one, where it reads like "accept"); Reopen on a done one. Shared with Today.
export function ReminderActions({ reminder }: { reminder: Reminder }) {
  const [choosing, setChoosing] = useState(false);
  const snooze = useSnoozeReminder();
  const done = useReminderDone();
  const patch = usePatchReminder();
  const now = useNow();
  if (reminder.status === "done") {
    // Back at its original time while that is still ahead; otherwise again in ten minutes.
    const reopen = () =>
      canReopenAtOriginalTime(reminder, now)
        ? patch.mutate({ id: reminder.id, fields: { at: reminder.at } })
        : snooze.mutate({ id: reminder.id, minutes: 10 });
    return (
      <div className={styles.inlineActions}>
        <Button compact busy={patch.isPending || snooze.isPending} onClick={reopen} aria-label={`Reopen ${reminder.text}`}>
          Reopen
        </Button>
      </div>
    );
  }
  if (!showsSnooze(reminder)) return null;
  const options = [
    { label: "10 min", minutes: () => 10 },
    { label: "1 hour", minutes: () => 60 },
    { label: "3 hours", minutes: () => 180 },
    { label: "Tomorrow 9:00", minutes: () => minutesUntilTomorrowMorning(Date.now()) },
  ];
  return (
    <div className={styles.inlineActions}>
      {choosing ? (
        <>
          {options.map((o) => (
            <Chip
              key={o.label}
              onClick={() => {
                setChoosing(false);
                snooze.mutate({ id: reminder.id, minutes: o.minutes() });
              }}
            >
              {o.label}
            </Chip>
          ))}
          <Button variant="quiet" compact onClick={() => setChoosing(false)}>
            Cancel
          </Button>
        </>
      ) : (
        <>
          <Button compact onClick={() => setChoosing(true)} aria-label={`Snooze ${reminder.text}`}>
            <SnoozeIcon />
            Snooze
          </Button>
          {showsDone(reminder, now) && (
            <Button compact variant="primary" busy={done.isPending} onClick={() => done.mutate(reminder.id)} aria-label={`Mark ${reminder.text} done`}>
              <CheckIcon />
              Done
            </Button>
          )}
        </>
      )}
    </div>
  );
}

// The next whole hour, as the default time for a new reminder.
function nextHour(nowMs: number): number {
  return (Math.floor(nowMs / 3_600_000) + 1) * 3_600_000;
}

interface FormValue {
  text: string;
  at: string;
  rrule: string | null;
}

function ReminderForm({ initial, now, submitLabel, busy, onSubmit, onCancel, children }: {
  initial?: Reminder;
  now: number;
  submitLabel: string;
  busy: boolean;
  // `base` is the reminder as it was when editing began, to diff against.
  onSubmit: (value: FormValue, base?: Reminder) => void;
  onCancel: () => void;
  children?: ReactNode;
}) {
  const [base] = useState(initial);
  const startAt = initial?.at ?? toRfc3339(nextHour(now));
  const [text, setText] = useState(initial?.text ?? "");
  const [date, setDate] = useState(toDateInput(startAt));
  const [time, setTime] = useState(toTimeInput(startAt));
  const [rrule, setRrule] = useState<string | null>(initial?.rrule ?? null);
  const first = useAutoFocus<HTMLInputElement>();
  // Untouched inputs keep the exact original time (a rebuilt one would look like an edit,
  // re-anchoring a repeating reminder or failing once it has passed).
  const at = timestampFromInputs(date, time, base?.at);
  const valid = text.trim() !== "" && at !== "";
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) onSubmit({ text: text.trim(), at, rrule }, base);
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <TextField ref={first} label="Remind me to" value={text} onChange={(e) => setText(e.target.value)} placeholder="Call the dentist" maxLength={500} required />
      <div className={styles.pair}>
        <TextField label="Date" type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
        <TextField label="Time" type="time" value={time} onChange={(e) => setTime(e.target.value)} required hint={localTimeExists(date, time) ? undefined : SKIPPED_TIME_HINT} />
      </div>
      <RepeatSelect value={rrule} onChange={setRrule} />
      <div className={styles.actions}>
        {children}
        <span className={styles.spacer} />
        <Button variant="quiet" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" type="submit" busy={busy} disabled={!valid}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

export function RemindersSection() {
  const active = useMenuActive();
  const now = useNow();
  const [view, setView] = useState<"upcoming" | "past">("upcoming");
  const [adding, setAdding] = useState(false);
  const [openId, setOpenId] = useState<string>();
  const query = useReminders(view, active);
  const create = useCreateReminder();
  const patch = usePatchReminder();
  const remove = useDeleteReminder();
  const reminders = query.data ?? [];

  return (
    <div className={styles.stack}>
      <div className={styles.toolbar}>
        <Segmented
          label="Show"
          value={view}
          onChange={(v) => {
            setView(v);
            setOpenId(undefined);
          }}
          options={[
            { value: "upcoming", label: "Upcoming" },
            { value: "past", label: "Past" },
          ]}
        />
        <span style={{ flex: 1 }} />
        {!adding && (
          <Button
            variant="primary"
            onClick={() => {
              setAdding(true);
              setOpenId(undefined);
            }}
          >
            <PlusIcon />
            Add
          </Button>
        )}
      </div>

      {adding && (
        <div className={`${styles.formCard}`}>
          <ReminderForm
            now={now}
            submitLabel="Add reminder"
            busy={create.isPending}
            onCancel={() => setAdding(false)}
            onSubmit={(v) =>
              create.mutate(
                { text: v.text, at: v.at, ...(v.rrule ? { rrule: v.rrule } : {}) },
                {
                  onSuccess: () => {
                    setAdding(false);
                    setView("upcoming");
                  },
                },
              )
            }
          />
        </div>
      )}

      {query.isError && !query.data ? (
        <StateMessage kind="error" title="Couldn't load reminders" onRetry={() => void query.refetch()}>
          {query.error.message}
        </StateMessage>
      ) : query.isPending ? (
        <StateMessage kind="loading" />
      ) : reminders.length === 0 ? (
        <StateMessage kind="empty" title={view === "upcoming" ? "No reminders coming up" : "Nothing in the past yet"}>
          {view === "upcoming" ? "Ask Renamon to remind you of something, or add one here." : "Reminders that have fired or been completed show up here."}
        </StateMessage>
      ) : (
        <div className={styles.group}>
          {reminders.map((r) => {
            const open = openId === r.id;
            const editable = r.status === "scheduled";
            return (
              <ReminderRow key={r.id} reminder={r} now={now} expanded={open} onOpen={editable || r.status === "fired" ? () => setOpenId(open ? undefined : r.id) : undefined}>
                {!open && <ReminderActions reminder={r} />}
                {open && (
                  <div className={styles.editor}>
                    <ReminderForm
                      initial={r}
                      now={now}
                      submitLabel="Save"
                      busy={patch.isPending}
                      onCancel={() => setOpenId(undefined)}
                      onSubmit={(v, base = r) => {
                        // Diff against the reminder as it was when editing began, not a refetched copy.
                        const fields: { text?: string; at?: string; rrule?: string | null } = {};
                        if (v.text !== base.text) fields.text = v.text;
                        if (toMs(v.at) !== toMs(base.at)) fields.at = v.at;
                        if (v.rrule !== base.rrule) fields.rrule = v.rrule;
                        if (Object.keys(fields).length === 0) return setOpenId(undefined);
                        patch.mutate({ id: r.id, fields }, { onSuccess: () => setOpenId(undefined) });
                      }}
                    >
                      <ConfirmButton
                        label="Delete"
                        icon={<TrashIcon />}
                        busy={remove.isPending}
                        onConfirm={() => remove.mutate(r.id, { onSuccess: () => setOpenId(undefined) })}
                      />
                    </ReminderForm>
                  </div>
                )}
              </ReminderRow>
            );
          })}
        </div>
      )}
    </div>
  );
}
