package outbox

import (
	"context"
	"log/slog"
)

// LogPublisher logs each outbox message instead of sending it to a real consumer.
// Use this in development and tests; replace with KafkaPublisher in production.
type LogPublisher struct {
	logger *slog.Logger
}

// NewLogPublisher creates a LogPublisher that writes to logger.
func NewLogPublisher(logger *slog.Logger) *LogPublisher {
	return &LogPublisher{logger: logger}
}

func (p *LogPublisher) Publish(ctx context.Context, msg OutboxMessage) error {
	p.logger.InfoContext(ctx, "outbox message published",
		slog.String("event_type", msg.EventType),
		slog.String("aggregate_id", msg.AggregateID.String()),
		slog.String("payload", string(msg.Payload)),
	)
	return nil
}
