import { useState } from "react";
import { canRetry, changeKindLabel, changeStatusLabel, changeStatusTone, changeSummary } from "../../data/changes";
import { useChanges, useIdeas, useNow, useRetryChange, useSetIdeaStatus } from "../../data/hooks";
import type { ChangeRequest, Idea } from "../../data/model";
import { relativeTime } from "../../data/time";
import { ChevronIcon } from "../icons";
import { Button, SectionTitle, StateMessage } from "../kit/kit";
import styles from "./changes.module.css";
import { useMenuActive } from "./MenuContext";

// What Renamon has proposed to change about Hekate herself, and how each one is going. Deciding
// happens on the approval card; this is where you follow it afterwards, and send a change that
// failed or was turned down back for another try. Below them, the smaller ideas she noted about
// herself (idea_log): notes to read, mark done or dismiss, nothing gets built from them.

function ChangeRow({ change, now, open, onToggle }: { change: ChangeRequest; now: number; open: boolean; onToggle: () => void }) {
  const summary = changeSummary(change);
  const when = change.finished_at ?? change.updated_at ?? change.created_at;
  const retry = useRetryChange();
  const retryable = canRetry(change);
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
      {(change.issue_url || change.pr_url || retryable) && (
        <div className={styles.links}>
          {retryable && (
            <Button
              compact
              busy={retry.isPending}
              disabled={retry.isPending}
              onClick={() => retry.mutate(change.id)}
              aria-label={`Try “${change.title}” again`}
            >
              Try again
            </Button>
          )}
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
      {retry.isError && (
        <p className={styles.error} role="alert">
          Couldn't send it back: {retry.error.message}
        </p>
      )}
      {open && (
        <div className={styles.detail}>
          <h3>What</h3>
          <p>{change.what || "No details."}</p>
          <h3>Why</h3>
          <p>{change.why || "No reason given."}</p>
          {change.previous_result && (
            <>
              <h3>Last attempt</h3>
              <p>{change.previous_result}</p>
              {change.previous_pr_url && (
                <a className={styles.inline} href={change.previous_pr_url} target="_blank" rel="noopener noreferrer">
                  Its pull request
                </a>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function IdeaRow({ idea, now }: { idea: Idea; now: number }) {
  const set = useSetIdeaStatus();
  const busy = set.isPending;
  return (
    <div className={styles.row}>
      <div className={styles.idea}>
        <span className={styles.title}>{idea.text}</span>
        <span className={styles.meta}>
          <span className={styles.kind}>{changeKindLabel(idea.kind)}</span>
          <span className={styles.when}>{relativeTime(idea.created_at, now)}</span>
        </span>
      </div>
      <div className={styles.links}>
        <Button compact busy={busy && set.variables?.status === "done"} disabled={busy} onClick={() => set.mutate({ id: idea.id, status: "done" })} aria-label={`Mark “${idea.text}” done`}>
          Done
        </Button>
        <Button compact disabled={busy} onClick={() => set.mutate({ id: idea.id, status: "dismissed" })} aria-label={`Dismiss “${idea.text}”`}>
          Dismiss
        </Button>
      </div>
      {set.isError && (
        <p className={styles.error} role="alert">
          Couldn't update it: {set.error.message}
        </p>
      )}
    </div>
  );
}

function Ideas({ now }: { now: number }) {
  const active = useMenuActive();
  const query = useIdeas(active);
  const ideas = query.data ?? [];
  if (query.isPending || (query.isError && !query.data) || ideas.length === 0) return null;
  return (
    <section>
      <SectionTitle>Her ideas</SectionTitle>
      <div className={styles.group}>
        {ideas.map((i) => (
          <IdeaRow key={i.id} idea={i} now={now} />
        ))}
      </div>
    </section>
  );
}

function Changes({ now }: { now: number }) {
  const active = useMenuActive();
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

export function ChangesSection() {
  const now = useNow();
  return (
    <div className={styles.stack}>
      <Changes now={now} />
      <Ideas now={now} />
    </div>
  );
}
