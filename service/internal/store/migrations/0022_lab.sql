-- Security checks of her lab VM (internal/lab): the verdict, what was found (JSON
-- [{level, key, text}]), what the homelab saw of the VM (hekate-labctl stats, JSON), whether the
-- VM's own audit ran, and whether this check cut the VM off from the network.
CREATE TABLE lab_checks (
	id INTEGER PRIMARY KEY,
	created_at TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('clean', 'vulnerable', 'urgent', 'suspicious', 'compromised')),
	findings TEXT NOT NULL,
	host TEXT NOT NULL,
	scanned INTEGER NOT NULL CHECK (scanned IN (0, 1)),
	isolated INTEGER NOT NULL CHECK (isolated IN (0, 1))
);
CREATE INDEX lab_checks_created_at ON lab_checks (created_at);

-- Lab tasks: multi-step work she hands to a background worker. last_step is its latest tool
-- call, for "how's it going"; report is the worker's closing words for the owner.
CREATE TABLE lab_tasks (
	id INTEGER PRIMARY KEY,
	goal TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('running', 'done', 'unfinished', 'failed', 'stopped', 'interrupted')),
	steps INTEGER NOT NULL DEFAULT 0,
	last_step TEXT NOT NULL DEFAULT '',
	report TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	finished_at TEXT
);
