import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { MAX_TEXT_LENGTH } from "../../conversation/protocol";
import { useAutoGrow } from "../kit/useAutoGrow";
import { useConversation, useConversationState, useThreadLive } from "../ConversationContext";
import { useMenuActive } from "../menu/MenuContext";
import { MicIcon, SendIcon, StopIcon } from "../icons";
import styles from "./threads.module.css";

const MAX_LINES = 6;

interface Props {
  thread: string;
  running: boolean;
  transcribing: boolean;
}

// The thread's message field: Enter sends, Shift+Enter starts a new line. The mic works like the
// voice stage's talk button (tap to start, tap again to send), and P held down does the same.
export function ThreadComposer({ thread, running, transcribing }: Props) {
  const conversation = useConversation();
  const state = useConversationState();
  const live = useThreadLive(thread);
  const menuActive = useMenuActive();
  const [draft, setDraft] = useState("");
  // What was last sent, until the service has taken it: if it refuses, the words come back.
  const [sent, setSent] = useState<{ id: string; text: string }>();
  const [handledFail, setHandledFail] = useState<string>();
  if (live.failedInput && live.failedInput !== handledFail) {
    setHandledFail(live.failedInput);
    if (sent?.id === live.failedInput) {
      // Refused words come back; anything typed since stays, below them.
      setDraft(draft ? `${sent.text}\n\n${draft}` : sent.text);
      setSent(undefined);
    }
  }
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const ringRef = useRef<HTMLSpanElement>(null);
  useAutoGrow(inputRef, draft, MAX_LINES);

  const connected = state.connection === "open";
  const talk = state.threadTalk?.thread === thread ? state.threadTalk : undefined;
  const listening = !!talk;
  // The microphone is already in use for the voice stage.
  const micBusy = !listening && (state.phase === "listening" || state.phase === "starting");
  const micOff = !connected || (state.capabilities !== undefined && !state.capabilities.stt);

  // The level changes every frame; write it straight to the DOM instead of re-rendering.
  const ready = talk?.ready;
  useEffect(() => {
    const ring = ringRef.current;
    if (!ring || !ready) return;
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
  }, [conversation, ready]);

  // Leaving the thread, or closing the menu (its contents stay mounted), discards a recording.
  useEffect(() => {
    if (!menuActive) return;
    return () => conversation.cancelThreadCapture(thread);
  }, [conversation, thread, menuActive]);

  function send() {
    if (running) return; // the draft waits until she's done
    const id = conversation.sendThreadText(thread, draft);
    if (id) {
      setSent({ id, text: draft.trim() });
      setDraft("");
    }
    inputRef.current?.focus({ preventScroll: true });
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      send();
    }
  }

  // Space or Enter on the focused mic button behaves like a tap.
  const keyTap = (e: KeyboardEvent<HTMLButtonElement>) => {
    if ((e.key !== " " && e.key !== "Enter") || e.repeat) return;
    e.preventDefault();
    if (e.type === "keydown") conversation.pressTalk(thread);
    else conversation.releaseTalk();
  };

  const hint = listening
    ? talk.ready
      ? "Listening. Tap the mic again to send."
      : "Opening the microphone…"
    : transcribing
      ? "Sending what you said…"
      : running
        ? "She's working. Send your next message when she's done, or stop her."
        : "";

  return (
    <form
      className={styles.composer}
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
    >
      <p className={styles.composerHint} aria-live="polite">
        {hint}
      </p>
      <div className={styles.composerRow}>
        <span className={styles.micWrap} data-listening={listening || undefined}>
          <span ref={ringRef} className={styles.micRing} aria-hidden="true" />
          <button
            type="button"
            className={styles.sideButton}
            aria-label={listening ? "Send what you said" : "Talk to this thread"}
            aria-pressed={listening}
            disabled={micBusy || micOff || (running && !listening)}
            onPointerDown={(e) => {
              if (e.button !== 0) return;
              e.currentTarget.setPointerCapture(e.pointerId);
              conversation.pressTalk(thread);
            }}
            onPointerUp={() => conversation.releaseTalk()}
            onPointerCancel={() => conversation.abortTalk()}
            onKeyDown={keyTap}
            onKeyUp={keyTap}
            onContextMenu={(e) => e.preventDefault()}
          >
            <MicIcon />
          </button>
        </span>
        <textarea
          ref={inputRef}
          className={styles.input}
          rows={1}
          value={draft}
          maxLength={MAX_TEXT_LENGTH}
          placeholder={connected ? "Message this thread…" : "Offline"}
          enterKeyHint="send"
          autoComplete="off"
          autoCorrect="on"
          aria-label="Message"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
        />
        {running ? (
          <button type="button" className={styles.stop} aria-label="Stop her" title="Stop" onClick={() => conversation.stopThread(thread)}>
            <StopIcon />
          </button>
        ) : (
          <button type="submit" className={styles.send} aria-label="Send" disabled={!connected || !draft.trim()}>
            <SendIcon />
          </button>
        )}
      </div>
    </form>
  );
}
