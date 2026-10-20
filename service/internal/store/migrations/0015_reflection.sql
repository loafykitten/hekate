-- Nightly reflection: what it changed in her memories (each change can be
-- undone from the menu) and the diary entry it wrote.
CREATE TABLE reflections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    day          TEXT NOT NULL, -- the local date reflected on, YYYY-MM-DD
    window_start TEXT NOT NULL, -- UTC RFC 3339: the conversation it read
    window_end   TEXT NOT NULL,
    summary      TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
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

CREATE TABLE diary (
    day        TEXT PRIMARY KEY, -- local date, YYYY-MM-DD
    text       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
