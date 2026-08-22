package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OutboxStore persists and queries outbox messages.
type OutboxStore interface {
	// SaveTx writes a message within the provided transaction.
	// The caller owns the transaction lifecycle (Begin/Commit/Rollback).
	// This is the critical method: calling it outside a transaction defeats
	// the entire purpose of the outbox pattern.
	SaveTx(ctx context.Context, tx pgx.Tx, msg OutboxMessage) error

	// ClaimBatch atomically marks up to limit unprocessed messages as locked
	// and returns them, ordered by created_at. A message claimed by another
	// poller is skipped; a message locked for longer than reclaimAfter is
	// treated as abandoned and claimed again.
	ClaimBatch(ctx context.Context, limit int, reclaimAfter time.Duration) ([]OutboxMessage, error)

	// MarkProcessed records successful delivery of a message.
	MarkProcessed(ctx context.Context, id uuid.UUID) error

	// MarkFailed increments the attempt counter, records the delivery error,
	// and releases the claim so the next tick retries the message.
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
}
