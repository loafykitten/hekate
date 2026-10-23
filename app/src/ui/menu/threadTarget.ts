import { useSyncExternalStore } from "react";

// "Open this thread in the Threads section": a notification, a push or a link sets it, the section
// picks it up (whether it was mounted already or is about to be) and clears it.

export interface ThreadTarget {
  id: string;
}

let target: ThreadTarget | undefined;
let active: string | undefined;
const listeners = new Set<() => void>();

function emit() {
  for (const l of [...listeners]) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function setThreadTarget(id: string) {
  target = { id };
  emit();
}

export function clearThreadTarget(handled: ThreadTarget) {
  if (target !== handled) return;
  target = undefined;
  emit();
}

export function useThreadTarget(): ThreadTarget | undefined {
  return useSyncExternalStore(subscribe, () => target);
}

// The thread whose view is open on screen, if any: where P (hold to talk) sends speech.
export function setActiveThread(id: string | undefined) {
  if (active === id) return;
  active = id;
  emit();
}

export function useActiveThread(): string | undefined {
  return useSyncExternalStore(subscribe, () => active);
}
