-- Nightly reflection: what it changed in her memories (each change can be
-- undone from the menu) and the diary entry it wrote.
CREATE TABLE reflections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    day          TEXT NOT NULL, -- the local date reflected on, YYYY-MM-DD
    window_start TEXT NOT NULL, -- UTC RFC 3339: the conversation it read
    window_end   TEXT NOT NULL,
    summary      TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    finished_at  TEXT -- set once every change and the diary are written; the next one reads on from window_end
);
CREATE INDEX reflections_day ON reflections(day);

CREATE TABLE reflection_changes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    reflection_id INTEGER NOT NULL REFERENCES reflections(id) ON DELETE CASCADE,
    action        TEXT NOT NULL, -- save, update, retire
    memory_id     INTEGER NOT NULL,
    before        TEXT NOT NULL DEFAULT '', -- the memory as JSON before the change; '' for save
    after         TEXT NOT NULL DEFAULT '', -- and after it; '' for retire
    reason        TEXT NOT NULL DEFAULT '',
    undone_at     TEXT
);
CREATE INDEX reflection_changes_reflection ON reflection_changes(reflection_id);
CREATE INDEX reflection_changes_memory ON reflection_changes(memory_id);

-- When reflection merges memories, the ones retired into another. Forgetting any
-- memory of a merge forgets the whole group (see memory.Forget): each holds the others' words.
CREATE TABLE memory_merges (
    source_id INTEGER NOT NULL,
    into_id   INTEGER NOT NULL,
    PRIMARY KEY (source_id, into_id)
);
CREATE INDEX memory_merges_into ON memory_merges(into_id);

-- One-way fingerprints of forgotten memories' wording (never the text), so
-- reflection, which rereads the conversation they came from, can't save them again.
CREATE TABLE memory_forgotten (
    fingerprint  TEXT PRIMARY KEY, -- sha256 of the normalized content
    forgotten_at TEXT NOT NULL
);

-- Forgetting a memory for good also forgets the copies reflection kept of it.
CREATE TRIGGER memories_forget_reflection_copies AFTER DELETE ON memories BEGIN
    DELETE FROM reflection_changes WHERE memory_id = old.id;
    DELETE FROM memory_merges WHERE source_id = old.id OR into_id = old.id;
END;

CREATE TABLE diary (
    day        TEXT PRIMARY KEY, -- local date, YYYY-MM-DD
    text       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
