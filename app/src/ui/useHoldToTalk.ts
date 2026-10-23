import { useEffect } from "react";
import type { Conversation } from "../conversation/conversation";
import { isHoldKey, startsHoldTalk } from "./keys";

// Hold P to talk, release to send. `target` is where the speech goes: a thread id, "main" for the
// voice stage, or undefined when something covers both (then the key does nothing).
export function useHoldToTalk(conversation: Conversation, target: string | undefined) {
  useEffect(() => {
    if (!target) return;
    let down = false;
    const onKeyDown = (e: KeyboardEvent) => {
      if (!startsHoldTalk(e)) return;
      e.preventDefault();
      if (down) return;
      down = conversation.holdTalk(target === "main" ? undefined : target);
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (!isHoldKey(e.key) || !down) return;
      down = false;
      conversation.releaseHold();
    };
    // The release never arrives once the window loses focus.
    const onBlur = () => {
      if (!down) return;
      down = false;
      conversation.cancelHold();
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", onBlur);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", onBlur);
      if (down) conversation.cancelHold();
    };
  }, [conversation, target]);
}
