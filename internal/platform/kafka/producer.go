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
// Retries belong to the franz-go client, configured in NewProducer; a send the
// client gives up on is forwarded to the DLQ.
type Producer struct {
	client *kgo.Client
	topic  string
	dlq    *DLQWriter
	logger *slog.Logger
}

// NewProducer creates a Kafka producer.
// Aggregate ID is used as the partition key to guarantee per-aggregate ordering.
// retries bounds the client's own request retries; broker-level concerns —
// leader election, metadata refresh — are what those retries exist to ride out,
// and the application cannot see them.
func NewProducer(brokers []string, topic string, dlq *DLQWriter, retries int, logger *slog.Logger) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RequestRetries(retries),
		kgo.RetryBackoffFn(func(n int) time.Duration {
			return time.Duration(math.Pow(2, float64(n))) * 100 * time.Millisecond
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka.NewProducer: %w", err)
	}
	return &Producer{
		client: client,
		topic:  topic,
		dlq:    dlq,
		logger: logger,
	}, nil
}

// Publish implements outbox.Publisher.
// Uses AggregateID as the Kafka key to guarantee ordering per aggregate.
//
// Exactly one ProduceSync call: retries are the client's job (RequestRetries and
// RetryBackoffFn in NewProducer). A second loop here would nest one retry layer
// inside another, making the worst-case latency the product of the two, and
// only one of them would be documented. A send the client abandons goes to the
// DLQ, and the outbox schedules its own retry from there.
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

	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		p.logger.ErrorContext(ctx, "kafka produce failed, sending to DLQ",
			"message_id", msg.ID,
			"error", err,
		)
		return p.dlq.Write(ctx, msg, err)
	}
	return nil
}

// Close closes the underlying Kafka client.
func (p *Producer) Close() { p.client.Close() }
