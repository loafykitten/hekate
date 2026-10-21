import { useConversation, useConversationState } from "./ConversationContext";
import { BellIcon } from "./icons";
import type { Section } from "./menu/sections";
import styles from "./Toasts.module.css";

// Short notices at the top, out of the way: things that fired (reminders) first, then errors
// and info (mic problems, a dropped connection, a failed save). A notice that came with a card
// shows only while that card doesn't (the menu or log covers the cards, or it's gone): one or the
// other, not both.
export function Toasts({ onOpenSection, onCheckIn, cardsShown }: { onOpenSection: (section: Section) => void; onCheckIn: () => void; cardsShown: boolean }) {
  const conversation = useConversation();
  const state = useConversationState();
  const { toasts } = state;
  const notices = state.notices.filter((n) => !(cardsShown && n.cardId && state.cards.some((c) => !c.leaving && c.card.id === n.cardId)));
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
                onCheckIn();
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
