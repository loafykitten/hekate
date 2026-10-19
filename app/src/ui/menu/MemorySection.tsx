import { useEffect, useState } from "react";
import { useCoreMemory, useDeleteCore, useDeleteMemory, useMemories, useNow, usePatchMemory, usePutCore } from "../../data/hooks";
import { MEMORY_KINDS, type CoreEntry, type Memory } from "../../data/model";
import { EditIcon, PlusIcon, TrashIcon } from "../icons";
import { Button, Chip, ConfirmButton, IconButton, SectionTitle, SelectField, StateMessage, TextArea, TextField } from "../kit/kit";
import { useAutoFocus } from "../kit/useAutoFocus";
import { kindLabel } from "../rows/labels";
import { MemoryRow } from "../rows/rows";
import { useMenuActive } from "./MenuContext";
import styles from "./sections.module.css";
import rowStyles from "../rows/rows.module.css";

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

// "favorite_food" -> "Favorite food"
function prettyKey(key: string): string {
  const spaced = key.replace(/[_-]+/g, " ").trim();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

function CoreForm({ entry, busy, onSave, onCancel, onDelete, deleting }: {
  entry?: CoreEntry;
  busy: boolean;
  deleting?: boolean;
  onSave: (key: string, value: string) => void;
  onCancel: () => void;
  onDelete?: () => void;
}) {
  const [key, setKey] = useState(entry?.key ?? "");
  const [value, setValue] = useState(entry?.value ?? "");
  const first = useAutoFocus<HTMLInputElement & HTMLTextAreaElement>();
  const valid = key.trim() !== "" && value.trim() !== "";
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) onSave(key.trim(), value.trim());
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      {entry ? (
        <TextArea ref={first} label={prettyKey(entry.key)} value={value} onChange={(e) => setValue(e.target.value)} maxLength={2000} required />
      ) : (
        <>
          <TextField ref={first} label="Label" value={key} onChange={(e) => setKey(e.target.value)} placeholder="Favorite tea" maxLength={80} required />
          <TextArea label="Detail" value={value} onChange={(e) => setValue(e.target.value)} maxLength={2000} required />
        </>
      )}
      <div className={styles.actions}>
        {onDelete && <ConfirmButton label="Delete" icon={<TrashIcon />} busy={deleting} onConfirm={onDelete} />}
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

function CoreProfile({ active }: { active: boolean }) {
  const core = useCoreMemory(active);
  const put = usePutCore();
  const remove = useDeleteCore();
  const [editing, setEditing] = useState<string>();
  const [adding, setAdding] = useState(false);
  const entries = core.data ?? [];
  return (
    <section>
      <SectionTitle
        action={
          !adding && (
            <Button
              variant="quiet"
              compact
              onClick={() => {
                setAdding(true);
                setEditing(undefined);
              }}
            >
              <PlusIcon />
              Add
            </Button>
          )
        }
      >
        About you
      </SectionTitle>
      {adding && (
        <div className={styles.formCard} style={{ marginBottom: "var(--space-3)" }}>
          <CoreForm busy={put.isPending} onCancel={() => setAdding(false)} onSave={(key, value) => put.mutate({ key, value }, { onSuccess: () => setAdding(false) })} />
        </div>
      )}
      {core.isError && !core.data ? (
        <StateMessage kind="error" title="Couldn't load your profile" onRetry={() => void core.refetch()}>
          {core.error.message}
        </StateMessage>
      ) : core.isPending ? (
        <StateMessage kind="loading" />
      ) : entries.length === 0 ? (
        <p className={styles.callout}>Nothing here yet. Renamon adds the essentials as she gets to know you, and you can add your own.</p>
      ) : (
        <div className={styles.group}>
          {entries.map((e) => {
            const open = editing === e.key;
            return (
              <div key={e.key} className={rowStyles.row} data-expanded={open || undefined}>
                <div className={rowStyles.line}>
                  <button type="button" className={rowStyles.body} aria-expanded={open} onClick={() => setEditing(open ? undefined : e.key)}>
                    <span className={rowStyles.when} style={{ textTransform: "uppercase", letterSpacing: "0.08em", fontWeight: 650 }}>
                      {prettyKey(e.key)}
                    </span>
                    <span className={rowStyles.memoryText}>{e.value}</span>
                  </button>
                  <IconButton label={`Edit ${prettyKey(e.key)}`} onClick={() => setEditing(open ? undefined : e.key)} style={{ marginTop: 6 }}>
                    <EditIcon />
                  </IconButton>
                </div>
                {open && (
                  <div className={styles.editor}>
                    <CoreForm
                      entry={e}
                      busy={put.isPending}
                      deleting={remove.isPending}
                      onCancel={() => setEditing(undefined)}
                      onSave={(key, value) => put.mutate({ key, value }, { onSuccess: () => setEditing(undefined) })}
                      onDelete={() => remove.mutate(e.key, { onSuccess: () => setEditing(undefined) })}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function MemoryEditor({ memory, busy, deleting, onSave, onCancel, onDelete }: {
  memory: Memory;
  busy: boolean;
  deleting: boolean;
  onSave: (fields: { content?: string; kind?: string; importance?: number }) => void;
  onCancel: () => void;
  onDelete: () => void;
}) {
  // Compare against the record as it was when editing began: refetches replace the prop, and
  // diffing against the new copy would undo changes made meanwhile on another device.
  const [base] = useState(memory);
  const [content, setContent] = useState(memory.content);
  const [kind, setKind] = useState(memory.kind);
  const [importance, setImportance] = useState(memory.importance);
  const first = useAutoFocus<HTMLInputElement & HTMLTextAreaElement>();
  const kinds = MEMORY_KINDS.includes(memory.kind as (typeof MEMORY_KINDS)[number]) ? [...MEMORY_KINDS] : [...MEMORY_KINDS, memory.kind];
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        const fields: { content?: string; kind?: string; importance?: number } = {};
        if (content.trim() !== base.content) fields.content = content.trim();
        if (kind !== base.kind) fields.kind = kind;
        if (importance !== base.importance) fields.importance = importance;
        if (Object.keys(fields).length === 0) return onCancel();
        onSave(fields);
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <TextArea ref={first} label="What she remembers" value={content} onChange={(e) => setContent(e.target.value)} maxLength={2000} required />
      <div className={styles.pair}>
        <SelectField label="Kind" value={kind} onChange={(e) => setKind(e.target.value)}>
          {kinds.map((k) => (
            <option key={k} value={k}>
              {kindLabel(k)}
            </option>
          ))}
        </SelectField>
        <SelectField label="Importance" value={importance} onChange={(e) => setImportance(Number(e.target.value))}>
          {[1, 2, 3, 4, 5].map((n) => (
            <option key={n} value={n}>
              {n} of 5
            </option>
          ))}
        </SelectField>
      </div>
      <div className={styles.actions}>
        <ConfirmButton label="Forget" icon={<TrashIcon />} busy={deleting} onConfirm={onDelete} />
        <span className={styles.spacer} />
        <Button variant="quiet" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" type="submit" busy={busy} disabled={content.trim() === ""}>
          Save
        </Button>
      </div>
    </form>
  );
}

export function MemorySection() {
  const active = useMenuActive();
  const now = useNow();
  const [search, setSearch] = useState("");
  const [kind, setKind] = useState("");
  const [openId, setOpenId] = useState<string>();
  const q = useDebounced(search.trim(), 250);
  const memories = useMemories(q, kind, active);
  const patch = usePatchMemory();
  const remove = useDeleteMemory();
  const items = memories.data ?? [];

  return (
    <div className={styles.stack}>
      <CoreProfile active={active} />

      <section>
        <SectionTitle>What she remembers</SectionTitle>
        <div className={styles.stack} style={{ gap: "var(--space-3)" }}>
          <TextField label="Search memories" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Coffee, birthdays, the dentist…" enterKeyHint="search" autoComplete="off" />
          <div className={styles.chips} role="group" aria-label="Kind">
            <Chip selected={kind === ""} onClick={() => setKind("")}>
              All
            </Chip>
            {MEMORY_KINDS.map((k) => (
              <Chip key={k} selected={kind === k} onClick={() => setKind(k)}>
                {kindLabel(k)}
              </Chip>
            ))}
          </div>
          {memories.isError && !memories.data ? (
            <StateMessage kind="error" title="Couldn't load memories" onRetry={() => void memories.refetch()}>
              {memories.error.message}
            </StateMessage>
          ) : memories.isPending ? (
            <StateMessage kind="loading" />
          ) : items.length === 0 ? (
            <StateMessage kind="empty" title={q || kind ? "No matches" : "Nothing remembered yet"}>
              {q || kind ? "Try different words or another kind." : "Things you tell her to remember, and details she picks up, are kept here."}
            </StateMessage>
          ) : (
            <div className={styles.group}>
              {items.map((m) => {
                const open = openId === m.id;
                return (
                  <MemoryRow key={m.id} memory={m} now={now} expanded={open} onOpen={() => setOpenId(open ? undefined : m.id)}>
                    {open && (
                      <div className={styles.editor}>
                        <MemoryEditor
                          memory={m}
                          busy={patch.isPending}
                          deleting={remove.isPending}
                          onCancel={() => setOpenId(undefined)}
                          onSave={(fields) => patch.mutate({ id: m.id, fields }, { onSuccess: () => setOpenId(undefined) })}
                          onDelete={() => remove.mutate(m.id, { onSuccess: () => setOpenId(undefined) })}
                        />
                      </div>
                    )}
                  </MemoryRow>
                );
              })}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
