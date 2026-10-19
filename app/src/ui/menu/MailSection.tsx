import { useEffect, useState } from "react";
import {
  useArchiveMessage,
  useDeleteMailRule,
  useMailMessage,
  useMailMessages,
  useMailRules,
  useMailStatus,
  useMailSummary,
  useMarkRead,
  useNow,
  useTrashMessage,
} from "../../data/hooks";
import { CATEGORY_LABEL, categoryLabel, filterCategories, mailWhenLong, senderName, totalUnread } from "../../data/mail";
import type { MailRule, MailStatus } from "../../data/model";
import { ApiError } from "../../data/rest";
import { relativeTime } from "../../data/time";
import { useConversation } from "../ConversationContext";
import { ArchiveIcon, BackIcon, TrashIcon } from "../icons";
import { Button, Chip, ConfirmButton, Segmented, SectionTitle, StateMessage, TextField } from "../kit/kit";
import { useDebounced } from "../kit/useDebounced";
import { MessageRow } from "../rows/rows";
import { useMenuActive } from "./MenuContext";
import { clearMailTarget, useMailTarget } from "./mailTarget";
import styles from "./sections.module.css";
import rowStyles from "../rows/rows.module.css";

type Tab = "messages" | "rules";

// Her view of your mail: what needs you, the inbox by category, one message at a time, and the
// rules she has set up. Mail itself stays on the mail server; bodies are fetched when opened.
export function MailSection() {
  const active = useMenuActive();
  const status = useMailStatus(active);
  const [tab, setTab] = useState<Tab>("messages");
  const [openId, setOpenId] = useState<string>();

  // A card asked for a message: show it, whether this section was already mounted or not.
  const target = useMailTarget();
  const [handled, setHandled] = useState<unknown>();
  if (target && target !== handled) {
    setHandled(target);
    setOpenId(target.id);
    setTab("messages");
  }
  useEffect(() => {
    if (target) clearMailTarget(target);
  }, [target]);

  if (status.isError && !status.data) {
    return (
      <StateMessage kind="error" title="Couldn't load mail" onRetry={() => void status.refetch()}>
        {status.error.message}
      </StateMessage>
    );
  }
  if (!status.data) return <StateMessage kind="loading" />;
  if (!status.data.configured) {
    return (
      <StateMessage kind="empty" title="Mail isn't set up yet">
        Once a mail account is added on the homelab, Renamon can sort your inbox, flag what needs a reply and tidy up. Nothing to show until then.
      </StateMessage>
    );
  }

  if (openId) return <MessageView id={openId} onBack={() => setOpenId(undefined)} />;
  return (
    <div className={styles.stack}>
      <AccountProblems status={status.data} />
      <div>
        <Segmented
          label="Mail view"
          value={tab}
          onChange={setTab}
          options={[
            { value: "messages", label: "Messages" },
            { value: "rules", label: "Rules" },
          ]}
        />
      </div>
      {tab === "messages" ? <Messages active={active} multiAccount={status.data.accounts.length > 1} onOpen={setOpenId} /> : <Rules active={active} />}
    </div>
  );
}

function AccountProblems({ status }: { status: MailStatus }) {
  const broken = status.accounts.filter((a) => !a.ok);
  if (broken.length === 0) return null;
  return (
    <div className={styles.stack} style={{ gap: "var(--space-2)" }}>
      {broken.map((a) => (
        <p key={a.name} className={styles.callout} data-tone="danger" role="alert">
          <strong>Can&rsquo;t reach {a.name}.</strong> The mail server said: {(a.error || "no answer").replace(/[.\s]+$/, "")}. What you see for it may be out of date.
        </p>
      ))}
    </div>
  );
}

// --- messages ------------------------------------------------------------------------------

function Messages({ active, multiAccount, onOpen }: { active: boolean; multiAccount: boolean; onOpen: (id: string) => void }) {
  const now = useNow();
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState("");
  const q = useDebounced(search.trim(), 250);
  const summary = useMailSummary(active);
  const messages = useMailMessages({ q, category, account: "" }, active);
  const unread = summary.data?.unread ?? {};
  const attention = summary.data?.attention ?? [];
  const items = messages.data ?? [];
  const filtered = q !== "" || category !== "";

  return (
    <>
      {!filtered && attention.length > 0 && (
        <section>
          <SectionTitle>Needs your attention</SectionTitle>
          <div className={styles.group}>
            {attention.map((m) => (
              <MessageRow key={m.id} message={m} now={now} showAccount={multiAccount} onOpen={() => onOpen(m.id)} />
            ))}
          </div>
        </section>
      )}

      <section>
        <SectionTitle>{category ? CATEGORY_LABEL[category] ?? categoryLabel(category) : q ? "Search results" : "Inbox"}</SectionTitle>
        <div className={styles.stack} style={{ gap: "var(--space-3)" }}>
          <TextField label="Search mail" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Sender, subject or summary" enterKeyHint="search" autoComplete="off" />
          <div className={styles.chips} role="group" aria-label="Category">
            <Chip selected={category === ""} count={totalUnread(unread) || undefined} onClick={() => setCategory("")}>
              All
            </Chip>
            {filterCategories(unread).map((c) => (
              <Chip key={c} selected={category === c} count={unread[c] || undefined} onClick={() => setCategory(c)}>
                {CATEGORY_LABEL[c] ?? categoryLabel(c)}
              </Chip>
            ))}
          </div>
          {messages.isError && !messages.data ? (
            <StateMessage kind="error" title="Couldn't load mail" onRetry={() => void messages.refetch()}>
              {messages.error.message}
            </StateMessage>
          ) : messages.isPending ? (
            <StateMessage kind="loading" />
          ) : items.length === 0 ? (
            <StateMessage kind="empty" title={filtered ? "No matches" : "Inbox zero"}>
              {filtered ? "Try different words or another category." : "Nothing in the inbox right now."}
            </StateMessage>
          ) : (
            <div className={styles.group}>
              {items.map((m) => (
                <MessageRow key={m.id} message={m} now={now} showAccount={multiAccount} showCategory={!category} onOpen={() => onOpen(m.id)} />
              ))}
            </div>
          )}
        </div>
      </section>
    </>
  );
}

// --- one message ---------------------------------------------------------------------------

function MessageView({ id, onBack }: { id: string; onBack: () => void }) {
  const now = useNow();
  const conversation = useConversation();
  const detail = useMailMessage(id);
  const archive = useArchiveMessage();
  const trash = useTrashMessage();
  const mark = useMarkRead();
  const busy = archive.isPending || trash.isPending;

  const back = (
    <div>
      <Button variant="quiet" compact onClick={onBack}>
        <BackIcon />
        Messages
      </Button>
    </div>
  );

  if (detail.isError && !detail.data) {
    const gone = detail.error instanceof ApiError && detail.error.status === 404;
    return (
      <div className={styles.stack}>
        {back}
        <StateMessage kind={gone ? "empty" : "error"} title={gone ? "This message is gone" : "Couldn't open this message"} onRetry={gone ? undefined : () => void detail.refetch()}>
          {gone ? "It was deleted or moved on the mail server. The list will catch up." : detail.error.message}
        </StateMessage>
      </div>
    );
  }
  if (!detail.data) {
    return (
      <div className={styles.stack}>
        {back}
        <StateMessage kind="loading" />
      </div>
    );
  }

  const { message, body } = detail.data;
  const toast = (text: string) => conversation.toast("info", text, "mail-action");
  return (
    <div className={styles.stack} style={{ gap: "var(--space-4)" }}>
      {back}
      <article className={styles.mailHead}>
        <h3 className={styles.mailSubject}>{message.subject || "(no subject)"}</h3>
        <p className={styles.mailFrom}>
          <strong>{senderName(message.from)}</strong>
          {message.from.name && message.from.address && <span>{message.from.address}</span>}
        </p>
        <p className={styles.mailMeta}>
          {[mailWhenLong(message.date, now), message.account, message.folder.toUpperCase() === "INBOX" ? "" : message.folder, message.category ? categoryLabel(message.category) : ""]
            .filter(Boolean)
            .join(" · ")}
        </p>
        {message.gist && (
          <p className={styles.callout}>
            <strong>In short:</strong> {message.gist}
          </p>
        )}
      </article>

      <div className={styles.actions} style={{ justifyContent: "flex-start" }}>
        <Button
          busy={archive.isPending}
          disabled={busy}
          onClick={() =>
            archive.mutate(id, {
              onSuccess: () => {
                toast("Archived.");
                onBack();
              },
            })
          }
        >
          <ArchiveIcon />
          Archive
        </Button>
        <Button
          variant="danger"
          busy={trash.isPending}
          disabled={busy}
          onClick={() =>
            trash.mutate(id, {
              onSuccess: () => {
                toast("Moved to Trash.");
                onBack();
              },
            })
          }
        >
          <TrashIcon />
          Trash
        </Button>
        <Button variant="quiet" busy={mark.isPending} disabled={busy} onClick={() => mark.mutate({ id, unread: !message.unread })}>
          {message.unread ? "Mark read" : "Mark unread"}
        </Button>
      </div>

      <section aria-label="Message">
        {body.text.trim() ? <pre className={styles.mailBody}>{body.text}</pre> : <p className={styles.callout}>There&rsquo;s no text in this message.</p>}
        {body.truncated && <p className={styles.mailNote}>This is the start of a long message.</p>}
      </section>
    </div>
  );
}

// --- rules ---------------------------------------------------------------------------------

function actionLabel(rule: MailRule): string {
  switch (rule.action) {
    case "archive":
      return "Archives";
    case "trash":
      return "Moves to Trash";
    case "mark_read":
      return "Marks as read";
    case "move":
      return rule.folder ? `Moves to ${rule.folder}` : "Moves";
    default:
      return rule.action;
  }
}

function Rules({ active }: { active: boolean }) {
  const now = useNow();
  const rules = useMailRules(active);
  const remove = useDeleteMailRule();
  const items = rules.data ?? [];
  return (
    <section>
      <SectionTitle>Rules</SectionTitle>
      <p className={styles.sectionNote}>Renamon applies these to new mail as it arrives.</p>
      {rules.isError && !rules.data ? (
        <StateMessage kind="error" title="Couldn't load rules" onRetry={() => void rules.refetch()}>
          {rules.error.message}
        </StateMessage>
      ) : rules.isPending ? (
        <StateMessage kind="loading" />
      ) : items.length === 0 ? (
        <StateMessage kind="empty" title="No rules yet">
          Ask Renamon to archive or tidy a sender and she&rsquo;ll offer a rule here.
        </StateMessage>
      ) : (
        <div className={styles.group}>
          {items.map((r) => (
            <div key={r.id} className={rowStyles.row}>
              <div className={rowStyles.line}>
                <div className={rowStyles.body}>
                  <span className={rowStyles.title}>{r.label}</span>
                  <span className={rowStyles.meta}>
                    <span className={rowStyles.badge}>{actionLabel(r)}</span>
                    <span className={rowStyles.when}>
                      {r.hits === 0 ? "Hasn't matched yet" : `${r.hits} ${r.hits === 1 ? "hit" : "hits"}${r.last_hit_at ? `, last ${relativeTime(r.last_hit_at, now)}` : ""}`}
                    </span>
                  </span>
                </div>
                <div className={styles.rowAction}>
                  <ConfirmButton
                    compact
                    label="Delete"
                    confirmLabel="Confirm"
                    describe={`rule ${r.label}`}
                    busy={remove.isPending && remove.variables === r.id}
                    onConfirm={() => remove.mutate(r.id)}
                  />
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
