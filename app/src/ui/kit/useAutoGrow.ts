import { useLayoutEffect, type RefObject } from "react";

// Grows a textarea with what's typed, up to `maxLines`, then scrolls.
export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, value: string, maxLines: number) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const fit = () => {
      el.style.height = "";
      const style = getComputedStyle(el);
      const extra = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth);
      const max = parseFloat(style.lineHeight) * maxLines + extra;
      const want = el.scrollHeight + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth);
      el.style.height = `${Math.min(want, max)}px`;
      el.style.overflowY = want > max ? "auto" : "hidden";
    };
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [ref, value, maxLines]);
}
