import { lazy, Suspense, useEffect, useState } from "react";
import { threadRunning } from "../../conversation/threadLive";
import { useCreateThread, useNow, useThreads } from "../../data/hooks";
import type { Thread } from "../../data/model";
import { relativeTime } from "../../data/time";
import { useThreadLive } from "../ConversationContext";
import { ChevronIcon, PlusIcon } from "../icons";
import { Button, Segmented, StateMessage } from "../kit/kit";
import rowStyles from "../rows/rows.module.css";
import { MODEL_LABEL } from "../threads/labels";
import threadStyles from "../threads/threads.module.css";
import { useMenuActive } from "./MenuContext";
import styles from "./sections.module.css";
import { clearThreadTarget, useThreadTarget } from "./threadTarget";

// The markdown renderer is most of what a thread view adds; it loads when the first thread opens.
const ThreadView = lazy(() => import("../threads/ThreadView").then((m) => ({ default: m.ThreadView })));

type Tab = "active" | "archived";

// Longer work, each in a conversation of its own: research, lab work, anything that would crowd
// the one she talks in.
export function ThreadsSection() {
  const active = useMenuActive();
  const [tab, setTab] = useState<Tab>("active");
  const [openId, setOpenId] = useState<string>();
  const create = useCreateThread();

  // A notification or link asked for a thread: show it, whether this section was already mounted
  // or not.
  const target = useThreadTarget();
  const [handled, setHandled] = useState<unknown>();
  if (target && target !== handled) {
    setHandled(target);
    setOpenId(target.id);
  }
  useEffect(() => {
    if (target) clearThreadTarget(target);
  }, [target]);

  if (openId) {
    return (
      <Suspense fallback={<StateMessage kind="loading" />}>
        <ThreadView id={openId} onBack={() => setOpenId(undefined)} />
      </Suspense>
    );
  }
  return <ThreadList active={active} tab={tab} onTab={setTab} onOpen={setOpenId} creating={create.isPending} onCreate={() => create.mutate({}, { onSuccess: (t) => (setTab("active"), setOpenId(t.id)) })} />;
}

interface ListProps {
  active: boolean;
  tab: Tab;
  onTab: (tab: Tab) => void;
  onOpen: (id: string) => void;
  creating: boolean;
  onCreate: () => void;
}

function ThreadList({ active, tab, onTab, onOpen, creating, onCreate }: ListProps) {
  const now = useNow();
  const threads = useThreads(tab === "archived", active);
  const items = threads.data ?? [];
  return (
    <div className={styles.stack}>
      <div className={styles.toolbar}>
        <Segmented<Tab>
          label="Which threads"
          value={tab}
          onChange={onTab}
          options={[
            { value: "active", label: "Active" },
            { value: "archived", label: "Archived" },
          ]}
        />
        <span className={styles.grow} />
        <Button variant="primary" busy={creating} onClick={onCreate}>
          <PlusIcon />
          New thread
        </Button>
      </div>
      {threads.isError && !threads.data ? (
        <StateMessage kind="error" title="Couldn't load threads" onRetry={() => void threads.refetch()}>
          {threads.error.message}
        </StateMessage>
      ) : threads.isPending ? (
        <StateMessage kind="loading" />
      ) : items.length === 0 ? (
        <StateMessage kind="empty" title={tab === "archived" ? "Nothing archived" : "No threads yet"}>
          {tab === "archived"
            ? "Threads you archive wait here."
            : "Start one for research or lab work, or ask Renamon to open one for you."}
        </StateMessage>
      ) : (
        <div className={styles.group}>
          {items.map((t) => (
            <ThreadRow key={t.id} thread={t} now={now} onOpen={() => onOpen(t.id)} />
          ))}
        </div>
      )}
    </div>
  );
}

function ThreadRow({ thread, now, onOpen }: { thread: Thread; now: number; onOpen: () => void }) {
  const live = useThreadLive(thread.id);
  const running = threadRunning(thread.running, live);
  return (
    <div className={rowStyles.row}>
      <div className={rowStyles.line}>
        <button type="button" className={rowStyles.body} onClick={onOpen}>
          <span className={rowStyles.title}>{thread.title}</span>
          {thread.preview && <span className={rowStyles.notes}>{thread.preview}</span>}
          <span className={rowStyles.meta}>
            {running && (
              <span className={rowStyles.badge} data-tone="accent">
                <span className={threadStyles.pulse} aria-hidden="true" />
                Working
              </span>
            )}
            <span className={rowStyles.badge}>{MODEL_LABEL[thread.model]}</span>
            {thread.origin === "renamon" && <span className={rowStyles.badge}>Started by Renamon</span>}
            {thread.updated_at && <span className={rowStyles.when}>{relativeTime(thread.updated_at, now)}</span>}
          </span>
          <ChevronIcon className={rowStyles.chevron} />
        </button>
      </div>
    </div>
  );
}
