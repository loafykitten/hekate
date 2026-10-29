import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { MAX_ATTACHMENTS, uploadThreadFile } from "../../data/files";
import type { ThreadFile } from "../../data/model";

export interface Attachment {
  key: string;
  name: string;
  size: number;
  status: "uploading" | "done" | "error";
  progress: number; // 0..1 while uploading
  file?: ThreadFile; // once uploaded
  local?: File; // the picked file, for a thumbnail
  error?: string;
}

export interface Attachments {
  items: Attachment[];
  /** Starts uploading each file right away (up to the limit; the rest are dropped with a note). */
  add: (files: File[]) => void;
  remove: (key: string) => void;
  /** Forgets the finished ones without touching the service (they were just sent). */
  clear: () => void;
  /** Puts uploaded files back, after a send the service refused. */
  restore: (items: Attachment[]) => void;
  /** Some are still uploading. */
  busy: boolean;
  /** The ids to send; undefined while any is uploading or failed. */
  ids: string[] | undefined;
  /** Why the last attach was cut short, if it was. */
  note?: string;
}

let counter = 0;

// The files attached to a thread's next message. Uploads start on attach; removing one aborts it.
export function useAttachments(thread: string): Attachments {
  const [items, setItems] = useState<Attachment[]>([]);
  const [note, setNote] = useState<string>();
  const aborts = useRef(new Map<string, () => void>());

  const patch = useCallback((key: string, fields: Partial<Attachment>) => setItems((list) => list.map((a) => (a.key === key ? { ...a, ...fields } : a))), []);

  const taken = items.length;
  const add = useCallback(
    (files: File[]) => {
      const room = Math.max(0, MAX_ATTACHMENTS - taken);
      const take = files.slice(0, room);
      setNote(files.length > take.length ? `Up to ${MAX_ATTACHMENTS} files at a time.` : undefined);
      if (take.length === 0) return;
      const fresh = take.map((f): Attachment => ({ key: `a${++counter}`, name: f.name, size: f.size, status: "uploading", progress: 0, local: f }));
      setItems((list) => [...list, ...fresh]);
      take.forEach((f, i) => {
        const key = fresh[i]!.key;
        const upload = uploadThreadFile(thread, f, (progress) => patch(key, { progress }));
        aborts.current.set(key, upload.abort);
        upload.promise.then(
          (file) => {
            aborts.current.delete(key);
            patch(key, { status: "done", progress: 1, file });
          },
          (e: unknown) => {
            aborts.current.delete(key);
            if (e instanceof DOMException && e.name === "AbortError") return;
            patch(key, { status: "error", error: e instanceof Error ? e.message : "The upload failed." });
          },
        );
      });
    },
    [thread, patch, taken],
  );

  const remove = useCallback((key: string) => {
    aborts.current.get(key)?.();
    aborts.current.delete(key);
    setNote(undefined);
    setItems((list) => list.filter((a) => a.key !== key));
  }, []);

  const clear = useCallback(() => {
    setNote(undefined);
    setItems((list) => list.filter((a) => a.status === "uploading"));
  }, []);

  const restore = useCallback((back: Attachment[]) => setItems((list) => [...back.filter((b) => !list.some((a) => a.key === b.key)), ...list].slice(0, MAX_ATTACHMENTS)), []);

  // Leaving the thread abandons uploads in flight.
  useEffect(() => {
    const live = aborts.current;
    return () => {
      for (const abort of live.values()) abort();
      live.clear();
    };
  }, [thread]);

  const busy = items.some((a) => a.status === "uploading");
  const ids = useMemo(() => (items.every((a) => a.status === "done") ? items.map((a) => a.file!.id) : undefined), [items]);
  return { items, add, remove, clear, restore, busy, ids, note };
}
