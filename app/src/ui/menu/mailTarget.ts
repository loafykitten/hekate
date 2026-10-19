import { useSyncExternalStore } from "react";

// "Open this message in the Mail section": a card sets it, the section picks it up (whether it
// was mounted already or is about to be) and clears it.

export interface MailTarget {
  id: string;
}

let target: MailTarget | undefined;
const listeners = new Set<() => void>();

function emit() {
  for (const l of [...listeners]) l();
}

export function setMailTarget(id: string) {
  target = { id };
  emit();
}

export function clearMailTarget(handled: MailTarget) {
  if (target !== handled) return;
  target = undefined;
  emit();
}

export function useMailTarget(): MailTarget | undefined {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => target,
  );
}
