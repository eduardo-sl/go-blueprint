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
}
