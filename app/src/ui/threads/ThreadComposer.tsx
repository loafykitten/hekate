import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { MAX_TEXT_LENGTH } from "../../conversation/protocol";
import { useAutoGrow } from "../kit/useAutoGrow";
import { useConversation, useConversationState, useThreadLive } from "../ConversationContext";
import { useMenuActive } from "../menu/MenuContext";
import { formatSize, isImageFile, MAX_ATTACHMENTS } from "../../data/files";
import { CloseIcon, MicIcon, PaperclipIcon, SendIcon, StopIcon } from "../icons";
import { FileName, Thumb } from "./ThreadFiles";
import type { Attachment, Attachments } from "./useAttachments";
import styles from "./threads.module.css";

const MAX_LINES = 6;

interface Props {
  thread: string;
  attachments: Attachments;
  running: boolean;
  /** A second opinion on her work is being had: she's done, it's being checked. */
  reviewing?: boolean;
  transcribing: boolean;
}

// The thread's message field: Enter sends, Shift+Enter starts a new line. The mic works like the
// voice stage's talk button (tap to start, tap again to send), and P held down does the same.
export function ThreadComposer({ thread, attachments, running, reviewing = false, transcribing }: Props) {
  const conversation = useConversation();
  const state = useConversationState();
  const live = useThreadLive(thread);
  const menuActive = useMenuActive();
  const [draft, setDraft] = useState("");
  // What was last sent, until the service has taken it: if it refuses, the words come back.
  const [sent, setSent] = useState<{ id: string; text: string; files: Attachment[] }>();
  const [handledFail, setHandledFail] = useState<string>();
  if (live.failedInput && live.failedInput !== handledFail) {
    setHandledFail(live.failedInput);
    if (sent?.id === live.failedInput) {
      // Refused words come back; anything typed since stays, below them.
      setDraft(draft ? `${sent.text}\n\n${draft}` : sent.text);
      setSent(undefined);
      attachments.restore(sent.files);
    }
  }
  const fileRef = useRef<HTMLInputElement>(null);
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
    if (!attachments.ids) return; // files still going up, or one failed
    const id = conversation.sendThreadText(thread, draft, attachments.ids);
    if (id) {
      setSent({ id, text: draft.trim(), files: attachments.items });
      setDraft("");
      attachments.clear();
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

  const hasFiles = attachments.items.length > 0;
  const hint = listening
    ? talk.ready
      ? "Listening. Tap the mic again to send."
      : "Opening the microphone…"
    : transcribing
      ? "Sending what you said…"
      : reviewing
        ? "Her work is being checked. Send your next message when that's done, or stop it."
        : running
          ? "She's working. Send your next message when she's done, or stop her."
          : attachments.note ?? "";
  const placeholder = !connected ? "Offline" : hasFiles ? "Say what to do with it…" : "Message this thread…";

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
      {hasFiles && (
        <ul className={styles.chips} aria-label="Attached files">
          {attachments.items.map((a) => (
            <li key={a.key} className={styles.chip} data-status={a.status}>
              <div className={styles.chipMain}>
                {a.local && isImageFile(a.local) && <Thumb file={a.local} />}
                <FileName name={a.name} />
                <span className={styles.fileSize}>{a.status === "error" ? "Failed" : a.status === "uploading" ? `${Math.round(a.progress * 100)}%` : formatSize(a.size)}</span>
              </div>
              <button type="button" className={styles.chipRemove} aria-label={`Remove ${a.name}`} onClick={() => attachments.remove(a.key)}>
                <CloseIcon />
              </button>
              {a.status === "uploading" && (
                <span className={styles.chipBar} role="progressbar" aria-label={`Uploading ${a.name}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(a.progress * 100)}>
                  <i style={{ width: `${Math.round(a.progress * 100)}%` }} />
                </span>
              )}
              {a.status === "error" && (
                <p className={styles.chipError} role="alert">
                  {a.error}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
      <div className={styles.composerRow}>
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          tabIndex={-1}
          onChange={(e) => {
            attachments.add([...(e.target.files ?? [])]);
            e.target.value = ""; // the same file can be picked again
          }}
        />
        <button
          type="button"
          className={styles.sideButton}
          aria-label="Attach files (or paste an image)"
          title="Attach files (or paste an image)"
          disabled={!connected || attachments.items.length >= MAX_ATTACHMENTS}
          onClick={() => fileRef.current?.click()}
        >
          <PaperclipIcon />
        </button>
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
          placeholder={placeholder}
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
          <button type="submit" className={styles.send} aria-label="Send" disabled={!connected || !draft.trim() || !attachments.ids}>
            <SendIcon />
          </button>
        )}
      </div>
    </form>
  );
}
