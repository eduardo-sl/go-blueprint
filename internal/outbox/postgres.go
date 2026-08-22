package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresOutboxStore implements OutboxStore using pgx.
type PostgresOutboxStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a PostgresOutboxStore backed by pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresOutboxStore {
	return &PostgresOutboxStore{pool: pool}
}

func (s *PostgresOutboxStore) SaveTx(ctx context.Context, tx pgx.Tx, msg OutboxMessage) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox_messages (id, aggregate_id, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, msg.ID, msg.AggregateID, msg.EventType, msg.Payload, msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("outbox.SaveTx: %w", err)
	}
	return nil
}

// ClaimBatch marks a batch of messages as locked and returns them in one
// statement. The UPDATE's own transaction holds the SKIP LOCKED rows through
// the write, so a concurrent poller cannot select the same ids. Reading via
// pool.Query on a bare SELECT ... FOR UPDATE releases the locks as soon as the
// rows are returned, which is the defect this replaces.
func (s *PostgresOutboxStore) ClaimBatch(ctx context.Context, limit int, reclaimAfter time.Duration) ([]OutboxMessage, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE outbox_messages
		SET locked_at = now()
		WHERE id IN (
			SELECT id FROM outbox_messages
			WHERE processed_at IS NULL
			  AND (locked_at IS NULL OR locked_at < now() - $2::interval)
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, aggregate_id, event_type, payload, created_at, attempts, last_error
	`, limit, reclaimAfter)
	if err != nil {
		return nil, fmt.Errorf("outbox.ClaimBatch: %w", err)
	}
	defer rows.Close()

	var msgs []OutboxMessage
	for rows.Next() {
		var m OutboxMessage
		var lastError *string
		if err := rows.Scan(
			&m.ID,
			&m.AggregateID,
			&m.EventType,
			&m.Payload,
			&m.CreatedAt,
			&m.Attempts,
			&lastError,
		); err != nil {
			return nil, fmt.Errorf("outbox.ClaimBatch: scan: %w", err)
		}
		m.LastError = lastError
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox.ClaimBatch: rows: %w", err)
	}
	return msgs, nil
}

func (s *PostgresOutboxStore) MarkProcessed(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		UPDATE outbox_messages SET processed_at = $1 WHERE id = $2
	`, now, id)
	if err != nil {
		return fmt.Errorf("outbox.MarkProcessed: %w", err)
	}
	return nil
}

func (s *PostgresOutboxStore) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	// Clearing locked_at is what makes the retry happen: without it the message
	// stays claimed for the full reclaim window instead of being picked up on
	// the next tick.
	_, err := s.pool.Exec(ctx, `
		UPDATE outbox_messages
		SET attempts = attempts + 1, last_error = $1, locked_at = NULL
		WHERE id = $2
	`, reason, id)
	if err != nil {
		return fmt.Errorf("outbox.MarkFailed: %w", err)
	}
	return nil
}
