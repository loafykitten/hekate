import { useConversationState } from "./ConversationContext";
import styles from "./StatusArea.module.css";

// Above the dock: what you just said (briefly) and what she's doing while tools run.
export function StatusArea() {
  const { caption, activity } = useConversationState();
  return (
    <div className={styles.area}>
      {activity && (
        <div key={activity} className={styles.chip} role="status">
          <span className={styles.dots} aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          {activity}…
        </div>
      )}
      {caption && (
        <p key={caption.id} className={styles.caption}>
          {caption.text}
        </p>
      )}
    </div>
  );
}
