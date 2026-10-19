import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";
import { mergeLog } from "../conversation/log";
import { useConversationState } from "./ConversationContext";
import { fetchMessages, HISTORY_PAGE } from "./api";
import { CloseIcon } from "./icons";
import styles from "./LogSheet.module.css";

interface Props {
  open: boolean;
  onClose: () => void;
}

// The conversation so far: saved history from the service plus the turns happening now, newest
// at the bottom. A bottom sheet on phones, a side panel on wide screens.
export function LogSheet({ open, onClose }: Props) {
  const { live, historyEpoch, connection } = useConversationState();
  const sheetRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const drag = useRef<{ y: number; id: number }>(undefined);

  // The epoch each page was fetched at, so live entries the history has caught up with can go.
  const epochNow = useRef(historyEpoch);
  useEffect(() => {
    epochNow.current = historyEpoch;
  }, [historyEpoch]);
  const history = useInfiniteQuery({
    queryKey: ["messages"],
    queryFn: async ({ pageParam, signal }) => {
      const epoch = epochNow.current;
      return { messages: await fetchMessages(pageParam, signal), epoch };
    },
    initialPageParam: undefined as string | undefined,
    // Pages arrive newest first; a full page means there may be older messages.
    getNextPageParam: (last) => (last.messages.length >= HISTORY_PAGE ? last.messages[0]?.id : undefined),
    enabled: open,
    retry: false,
    networkMode: "always",
  });

  // Refetch when a connection was (re)established or a turn finished while the sheet is open.
  const refetch = history.refetch;
  const seenEpoch = useRef(historyEpoch);
  useEffect(() => {
    if (seenEpoch.current === historyEpoch) return;
    seenEpoch.current = historyEpoch;
    if (open) void refetch();
  }, [historyEpoch, open, refetch]);

  const entries = useMemo(() => {
    const pages = history.data?.pages ?? [];
    const messages = [...pages].reverse().flatMap((p) => p.messages);
    return mergeLog(messages, live, pages[0]?.epoch);
  }, [history.data, live]);

  // Focus moves into the sheet when it opens and back out when it closes.
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    sheetRef.current?.focus({ preventScroll: true });
    stickToBottom.current = true;
    return () => previous?.focus?.({ preventScroll: true });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  // Keep the newest message in view unless the reader scrolled up.
  const last = entries[entries.length - 1];
  useEffect(() => {
    const list = listRef.current;
    if (open && list && stickToBottom.current) list.scrollTop = list.scrollHeight;
  }, [open, entries.length, last?.text]);

  const empty = entries.length === 0;
  return (
    <div className={styles.root} data-open={open || undefined} aria-hidden={!open}>
      <div className={styles.backdrop} onClick={onClose} />
      <div
        ref={sheetRef}
        className={styles.sheet}
        role="dialog"
        aria-modal="true"
        aria-label="Conversation"
        tabIndex={-1}
        inert={!open}
      >
        <header
          className={styles.header}
          onPointerDown={(e) => (drag.current = { y: e.clientY, id: e.pointerId })}
          onPointerMove={(e) => {
            if (drag.current?.id === e.pointerId && e.clientY - drag.current.y > 48) {
              drag.current = undefined;
              onClose();
            }
          }}
          onPointerUp={() => (drag.current = undefined)}
          onPointerCancel={() => (drag.current = undefined)}
        >
          <span className={styles.grabber} aria-hidden="true" />
          <h2 className={styles.title}>Conversation</h2>
          <button type="button" className={styles.close} aria-label="Close conversation" onClick={onClose}>
            <CloseIcon />
          </button>
        </header>
        <div
          ref={listRef}
          className={styles.list}
          onScroll={(e) => {
            const el = e.currentTarget;
            stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
          }}
        >
          {history.hasNextPage && (
            <button type="button" className={styles.earlier} disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>
              {history.isFetchingNextPage ? "Loading…" : "Earlier messages"}
            </button>
          )}
          {entries.map((e) => (
            <p key={e.id} className={styles.entry} data-role={e.role} data-pending={e.pending && !e.text ? true : undefined}>
              {e.text || <span className={styles.typing} aria-label="Thinking">…</span>}
            </p>
          ))}
          {empty && (
            <p className={styles.empty}>
              {history.isPending && open
                ? "Loading…"
                : history.isError || connection !== "open"
                  ? "The conversation will appear here once Hekate is reachable."
                  : "Nothing yet. Say hello."}
            </p>
          )}
          {history.isError && !empty && <p className={styles.note}>Couldn&rsquo;t load earlier messages.</p>}
        </div>
      </div>
    </div>
  );
}
