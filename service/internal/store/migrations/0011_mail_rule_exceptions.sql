-- Exceptions a mail rule spares: a JSON array of {from?, category?, subject?}, each a set of
-- conditions that must all match. NULL when the rule has none. A rule's exceptions never change
-- in place: new exceptions come as a new rule that replaces it and takes its place, the order
-- rules are tried in.
ALTER TABLE mail_rules ADD COLUMN except_json TEXT;
ALTER TABLE mail_rules ADD COLUMN place INTEGER;
UPDATE mail_rules SET place = id;
