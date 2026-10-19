import { MenuIcon } from "./icons";
import styles from "./LogButton.module.css";

// Top right, balancing the conversation button on the left.
export function MenuButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" className={`${styles.button} ${styles.right}`} aria-label="Open menu" aria-haspopup="dialog" onClick={onClick}>
      <MenuIcon />
    </button>
  );
}
