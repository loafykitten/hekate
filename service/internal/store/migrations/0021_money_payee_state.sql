-- More of what the owner can say about a payee: that a regular payment or deposit
-- has stopped (optionally from a date), and what amount to expect from it instead
-- of the latest payment, which may be a one-off. Amounts are cents, positive; a
-- single expected amount has min = max.
ALTER TABLE money_payees ADD COLUMN stopped INTEGER NOT NULL DEFAULT 0 CHECK (stopped IN (0, 1));
ALTER TABLE money_payees ADD COLUMN stopped_on TEXT NOT NULL DEFAULT ''; -- YYYY-MM-DD, or '' for "already"
ALTER TABLE money_payees ADD COLUMN expected_min_cents INTEGER CHECK (expected_min_cents IS NULL OR expected_min_cents > 0);
ALTER TABLE money_payees ADD COLUMN expected_max_cents INTEGER CHECK (expected_max_cents IS NULL OR expected_max_cents >= expected_min_cents);
