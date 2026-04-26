-- +goose Up
CREATE TABLE event_log (
    id           TEXT        PRIMARY KEY,
    aggregate_id TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_event_log_aggregate_id ON event_log (aggregate_id);

-- +goose Down
DROP TABLE event_log;
