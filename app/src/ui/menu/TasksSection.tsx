import { useId, useState } from "react";
import { useCreateTask, useDeleteTask, useNow, usePatchTask, useSetTaskDone, useTaskLists, useTasks } from "../../data/hooks";
import type { Task } from "../../data/model";
import type { TaskFields } from "../../data/rest";
import { fromDateTimeInputs, isDateOnly, toDateInput, toMs, toTimeInput } from "../../data/time";
import { PlusIcon, TrashIcon } from "../icons";
import { Button, Chip, ConfirmButton, StateMessage, SectionTitle, SelectField, Switch, TextArea, TextField } from "../kit/kit";
import { useAutoFocus } from "../kit/useAutoFocus";
import { TaskRow } from "../rows/rows";
import { useMenuActive } from "./MenuContext";
import { RepeatSelect } from "./RepeatSelect";
import styles from "./sections.module.css";

const PRIORITIES = [
  { value: 0, label: "None" },
  { value: 1, label: "Low" },
  { value: 2, label: "Medium" },
  { value: 3, label: "High" },
];

// What a due value looks like in the date and time inputs, and back.
function dueFromInputs(date: string, time: string): string | null {
  if (!date) return null;
  return time ? fromDateTimeInputs(date, time) || null : date;
}

function sameDue(a: string | null, b: string | null): boolean {
  if (a === b) return true;
  if (!a || !b || isDateOnly(a) || isDateOnly(b)) return false;
  return toMs(a) === toMs(b);
}

function TaskEditor({ task, lists, busy, onSave, onCancel, onDelete, deleting }: {
  task: Task;
  lists: string[];
  busy: boolean;
  deleting: boolean;
  onSave: (fields: TaskFields) => void;
  onCancel: () => void;
  onDelete: () => void;
}) {
  const listId = useId();
  // Compare against the record as it was when editing began: refetches replace the prop, and
  // diffing against the new copy would undo changes made meanwhile on another device.
  const [base] = useState(task);
  const [title, setTitle] = useState(task.title);
  const [notes, setNotes] = useState(task.notes);
  const [date, setDate] = useState(task.due ? toDateInput(task.due) : "");
  const [time, setTime] = useState(task.due ? toTimeInput(task.due) : "");
  const [rrule, setRrule] = useState<string | null>(task.rrule);
  const [priority, setPriority] = useState(task.priority);
  const [list, setList] = useState(task.list);
  const first = useAutoFocus<HTMLInputElement>();
  const needsDate = rrule !== null && date === "";
  const valid = title.trim() !== "" && list.trim() !== "" && !needsDate;

  function save() {
    const due = dueFromInputs(date, time);
    const fields: TaskFields = {};
    if (title.trim() !== base.title) fields.title = title.trim();
    if (notes !== base.notes) fields.notes = notes;
    if (!sameDue(due, base.due)) fields.due = due;
    if (rrule !== base.rrule) fields.rrule = rrule;
    if (priority !== base.priority) fields.priority = priority;
    if (list.trim() !== base.list) fields.list = list.trim();
    if (Object.keys(fields).length === 0) return onCancel();
    onSave(fields);
  }

  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) save();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <TextField ref={first} label="Title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={300} required />
      <TextArea label="Notes" value={notes} onChange={(e) => setNotes(e.target.value)} maxLength={4000} />
      <div className={styles.pair}>
        <TextField label="Due date" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        <TextField label="Due time" type="time" value={time} onChange={(e) => setTime(e.target.value)} disabled={!date} hint={date ? "Optional" : undefined} />
      </div>
      {date && (
        <div>
          <Button
            variant="quiet"
            compact
            onClick={() => {
              setDate("");
              setTime("");
            }}
          >
            Clear due date
          </Button>
        </div>
      )}
      <div className={styles.pair}>
        <RepeatSelect value={rrule} onChange={setRrule} />
        <SelectField label="Priority" value={priority} onChange={(e) => setPriority(Number(e.target.value))}>
          {PRIORITIES.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </SelectField>
      </div>
      {needsDate && <p className={styles.callout} data-tone="accent">A repeating task needs a due date to count from.</p>}
      <TextField label="List" value={list} onChange={(e) => setList(e.target.value)} list={listId} maxLength={80} required />
      <datalist id={listId}>
        {lists.map((l) => (
          <option key={l} value={l} />
        ))}
      </datalist>
      <div className={styles.actions}>
        <ConfirmButton label="Delete" icon={<TrashIcon />} busy={deleting} onConfirm={onDelete} />
        <span className={styles.spacer} />
        <Button variant="quiet" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" type="submit" busy={busy} disabled={!valid}>
          Save
        </Button>
      </div>
    </form>
  );
}

export function TasksSection() {
  const active = useMenuActive();
  const now = useNow();
  const [selected, setSelected] = useState<string>();
  const [showDone, setShowDone] = useState(false);
  const [openId, setOpenId] = useState<string>();
  const [draft, setDraft] = useState("");
  const lists = useTaskLists(active);
  const tasks = useTasks(selected, showDone ? "all" : "open", active);
  const create = useCreateTask();
  const patch = usePatchTask();
  const remove = useDeleteTask();
  const setDone = useSetTaskDone();

  const listNames = (lists.data ?? []).map((l) => l.name);
  const totalOpen = (lists.data ?? []).reduce((n, l) => n + l.open_count, 0);
  const all = tasks.data ?? [];
  const open = all.filter((t) => !t.completed_at);
  const done = all.filter((t) => t.completed_at);

  function add() {
    const title = draft.trim();
    if (!title) return;
    create.mutate({ title, ...(selected ? { list: selected } : {}) }, { onSuccess: () => setDraft("") });
  }

  function renderRow(t: Task) {
    const isOpen = openId === t.id;
    return (
      <TaskRow
        key={t.id}
        task={t}
        now={now}
        showList={selected === undefined}
        expanded={isOpen}
        onToggle={(d) => setDone.mutate({ id: t.id, done: d })}
        onOpen={() => setOpenId(isOpen ? undefined : t.id)}
      >
        {isOpen && (
          <div className={styles.editor}>
            <TaskEditor
              task={t}
              lists={listNames}
              busy={patch.isPending}
              deleting={remove.isPending}
              onCancel={() => setOpenId(undefined)}
              onSave={(fields) => patch.mutate({ id: t.id, fields }, { onSuccess: () => setOpenId(undefined) })}
              onDelete={() => remove.mutate(t.id, { onSuccess: () => setOpenId(undefined) })}
            />
          </div>
        )}
      </TaskRow>
    );
  }

  return (
    <div className={styles.stack}>
      <div className={styles.chips} role="group" aria-label="Lists">
        <Chip selected={selected === undefined} count={lists.data ? totalOpen : undefined} onClick={() => setSelected(undefined)}>
          All
        </Chip>
        {(lists.data ?? []).map((l) => (
          <Chip key={l.name} selected={selected === l.name} count={l.open_count} onClick={() => setSelected(l.name)}>
            {l.name}
          </Chip>
        ))}
      </div>

      <form
        className={styles.quickAdd}
        onSubmit={(e) => {
          e.preventDefault();
          add();
        }}
      >
        <TextField label={selected ? `Add to ${selected}` : "Add a task"} value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="Oat milk" enterKeyHint="done" maxLength={300} />
        <Button type="submit" variant="primary" busy={create.isPending} disabled={!draft.trim()} aria-label="Add task">
          <PlusIcon />
          Add
        </Button>
      </form>

      <Switch label="Show completed" checked={showDone} onChange={setShowDone} />

      {tasks.isError && !tasks.data ? (
        <StateMessage kind="error" title="Couldn't load tasks" onRetry={() => void tasks.refetch()}>
          {tasks.error.message}
        </StateMessage>
      ) : tasks.isPending ? (
        <StateMessage kind="loading" />
      ) : all.length === 0 ? (
        <StateMessage kind="empty" title={selected ? `${selected} is empty` : "Nothing to do"}>
          Add something above, or ask Renamon to.
        </StateMessage>
      ) : (
        <>
          {open.length > 0 && <div className={styles.group}>{open.map(renderRow)}</div>}
          {showDone && done.length > 0 && (
            <section>
              <SectionTitle>Completed</SectionTitle>
              <div className={styles.group}>{done.map(renderRow)}</div>
            </section>
          )}
        </>
      )}
    </div>
  );
}
