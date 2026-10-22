import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { MAX_TEXT_LENGTH } from "../conversation/protocol";
import { useConversation, useConversationState } from "./ConversationContext";
import styles from "./Dock.module.css";
import { KeyboardIcon, MicIcon, SendIcon, SpeakerIcon, SpeakerOffIcon } from "./icons";
import { useKeyboardInset } from "./keyboardInset";
import { talkCopy, talkVerb } from "./talkCopy";
import { TalkButton } from "./TalkButton";

type Mode = "voice" | "text";
const MODE_KEY = "hekate.dockMode";
// Lines the message field grows to before it scrolls.
const MAX_LINES = 6;

function loadMode(): Mode {
  try {
    return localStorage.getItem(MODE_KEY) === "text" ? "text" : "voice";
  } catch {
    return "voice";
  }
}

function saveMode(mode: Mode) {
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch {
    // storage unavailable (private window, blocked): the mode just isn't remembered
  }
}

// Keys that belong to something else on the page.
function claimsSpace(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  const talk = target.closest("#talk-button");
  if (talk) return false;
  return !!target.closest("input, textarea, select, button, a, summary, [contenteditable], [role=dialog]");
}

interface Props {
  onOpenLog: () => void;
  logOpen: boolean;
}

export function Dock({ onOpenLog, logOpen }: Props) {
  const conversation = useConversation();
  const state = useConversationState();
  const [mode, setMode] = useState<Mode>(loadMode);
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const swipe = useRef<{ y: number; id: number }>(undefined);
  useKeyboardInset();

  // The message field grows with what's typed, so a long message stays readable. How much it grew
  // is published as --composer-grow, so what sits above the dock moves up with it.
  useLayoutEffect(() => {
    const root = document.documentElement;
    const el = inputRef.current;
    if (!el) {
      root.style.removeProperty("--composer-grow");
      return;
    }
    const fit = () => {
      el.style.height = "";
      const style = getComputedStyle(el);
      const border = parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth);
      const want = el.scrollHeight + border;
      const max = parseFloat(style.lineHeight) * MAX_LINES + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + border;
      const height = Math.min(want, max);
      el.style.height = `${height}px`;
      el.style.overflowY = want > max ? "auto" : "hidden";
      root.style.setProperty("--composer-grow", `${Math.max(0, height - parseFloat(style.minHeight))}px`);
    };
    fit();
    // A rotated or resized screen rewraps the draft.
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [draft, mode]);
  useEffect(() => () => void document.documentElement.style.removeProperty("--composer-grow"), []);

  function switchMode(next: Mode) {
    setMode(next);
    saveMode(next);
    if (next === "text") conversation.cancelCapture();
    requestAnimationFrame(() => (next === "text" ? inputRef.current : toggleRef.current)?.focus({ preventScroll: true }));
  }

  // Space works like the talk button on desktop (press to start, press again to end), unless a
  // text field or button has the keyboard.
  useEffect(() => {
    if (mode !== "voice" || logOpen) return;
    let down = false;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.code === "Escape") return conversation.interruptNow();
      if (e.code !== "Space" || e.repeat || e.ctrlKey || e.metaKey || e.altKey || claimsSpace(e.target)) return;
      e.preventDefault();
      if (down) return;
      down = true;
      conversation.pressTalk();
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.code !== "Space" || !down) return;
      down = false;
      e.preventDefault();
      conversation.releaseTalk();
    };
    const onBlur = () => {
      if (!down) return;
      down = false;
      conversation.releaseTalk();
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", onBlur);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", onBlur);
      if (down) conversation.releaseTalk();
    };
  }, [conversation, mode, logOpen]);

  const connected = state.connection === "open";
  const verb = useMemo(() => talkVerb(), []);
  const copy = talkCopy(state.phase, connected, state.capabilities?.stt, verb);
  const busy = state.phase === "thinking" || state.phase === "speaking";

  // Her voice on this device: off for using Hekate in public, by text.
  const voiceButton = (
    <button
      type="button"
      className={styles.sideButton}
      data-side="start"
      aria-label={state.voice ? "Turn her voice off" : "Turn her voice on"}
      onClick={() => {
        conversation.setVoice(!state.voice);
        conversation.toast("info", state.voice ? "Voice off. She'll answer in the bubble." : "Voice on.", "voice");
      }}
    >
      {state.voice ? <SpeakerIcon /> : <SpeakerOffIcon />}
    </button>
  );

  function send() {
    if (conversation.sendText(draft)) setDraft("");
    inputRef.current?.focus({ preventScroll: true });
  }

  return (
    <div className={styles.dock} data-mode={mode}>
      {/* Swiping up on the handle (or tapping it) opens the conversation log. */}
      <button
        type="button"
        className={styles.handle}
        aria-label="Open conversation log"
        onClick={onOpenLog}
        onPointerDown={(e) => (swipe.current = { y: e.clientY, id: e.pointerId })}
        onPointerMove={(e) => {
          if (swipe.current?.id === e.pointerId && swipe.current.y - e.clientY > 28) {
            swipe.current = undefined;
            onOpenLog();
          }
        }}
        onPointerUp={() => (swipe.current = undefined)}
        onPointerCancel={() => (swipe.current = undefined)}
      >
        <span aria-hidden="true" />
      </button>

      {mode === "voice" ? (
        <div className={styles.voiceRow}>
          {voiceButton}
          <TalkButton phase={state.phase} latched={state.latched} disabled={copy.disabled} label={copy.label} />
          <button
            ref={toggleRef}
            type="button"
            className={styles.sideButton}
            aria-label="Type instead"
            onClick={() => switchMode("text")}
          >
            <KeyboardIcon />
          </button>
        </div>
      ) : (
        <form
          className={styles.textRow}
          onSubmit={(e) => {
            e.preventDefault();
            send();
          }}
        >
          <button type="button" className={styles.sideButton} aria-label="Talk instead" onClick={() => switchMode("voice")}>
            <MicIcon />
          </button>
          {voiceButton}
          <textarea
            ref={inputRef}
            className={styles.input}
            rows={1}
            value={draft}
            maxLength={MAX_TEXT_LENGTH}
            placeholder={connected ? "Say something…" : "Offline"}
            enterKeyHint="send"
            autoComplete="off"
            autoCorrect="on"
            aria-label="Message"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                switchMode("voice");
              } else if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                // Enter sends; Shift+Enter starts a new line.
                e.preventDefault();
                send();
              }
            }}
          />
          <button type="submit" className={styles.send} aria-label="Send" disabled={!connected || !draft.trim()}>
            <SendIcon />
          </button>
        </form>
      )}

      <p className={styles.hint} aria-live="polite">
        {mode === "voice" ? copy.hint : busy ? (state.phase === "thinking" ? "Thinking…" : "Speaking · send to interrupt") : ""}
      </p>
    </div>
  );
}
