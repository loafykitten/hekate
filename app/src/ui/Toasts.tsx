import { useConversation, useConversationState } from "./ConversationContext";
import styles from "./Toasts.module.css";

// Short notices (errors, mic problems, a dropped connection) at the top, out of the way.
export function Toasts() {
  const conversation = useConversation();
  const { toasts } = useConversationState();
  return (
    <div className={styles.stack} aria-live="polite">
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
