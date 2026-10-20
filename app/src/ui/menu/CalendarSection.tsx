import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useCalendarStatus, useCreateEvent, useDeleteEvent, useEvents, useNow, usePatchEvent } from "../../data/hooks";
import type { CalendarEvent } from "../../data/model";
import type { EventFields } from "../../data/rest";
import {
  addDays,
  addMonths,
  dayHeading,
  dayKeyOfMs,
  daysBetween,
  daysInMonth,
  formatDay,
  formatMonth,
  formatWeekday,
  fromDateTimeInputs,
  groupEventsByDay,
  isDateOnly,
  monthStart,
  toDateInput,
  toMs,
  toRfc3339,
  toTimeInput,
  weekdayOf,
  timestampFromInputs,
  localTimeExists,
  SKIPPED_TIME_HINT,
} from "../../data/time";
import { ChevronIcon, PlusIcon, TrashIcon } from "../icons";
import { Button, ConfirmButton, IconButton, SectionTitle, StateMessage, Switch, TextArea, TextField } from "../kit/kit";
import { useAutoFocus } from "../kit/useAutoFocus";
import { EventRow } from "../rows/rows";
import { useMenuActive } from "./MenuContext";
import { RepeatSelect } from "./RepeatSelect";
import styles from "./sections.module.css";

const AGENDA_DAYS = 14;
const HOUR = 3_600_000;

interface EventDraft {
  title: string;
  allDay: boolean;
  startDate: string;
  startTime: string;
  endDate: string;
  endTime: string;
  location: string;
  notes: string;
  rrule: string | null;
}

function draftFor(e: CalendarEvent | undefined, startMs: number): EventDraft {
  if (e) {
    const allDay = e.all_day || isDateOnly(e.start);
    return {
      title: e.title,
      allDay,
      startDate: toDateInput(e.start),
      startTime: allDay ? "09:00" : toTimeInput(e.start),
      endDate: allDay ? toDateInput(e.start) : toDateInput(e.end),
      endTime: allDay ? "10:00" : toTimeInput(e.end),
      location: e.location,
      notes: e.notes,
      rrule: e.rrule,
    };
  }
  const start = toRfc3339(startMs);
  const end = toRfc3339(startMs + HOUR);
  return { title: "", allDay: false, startDate: toDateInput(start), startTime: toTimeInput(start), endDate: toDateInput(end), endTime: toTimeInput(end), location: "", notes: "", rrule: null };
}

function EventForm({ event, startMs, busy, submitLabel, onSubmit, onCancel, children }: {
  event?: CalendarEvent;
  startMs: number;
  busy: boolean;
  submitLabel: string;
  onSubmit: (fields: EventFields & { title: string; start: string }) => void;
  onCancel: () => void;
  children?: ReactNode;
}) {
  // Compare against the record as it was when editing began: refetches replace the prop, and
  // diffing against the new copy would undo changes made meanwhile on another device.
  const [base] = useState(event);
  const [d, setD] = useState(() => draftFor(event, startMs));
  const first = useAutoFocus<HTMLInputElement>();
  const set = (patch: Partial<EventDraft>) => setD((prev) => ({ ...prev, ...patch }));

  // Untouched time inputs keep the exact original timestamps (a rebuilt one would look like a
  // reschedule, which clears a recurring series' exceptions).
  const timed = base !== undefined && !(base.all_day || isDateOnly(base.start));
  const start = d.allDay ? d.startDate : timestampFromInputs(d.startDate, d.startTime, timed ? base.start : undefined);
  const end = d.allDay ? "" : timestampFromInputs(d.endDate, d.endTime, timed ? base.end : undefined);
  const endsBefore = !d.allDay && start !== "" && end !== "" && toMs(end) < toMs(start);
  const valid = d.title.trim() !== "" && start !== "" && (d.allDay || end !== "") && !endsBefore;

  // Moving the start moves the end with it, keeping the length of the event.
  function moveStart(startDate: string, startTime: string) {
    const oldStart = fromDateTimeInputs(d.startDate, d.startTime);
    const oldEnd = fromDateTimeInputs(d.endDate, d.endTime);
    const next = fromDateTimeInputs(startDate, startTime);
    if (!oldStart || !oldEnd || !next) return set({ startDate, startTime });
    const length = toMs(oldEnd) - toMs(oldStart);
    const newEnd = toRfc3339(toMs(next) + Math.max(0, length));
    set({ startDate, startTime, endDate: toDateInput(newEnd), endTime: toTimeInput(newEnd) });
  }

  // All-day ends are exclusive (RFC 5545): a one-day event on the 20th ends on the 21st. Moving a
  // multi-day event keeps its length.
  const wasAllDayEvent = !!base && (base.all_day || isDateOnly(base.start));
  const spanDays = base && wasAllDayEvent ? Math.max(1, daysBetween(toDateInput(base.start), toDateInput(base.end))) : 1;

  function submit() {
    const fields: EventFields & { title: string; start: string } = { title: d.title.trim(), start };
    if (!base) {
      fields.all_day = d.allDay;
      fields.end = d.allDay ? addDays(d.startDate, 1) : end;
      if (d.location.trim()) fields.location = d.location.trim();
      if (d.notes.trim()) fields.notes = d.notes.trim();
      if (d.rrule) fields.rrule = d.rrule;
      return onSubmit(fields);
    }
    // Editing sends only what changed, so a recurring series keeps its original start.
    const changed: EventFields = {};
    if (fields.title !== base.title) changed.title = fields.title;
    const wasAllDay = base.all_day || isDateOnly(base.start);
    if (d.allDay !== wasAllDay) changed.all_day = d.allDay;
    const startChanged = d.allDay !== wasAllDay || (d.allDay ? start !== toDateInput(base.start) : toMs(start) !== toMs(base.start));
    if (startChanged) changed.start = start;
    if (d.allDay) {
      if (startChanged) changed.end = addDays(d.startDate, wasAllDay ? spanDays : 1);
    } else if (startChanged || toMs(end) !== toMs(base.end)) changed.end = end;
    if (d.location.trim() !== base.location) changed.location = d.location.trim();
    if (d.notes.trim() !== base.notes) changed.notes = d.notes.trim();
    if (d.rrule !== base.rrule) changed.rrule = d.rrule;
    if (Object.keys(changed).length === 0) return onCancel();
    // Times edited on one occurrence of a series apply to the series relative to that occurrence.
    if (base.instance && (changed.start || changed.end)) changed.instance = base.instance;
    // The version this edit was made against: if someone saved in between, the service refuses it.
    if (base.version) changed.version = base.version;
    onSubmit(changed as EventFields & { title: string; start: string });
  }

  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) submit();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <TextField ref={first} label="Title" value={d.title} onChange={(e) => set({ title: e.target.value })} maxLength={300} required />
      <Switch label="All day" checked={d.allDay} onChange={(allDay) => set({ allDay })} />
      {d.allDay ? (
        <TextField label="Date" type="date" value={d.startDate} onChange={(e) => set({ startDate: e.target.value })} required />
      ) : (
        <>
          <div className={styles.pair}>
            <TextField label="Starts" type="date" value={d.startDate} onChange={(e) => moveStart(e.target.value, d.startTime)} required />
            <TextField label="Start time" type="time" value={d.startTime} onChange={(e) => moveStart(d.startDate, e.target.value)} required hint={localTimeExists(d.startDate, d.startTime) ? undefined : SKIPPED_TIME_HINT} />
          </div>
          <div className={styles.pair}>
            <TextField label="Ends" type="date" value={d.endDate} onChange={(e) => set({ endDate: e.target.value })} required />
            <TextField label="End time" type="time" value={d.endTime} onChange={(e) => set({ endTime: e.target.value })} required hint={localTimeExists(d.endDate, d.endTime) ? undefined : SKIPPED_TIME_HINT} />
          </div>
          {endsBefore && <p className={styles.callout} data-tone="danger">The event ends before it starts.</p>}
        </>
      )}
      <TextField label="Location" value={d.location} onChange={(e) => set({ location: e.target.value })} maxLength={300} />
      <TextArea label="Notes" value={d.notes} onChange={(e) => set({ notes: e.target.value })} maxLength={4000} />
      <RepeatSelect value={d.rrule} onChange={(rrule) => set({ rrule })} />
      {event?.rrule && <p className={styles.callout}>This repeats, so changes apply to every occurrence.</p>}
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

function eventKey(e: CalendarEvent): string {
  return `${e.uid}:${e.instance ?? ""}`;
}

export function CalendarSection() {
  const active = useMenuActive();
  const now = useNow();
  const today = dayKeyOfMs(now);
  const [anchor, setAnchor] = useState(today);
  const [month, setMonth] = useState(monthStart(today));
  const [adding, setAdding] = useState(false);
  const [openKey, setOpenKey] = useState<string>();
  const status = useCalendarStatus(active);
  const configured = status.data?.configured === true;

  // One query covers the month strip and the agenda window.
  const rangeFrom = month < anchor ? month : anchor;
  const agendaEnd = addDays(anchor, AGENDA_DAYS - 1);
  const monthEnd = addDays(month, daysInMonth(month) - 1);
  const rangeLast = monthEnd > agendaEnd ? monthEnd : agendaEnd;
  const events = useEvents(toRfc3339(toMs(rangeFrom)), toRfc3339(toMs(addDays(rangeLast, 1))), active && configured);
  const create = useCreateEvent();
  const patch = usePatchEvent();
  const remove = useDeleteEvent();

  const groups = useMemo(() => groupEventsByDay(events.data ?? [], {}, { from: rangeFrom, to: rangeLast }), [events.data, rangeFrom, rangeLast]);
  const busyDays = useMemo(() => new Set(groups.map((g) => g.key)), [groups]);
  const agenda = groups.filter((g) => g.key >= anchor && g.key <= agendaEnd);

  const selectedRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    selectedRef.current?.scrollIntoView({ inline: "center", block: "nearest" });
  }, [anchor, month, configured]);

  if (status.isPending) return <StateMessage kind="loading" />;
  if (status.isError && !status.data) {
    return (
      <StateMessage kind="error" title="Couldn't check the calendar" onRetry={() => void status.refetch()}>
        {status.error.message}
      </StateMessage>
    );
  }
  if (!configured) {
    return (
      <StateMessage kind="empty" title="No calendar connected">
        Hekate doesn&rsquo;t have a calendar to work with yet. Once a calendar server is set up on the homelab, your agenda shows up here and she can add events for you.
      </StateMessage>
    );
  }

  const monthDays = Array.from({ length: daysInMonth(month) }, (_, i) => addDays(month, i));
  // A new event starts at the next full hour today, or at 9:00 on the day picked in the strip.
  const startMs = anchor === today ? Math.ceil(now / HOUR) * HOUR : toMs(anchor) + 9 * HOUR;

  return (
    <div className={styles.stack}>
      <div>
        <div className={styles.monthBar}>
          <IconButton label="Previous month" onClick={() => setMonth(addMonths(month, -1))}>
            <ChevronIcon style={{ rotate: "180deg" }} />
          </IconButton>
          <h3 className={styles.monthName}>{formatMonth(month)}</h3>
          <IconButton label="Next month" onClick={() => setMonth(addMonths(month, 1))}>
            <ChevronIcon />
          </IconButton>
        </div>
        <div className={styles.strip} role="group" aria-label="Jump to a day">
          {monthDays.map((k) => {
            const weekday = weekdayOf(k);
            const selected = k === anchor;
            return (
              <button
                key={k}
                ref={selected ? selectedRef : undefined}
                type="button"
                className={styles.dayCell}
                data-selected={selected || undefined}
                data-today={k === today || undefined}
                data-has={busyDays.has(k) || undefined}
                data-weekend={weekday === 0 || weekday === 6 || undefined}
                aria-pressed={selected}
                aria-label={`${formatDay(k)}${busyDays.has(k) ? ", has events" : ""}`}
                onClick={() => setAnchor(k)}
              >
                <small>{formatWeekday(k).slice(0, 3)}</small>
                <b>{Number(k.slice(8))}</b>
                <i />
              </button>
            );
          })}
        </div>
        <div className={styles.toolbar}>
          {anchor !== today && (
            <Button
              variant="quiet"
              compact
              onClick={() => {
                setAnchor(today);
                setMonth(monthStart(today));
              }}
            >
              Back to today
            </Button>
          )}
          <span style={{ flex: 1 }} />
          {!adding && (
            <Button
              variant="primary"
              onClick={() => {
                setAdding(true);
                setOpenKey(undefined);
              }}
            >
              <PlusIcon />
              Add event
            </Button>
          )}
        </div>
      </div>

      {adding && (
        <div className={styles.formCard}>
          <EventForm
            startMs={startMs}
            busy={create.isPending}
            submitLabel="Add event"
            onCancel={() => setAdding(false)}
            onSubmit={(f) => create.mutate(f, { onSuccess: () => setAdding(false) })}
          />
        </div>
      )}

      <section>
        <SectionTitle>{anchor === today ? "Next 14 days" : `From ${formatDay(anchor)}`}</SectionTitle>
        {events.isError && !events.data ? (
          <StateMessage kind="error" title="Couldn't load events" onRetry={() => void events.refetch()}>
            {events.error.message}
          </StateMessage>
        ) : !events.data ? (
          <StateMessage kind="loading" />
        ) : agenda.length === 0 ? (
          <StateMessage kind="empty" title="Nothing planned">
            {anchor === today ? "The next two weeks are clear." : "These two weeks are clear."}
          </StateMessage>
        ) : (
          <div className={styles.stack} style={{ gap: "var(--space-4)" }}>
            {agenda.map((g) => (
              <div key={g.key} className={styles.dayBlock}>
                <h4 className={styles.dayTitle}>
                  {dayHeading(g.key, today)}
                  {dayHeading(g.key, today) !== formatDay(g.key) && <span>{formatDay(g.key)}</span>}
                </h4>
                <div className={styles.group}>
                  {g.events.map((e) => {
                    const key = `${g.key}|${eventKey(e)}`;
                    const open = openKey === key;
                    return (
                      <EventRow key={key} event={e} expanded={open} onOpen={() => setOpenKey(open ? undefined : key)}>
                        {open && (
                          <div className={styles.editor}>
                            {e.created_by_hekate ? (
                              <EventForm
                                event={e}
                                startMs={toMs(e.start)}
                                busy={patch.isPending}
                                submitLabel="Save"
                                onCancel={() => setOpenKey(undefined)}
                                onSubmit={(fields) => patch.mutate({ uid: e.uid, fields }, { onSuccess: () => setOpenKey(undefined) })}
                              >
                                <ConfirmButton
                                  label={e.rrule ? "Delete series" : "Delete"}
                                  icon={<TrashIcon />}
                                  busy={remove.isPending}
                                  onConfirm={() => remove.mutate(e.uid, { onSuccess: () => setOpenKey(undefined) })}
                                />
                              </EventForm>
                            ) : (
                              <div className={styles.form}>
                                {e.notes && <p className={styles.callout}>{e.notes}</p>}
                                <p className={styles.callout}>
                                  <strong>View only.</strong> This event wasn&rsquo;t created by Hekate, so it can&rsquo;t be changed here. Edit it in the calendar app where you made it.
                                </p>
                              </div>
                            )}
                          </div>
                        )}
                      </EventRow>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
      {status.data?.calendar_name && <p className={styles.callout}>Synced with {status.data.calendar_name}.</p>}
    </div>
  );
}
