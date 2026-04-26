package outbox

import (
	"context"

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

	// FetchUnprocessed returns up to limit unprocessed messages ordered by created_at.
	FetchUnprocessed(ctx context.Context, limit int) ([]OutboxMessage, error)

	// MarkProcessed records successful delivery of a message.
	MarkProcessed(ctx context.Context, id uuid.UUID) error

	// MarkFailed increments the attempt counter and records the delivery error.
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
}
