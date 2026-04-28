package eventlog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ID          uuid.UUID
	AggregateID uuid.UUID
	EventType   string
	Payload     json.RawMessage
	OccurredAt  time.Time
}

type Store interface {
	Append(ctx context.Context, e Event) error
	// FetchSince returns events that occurred after since, optionally filtered by
	// aggregateID (empty string = all aggregates).
	FetchSince(ctx context.Context, aggregateID string, since time.Time) ([]Event, error)
}
