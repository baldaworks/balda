-- +goose Up
CREATE TABLE balda_managed_aliases (
    name TEXT PRIMARY KEY,
    locator_ref TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS balda_managed_aliases;
