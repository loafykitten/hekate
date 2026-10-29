-- Files passed in threads (internal/threads; PROTOCOL.md, Threads, Files). The files themselves
-- live in her lab at path; this is what's known of them. direction 'in': the owner sent it, and
-- turn_id stays '' until a message sends it. 'out': she shared it from that turn. size and mtime
-- (Unix seconds) are the file's when it arrived or was shared: a download of a file changed since
-- is refused.
CREATE TABLE thread_files (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL REFERENCES threads(id),
	turn_id TEXT NOT NULL DEFAULT '',
	direction TEXT NOT NULL CHECK (direction IN ('in', 'out')),
	name TEXT NOT NULL,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	path TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX thread_files_turn ON thread_files (thread_id, turn_id);
