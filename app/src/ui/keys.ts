// Which keys belong to the thing that has the keyboard, so the page's own shortcuts (Space and P to
// talk) stay out of the way. Duck-typed on `closest` so it needs no DOM to test.

type Targetish = { closest?: (selector: string) => unknown } | null | undefined;

function closest(target: EventTarget | Targetish, selector: string): boolean {
  const el = target as Targetish;
  return typeof el?.closest === "function" && !!el.closest(selector);
}

// Space also presses buttons and follows links, and dialogs are full of both. The talk button is
// the exception: it is what Space is for.
export function claimsSpace(target: EventTarget | null): boolean {
  if (closest(target, "#talk-button")) return false;
  return closest(target, "input, textarea, select, button, a, summary, [contenteditable], [role=dialog]");
}

// A letter only matters to something you can type into.
export function claimsTyping(target: EventTarget | null): boolean {
  return closest(target, "input:not([type=checkbox], [type=radio], [type=button], [type=submit]), textarea, select, [contenteditable]");
}

export interface HoldKeyEvent {
  key: string;
  repeat: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  target: EventTarget | null;
}

export function isHoldKey(key: string): boolean {
  return key.toLowerCase() === "p";
}

// A press of P that starts holding-to-talk: bare P, not typing, not auto-repeat.
export function startsHoldTalk(e: HoldKeyEvent): boolean {
  return isHoldKey(e.key) && !e.repeat && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && !claimsTyping(e.target);
}
