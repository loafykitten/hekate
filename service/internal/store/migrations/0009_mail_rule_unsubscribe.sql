-- Mail rules can also unsubscribe (RFC 8058 one-click). SQLite can't change a
-- CHECK in place, so the table is rebuilt with the same rows, ids and id
-- sequence. An unsubscribe rule always names a sender.
CREATE TABLE mail_rules_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    match_from     TEXT, -- an address or "@domain"
    match_category TEXT CHECK (match_category IN ('needs_reply', 'important', 'personal', 'receipt', 'notification', 'newsletter', 'promo')),
    action         TEXT NOT NULL CHECK (action IN ('archive', 'trash', 'mark_read', 'move', 'unsubscribe')),
    folder         TEXT, -- for move
    label          TEXT NOT NULL,
    hits           INTEGER NOT NULL DEFAULT 0,
    last_hit_at    TEXT,
    created_at     TEXT NOT NULL,
    CHECK (match_from IS NOT NULL OR match_category IS NOT NULL),
    CHECK (action <> 'unsubscribe' OR match_from IS NOT NULL)
);

INSERT INTO mail_rules_new (id, match_from, match_category, action, folder, label, hits, last_hit_at, created_at)
    SELECT id, match_from, match_category, action, folder, label, hits, last_hit_at, created_at FROM mail_rules;

-- Keep ids of deleted rules from being handed out again.
DELETE FROM sqlite_sequence WHERE name = 'mail_rules_new';
INSERT INTO sqlite_sequence (name, seq) SELECT 'mail_rules_new', seq FROM sqlite_sequence WHERE name = 'mail_rules';

DROP TABLE mail_rules;
ALTER TABLE mail_rules_new RENAME TO mail_rules;
