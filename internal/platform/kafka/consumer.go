package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Consumer runs a consumer group. Each fetched record is dispatched to the Handler.
// Offsets are committed only after a fully successful batch (at-least-once semantics).
// Graceful shutdown: cancel ctx → PollFetches returns → Run exits.
type Consumer struct {
	client  *kgo.Client
	handler Handler
	logger  *slog.Logger
}

// NewConsumer creates a Consumer in the given consumer group, subscribed to topic.
// Manual offset commit and blocked rebalance-on-poll are always enabled.
func NewConsumer(brokers []string, group, topic string, handler Handler, logger *slog.Logger) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka.NewConsumer: %w", err)
	}
	return &Consumer{client: client, handler: handler, logger: logger}, nil
}

// Run polls records and dispatches them to the handler until ctx is cancelled.
// Designed to run in a goroutine: go consumer.Run(ctx).
// If any record in a batch fails, the batch offset is NOT committed so records
// are redelivered on the next poll (at-least-once guarantee).
// AllowRebalance is always called before any return path to avoid blocking the
// group management goroutine when BlockRebalanceOnPoll is active.
func (c *Consumer) Run(ctx context.Context) {
	for {
		fetches := c.client.PollFetches(ctx)

		// Unblock any pending rebalance before processing or returning.
		// Required whenever BlockRebalanceOnPoll is set — PollFetches holds
		// the rebalance lock until AllowRebalance is called.
		c.client.AllowRebalance()

		if fetches.IsClientClosed() || errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, fe := range errs {
				c.logger.ErrorContext(ctx, "kafka fetch error",
					"topic", fe.Topic,
					"partition", fe.Partition,
					"error", fe.Err,
				)
			}
			continue
		}

		var batchFailed bool
		fetches.EachRecord(func(record *kgo.Record) {
			if err := c.handler.Handle(ctx, record); err != nil {
				c.logger.ErrorContext(ctx, "kafka handler failed",
					"topic", record.Topic,
					"partition", record.Partition,
					"offset", record.Offset,
					"error", err,
				)
				batchFailed = true
			}
		})

		if !batchFailed {
			if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
				c.logger.ErrorContext(ctx, "kafka commit failed", "error", err)
			}
		}
	}
}

// Close closes the underlying Kafka client.
func (c *Consumer) Close() { c.client.Close() }
