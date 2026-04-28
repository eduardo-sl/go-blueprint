package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
)

// DLQWriter writes failed outbox messages to the dead letter topic.
// DLQ records carry the original payload plus failure metadata as headers.
type DLQWriter struct {
	client *kgo.Client
	topic  string
	logger *slog.Logger
}

// NewDLQWriter creates a DLQWriter connected to the given brokers.
func NewDLQWriter(brokers []string, topic string, logger *slog.Logger) (*DLQWriter, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return nil, fmt.Errorf("kafka.NewDLQWriter: %w", err)
	}
	return &DLQWriter{client: client, topic: topic, logger: logger}, nil
}

// Write sends a failed message to the DLQ topic.
// DLQ write failures are logged as critical but never panic.
func (d *DLQWriter) Write(ctx context.Context, msg outbox.OutboxMessage, reason error) error {
	record := &kgo.Record{
		Topic: d.topic,
		Key:   []byte(msg.AggregateID.String()),
		Value: msg.Payload,
		Headers: []kgo.RecordHeader{
			{Key: "event_type", Value: []byte(msg.EventType)},
			{Key: "message_id", Value: []byte(msg.ID.String())},
			{Key: "failure_reason", Value: []byte(reason.Error())},
			{Key: "failed_at", Value: []byte(time.Now().UTC().Format(time.RFC3339))},
		},
	}
	if err := d.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		d.logger.ErrorContext(ctx, "DLQ write failed — message lost",
			"message_id", msg.ID,
			"error", err,
		)
		return fmt.Errorf("kafka.DLQWriter.Write: %w", err)
	}
	return nil
}

// Close closes the underlying Kafka client.
func (d *DLQWriter) Close() { d.client.Close() }
