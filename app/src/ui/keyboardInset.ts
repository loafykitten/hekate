import { useEffect } from "react";

// On iOS the on-screen keyboard covers fixed elements instead of resizing the page. Publishes
// how much of the bottom the keyboard hides as --keyboard-inset, so the dock can ride above it.
export function useKeyboardInset() {
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const root = document.documentElement;
    const update = () => {
      const hidden = Math.max(0, window.innerHeight - vv.height - vv.offsetTop);
      // Small differences are browser chrome, not a keyboard.
      root.style.setProperty("--keyboard-inset", hidden > 80 ? `${Math.round(hidden)}px` : "0px");
    };
    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
      root.style.removeProperty("--keyboard-inset");
    };
  }, []);
}
