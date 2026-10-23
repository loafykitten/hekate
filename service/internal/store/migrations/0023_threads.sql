-- Threads (internal/threads): conversations of their own for longer work. Each is a row in
-- conversations too, so its turns and messages live with the main conversation's. titled is 0
-- while the title is the placeholder, until the owner names it or the first message does.
-- preview is her latest reply, shortened, for lists.
CREATE TABLE threads (
	id TEXT PRIMARY KEY REFERENCES conversations(id),
	title TEXT NOT NULL,
	titled INTEGER NOT NULL DEFAULT 0 CHECK (titled IN (0, 1)),
	model TEXT NOT NULL CHECK (model IN ('deep', 'chat')),
	origin TEXT NOT NULL CHECK (origin IN ('owner', 'renamon')),
	archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
	preview TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX threads_updated_at ON threads (archived, updated_at);

-- Lab tasks became threads.
DROP TABLE lab_tasks;
