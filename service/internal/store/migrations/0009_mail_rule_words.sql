-- Words a mail rule also requires in the subject or the body (a phrase,
-- matched case-insensitively). A rule with words still needs a sender or a
-- category, which the existing CHECK on mail_rules enforces.
ALTER TABLE mail_rules ADD COLUMN match_subject TEXT;
ALTER TABLE mail_rules ADD COLUMN match_body TEXT;
