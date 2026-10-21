-- What the owner says a regularly repeating payment is. Detection can only say a
-- payment looks recurring; whether it is a necessary bill, an optional
-- subscription, income they rely on, or not a regular payment at all is theirs to
-- say. A row is keyed by a short id derived from the normalized payee and the
-- direction of the money; the payee's own text (the bank's) is never stored here.
CREATE TABLE money_payees (
    payee_id   TEXT PRIMARY KEY, -- 'p' + 10 hex chars of sha256(direction + normalized payee)
    kind       TEXT NOT NULL CHECK (kind IN ('bill', 'subscription', 'income', 'not_recurring')),
    note       TEXT NOT NULL DEFAULT '', -- the owner's own words for it, one line
    updated_at TEXT NOT NULL
);
