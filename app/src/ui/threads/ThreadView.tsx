import { Fragment, useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { currentStep, historyHolds, isRunning, mergeReviews, placeReviews, reviewRunning } from "../../conversation/threadLive";
import { useThread, useThreadMessages, usePatchThread } from "../../data/hooks";
import type { Review, Thread, ThreadFile, ThreadModel, ThreadStep } from "../../data/model";
import { ApiError } from "../../data/rest";
import { useConversation, useThreadLive } from "../ConversationContext";
import { ArchiveIcon, BackIcon, EditIcon } from "../icons";
import { Button, IconButton, Segmented, StateMessage } from "../kit/kit";
import { useMenuActive } from "../menu/MenuContext";
import { setActiveThread } from "../menu/threadTarget";
import { Markdown } from "./Markdown";
import { MODEL_LABEL } from "./labels";
import { ReviewCard } from "./ReviewCard";
import { ThreadComposer } from "./ThreadComposer";
import { ThreadFiles } from "./ThreadFiles";
import { useAttachments } from "./useAttachments";
import { ThreadSteps, type StepView } from "./ThreadSteps";
import styles from "./threads.module.css";

const MAX_TITLE = 80;

function savedSteps(steps: ThreadStep[], turnId: string): StepView[] {
  return steps.filter((s) => s.turn_id === turnId).map((s) => ({ tool: s.tool, label: s.label, state: s.status, summary: s.summary }));
}

// One thread: header (rename, model, archive), the transcript with her replies as markdown and each
// turn's tool steps folded away, the turn happening now, and the composer.
export function ThreadView({ id, onBack }: { id: string; onBack: () => void }) {
  const conversation = useConversation();
  const menuActive = useMenuActive();
  const thread = useThread(id);
  const history = useThreadMessages(id);
  const live = useThreadLive(id);
  const patch = usePatchThread();
  const rootRef = useRef<HTMLElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const attachments = useAttachments(id);
  const [dragging, setDragging] = useState(false);

  // The thread view is what's on screen: P talks to it, the menu makes room, focus moves in.
  useEffect(() => {
    if (!menuActive) return;
    setActiveThread(id);
    document.documentElement.dataset.threadOpen = "";
    return () => {
      setActiveThread(undefined);
      delete document.documentElement.dataset.threadOpen;
    };
  }, [id, menuActive]);
  useEffect(() => {
    rootRef.current?.focus({ preventScroll: true });
  }, [id]);

  // A turn ended: fetch the saved transcript, which has her whole reply and its steps.
  const { refetch: refetchMessages } = history;
  const { refetch: refetchThread } = thread;
  const seenEpoch = useRef(live.epoch);
  useEffect(() => {
    if (seenEpoch.current === live.epoch) return;
    seenEpoch.current = live.epoch;
    void refetchMessages();
    void refetchThread();
  }, [live.epoch, refetchMessages, refetchThread]);

  const pages = history.data?.pages;
  const messages = useMemo(() => [...(pages ?? [])].reverse().flatMap((p) => p.messages), [pages]);
  const steps = useMemo(() => (pages ?? []).flatMap((p) => p.steps), [pages]);
  // Saved reviews (a page may repeat one) and the ones seen live, one card each.
  const liveReviews = live.reviews;
  const reviews = useMemo(() => mergeReviews((pages ?? []).flatMap((p) => p.reviews ?? []), liveReviews), [pages, liveReviews]);
  // The files of the turns on screen, once each (a page may repeat one), by turn and direction.
  const files = useMemo(() => {
    const byTurn = new Map<string, { in: ThreadFile[]; out: ThreadFile[] }>();
    const seen = new Set<string>();
    for (const f of (pages ?? []).flatMap((p) => p.files ?? [])) {
      if (seen.has(f.id) || !f.turn_id) continue;
      seen.add(f.id);
      const entry = byTurn.get(f.turn_id) ?? { in: [], out: [] };
      entry[f.direction].push(f);
      byTurn.set(f.turn_id, entry);
    }
    return byTurn;
  }, [pages]);
  const placed = useMemo(() => placeReviews(messages, reviews), [messages, reviews]);

  const turn = live.turn;
  // The copy that was streaming goes only once the saved transcript holds the turn; if the refetch
  // failed or came up short, it keeps showing.
  const finished = turn && historyHolds(messages, turn) ? turn.id : undefined;
  useEffect(() => {
    if (finished) conversation.threads.settle(id, finished);
  }, [finished, id, conversation]);
  // What history already holds of the turn happening now (the owner's words are saved when it starts).
  const savedUser = turn && messages.some((m) => m.role === "user" && m.turn_id === turn.id);
  const savedReply = turn && messages.some((m) => m.role === "assistant" && m.turn_id === turn.id);

  const reviewing = reviewRunning(reviews);
  const running = isRunning(live) || reviewing || (!turn && !!thread.data?.running);
  const lastText = turn?.text;
  useEffect(() => {
    const list = listRef.current;
    if (list && stickToBottom.current) list.scrollTop = list.scrollHeight;
  }, [messages.length, lastText, turn?.steps.length, running, live.error, reviews]);

  if (thread.isError && !thread.data) {
    const gone = thread.error instanceof ApiError && thread.error.status === 404;
    return (
      <div className={styles.view}>
        <BackButton onBack={onBack} />
        <StateMessage kind={gone ? "empty" : "error"} title={gone ? "This thread is gone" : "Couldn't open this thread"} onRetry={gone ? undefined : () => void thread.refetch()}>
          {gone ? "It was deleted, or the link is old." : thread.error.message}
        </StateMessage>
      </div>
    );
  }
  if (!thread.data) {
    return (
      <div className={styles.view}>
        <BackButton onBack={onBack} />
        <StateMessage kind="loading" />
      </div>
    );
  }
  const t = thread.data;
  const looseBefore = placed.loose.filter((r) => turn && r.followup_turn_id === turn.id);
  const looseAfter = placed.loose.filter((r) => !looseBefore.includes(r));
  const shownSteps = new Set<string>();
  const canAttach = !t.archived;
  // Files dropped anywhere on the thread attach to the next message (desktop; a phone has the button).
  const hasFiles = (e: DragEvent) => canAttach && e.dataTransfer.types.includes("Files");

  return (
    <section
      ref={rootRef}
      className={styles.view}
      role="region"
      aria-label={`Thread: ${t.title}`}
      tabIndex={-1}
      onKeyDown={onKeyDown}
      data-dropping={dragging || undefined}
      onDragOver={(e) => {
        if (!hasFiles(e)) return;
        e.preventDefault();
        setDragging(true);
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={(e) => {
        if (!hasFiles(e)) return;
        e.preventDefault();
        setDragging(false);
        attachments.add([...e.dataTransfer.files]);
      }}
    >
      <header className={styles.head}>
        <div className={styles.headRow}>
          <BackButton onBack={onBack} />
          <div className={styles.headTools}>
            <Segmented<ThreadModel>
              label="Model"
              value={t.model}
              onChange={(model) => patch.mutate({ id, fields: { model } })}
              options={[
                { value: "deep", label: MODEL_LABEL.deep },
                { value: "chat", label: MODEL_LABEL.chat },
              ]}
            />
            <IconButton label={t.archived ? "Restore thread" : "Archive thread"} disabled={patch.isPending} onClick={() => archive(t)}>
              <ArchiveIcon />
            </IconButton>
          </div>
        </div>
        <Title thread={t} />
      </header>

      <div
        ref={listRef}
        className={styles.transcript}
        onScroll={(e) => {
          const el = e.currentTarget;
          stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 64;
        }}
      >
        {history.hasNextPage && (
          <button type="button" className={styles.earlier} disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>
            {history.isFetchingNextPage ? "Loading…" : "Earlier messages"}
          </button>
        )}
        {messages.map((m) => {
          // A review stands after the reply it checked, or before the reply to its findings.
          const cards = (list: Review[] | undefined) => list?.map((r) => <ReviewCard key={r.id} review={r} />);
          let body;
          if (m.role === "user") {
            // A turn started by a review has no owner message; never an empty bubble.
            body = m.text ? <Mine thread={id} text={m.text} files={files.get(m.turn_id)?.in ?? []} /> : null;
          } else {
            // A turn's steps sit under its first reply.
            const first = !shownSteps.has(m.turn_id);
            const own = first ? savedSteps(steps, m.turn_id) : [];
            shownSteps.add(m.turn_id);
            body = (
              <div className={styles.hers}>
                <Markdown text={m.text} />
                <ThreadSteps steps={own} />
                {first && <ThreadFiles thread={id} files={files.get(m.turn_id)?.out ?? []} direction="out" />}
              </div>
            );
          }
          return (
            <Fragment key={m.id}>
              {cards(placed.before.get(m.id))}
              {body}
              {cards(placed.after.get(m.id))}
            </Fragment>
          );
        })}
        {turn && !savedUser && turn.userText && <Mine thread={id} text={turn.userText} files={turn.files} />}
        {/* Reviews whose turn isn't saved yet come before the reply being written to their findings, else after. */}
        {looseBefore.map((r) => (
          <ReviewCard key={r.id} review={r} />
        ))}
        {turn && !savedReply && (turn.text || !turn.ended || turn.steps.length > 0) && (
          <div className={styles.hers} data-live={!turn.ended || undefined}>
            {turn.text && <Markdown text={turn.text} />}
            <ThreadSteps steps={turn.steps} live />
            {!turn.ended && <Working label={currentStep(turn)?.label} />}
          </div>
        )}
        {looseAfter.map((r) => (
          <ReviewCard key={r.id} review={r} />
        ))}
        {!turn && running && !reviewing && (
          <div className={styles.hers} data-live>
            <Working />
          </div>
        )}
        {messages.length === 0 && !turn && !running && !history.isPending && (
          <p className={styles.empty}>{history.isError ? "Couldn't load this thread's messages." : "Nothing here yet. Say what you want done."}</p>
        )}
        {history.isPending && <StateMessage kind="loading" />}
        {history.isError && messages.length > 0 && <p className={styles.note}>Couldn&rsquo;t load earlier messages.</p>}
        {live.error && (
          <p className={styles.problem} role="alert">
            {live.error}
          </p>
        )}
      </div>

      {t.archived ? (
        <div className={styles.archivedNote}>
          <span>This thread is archived.</span>
          <Button compact busy={patch.isPending} onClick={() => archive(t)}>
            Restore it
          </Button>
        </div>
      ) : (
        <ThreadComposer thread={id} attachments={attachments} running={running} reviewing={reviewing && !isRunning(live)} transcribing={live.transcribing} />
      )}
      {dragging && (
        <div className={styles.dropHint} aria-hidden="true">
          Drop files to attach them
        </div>
      )}
    </section>
  );

  function archive(target: Thread) {
    patch.mutate(
      { id, fields: { archived: !target.archived } },
      { onSuccess: () => target.archived || onBack() },
    );
  }

  // Esc discards a recording first, then leaves the thread to the menu (which closes itself).
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape" && conversation.getState().threadTalk?.thread === id) {
      e.stopPropagation();
      conversation.cancelCapture();
    }
  }
}

function BackButton({ onBack }: { onBack: () => void }) {
  return (
    <Button variant="quiet" compact onClick={onBack}>
      <BackIcon />
      Threads
    </Button>
  );
}

// The owner's message: their words in a bubble, the files they sent beneath it.
function Mine({ thread, text, files }: { thread: string; text: string; files: readonly ThreadFile[] }) {
  if (files.length === 0) return <p className={styles.mine}>{text}</p>;
  return (
    <div className={styles.mineGroup}>
      <p className={styles.mine}>{text}</p>
      <ThreadFiles thread={thread} files={files} direction="in" />
    </div>
  );
}

function Working({ label }: { label?: string }) {
  return (
    <p className={styles.working} role="status">
      <span className={styles.dots} aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      {label || "Working on it…"}
    </p>
  );
}

// The title is the heading; tapping it renames in place.
function Title({ thread }: { thread: Thread }) {
  const patch = usePatchThread();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");

  function save() {
    const next = draft.trim();
    setEditing(false);
    if (next && next !== thread.title) patch.mutate({ id: thread.id, fields: { title: next } });
  }

  if (editing) {
    return (
      <input
        className={styles.titleInput}
        value={draft}
        maxLength={MAX_TITLE}
        aria-label="Thread title"
        autoFocus
        enterKeyHint="done"
        onChange={(e) => setDraft(e.target.value)}
        onFocus={(e) => e.currentTarget.select()}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
          else if (e.key === "Escape") {
            e.stopPropagation();
            setDraft(thread.title);
            setEditing(false);
          }
        }}
      />
    );
  }
  return (
    <h3 className={styles.title}>
      <button
        type="button"
        onClick={() => {
          setDraft(thread.title);
          setEditing(true);
        }}
        aria-label={`${thread.title}. Rename`}
      >
        <span>{thread.title}</span>
        <EditIcon aria-hidden="true" />
      </button>
    </h3>
  );
}
