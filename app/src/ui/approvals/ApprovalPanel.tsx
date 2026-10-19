import { useDecideApproval } from "../../data/hooks";
import type { Approval, ApprovalStatus } from "../../data/model";
import { ApiError, type ApprovalDecision } from "../../data/rest";
import { Button } from "../kit/kit";
import styles from "./approvals.module.css";

// Something she needs your say-so for. Pending, it asks; once decided it says what happened.
// Shared by the card on stage and the list on Today, so it works the same in both.

const OUTCOME: Record<Exclude<ApprovalStatus, "pending">, { label: string; tone?: "ok" | "danger" | "accent" }> = {
  running: { label: "Working on it…", tone: "accent" },
  done: { label: "Done", tone: "ok" },
  failed: { label: "Didn't work", tone: "danger" },
  denied: { label: "Denied" },
  expired: { label: "Expired" },
};

function outcomeNote(a: Approval): string {
  if (a.status === "expired") return "Nobody answered in time, so nothing was done.";
  if (a.status === "denied") return a.decided_by === "rule" ? "Refused by a standing permission." : "You said no, so nothing was done.";
  if (a.status === "running") return "Allowed. The action is running now.";
  const by = a.decided_by === "rule" ? "Allowed by a standing permission." : "";
  return [a.result, by].filter(Boolean).join(" ");
}

export function ApprovalPanel({ approval }: { approval: Approval }) {
  const decide = useDecideApproval();
  const pending = approval.status === "pending";
  const sending = decide.isPending;
  const choice = decide.variables?.decision;
  const raced = decide.error instanceof ApiError && decide.error.status === 409;

  function send(decision: ApprovalDecision) {
    decide.mutate({ id: approval.id, decision });
  }

  const outcome = approval.status === "pending" ? undefined : OUTCOME[approval.status];
  const scope = approval.scope_label;
  return (
    <div className={styles.panel} data-status={approval.status}>
      <p className={styles.title}>{approval.title}</p>
      {approval.detail && <p className={styles.detail}>{approval.detail}</p>}
      {pending ? (
        <>
          <div className={styles.buttons}>
            <Button compact variant="primary" busy={sending && choice === "once"} disabled={sending} onClick={() => send("once")}>
              Allow once
            </Button>
            {scope && (
              <Button compact busy={sending && choice === "always"} disabled={sending} onClick={() => send("always")} aria-label={`Always allow ${scope}`}>
                Always
              </Button>
            )}
            <Button compact variant="quiet" busy={sending && choice === "deny"} disabled={sending} onClick={() => send("deny")}>
              Deny
            </Button>
          </div>
          <p className={styles.scope}>{scope ? `Always covers ${scope}. You can undo it in Settings.` : "This one is asked each time."}</p>
        </>
      ) : (
        outcome && (
          <div className={styles.outcome} role="status">
            <span className={styles.badge} data-tone={outcome.tone}>
              {outcome.label}
            </span>
            <span className={styles.note}>{outcomeNote(approval)}</span>
          </div>
        )
      )}
      {raced && !pending && <p className={styles.scope}>Already answered on another device.</p>}
    </div>
  );
}
