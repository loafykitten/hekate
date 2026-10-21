-- Read-only bank data through Plaid. Money is stored as integer cents, in the
-- account's currency. Plaid's sign (positive = money out) is flipped once, when a
-- transaction is stored, so amount_cents here is money in (+) or out (-).

-- A connected bank. The access token never leaves the database: not in REST, not in logs.
CREATE TABLE money_items (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    plaid_item_id    TEXT NOT NULL UNIQUE,
    access_token     TEXT NOT NULL,
    institution_id   TEXT NOT NULL DEFAULT '',
    institution_name TEXT NOT NULL DEFAULT '',
    cursor           TEXT NOT NULL DEFAULT '', -- /transactions/sync cursor; '' before the first sync
    status           TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'login_required', 'error')),
    error            TEXT NOT NULL DEFAULT '', -- what went wrong; empty when status is ok
    last_sync_at     TEXT, -- UTC RFC 3339, last successful sync
    created_at       TEXT NOT NULL
);

CREATE TABLE money_accounts (
    account_id     TEXT PRIMARY KEY, -- Plaid's
    item_id        INTEGER NOT NULL REFERENCES money_items(id) ON DELETE CASCADE,
    name           TEXT NOT NULL DEFAULT '',
    official_name  TEXT NOT NULL DEFAULT '',
    mask           TEXT NOT NULL DEFAULT '',
    type           TEXT NOT NULL DEFAULT '',
    subtype        TEXT NOT NULL DEFAULT '',
    current_cents  INTEGER, -- NULL when the bank doesn't say
    available_cents INTEGER,
    limit_cents    INTEGER,
    currency       TEXT NOT NULL DEFAULT 'USD',
    -- 0 once the bank stops listing it (closed): its balances are no longer shown,
    -- but its name stays for its old transactions.
    active         INTEGER NOT NULL DEFAULT 1,
    updated_at     TEXT NOT NULL -- UTC RFC 3339
);
CREATE INDEX money_accounts_item ON money_accounts(item_id);

-- No foreign key to money_accounts: a sync page can name an account before the
-- balances refresh that follows it has stored the account.
CREATE TABLE money_transactions (
    transaction_id  TEXT PRIMARY KEY, -- Plaid's
    item_id         INTEGER NOT NULL REFERENCES money_items(id) ON DELETE CASCADE,
    account_id      TEXT NOT NULL,
    date            TEXT NOT NULL, -- YYYY-MM-DD, when it posted (or will)
    authorized_date TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '', -- the bank's text: untrusted
    merchant        TEXT NOT NULL DEFAULT '', -- likewise
    amount_cents    INTEGER NOT NULL, -- money in (+) or out (-)
    currency        TEXT NOT NULL DEFAULT 'USD',
    pending         INTEGER NOT NULL DEFAULT 0,
    category        TEXT NOT NULL DEFAULT '', -- Plaid's primary personal finance category
    detail          TEXT NOT NULL DEFAULT '', -- and its detailed one
    channel         TEXT NOT NULL DEFAULT '' -- online | in store | other
);
CREATE INDEX money_transactions_date ON money_transactions(date);
CREATE INDEX money_transactions_account ON money_transactions(account_id, date);
CREATE INDEX money_transactions_item ON money_transactions(item_id);
