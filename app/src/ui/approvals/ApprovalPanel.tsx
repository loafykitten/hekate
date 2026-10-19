import { useEffect, useRef, useState } from "react";
import { useDecideApproval } from "../../data/hooks";
import type { Approval, ApprovalStatus } from "../../data/model";
import { ApiError, MAX_DENY_NOTE, type ApprovalDecision } from "../../data/rest";
import { Button, TextArea } from "../kit/kit";
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

// A detail longer than this folds behind "More" (a change request's what and why can run long).
const LONG_DETAIL_LINES = 5;
const LONG_DETAIL_CHARS = 320;

function isLong(detail: string): boolean {
  return detail.length > LONG_DETAIL_CHARS || detail.split("\n").length > LONG_DETAIL_LINES;
}

export function ApprovalPanel({ approval }: { approval: Approval }) {
  const decide = useDecideApproval();
  const pending = approval.status === "pending";
  const sending = decide.isPending;
  const choice = decide.variables?.decision;
  const raced = decide.error instanceof ApiError && decide.error.status === 409;

  // Deny asks for an optional reason first, right where the buttons were.
  const [denying, setDenying] = useState(false);
  const [note, setNote] = useState("");
  const [more, setMore] = useState(false);
  const noteField = useRef<HTMLTextAreaElement>(null);
  const denyForm = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (!denying) return;
    noteField.current?.focus({ preventScroll: true });
    // Keep Deny and Back in sight: a long detail can push them below the card's fold.
    denyForm.current?.scrollIntoView?.({ block: "nearest" }); // absent in jsdom
  }, [denying]);
  const long = isLong(approval.detail);

  function send(decision: ApprovalDecision, text?: string) {
    decide.mutate({ id: approval.id, decision, note: text });
  }

  const outcome = approval.status === "pending" ? undefined : OUTCOME[approval.status];
  const scope = approval.scope_label;
  return (
    <div className={styles.panel} data-status={approval.status}>
      <p className={styles.title}>{approval.title}</p>
      {approval.detail && (
        <>
          <p className={styles.detail} data-clamped={(long && !more) || undefined}>
            {approval.detail}
          </p>
          {long && (
            <button type="button" className={styles.more} aria-expanded={more} onClick={() => setMore(!more)}>
              {more ? "Less" : "More"}
            </button>
          )}
        </>
      )}
      {pending && denying ? (
        <form
          ref={denyForm}
          className={styles.deny}
          onSubmit={(e) => {
            e.preventDefault();
            send("deny", note);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape" && !sending) {
              e.stopPropagation();
              setDenying(false);
            }
          }}
        >
          <TextArea
            ref={noteField}
            label="Why not? (optional)"
            rows={2}
            maxLength={MAX_DENY_NOTE}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="Renamon will read this"
            disabled={sending}
          />
          <div className={styles.buttons}>
            <Button compact variant="danger" type="submit" busy={sending} disabled={sending}>
              Deny
            </Button>
            <Button compact variant="quiet" disabled={sending} onClick={() => setDenying(false)}>
              Back
            </Button>
          </div>
        </form>
      ) : pending ? (
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
            <Button compact variant="quiet" disabled={sending} onClick={() => setDenying(true)}>
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
            {approval.status === "denied" && approval.note && <p className={styles.reason}>&ldquo;{approval.note}&rdquo;</p>}
          </div>
        )
      )}
      {raced && !pending && <p className={styles.scope}>Already answered on another device.</p>}
    </div>
  );
}
