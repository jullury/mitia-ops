PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS services (
    id      TEXT PRIMARY KEY,
    kind    TEXT NOT NULL,
    name    TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS config_items (
    id         INTEGER PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    UNIQUE(service_id, key)
);

-- Rows here pair each legacy integer service id with the UUID it was migrated
-- to. They are the crash-safe resume point for the one-time migration that
-- renames the on-disk deploy directories and named Docker volumes; once those
-- are done the rows are deleted (see CompleteMigrations).
CREATE TABLE IF NOT EXISTS migrated_ids (
    old_id TEXT PRIMARY KEY,
    new_id TEXT NOT NULL
);
