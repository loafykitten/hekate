-- The always-loaded profile: short facts about the owner.
CREATE TABLE core_memory (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Atomic notes she keeps. deleted_at lets reflection retire a memory without
-- losing it; "forget" removes the row for real.
CREATE TABLE memories (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT NOT NULL CHECK (kind IN ('fact', 'preference', 'person', 'event', 'follow_up')),
    content        TEXT NOT NULL,
    importance     INTEGER NOT NULL DEFAULT 3 CHECK (importance BETWEEN 1 AND 5),
    follow_up_at   TEXT, -- UTC RFC 3339: when it is worth asking about
    source_turn_id TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    deleted_at     TEXT
);
CREATE INDEX memories_follow_up ON memories(follow_up_at) WHERE follow_up_at IS NOT NULL AND deleted_at IS NULL;

-- Full-text index over memories.content, kept in sync by the triggers below.
-- External content: the text lives once, in memories.
CREATE VIRTUAL TABLE memories_fts USING fts5(
    content,
    content = 'memories',
    content_rowid = 'id',
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts (rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER memories_au AFTER UPDATE OF content ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, content) VALUES ('delete', old.id, old.content);
    INSERT INTO memories_fts (rowid, content) VALUES (new.id, new.content);
END;
