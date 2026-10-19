import { useEffect, useRef } from "react";

// Focuses the first field of a freshly opened form.
export function useAutoFocus<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  return ref;
}
