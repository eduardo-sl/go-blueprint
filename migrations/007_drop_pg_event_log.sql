-- +goose Up
-- The Postgres event_log has never been read or written: eventlog.NewSQLiteStore
-- creates and owns its own schema, and nothing in the repository references this
-- table. A table created on every boot is a running artifact, not documentation
-- of an alternative — so it goes. The SQLite event log is unaffected.
DROP INDEX IF EXISTS idx_event_log_aggregate_id;
DROP TABLE IF EXISTS event_log;

-- +goose Down
-- Recreated exactly as 003_create_event_log.sql defined it, so a rollback lands
-- on the schema the previous binary expects.
CREATE TABLE event_log (
    id           TEXT        PRIMARY KEY,
    aggregate_id TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_event_log_aggregate_id ON event_log (aggregate_id);
