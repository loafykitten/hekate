import { useEffect, useId, useState, type ButtonHTMLAttributes, type ComponentProps, type ReactNode } from "react";
import { CheckIcon } from "../icons";
import styles from "./kit.module.css";

// Small building blocks shared by the menu and the cards: buttons, form fields, chips and
// state messages. Every interactive piece has a 44 px target (the compact chips extend theirs).

type ButtonVariant = "primary" | "secondary" | "quiet" | "danger";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  compact?: boolean;
  busy?: boolean;
}

export function Button({ variant = "secondary", compact, busy, className, children, disabled, type = "button", ...rest }: ButtonProps) {
  return (
    <button
      type={type}
      className={[styles.btn, className].filter(Boolean).join(" ")}
      data-variant={variant}
      data-compact={compact || undefined}
      data-busy={busy || undefined}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      {...rest}
    >
      {children}
    </button>
  );
}

export function IconButton({ label, children, className, type = "button", ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button type={type} className={[styles.iconBtn, className].filter(Boolean).join(" ")} aria-label={label} title={label} {...rest}>
      {children}
    </button>
  );
}

// A two-step delete: the first press arms it, the second confirms; it disarms itself.
export function ConfirmButton({ label, confirmLabel = "Tap again to confirm", onConfirm, busy, icon }: {
  label: string;
  confirmLabel?: string;
  onConfirm: () => void;
  busy?: boolean;
  icon?: ReactNode;
}) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(t);
  }, [armed]);
  return (
    <Button
      variant="danger"
      busy={busy}
      data-armed={armed || undefined}
      onClick={() => {
        if (armed) onConfirm();
        else setArmed(true);
      }}
    >
      {icon}
      {armed ? confirmLabel : label}
    </Button>
  );
}

interface FieldShellProps {
  label: string;
  hint?: string;
  className?: string;
}

function useFieldIds() {
  const id = useId();
  return { id, hintId: `${id}-hint` };
}

export function TextField({ label, hint, className, ...rest }: FieldShellProps & ComponentProps<"input">) {
  const { id, hintId } = useFieldIds();
  return (
    <div className={[styles.field, className].filter(Boolean).join(" ")}>
      <label htmlFor={id}>{label}</label>
      <input id={id} aria-describedby={hint ? hintId : undefined} {...rest} />
      {hint && (
        <p id={hintId} className={styles.hint}>
          {hint}
        </p>
      )}
    </div>
  );
}

export function TextArea({ label, hint, className, ...rest }: FieldShellProps & ComponentProps<"textarea">) {
  const { id, hintId } = useFieldIds();
  return (
    <div className={[styles.field, className].filter(Boolean).join(" ")}>
      <label htmlFor={id}>{label}</label>
      <textarea id={id} rows={3} aria-describedby={hint ? hintId : undefined} {...rest} />
      {hint && (
        <p id={hintId} className={styles.hint}>
          {hint}
        </p>
      )}
    </div>
  );
}

export function SelectField({ label, hint, className, children, ...rest }: FieldShellProps & ComponentProps<"select">) {
  const { id, hintId } = useFieldIds();
  return (
    <div className={[styles.field, className].filter(Boolean).join(" ")}>
      <label htmlFor={id}>{label}</label>
      <select id={id} aria-describedby={hint ? hintId : undefined} {...rest}>
        {children}
      </select>
      {hint && (
        <p id={hintId} className={styles.hint}>
          {hint}
        </p>
      )}
    </div>
  );
}

// Labelled on/off switch.
export function Switch({ label, checked, onChange, className }: { label: string; checked: boolean; onChange: (v: boolean) => void; className?: string }) {
  return (
    <label className={[styles.switch, className].filter(Boolean).join(" ")}>
      <input type="checkbox" role="switch" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className={styles.track} aria-hidden="true" />
      <span>{label}</span>
    </label>
  );
}

// Round check target used by task rows and cards.
export function CheckCircle({ checked, label, onChange, disabled }: { checked: boolean; label: string; onChange: (checked: boolean) => void; disabled?: boolean }) {
  return (
    <label className={styles.check} data-checked={checked || undefined}>
      <input type="checkbox" checked={checked} disabled={disabled} aria-label={label} onChange={(e) => onChange(e.target.checked)} />
      <span className={styles.checkBox} aria-hidden="true">
        <CheckIcon />
      </span>
    </label>
  );
}

export function Chip({ selected, children, onClick, count }: { selected?: boolean; children: ReactNode; onClick: () => void; count?: number }) {
  return (
    <button type="button" className={styles.chip} data-selected={selected || undefined} aria-pressed={selected} onClick={onClick}>
      {children}
      {count !== undefined && <span className={styles.chipCount}>{count}</span>}
    </button>
  );
}

export function Segmented<T extends string>({ label, options, value, onChange }: {
  label: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div className={styles.segmented} role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} data-selected={o.value === value || undefined} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

// Loading, empty and error states, one voice.
export function StateMessage({ kind, title, children, onRetry }: { kind: "loading" | "empty" | "error"; title?: string; children?: ReactNode; onRetry?: () => void }) {
  return (
    <div className={styles.state} data-kind={kind} role={kind === "error" ? "alert" : "status"}>
      {kind === "loading" && <span className={styles.spinner} aria-hidden="true" />}
      {title && <p className={styles.stateTitle}>{title}</p>}
      {children && <p className={styles.stateText}>{children}</p>}
      {onRetry && (
        <Button onClick={onRetry} compact>
          Try again
        </Button>
      )}
    </div>
  );
}

export function SectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className={styles.sectionTitle}>
      <h3>{children}</h3>
      {action}
    </div>
  );
}
