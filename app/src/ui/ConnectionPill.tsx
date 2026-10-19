import { useConnection, type ConnectionState } from "./health";
import styles from "./ConnectionPill.module.css";

function describe(c: ConnectionState): { label: string; detail?: string } {
  switch (c.kind) {
    case "checking":
      return { label: "Connecting" };
    case "connected":
      return { label: "Connected", detail: c.version || undefined };
    case "degraded":
      return { label: "Unwell", detail: c.version || undefined };
    case "offline":
      return { label: "Offline" };
  }
}

// Small status pill in the top corner: whether the Hekate service answers /healthz.
export function ConnectionPill() {
  const connection = useConnection();
  const { label, detail } = describe(connection);
  return (
    <div className={styles.pill} data-state={connection.kind} role="status" aria-live="polite">
      <span className={styles.dot} aria-hidden="true" />
      <span>{label}</span>
      {detail && <span className={styles.detail}>{detail}</span>}
    </div>
  );
}
