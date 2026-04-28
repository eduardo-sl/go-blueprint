package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/eduardo-sl/go-blueprint/internal/outbox"
)

// Producer implements outbox.Publisher using Kafka.
// Messages are sent with retries and exponential backoff.
// On max retries exceeded, the message is forwarded to the DLQ.
type Producer struct {
	client  *kgo.Client
	topic   string
	dlq     *DLQWriter
	retries int
	logger  *slog.Logger
}

// NewProducer creates a Kafka producer.
// Aggregate ID is used as the partition key to guarantee per-aggregate ordering.
func NewProducer(brokers []string, topic string, dlq *DLQWriter, retries int, logger *slog.Logger) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RetryBackoffFn(func(n int) time.Duration {
			return time.Duration(math.Pow(2, float64(n))) * 100 * time.Millisecond
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka.NewProducer: %w", err)
	}
	return &Producer{
		client:  client,
		topic:   topic,
		dlq:     dlq,
		retries: retries,
		logger:  logger,
	}, nil
}

// Publish implements outbox.Publisher.
// Uses AggregateID as the Kafka key to guarantee ordering per aggregate.
// Retries up to p.retries times with exponential backoff; sends to DLQ on exhaustion.
func (p *Producer) Publish(ctx context.Context, msg outbox.OutboxMessage) error {
	record := &kgo.Record{
		Topic: p.topic,
		Key:   []byte(msg.AggregateID.String()),
		Value: msg.Payload,
		Headers: []kgo.RecordHeader{
			{Key: "event_type", Value: []byte(msg.EventType)},
			{Key: "message_id", Value: []byte(msg.ID.String())},
			{Key: "occurred_at", Value: []byte(msg.CreatedAt.Format(time.RFC3339))},
		},
	}

	var lastErr error
	for attempt := 0; attempt <= p.retries; attempt++ {
		if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
			lastErr = err
			p.logger.WarnContext(ctx, "kafka produce failed, retrying",
				"attempt", attempt,
				"message_id", msg.ID,
				"error", err,
			)
			continue
		}
		return nil
	}

	p.logger.ErrorContext(ctx, "kafka produce failed after retries, sending to DLQ",
		"message_id", msg.ID,
		"error", lastErr,
	)
	return p.dlq.Write(ctx, msg, lastErr)
}

// Close closes the underlying Kafka client.
func (p *Producer) Close() { p.client.Close() }
