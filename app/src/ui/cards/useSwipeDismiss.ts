import { useRef, type PointerEvent } from "react";

// Drag a card away to dismiss it: up on phones (sideways scrolls the stack), right on wide
// screens. The card follows the finger and springs back if it isn't dragged far enough. A
// drag that starts on a control (button, link, field) is left alone, except the card's title,, and the click that ends a
// drag is swallowed so it doesn't also open the card.

const START_PX = 8;
const DISMISS_PX = 72;

interface Drag {
  id: number;
  x: number;
  y: number;
  active: boolean;
}

export function useSwipeDismiss(direction: "up" | "right", onDismiss: () => void) {
  const drag = useRef<Drag | undefined>(undefined);
  const swallowClick = useRef(false);

  const along = (dx: number, dy: number) => (direction === "up" ? -dy : dx);
  const across = (dx: number, dy: number) => (direction === "up" ? dx : dy);

  function place(el: HTMLElement, amount: number) {
    const clamped = Math.max(0, amount);
    el.style.transform = direction === "up" ? `translateY(${-clamped}px)` : `translateX(${clamped}px)`;
    el.style.opacity = String(Math.max(0.15, 1 - clamped / 220));
  }

  return {
    onPointerDown(e: PointerEvent<HTMLElement>) {
      if (e.pointerType === "mouse" && e.button !== 0) return;
      const control = (e.target as Element).closest("a, button, input, select, textarea, label, [data-no-swipe]");
      if (control && !control.hasAttribute("data-swipe-ok")) return;
      drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY, active: false };
    },
    onPointerMove(e: PointerEvent<HTMLElement>) {
      const d = drag.current;
      if (!d || d.id !== e.pointerId) return;
      const dx = e.clientX - d.x;
      const dy = e.clientY - d.y;
      if (!d.active) {
        if (Math.abs(across(dx, dy)) > START_PX && Math.abs(across(dx, dy)) > Math.abs(along(dx, dy))) {
          drag.current = undefined; // the gesture is scrolling the other way
          return;
        }
        if (along(dx, dy) < START_PX) return;
        d.active = true;
        e.currentTarget.setPointerCapture(e.pointerId);
        e.currentTarget.style.transition = "none";
      }
      place(e.currentTarget, along(dx, dy));
    },
    onPointerUp(e: PointerEvent<HTMLElement>) {
      const d = drag.current;
      drag.current = undefined;
      if (!d || d.id !== e.pointerId || !d.active) return;
      swallowClick.current = true;
      setTimeout(() => (swallowClick.current = false), 0);
      const el = e.currentTarget;
      el.style.transition = "";
      if (along(e.clientX - d.x, e.clientY - d.y) > DISMISS_PX) {
        place(el, 260);
        onDismiss();
      } else {
        el.style.transform = "";
        el.style.opacity = "";
      }
    },
    onPointerCancel(e: PointerEvent<HTMLElement>) {
      const d = drag.current;
      drag.current = undefined;
      if (!d?.active) return;
      e.currentTarget.style.transition = "";
      e.currentTarget.style.transform = "";
      e.currentTarget.style.opacity = "";
    },
    onClickCapture(e: React.MouseEvent<HTMLElement>) {
      if (swallowClick.current) {
        e.stopPropagation();
        e.preventDefault();
      }
    },
  };
}
