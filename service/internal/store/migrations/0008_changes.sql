-- The owner's reason for denying an approval, when they gave one.
ALTER TABLE approvals ADD COLUMN note TEXT;

-- Changes Renamon proposed to Hekate itself. Until the owner allows its
-- approval a proposal reads its status from that approval (pending, denied,
-- expired); from then on state tracks the worker on the development machine.
CREATE TABLE change_requests (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT NOT NULL,
    what        TEXT NOT NULL,
    why         TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('feature', 'fix', 'improvement')),
    revises     INTEGER REFERENCES change_requests(id), -- the denied proposal this replaces
    approval_id INTEGER REFERENCES approvals(id), -- set once the approval exists
    state       TEXT NOT NULL DEFAULT 'proposed' CHECK (state IN ('proposed', 'queued', 'working', 'merged', 'rejected', 'failed')),
    stage       TEXT NOT NULL DEFAULT '', -- the worker's latest step, in words
    issue_url   TEXT,
    pr_url      TEXT,
    result      TEXT, -- once finished: what happened
    created_at  TEXT NOT NULL, -- UTC RFC 3339
    updated_at  TEXT NOT NULL,
    claimed_at  TEXT,
    finished_at TEXT
);
CREATE INDEX change_requests_created ON change_requests(created_at);
