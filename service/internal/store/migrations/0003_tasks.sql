-- Tasks and lists. A list is just the distinct list names; NOCASE makes
-- "groceries" and "Groceries" the same list.
CREATE TABLE tasks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    title        TEXT NOT NULL,
    notes        TEXT NOT NULL DEFAULT '',
    list         TEXT NOT NULL DEFAULT 'Inbox' COLLATE NOCASE,
    due          TEXT,    -- the API form: YYYY-MM-DD or RFC 3339 in the configured zone
    due_at       INTEGER, -- the same moment as unix seconds, for ordering and range filters
    rrule        TEXT,    -- RFC 5545 RRULE for repeating tasks
    priority     INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 3),
    completed_at TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    deleted_at   TEXT     -- soft delete
);
CREATE INDEX tasks_list ON tasks(list) WHERE deleted_at IS NULL;
CREATE INDEX tasks_due ON tasks(due_at) WHERE deleted_at IS NULL AND completed_at IS NULL;

-- One row each time a repeating task is completed; the task itself stays open
-- with its due moved on.
CREATE TABLE task_completions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      INTEGER NOT NULL REFERENCES tasks(id),
    due          TEXT, -- the occurrence that was completed
    completed_at TEXT NOT NULL
);
CREATE INDEX task_completions_task ON task_completions(task_id, completed_at);
