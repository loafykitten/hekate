-- Exceptions a mail rule checks before it acts: a JSON array of
-- {"from","category","subject"} objects (each field optional, at least one set).
-- Mail that matches any of them is left alone by that rule.
ALTER TABLE mail_rules ADD COLUMN except_json TEXT NOT NULL DEFAULT '[]';
