import type { Review, ReviewFinding, ReviewVerdict, Severity } from "../../data/model";
import { ChevronIcon } from "../icons";
import { sortFindings } from "./labels";
import styles from "./threads.module.css";

// A second model's review of her lab work, in the transcript where the owner would judge it:
// who reviewed and the verdict, then the summary, the findings (folded, worst first) and what
// checked out (folded). Running, failed and stopped reviews say so in a line.

const VERDICT_LABEL: Record<ReviewVerdict, string> = { looks_good: "Looks good", needs_changes: "Needs changes", blocked: "Blocked" };
const SEVERITY_LABEL: Record<Severity, string> = { high: "High", medium: "Medium", low: "Low" };
export function ReviewCard({ review }: { review: Review }) {
  const { status } = review;
  const findings = sortFindings(review.findings);
  const clean = status === "done" && findings.length === 0;
  const verdict = status === "done" && review.verdict ? review.verdict : undefined;

  return (
    <section className={styles.review} data-status={status} data-verdict={verdict} aria-label={review.round > 1 ? "Second opinion, re-check of her fixes" : "Second opinion"}>
      <header className={styles.reviewHead}>
        <span className={styles.reviewWho}>
          <span className={styles.reviewTitle}>Second opinion</span>
          {review.model && <code className={styles.reviewModel}>{review.model}</code>}
        </span>
        {review.round > 1 && <span className={styles.reviewRound}>Re-check of her fixes</span>}
        {verdict && (
          <span className={styles.verdict} data-verdict={verdict}>
            {VERDICT_LABEL[verdict]}
          </span>
        )}
      </header>

      {status === "running" && (
        <p className={styles.reviewLine} role="status">
          <span className={styles.dots} aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          Checking her work in the lab…
          {review.commands > 0 && <span className={styles.reviewMeta}>{commandsText(review.commands)} so far</span>}
        </p>
      )}
      {status === "stopped" && <p className={styles.reviewLine}>The review was stopped.</p>}
      {status === "failed" && (
        <>
          <p className={styles.reviewLine}>Couldn&rsquo;t review her work this time.</p>
          {review.error && <p className={styles.reviewError}>{review.error}</p>}
        </>
      )}

      {status === "done" && (
        <>
          {clean ? <p className={styles.reviewLine}>No problems found.</p> : null}
          {review.summary && <p className={styles.reviewSummary}>{review.summary}</p>}
          {findings.length > 0 && (
            <ol className={styles.findings}>
              {findings.map((f, i) => (
                <li key={i}>
                  <Finding finding={f} />
                </li>
              ))}
            </ol>
          )}
          {review.verified.length > 0 && (
            <details className={styles.checked}>
              <summary>
                <ChevronIcon aria-hidden="true" />
                <span>What checked out</span>
                <span className={styles.reviewMeta}>{review.verified.length}</span>
              </summary>
              <ul>
                {review.verified.map((v, i) => (
                  <li key={i}>{v}</li>
                ))}
              </ul>
            </details>
          )}
          {review.commands > 0 && <p className={styles.reviewMeta}>Ran {review.commands} read-only {review.commands === 1 ? "command" : "commands"} in the lab.</p>}
        </>
      )}
    </section>
  );
}

function commandsText(n: number): string {
  return `${n} ${n === 1 ? "command" : "commands"}`;
}

function Finding({ finding: f }: { finding: ReviewFinding }) {
  return (
    <details className={styles.finding} data-severity={f.severity}>
      <summary>
        <ChevronIcon aria-hidden="true" />
        <span className={styles.severity} data-severity={f.severity}>
          {SEVERITY_LABEL[f.severity]}
        </span>
        <span className={styles.findingTitle}>{f.title}</span>
      </summary>
      <div className={styles.findingBody}>
        {f.unverified && (
          <p className={styles.unverified} title="Rests on what the reviewer knows, not on a command it ran.">
            Unverified: not checked in the lab
          </p>
        )}
        {f.claim && <Field label="Her claim">{f.claim}</Field>}
        {f.evidence && <Field label="What the lab showed">{f.evidence}</Field>}
        {f.fix && <Field label="Suggested fix">{f.fix}</Field>}
        {f.commands.length > 0 && (
          <div className={styles.field}>
            <span className={styles.fieldLabel}>Commands it cites</span>
            <ul className={styles.cmds}>
              {f.commands.map((c, i) => (
                <li key={i}>
                  <code>{c}</code>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </details>
  );
}

function Field({ label, children }: { label: string; children: string }) {
  return (
    <div className={styles.field}>
      <span className={styles.fieldLabel}>{label}</span>
      <p>{children}</p>
    </div>
  );
}
