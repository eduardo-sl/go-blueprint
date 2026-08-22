-- +goose Up
-- locked_at marks a row as claimed by a poller. A row whose lock is older than
-- the reclaim window is treated as abandoned (the poller died mid-flight) and
-- becomes claimable again.
ALTER TABLE outbox_messages ADD COLUMN locked_at TIMESTAMPTZ;

-- The claim query selects on processed_at IS NULL ordered by created_at, then
-- writes locked_at. The partial index is renamed to match what it now serves.
DROP INDEX idx_outbox_unprocessed;
CREATE INDEX idx_outbox_claimable
    ON outbox_messages (created_at)
    WHERE processed_at IS NULL;

-- +goose Down
DROP INDEX idx_outbox_claimable;
CREATE INDEX idx_outbox_unprocessed
    ON outbox_messages (created_at)
    WHERE processed_at IS NULL;
ALTER TABLE outbox_messages DROP COLUMN locked_at;
