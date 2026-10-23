import { ChevronIcon } from "../icons";
import styles from "./threads.module.css";

// The tool calls behind a turn, folded under it: "7 steps", opening to what each one was and how
// it went.
export interface StepView {
  tool: string;
  label: string;
  state: "running" | "ok" | "error";
  summary?: string;
}

const STATE_LABEL = { running: "Working", ok: "Done", error: "Failed" } as const;

export function ThreadSteps({ steps, live }: { steps: StepView[]; live?: boolean }) {
  if (steps.length === 0) return null;
  const failed = steps.filter((s) => s.state === "error").length;
  return (
    <details className={styles.steps} data-live={live || undefined}>
      <summary>
        <ChevronIcon aria-hidden="true" />
        <span>
          {steps.length} {steps.length === 1 ? "step" : "steps"}
        </span>
        {failed > 0 && <span className={styles.stepsFailed}>{failed} failed</span>}
      </summary>
      <ol className={styles.stepList}>
        {steps.map((s, i) => (
          <li key={i} className={styles.step} data-state={s.state}>
            <span className={styles.stepLine}>
              <span className={styles.stepLabel}>{s.label}</span>
              {s.tool && s.tool !== s.label && <code className={styles.stepTool}>{s.tool}</code>}
              <span className={styles.stepState}>{STATE_LABEL[s.state]}</span>
            </span>
            {s.summary && <span className={styles.stepSummary}>{s.summary}</span>}
          </li>
        ))}
      </ol>
    </details>
  );
}
