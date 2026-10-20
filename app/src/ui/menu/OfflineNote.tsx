import { useQueryClient } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { useNow } from "../../data/hooks";
import { offlineNote, oldestUpdate } from "../../data/offline";
import { useConnection } from "../health";
import styles from "./Menu.module.css";

// While the service can't be reached, says the menu is showing what it last had, and from when.
export function OfflineNote() {
  const qc = useQueryClient();
  const offline = useConnection().kind === "offline";
  const now = useNow();
  const asOf = useSyncExternalStore(
    (notify) => qc.getQueryCache().subscribe(notify),
    () => oldestUpdate(qc.getQueryCache().findAll({ type: "active" })),
  );
  if (!offline || asOf === undefined) return null;
  return (
    <p className={styles.offlineNote} role="status">
      {offlineNote(asOf, now)}
    </p>
  );
}
