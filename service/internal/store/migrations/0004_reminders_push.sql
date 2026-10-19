CREATE TABLE reminders (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    text          TEXT NOT NULL,
    at            TEXT NOT NULL, -- when it fires next, UTC RFC 3339
    start         TEXT NOT NULL, -- anchor of the repeat rule: the time the owner chose, unchanged by snoozes
    rrule         TEXT,
    status        TEXT NOT NULL CHECK (status IN ('scheduled', 'fired', 'done', 'cancelled')),
    last_fired_at TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE INDEX reminders_due ON reminders(at) WHERE status = 'scheduled';

-- Browsers that asked for web push, keyed by their push-service endpoint.
CREATE TABLE push_subscriptions (
    endpoint   TEXT PRIMARY KEY,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at TEXT NOT NULL
);
