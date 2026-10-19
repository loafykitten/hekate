import { useMemo } from "react";
import { useCalendarStatus, useEvents, useNow, useSetTaskDone, useTasks, useReminders, useWeather } from "../../data/hooks";
import { addDays, dayKey, dayKeyOfMs, formatDayLong, greeting, groupEventsByDay, toRfc3339, toMs } from "../../data/time";
import { degrees, GROUP_LABEL, isDaytime, precipLabel, weatherGroup } from "../../data/weather";
import { WeatherIcon } from "../cards/WeatherIcon";
import { Button, SectionTitle, StateMessage } from "../kit/kit";
import { EventRow, ReminderRow, TaskRow } from "../rows/rows";
import { useMenuActive, useMenuNavigate } from "./MenuContext";
import { ReminderActions } from "./RemindersSection";
import styles from "./sections.module.css";

// The day at a glance: weather, today's events, what's due or late, and the next reminders.
export function TodaySection() {
  const active = useMenuActive();
  const navigate = useMenuNavigate();
  const now = useNow();
  const today = dayKeyOfMs(now);

  const weather = useWeather(active);
  const calendar = useCalendarStatus(active);
  const calendarOn = calendar.data?.configured === true;
  const from = toRfc3339(toMs(today));
  const to = toRfc3339(toMs(addDays(today, 1)));
  const events = useEvents(from, to, active && calendarOn);
  const tasks = useTasks(undefined, "open", active);
  const reminders = useReminders("upcoming", active);
  const setDone = useSetTaskDone();

  const todayEvents = useMemo(
    () => groupEventsByDay(events.data ?? [], {}, { from: today, to: today })[0]?.events ?? [],
    [events.data, today],
  );
  const dueTasks = useMemo(
    () =>
      (tasks.data ?? [])
        .filter((t) => t.due && dayKey(t.due) <= today)
        .sort((a, b) => (a.due ?? "").localeCompare(b.due ?? "") || b.priority - a.priority),
    [tasks.data, today],
  );
  const nextReminders = (reminders.data ?? []).slice(0, 4);

  const quiet =
    !!events.data && !!tasks.data && !!reminders.data && todayEvents.length === 0 && dueTasks.length === 0 && nextReminders.length === 0;

  return (
    <div className={styles.stack}>
      <div>
        <h3 className={styles.hello}>{greeting(now)}</h3>
        <p className={styles.date}>{formatDayLong(today)}</p>
      </div>

      <WeatherSummary query={weather} now={now} />

      {quiet && (
        <p className={styles.callout}>
          <strong>A quiet day.</strong> Nothing on the calendar, nothing due, no reminders coming up.
        </p>
      )}

      {calendar.data && !calendarOn ? null : (
        <section>
          <SectionTitle
            action={
              <Button variant="quiet" compact onClick={() => navigate("calendar")}>
                Calendar
              </Button>
            }
          >
            Today&rsquo;s events
          </SectionTitle>
          {events.isError ? (
            <StateMessage kind="error" title="Couldn't load events" onRetry={() => void events.refetch()} />
          ) : !events.data ? (
            <StateMessage kind="loading" />
          ) : todayEvents.length === 0 ? (
            <p className={styles.callout}>Nothing on the calendar today.</p>
          ) : (
            <div className={styles.group}>
              {todayEvents.map((e) => (
                <EventRow key={`${e.uid}:${e.instance ?? ""}`} event={e} />
              ))}
            </div>
          )}
        </section>
      )}

      <section>
        <SectionTitle
          action={
            <Button variant="quiet" compact onClick={() => navigate("tasks")}>
              All tasks
            </Button>
          }
        >
          Due today &amp; overdue
        </SectionTitle>
        {tasks.isError ? (
          <StateMessage kind="error" title="Couldn't load tasks" onRetry={() => void tasks.refetch()} />
        ) : !tasks.data ? (
          <StateMessage kind="loading" />
        ) : dueTasks.length === 0 ? (
          <p className={styles.callout}>Nothing due. Nicely done.</p>
        ) : (
          <div className={styles.group}>
            {dueTasks.map((t) => (
              <TaskRow key={t.id} task={t} now={now} showList onToggle={(done) => setDone.mutate({ id: t.id, done })} />
            ))}
          </div>
        )}
      </section>

      <section>
        <SectionTitle
          action={
            <Button variant="quiet" compact onClick={() => navigate("reminders")}>
              All reminders
            </Button>
          }
        >
          Coming up
        </SectionTitle>
        {reminders.isError ? (
          <StateMessage kind="error" title="Couldn't load reminders" onRetry={() => void reminders.refetch()} />
        ) : !reminders.data ? (
          <StateMessage kind="loading" />
        ) : nextReminders.length === 0 ? (
          <p className={styles.callout}>No reminders set.</p>
        ) : (
          <div className={styles.group}>
            {nextReminders.map((r) => (
              <ReminderRow key={r.id} reminder={r} now={now}>
                <ReminderActions reminder={r} />
              </ReminderRow>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function WeatherSummary({ query, now }: { query: ReturnType<typeof useWeather>; now: number }) {
  if (query.isError) return <StateMessage kind="error" title="Couldn't load the weather" onRetry={() => void query.refetch()} />;
  if (query.isPending) return <StateMessage kind="loading" />;
  const w = query.data;
  if (!w) {
    return (
      <p className={styles.callout} data-tone="accent">
        <strong>Where do you live?</strong> Tell Renamon your city (&ldquo;I live in Lisbon&rdquo;) and today&rsquo;s weather will show up here.
      </p>
    );
  }
  const group = weatherGroup(w.now.code);
  const night = !isDaytime(new Date(now).toISOString(), w.today.sunrise, w.today.sunset);
  return (
    <div className={styles.weatherSummary}>
      <WeatherIcon group={group} night={night} size={56} />
      <span className={styles.weatherTemp}>
        {degrees(w.now.temp)}
        <small style={{ fontSize: "0.875rem", color: "var(--fg-muted)" }}>{w.unit}</small>
      </span>
      <span className={styles.weatherText}>
        <strong>{w.now.condition || GROUP_LABEL[group]}</strong>
        <span>
          High {degrees(w.today.high)} · Low {degrees(w.today.low)}
        </span>
        <span>
          {[precipLabel(w.today.precip_chance) && `${precipLabel(w.today.precip_chance)} rain`, w.place].filter(Boolean).join(" · ")}
        </span>
      </span>
    </div>
  );
}
