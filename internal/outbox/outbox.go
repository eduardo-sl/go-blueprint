package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// OutboxMessage is a domain event pending delivery to a downstream consumer.
type OutboxMessage struct {
	ID          uuid.UUID
	AggregateID uuid.UUID
	EventType   string
	Payload     json.RawMessage
	CreatedAt   time.Time
	ProcessedAt *time.Time // nil means not yet delivered
	Attempts    int
	LastError   *string
}

// Publisher delivers an outbox message to a downstream consumer.
// Implementations: LogPublisher (dev/test), KafkaPublisher, WebhookPublisher.
type Publisher interface {
	Publish(ctx context.Context, msg OutboxMessage) error
}
