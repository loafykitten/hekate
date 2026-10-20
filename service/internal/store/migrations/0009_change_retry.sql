-- Retrying a failed or rejected change: how many runs it has had, and how the last one ended,
-- so the next run can pick up from there.
ALTER TABLE change_requests ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE change_requests ADD COLUMN last_result TEXT;
ALTER TABLE change_requests ADD COLUMN last_pr_url TEXT;
UPDATE change_requests SET attempts = 1 WHERE claimed_at IS NOT NULL;
