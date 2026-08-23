package customer

import (
	"context"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
)

// EventHandler consumes customer domain events from Kafka. It dispatches on
// the event_type header and nothing more: deduplication belongs to
// kafka.WithIdempotency, which composes and is wired ahead of this handler in
// cmd/api/main.go.
//
// Handlers must still be idempotent in the sense the Handler contract requires
// — the middleware's store is in-memory and bounded, so redelivery past the
// bound reaches this handler again.
type EventHandler struct {
	logger *slog.Logger
}

// NewEventHandler creates an EventHandler.
func NewEventHandler(logger *slog.Logger) *EventHandler {
	return &EventHandler{logger: logger}
}

// Handle implements kafka.Handler. MUST be idempotent.
func (h *EventHandler) Handle(ctx context.Context, record *kgo.Record) error {
	messageID := kafkaHeaderValue(record, "message_id")
	if messageID == "" {
		h.logger.WarnContext(ctx, "kafka record missing message_id header, skipping")
		return nil
	}

	eventType := kafkaHeaderValue(record, "event_type")
	h.logger.InfoContext(ctx, "kafka customer event received",
		"event_type", eventType,
		"message_id", messageID,
		"aggregate_id", string(record.Key),
		"offset", record.Offset,
	)

	switch eventType {
	case "CustomerRegistered":
		return h.handleRegistered(ctx, record.Value)
	case "CustomerUpdated":
		return h.handleUpdated(ctx, record.Value)
	case "CustomerRemoved":
		return h.handleRemoved(ctx, record.Value)
	default:
		h.logger.WarnContext(ctx, "kafka unknown event type, skipping", "event_type", eventType)
		return nil // unknown events are skipped, not failed — they must not block the queue
	}
}

func (h *EventHandler) handleRegistered(ctx context.Context, payload []byte) error {
	h.logger.InfoContext(ctx, "customer registered event processed", "payload_bytes", len(payload))
	return nil
}

func (h *EventHandler) handleUpdated(ctx context.Context, payload []byte) error {
	h.logger.InfoContext(ctx, "customer updated event processed", "payload_bytes", len(payload))
	return nil
}

func (h *EventHandler) handleRemoved(ctx context.Context, payload []byte) error {
	h.logger.InfoContext(ctx, "customer removed event processed", "payload_bytes", len(payload))
	return nil
}

// kafkaHeaderValue extracts a Kafka record header value by key.
func kafkaHeaderValue(record *kgo.Record, key string) string {
	for _, h := range record.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
