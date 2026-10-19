import { useEffect, useRef } from "react";
import type { Phase } from "../conversation/conversation";
import { useConversation } from "./ConversationContext";
import { MicIcon } from "./icons";
import styles from "./TalkButton.module.css";

interface Props {
  phase: Phase;
  latched: boolean;
  disabled: boolean;
  label: string;
}

// The big round push-to-talk button. Press and hold talks and release sends; a quick tap
// latches listening until the next tap. The ring follows the microphone level.
export function TalkButton({ phase, latched, disabled, label }: Props) {
  const conversation = useConversation();
  const ringRef = useRef<HTMLSpanElement>(null);

  // The level changes every frame; write it straight to the DOM instead of re-rendering.
  useEffect(() => {
    const ring = ringRef.current;
    if (!ring || phase !== "listening") return;
    let raf = 0;
    let shown = 0;
    const frame = () => {
      const target = conversation.micLevel();
      shown += (target - shown) * (target > shown ? 0.5 : 0.15);
      ring.style.setProperty("--level", shown.toFixed(3));
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);
    return () => {
      cancelAnimationFrame(raf);
      ring.style.setProperty("--level", "0");
    };
  }, [conversation, phase]);

  return (
    <span className={styles.wrap} data-phase={phase} data-latched={latched || undefined}>
      <span ref={ringRef} className={styles.ring} aria-hidden="true" />
      <span className={styles.halo} aria-hidden="true" />
      <button
        type="button"
        id="talk-button"
        className={styles.button}
        aria-disabled={disabled || undefined}
        aria-pressed={phase === "listening"}
        aria-label={label}
        onPointerDown={(e) => {
          if (e.button !== 0) return;
          e.currentTarget.setPointerCapture(e.pointerId);
          conversation.pressTalk();
        }}
        onPointerUp={() => conversation.releaseTalk()}
        onPointerCancel={() => conversation.abortTalk()}
        onContextMenu={(e) => e.preventDefault()}
      >
        <MicIcon width={30} height={30} />
      </button>
    </span>
  );
}
