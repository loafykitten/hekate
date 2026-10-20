import { useState } from "react";
import { useDeleteMemory, useJournal, useUndoReflectionChange } from "../../data/hooks";
import { diaryDate } from "../../data/journal";
import type { JournalDay, ReflectionChange } from "../../data/model";
import { ChevronIcon } from "../icons";
import { Button, ConfirmButton, SectionTitle, StateMessage } from "../kit/kit";
import styles from "./diary.module.css";

// Her diary: one entry per day, written in the nightly reflection, with what that reflection
// changed in her memories. Each change can be undone here.

const ACTION_LABEL: Record<ReflectionChange["action"], string> = { save: "Saved", update: "Updated", retire: "Let go of" };

function ChangeRow({ change }: { change: ReflectionChange }) {
  const undo = useUndoReflectionChange();
  const forget = useDeleteMemory();
  const memory = change.after ?? change.before;
  const was = change.action === "update" && change.before && change.before.content !== change.after?.content ? change.before.content : undefined;
  const undone = change.undone_at !== null;
  return (
    <li className={styles.change} data-undone={undone || undefined}>
      <div className={styles.changeBody}>
        <span className={styles.action} data-action={change.action}>
          {ACTION_LABEL[change.action]}
        </span>
        <span className={styles.content}>{memory?.content ?? `memory ${change.memory_id}`}</span>
        {was && <span className={styles.was}>Was: {was}</span>}
        {change.reason && <span className={styles.reason}>{change.reason}</span>}
        {(undo.isError || forget.isError) && (
          <span className={styles.error} role="alert">
            {(undo.error ?? forget.error)?.message}
          </span>
        )}
      </div>
      {undone ? (
        <span className={styles.undone}>Undone</span>
      ) : (
        <span className={styles.buttons}>
          <Button compact busy={undo.isPending} disabled={undo.isPending || forget.isPending} onClick={() => undo.mutate(change.id)} aria-label={`Undo: ${ACTION_LABEL[change.action]} “${memory?.content ?? ""}”`}>
            Undo
          </Button>
          {/* Retired memories are hidden everywhere else: this is where to forget one for good. */}
          {change.action === "retire" && (
            <ConfirmButton compact label="Forget" confirmLabel="Forget for good" describe={memory?.content} busy={forget.isPending} onConfirm={() => forget.mutate(change.memory_id)} />
          )}
        </span>
      )}
    </li>
  );
}

function Day({ day }: { day: JournalDay }) {
  const [open, setOpen] = useState(false);
  const changes = day.reflections.flatMap((r) => r.changes);
  const live = changes.filter((c) => c.undone_at === null).length;
  return (
    <section>
      <SectionTitle>{diaryDate(day.day)}</SectionTitle>
      <article className={styles.entry}>
        {day.diary ? <p className={styles.text}>{day.diary.text}</p> : <p className={styles.missing}>No entry this day.</p>}
        {changes.length > 0 && (
          <div className={styles.changes}>
            <button type="button" className={styles.toggle} aria-expanded={open} onClick={() => setOpen(!open)}>
              <span>
                {changes.length === 1 ? "1 change to her memories" : `${changes.length} changes to her memories`}
                {live < changes.length && ` · ${changes.length - live} undone`}
              </span>
              <ChevronIcon className={styles.chevron} />
            </button>
            {open && (
              <ul className={styles.list}>
                {changes.map((c) => (
                  <ChangeRow key={c.id} change={c} />
                ))}
              </ul>
            )}
          </div>
        )}
      </article>
    </section>
  );
}

export function DiaryView({ active }: { active: boolean }) {
  const journal = useJournal(active);
  const days = journal.data ?? [];
  if (journal.isError && !journal.data) {
    return (
      <StateMessage kind="error" title="Couldn't load her diary" onRetry={() => void journal.refetch()}>
        {journal.error.message}
      </StateMessage>
    );
  }
  if (journal.isPending) return <StateMessage kind="loading" />;
  if (days.length === 0) {
    return (
      <StateMessage kind="empty" title="No diary yet">
        Each night Renamon reflects on the day: she tidies her memories and writes here. The first entry comes after a day of talking.
      </StateMessage>
    );
  }
  return (
    <>
      {days.map((d) => (
        <Day key={d.day} day={d} />
      ))}
    </>
  );
}
