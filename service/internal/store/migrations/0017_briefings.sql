-- The morning briefing: written at the owner's chosen time, performed when the app opens.
CREATE TABLE briefings (
    day          TEXT PRIMARY KEY, -- local date, YYYY-MM-DD
    text         TEXT NOT NULL,    -- her words, performance tags included
    cards        TEXT NOT NULL DEFAULT '[]', -- JSON array of cards shown with it
    model        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    delivered_at TEXT              -- first played on a device
);
