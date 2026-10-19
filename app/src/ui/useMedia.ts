import { useSyncExternalStore } from "react";

// Subscribes to a CSS media query.
export function useMedia(query: string): boolean {
  return useSyncExternalStore(
    (notify) => {
      const mql = matchMedia(query);
      mql.addEventListener("change", notify);
      return () => mql.removeEventListener("change", notify);
    },
    () => matchMedia(query).matches,
    () => false,
  );
}

// The wide layout: cards in a column beside her, the menu as a side panel.
export const WIDE_QUERY = "(min-width: 900px)";
export const useWide = () => useMedia(WIDE_QUERY);
