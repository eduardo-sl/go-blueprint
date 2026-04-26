-- +goose Up
CREATE TABLE outbox_messages (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id UUID        NOT NULL,
    event_type   TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    attempts     INT         NOT NULL DEFAULT 0,
    last_error   TEXT
);

-- Partial index: only rows pending delivery are indexed.
-- FOR UPDATE SKIP LOCKED on FetchUnprocessed benefits from this index.
CREATE INDEX idx_outbox_unprocessed
    ON outbox_messages (created_at)
    WHERE processed_at IS NULL;

-- +goose Down
DROP TABLE outbox_messages;
