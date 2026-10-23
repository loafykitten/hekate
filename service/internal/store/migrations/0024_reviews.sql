-- Reviews of her lab work in threads (internal/review): a second model checks a turn that changed
-- the lab, against the lab itself. findings and verified are the report (JSON arrays); calls is
-- every command the reviewer ran, with the start of its output, for looking back.
-- followup_turn_id is the turn that handed her the findings ('' if none).
CREATE TABLE reviews (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL REFERENCES threads(id),
	turn_id TEXT NOT NULL REFERENCES turns(id),
	round INTEGER NOT NULL CHECK (round IN (1, 2)),
	status TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed', 'stopped')),
	model TEXT NOT NULL,
	verdict TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	findings TEXT NOT NULL DEFAULT '[]',
	verified TEXT NOT NULL DEFAULT '[]',
	calls TEXT NOT NULL DEFAULT '[]',
	error TEXT NOT NULL DEFAULT '',
	followup_turn_id TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	finished_at TEXT
);
CREATE INDEX reviews_thread ON reviews (thread_id, started_at);
CREATE INDEX reviews_turn ON reviews (turn_id);
CREATE INDEX reviews_followup ON reviews (followup_turn_id);
