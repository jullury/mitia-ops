PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS services (
    id      INTEGER PRIMARY KEY,
    kind    TEXT NOT NULL,
    name    TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS config_items (
    id         INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    UNIQUE(service_id, key)
);
