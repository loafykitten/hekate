-- Telegram messages that carry an approval, so a reply to one can deny it with a reason.
CREATE TABLE telegram_approvals (
    message_id  INTEGER PRIMARY KEY,
    approval_id INTEGER NOT NULL,
    created_at  TEXT NOT NULL -- UTC RFC 3339
);
