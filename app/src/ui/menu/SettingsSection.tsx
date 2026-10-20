import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import {
  browserPushManager,
  currentPushEnvironment,
  disablePush,
  enablePush,
  isSubscribed,
  pushSupport,
  type PushDeps,
} from "../../data/push";
import { approvalKindLabel } from "../../data/approvals";
import { useApprovalRules, useDeleteApprovalRule, usePatchRoutines, useRoutines } from "../../data/hooks";
import { pushApi } from "../../data/rest";
import { useConversation, useConversationState } from "../ConversationContext";
import { useConnection } from "../health";
import { Button, ConfirmButton, SectionTitle, StateMessage, SwitchRow, TextField } from "../kit/kit";
import { useMenuActive } from "./MenuContext";
import styles from "./sections.module.css";
import rowStyles from "../rows/rows.module.css";

type Tone = "ok" | "warn" | "danger" | undefined;

function Pill({ tone, children }: { tone?: Tone; children: string }) {
  return (
    <span className={styles.pill} data-tone={tone}>
      {children}
    </span>
  );
}

const pushDeps: PushDeps = {
  permission: () => Notification.permission,
  requestPermission: () => Notification.requestPermission(),
  pushManager: browserPushManager,
  fetchKey: pushApi.key,
  postSubscription: pushApi.subscribe,
  deleteSubscription: pushApi.unsubscribe,
};

function Notifications() {
  const active = useMenuActive();
  const conversation = useConversation();
  const qc = useQueryClient();
  const support = useMemo(() => pushSupport(currentPushEnvironment()), []);

  const state = useQuery({
    queryKey: ["push", "state"],
    enabled: active && support.supported,
    staleTime: 0,
    queryFn: async () => ({ permission: Notification.permission, subscribed: await isSubscribed(pushDeps) }),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ["push"] });
  const enable = useMutation({
    mutationFn: () => enablePush(pushDeps),
    onSettled: refresh,
    onSuccess: () => conversation.toast("info", "Notifications are on.", "push-on"),
  });
  const disable = useMutation({
    mutationFn: () => disablePush(pushDeps),
    onSettled: refresh,
    onSuccess: () => conversation.toast("info", "Notifications are off on this device.", "push-off"),
  });
  const test = useMutation({
    mutationFn: pushApi.test,
    onSuccess: () => conversation.toast("info", "Test sent. It should arrive in a moment.", "push-test"),
  });

  const permission = state.data?.permission;
  const subscribed = state.data?.subscribed === true;
  let status: { label: string; tone: Tone };
  if (support.needsHomeScreen) status = { label: "Needs Home Screen", tone: "warn" };
  else if (!support.supported) status = { label: "Unavailable", tone: undefined };
  else if (permission === "denied") status = { label: "Blocked", tone: "danger" };
  else if (subscribed) status = { label: "On", tone: "ok" };
  else status = { label: "Off", tone: undefined };

  const canAsk = support.supported && !support.needsHomeScreen && permission !== "denied";
  return (
    <section>
      <SectionTitle>Notifications</SectionTitle>
      <div className={styles.group}>
        <div className={styles.settingRow}>
          <div className={styles.settingLabel}>
            <strong>Push notifications</strong>
            <span>Reminders reach this device even when Hekate is closed.</span>
          </div>
          <Pill tone={status.tone}>{status.label}</Pill>
        </div>
        {canAsk && (
          <div className={styles.settingActions}>
            {subscribed ? (
              <>
                <Button variant="primary" busy={test.isPending} onClick={() => test.mutate()}>
                  Send a test
                </Button>
                <Button busy={disable.isPending} onClick={() => disable.mutate()}>
                  Turn off
                </Button>
              </>
            ) : (
              <Button variant="primary" busy={enable.isPending || state.isPending} onClick={() => enable.mutate()}>
                Turn on notifications
              </Button>
            )}
          </div>
        )}
      </div>
      <div className={styles.stack} style={{ gap: "var(--space-3)", marginTop: "var(--space-3)" }}>
        {support.needsHomeScreen && (
          <p className={styles.callout} data-tone="accent">
            <strong>On iPhone and iPad, notifications need the Home Screen.</strong> Tap the Share button in Safari, choose Add to Home Screen, then open Hekate from the new icon and come back here.
          </p>
        )}
        {!support.supported && !support.needsHomeScreen && (
          <p className={styles.callout}>This browser can&rsquo;t receive push notifications. Reminders still appear here while Hekate is open.</p>
        )}
        {permission === "denied" && (
          <p className={styles.callout} data-tone="danger">
            Notifications are blocked for Hekate. Allow them in this browser&rsquo;s site settings, then turn them on here.
          </p>
        )}
        {enable.isError && permission !== "denied" && (
          <p className={styles.callout} data-tone="danger">
            {enable.error.message}
          </p>
        )}
      </div>
    </section>
  );
}

// What she may do without asking, because you chose "Always" once.
function StandingPermissions() {
  const active = useMenuActive();
  const rules = useApprovalRules(active);
  const remove = useDeleteApprovalRule();
  const items = rules.data ?? [];
  return (
    <section>
      <SectionTitle>Standing permissions</SectionTitle>
      {rules.isError && !rules.data ? (
        <StateMessage kind="error" title="Couldn't load permissions" onRetry={() => void rules.refetch()}>
          {rules.error.message}
        </StateMessage>
      ) : rules.isPending ? (
        <StateMessage kind="loading" />
      ) : items.length === 0 ? (
        <p className={styles.callout}>None yet. When you choose Always on a request, it shows up here and you can take it back any time.</p>
      ) : (
        <>
          <p className={styles.sectionNote}>Renamon does these without asking. Revoke one and she&rsquo;ll ask again.</p>
          <div className={styles.group}>
            {items.map((r) => (
              <div key={r.id} className={rowStyles.row}>
                <div className={rowStyles.line}>
                  <div className={rowStyles.body}>
                    <span className={rowStyles.title}>{approvalKindLabel(r.kind)}</span>
                    <span className={rowStyles.meta}>
                      <span className={rowStyles.badge} data-tone="accent">
                        {r.scope_label}
                      </span>
                      <span className={rowStyles.when}>{r.uses === 0 ? "Not used yet" : `Used ${r.uses} ${r.uses === 1 ? "time" : "times"}`}</span>
                    </span>
                  </div>
                  <div className={styles.rowAction}>
                    <ConfirmButton
                      compact
                      label="Revoke"
                      confirmLabel="Confirm"
                      describe={`${approvalKindLabel(r.kind)}: ${r.scope_label}`}
                      busy={remove.isPending && remove.variables === r.id}
                      onConfirm={() => remove.mutate(r.id)}
                    />
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </section>
  );
}

// Her proactive routines: check-in nudges and the waking hours they keep to.
function Routines() {
  const active = useMenuActive();
  const routines = useRoutines(active);
  const patch = usePatchRoutines();
  const st = routines.data;
  return (
    <section>
      <SectionTitle>Routines</SectionTitle>
      {routines.isError && !st ? (
        <StateMessage kind="error" title="Couldn't load routines" onRetry={() => void routines.refetch()}>
          {routines.error.message}
        </StateMessage>
      ) : !st ? (
        <StateMessage kind="loading" />
      ) : (
        <div className={styles.group}>
          <SwitchRow className={styles.settingRow} checked={st.heartbeat} onChange={(heartbeat) => patch.mutate({ heartbeat })}>
            <span className={styles.settingLabel}>
              <strong>Check-ins</strong>
              <span>She nudges you, once each, about an event you'll need to leave for, overdue tasks, mail that needs you and things worth following up.</span>
            </span>
          </SwitchRow>
          <div className={styles.settingRow} style={{ borderBottom: 0 }}>
            <div className={styles.settingLabel}>
              <strong>Waking hours</strong>
              <span>Check-ins only come between these times.</span>
            </div>
          </div>
          <div className={styles.pair} style={{ paddingBottom: "var(--space-3)" }}>
            <TextField label="From" type="time" value={st.waking_start} onChange={(e) => e.target.value && patch.mutate({ waking_start: e.target.value })} />
            <TextField label="Until" type="time" value={st.waking_end} onChange={(e) => e.target.value && patch.mutate({ waking_end: e.target.value })} />
          </div>
        </div>
      )}
      {patch.isError && (
        <p className={styles.callout} data-tone="danger" style={{ marginTop: "var(--space-3)" }}>
          {patch.error.message}
        </p>
      )}
    </section>
  );
}

function Voice() {
  const conversation = useConversation();
  const { capabilities, connection, voice } = useConversationState();
  const row = (title: string, detail: string, on: boolean | undefined) => (
    <div className={styles.settingRow}>
      <div className={styles.settingLabel}>
        <strong>{title}</strong>
        <span>{detail}</span>
      </div>
      <Pill tone={on === undefined ? undefined : on ? "ok" : "warn"}>{on === undefined ? "Unknown" : on ? "Ready" : "Not set up"}</Pill>
    </div>
  );
  return (
    <section>
      <SectionTitle>Voice</SectionTitle>
      <div className={styles.group}>
        <SwitchRow className={styles.settingRow} checked={voice} onChange={(on) => conversation.setVoice(on)}>
          <span className={styles.settingLabel}>
            <strong>Speak out loud</strong>
            <span>On this device. Off, she answers in the speech bubble with her gestures, and nothing plays. Handy in public.</span>
          </span>
        </SwitchRow>
        {row("Listening", "Tap or click the mic button to talk, and again when you're done. Needs speech recognition on the homelab.", capabilities?.stt)}
        {row("Her voice", "Without it she answers in the speech bubble only.", capabilities?.tts)}
      </div>
      {connection !== "open" && <p className={styles.callout} style={{ marginTop: "var(--space-3)" }}>Voice status appears once Hekate is connected.</p>}
    </section>
  );
}

function About() {
  const connection = useConnection();
  const label =
    connection.kind === "connected" ? "Running" : connection.kind === "degraded" ? "Running, but unwell" : connection.kind === "offline" ? "Not reachable" : "Checking…";
  const tone: Tone = connection.kind === "connected" ? "ok" : connection.kind === "degraded" ? "warn" : connection.kind === "offline" ? "danger" : undefined;
  const version = connection.kind === "connected" || connection.kind === "degraded" ? connection.version : "";
  return (
    <section>
      <SectionTitle>About</SectionTitle>
      <div className={styles.group}>
        <div className={styles.settingRow}>
          <div className={styles.settingLabel}>
            <strong>Hekate service</strong>
            <span>{version ? `Version ${version}` : "Version unknown"}</span>
          </div>
          <Pill tone={tone}>{label}</Pill>
        </div>
      </div>
    </section>
  );
}

export function SettingsSection() {
  return (
    <div className={styles.stack}>
      <Notifications />
      <StandingPermissions />
      <Routines />
      <Voice />
      <About />
    </div>
  );
}
