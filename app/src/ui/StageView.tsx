import { useEffect, useRef, useState } from "react";
import { Stage } from "../character/stage";
import styles from "./StageView.module.css";

type Status = { state: "loading" } | { state: "ready" } | { state: "error"; message: string };

interface Props {
  // Called with the stage once the character is loaded, and with undefined when it goes away.
  onStageChange?: (stage: Stage | undefined) => void;
}

// Owns the canvas and the Stage lifecycle. The canvas is created inside the effect, so a
// StrictMode remount (or a retry) gets a fresh canvas and WebGL context instead of reusing one
// the previous Stage disposed.
export function StageView({ onStageChange }: Props) {
  const rootRef = useRef<HTMLDivElement>(null);
  const bubbleRef = useRef<HTMLDivElement>(null);
  const onStageChangeRef = useRef(onStageChange);
  const [attempt, setAttempt] = useState(0);
  const [status, setStatus] = useState<Status>({ state: "loading" });

  useEffect(() => {
    onStageChangeRef.current = onStageChange;
  });

  useEffect(() => {
    const root = rootRef.current!;
    const canvas = document.createElement("canvas");
    canvas.id = "stage";
    canvas.className = styles.canvas;
    root.prepend(canvas);

    let stage: Stage | undefined;
    let cancelled = false;
    // Construction (which creates the WebGL context) can throw; treat that like a failed load.
    new Promise<Stage>((resolve) => {
      stage = new Stage(canvas, new URLSearchParams(location.search), bubbleRef.current ?? undefined);
      resolve(stage);
    })
      .then(async (s) => {
        await s.load();
        if (cancelled) return;
        setStatus({ state: "ready" });
        onStageChangeRef.current?.(s);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        console.error(err);
        setStatus({ state: "error", message: err instanceof Error ? err.message : String(err) });
      });

    return () => {
      cancelled = true;
      onStageChangeRef.current?.(undefined);
      stage?.dispose();
      canvas.remove();
    };
  }, [attempt]);

  function retry() {
    setStatus({ state: "loading" });
    setAttempt((n) => n + 1);
  }

  return (
    <div ref={rootRef} className={styles.root} data-stage={status.state}>
      <div ref={bubbleRef} className={styles.bubble} hidden>
        {/* "bubble-text" is the hook the framework-free Bubble class looks up. */}
        <div className={`${styles.bubbleText} bubble-text`} />
      </div>
      {status.state === "loading" && (
        <div className={styles.notice} role="status">
          <span className={styles.pulse} aria-hidden="true" />
          Waking Hekate
        </div>
      )}
      {status.state === "error" && (
        <div className={styles.notice} role="alert">
          <p className={styles.title}>Hekate couldn&rsquo;t be loaded</p>
          <p className={styles.detail}>{status.message}</p>
          <button type="button" className={styles.retry} onClick={retry}>
            Try again
          </button>
        </div>
      )}
    </div>
  );
}
