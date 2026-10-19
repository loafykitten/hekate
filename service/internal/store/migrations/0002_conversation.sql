CREATE TABLE conversations (
    id         TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);

CREATE TABLE turns (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    input_id        TEXT NOT NULL DEFAULT '',
    origin          TEXT NOT NULL DEFAULT '',
    user_text       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL, -- running, done, interrupted, error
    model           TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    ended_at        TEXT
);
CREATE INDEX turns_conversation ON turns(conversation_id, started_at);

CREATE TABLE messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    turn_id         TEXT NOT NULL REFERENCES turns(id),
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    text            TEXT NOT NULL DEFAULT '', -- what the user saw: no tags
    content         TEXT NOT NULL DEFAULT '', -- what the model saw or wrote, tags included
    tool_calls      TEXT NOT NULL DEFAULT '', -- JSON array on assistant messages that call tools
    tool_call_id    TEXT NOT NULL DEFAULT '', -- on tool messages
    model           TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);
CREATE INDEX messages_conversation ON messages(conversation_id, id);
CREATE INDEX messages_turn ON messages(turn_id);

CREATE TABLE usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id       TEXT NOT NULL REFERENCES turns(id),
    model         TEXT NOT NULL,
    tier          TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL, -- NULL when the provider did not report a cost
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    request_id    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX usage_turn ON usage(turn_id);
CREATE INDEX usage_created ON usage(created_at);

CREATE TABLE activity (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id     TEXT NOT NULL REFERENCES turns(id),
    tool        TEXT NOT NULL,
    effect      TEXT NOT NULL, -- read, local, external
    args        TEXT NOT NULL DEFAULT '', -- JSON, truncated to 2 KB
    result      TEXT NOT NULL DEFAULT '', -- short summary, never full mail or page bodies
    status      TEXT NOT NULL, -- ok, error
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);
CREATE INDEX activity_turn ON activity(turn_id);
CREATE INDEX activity_created ON activity(created_at);
