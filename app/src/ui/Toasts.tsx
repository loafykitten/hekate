import { useConversation, useConversationState } from "./ConversationContext";
import { BellIcon } from "./icons";
import type { Section } from "./menu/sections";
import styles from "./Toasts.module.css";

// Short notices at the top, out of the way: things that fired (reminders) first, then errors
// and info (mic problems, a dropped connection, a failed save).
export function Toasts({ onOpenSection }: { onOpenSection: (section: Section) => void }) {
  const conversation = useConversation();
  const { toasts, notices } = useConversationState();
  return (
    <div className={styles.stack} aria-live="polite">
      {notices.map((n) => (
        <div key={n.id} className={styles.toast} data-kind="notice" role="status">
          <span className={styles.bell} aria-hidden="true">
            <BellIcon />
          </span>
          <span className={styles.notice}>
            <strong>{n.title}</strong>
            {n.body && <span>{n.body}</span>}
          </span>
          {n.checkIn && (
            <button
              type="button"
              className={styles.action}
              onClick={() => {
                conversation.startCheckIn(undefined, true);
                conversation.dismissNotice(n.id);
              }}
            >
              Talk it over
            </button>
          )}
          {n.section && (
            <button
              type="button"
              className={styles.action}
              onClick={() => {
                onOpenSection(n.section!);
                conversation.dismissNotice(n.id);
              }}
            >
              View
            </button>
          )}
          <button type="button" className={styles.dismiss} aria-label="Dismiss" onClick={() => conversation.dismissNotice(n.id)}>
            &times;
          </button>
        </div>
      ))}
      {toasts.map((t) => (
        <div key={t.id} className={styles.toast} data-kind={t.kind} role={t.kind === "error" ? "alert" : "status"}>
          <span className={styles.text}>{t.text}</span>
          <button type="button" className={styles.dismiss} aria-label="Dismiss" onClick={() => conversation.dismissToast(t.key)}>
            &times;
          </button>
        </div>
      ))}
    </div>
  );
}
