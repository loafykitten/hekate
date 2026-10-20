-- What the heartbeat already told the owner about, so it never nudges twice.
CREATE TABLE nudges (
    key     TEXT PRIMARY KEY, -- e.g. task:12:2026-10-20, mail:381, follow_up:5:<when>
    sent_at TEXT NOT NULL     -- UTC RFC 3339; rows older than 30 days are dropped
);
