-- Actions waiting for the owner's say-so, and what became of them. payload is
-- opaque to this table: the registered executor for kind reads it.
CREATE TABLE approvals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,
    title       TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    scope       TEXT NOT NULL DEFAULT '', -- what an "always" rule covers; empty when it can't become one
    scope_label TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT 'null',
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed', 'denied', 'expired')),
    result      TEXT, -- after running: what happened, or the error
    decided_by  TEXT CHECK (decided_by IN ('owner', 'rule')),
    created_at  TEXT NOT NULL, -- UTC RFC 3339
    expires_at  TEXT NOT NULL,
    decided_at  TEXT
);
CREATE INDEX approvals_pending ON approvals(expires_at) WHERE status = 'pending';
CREATE INDEX approvals_decided ON approvals(decided_at) WHERE decided_at IS NOT NULL;

-- Standing "always allow" decisions.
CREATE TABLE approval_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,
    scope       TEXT NOT NULL,
    scope_label TEXT NOT NULL,
    uses        INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    UNIQUE (kind, scope)
);
