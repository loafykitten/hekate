-- Per-account sync state for the INBOX.
CREATE TABLE mail_accounts (
    name          TEXT PRIMARY KEY, -- the account's label from the configuration
    uidvalidity   INTEGER NOT NULL DEFAULT 0, -- INBOX UIDVALIDITY the cache was built against; 0 before the first sync
    last_uid      INTEGER NOT NULL DEFAULT 0, -- highest INBOX UID already fetched
    last_sweep_at TEXT, -- UTC RFC 3339, last successful sweep
    last_error    TEXT NOT NULL DEFAULT '', -- why the last sweep failed; empty when it worked
    updated_at    TEXT NOT NULL
);

-- Header cache and classification. Bodies are never stored: they are fetched
-- from the server when she reads a message. A message is addressed by (account,
-- folder, uidvalidity, uid) on the server and by id here; a move updates the row
-- so the id stays valid.
CREATE TABLE mail_messages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    account           TEXT NOT NULL,
    folder            TEXT NOT NULL, -- where it is now; empty when it left the inbox for somewhere unknown
    uidvalidity       INTEGER NOT NULL,
    uid               INTEGER NOT NULL, -- 0 when the server did not say where a moved message went
    message_id        TEXT NOT NULL DEFAULT '', -- Message-ID header, to carry a row across a UIDVALIDITY reset
    from_name         TEXT NOT NULL DEFAULT '',
    from_addr         TEXT NOT NULL DEFAULT '', -- lower-cased
    subject           TEXT NOT NULL DEFAULT '',
    date              TEXT NOT NULL, -- UTC RFC 3339
    unread            INTEGER NOT NULL DEFAULT 1,
    category          TEXT CHECK (category IN ('needs_reply', 'important', 'personal', 'receipt', 'notification', 'newsletter', 'promo')),
    gist              TEXT,
    classified_at     TEXT,
    classify_attempts INTEGER NOT NULL DEFAULT 0,
    list_id           TEXT NOT NULL DEFAULT '',
    unsub_url         TEXT NOT NULL DEFAULT '', -- the https URL from List-Unsubscribe
    has_post          INTEGER NOT NULL DEFAULT 0, -- List-Unsubscribe-Post announced one-click (RFC 8058)
    unsubscribed_at   TEXT,
    rules_applied     INTEGER NOT NULL DEFAULT 0, -- rules have been run on it (or it is not new inbox mail)
    first_seen_at     TEXT NOT NULL,
    left_inbox_at     TEXT, -- set when it was moved out of the inbox or deleted
    gone              INTEGER NOT NULL DEFAULT 0 -- left the inbox and Hekate does not know where it is
);
CREATE UNIQUE INDEX mail_messages_server ON mail_messages(account, folder, uidvalidity, uid) WHERE uid > 0;
CREATE INDEX mail_messages_inbox ON mail_messages(account, date) WHERE folder = 'INBOX' AND gone = 0;
CREATE INDEX mail_messages_message_id ON mail_messages(account, message_id) WHERE message_id <> '';
CREATE INDEX mail_messages_sender ON mail_messages(from_addr);
CREATE INDEX mail_messages_unclassified ON mail_messages(account) WHERE classified_at IS NULL AND folder = 'INBOX' AND gone = 0;

-- Full-text index over sender, subject and gist, kept in sync by the triggers
-- below. External content: the text lives once, in mail_messages.
CREATE VIRTUAL TABLE mail_messages_fts USING fts5(
    from_name, from_addr, subject, gist,
    content = 'mail_messages',
    content_rowid = 'id',
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER mail_messages_ai AFTER INSERT ON mail_messages BEGIN
    INSERT INTO mail_messages_fts (rowid, from_name, from_addr, subject, gist)
    VALUES (new.id, new.from_name, new.from_addr, new.subject, COALESCE(new.gist, ''));
END;

CREATE TRIGGER mail_messages_ad AFTER DELETE ON mail_messages BEGIN
    INSERT INTO mail_messages_fts (mail_messages_fts, rowid, from_name, from_addr, subject, gist)
    VALUES ('delete', old.id, old.from_name, old.from_addr, old.subject, COALESCE(old.gist, ''));
END;

CREATE TRIGGER mail_messages_au AFTER UPDATE OF from_name, from_addr, subject, gist ON mail_messages BEGIN
    INSERT INTO mail_messages_fts (mail_messages_fts, rowid, from_name, from_addr, subject, gist)
    VALUES ('delete', old.id, old.from_name, old.from_addr, old.subject, COALESCE(old.gist, ''));
    INSERT INTO mail_messages_fts (rowid, from_name, from_addr, subject, gist)
    VALUES (new.id, new.from_name, new.from_addr, new.subject, COALESCE(new.gist, ''));
END;

-- Rules the sweep applies to new inbox mail.
CREATE TABLE mail_rules (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    match_from     TEXT, -- an address or "@domain"
    match_category TEXT CHECK (match_category IN ('needs_reply', 'important', 'personal', 'receipt', 'notification', 'newsletter', 'promo')),
    action         TEXT NOT NULL CHECK (action IN ('archive', 'trash', 'mark_read', 'move')),
    folder         TEXT, -- for move
    label          TEXT NOT NULL,
    hits           INTEGER NOT NULL DEFAULT 0,
    last_hit_at    TEXT,
    created_at     TEXT NOT NULL,
    CHECK (match_from IS NOT NULL OR match_category IS NOT NULL)
);

-- Rules proposed by the weekly noise report, so the same sender is not asked
-- about again while an answer is pending or after the owner said no.
CREATE TABLE mail_noise_proposals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sender      TEXT NOT NULL, -- an address or "@domain"
    approval_id INTEGER NOT NULL,
    proposed_at TEXT NOT NULL
);
CREATE INDEX mail_noise_sender ON mail_noise_proposals(sender, proposed_at);
