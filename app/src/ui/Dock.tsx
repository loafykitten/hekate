import { useEffect, useMemo, useRef, useState } from "react";
import { MAX_TEXT_LENGTH } from "../conversation/protocol";
import { useConversation, useConversationState } from "./ConversationContext";
import styles from "./Dock.module.css";
import { KeyboardIcon, MicIcon, SendIcon, SpeakerIcon, SpeakerOffIcon } from "./icons";
import { useKeyboardInset } from "./keyboardInset";
import { talkCopy, talkVerb } from "./talkCopy";
import { TalkButton } from "./TalkButton";

type Mode = "voice" | "text";
const MODE_KEY = "hekate.dockMode";

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
  const inputRef = useRef<HTMLInputElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const swipe = useRef<{ y: number; id: number }>(undefined);
  useKeyboardInset();

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
          <input
            ref={inputRef}
            className={styles.input}
            type="text"
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
