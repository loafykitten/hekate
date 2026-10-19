import { useState } from "react";
import { changeKindLabel, changeStatusLabel, changeStatusTone, changeSummary } from "../../data/changes";
import { useChanges, useNow } from "../../data/hooks";
import type { ChangeRequest } from "../../data/model";
import { relativeTime } from "../../data/time";
import { ChevronIcon } from "../icons";
import { StateMessage } from "../kit/kit";
import styles from "./changes.module.css";
import { useMenuActive } from "./MenuContext";

// What Renamon has proposed to change about Hekate herself, and how each one is going. Deciding
// happens on the approval card; this is where you follow it afterwards.

function ChangeRow({ change, now, open, onToggle }: { change: ChangeRequest; now: number; open: boolean; onToggle: () => void }) {
  const summary = changeSummary(change);
  const when = change.finished_at ?? change.updated_at ?? change.created_at;
  return (
    <div className={styles.row} data-expanded={open || undefined}>
      <button type="button" className={styles.head} aria-expanded={open} onClick={onToggle}>
        <span className={styles.title}>{change.title}</span>
        <span className={styles.meta}>
          <span className={styles.chip} data-tone={changeStatusTone(change.status)}>
            {changeStatusLabel(change.status)}
          </span>
          <span className={styles.kind}>{changeKindLabel(change.kind)}</span>
          {when && <span className={styles.when}>{relativeTime(when, now)}</span>}
        </span>
        {summary && <span className={styles.summary}>{summary}</span>}
        <ChevronIcon className={styles.chevron} />
      </button>
      {(change.issue_url || change.pr_url) && (
        <div className={styles.links}>
          {change.issue_url && (
            <a href={change.issue_url} target="_blank" rel="noopener noreferrer">
              Issue
            </a>
          )}
          {change.pr_url && (
            <a href={change.pr_url} target="_blank" rel="noopener noreferrer">
              Pull request
            </a>
          )}
        </div>
      )}
      {open && (
        <div className={styles.detail}>
          <h3>What</h3>
          <p>{change.what || "No details."}</p>
          <h3>Why</h3>
          <p>{change.why || "No reason given."}</p>
        </div>
      )}
    </div>
  );
}

export function ChangesSection() {
  const active = useMenuActive();
  const now = useNow();
  const [openId, setOpenId] = useState<string>();
  const query = useChanges(active);
  const changes = query.data ?? [];

  if (query.isError && !query.data) {
    return (
      <StateMessage kind="error" title="Couldn't load changes" onRetry={() => void query.refetch()}>
        {query.error.message}
      </StateMessage>
    );
  }
  if (query.isPending) return <StateMessage kind="loading" />;
  if (changes.length === 0) {
    return (
      <StateMessage kind="empty" title="No changes yet">
        When Renamon proposes a change to herself, it shows up here and you can follow it from approval to merge.
      </StateMessage>
    );
  }
  return (
    <div className={styles.group}>
      {changes.map((c) => (
        <ChangeRow key={c.id} change={c} now={now} open={openId === c.id} onToggle={() => setOpenId(openId === c.id ? undefined : c.id)} />
      ))}
    </div>
  );
}
