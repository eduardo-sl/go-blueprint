-- +goose Up
-- next_attempt_at carries the retry schedule in the row rather than in the
-- poller: a restarted process resumes the same backoff, and psql shows exactly
-- why a message is not moving. DEFAULT now() makes every existing unprocessed
-- row immediately claimable, so behaviour on first boot is unchanged.
ALTER TABLE outbox_messages ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE outbox_messages DROP COLUMN next_attempt_at;
