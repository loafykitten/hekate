import { ChatIcon } from "./icons";
import styles from "./LogButton.module.css";

export function LogButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" className={styles.button} aria-label="Open conversation log" onClick={onClick}>
      <ChatIcon />
    </button>
  );
}
