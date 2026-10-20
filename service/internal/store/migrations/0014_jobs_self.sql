-- Scheduled background work (reflection, heartbeat, briefing): one row per run.
CREATE TABLE job_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job         TEXT NOT NULL,
    status      TEXT NOT NULL, -- running, ok, failed
    summary     TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL, -- UTC RFC 3339
    finished_at TEXT
);
CREATE INDEX job_runs_job ON job_runs(job, started_at);

-- LLM requests made outside a conversation turn (mail classification, jobs).
CREATE TABLE background_usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source        TEXT NOT NULL, -- mail, reflection, briefing…
    model         TEXT NOT NULL,
    tier          TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL, -- NULL when the provider did not report a cost
    created_at    TEXT NOT NULL
);
CREATE INDEX background_usage_created ON background_usage(created_at);

-- Improvement ideas Renamon notes about herself (idea_log).
CREATE TABLE ideas (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT NOT NULL,
    kind       TEXT NOT NULL, -- tool, fix, improvement, other
    status     TEXT NOT NULL DEFAULT 'open', -- open, done, dismissed
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX ideas_status ON ideas(status, id);
